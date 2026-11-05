package store_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/store"
)

type liveClaimsQueries struct{ operations []string }

func (h *liveClaimsQueries) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	h.operations = append(h.operations, event.Operation())
	return ctx
}

func (*liveClaimsQueries) AfterQuery(context.Context, *bun.QueryEvent) {}

func TestLiveClaimsFiltersWithoutChangingRuns(t *testing.T) {
	q := newQueue(t)
	runner, other := q.newRunner(), q.newRunner()
	now := liveClaimsClock(t, q)
	var ids, want []uuid.UUID
	for _, tt := range []struct {
		name   string
		owner  string
		status string
		lease  time.Time
		live   bool
	}{
		{"preparing", runner, store.RunPreparing, now.Add(time.Hour), true},
		{"cancelling", runner, store.RunCancelling, now.Add(time.Hour), true},
		{"running", runner, store.RunRunning, now.Add(time.Hour), false},
		{"queued", runner, store.RunQueued, now.Add(time.Hour), false},
		{"completed", runner, store.RunCompleted, now.Add(time.Hour), false},
		{"failed", runner, store.RunFailed, now.Add(time.Hour), false},
		{"cancelled", runner, store.RunCancelled, now.Add(time.Hour), false},
		{"interrupted", runner, store.RunInterrupted, now.Add(time.Hour), false},
		{"timed out", runner, store.RunTimedOut, now.Add(time.Hour), false},
		{"skipped", runner, store.RunSkipped, now.Add(time.Hour), false},
		{"foreign runner", other, store.RunPreparing, now.Add(time.Hour), false},
		{"no owner", "", store.RunPreparing, now.Add(time.Hour), false},
		{"expired unswept", runner, store.RunPreparing, now.Add(-time.Second), false},
		{"expiry boundary", runner, store.RunCancelling, now, false},
		{"null lease", runner, store.RunPreparing, time.Time{}, false},
	} {
		run := q.mustInsertRun(func(r *store.Run) {
			r.RunnerID, r.Status, r.LeaseExpiresAt = tt.owner, tt.status, tt.lease
		})
		ids = append(ids, run.ID)
		if tt.live {
			want = append(want, run.ID)
		}
		t.Run(tt.name, func(t *testing.T) {
			before := q.get(run.ID)
			got, err := q.db.LiveClaims(t.Context(), runner, []uuid.UUID{run.ID})
			if err != nil || len(got) != 0 && !tt.live || len(got) != 1 && tt.live {
				t.Fatalf("LiveClaims = %v, %v; eligible=%t", got, err, tt.live)
			}
			if tt.live && got[0] != run.ID {
				t.Fatalf("LiveClaims returned %s instead of %s", got[0], run.ID)
			}
			if after := q.get(run.ID); before != after {
				t.Fatalf("filter changed run: before=%+v after=%+v", before, after)
			}
		})
	}
	// Include a duplicate, a nonexistent run and a nil UUID in the same batch.
	ids = append(ids, ids[0], store.NewID(), uuid.Nil)
	hook := &liveClaimsQueries{}
	q.db.DB = q.db.WithQueryHook(hook)
	got, err := q.db.LiveClaims(t.Context(), runner, ids)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(got, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	slices.SortFunc(want, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	if !slices.Equal(got, want) || !slices.Equal(hook.operations, []string{"SELECT"}) {
		t.Fatalf("batch = %v, want %v; statements=%v", got, want, hook.operations)
	}
	hook.operations = nil
	if got, err := q.db.LiveClaims(t.Context(), runner, nil); err != nil || got != nil || len(hook.operations) != 0 {
		t.Fatalf("empty batch = %v, %v; statements=%v", got, err, hook.operations)
	}
}

func TestLiveClaimsErrors(t *testing.T) {
	q := newQueue(t)
	runner := q.newRunner()
	run := q.mustInsertRun(func(r *store.Run) {
		r.RunnerID, r.Status, r.LeaseExpiresAt = runner, store.RunPreparing, store.Now().Add(time.Hour)
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := q.db.LiveClaims(ctx, runner, []uuid.UUID{run.ID}); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("cancelled query = %v, %v", got, err)
	}
	if _, err := q.db.ExecContext(t.Context(), "ALTER TABLE runs RENAME TO live_claims_unavailable"); err != nil {
		t.Fatal(err)
	}
	if got, err := q.db.LiveClaims(t.Context(), runner, []uuid.UUID{run.ID}); err == nil || got != nil {
		t.Fatalf("failed query = %v, %v", got, err)
	}
}

func TestLiveClaimsSQLiteChecksClockAfterPoolWait(t *testing.T) {
	q := newQueue(t)
	if q.isPostgres() {
		t.Skip("SQLite samples the application clock after the pool wait")
	}
	runner := q.newRunner()
	deadline := store.Now().Add(300 * time.Millisecond)
	run := q.mustInsertRun(func(r *store.Run) {
		r.RunnerID, r.Status, r.LeaseExpiresAt = runner, store.RunPreparing, deadline
	})
	pool := q.db.DB.DB
	pool.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	held, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	waits := pool.Stats().WaitCount
	type result struct {
		ids []uuid.UUID
		err error
	}
	done := make(chan result, 1)
	go func() {
		ids, err := q.db.LiveClaims(ctx, runner, []uuid.UUID{run.ID})
		done <- result{ids, err}
	}()
	for pool.Stats().WaitCount == waits {
		select {
		case <-ctx.Done():
			t.Fatal("LiveClaims did not wait for the held connection")
		case <-time.After(time.Millisecond):
		}
	}
	if !store.Now().Before(deadline) {
		t.Fatal("lease expired before LiveClaims entered the pool wait")
	}
	timer := time.NewTimer(time.Until(deadline) + 10*time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || len(got.ids) != 0 {
			t.Fatalf("claim after pool wait = %v, %v", got.ids, got.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestLiveClaimsUsesFreshSnapshot(t *testing.T) {
	q := newQueue(t)
	runner := q.newRunner()
	run := q.mustInsertRun(func(r *store.Run) {
		r.RunnerID, r.Status, r.LeaseExpiresAt = runner, store.RunPreparing, liveClaimsClock(t, q).Add(time.Hour)
	})
	opts := &sql.TxOptions{ReadOnly: true}
	if q.isPostgres() {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := q.db.BeginTx(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var old store.Run
	if err := tx.NewSelect().Model(&old).Where("id = ?", run.ID).Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := q.setStatus(run.ID, store.RunInterrupted); err != nil {
		t.Fatal(err)
	}
	got, err := q.db.LiveClaims(t.Context(), runner, []uuid.UUID{run.ID})
	if err != nil || len(got) != 0 {
		t.Fatalf("fresh snapshot = %v, %v", got, err)
	}
	if err := tx.NewSelect().Model(&old).Where("id = ?", run.ID).Scan(t.Context()); err != nil || old.Status != store.RunPreparing {
		t.Fatalf("old snapshot control = %s, %v", old.Status, err)
	}
}

func TestLiveClaimsDoesNotWaitForRunWriter(t *testing.T) {
	q := newQueue(t)
	runner := q.newRunner()
	run := q.mustInsertRun(func(r *store.Run) {
		r.RunnerID, r.Status, r.LeaseExpiresAt = runner, store.RunPreparing, liveClaimsClock(t, q).Add(time.Hour)
	})
	tx, err := q.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.NewUpdate().Model((*store.Run)(nil)).Set("runner_name = runner_name").Where("id = ?", run.ID).Exec(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	got, err := q.db.LiveClaims(ctx, runner, []uuid.UUID{run.ID})
	if err != nil || !slices.Equal(got, []uuid.UUID{run.ID}) {
		t.Fatalf("read past uncommitted row writer = %v, %v", got, err)
	}
}

func TestLiveClaimsPostgresChecksClockAfterStatementWait(t *testing.T) {
	q := newQueue(t)
	if !q.isPostgres() {
		t.Skip("PostgreSQL must use wall time after a statement lock wait")
	}
	runner := q.newRunner()
	deadline := liveClaimsClock(t, q).Add(750 * time.Millisecond)
	run := q.mustInsertRun(func(r *store.Run) {
		r.RunnerID, r.Status, r.LeaseExpiresAt = runner, store.RunPreparing, deadline
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var tableOID int64
	if err := q.db.NewRaw("SELECT 'runs'::regclass::oid").Scan(ctx, &tableOID); err != nil {
		t.Fatal(err)
	}
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "LOCK TABLE runs IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	type result struct {
		ids []uuid.UUID
		err error
	}
	done := make(chan result, 1)
	go func() {
		ids, err := q.db.LiveClaims(ctx, runner, []uuid.UUID{run.ID})
		done <- result{ids, err}
	}()
	for {
		var waiting bool
		if err := q.db.NewRaw("SELECT EXISTS (SELECT 1 FROM pg_locks WHERE relation = ? AND mode = 'AccessShareLock' AND NOT granted)", tableOID).Scan(ctx, &waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-time.After(time.Millisecond):
		case <-ctx.Done():
			t.Fatal("LiveClaims did not reach the table lock wait")
		}
	}
	if !liveClaimsClock(t, q).Before(deadline) {
		t.Fatal("lease expired before the statement started waiting")
	}
	for liveClaimsClock(t, q).Before(deadline) {
		select {
		case <-time.After(5 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || len(got.ids) != 0 {
			t.Fatalf("claim after statement wait = %v, %v", got.ids, got.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func liveClaimsClock(t *testing.T, q *queue) time.Time {
	t.Helper()
	if !q.isPostgres() {
		return store.Now()
	}
	var now time.Time
	if err := q.db.NewRaw("SELECT clock_timestamp()").Scan(t.Context(), &now); err != nil {
		t.Fatal(err)
	}
	return now.UTC().Truncate(time.Microsecond)
}
