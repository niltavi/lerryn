package service_test

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

type stateQueryHookKey struct{}

type stateHeadHook struct {
	once      sync.Once
	lockOnce  sync.Once
	paused    chan struct{}
	resume    chan struct{}
	finishing chan struct{}
}

func (h *stateHeadHook) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if ctx.Value(stateQueryHookKey{}) == "finish" && event.Operation() == "UPDATE" && strings.Contains(event.Query, `UPDATE "tasks"`) {
		h.lockOnce.Do(func() { close(h.finishing) })
	}
	return ctx
}

func (h *stateHeadHook) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
	if ctx.Value(stateQueryHookKey{}) == "claim" && event.Err == nil && event.Operation() == "SELECT" && strings.Contains(event.Query, `FROM "task_states"`) {
		h.once.Do(func() {
			close(h.paused)
			select {
			case <-h.resume:
			case <-ctx.Done():
			}
		})
	}
}

func TestTaskStateClaimPinsBeforeConcurrentHeadReplacement(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "state", allowOverlap())
	r1, r2 := f.runner("r1"), f.runner("r2")
	first := f.stateRun(alice, task.Task.ID, r1)
	f.publishState(r1, first.ID, stateSnapshot("old"))
	old := f.stateHead(task.Task.ID).BlobHash
	writer := f.stateRun(alice, task.Task.ID, r1)
	if err := f.coord.PutState(f.ctx, r1, writer.ID, stateSnapshot("new")); err != nil {
		t.Fatal(err)
	}
	queued := f.start(alice, task.Task.ID)
	hook := &stateHeadHook{paused: make(chan struct{}), resume: make(chan struct{}), finishing: make(chan struct{})}
	f.db.DB = f.db.WithQueryHook(hook)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	var release sync.Once
	defer release.Do(func() { close(hook.resume) })
	type claimResult struct {
		assignments []runproto.Assignment
		err         error
	}
	claimed := make(chan claimResult, 1)
	finished := make(chan error, 1)
	go func() {
		assignments, err := f.coord.Claim(context.WithValue(ctx, stateQueryHookKey{}, "claim"), r2, 1)
		claimed <- claimResult{assignments, err}
	}()
	select {
	case <-hook.paused:
	case <-ctx.Done():
		t.Fatal("claim did not pause between reading the head and pinning it")
	}
	go func() {
		finished <- f.coord.Finish(context.WithValue(ctx, stateQueryHookKey{}, "finish"), r1, writer.ID, runproto.Finish{Status: runproto.StatusCompleted})
	}()
	if f.db.Native().Name() == "postgres" {
		// PostgreSQL admits both writers, so wait until Finish attempts
		// the task row lock. SQLite instead queues at WriteTx's gate.
		select {
		case <-hook.finishing:
		case <-ctx.Done():
			t.Fatal("Finish did not reach the state head lock")
		}
	}
	select {
	case err := <-finished:
		t.Fatalf("head replacement passed the claim's uncommitted input pin: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	release.Do(func() { close(hook.resume) })
	var result claimResult
	select {
	case result = <-claimed:
	case <-ctx.Done():
		t.Fatal("claim did not finish")
	}
	if result.err != nil || len(result.assignments) != 1 || result.assignments[0].RunID != queued.ID {
		t.Fatalf("claim = %+v, %v", result.assignments, result.err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Finish did not finish after the input pin committed")
	}
	f.cleanup(service.NewRetention(f.db, f.blobs, service.RetentionPolicy{}, nil))
	got, err := f.coord.GetState(f.ctx, r2, queued.ID)
	if err != nil || len(got.Entries) != 1 || string(got.Entries[0].Content) != "old" || !f.has(old) {
		t.Fatalf("new run lost its pinned input: %+v, %v", got, err)
	}
}

type stateCloseHook struct {
	io.ReadCloser
	after func()
}

func (r *stateCloseHook) Close() error {
	err := r.ReadCloser.Close()
	r.after()
	return err
}

func TestTaskStatePutFencesLeaseAfterObjectIO(t *testing.T) {
	for _, phase := range []string{"put", "readback"} {
		t.Run(phase, func(t *testing.T) {
			f := newFixture(t)
			alice := f.user("alice")
			task := f.apply(alice, "state", allowOverlap())
			runner := f.runner("r")
			run := f.stateRun(alice, task.Task.ID, runner)
			expire := func() {
				if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).Set("lease_expires_at = ?", store.Now().Add(-time.Minute)).Where("id = ?", run.ID).Exec(f.ctx); err != nil {
					t.Fatal(err)
				}
			}
			bs := &stateFaultStore{Store: f.blobs}
			if phase == "put" {
				bs.afterPut = func(string) { expire() }
			} else {
				bs.get = func(ctx context.Context, hash string) (io.ReadCloser, error) {
					rc, err := f.blobs.Get(ctx, hash)
					return &stateCloseHook{ReadCloser: rc, after: expire}, err
				}
			}
			coord := service.NewCoordinator(f.db, bs, f.runs, nil)
			wantErr(t, coord.PutState(f.ctx, runner, run.ID, stateSnapshot("pending")), runproto.ErrLeaseLost)
			if f.stateHead(task.Task.ID).BlobHash != "" {
				t.Fatal("object IO published after its lease expired")
			}
			var ready int64
			if err := f.db.NewSelect().Model((*store.RunStateRef)(nil)).ColumnExpr("COUNT(*)").Where("run_id = ? AND role = ? AND ready = ?", run.ID, store.RunStateOutput, true).Scan(f.ctx, &ready); err != nil || ready != 0 {
				t.Fatalf("expired candidate was ready: %d, %v", ready, err)
			}
		})
	}
}

func TestTaskStateCompletedIncludesTestAndBusinessFailure(t *testing.T) {
	for _, test := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal run", true: "test run"}[test], func(t *testing.T) {
			f := newFixture(t)
			alice := f.user("alice")
			task := f.apply(alice, "state", allowOverlap())
			runner := f.runner("r")
			detail, err := f.runs.Start(f.ctx, alice, task.Task.ID, service.StartRunInput{Test: test})
			if err != nil {
				t.Fatal(err)
			}
			f.claim(runner, 1)
			id := detail.Run.ID
			if err := f.coord.Started(f.ctx, runner, id, "", store.Now()); err != nil {
				t.Fatal(err)
			}
			if err := f.coord.AppendEvents(f.ctx, runner, id, []runproto.Event{{Seq: 1, Kind: runproto.EventResult, Result: &runproto.Result{Status: "failed", Summary: "business outcome"}}}); err != nil {
				t.Fatal(err)
			}
			f.publishState(runner, id, stateSnapshot("checkpoint"))
			if f.stateHead(task.Task.ID).BlobHash == "" || f.get(alice, id).Run.Status != store.RunCompleted {
				t.Fatal("business result or is_test changed state publication for a completed process")
			}
		})
	}
}

