package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

type claimDeliveryFixture struct {
	*fixture
	owner  *auth.Principal
	taskID uuid.UUID
	runner string
	run    store.Run
	ended  []uuid.UUID
}

func newClaimDeliveryFixture(t *testing.T) *claimDeliveryFixture {
	t.Helper()
	f := newFixture(t)
	owner := f.user("alice")
	_, err := f.tasks.RegisterTool(f.ctx, owner, owner.PersonalProjectID, service.RegisterToolInput{
		Name: "claim-library", Kind: store.ToolKindScript, Description: "Claim delivery fixture",
		Files: []service.FileInput{{Path: "library.py", Content: []byte("value = 1\n")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := allowOverlap()
	m.Tools = []string{"claim-library"}
	task := f.apply(owner, "claim-delivery", m)
	runner := f.runner("claim-runner")
	seed := f.stateRun(owner, task.Task.ID, runner)
	f.publishState(runner, seed.ID, stateSnapshot("published"))
	c := &claimDeliveryFixture{fixture: f, owner: owner, taskID: task.Task.ID, runner: runner}
	c.run = f.start(owner, c.taskID)
	f.runs.OnRunEnded(func(id uuid.UUID) { c.ended = append(c.ended, id) })
	return c
}

type claimDeliverySnapshot struct {
	Run      store.Run
	Events   []store.RunEvent
	Refs     []store.RunStateRef
	Releases []store.BlobRelease
	Head     store.TaskState
}

func (f *claimDeliveryFixture) snapshot() claimDeliverySnapshot {
	f.t.Helper()
	var s claimDeliverySnapshot
	if err := f.db.NewSelect().Model(&s.Run).Where("id = ?", f.run.ID).Scan(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.NewSelect().Model(&s.Events).Where("run_id = ?", f.run.ID).OrderExpr("seq ASC").Scan(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.NewSelect().Model(&s.Refs).Where("run_id = ?", f.run.ID).OrderExpr("role ASC").Scan(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.NewSelect().Model(&s.Releases).OrderExpr("id ASC").Scan(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	s.Head = f.stateHead(f.taskID)
	return s
}

// PostgreSQL may run on a different clock from the test process. Read its
// current clock, and poll through the pool independently of any writer.
func claimDeliveryClock(ctx context.Context, db *store.DB) (time.Time, error) {
	if db.Native().Name() != "postgres" {
		return store.Now(), nil
	}
	var now time.Time
	err := db.NewRaw("SELECT clock_timestamp()").Scan(ctx, &now)
	return now, err
}

func (f *claimDeliveryFixture) shortLease(id uuid.UUID, duration time.Duration) {
	f.t.Helper()
	now, err := claimDeliveryClock(f.ctx, f.db)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).Set("lease_expires_at = ?", now.Add(duration)).Where("id = ?", id).Exec(f.ctx); err != nil {
		f.t.Fatal(err)
	}
}

func claimDeliveryWaitExpired(ctx context.Context, db *store.DB, id uuid.UUID) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		var lease time.Time
		if err := db.NewSelect().Model((*store.Run)(nil)).Column("lease_expires_at").Where("id = ?", id).Scan(ctx, &lease); err != nil {
			return err
		}
		now, err := claimDeliveryClock(ctx, db)
		if err != nil {
			return err
		}
		if !now.Before(lease) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (f *claimDeliveryFixture) expire(id uuid.UUID) {
	f.t.Helper()
	f.shortLease(id, 20*time.Millisecond)
	if err := claimDeliveryWaitExpired(f.ctx, f.db, id); err != nil {
		f.t.Fatal(err)
	}
}

func (f *claimDeliveryFixture) stageOutput() {
	f.t.Helper()
	if err := f.coord.PutState(f.ctx, f.runner, f.run.ID, stateSnapshot("candidate")); err != nil {
		f.t.Fatal(err)
	}
}

func TestClaimDeliveryLifecycleDuringToolRead(t *testing.T) {
	for _, lifecycle := range []string{"live", "expired", "reconciled", "foreign", "running", "terminal", "cancelling"} {
		for _, readFails := range []bool{false, true} {
			name := lifecycle + "/success"
			if readFails {
				name = lifecycle + "/failure"
			}
			t.Run(name, func(t *testing.T) {
				f := newClaimDeliveryFixture(t)
				var before claimDeliverySnapshot
				var endedBefore int
				reads := 0
				bs := &stateFaultStore{Store: f.blobs}
				bs.get = func(ctx context.Context, hash string) (io.ReadCloser, error) {
					reads++
					f.stageOutput()
					switch lifecycle {
					case "expired", "reconciled":
						f.expire(f.run.ID)
						if lifecycle == "reconciled" {
							ids, err := f.runs.Reconcile(f.ctx)
							if err != nil || !slices.Equal(ids, []uuid.UUID{f.run.ID}) {
								t.Fatalf("reconcile = %v, %v", ids, err)
							}
						}
					case "foreign":
						// Ownership cannot normally change mid-claim; exercise the
						// defensive guard without impersonating another runner.
						other := f.fixture.runner("other-runner")
						if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).Set("runner_id = ?", other).Where("id = ?", f.run.ID).Exec(f.ctx); err != nil {
							t.Fatal(err)
						}
					case "running":
						if err := f.coord.Started(f.ctx, f.runner, f.run.ID, "", store.Now()); err != nil {
							t.Fatal(err)
						}
					case "terminal":
						if err := f.coord.Finish(f.ctx, f.runner, f.run.ID, runproto.Finish{Status: runproto.StatusCancelled}); err != nil {
							t.Fatal(err)
						}
					case "cancelling":
						if _, err := f.runs.Cancel(f.ctx, f.owner, f.run.ID); err != nil {
							t.Fatal(err)
						}
					}
					before, endedBefore = f.snapshot(), len(f.ended)
					if readFails {
						return nil, errors.New("injected tool object read failure")
					}
					return f.blobs.Get(ctx, hash)
				}
				coord := service.NewCoordinator(f.db, bs, f.runs, nil)
				got, err := coord.Claim(f.ctx, f.runner, 1)
				if err != nil || reads != 1 {
					t.Fatalf("Claim: reads=%d, err=%v", reads, err)
				}
				live := lifecycle == "live" || lifecycle == "cancelling"
				if live && !readFails {
					if len(got) != 1 || got[0].RunID != f.run.ID || !got[0].HasState || len(got[0].Manifest.Tools) != 1 {
						t.Fatalf("live assignment = %+v", got)
					}
				} else if len(got) != 0 {
					t.Errorf("Claim returned an ineligible assignment: %+v", got)
				}
				after := f.snapshot()
				if live && readFails {
					if after.Run.Status != store.RunFailed || after.Run.ErrorCode != "internal_error" || after.Run.FinishedAt.IsZero() || after.Run.EventSeq != before.Run.EventSeq+1 {
						t.Fatalf("live failure = %+v", after.Run)
					}
					if len(after.Refs) != 0 || len(after.Events) != len(before.Events)+1 || !reflect.DeepEqual(after.Head, before.Head) || !slices.Equal(f.ended[endedBefore:], []uuid.UUID{f.run.ID}) {
						t.Fatal("live failure did not atomically record one event, release refs, preserve the head and notify once")
					}
					var transition struct{ From, To string }
					last := after.Events[len(after.Events)-1]
					if err := json.Unmarshal([]byte(last.Payload), &transition); err != nil || last.Kind != "status" || transition.From != before.Run.Status || transition.To != store.RunFailed {
						t.Fatalf("failure transition = %+v, %v", last, err)
					}
					for _, ref := range before.Refs {
						if ref.BlobHash != "" && !slices.ContainsFunc(after.Releases, func(r store.BlobRelease) bool { return r.Hash == ref.BlobHash }) {
							t.Fatalf("released state hash %s was not queued for collection", ref.BlobHash)
						}
					}
				} else if !reflect.DeepEqual(after, before) || len(f.ended) != endedBefore {
					t.Fatalf("late delivery changed lifecycle data or emitted ended: before=%+v after=%+v ended=%v", before, after, f.ended)
				}
				if lifecycle == "expired" {
					ids, err := f.runs.Reconcile(f.ctx)
					if err != nil || !slices.Equal(ids, []uuid.UUID{f.run.ID}) || f.stateRefCount(f.run.ID) != 0 || f.get(f.owner, f.run.ID).Run.Status != store.RunInterrupted || !slices.Equal(f.ended[endedBefore:], []uuid.UUID{f.run.ID}) {
						t.Fatalf("unswept claim did not remain available for reconciliation: %v, %v", ids, err)
					}
				}
			})
		}
	}
}

func TestClaimDeliveryPropagatesCancellation(t *testing.T) {
	for _, readFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "read succeeds", true: "read fails"}[readFails], func(t *testing.T) {
			f := newClaimDeliveryFixture(t)
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			var before claimDeliverySnapshot
			bs := &stateFaultStore{Store: f.blobs}
			bs.get = func(_ context.Context, hash string) (io.ReadCloser, error) {
				f.stageOutput()
				before = f.snapshot()
				cancel()
				if readFails {
					return nil, errors.New("injected tool read failure after cancellation")
				}
				return f.blobs.Get(f.ctx, hash)
			}
			got, err := service.NewCoordinator(f.db, bs, f.runs, nil).Claim(ctx, f.runner, 1)
			if !errors.Is(err, context.Canceled) || len(got) != 0 {
				t.Fatalf("cancelled Claim = %+v, %v", got, err)
			}
			if !reflect.DeepEqual(f.snapshot(), before) || len(f.ended) != 0 {
				t.Fatal("cancelled delivery changed committed lifecycle data")
			}
		})
	}
}

func TestClaimDeliveryPropagatesDatabaseFailure(t *testing.T) {
	for _, readFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "read succeeds", true: "read fails"}[readFails], func(t *testing.T) {
			f := newClaimDeliveryFixture(t)
			var before claimDeliverySnapshot
			renamed := false
			restore := func() {
				if renamed {
					if _, err := f.db.ExecContext(f.ctx, `ALTER TABLE runs_claim_delivery_unavailable RENAME TO runs`); err != nil {
						t.Fatal(err)
					}
					renamed = false
				}
			}
			defer restore()
			bs := &stateFaultStore{Store: f.blobs}
			bs.get = func(ctx context.Context, hash string) (io.ReadCloser, error) {
				f.stageOutput()
				before = f.snapshot()
				// Each fixture owns its schema. A real statement error tests
				// propagation without replacing database calls with a mock.
				if _, err := f.db.ExecContext(f.ctx, `ALTER TABLE runs RENAME TO runs_claim_delivery_unavailable`); err != nil {
					t.Fatal(err)
				}
				renamed = true
				if readFails {
					return nil, errors.New("injected tool read failure during database failure")
				}
				return f.blobs.Get(ctx, hash)
			}
			got, err := service.NewCoordinator(f.db, bs, f.runs, nil).Claim(f.ctx, f.runner, 1)
			restore()
			if err == nil || len(got) != 0 {
				t.Fatalf("database failure became a successful Claim: %+v, %v", got, err)
			}
			if !reflect.DeepEqual(f.snapshot(), before) || len(f.ended) != 0 {
				t.Fatal("failed database operation changed lifecycle data")
			}
		})
	}
}
