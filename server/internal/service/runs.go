package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/events"
	"github.com/niltavi/lerryn/server/internal/scheduler"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Run rules.
const (
	// MaxRunWait is the longest a getRun request may wait for a run to end.
	MaxRunWait = 60 * time.Second
	// ReconcileInterval is how often the server should call
	// Runs.Reconcile. Leases last runproto.LeaseTTL, so a lost run is
	// noticed within that plus this interval.
	ReconcileInterval = 5 * time.Second

	defaultLogPage = 200
	maxLogPage     = 1000

	// waitPoll bounds how long Wait can miss a run that ended on another
	// server, whose coordinator cannot wake this process.
	waitPoll = time.Second
	// waitPollWithBus replaces waitPoll when an event bus reaches the
	// other servers; polling then only covers lost notifications.
	waitPollWithBus = 5 * time.Second

	triggerManual = "manual"
)

// Stage statuses of RunStage.
const (
	StagePending = "pending"
	StageRunning = "running"
	StageDone    = "done"
	StageFailed  = "failed"
)

// Error codes recorded on runs. They are values of the API's ErrorCode.
const (
	codeInternalError     = "internal_error"
	codeRunnerUnavailable = "runner_unavailable"
	codeRunInterrupted    = "run_interrupted"
	codeScriptError       = "script_error"
	codeTimeout           = "timeout"
)

// Runs starts, reads and cancels runs, and serves their logs.
type Runs struct {
	db    *store.DB
	authz *Authorizer
	log   *slog.Logger
	watch *runWatch
	// bus carries run events to other server processes; nil keeps them
	// within this process.
	bus events.Bus
	// blobs holds archived logs and artifact contents; nil when neither
	// is read.
	blobs blob.Store
	// waitPoll bounds how long Wait can miss a run that ended on another
	// server.
	waitPoll time.Duration
	// logWatch wakes log streams when a run's log grows; feed carries run
	// updates to event streams. Both reach this process only.
	logWatch *runWatch
	feed     *runFeed
	// origin marks the events this process publishes; crossNode is true
	// when the bus reaches other servers, so run changes are published
	// for their event streams.
	origin    string
	crossNode bool
	// planner expands cron schedules for the calendar: scheduler.Planner
	// unless WithFirePlanner replaces it.
	planner FirePlanner
	queue   QueueLimits
	// plaintext rejects params containing credential values; nil skips
	// the check.
	plaintext *Credentials
}

// WithPlaintextCheck rejects run params that contain the value of a
// credential the caller owns or one bound to the task (tech design 11.7).
// It returns r for chaining.
func (r *Runs) WithPlaintextCheck(c *Credentials) *Runs {
	r.plaintext = c
	return r
}

// WithQueueLimits bounds the queue for runs started through the API:
// beyond a limit Start returns ErrQueueFull. It returns r for chaining.
func (r *Runs) WithQueueLimits(l QueueLimits) *Runs {
	r.queue = l
	return r
}

// NewRuns returns the run service. Pass it to NewCoordinator as well, so
// that requests waiting for a run wake up as soon as the run ends.
func NewRuns(db *store.DB, authz *Authorizer, log *slog.Logger) *Runs {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Runs{db: db, authz: authz, log: log, watch: newRunWatch(), waitPoll: waitPoll, logWatch: newRunWatch(), feed: newRunFeed(),
		origin: uuid.NewString(), planner: scheduler.NewPlanner()}
}

// WithBlobs sets the object store artifacts are downloaded from, the one
// the coordinator stores them in; it is also the log archive. It returns
// r for chaining.
func (r *Runs) WithBlobs(blobs blob.Store) *Runs {
	r.blobs = blobs
	return r
}

// WithEvents connects the service to an event bus: run starts and ends are
// published on it, and ends published by other servers wake requests
// waiting here. Call Listen to receive them. With a bus that spans
// servers, Wait polls less often. It returns r for chaining.
func (r *Runs) WithEvents(bus events.Bus, crossNode bool) *Runs {
	r.bus = bus
	r.crossNode = crossNode
	if crossNode {
		r.waitPoll = waitPollWithBus
	}
	return r
}