type stateFinishDelay struct {
	query     string
	until     time.Time
	triggered bool
	wait      func(context.Context) error
	waitErr   error
}

func (h *stateFinishDelay) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if !h.triggered && strings.Contains(event.Query, h.query) {
		h.triggered = true
		if h.wait != nil {
			h.waitErr = h.wait(ctx)
		} else if delay := time.Until(h.until); delay > 0 {
			time.Sleep(delay)
		}
	}
	return ctx
}

func (*stateFinishDelay) AfterQuery(context.Context, *bun.QueryEvent) {}

func TestTaskStateFinishFencesLeaseAfterAllWrites(t *testing.T) {
	for _, phase := range []string{"status event", "version tested"} {
		t.Run(phase, func(t *testing.T) {
			f := newFixture(t)
			alice := f.user("alice")
			task := f.apply(alice, "state", allowOverlap())
			runner := f.runner("r")
			first := f.stateRun(alice, task.Task.ID, runner)
			f.publishState(runner, first.ID, stateSnapshot("old"))
			old := f.stateHead(task.Task.ID)
			detail, err := f.runs.Start(f.ctx, alice, task.Task.ID, service.StartRunInput{Test: phase == "version tested"})
			if err != nil {
				t.Fatal(err)
			}
			f.claim(runner, 1)
			id := detail.Run.ID
			if err := f.coord.Started(f.ctx, runner, id, "", store.Now()); err != nil {
				t.Fatal(err)
			}
			if err := f.coord.PutState(f.ctx, runner, id, stateSnapshot("new")); err != nil {
				t.Fatal(err)
			}
			before := f.get(alice, id).Run
			deadline := store.Now().Add(750 * time.Millisecond)
			if f.db.Native().Name() == "postgres" {
				// The PostgreSQL lease uses the daemon's clock. A Docker
				// VM can be offset from the test host, so host sleep alone
				// cannot establish that the lease actually expired.
				var backendNow time.Time
				if err := f.db.NewRaw("SELECT clock_timestamp()").Scan(f.ctx, &backendNow); err != nil {
					t.Fatal(err)
				}
				t.Logf("database clock offset from host: %s", backendNow.Sub(store.Now()))
				deadline = backendNow.Add(750 * time.Millisecond)
			}
			if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).Set("lease_expires_at = ?", deadline).Where("id = ?", id).Exec(f.ctx); err != nil {
				t.Fatal(err)
			}
			query := "event_seq = event_seq + 1"
			if phase == "version tested" {
				query = `UPDATE "task_versions"`
			}
			hook := &stateFinishDelay{query: query, until: deadline.Add(30 * time.Millisecond)}
			if f.db.Native().Name() == "postgres" {
				hook.wait = func(ctx context.Context) error {
					waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
					defer cancel()
					for {
						var backendNow, expires time.Time
						// This uses the pool, independently of the Finish
						// transaction holding the run's row lock.
						if err := f.db.NewRaw("SELECT clock_timestamp(), lease_expires_at FROM runs WHERE id = ?", id).Scan(waitCtx, &backendNow, &expires); err != nil {
							return err
						}
						if !backendNow.Before(expires) {
							t.Logf("independent connection confirmed database lease expired by %s", backendNow.Sub(expires))
							return nil
						}
						select {
						case <-waitCtx.Done():
							return waitCtx.Err()
						case <-time.After(10 * time.Millisecond):
						}
					}
				}
			}
			f.db.DB = f.db.WithQueryHook(hook)
			err = f.coord.Finish(f.ctx, runner, id, runproto.Finish{Status: runproto.StatusCompleted})
			if !hook.triggered || hook.waitErr != nil {
				t.Fatalf("lease-expiry fixture did not establish its boundary: triggered=%t err=%v", hook.triggered, hook.waitErr)
			}
			wantErr(t, err, runproto.ErrLeaseLost)
			got := f.get(alice, id).Run
			if !hook.triggered || got.Status != store.RunRunning || got.EventSeq != before.EventSeq || !got.FinishedAt.IsZero() || f.stateRefCount(id) != 2 || f.stateHead(task.Task.ID).BlobHash != old.BlobHash {
				t.Fatal("late lease failure did not roll back the run, event, head and references together")
			}
			var version store.TaskVersion
			if err := f.db.NewSelect().Model(&version).Where("id = ?", got.VersionID).Scan(f.ctx); err != nil || !version.TestedAt.IsZero() {
				t.Fatalf("late lease failure marked the version tested: %v", err)
			}
			if _, err := f.runs.Reconcile(f.ctx); err != nil || f.stateRefCount(id) != 0 || f.stateHead(task.Task.ID).BlobHash != old.BlobHash {
				t.Fatalf("reconciliation did not clean the rolled-back run safely: %v", err)
			}
		})
	}
}

