package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/store"
)

// ErrProtocol is wrapped by Coordinator errors for calls that break the
// runner protocol, such as a gap in log line numbers or a log beyond the
// per-run limits. Sending the same call again fails the same way.
var ErrProtocol = errors.New("runner protocol violation")

// errAppendConflict means another call for the same run changed it
// between read and write; the call is retried in a new transaction.
var errAppendConflict = errors.New("concurrent update of the run")

// Coordinator limits.
const (
	// maxClaim caps how many runs one Claim hands out.
	maxClaim = 64
	// maxEventText caps stage messages and result summaries; longer text
	// is cut at a character boundary.
	maxEventText = 4 << 10
	// maxResultData caps the JSON data of a result.
	maxResultData = 64 << 10
	maxStageID    = 128
	maxRunnerName = 255
	// maxRunnerVersion caps the release string a runner reports.
	maxRunnerVersion = 64
	// coordinatorAttempts bounds retries after errAppendConflict.
	coordinatorAttempts = 3
)

// runnerIDPattern is the executor's ID rule (runproto.Registration).
var runnerIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// claimedStatuses are the statuses of a run assigned to a runner.
var claimedStatuses = []string{store.RunPreparing, store.RunRunning, store.RunCancelling}

// runErrorCodes are the ErrorCode values a runner may record on a run.
// validation_failed is what a runner reports for a run it refuses before
// starting a container, such as a network policy the runner does not
// allow or a runtime it does not have.
var runErrorCodes = []string{
	"dependency_install_failed", codeScriptError, codeTimeout, "out_of_memory",
	codeRunInterrupted, codeRunnerUnavailable, codeInternalError, "credential_missing", "credential_unavailable",
	codeValidationFailed,
}

// finishFrom lists, for each terminal status a runner reports, the
// statuses a run may finish from (tech design 5.4). A cancelling run may
// end any way: the process can exit or time out before the container is
// stopped.
var finishFrom = map[string][]string{
	runproto.StatusCompleted:   {store.RunRunning, store.RunCancelling},
	runproto.StatusFailed:      {store.RunPreparing, store.RunRunning, store.RunCancelling},
	runproto.StatusTimedOut:    {store.RunRunning, store.RunCancelling},
	runproto.StatusCancelled:   {store.RunPreparing, store.RunRunning, store.RunCancelling},
	runproto.StatusInterrupted: {store.RunPreparing, store.RunRunning, store.RunCancelling},
}

// Coordinator is the server side of the runner protocol.
type Coordinator struct {
	db    *store.DB
	blobs blob.Store
	runs  *Runs
	log   *slog.Logger
	// projectCap caps the claimed runs per project; 0 means no cap.
	projectCap int
	// creds hands out credential values (credentials_runner.go).
	creds *Credentials
}

var _ runproto.Coordinator = (*Coordinator)(nil)

// WithProjectConcurrency caps how many runs of one project are claimed
// (preparing, running or cancelling) at once; the rest wait in the queue
// (tech design 8.4). 0 means no cap. It returns c for chaining.
func (c *Coordinator) WithProjectConcurrency(n int) *Coordinator {
	c.projectCap = max(n, 0)
	return c
}

// NewCoordinator returns the coordinator. runs, when not nil, is woken
// whenever a run ends, so that getRun requests waiting on it return at
// once; pass the same *Runs the API uses.
func NewCoordinator(db *store.DB, blobs blob.Store, runs *Runs, log *slog.Logger) *Coordinator {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Coordinator{db: db, blobs: blobs, runs: runs, log: log}
}

func (c *Coordinator) ended(ctx context.Context, id uuid.UUID) {
	if c.runs != nil {
		c.runs.ended(ctx, id)
		c.runs.changed(ctx, id)
	}
}

// progressed tells local streams that runs changed without ending.
func (c *Coordinator) progressed(ctx context.Context, ids ...uuid.UUID) {
	if c.runs != nil {
		c.runs.changed(ctx, ids...)
	}
}

// Register implements runproto.Coordinator. It records the runner's name,
// slots and labels; the runner's credential, if any, is left as it is.
func (c *Coordinator) Register(ctx context.Context, r runproto.Registration) error {
	return c.register(ctx, r, ProtocolVersion)
}

// register validates r and creates or updates the runner, leaving its
// credential as it is. Only the embedded runner and runners that already
// authenticated get here; credentials are issued by Runners.Exchange,
// which refuses IDs that are in use.
func (c *Coordinator) register(ctx context.Context, r runproto.Registration, protocol int) error {
	labels, err := checkRegistration(r)
	if err != nil {
		return err
	}
	runner := runnerRow(r, protocol, "")
	return c.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.NewInsert().Model(&runner).
			On("CONFLICT (id) DO UPDATE").
			Set("name = EXCLUDED.name").
			Set("slots = EXCLUDED.slots").
			Set("protocol_version = EXCLUDED.protocol_version").
			Set("version = EXCLUDED.version").
			Set("last_seen_at = EXCLUDED.last_seen_at").
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("register runner %s: %w", r.ID, err)
		}
		return replaceRunnerLabels(ctx, tx, r.ID, labels)
	})
}

