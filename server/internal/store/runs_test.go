package store_test

// Run queue tests, carried over from the S1 spike to the real runs table.
// They run unchanged on SQLite and PostgreSQL; the only dialect branches
// are where the two databases are expected to behave differently.

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/dialect"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

const leaseTTL = 30 * time.Second

// queue is a database with one task version to enqueue runs of.
type queue struct {
	t       *testing.T
	db      *store.DB
	user    store.User
	project store.Project
	task    store.Task
	version store.TaskVersion
	// queuedAt is advanced by every enqueue so that queue order is defined.
	queuedAt time.Time
}

func newQueue(t *testing.T) *queue {
	t.Helper()
	db := storetest.Open(t)
	ctx := context.Background()
	q := &queue{t: t, db: db, queuedAt: time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)}
	q.user = insertUser(t, db, "queue-owner")
	q.project = newProject(q.user.ID, "queue", true)
	now := store.Now()
	q.task = store.Task{ID: store.NewID(), ProjectID: q.project.ID, Name: "t", LatestSeq: 1, CreatedBy: q.user.ID, CreatedAt: now, UpdatedAt: now}
	q.version = store.TaskVersion{ID: store.NewID(), TaskID: q.task.ID, Seq: 1, Manifest: "{}", TreeHash: "x", CreatedBy: q.user.ID, CreatedAt: now}
	for _, m := range []any{&q.project, &q.task, &q.version} {
		if _, err := db.NewInsert().Model(m).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return q
}

func (q *queue) isPostgres() bool { return q.db.Native().Name() == dialect.PostgresName }

type runOpt func(*store.Run)

func withKey(key string) runOpt  { return func(r *store.Run) { r.ConcurrencyKey = key } }
func withStatus(s string) runOpt { return func(r *store.Run) { r.Status = s } }

func (q *queue) insertRun(opts ...runOpt) (store.Run, error) {
	q.queuedAt = q.queuedAt.Add(time.Second)
	run := store.Run{
		ID: store.NewID(), ProjectID: q.project.ID, TaskID: q.task.ID, VersionID: q.version.ID, VersionSeq: 1,
		Status: store.RunQueued, TriggerKind: "manual", Params: "{}", QueuedAt: q.queuedAt,
	}
	for _, o := range opts {
		o(&run)
	}
	_, err := q.db.NewInsert().Model(&run).Exec(context.Background())
	return run, err
}

func (q *queue) mustInsertRun(opts ...runOpt) store.Run {
	q.t.Helper()
	run, err := q.insertRun(opts...)
	if err != nil {
		q.t.Fatalf("insert run: %v", err)
	}
	return run
}

func (q *queue) newRunner() string {
	q.t.Helper()
	now := store.Now()
	r := store.Runner{ID: "runner-" + store.NewID().String(), Name: "test", Slots: 4, LastSeenAt: now, CreatedAt: now}
	if _, err := q.db.NewInsert().Model(&r).Exec(context.Background()); err != nil {
		q.t.Fatal(err)
	}
	return r.ID
}

// claim runs one claim in its own write transaction, as the coordinator does.
func (q *queue) claim(ctx context.Context, runner string, limit int) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := q.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		var err error
		ids, err = q.db.ClaimRuns(ctx, tx, runner, limit, leaseTTL)
		return err
	})
	return ids, err
}

func (q *queue) mustClaim(runner string, limit int) []uuid.UUID {
	q.t.Helper()
	ids, err := q.claim(context.Background(), runner, limit)
	if err != nil {
		q.t.Fatal(err)
	}
	return ids
}

func (q *queue) get(id uuid.UUID) store.Run {
	q.t.Helper()
	var run store.Run
	if err := q.db.NewSelect().Model(&run).Where("id = ?", id).Scan(context.Background()); err != nil {
		q.t.Fatal(err)
	}
	return run
}

func (q *queue) setStatus(id uuid.UUID, status string) error {
	_, err := q.db.NewUpdate().Model((*store.Run)(nil)).Set("status = ?", status).Where("id = ?", id).Exec(context.Background())
	return err
}

func TestRunRoundTrip(t *testing.T) {
	t.Parallel()
	q := newQueue(t)
	zero := 0
	run := q.mustInsertRun(func(r *store.Run) {
		r.ExitCode = &zero
		r.IsTest = true
		r.Result = `{"status":"ok"}`
	})
	got := q.get(run.ID)
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("exit code 0 must survive the round trip, got %v", got.ExitCode)
	}
	if !got.IsTest || got.Result != run.Result || !got.QueuedAt.Equal(run.QueuedAt) {
		t.Fatalf("run = %+v", got)
	}
	if got.RunnerID != "" || !got.StartedAt.IsZero() || got.ConcurrencyKey != "" || got.CreatedBy != uuid.Nil {
		t.Fatalf("NULL columns read back as %+v", got)
	}
	other := q.mustInsertRun()
	if got := q.get(other.ID); got.ExitCode != nil {
		t.Fatalf("NULL exit code read back as %d", *got.ExitCode)
	}
}