// WithLogArchive sets the object store that archived logs are written to
// and read from (tech design 6.5). It returns r for chaining.
func (r *Runs) WithLogArchive(blobs blob.Store) *Runs {
	r.blobs = blobs
	return r
}

// Listen wakes requests waiting for runs that ended elsewhere, and feeds
// run changes made on other servers to the event streams here, until ctx
// ends. It is a no-op without a bus.
//
// A run queued by Runs is announced with RunChanged as well; runs queued
// by triggers are announced only with RunQueued, which carries no origin,
// so those reach the streams through it.
func (r *Runs) Listen(ctx context.Context) {
	if r.bus == nil {
		return
	}
	ch, stop := r.bus.Subscribe()
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-ch:
			switch e.Kind {
			case events.RunEnded:
				r.watch.notify(e.RunID)
				r.logWatch.notify(e.RunID)
			case events.RunChanged:
				// Changes made here reached the local streams already.
				if e.Origin != r.origin {
					r.publishUpdate(ctx, e.RunID)
				}
			case events.RunQueued:
				if e.Origin == "" {
					r.publishUpdate(ctx, e.RunID)
				}
			}
		}
	}
}

// ended wakes requests waiting for a run that has just ended, and log
// streams following it, here and, through the bus, on other servers.
func (r *Runs) ended(ctx context.Context, id uuid.UUID) {
	r.watch.notify(id)
	r.logWatch.notify(id)
	r.publish(ctx, events.Event{Kind: events.RunEnded, RunID: id})
}

// publish sends e on the bus. Failures are logged only: receivers poll as
// well.
func (r *Runs) publish(ctx context.Context, e events.Event) {
	if r.bus == nil {
		return
	}
	e.Origin = r.origin
	if err := r.bus.Publish(context.WithoutCancel(ctx), e); err != nil {
		r.log.WarnContext(ctx, "publish run event", "kind", e.Kind, "run_id", e.RunID, "error", err)
	}
}

// RunDetail is a run with its stages and result.
type RunDetail struct {
	Run store.Run
	// Stages lists the declared stages in declaration order, then stages
	// the task reported without declaring them, in order of first report.
	Stages []StageState
	// Result is the last result the task reported, nil if none.
	Result *Result
}

// StageState is the state of one stage of a run.
type StageState struct {
	ID   string
	Name string
	// Declared is whether the run's manifest version declares the stage.
	Declared bool
	// After lists the declared dependencies of the stage, in declaration
	// order; it is empty, never nil, for stages without any, so the
	// console can draw the run's stage graph from the run alone.
	After  []string
	Status string
	// StartedAt and FinishedAt are zero when unknown.
	StartedAt  time.Time
	FinishedAt time.Time
	// Message is the last non-empty message reported with the stage.
	Message string
}

// Result is the business result a task reported with `sbx result`.
type Result struct {
	Status  string          `json:"status"`
	Summary string          `json:"summary"`
	Data    json.RawMessage `json:"data"`
}

// StartRunInput is a request to run a task.
type StartRunInput struct {
	// Version is the version number to run; nil means the latest.
	Version *int
	// Params, when not nil, replaces the manifest's params.
	Params map[string]any
	Test   bool
	// rawParams, when Params is nil, are params copied verbatim from
	// another run, so a rerun keeps large numbers exact.
	rawParams string
}

// Start queues a run of a task version. When the task forbids
// overlapping runs and one is active, it returns a *RunInProgressError.
func (r *Runs) Start(ctx context.Context, p *auth.Principal, taskID uuid.UUID, in StartRunInput) (RunDetail, error) {
	var task store.Task
	err := r.db.NewSelect().Model(&task).Where("id = ?", taskID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return RunDetail{}, ErrNotFound
	}
	if err != nil {
		return RunDetail{}, err
	}
	if err := r.authz.Require(ctx, p, task.ProjectID, ActionControlRun); err != nil {
		return RunDetail{}, err
	}
	// The switch is about the console's button; agents and scripts using
	// access tokens keep starting runs, and reruns are not affected.
	if task.ConsoleHideManualRun && p.Method == auth.MethodSession && task.ArchivedAt.IsZero() {
		return RunDetail{}, ErrManualRunDisabled
	}
	return r.enqueue(ctx, p, task, in, uuid.Nil)
}

