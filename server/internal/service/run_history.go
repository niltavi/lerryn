package service

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Run history limits.
const (
	maxStatusFilter    = 10
	defaultEventPage   = 200
	maxEventPage       = 500
	logDownloadBatch   = 5000
	logStreamBatch     = 1000
	logStreamPoll      = time.Second
	runCursor          = "r"
	eventCursor        = "e"
	maxRunnerIDFilter  = 128
	logDownloadTimeFmt = "2006-01-02T15:04:05.000Z07:00"
)

// RunListInput selects a page of runs. Zero fields do not filter.
type RunListInput struct {
	Archived             bool
	ExcludeArchivedTasks bool
	ProjectID            *uuid.UUID
	TaskID               *uuid.UUID
	Statuses             []string
	Trigger              string
	Test                 *bool
	RerunOf              *uuid.UUID
	RunnerID             string
	Since                *time.Time
	Until                *time.Time
	Limit                *int
	Cursor               string
}

// RunListItem is a run in a list.
type RunListItem struct {
	Run      store.Run
	TaskName string
	// CurrentStage is the display name of the stage an active run is in,
	// empty if none.
	CurrentStage string
}

// RunPage is a page of runs, newest first.
type RunPage struct {
	Runs       []RunListItem
	NextCursor string
}

var allStatuses = []string{
	store.RunQueued, store.RunPreparing, store.RunRunning, store.RunCancelling, store.RunCompleted,
	store.RunFailed, store.RunTimedOut, store.RunCancelled, store.RunInterrupted, store.RunSkipped,
}

var triggerKinds = []string{store.TriggerManual, store.TriggerCron, store.TriggerWebhook, store.TriggerRetry}

// List returns the runs p can view, most recently queued first.
func (r *Runs) List(ctx context.Context, p *auth.Principal, in RunListInput) (RunPage, error) {
	var v validation
	limit := pageSize(&v, in.Limit)
	after := decodeTimeCursor(&v, in.Cursor, runCursor)
	if len(in.Statuses) > maxStatusFilter {
		v.param("status", fmt.Sprintf("最多 %d 个状态", maxStatusFilter))
	}
	for _, s := range in.Statuses {
		if !slices.Contains(allStatuses, s) {
			v.param("status", fmt.Sprintf("未知的运行状态 %s", s))
		}
	}
	if in.Trigger != "" && !slices.Contains(triggerKinds, in.Trigger) {
		v.param("trigger", fmt.Sprintf("未知的运行来源 %s", in.Trigger))
	}
	if len(in.RunnerID) > maxRunnerIDFilter {
		v.param("runner_id", "runner ID 最长 128 个字符")
	}
	if err := v.err(); err != nil {
		return RunPage{}, err
	}
	projects, err := r.authz.Visible(ctx, p, ActionViewTask, in.ProjectID)
	if err != nil || len(projects) == 0 {
		return RunPage{}, err
	}
	var runs []store.Run
	q := r.db.NewSelect().Model(&runs).Where("project_id IN (?)", bun.List(projects))
	if in.Archived {
		q = q.Where("archived_at IS NOT NULL")
	} else {
		q = q.Where("archived_at IS NULL")
	}
	if in.ExcludeArchivedTasks {
		q = q.Where("EXISTS (SELECT 1 FROM tasks WHERE tasks.id = run.task_id AND tasks.archived_at IS NULL)")
	}
	if in.TaskID != nil {
		q = q.Where("task_id = ?", *in.TaskID)
	}
	if len(in.Statuses) > 0 {
		q = q.Where("status IN (?)", bun.List(in.Statuses))
	}
	if in.Trigger != "" {
		q = q.Where("trigger_kind = ?", in.Trigger)
	}
	if in.Test != nil {
		q = q.Where("is_test = ?", *in.Test)
	}
	if in.RerunOf != nil {
		q = q.Where("rerun_of = ?", *in.RerunOf)
	}
	if in.RunnerID != "" {
		q = q.Where("runner_id = ?", in.RunnerID)
	}
	if in.Since != nil {
		q = q.Where("queued_at >= ?", in.Since.UTC())
	}
	if in.Until != nil {
		q = q.Where("queued_at < ?", in.Until.UTC())
	}
	if after != nil {
		q = q.Where("(queued_at < ? OR (queued_at = ? AND id < ?))", after.at, after.at, after.id)
	}
	if err := q.Order("queued_at DESC", "id DESC").Limit(int64(limit + 1)).Scan(ctx); err != nil {
		return RunPage{}, fmt.Errorf("list runs: %w", err)
	}
	var page RunPage
	if len(runs) > limit {
		runs = runs[:limit]
		last := runs[limit-1]
		page.NextCursor = encodeTimeCursor(runCursor, last.QueuedAt, last.ID)
	}
	page.Runs, err = r.listItems(ctx, runs)
	return page, err
}