// runnerRow is the stored form of a registration seen now.
func runnerRow(r runproto.Registration, protocol int, tokenHash string) store.Runner {
	name := r.Name
	if name == "" {
		name = r.ID
	}
	now := store.Now()
	return store.Runner{ID: r.ID, Name: truncateUTF8(name, maxRunnerName), Slots: r.Slots, LastSeenAt: now, CreatedAt: now,
		TokenHash: tokenHash, ProtocolVersion: protocol, Version: truncateUTF8(r.Version, maxRunnerVersion)}
}

// replaceRunnerLabels stores labels as the runner's whole label set.
func replaceRunnerLabels(ctx context.Context, tx bun.Tx, id string, labels []string) error {
	if _, err := tx.NewDelete().Model((*store.RunnerLabel)(nil)).Where("runner_id = ?", id).Exec(ctx); err != nil {
		return fmt.Errorf("replace labels of runner %s: %w", id, err)
	}
	if len(labels) == 0 {
		return nil
	}
	rows := make([]store.RunnerLabel, len(labels))
	for i, l := range labels {
		rows[i] = store.RunnerLabel{ID: store.NewID(), RunnerID: id, Label: l}
	}
	if _, err := tx.NewInsert().Model(&rows).Exec(ctx); err != nil {
		return fmt.Errorf("store labels of runner %s: %w", id, err)
	}
	return nil
}

// checkRegistration validates a registration and returns its labels
// sorted and without duplicates.
func checkRegistration(r runproto.Registration) ([]string, error) {
	if !runnerIDPattern.MatchString(r.ID) {
		return nil, fmt.Errorf("%w: runner ID %q must be 1 to 128 letters, digits, '_', '.' or '-'", ErrProtocol, r.ID)
	}
	if r.Slots < 1 {
		return nil, fmt.Errorf("%w: runner %s has %d slots", ErrProtocol, r.ID, r.Slots)
	}
	if len(r.Labels) > runproto.MaxLabels {
		return nil, fmt.Errorf("%w: runner %s has %d labels, at most %d are allowed", ErrProtocol, r.ID, len(r.Labels), runproto.MaxLabels)
	}
	for _, l := range r.Labels {
		if !runproto.ValidLabel(l) {
			return nil, fmt.Errorf("%w: runner %s has the invalid label %q", ErrProtocol, r.ID, l)
		}
	}
	labels := slices.Clone(r.Labels)
	slices.Sort(labels)
	return slices.Compact(labels), nil
}