// enqueue queues a manual run of a task the caller may control; rerunOf
// is the run it reruns, uuid.Nil for none.
func (r *Runs) enqueue(ctx context.Context, p *auth.Principal, task store.Task, in StartRunInput, rerunOf uuid.UUID) (RunDetail, error) {
	if !task.ArchivedAt.IsZero() {
		return RunDetail{}, ErrTaskArchived
	}
	var v validation
	seq := task.LatestSeq
	if in.Version != nil {
		if *in.Version < 1 {
			v.body("#/version", "版本号从 1 开始")
		}
		seq = *in.Version
	}
	var (
		params []byte
		err    error
	)
	if in.Params != nil {
		params, err = json.Marshal(in.Params)
		if err != nil || len(params) > maxParamsBytes {
			v.content("#/params", "参数序列化为 JSON 后不能超过 16 KiB")
		}
	}
	if err := v.err(); err != nil {
		return RunDetail{}, err
	}
	client, err := clientName(ctx, r.db, p)
	if err != nil {
		return RunDetail{}, err
	}
	version, err := loadVersion(ctx, r.db, task.ID, seq)
	if errors.Is(err, sql.ErrNoRows) {
		v.content("#/version", fmt.Sprintf("任务没有版本 %d，最新版本是 %d", seq, task.LatestSeq))
		return RunDetail{}, v.err()
	}
	if err != nil {
		return RunDetail{}, err
	}
	if err := checkRunCredentials(ctx, r.db, task.ID, version.Manifest.Credentials); err != nil {
		return RunDetail{}, err
	}
	if params == nil && in.rawParams != "" {
		params = []byte(in.rawParams)
	}
	if params == nil {
		params = []byte("{}")
		if version.Manifest.Params != nil {
			if params, err = json.Marshal(version.Manifest.Params); err != nil {
				return RunDetail{}, fmt.Errorf("encode params: %w", err)
			}
		}
	}
	if r.plaintext != nil {
		// Checked whatever the params came from: a rerun copies them from
		// a run that may predate the credential.
		texts, err := JSONScanTexts("#/params", params)
		if err != nil {
			return RunDetail{}, err
		}
		if err := r.plaintext.CheckPlaintext(ctx, p, task.ID, texts); err != nil {
			return RunDetail{}, err
		}
	}

	run := store.Run{
		ID:          store.NewID(),
		ProjectID:   task.ProjectID,
		TaskID:      task.ID,
		VersionID:   version.Version.ID,
		VersionSeq:  version.Version.Seq,
		Status:      store.RunQueued,
		IsTest:      in.Test,
		TriggerKind: triggerManual,
		CreatedBy:   p.UserID,
		Params:      string(params),
		QueuedAt:    store.Now(),
		MaxAttempts: version.Manifest.maxAttempts(in.Test),
		Client:      client,
		RerunOf:     rerunOf,
		Attempt:     1,
	}
	if version.Manifest.concurrency() == ConcurrencyForbid {
		run.ConcurrencyKey = task.ID.String()
	}
	err = r.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if full, err := r.queue.full(ctx, tx, task.ProjectID); err != nil {
			return err
		} else if full {
			return ErrQueueFull
		}
		if _, err := tx.NewInsert().Model(&run).Exec(ctx); err != nil {
			return err
		}
		if err := insertRequiredLabels(ctx, tx, run.ID, version.Manifest.RunnerLabels); err != nil {
			return err
		}
		if err := recordStatus(ctx, tx, run.ID, "", store.RunQueued, run.QueuedAt); err != nil {
			return err
		}
		run.EventSeq = 1
		action, details := "run.start", map[string]any{"task_id": task.ID.String(), "version": run.VersionSeq, "test": run.IsTest}
		if rerunOf != uuid.Nil {
			action, details["rerun_of"] = "run.rerun", rerunOf.String()
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: action, resourceKind: "run", resourceID: run.ID.String(),
			result: resultSuccess, details: details,
		})
	})
	if store.IsUniqueViolation(err) && run.ConcurrencyKey != "" {
		return RunDetail{}, r.runInProgress(ctx, run.ConcurrencyKey)
	}
	if err != nil {
		return RunDetail{}, err
	}
	r.publish(ctx, events.Event{Kind: events.RunQueued, RunID: run.ID})
	r.changed(ctx, run.ID)
	return RunDetail{Run: run, Stages: assembleStages(version.Manifest.Stages, nil)}, nil
}