func TestClaimRunsOldestFirst(t *testing.T) {
	t.Parallel()
	q := newQueue(t)
	var want []uuid.UUID
	for range 5 {
		want = append(want, q.mustInsertRun().ID)
	}
	q.mustInsertRun(withStatus(store.RunCompleted))
	runner := q.newRunner()
	for i, id := range want {
		got := q.mustClaim(runner, 1)
		if len(got) != 1 || got[0] != id {
			t.Fatalf("claim %d returned %v, want %v", i, got, id)
		}
		run := q.get(id)
		if run.Status != store.RunPreparing || run.RunnerID != runner || run.LeaseExpiresAt.IsZero() {
			t.Fatalf("claimed run not assigned: %+v", run)
		}
	}
	if got := q.mustClaim(runner, 1); len(got) != 0 {
		t.Fatalf("empty queue returned %v", got)
	}
	if _, err := q.db.ClaimRuns(context.Background(), q.db, runner, 0, leaseTTL); err == nil {
		t.Fatal("a zero limit must be rejected")
	}
}

func TestConcurrentClaimsNeverDuplicate(t *testing.T) {
	// Not parallel: it opens many connections.
	q := newQueue(t)
	const runs, workers = 300, 16
	for range runs {
		q.mustInsertRun()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	type claimed struct {
		run    uuid.UUID
		runner string
	}
	var (
		mu   sync.Mutex
		got  []claimed
		errs []error
		wg   sync.WaitGroup
	)
	start := time.Now()
	for w := range workers {
		runner := q.newRunner()
		limit := 1 + w%4 // mix single and batch claims
		wg.Go(func() {
			for {
				batch, err := q.claim(ctx, runner, limit)
				mu.Lock()
				if err != nil {
					errs = append(errs, err)
				}
				for _, id := range batch {
					got = append(got, claimed{id, runner})
				}
				mu.Unlock()
				if err != nil || len(batch) == 0 {
					return
				}
			}
		})
	}
	wg.Wait()
	elapsed := time.Since(start)

	if err := errors.Join(errs...); err != nil {
		t.Fatalf("claim errors: %v", err)
	}
	seen := map[uuid.UUID]string{}
	for _, c := range got {
		if prev, dup := seen[c.run]; dup {
			t.Fatalf("run %s claimed twice, by %s and %s", c.run, prev, c.runner)
		}
		seen[c.run] = c.runner
	}
	if len(seen) != runs {
		t.Fatalf("claimed %d runs, want %d", len(seen), runs)
	}
	var stored []store.Run
	if err := q.db.NewSelect().Model(&stored).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	for _, r := range stored {
		if r.Status != store.RunPreparing || r.RunnerID != seen[r.ID] {
			t.Fatalf("run %s stored as %s/%s, claimed by %s", r.ID, r.Status, r.RunnerID, seen[r.ID])
		}
	}
	t.Logf("%s: %d runs claimed by %d workers in %s", q.db.Native().Name(), runs, workers, elapsed.Round(time.Millisecond))
}

// TestClaimWhileAnotherClaimIsOpen shows the one behavioural difference
// between the dialects: PostgreSQL skips rows another claim has locked,
// SQLite makes the second claimer wait for the write lock.
func TestClaimWhileAnotherClaimIsOpen(t *testing.T) {
	t.Parallel()
	q := newQueue(t)
	q.mustInsertRun()
	q.mustInsertRun()
	runnerA, runnerB := q.newRunner(), q.newRunner()
	ctx := context.Background()

	// Held open outside WriteTx, as another process would.
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	first, err := q.db.ClaimRuns(ctx, tx, runnerA, 1, leaseTTL)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim: %v, %v", first, err)
	}

	type result struct {
		ids []uuid.UUID
		err error
	}
	done := make(chan result, 1)
	go func() {
		ids, err := q.claim(ctx, runnerB, 1)
		done <- result{ids, err}
	}()

	if q.isPostgres() {
		select {
		case r := <-done:
			if r.err != nil || len(r.ids) != 1 || r.ids[0] == first[0] {
				t.Fatalf("second claim: %v, %v; want the other run", r.ids, r.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("second claim blocked behind the first; SKIP LOCKED not effective")
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return
	}

	select {
	case r := <-done:
		t.Fatalf("second claim finished while the first held the write lock: %v, %v", r.ids, r.err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil || len(r.ids) != 1 || r.ids[0] == first[0] {
			t.Fatalf("second claim: %v, %v; want the other run", r.ids, r.err)
		}
	case <-time.After(dialect.DefaultBusyTimeout):
		t.Fatal("second claim did not proceed after the first committed")
	}
}

func TestConcurrencyKey(t *testing.T) {
	t.Parallel()
	q := newQueue(t)
	key := q.task.ID.String()
	wantConflict := func(t *testing.T, err error) {
		t.Helper()
		if !store.IsUniqueViolation(err) {
			t.Fatalf("err = %v, want a unique violation", err)
		}
	}

	active := q.mustInsertRun(withKey(key))
	_, err := q.insertRun(withKey(key))
	wantConflict(t, err)

	// Runs without a key never conflict; finished runs keep their key
	// without blocking anything.
	for range 3 {
		q.mustInsertRun()
	}
	done := q.mustInsertRun(withKey(key), withStatus(store.RunCompleted))
	q.mustInsertRun(withKey(key), withStatus(store.RunFailed))

	// Every active status holds the key.
	for _, st := range []string{store.RunPreparing, store.RunRunning, store.RunCancelling} {
		if err := q.setStatus(active.ID, st); err != nil {
			t.Fatal(err)
		}
		_, err := q.insertRun(withKey(key))
		wantConflict(t, err)
	}

	// Finishing the run frees the key.
	if err := q.setStatus(active.ID, store.RunCancelled); err != nil {
		t.Fatal(err)
	}
	q.mustInsertRun(withKey(key))

	// The index also guards updates: a finished run cannot be revived
	// while another run holds the key.
	wantConflict(t, q.setStatus(done.ID, store.RunQueued))
}

func TestConcurrencyKeyUnderContention(t *testing.T) {
	t.Parallel()
	q := newQueue(t)
	key := q.task.ID.String()
	const writers = 16
	var wg sync.WaitGroup
	errs := make([]error, writers)
	runs := make([]store.Run, writers)
	for i := range writers {
		runs[i] = store.Run{
			ID: store.NewID(), ProjectID: q.project.ID, TaskID: q.task.ID, VersionID: q.version.ID, VersionSeq: 1,
			Status: store.RunQueued, TriggerKind: "manual", Params: "{}", ConcurrencyKey: key, QueuedAt: store.Now(),
		}
	}
	for i := range writers {
		wg.Go(func() {
			errs[i] = q.db.WriteTx(context.Background(), func(ctx context.Context, tx bun.Tx) error {
				_, err := tx.NewInsert().Model(&runs[i]).Exec(ctx)
				return err
			})
		})
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !store.IsUniqueViolation(err):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d inserts succeeded, want exactly 1", ok)
	}
}

func TestRunLease(t *testing.T) {
	t.Parallel()
	q := newQueue(t)
	ctx := context.Background()
	key := q.task.ID.String()
	q.mustInsertRun(withKey(key))
	q.mustInsertRun()
	runner := q.newRunner()
	claimed := q.mustClaim(runner, 2)
	if len(claimed) != 2 {
		t.Fatalf("claimed %d runs", len(claimed))
	}
	var run, other store.Run
	for _, id := range claimed {
		if r := q.get(id); r.ConcurrencyKey == key {
			run = r
		} else {
			other = r
		}
	}
	renew := func(runner string, ids ...uuid.UUID) []dialect.RenewedRun {
		t.Helper()
		renewed, err := q.db.RenewRunLeases(ctx, q.db, runner, ids, leaseTTL)
		if err != nil {
			t.Fatal(err)
		}
		return renewed
	}

	t.Run("owner renews", func(t *testing.T) {
		time.Sleep(2 * time.Millisecond)
		if err := q.setStatus(other.ID, store.RunCancelling); err != nil {
			t.Fatal(err)
		}
		renewed := renew(runner, run.ID, other.ID, store.NewID())
		slices.SortFunc(renewed, func(a, b dialect.RenewedRun) int { return slices.Compare(a.ID[:], b.ID[:]) })
		want := []dialect.RenewedRun{{ID: run.ID, Status: store.RunPreparing}, {ID: other.ID, Status: store.RunCancelling}}
		slices.SortFunc(want, func(a, b dialect.RenewedRun) int { return slices.Compare(a.ID[:], b.ID[:]) })
		if !slices.Equal(renewed, want) {
			t.Fatalf("renewed %+v, want %+v", renewed, want)
		}
		if got := q.get(run.ID); !got.LeaseExpiresAt.After(run.LeaseExpiresAt) {
			t.Fatalf("expiry %v -> %v", run.LeaseExpiresAt, got.LeaseExpiresAt)
		}
		if got := renew(runner); len(got) != 0 {
			t.Fatalf("renewing nothing returned %+v", got)
		}
	})

	t.Run("another runner cannot renew", func(t *testing.T) {
		if got := renew(q.newRunner(), run.ID); len(got) != 0 {
			t.Fatalf("renewed %+v", got)
		}
	})

	t.Run("clock source", func(t *testing.T) {
		// An application clock ten years slow, passed to the dialect
		// directly since the store wrapper always uses the real clock.
		skewed := store.Now().AddDate(-10, 0, 0)
		renewed, err := q.db.Native().RenewRunLeases(ctx, q.db, dialect.RenewRequest{
			RunnerID: runner, RunIDs: []uuid.UUID{run.ID}, LeaseTTL: leaseTTL, Now: skewed,
		})
		if err != nil || len(renewed) != 1 {
			t.Fatalf("renew: %+v, %v", renewed, err)
		}
		exp := q.get(run.ID).LeaseExpiresAt
		if q.isPostgres() {
			// The database clock wins; in tests it is this machine's clock.
			if d := time.Until(exp); d < leaseTTL-10*time.Second || d > leaseTTL+10*time.Second {
				t.Fatalf("lease expires in %s, want about %s from the database clock", d, leaseTTL)
			}
		} else if !exp.Equal(skewed.Add(leaseTTL)) {
			t.Fatalf("lease expires at %v, want %v from the application clock", exp, skewed.Add(leaseTTL))
		}
		// Put the lease back on real time for the next subtests.
		renew(runner, run.ID)
	})

	t.Run("expired lease interrupts the run", func(t *testing.T) {
		expire := func() []uuid.UUID {
			t.Helper()
			var ids []uuid.UUID
			err := q.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
				var err error
				ids, err = q.db.ExpireRunLeases(ctx, tx, "runner_unavailable", "lease expired")
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			return ids
		}
		if expired := expire(); len(expired) != 0 {
			t.Fatalf("live leases expired: %v", expired)
		}
		_, err := q.db.NewUpdate().Model((*store.Run)(nil)).
			Set("lease_expires_at = ?", store.Now().Add(-time.Minute)).
			Where("id = ?", run.ID).Exec(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if expired := expire(); !slices.Equal(expired, []uuid.UUID{run.ID}) {
			t.Fatalf("expired %v; want only %s", expired, run.ID)
		}
		got := q.get(run.ID)
		if got.Status != store.RunInterrupted || got.ErrorCode != "runner_unavailable" || got.ErrorMessage != "lease expired" || got.FinishedAt.IsZero() {
			t.Fatalf("interrupted run = %+v", got)
		}
		if got := q.get(other.ID); got.Status != store.RunCancelling {
			t.Fatalf("run with a live lease changed to %s", got.Status)
		}
		// The runner learns it lost the run on its next renewal.
		if renewed := renew(runner, run.ID); len(renewed) != 0 {
			t.Fatalf("renewal after interruption: %+v", renewed)
		}
		// An interrupted run no longer holds the concurrency key.
		q.mustInsertRun(withKey(key))
	})
}

func TestWriteTxQueuesWriters(t *testing.T) {
	t.Parallel()
	db := storetest.Open(t)
	ctx := context.Background()
	inside := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
			u := newUser("writer-one")
			if _, err := tx.NewInsert().Model(&u).Exec(ctx); err != nil {
				return err
			}
			close(inside)
			<-release
			return nil
		})
	}()
	<-inside

	if db.Native().SingleWriter() {
		// The second writer waits on the in-process gate and gives up
		// with its context, without touching the database.
		short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		err := db.WriteTx(short, func(context.Context, bun.Tx) error {
			t.Error("second write transaction ran while the first held the gate")
			return nil
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want the context's deadline", err)
		}
	}
	second := make(chan error, 1)
	go func() {
		second <- db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
			u := newUser("writer-two")
			_, err := tx.NewInsert().Model(&u).Exec(ctx)
			return err
		})
	}()
	close(release)
	for _, ch := range []chan error{first, second} {
		if err := <-ch; err != nil {
			t.Fatal(err)
		}
	}
	if n, err := db.NewSelect().Model((*store.User)(nil)).Count(ctx); err != nil || n != 2 {
		t.Fatalf("users = %d, %v", n, err)
	}
}