// Claim implements runproto.Coordinator. A run whose stored manifest
// cannot be read is failed with internal_error instead of being handed
// out, so it cannot block the queue.
func (c *Coordinator) Claim(ctx context.Context, runnerID string, maxRuns int) ([]runproto.Assignment, error) {
	if maxRuns < 1 {
		return nil, nil
	}
	maxRuns = min(maxRuns, maxClaim)
	var (
		assignments []runproto.Assignment
		failed      []uuid.UUID
		scripts     map[uuid.UUID][]scriptFiles
	)
	err := c.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		assignments, failed, scripts = nil, nil, map[uuid.UUID][]scriptFiles{}
		res, err := tx.NewUpdate().Model((*store.Runner)(nil)).
			Set("last_seen_at = ?", store.Now()).Where("id = ?", runnerID).Exec(ctx)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("%w: runner %q is not registered", ErrProtocol, runnerID)
		}
		var runner store.Runner
		if err := tx.NewSelect().Model(&runner).Column("protocol_version").Where("id = ?", runnerID).Scan(ctx); err != nil {
			return err
		}
		if runner.ProtocolVersion < runproto.StateProtocolVersion {
			return fmt.Errorf("%w: upgrade the runner before claiming tasks with state", ErrProtocol)
		}

		ids, err := c.db.ClaimRunsCapped(ctx, tx, runnerID, maxRuns, runproto.LeaseTTL, c.projectCap)
		if err != nil || len(ids) == 0 {
			return err
		}
		// The runner's name is copied so the run still names it after the
		// runner is gone.
		now := store.Now()
		if _, err := tx.NewUpdate().Model((*store.Run)(nil)).
			Set("claimed_at = ?", now).
			Set("runner_name = (SELECT name FROM runners WHERE runners.id = ?)", runnerID).
			Where("id IN (?)", bun.List(ids)).Exec(ctx); err != nil {
			return err
		}
		for _, id := range ids {
			if err := recordStatus(ctx, tx, id, store.RunQueued, store.RunPreparing, now); err != nil {
				return err
			}
		}
		var runs []store.Run
		if err := tx.NewSelect().Model(&runs).Where("id IN (?)", bun.List(ids)).Order("queued_at", "id").Scan(ctx); err != nil {
			return err
		}
		versionIDs := make([]uuid.UUID, 0, len(runs))
		for _, r := range runs {
			versionIDs = append(versionIDs, r.VersionID)
		}
		var versions []store.TaskVersion
		if err := tx.NewSelect().Model(&versions).Column("id", "manifest").Where("id IN (?)", bun.List(versionIDs)).Scan(ctx); err != nil {
			return err
		}
		manifests := map[uuid.UUID]string{}
		for _, v := range versions {
			manifests[v.ID] = v.Manifest
		}
		for _, r := range runs {
			spec, err := runSpec(manifests[r.VersionID])
			if err == nil && !json.Valid([]byte(r.Params)) {
				err = errors.New("stored params are not JSON")
			}
			if err == nil {
				var (
					missing error
					files   []scriptFiles
				)
				if spec.Tools, files, missing, err = resolveTools(ctx, tx, r.ProjectID, manifests[r.VersionID]); err != nil {
					return err
				}
				if len(files) > 0 {
					scripts[r.ID] = files
				}
				if missing != nil {
					if err := failRunWith(ctx, tx, r.ID, codeValidationFailed, missing.Error()+"：重新登记工具，或提交不引用它的版本"); err != nil {
						return err
					}
					failed = append(failed, r.ID)
					continue
				}
			}
			if err != nil {
				c.log.ErrorContext(ctx, "fail unreadable run at claim", "run_id", r.ID, "error", err)
				if err := failRun(ctx, tx, r.ID, "运行的任务版本无法读取，请重新提交任务"); err != nil {
					return err
				}
				failed = append(failed, r.ID)
				continue
			}
			assignments = append(assignments, runproto.Assignment{
				RunID:     r.ID,
				TaskID:    r.TaskID,
				VersionID: r.VersionID,
				Manifest:  spec,
				Params:    json.RawMessage(r.Params),
				HasInput:  r.InputKey != "",
			})
		}
		return pinStateInputs(ctx, tx, assignments)
	})
	if err != nil {
		return nil, err
	}
	// Script tool contents are read once the claim is stored, so the
	// transaction does not wait on the object store.
	if len(scripts) > 0 {
		kept := assignments[:0]
		for _, a := range assignments {
			if err := loadToolContents(ctx, c.blobs, a.Manifest.Tools, scripts[a.RunID]); err != nil {
				changed, failErr := c.failClaimedToolRead(ctx, runnerID, a.RunID)
				if failErr != nil {
					return nil, failErr
				}
				if changed {
					c.log.ErrorContext(ctx, "fail run whose tool files cannot be read", "run_id", a.RunID, "error", err)
					failed = append(failed, a.RunID)
				}
				continue
			}
			kept = append(kept, a)
		}
		assignments = kept
	}
	for _, id := range failed {
		c.ended(ctx, id)
	}
	claimed := make([]uuid.UUID, len(assignments))
	for i, a := range assignments {
		claimed[i] = a.RunID
	}
	c.progressed(ctx, claimed...)
	// Object reads, failure transactions and notifications may all wait
	// beyond a lease. Observe the entire batch once, after their last IO.
	live, err := c.db.LiveClaims(ctx, runnerID, claimed)
	if err != nil {
		return nil, err
	}
	eligible := make(map[uuid.UUID]bool, len(live))
	for _, id := range live {
		eligible[id] = true
	}
	kept := assignments[:0]
	for _, a := range assignments {
		if eligible[a.RunID] {
			kept = append(kept, a)
		}
	}
	return kept, nil
}

func runSpec(manifest string) (runproto.Manifest, error) {
	if manifest == "" {
		return runproto.Manifest{}, errors.New("version not found")
	}
	m, err := decodeManifest(manifest)
	if err != nil {
		return runproto.Manifest{}, err
	}
	return m.RunSpec()
}

// resolveTools looks up the tools a version's manifest references. A tool
// that is no longer registered is returned as missing, not as an error.
func resolveTools(ctx context.Context, tx bun.IDB, projectID uuid.UUID, manifest string) (tools []runproto.Tool, pending []scriptFiles, missing, err error) {
	m, err := decodeManifest(manifest)
	if err != nil || len(m.Tools) == 0 {
		return nil, nil, nil, err
	}
	registered, err := projectTools(ctx, tx, projectID, m.Tools)
	if err != nil {
		return nil, nil, nil, err
	}
	return runTools(ctx, tx, m.Tools, registered)
}

// failRun ends a run the server cannot hand out with internal_error.
func failRun(ctx context.Context, tx bun.Tx, id uuid.UUID, message string) error {
	return failRunWith(ctx, tx, id, codeInternalError, message)
}

// failRunWith ends a run the server cannot hand out with the error code.
func failRunWith(ctx context.Context, tx bun.Tx, id uuid.UUID, code, message string) error {
	now := store.Now()
	_, err := tx.NewUpdate().Model((*store.Run)(nil)).
		Set("status = ?", store.RunFailed).
		Set("error_code = ?", code).
		Set("error_message = ?", message).
		Set("finished_at = ?", now).
		Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	if err := recordStatus(ctx, tx, id, store.RunPreparing, store.RunFailed, now); err != nil {
		return err
	}
	return releaseRunStateRefs(ctx, tx, []uuid.UUID{id}, now)
}