// runInProgress names the active run holding a concurrency key.
func (r *Runs) runInProgress(ctx context.Context, key string) error {
	var active store.Run
	err := r.db.NewSelect().Model(&active).Column("id").
		Where("concurrency_key = ? AND status IN (?)", key, bun.List(activeStatuses)).
		Limit(1).Scan(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return &RunInProgressError{RunID: active.ID}
}

var activeStatuses = []string{store.RunQueued, store.RunPreparing, store.RunRunning, store.RunCancelling}

// Get returns a run. A run the principal may not see is ErrNotFound.
func (r *Runs) Get(ctx context.Context, p *auth.Principal, id uuid.UUID) (RunDetail, error) {
	run, err := r.load(ctx, p, id, ActionViewTask)
	if err != nil {
		return RunDetail{}, err
	}
	return r.detail(ctx, run)
}

// Wait returns a run once it has ended, or after wait at the latest,
// whichever comes first. Coordinators in this process wake it at once;
// a run that ends on another server is noticed by polling.
func (r *Runs) Wait(ctx context.Context, p *auth.Principal, id uuid.UUID, wait time.Duration) (RunDetail, error) {
	if wait < 0 || wait > MaxRunWait {
		var v validation
		v.param("wait", fmt.Sprintf("取值为 0 到 %d 秒", int(MaxRunWait/time.Second)))
		return RunDetail{}, v.err()
	}
	run, err := r.load(ctx, p, id, ActionViewTask)
	if err != nil {
		return RunDetail{}, err
	}
	deadline := time.Now().Add(wait)
	for store.RunActive(run.Status) && time.Now().Before(deadline) {
		if run, err = r.waitForChange(ctx, id, time.Until(deadline)); err != nil {
			return RunDetail{}, err
		}
	}
	return r.detail(ctx, run)
}

// waitForChange returns the run after it ends, after the next poll, or
// after limit, whichever comes first.
func (r *Runs) waitForChange(ctx context.Context, id uuid.UUID, limit time.Duration) (store.Run, error) {
	// Subscribe before reading, so an end between the read and the select
	// is not missed.
	ended, stop := r.watch.subscribe(id)
	defer stop()
	run, err := r.reload(ctx, id)
	if err != nil || !store.RunActive(run.Status) {
		return run, err
	}
	timer := time.NewTimer(min(limit, r.waitPoll))
	defer timer.Stop()
	select {
	case <-ended:
	case <-timer.C:
	case <-ctx.Done():
		return store.Run{}, ctx.Err()
	}
	return r.reload(ctx, id)
}

// Cancel requests that a run stop. A queued run is cancelled at once; a
// preparing or running run becomes cancelling until its runner confirms
// the container stopped; a run that is already cancelling or has ended is
// left as it is.
func (r *Runs) Cancel(ctx context.Context, p *auth.Principal, id uuid.UUID) (RunDetail, error) {
	if _, err := r.load(ctx, p, id, ActionControlRun); err != nil {
		return RunDetail{}, err
	}
	client, err := clientName(ctx, r.db, p)
	if err != nil {
		return RunDetail{}, err
	}
	var run store.Run
	err = r.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		// Re-read inside the transaction and update only from the status
		// read; on PostgreSQL a runner may claim or start the run in
		// between, which the guarded update notices. Statuses only move
		// forward, so a few rounds always settle.
		for range 5 {
			if err := tx.NewSelect().Model(&run).Where("id = ?", id).Scan(ctx); err != nil {
				return err
			}
			before := run.Status
			now := store.Now()
			q := tx.NewUpdate().Model((*store.Run)(nil)).
				Set("cancel_requested_at = ?", now).
				Set("cancel_requested_by = ?", p.UserID).
				Set("cancel_client = ?", nullable(client)).
				Where("id = ? AND status = ?", id, before)
			switch before {
			case store.RunQueued:
				run.Status, run.FinishedAt = store.RunCancelled, now
				q = q.Set("status = ?", run.Status).Set("finished_at = ?", now)
			case store.RunPreparing, store.RunRunning:
				run.Status = store.RunCancelling
				q = q.Set("status = ?", run.Status)
			default:
				q = nil
			}
			if q != nil {
				res, err := q.Exec(ctx)
				if err != nil {
					return err
				}
				if n, err := res.RowsAffected(); err != nil {
					return err
				} else if n == 0 {
					continue
				}
				run.CancelRequestedAt, run.CancelRequestedBy, run.CancelClient = now, p.UserID, client
				if err := recordStatus(ctx, tx, id, before, run.Status, now); err != nil {
					return err
				}
			}
			kind, actor := actorOf(p)
			return writeAudit(ctx, tx, auditEntry{
				actorKind: kind, actorID: actor,
				action: "run.cancel", resourceKind: "run", resourceID: id.String(),
				result:  resultSuccess,
				details: map[string]any{"task_id": run.TaskID.String(), "status_before": before, "status_after": run.Status},
			})
		}
		return fmt.Errorf("cancel run %s: status kept changing", id)
	})
	if err != nil {
		return RunDetail{}, err
	}
	if run.Status == store.RunCancelled {
		r.ended(ctx, id)
	}
	r.changed(ctx, id)
	return r.detail(ctx, run)
}

