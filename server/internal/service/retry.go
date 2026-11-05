package service

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/store"
)

// RetryPolicy is the manifest's retry section (API schema RetryPolicy,
// tech design 8.5). Retries are off unless MaxAttempts is set.
type RetryPolicy struct {
	// MaxAttempts is how many times a failed run is retried, 0 to 5.
	MaxAttempts int `json:"max_attempts,omitempty"`
	// Delay is the wait before each retry, such as 30s; at most 1h.
	Delay string `json:"delay,omitempty"`
	// On lists the end states that are retried; empty means failed,
	// timed_out and interrupted.
	On []string `json:"on,omitempty"`
}

// Retry rules.
const (
	maxRetries        = 5
	defaultRetryDelay = 30 * time.Second
	maxRetryDelay     = time.Hour
	// retryBatch bounds the attempts one RetryFailed call decides on.
	retryBatch = 100
)

var defaultRetryOn = []string{store.RunFailed, store.RunTimedOut, store.RunInterrupted}

// noRetryCodes are error codes a retry cannot fix (tech design 8.5):
// missing or revoked credentials, and requests that failed validation.
var noRetryCodes = []string{"credential_missing", "credential_unavailable", "validation_failed", "queue_full"}

func validateRetry(v *validation, at string, r *RetryPolicy) {
	if r == nil {
		return
	}
	if r.MaxAttempts < 0 || r.MaxAttempts > maxRetries {
		v.content(at+"/max_attempts", fmt.Sprintf("重试次数为 0 到 %d", maxRetries))
	}
	if r.Delay != "" {
		if d, ok := parseTimeout(r.Delay); !ok {
			v.content(at+"/delay", "等待时间的格式为正整数加单位 s、m 或 h，例如 30s")
		} else if d > maxRetryDelay {
			v.content(at+"/delay", "等待时间最长 1h")
		}
	}
	for i, s := range r.On {
		if !slices.Contains(defaultRetryOn, s) {
			v.content(fmt.Sprintf("%s/on/%d", at, i), "只能是 failed、timed_out 或 interrupted")
		}
	}
}

// maxAttempts is the max_attempts of a new run of a version with m: one
// plus the retries, and 1 for test runs, which are never retried so that
// an agent sees the failure it is testing for.
func (m Manifest) maxAttempts(test bool) int {
	if test || m.Retry == nil || m.Retry.MaxAttempts <= 0 {
		return 1
	}
	return 1 + min(m.Retry.MaxAttempts, maxRetries)
}

func (m Manifest) retryDelay() time.Duration {
	if m.Retry != nil && m.Retry.Delay != "" {
		if d, ok := parseTimeout(m.Retry.Delay); ok {
			return min(d, maxRetryDelay)
		}
	}
	return defaultRetryDelay
}

func (m Manifest) retriesOn(status string) bool {
	on := defaultRetryOn
	if m.Retry != nil && len(m.Retry.On) > 0 {
		on = m.Retry.On
	}
	return slices.Contains(on, status)
}

// RetryFailed decides, for ended attempts that still have attempts left,
// whether they are retried, and queues the retries. A retry waits in the
// queue until the attempt's end plus the manifest's delay. It returns how
// many retries were queued or recorded as skipped. Call it from the
// leader; concurrent calls decide each attempt once.
func (t *Triggers) RetryFailed(ctx context.Context, now time.Time) (int, error) {
	var ended []store.Run
	err := t.db.NewSelect().Model(&ended).
		Where("retry_decided_at IS NULL AND attempt < max_attempts").
		Where("status NOT IN (?)", bun.List(activeStatuses)).
		Order("finished_at", "id").Limit(retryBatch).Scan(ctx)
	if err != nil {
		return 0, fmt.Errorf("find runs to retry: %w", err)
	}
	created := 0
	for _, run := range ended {
		ok, err := t.decideRetry(ctx, run, now)
		if err != nil {
			t.log.ErrorContext(ctx, "decide retry", "run_id", run.ID, "error", err)
			continue
		}
		if ok {
			created++
		}
	}
	return created, nil
}

// decideRetry marks run as decided and, when the policy says so, creates
// its retry. It reports whether a retry run was created.
func (t *Triggers) decideRetry(ctx context.Context, run store.Run, now time.Time) (bool, error) {
	created, queued := false, false
	var retryID uuid.UUID
	err := t.writeRetrying(ctx, func(ctx context.Context, tx bun.Tx) error {
		created, queued = false, false
		// Whoever sets retry_decided_at first decides; on PostgreSQL a
		// second leader waits on the row lock and then matches nothing.
		res, err := tx.NewUpdate().Model((*store.Run)(nil)).
			Set("retry_decided_at = ?", now).
			Where("id = ? AND retry_decided_at IS NULL", run.ID).Exec(ctx)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return err
		}
		version, err := loadVersionByID(ctx, tx, run.VersionID)
		if err != nil {
			return err
		}
		if run.IsTest || !version.Manifest.retriesOn(run.Status) || slices.Contains(noRetryCodes, run.ErrorCode) {
			return nil
		}
		var task store.Task
		if err := tx.NewSelect().Model(&task).Where("id = ?", run.TaskID).Scan(ctx); err != nil {
			return fmt.Errorf("task of run %s: %w", run.ID, err)
		}
		end := run.FinishedAt
		if end.IsZero() {
			end = now
		}
		src := runSource{
			task:        task,
			version:     version,
			kind:        store.TriggerRetry,
			params:      run.Params,
			inputKey:    run.InputKey,
			inputType:   run.InputContentType,
			attempt:     run.Attempt + 1,
			maxAttempts: run.MaxAttempts,
			retryOf:     run.ID,
			notBefore:   end.Add(version.Manifest.retryDelay()),
			createdBy:   run.CreatedBy,
			now:         now,
		}
		if !task.ArchivedAt.IsZero() {
			src.skip = store.SkipInactive
		}
		retry, err := t.admit(ctx, tx, src)
		if err != nil {
			return err
		}
		created, queued = true, retry.Status == store.RunQueued
		retryID = retry.ID
		return nil
	})
	if err == nil && queued {
		t.queued(ctx, []uuid.UUID{retryID})
	}
	if store.IsUniqueViolation(err) {
		// Another leader created the retry first.
		return false, nil
	}
	return created, err
}

// loadVersionByID reads a task version by ID and decodes its manifest.
func loadVersionByID(ctx context.Context, idb bun.IDB, id uuid.UUID) (VersionDetail, error) {
	var version store.TaskVersion
	if err := idb.NewSelect().Model(&version).Where("id = ?", id).Scan(ctx); err != nil {
		return VersionDetail{}, fmt.Errorf("version %s: %w", id, err)
	}
	m, err := decodeManifest(version.Manifest)
	if err != nil {
		return VersionDetail{}, fmt.Errorf("version %s: %w", version.ID, err)
	}
	return VersionDetail{Version: version, Manifest: m}, nil
}