// failClaimedToolRead settles only a still-owned claim after object IO.
// Reconciliation can have ended it while the object store was unavailable.
func (c *Coordinator) failClaimedToolRead(ctx context.Context, runnerID string, id uuid.UUID) (bool, error) {
	changed := false
	err := c.retry(ctx, func(ctx context.Context, tx bun.Tx) error {
		changed = false
		run, err := c.lockStateRun(ctx, tx, runnerID, id)
		if err != nil {
			return err
		}
		if run.Status != store.RunPreparing && run.Status != store.RunCancelling {
			return runproto.ErrLeaseLost
		}
		now := store.Now()
		res, err := tx.NewUpdate().Model((*store.Run)(nil)).
			Set("status = ?", store.RunFailed).
			Set("error_code = ?", codeInternalError).
			Set("error_message = ?", "运行引用的脚本工具的文件无法读取，请重新登记工具").
			Set("finished_at = ?", now).
			Where("id = ? AND runner_id = ? AND status = ?", id, runnerID, run.Status).Exec(ctx)
		if err := changedOne(res, err); err != nil {
			return conflictOrLost(ctx, tx, runnerID, id, err)
		}
		if err := recordStatus(ctx, tx, id, run.Status, store.RunFailed, now); err != nil {
			return err
		}
		if err := releaseRunStateRefs(ctx, tx, []uuid.UUID{id}, now); err != nil {
			return err
		}
		if err := c.requireStateLease(ctx, tx, id); err != nil {
			return err
		}
		changed = true
		return nil
	})
	// Only the local, unwrapped lifecycle sentinel is a skip. Wrapped or
	// joined database/context failures must remain errors after rollback.
	if err == runproto.ErrLeaseLost && ctx.Err() == nil { //nolint:errorlint // Exact identity preserves independent wrapped or joined failures.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("fail claimed run after tool read: %w", errors.Join(err, ctx.Err()))
	}
	return changed, nil
}

// Heartbeat implements runproto.Coordinator.
func (c *Coordinator) Heartbeat(ctx context.Context, runnerID string, active []uuid.UUID) (runproto.HeartbeatReply, error) {
	var reply runproto.HeartbeatReply
	ids := uniqueIDs(active)
	err := c.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		reply = runproto.HeartbeatReply{}
		if _, err := tx.NewUpdate().Model((*store.Runner)(nil)).
			Set("last_seen_at = ?", store.Now()).Where("id = ?", runnerID).Exec(ctx); err != nil {
			return err
		}
		renewed, err := c.db.RenewRunLeases(ctx, tx, runnerID, ids, runproto.LeaseTTL)
		if err != nil {
			return err
		}
		status := make(map[uuid.UUID]string, len(renewed))
		for _, r := range renewed {
			status[r.ID] = r.Status
		}
		for _, id := range ids {
			switch s, ok := status[id]; {
			case !ok:
				reply.Lost = append(reply.Lost, id)
			case s == store.RunCancelling:
				reply.Cancel = append(reply.Cancel, id)
			}
		}
		return nil
	})
	if err != nil {
		return runproto.HeartbeatReply{}, fmt.Errorf("heartbeat of runner %s: %w", runnerID, err)
	}
	return reply, nil
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// FilesFor returns the files of a task version to a runner that has an
// active run of that version. Remote runners fetch files through it, so a
// runner credential cannot read the code of tasks it was not given; any
// other version is ErrNotFound.
func (c *Coordinator) FilesFor(ctx context.Context, runnerID string, versionID uuid.UUID) ([]runproto.File, error) {
	n, err := c.db.NewSelect().Model((*store.Run)(nil)).
		Where("version_id = ? AND runner_id = ? AND status IN (?)", versionID, runnerID, bun.List(claimedStatuses)).
		Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("check assignment of version %s: %w", versionID, err)
	}
	if n == 0 {
		return nil, fmt.Errorf("files of version %s for runner %s: %w", versionID, runnerID, ErrNotFound)
	}
	return c.Files(ctx, versionID)
}

// Files implements runproto.Coordinator. Contents are checked against
// their hashes, so a damaged blob store fails the run instead of running
// different code.
func (c *Coordinator) Files(ctx context.Context, versionID uuid.UUID) ([]runproto.File, error) {
	var rows []store.TaskFile
	if err := c.db.NewSelect().Model(&rows).Where("version_id = ?", versionID).Order("path").Scan(ctx); err != nil {
		return nil, fmt.Errorf("files of version %s: %w", versionID, err)
	}
	if len(rows) == 0 {
		// Every version has at least one file.
		return nil, fmt.Errorf("files of version %s: %w", versionID, ErrNotFound)
	}
	files := make([]runproto.File, len(rows))
	for i, row := range rows {
		content, err := blob.ReadAll(ctx, c.blobs, row.BlobHash)
		if err != nil {
			return nil, fmt.Errorf("file %s of version %s: %w", row.Path, versionID, err)
		}
		if blob.Sum(content) != row.BlobHash {
			return nil, fmt.Errorf("file %s of version %s: content does not match its hash %s", row.Path, versionID, row.BlobHash)
		}
		files[i] = runproto.File{Path: row.Path, Content: content, Executable: row.Executable}
	}
	return files, nil
}