// LogsInput selects a page of a run's log. Tail and Cursor exclude each
// other; nil Limit means the default page size.
type LogsInput struct {
	Cursor string
	Limit  *int
	Tail   *int
}

// LogPage is a page of a run's log.
type LogPage struct {
	Lines []store.LogRecord
	// NextCursor continues after the last line of this page, or where the
	// request started when the page is empty.
	NextCursor string
	// Complete is true when the run has ended and the page reaches its
	// last line.
	Complete     bool
	DroppedLines int64
}

// Logs returns a page of a run's log.
func (r *Runs) Logs(ctx context.Context, p *auth.Principal, id uuid.UUID, in LogsInput) (LogPage, error) {
	var v validation
	size := defaultLogPage
	if in.Limit != nil {
		size = *in.Limit
		if size < 1 || size > maxLogPage {
			v.param("limit", fmt.Sprintf("取值为 1 到 %d", maxLogPage))
		}
	}
	if in.Tail != nil {
		if *in.Tail < 1 || *in.Tail > maxLogPage {
			v.param("tail", fmt.Sprintf("取值为 1 到 %d", maxLogPage))
		}
		if in.Cursor != "" {
			v.param("tail", "tail 和 cursor 不能同时使用：tail 读结尾，cursor 从上一页之后续读")
		}
	}
	after, err := decodeLogCursor(in.Cursor)
	if err != nil {
		v.param("cursor", "游标无效，请使用上一页响应中的 next_cursor")
	}
	if err := v.err(); err != nil {
		return LogPage{}, err
	}

	run, err := r.load(ctx, p, id, ActionViewLogs)
	if err != nil {
		return LogPage{}, err
	}
	if !run.LogsExpiredAt.IsZero() {
		return LogPage{}, ErrExpired
	}
	// Everything below works from this snapshot of the run. Lines
	// appended meanwhile wait for the next page, and since a runner
	// appends every line before it finishes a run, a run that has ended
	// here has all its lines.
	total := run.LogLines
	if in.Tail != nil {
		after = max(0, total-int64(*in.Tail))
		if in.Limit == nil {
			size = *in.Tail
		}
	}
	to := min(after+int64(size), total)
	lines, err := r.readLines(ctx, run, after+1, to)
	if err != nil {
		return LogPage{}, err
	}
	last := after
	if len(lines) > 0 {
		last = lines[len(lines)-1].Seq
	}
	return LogPage{
		Lines:        lines,
		NextCursor:   encodeLogCursor(last),
		Complete:     !store.RunActive(run.Status) && last >= total,
		DroppedLines: run.DroppedLines,
	}, nil
}

