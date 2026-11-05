package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/events"
	"github.com/niltavi/lerryn/server/internal/store"
)

// QueueLimits bound the run queue (tech design 8.4): runs are not piled
// up without limit. A scheduled or webhook run beyond a limit is recorded
// as skipped with reason queue_full; a run started through the API fails
// with ErrQueueFull. Zero means no limit.
type QueueLimits struct {
	// MaxQueued caps the queued runs of the whole deployment.
	MaxQueued int
	// MaxQueuedPerProject caps the queued runs of one project, so that one
	// project cannot fill the queue for everyone.
	MaxQueuedPerProject int
}

// DefaultQueueLimits are the limits used unless the configuration sets
// others.
var DefaultQueueLimits = QueueLimits{MaxQueued: 1000, MaxQueuedPerProject: 200}

// full reports whether a new queued run of the project would exceed a
// limit. Two admissions racing may both pass, so a limit can be exceeded
// by a few runs; it bounds the queue, it is not a lock.
func (l QueueLimits) full(ctx context.Context, idb bun.IDB, projectID uuid.UUID) (bool, error) {
	if l.MaxQueued > 0 {
		n, err := idb.NewSelect().Model((*store.Run)(nil)).Where("status = ?", store.RunQueued).Count(ctx)
		if err != nil {
			return false, fmt.Errorf("count queued runs: %w", err)
		}
		if n >= int64(l.MaxQueued) {
			return true, nil
		}
	}
	if l.MaxQueuedPerProject > 0 {
		n, err := idb.NewSelect().Model((*store.Run)(nil)).
			Where("project_id = ? AND status = ?", projectID, store.RunQueued).Count(ctx)
		if err != nil {
			return false, fmt.Errorf("count queued runs of project: %w", err)
		}
		if n >= int64(l.MaxQueuedPerProject) {
			return true, nil
		}
	}
	return false, nil
}

// runSource describes a run the scheduler, a webhook or a retry wants to
// queue. admit decides whether it is queued or recorded as skipped.
type runSource struct {
	task    store.Task
	version VersionDetail
	// kind is the run's trigger kind: cron, webhook or retry.
	kind        string
	triggerID   uuid.UUID
	fireID      uuid.UUID
	scheduledAt time.Time
	// params is the JSON object passed to the task.
	params    string
	inputKey  string
	inputType string
	// attempt and maxAttempts default to 1 and to the manifest's retry
	// policy.
	attempt     int
	maxAttempts int
	retryOf     uuid.UUID
	notBefore   time.Time
	createdBy   uuid.UUID
	// skip, when set, records the run as skipped for this reason without
	// looking at overlap or the queue; skipMessage explains it.
	skip        string
	skipMessage string
	now         time.Time
}

// admit writes the run src describes, queued or skipped: skipped with
// reason overlap when the task forbids overlapping runs and one is
// active, with reason queue_full when the queue is at its limit. A
// concurrent admission that takes the task's concurrency key first makes
// the insert fail with a unique violation; the caller retries its
// transaction, which then sees the active run.
func (t *Triggers) admit(ctx context.Context, tx bun.IDB, src runSource) (store.Run, error) {
	m := src.version.Manifest
	run := store.Run{
		ID:               store.NewID(),
		ProjectID:        src.task.ProjectID,
		TaskID:           src.task.ID,
		VersionID:        src.version.Version.ID,
		VersionSeq:       src.version.Version.Seq,
		Status:           store.RunQueued,
		TriggerKind:      src.kind,
		CreatedBy:        src.createdBy,
		Params:           src.params,
		QueuedAt:         src.now,
		TriggerID:        src.triggerID,
		TriggerFireID:    src.fireID,
		ScheduledAt:      src.scheduledAt,
		Attempt:          max(src.attempt, 1),
		MaxAttempts:      src.maxAttempts,
		RetryOf:          src.retryOf,
		NotBefore:        src.notBefore,
		InputKey:         src.inputKey,
		InputContentType: src.inputType,
	}
	if run.MaxAttempts == 0 {
		run.MaxAttempts = m.maxAttempts(false)
	}
	if run.Params == "" {
		run.Params = "{}"
	}
	forbid := m.concurrency() == ConcurrencyForbid
	reason := src.skip
	if reason == "" && forbid {
		n, err := tx.NewSelect().Model((*store.Run)(nil)).
			Where("concurrency_key = ? AND status IN (?)", src.task.ID.String(), bun.List(activeStatuses)).
			Count(ctx)
		if err != nil {
			return store.Run{}, fmt.Errorf("look for an active run: %w", err)
		}
		if n > 0 {
			reason = store.SkipOverlap
		}
	}
	if reason == "" {
		full, err := t.queue.full(ctx, tx, src.task.ProjectID)
		if err != nil {
			return store.Run{}, err
		}
		if full {
			reason = store.SkipQueueFull
		}
	}
	switch {
	case reason != "":
		run.Status, run.SkipReason, run.FinishedAt = store.RunSkipped, reason, src.now
		run.ErrorMessage = src.skipMessage
		run.NotBefore = time.Time{}
		// A skipped run is never retried; deciding now keeps it out of
		// the retry sweep.
		run.RetryDecidedAt = src.now
	case forbid:
		run.ConcurrencyKey = src.task.ID.String()
	}
	if _, err := tx.NewInsert().Model(&run).Exec(ctx); err != nil {
		return store.Run{}, err
	}
	if run.Status == store.RunQueued {
		if err := insertRequiredLabels(ctx, tx, run.ID, m.RunnerLabels); err != nil {
			return store.Run{}, err
		}
	}
	return run, nil
}

// queued wakes claims waiting for work, on this server and, through the
// bus, on the others. Call it after the transaction that queued the runs
// committed. Failures are logged only: claims poll as well.
func (t *Triggers) queued(ctx context.Context, ids []uuid.UUID) {
	if t.bus == nil {
		return
	}
	for _, id := range ids {
		if err := t.bus.Publish(context.WithoutCancel(ctx), events.Event{Kind: events.RunQueued, RunID: id}); err != nil {
			t.log.WarnContext(ctx, "publish run event", "kind", events.RunQueued, "run_id", id, "error", err)
		}
	}
}

// writeAttempts bounds the transactions writeRetrying runs.
const writeAttempts = 3

// writeRetrying runs fn in a write transaction and runs it again after a
// unique violation. Each such violation means a concurrent writer did
// part of the same work first (took the concurrency key, recorded the
// fire, created the retry); fn re-reads and sees that on the next
// attempt.
func (t *Triggers) writeRetrying(ctx context.Context, fn func(ctx context.Context, tx bun.Tx) error) error {
	var err error
	for range writeAttempts {
		err = t.db.WriteTx(ctx, fn)
		if !store.IsUniqueViolation(err) {
			return err
		}
	}
	return err
}