// Started implements runproto.Coordinator. A run whose cancellation was
// requested while it was preparing stays cancelling; the runner learns of
// it from its next heartbeat. Calling it again changes nothing but the
// environment ID.
func (c *Coordinator) Started(ctx context.Context, runnerID string, runID uuid.UUID, envID string, at time.Time) error {
	at = protocolTime(at)
	var env any
	if envID != "" {
		env = envID
	}
	err := c.retry(ctx, func(ctx context.Context, tx bun.Tx) error {
		// Read the status first, in the same transaction, to record the
		// change only once when the call is repeated.
		run, err := ownedRun(ctx, tx, runnerID, runID)
		if err != nil {
			return err
		}
		res, err := tx.NewUpdate().Model((*store.Run)(nil)).
			Set("status = CASE WHEN status = ? THEN ? ELSE status END", store.RunPreparing, store.RunRunning).
			Set("started_at = COALESCE(started_at, ?)", at).
			Set("env_id = ?", env).
			Where("id = ? AND runner_id = ? AND status = ?", runID, runnerID, run.Status).
			Exec(ctx)
		if err := changedOne(res, err); err != nil {
			return conflictOrLost(ctx, tx, runnerID, runID, fmt.Errorf("start run %s: %w", runID, err))
		}
		if run.Status == store.RunPreparing {
			return recordStatus(ctx, tx, runID, store.RunPreparing, store.RunRunning, at)
		}
		return nil
	})
	if err == nil {
		c.progressed(ctx, runID)
	}
	return err
}

// AppendLogs implements runproto.Coordinator. Lines must continue from
// the last stored line without a gap; lines already stored are skipped,
// so a batch can be resent. Each batch is stored as one log chunk.
func (c *Coordinator) AppendLogs(ctx context.Context, runnerID string, runID uuid.UUID, lines []runproto.LogLine, dropped runproto.Dropped) error {
	if err := consecutive(len(lines), func(i int) int64 { return lines[i].Seq }); err != nil {
		return fmt.Errorf("%w: log lines of run %s: %w", ErrProtocol, runID, err)
	}
	for _, l := range lines {
		switch {
		case l.Stream != runproto.StreamStdout && l.Stream != runproto.StreamStderr && l.Stream != runproto.StreamSystem:
			return fmt.Errorf("%w: log line %d of run %s has stream %q", ErrProtocol, l.Seq, runID, l.Stream)
		case len(l.Text) > runproto.MaxLineBytes:
			return fmt.Errorf("%w: log line %d of run %s is %d bytes, over the %d byte limit", ErrProtocol, l.Seq, runID, len(l.Text), runproto.MaxLineBytes)
		}
	}
	err := c.retry(ctx, func(ctx context.Context, tx bun.Tx) error {
		run, err := ownedRun(ctx, tx, runnerID, runID)
		if err != nil {
			return err
		}
		fresh := lines
		for len(fresh) > 0 && fresh[0].Seq <= run.LogLines {
			fresh = fresh[1:]
		}
		if len(fresh) > 0 && fresh[0].Seq != run.LogLines+1 {
			return fmt.Errorf("%w: run %s has %d log lines stored, the batch continues at %d", ErrProtocol, runID, run.LogLines, fresh[0].Seq)
		}
		droppedLines := max(run.DroppedLines, dropped.Lines)
		droppedBytes := max(run.DroppedBytes, dropped.Bytes)
		if len(fresh) == 0 && droppedLines == run.DroppedLines && droppedBytes == run.DroppedBytes {
			return nil
		}

		total := run.LogLines + int64(len(fresh))
		if total > runproto.MaxRunLines {
			return fmt.Errorf("%w: run %s would have %d log lines, over the limit of %d", ErrProtocol, runID, total, runproto.MaxRunLines)
		}
		var batchBytes int64
		for _, l := range fresh {
			batchBytes += int64(len(l.Text))
		}
		// Sum the stored bytes only when the limit is within reach: a run
		// with fewer lines than MaxRunBytes/MaxLineBytes cannot exceed it.
		if run.LogLines*runproto.MaxLineBytes+batchBytes > runproto.MaxRunBytes {
			var stored int64
			if err := tx.NewSelect().Model((*store.LogChunk)(nil)).ColumnExpr("COALESCE(SUM(bytes), 0)").
				Where("run_id = ?", runID).Scan(ctx, &stored); err != nil {
				return err
			}
			if stored+batchBytes > runproto.MaxRunBytes {
				return fmt.Errorf("%w: run %s would have %d log bytes, over the limit of %d", ErrProtocol, runID, stored+batchBytes, runproto.MaxRunBytes)
			}
		}

		// Update the run first: on PostgreSQL its row lock then orders
		// concurrent appends to the same run.
		res, err := tx.NewUpdate().Model((*store.Run)(nil)).
			Set("log_lines = ?", total).
			Set("dropped_lines = ?", droppedLines).
			Set("dropped_bytes = ?", droppedBytes).
			Where("id = ? AND runner_id = ? AND status IN (?) AND log_lines = ?", runID, runnerID, bun.List(claimedStatuses), run.LogLines).
			Exec(ctx)
		if err := changedOne(res, err); err != nil {
			return conflictOrLost(ctx, tx, runnerID, runID, err)
		}
		if run.DroppedLines == 0 && droppedLines > 0 {
			if err := recordRunEvent(ctx, tx, runID, store.EventLogTruncated, "日志超过上限，之后的输出被丢弃",
				map[string]int64{"dropped_lines": droppedLines, "dropped_bytes": droppedBytes}, store.Now()); err != nil {
				return err
			}
		}
		if len(fresh) == 0 {
			return nil
		}
		data, err := encodeLogChunk(fresh)
		if err != nil {
			return err
		}
		chunk := store.LogChunk{
			ID:        store.NewID(),
			RunID:     runID,
			SeqFrom:   fresh[0].Seq,
			SeqTo:     fresh[len(fresh)-1].Seq,
			Data:      data,
			Bytes:     batchBytes,
			CreatedAt: store.Now(),
		}
		_, err = tx.NewInsert().Model(&chunk).Exec(ctx)
		return err
	})
	if err == nil && c.runs != nil {
		c.runs.logWatch.notify(runID)
	}
	return err
}