// readLogLines returns the stored lines numbered from to to, in order.
func readLogLines(ctx context.Context, idb bun.IDB, runID uuid.UUID, from, to int64) ([]store.LogRecord, error) {
	if from > to {
		return nil, nil
	}
	// Start at the chunk that holds line from, so the scan does not touch
	// the chunks before it.
	var first int64
	err := idb.NewSelect().Model((*store.LogChunk)(nil)).Column("seq_from").
		Where("run_id = ? AND seq_from <= ?", runID, from).
		Order("seq_from DESC").Limit(1).Scan(ctx, &first)
	if errors.Is(err, sql.ErrNoRows) {
		first = from
	} else if err != nil {
		return nil, fmt.Errorf("find log chunk: %w", err)
	}
	var chunks []store.LogChunk
	err = idb.NewSelect().Model(&chunks).
		Where("run_id = ? AND seq_from >= ? AND seq_from <= ?", runID, first, to).
		Order("seq_from").Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("read log chunks: %w", err)
	}
	lines := make([]store.LogRecord, 0, to-from+1)
	for _, c := range chunks {
		for raw := range strings.SplitSeq(c.Data, "\n") {
			if raw == "" {
				continue
			}
			var rec store.LogRecord
			if err := json.Unmarshal([]byte(raw), &rec); err != nil {
				return nil, fmt.Errorf("decode log chunk %s: %w", c.ID, err)
			}
			if rec.Seq >= from && rec.Seq <= to {
				lines = append(lines, rec)
			}
		}
	}
	return lines, nil
}

// Log cursors encode the number of the last line read. The prefix leaves
// room for other cursor forms, such as positions in an archive.
const logCursorPrefix = "l"

func encodeLogCursor(after int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(logCursorPrefix + strconv.FormatInt(after, 10)))
}

func decodeLogCursor(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, err
	}
	digits, ok := strings.CutPrefix(string(b), logCursorPrefix)
	if !ok {
		return 0, errors.New("unknown cursor form")
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("invalid cursor position")
	}
	return n, nil
}

// Reconcile marks active runs whose runner lease has expired as
// interrupted and returns them. Call it every ReconcileInterval from one
// place, the leader or the single all-in-one process; concurrent calls
// are safe, each run is interrupted once.
func (r *Runs) Reconcile(ctx context.Context) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		var err error
		ids, err = r.db.ExpireRunLeases(ctx, tx, codeRunInterrupted,
			"runner 失联，运行的租约过期，运行已中断")
		if err != nil {
			return err
		}
		now := store.Now()
		for _, id := range ids {
			if err := recordStatus(ctx, tx, id, "", store.RunInterrupted, now); err != nil {
				return err
			}
		}
		return releaseRunStateRefs(ctx, tx, ids, now)
	})
	if err != nil {
		return nil, fmt.Errorf("reconcile runs: %w", err)
	}
	for _, id := range ids {
		r.ended(ctx, id)
	}
	r.changed(ctx, ids...)
	if len(ids) > 0 {
		r.log.WarnContext(ctx, "interrupted runs whose runner lease expired", "count", len(ids), "runs", ids)
	}
	return ids, nil
}

// load reads a run and checks that p may perform action on it.
func (r *Runs) load(ctx context.Context, p *auth.Principal, id uuid.UUID, action Action) (store.Run, error) {
	run, err := r.reload(ctx, id)
	if err != nil {
		return store.Run{}, err
	}
	if err := r.authz.Require(ctx, p, run.ProjectID, action); err != nil {
		return store.Run{}, err
	}
	return run, nil
}

func (r *Runs) reload(ctx context.Context, id uuid.UUID) (store.Run, error) {
	var run store.Run
	err := r.db.NewSelect().Model(&run).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Run{}, ErrNotFound
	}
	return run, err
}