// listItems adds the task name and the current stage to each run.
func (r *Runs) listItems(ctx context.Context, runs []store.Run) ([]RunListItem, error) {
	items := make([]RunListItem, len(runs))
	if len(runs) == 0 {
		return items, nil
	}
	taskIDs := map[uuid.UUID]bool{}
	var active []uuid.UUID
	for i, run := range runs {
		items[i].Run = run
		taskIDs[run.TaskID] = true
		if store.RunActive(run.Status) {
			active = append(active, run.ID)
		}
	}
	names, err := taskNames(ctx, r.db, taskIDs)
	if err != nil {
		return nil, err
	}
	stages, err := r.currentStageNames(ctx, runs, active)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].TaskName = names[items[i].Run.TaskID]
		items[i].CurrentStage = stages[items[i].Run.ID]
	}
	return items, nil
}

func taskNames(ctx context.Context, idb bun.IDB, ids map[uuid.UUID]bool) (map[uuid.UUID]string, error) {
	list := make([]uuid.UUID, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	var tasks []store.Task
	if err := idb.NewSelect().Model(&tasks).Column("id", "name").Where("id IN (?)", bun.List(list)).Scan(ctx); err != nil {
		return nil, fmt.Errorf("task names: %w", err)
	}
	out := make(map[uuid.UUID]string, len(tasks))
	for _, t := range tasks {
		out[t.ID] = t.Name
	}
	return out, nil
}

// currentStageNames returns the display name of the current stage of the
// active runs among runs: the name declared in the run's version, or the
// stage ID for an undeclared stage.
func (r *Runs) currentStageNames(ctx context.Context, runs []store.Run, active []uuid.UUID) (map[uuid.UUID]string, error) {
	ids, err := currentStages(ctx, r.db, active)
	if err != nil || len(ids) == 0 {
		return ids, err
	}
	versionOf := map[uuid.UUID]uuid.UUID{}
	var versionIDs []uuid.UUID
	for _, run := range runs {
		if _, ok := ids[run.ID]; ok {
			versionOf[run.ID] = run.VersionID
			versionIDs = append(versionIDs, run.VersionID)
		}
	}
	var versions []store.TaskVersion
	if err := r.db.NewSelect().Model(&versions).Column("id", "manifest").Where("id IN (?)", bun.List(versionIDs)).Scan(ctx); err != nil {
		return nil, fmt.Errorf("stage names: %w", err)
	}
	declared := map[uuid.UUID][]StageSpec{}
	for _, v := range versions {
		if m, err := decodeManifest(v.Manifest); err == nil {
			declared[v.ID] = m.Stages
		}
	}
	out := map[uuid.UUID]string{}
	for runID, stage := range ids {
		out[runID] = stage
		for _, s := range declared[versionOf[runID]] {
			if s.ID == stage {
				out[runID] = s.Name
			}
		}
	}
	return out, nil
}

// RerunInput is a request to rerun a run. Nil fields keep: the latest
// version, the original run's params, the original run's test flag.
type RerunInput struct {
	Version *int
	Params  map[string]any
	Test    *bool
}

// Rerun queues a new run modelled on an ended one, linked to it through
// rerun_of. An active original is a *RunInProgressError.
func (r *Runs) Rerun(ctx context.Context, p *auth.Principal, id uuid.UUID, in RerunInput) (RunDetail, error) {
	orig, err := r.load(ctx, p, id, ActionControlRun)
	if err != nil {
		return RunDetail{}, err
	}
	if store.RunActive(orig.Status) {
		return RunDetail{}, &RunInProgressError{RunID: orig.ID}
	}
	var task store.Task
	if err := r.db.NewSelect().Model(&task).Where("id = ?", orig.TaskID).Scan(ctx); err != nil {
		return RunDetail{}, fmt.Errorf("task of run %s: %w", id, err)
	}
	start := StartRunInput{Version: in.Version, Params: in.Params, Test: orig.IsTest}
	if in.Test != nil {
		start.Test = *in.Test
	}
	if in.Params == nil {
		start.rawParams = orig.Params
	}
	return r.enqueue(ctx, p, task, start, orig.ID)
}

// EventsInput selects a page of a run's events.
type EventsInput struct {
	Cursor string
	Kinds  []string
	Limit  *int
}

// EventPage is a page of a run's events in sequence order.
type EventPage struct {
	Events []store.RunEvent
	// NextCursor continues after this page, or where the request started
	// when the page is empty.
	NextCursor string
	// Complete is true when the run has ended and the page reaches its
	// last event.
	Complete bool
}

var eventKinds = []string{
	store.EventStatus, store.EventStageStart, store.EventStageDone, store.EventStageFail, store.EventResult,
	store.EventToolCall, store.EventArtifact, store.EventWarning, store.EventLogTruncated,
}

// Events returns a page of a run's events.
func (r *Runs) Events(ctx context.Context, p *auth.Principal, id uuid.UUID, in EventsInput) (EventPage, error) {
	var v validation
	size := defaultEventPage
	if in.Limit != nil {
		size = *in.Limit
		if size < 1 || size > maxEventPage {
			v.param("limit", fmt.Sprintf("取值为 1 到 %d", maxEventPage))
		}
	}
	var after int64
	if in.Cursor != "" {
		parts, err := decodePageCursor(in.Cursor, eventCursor, 1)
		if err == nil {
			after, err = strconv.ParseInt(parts[0], 10, 64)
		}
		if err != nil || after < 0 {
			v.param("cursor", "游标无效，请使用上一页响应中的 next_cursor")
		}
	}
	for _, k := range in.Kinds {
		if !slices.Contains(eventKinds, k) {
			v.param("kind", fmt.Sprintf("未知的事件类别 %s", k))
		}
	}
	if err := v.err(); err != nil {
		return EventPage{}, err
	}
	// The run is read first: events committed with a status change are
	// then all visible below, so an ended run's page is final.
	run, err := r.load(ctx, p, id, ActionViewTask)
	if err != nil {
		return EventPage{}, err
	}
	var events []store.RunEvent
	q := r.db.NewSelect().Model(&events).Where("run_id = ? AND seq > ? AND seq <= ?", id, after, run.EventSeq)
	if len(in.Kinds) > 0 {
		q = q.Where("kind IN (?)", bun.List(in.Kinds))
	}
	if err := q.Order("seq").Limit(int64(size)).Scan(ctx); err != nil {
		return EventPage{}, fmt.Errorf("run events: %w", err)
	}
	last := after
	if len(events) > 0 {
		last = events[len(events)-1].Seq
	}
	return EventPage{
		Events:     events,
		NextCursor: encodePageCursor(eventCursor, strconv.FormatInt(last, 10)),
		Complete:   !store.RunActive(run.Status) && len(events) < size,
	}, nil
}

// FollowInput says where a log stream starts: after line LastEventID when
// set, else after the page that ended at Cursor, else with the last Tail
// lines, else at the first line.
type FollowInput struct {
	LastEventID *int64
	Cursor      string
	Tail        *int
}

// LogFollower streams a run's log as it grows.
type LogFollower struct {
	r     *Runs
	p     *auth.Principal
	id    uuid.UUID
	after int64
	// dropped is the dropped line count already reported.
	dropped int64
	cred    *credentialGuard
}

// LogBatch is what a follower has to report: new lines, a newly started
// truncation, and the end of the log.
type LogBatch struct {
	Lines []store.LogRecord
	// DroppedLines is set when the log started dropping lines since the
	// last batch.
	DroppedLines int64
	// End is set once the run has ended and every line was returned.
	End *LogEnd
}

// LogEnd describes the end of a run's log.
type LogEnd struct {
	Status       string
	LastSeq      int64
	DroppedLines int64
}

// FollowLogs starts a log stream of a run.
func (r *Runs) FollowLogs(ctx context.Context, p *auth.Principal, id uuid.UUID, in FollowInput) (*LogFollower, error) {
	var v validation
	if in.Tail != nil && (*in.Tail < 1 || *in.Tail > maxLogPage) {
		v.param("tail", fmt.Sprintf("取值为 1 到 %d", maxLogPage))
	}
	after, err := decodeLogCursor(in.Cursor)
	if err != nil {
		v.param("cursor", "游标无效，请使用 readRunLogs 返回的 next_cursor")
	}
	if in.LastEventID != nil && *in.LastEventID < 0 {
		v.param("Last-Event-ID", "行号不能为负数")
	}
	if err := v.err(); err != nil {
		return nil, err
	}
	run, err := r.load(ctx, p, id, ActionViewLogs)
	if err != nil {
		return nil, err
	}
	if !run.LogsExpiredAt.IsZero() {
		return nil, ErrExpired
	}
	switch {
	case in.LastEventID != nil:
		after = *in.LastEventID
	case in.Cursor != "":
	case in.Tail != nil:
		after = max(0, run.LogLines-int64(*in.Tail))
	}
	// A reader resuming after the truncation started has seen it.
	f := &LogFollower{r: r, p: p, id: id, after: after, cred: newCredentialGuard(r.db, p)}
	if in.LastEventID != nil || in.Cursor != "" {
		f.dropped = run.DroppedLines
	}
	return f, nil
}

// Next returns the next batch, waiting up to wait for one. An empty batch
// means nothing happened in that time.
func (f *LogFollower) Next(ctx context.Context, wait time.Duration) (LogBatch, error) {
	deadline := time.Now().Add(wait)
	for {
		// Subscribe before reading, so an append in between is not missed.
		grew, stop := f.r.logWatch.subscribe(f.id)
		batch, err := f.read(ctx)
		if err != nil || batch.End != nil || len(batch.Lines) > 0 || batch.DroppedLines > 0 {
			stop()
			return batch, err
		}
		left := time.Until(deadline)
		if left <= 0 {
			stop()
			return batch, nil
		}
		// Appends on other servers do not wake this one; poll for them.
		timer := time.NewTimer(min(left, logStreamPoll))
		select {
		case <-grew:
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			stop()
			return LogBatch{}, ctx.Err()
		}
		timer.Stop()
		stop()
	}
}

func (f *LogFollower) read(ctx context.Context) (LogBatch, error) {
	// Access is checked on every read, so removing a member from the
	// project also ends their open streams. Role bindings survive logout,
	// revocation and disable, so the credential is checked too.
	if err := f.cred.check(ctx); err != nil {
		return LogBatch{}, err
	}
	run, err := f.r.load(ctx, f.p, f.id, ActionViewLogs)
	if err != nil {
		return LogBatch{}, err
	}
	var b LogBatch
	if run.DroppedLines > f.dropped {
		if f.dropped == 0 {
			b.DroppedLines = run.DroppedLines
		}
		f.dropped = run.DroppedLines
	}
	to := min(f.after+logStreamBatch, run.LogLines)
	// readLines also covers a log the leader archived while it was
	// followed.
	b.Lines, err = f.r.readLines(ctx, run, f.after+1, to)
	if err != nil {
		return LogBatch{}, err
	}
	if n := len(b.Lines); n > 0 {
		f.after = b.Lines[n-1].Seq
	}
	if !store.RunActive(run.Status) && f.after >= run.LogLines {
		b.End = &LogEnd{Status: run.Status, LastSeq: run.LogLines, DroppedLines: run.DroppedLines}
	}
	return b, nil
}

// LogDownload writes a run's whole log as text.
type LogDownload struct {
	r   *Runs
	run store.Run
	// total is the number of lines when the download started; lines
	// appended later are not included.
	total int64
}

// DownloadLogs checks access and returns the run's log for download. A
// log removed by retention is ErrExpired.
func (r *Runs) DownloadLogs(ctx context.Context, p *auth.Principal, id uuid.UUID) (*LogDownload, error) {
	run, err := r.load(ctx, p, id, ActionViewLogs)
	if err != nil {
		return nil, err
	}
	if !run.LogsExpiredAt.IsZero() {
		return nil, ErrExpired
	}
	return &LogDownload{r: r, run: run, total: run.LogLines}, nil
}

// WriteTo writes the log, one line per log line: time, stream, text. The
// lines come from the chunks or, once the leader archived them, from the
// archive.
func (d *LogDownload) WriteTo(ctx context.Context, w io.Writer) error {
	bw := bufio.NewWriterSize(w, 64<<10)
	for from := int64(1); from <= d.total; from += logDownloadBatch {
		lines, err := d.r.readLines(ctx, d.run, from, min(from+logDownloadBatch-1, d.total))
		if err != nil {
			return err
		}
		for _, l := range lines {
			if _, err := fmt.Fprintf(bw, "%s %s %s\n", l.TS.UTC().Format(logDownloadTimeFmt), l.Stream, l.Text); err != nil {
				return err
			}
		}
	}
	return bw.Flush()
}