func TestTaskStateBatchClaimFinishAndArchiveLockOrder(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	tasks := []service.TaskDetail{f.apply(alice, "state-a", allowOverlap()), f.apply(alice, "state-b", allowOverlap())}
	oldRunner, newRunner := f.runner("old"), f.runner("new")
	var active, queued []store.Run
	for _, task := range tasks {
		run := f.stateRun(alice, task.Task.ID, oldRunner)
		if err := f.coord.PutState(f.ctx, oldRunner, run.ID, stateSnapshot(task.Task.Name)); err != nil {
			t.Fatal(err)
		}
		active = append(active, run)
	}
	for _, task := range tasks {
		queued = append(queued, f.start(alice, task.Task.ID))
	}
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	type result struct {
		name        string
		assignments []runproto.Assignment
		err         error
	}
	done := make(chan result, 4)
	go func() {
		<-start
		a, err := f.coord.Claim(ctx, newRunner, 2)
		done <- result{name: "batch claim", assignments: a, err: err}
	}()
	for i, run := range active {
		go func() {
			<-start
			err := f.coord.Finish(ctx, oldRunner, run.ID, runproto.Finish{Status: runproto.StatusCompleted})
			done <- result{name: tasks[i].Task.Name, err: err}
		}()
	}
	go func() {
		<-start
		_, err := f.tasks.Archive(ctx, alice, tasks[0].Task.ID)
		done <- result{name: "archive", err: err}
	}()
	close(start)
	var claimed []runproto.Assignment
	for range 4 {
		select {
		case got := <-done:
			if got.err != nil {
				t.Fatalf("concurrent %s: %v", got.name, got.err)
			}
			if got.name == "batch claim" {
				claimed = got.assignments
			}
		case <-ctx.Done():
			t.Fatal("batch Claim, Finish and Archive did not complete within their lock deadline")
		}
	}
	if len(claimed) != len(queued) {
		t.Fatalf("batch claim returned %d runs", len(claimed))
	}
	for i, task := range tasks {
		if f.get(alice, active[i].ID).Run.Status != store.RunCompleted || f.stateRefCount(active[i].ID) != 0 || f.stateHead(task.Task.ID).BlobHash == "" {
			t.Fatal("concurrent operations left an inconsistent terminal run or task head")
		}
	}
	var archived store.Task
	if err := f.db.NewSelect().Model(&archived).Where("id = ?", tasks[0].Task.ID).Scan(f.ctx); err != nil || archived.ArchivedAt.IsZero() {
		t.Fatalf("task was not archived: %v", err)
	}
	for _, a := range claimed {
		if !a.HasState {
			t.Fatal("batch claim omitted its state input session")
		}
		input, err := f.coord.GetState(f.ctx, newRunner, a.RunID)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range input.Entries {
			for _, task := range tasks {
				if task.Task.ID == a.TaskID && string(entry.Content) != task.Task.Name {
					t.Fatal("a batch claim pinned another task's state")
				}
			}
		}
		// This also finishes the queued run of the now-archived task:
		// archiving never cancels work that was already accepted.
		if err := f.coord.Started(f.ctx, newRunner, a.RunID, "", store.Now()); err != nil {
			t.Fatal(err)
		}
		f.publishState(newRunner, a.RunID, stateSnapshot("next"))
	}
}