// detail assembles a run's stages and result.
func (r *Runs) detail(ctx context.Context, run store.Run) (RunDetail, error) {
	var version store.TaskVersion
	if err := r.db.NewSelect().Model(&version).Column("manifest").Where("id = ?", run.VersionID).Scan(ctx); err != nil {
		return RunDetail{}, fmt.Errorf("version of run %s: %w", run.ID, err)
	}
	m, err := decodeManifest(version.Manifest)
	if err != nil {
		return RunDetail{}, err
	}
	var events []store.RunEvent
	err = r.db.NewSelect().Model(&events).
		Where("run_id = ? AND kind IN (?)", run.ID, bun.List([]string{store.EventStageStart, store.EventStageDone, store.EventStageFail})).
		Order("seq").Scan(ctx)
	if err != nil {
		return RunDetail{}, fmt.Errorf("stage events of run %s: %w", run.ID, err)
	}
	d := RunDetail{Run: run, Stages: assembleStages(m.Stages, events)}
	if run.Result != "" {
		var res Result
		if err := json.Unmarshal([]byte(run.Result), &res); err != nil {
			return RunDetail{}, fmt.Errorf("decode result of run %s: %w", run.ID, err)
		}
		d.Result = &res
	}
	return d, nil
}

// assembleStages lists the declared stages first, in declaration order,
// then stages reported without being declared, in order of first report.
// Each stage's state is decided by its events alone, in sequence order.
func assembleStages(declared []StageSpec, events []store.RunEvent) []StageState {
	stages := make([]StageState, 0, len(declared))
	index := map[string]int{}
	for _, s := range declared {
		index[s.ID] = len(stages)
		after := slices.Clone(s.After)
		if after == nil {
			after = []string{}
		}
		stages = append(stages, StageState{ID: s.ID, Name: s.Name, Declared: true, After: after, Status: StagePending})
	}
	for _, e := range events {
		i, ok := index[e.Stage]
		if !ok {
			i = len(stages)
			index[e.Stage] = i
			stages = append(stages, StageState{ID: e.Stage, Name: e.Stage, After: []string{}, Status: StagePending})
		}
		st := &stages[i]
		switch e.Kind {
		case store.EventStageStart:
			st.Status, st.StartedAt, st.FinishedAt = StageRunning, e.TS, time.Time{}
		case store.EventStageDone:
			st.Status, st.FinishedAt = StageDone, e.TS
		case store.EventStageFail:
			st.Status, st.FinishedAt = StageFailed, e.TS
		}
		if e.Message != "" {
			st.Message = e.Message
		}
	}
	return stages
}

// runWatch wakes requests waiting for runs to end. It only reaches waiters
// in this process; Wait polls for the rest.
type runWatch struct {
	mu      sync.Mutex
	waiters map[uuid.UUID]*watchEntry
	// listeners are told about every run that ends here or, through the
	// event bus, on another server.
	listeners []func(uuid.UUID)
}

type watchEntry struct {
	ended chan struct{}
	n     int
}

func newRunWatch() *runWatch {
	return &runWatch{waiters: map[uuid.UUID]*watchEntry{}}
}

// subscribe returns a channel closed when notify is called for id, and a
// function to call when no longer waiting.
func (w *runWatch) subscribe(id uuid.UUID) (<-chan struct{}, func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	e := w.waiters[id]
	if e == nil {
		e = &watchEntry{ended: make(chan struct{})}
		w.waiters[id] = e
	}
	e.n++
	return e.ended, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		e.n--
		if e.n == 0 && w.waiters[id] == e {
			delete(w.waiters, id)
		}
	}
}

// notify wakes everyone waiting for id and tells the listeners.
func (w *runWatch) notify(id uuid.UUID) {
	if w == nil {
		return
	}
	w.mu.Lock()
	if e := w.waiters[id]; e != nil {
		close(e.ended)
		delete(w.waiters, id)
	}
	listeners := w.listeners
	w.mu.Unlock()
	for _, fn := range listeners {
		fn(id)
	}
}

// OnRunEnded registers fn to be called whenever a run ends in this
// process, or on another server when Listen runs with an event bus; for
// instance to wake the notification dispatcher. fn must not block. Events
// can be missed, so consumers that must see every run also poll the
// database.
func (r *Runs) OnRunEnded(fn func(id uuid.UUID)) {
	r.watch.mu.Lock()
	defer r.watch.mu.Unlock()
	r.watch.listeners = append(slices.Clip(r.watch.listeners), fn)
}