// encodeLogChunk writes lines as NDJSON, one record per line.
func encodeLogChunk(lines []runproto.LogLine) (string, error) {
	var b strings.Builder
	now := store.Now()
	for _, l := range lines {
		ts := l.TS.UTC()
		if l.TS.IsZero() {
			ts = now
		}
		rec, err := json.Marshal(store.LogRecord{Seq: l.Seq, TS: ts, Stream: l.Stream, Stage: l.Stage, Text: l.Text})
		if err != nil {
			return "", fmt.Errorf("encode log line %d: %w", l.Seq, err)
		}
		b.Write(rec)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// AppendEvents implements runproto.Coordinator with the same numbering
// rules as AppendLogs. A result event also becomes the run's result.
func (c *Coordinator) AppendEvents(ctx context.Context, runnerID string, runID uuid.UUID, events []runproto.Event) error {
	if err := consecutive(len(events), func(i int) int64 { return events[i].Seq }); err != nil {
		return fmt.Errorf("%w: events of run %s: %w", ErrProtocol, runID, err)
	}
	rows := make([]store.RunEvent, len(events))
	now := store.Now()
	for i, e := range events {
		row, err := eventRow(runID, e, now)
		if err != nil {
			return fmt.Errorf("%w: event %d of run %s: %w", ErrProtocol, e.Seq, runID, err)
		}
		rows[i] = row
	}
	stored := false
	err := c.retry(ctx, func(ctx context.Context, tx bun.Tx) error {
		stored = false
		run, err := ownedRun(ctx, tx, runnerID, runID)
		if err != nil {
			return err
		}
		// Runner numbers are deduplicated against runner_event_seq; the
		// stored events are numbered after the run's last event, which
		// may be one the server recorded (tech design 5.5).
		fresh := rows
		for len(fresh) > 0 && fresh[0].Seq <= run.RunnerEventSeq {
			fresh = fresh[1:]
		}
		if len(fresh) == 0 {
			return nil
		}
		if fresh[0].Seq != run.RunnerEventSeq+1 {
			return fmt.Errorf("%w: run %s has %d events from the runner, the batch continues at %d", ErrProtocol, runID, run.RunnerEventSeq, fresh[0].Seq)
		}
		q := tx.NewUpdate().Model((*store.Run)(nil)).
			Set("runner_event_seq = ?", fresh[len(fresh)-1].Seq).
			Set("event_seq = ?", run.EventSeq+int64(len(fresh))).
			Where("id = ? AND runner_id = ? AND status IN (?) AND event_seq = ?", runID, runnerID, bun.List(claimedStatuses), run.EventSeq)
		for _, e := range slices.Backward(fresh) {
			if e.Kind == store.EventResult {
				q = q.Set("result = ?", e.Payload)
				break
			}
		}
		if err := changedOne(q.Exec(ctx)); err != nil {
			return conflictOrLost(ctx, tx, runnerID, runID, err)
		}
		insert := slices.Clone(fresh)
		for i := range insert {
			insert[i].ID = store.NewID()
			insert[i].Seq = run.EventSeq + int64(i) + 1
		}
		_, err = tx.NewInsert().Model(&insert).Exec(ctx)
		stored = err == nil
		return err
	})
	if err == nil && stored {
		c.progressed(ctx, runID)
	}
	return err
}

// eventRow validates an event and converts it to a row without an ID.
func eventRow(runID uuid.UUID, e runproto.Event, now time.Time) (store.RunEvent, error) {
	row := store.RunEvent{RunID: runID, Seq: e.Seq, TS: protocolTimeOr(e.TS, now), Kind: e.Kind}
	switch e.Kind {
	case runproto.EventStageStart, runproto.EventStageDone, runproto.EventStageFail:
		if e.Stage == "" || len(e.Stage) > maxStageID || !utf8.ValidString(e.Stage) {
			return row, fmt.Errorf("stage event needs a stage ID of 1 to %d bytes, got %q", maxStageID, e.Stage)
		}
		row.Stage = e.Stage
		row.Message = truncateUTF8(e.Message, maxEventText)
	case runproto.EventResult:
		r := e.Result
		if r == nil {
			return row, errors.New("result event without a result")
		}
		switch r.Status {
		case "ok", "partial", "failed":
		default:
			return row, fmt.Errorf("result status %q is not ok, partial or failed", r.Status)
		}
		data := r.Data
		if len(data) > maxResultData {
			return row, fmt.Errorf("result data is %d bytes, over the %d byte limit", len(data), maxResultData)
		}
		if len(data) > 0 && !json.Valid(data) {
			return row, errors.New("result data is not JSON")
		}
		payload, err := json.Marshal(Result{Status: r.Status, Summary: truncateUTF8(r.Summary, maxEventText), Data: data})
		if err != nil {
			return row, err
		}
		row.Payload = string(payload)
	case runproto.EventToolCall:
		c := e.ToolCall
		if c == nil {
			return row, errors.New("tool.call event without a call")
		}
		if c.Tool == "" || len(c.Tool) > maxStageID || c.Method == "" || !utf8.ValidString(c.Method) ||
			!utf8.ValidString(c.ArgsSummary) || !utf8.ValidString(c.Error) || c.DurationMillis < 0 {
			return row, errors.New("tool.call event needs a tool, a method and valid text")
		}
		if len(e.Stage) > maxStageID || !utf8.ValidString(e.Stage) {
			return row, fmt.Errorf("tool.call stage %q is not a stage ID", e.Stage)
		}
		row.Stage = e.Stage
		row.Message = truncateUTF8(c.Tool+" "+c.Method, maxEventText)
		payload, err := json.Marshal(toolCallRecord{
			Tool: c.Tool, Method: truncateUTF8(c.Method, runproto.MaxToolMethodBytes), OK: c.OK,
			DurationMillis: min(c.DurationMillis, runproto.MaxToolCallDuration),
			ArgsSummary:    optionalText(cutRunes(c.ArgsSummary, runproto.MaxToolArgsRunes)),
			Error:          optionalText(truncateUTF8(c.Error, runproto.MaxToolErrorBytes)),
		})
		if err != nil {
			return row, err
		}
		row.Payload = string(payload)
	default:
		return row, fmt.Errorf("unknown event kind %q", e.Kind)
	}
	return row, nil
}

// Finish implements runproto.Coordinator.
func (c *Coordinator) Finish(ctx context.Context, runnerID string, runID uuid.UUID, f runproto.Finish) error {
	from, ok := finishFrom[f.Status]
	if !ok {
		return fmt.Errorf("%w: run %s cannot finish as %q", ErrProtocol, runID, f.Status)
	}
	code, message := c.finishError(ctx, runID, f)
	stage := ""
	if code != "" {
		stage = f.ErrorStage
	}
	finishedAt := protocolTime(f.FinishedAt)
	err := c.retry(ctx, func(ctx context.Context, tx bun.Tx) error {
		run, err := ownedRun(ctx, tx, runnerID, runID)
		if err != nil {
			return err
		}
		if !slices.Contains(from, run.Status) {
			return fmt.Errorf("%w: run %s is %s and cannot finish as %s", ErrProtocol, runID, run.Status, f.Status)
		}
		res, err := tx.NewUpdate().Model((*store.Run)(nil)).
			Set("status = ?", f.Status).
			Set("exit_code = ?", f.ExitCode).
			Set("error_code = ?", nullable(code)).
			Set("error_stage = ?", nullable(stage)).
			Set("error_message = ?", nullable(message)).
			Set("finished_at = ?", finishedAt).
			Where("id = ? AND runner_id = ? AND status = ?", runID, runnerID, run.Status).
			Exec(ctx)
		if err := changedOne(res, err); err != nil {
			return conflictOrLost(ctx, tx, runnerID, runID, err)
		}
		stateful, err := c.finishTaskState(ctx, tx, run, f.Status)
		if err != nil {
			return err
		}
		if err := recordStatus(ctx, tx, runID, run.Status, f.Status, finishedAt); err != nil {
			return err
		}
		if run.IsTest && f.Status == runproto.StatusCompleted && !resultFailed(run.Result) {
			// The first successful test run marks the version tested,
			// which activation may require (tech design 5.2).
			if _, err := tx.NewUpdate().Model((*store.TaskVersion)(nil)).
				Set("tested_at = ?", finishedAt).
				Where("id = ? AND tested_at IS NULL", run.VersionID).Exec(ctx); err != nil {
				return fmt.Errorf("mark version tested: %w", err)
			}
		}
		if stateful {
			return c.requireStateLease(ctx, tx, runID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	c.ended(ctx, runID)
	return nil
}

// resultFailed reports whether a stored result says the business outcome
// failed. A completed process with such a result is not a successful test.
func resultFailed(result string) bool {
	if result == "" {
		return false
	}
	var r Result
	return json.Unmarshal([]byte(result), &r) == nil && r.Status == "failed"
}

// finishError returns the error code and message to record. Completed and
// cancelled runs have none. Failures without a code get the most likely
// one, and codes outside the contract become internal_error, so that the
// API never returns an ErrorCode clients do not know.
func (c *Coordinator) finishError(ctx context.Context, runID uuid.UUID, f runproto.Finish) (code, message string) {
	switch f.Status {
	case runproto.StatusCompleted, runproto.StatusCancelled:
		return "", ""
	}
	code, message = f.ErrorCode, truncateUTF8(f.ErrorMessage, maxEventText)
	if code != "" && !slices.Contains(runErrorCodes, code) {
		c.log.WarnContext(ctx, "runner reported an unknown error code", "run_id", runID, "error_code", code)
		code = codeInternalError
	}
	if code == "" {
		switch f.Status {
		case runproto.StatusTimedOut:
			code = codeTimeout
		case runproto.StatusFailed:
			code = codeScriptError
			if f.ExitCode == nil {
				code = codeInternalError
			}
		case runproto.StatusInterrupted:
			code = codeRunInterrupted
		default:
			code = codeInternalError
		}
	}
	if message == "" {
		switch {
		case code == codeTimeout:
			message = "超过执行时限，进程已被停止"
		case code == codeScriptError && f.ExitCode != nil:
			message = fmt.Sprintf("进程以退出码 %d 结束", *f.ExitCode)
		case f.Status == runproto.StatusInterrupted:
			message = "运行中断"
		default:
			message = "运行失败"
		}
	}
	return code, message
}

// retry runs fn in a write transaction, again when it reports
// errAppendConflict.
func (c *Coordinator) retry(ctx context.Context, fn func(ctx context.Context, tx bun.Tx) error) error {
	var err error
	for range coordinatorAttempts {
		if err = c.db.WriteTx(ctx, fn); !errors.Is(err, errAppendConflict) {
			return err
		}
	}
	return err
}

// ownedRun reads a run for an update by runnerID. A run that is not
// assigned to the runner, or no longer active, is runproto.ErrLeaseLost.
func ownedRun(ctx context.Context, idb bun.IDB, runnerID string, runID uuid.UUID) (store.Run, error) {
	var run store.Run
	err := idb.NewSelect().Model(&run).Where("id = ?", runID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Run{}, runproto.ErrLeaseLost
	}
	if err != nil {
		return store.Run{}, err
	}
	if run.RunnerID != runnerID || !slices.Contains(claimedStatuses, run.Status) {
		return store.Run{}, runproto.ErrLeaseLost
	}
	return run, nil
}

// errNotChanged reports a guarded update that matched no row.
var errNotChanged = errors.New("no row changed")

// changedOne turns the result of a guarded single-row update into
// errNotChanged when it matched nothing.
func changedOne(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errNotChanged
	}
	return nil
}

// conflictOrLost explains a failed guarded update: the run is no longer
// the runner's (ErrLeaseLost), or another call changed it first
// (errAppendConflict, retried).
func conflictOrLost(ctx context.Context, idb bun.IDB, runnerID string, runID uuid.UUID, err error) error {
	if !errors.Is(err, errNotChanged) {
		return err
	}
	if _, err := ownedRun(ctx, idb, runnerID, runID); err != nil {
		return err
	}
	return errAppendConflict
}

// consecutive checks that n sequence numbers, read with seq, start at 1 or
// later and increase by one.
func consecutive(n int, seq func(int) int64) error {
	for i := range n {
		switch {
		case i == 0 && seq(0) < 1:
			return fmt.Errorf("numbers start at 1, got %d", seq(0))
		case i > 0 && seq(i) != seq(i-1)+1:
			return fmt.Errorf("number %d follows %d", seq(i), seq(i-1))
		}
	}
	return nil
}

// protocolTime normalises a time reported by a runner: UTC, at the
// precision both databases store, and now when unset.
func protocolTime(t time.Time) time.Time {
	return protocolTimeOr(t, store.Now())
}

func protocolTimeOr(t, now time.Time) time.Time {
	if t.IsZero() {
		return now
	}
	return t.UTC().Truncate(time.Microsecond)
}

// nullable maps "" to NULL in an update.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// truncateUTF8 cuts s to at most n bytes without splitting a character.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// toolCallRecord is the payload of a tool.call event, the API's
// ToolCallRecord.
type toolCallRecord struct {
	Tool           string  `json:"tool"`
	Method         string  `json:"method"`
	OK             bool    `json:"ok"`
	DurationMillis int64   `json:"duration_ms"`
	ArgsSummary    *string `json:"args_summary"`
	Error          *string `json:"error"`
}

func optionalText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// cutRunes cuts s to at most n runes, ending in "…" when cut.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
