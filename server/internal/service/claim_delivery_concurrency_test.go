package service_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/events"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

type claimDeliveryDelay struct {
	armed     bool
	query     string
	triggered bool
	wait      func(context.Context) error
	waitErr   error
}

func (h *claimDeliveryDelay) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if h.armed && !h.triggered && strings.Contains(event.Query, h.query) {
		h.triggered = true
		h.waitErr = h.wait(ctx)
	}
	return ctx
}

func (*claimDeliveryDelay) AfterQuery(context.Context, *bun.QueryEvent) {}

func TestClaimDeliveryFailureFencesAfterAllWrites(t *testing.T) {
	for _, phase := range []string{"status event", "state release"} {
		t.Run(phase, func(t *testing.T) {
			f := newClaimDeliveryFixture(t)
			query := "event_seq = event_seq + 1"
			if phase == "state release" {
				query = `INSERT INTO "blob_releases"`
			}
			hook := &claimDeliveryDelay{query: query, wait: func(ctx context.Context) error {
				return claimDeliveryWaitExpired(ctx, f.db, f.run.ID)
			}}
			f.db.DB = f.db.WithQueryHook(hook)
			var before claimDeliverySnapshot
			bs := &stateFaultStore{Store: f.blobs}
			bs.get = func(context.Context, string) (io.ReadCloser, error) {
				f.stageOutput()
				f.shortLease(f.run.ID, 750*time.Millisecond)
				before = f.snapshot()
				if before.Run.Status != store.RunPreparing || len(before.Refs) != 2 {
					t.Fatal("late-fence fixture did not retain both committed state references")
				}
				hook.armed = true
				return nil, errors.New("injected tool object read failure")
			}
			got, err := service.NewCoordinator(f.db, bs, f.runs, nil).Claim(f.ctx, f.runner, 1)
			if err != nil || len(got) != 0 || !hook.triggered || hook.waitErr != nil {
				t.Fatalf("late failure Claim: got=%+v err=%v paused=%t wait=%v", got, err, hook.triggered, hook.waitErr)
			}
			if after := f.snapshot(); !reflect.DeepEqual(after, before) || len(f.ended) != 0 {
				t.Fatalf("expired failure committed a partial run/event/ref/release/head change or ended: before=%+v after=%+v ended=%v", before, after, f.ended)
			}
			ids, err := f.runs.Reconcile(f.ctx)
			if err != nil || !slices.Equal(ids, []uuid.UUID{f.run.ID}) || f.stateRefCount(f.run.ID) != 0 || !slices.Equal(f.ended, []uuid.UUID{f.run.ID}) || f.stateHead(f.taskID).BlobHash != before.Head.BlobHash {
				t.Fatalf("reconciliation after rollback = %v, %v", ids, err)
			}
		})
	}
}

type claimDeliveryBus struct {
	*events.Local
	beforePublish func(events.Event)
}

func (b *claimDeliveryBus) Publish(ctx context.Context, event events.Event) error {
	if b.beforePublish != nil {
		b.beforePublish(event)
	}
	return b.Local.Publish(ctx, event)
}

func TestClaimDeliveryFiltersEntireBatchAfterLastWait(t *testing.T) {
	for _, phase := range []string{"later tool IO", "later failure transaction", "progress notification", "ended notification"} {
		t.Run(phase, func(t *testing.T) {
			f := newClaimDeliveryFixture(t)
			plainTask := f.apply(f.owner, "plain", allowOverlap())
			plain := f.start(f.owner, plainTask.Task.ID)
			laterScript := f.start(f.owner, f.taskID)
			lastPlain := f.start(f.owner, plainTask.Task.ID)
			expireEarlier := func() {
				f.expire(f.run.ID)
				f.expire(plain.ID)
			}
			var waited bool
			bus := &claimDeliveryBus{Local: events.NewLocal()}
			bus.beforePublish = func(event events.Event) {
				if (phase == "progress notification" && event.Kind == events.RunChanged && event.RunID == lastPlain.ID) ||
					(phase == "ended notification" && event.Kind == events.RunEnded && event.RunID == laterScript.ID) {
					waited = true
					expireEarlier()
				}
			}
			f.runs.WithEvents(bus, true)
			hook := &claimDeliveryDelay{query: "event_seq = event_seq + 1", wait: func(ctx context.Context) error {
				if err := claimDeliveryWaitExpired(ctx, f.db, f.run.ID); err != nil {
					return err
				}
				return claimDeliveryWaitExpired(ctx, f.db, plain.ID)
			}}
			f.db.DB = f.db.WithQueryHook(hook)
			reads := 0
			bs := &stateFaultStore{Store: f.blobs}
			bs.get = func(ctx context.Context, hash string) (io.ReadCloser, error) {
				reads++
				if reads == 2 {
					switch phase {
					case "later tool IO":
						waited = true
						expireEarlier()
					case "later failure transaction":
						f.shortLease(f.run.ID, 750*time.Millisecond)
						f.shortLease(plain.ID, 750*time.Millisecond)
						hook.armed = true
						return nil, errors.New("injected later tool read failure")
					case "ended notification":
						return nil, errors.New("injected later tool read failure")
					}
				}
				return f.blobs.Get(ctx, hash)
			}
			got, err := service.NewCoordinator(f.db, bs, f.runs, nil).Claim(f.ctx, f.runner, 4)
			want := []uuid.UUID{laterScript.ID, lastPlain.ID}
			if phase == "later failure transaction" || phase == "ended notification" {
				want = []uuid.UUID{lastPlain.ID}
			}
			ids := make([]uuid.UUID, len(got))
			for i, a := range got {
				ids[i] = a.RunID
			}
			if err != nil || reads != 2 || !slices.Equal(ids, want) {
				t.Fatalf("batch after %s = %v, want %v; reads=%d err=%v", phase, ids, want, reads, err)
			}
			if phase == "later failure transaction" {
				if !hook.triggered || hook.waitErr != nil {
					t.Fatalf("failure write did not wait for database expiry: triggered=%t err=%v", hook.triggered, hook.waitErr)
				}
			} else if !waited {
				t.Fatal("fixture did not reach the intended final wait")
			}
			for _, id := range []uuid.UUID{f.run.ID, plain.ID} {
				var run store.Run
				if err := f.db.NewSelect().Model(&run).Where("id = ?", id).Scan(f.ctx); err != nil || run.Status != store.RunPreparing || !run.FinishedAt.IsZero() || f.stateRefCount(id) != 1 {
					t.Fatalf("batch filter modified unswept claim %s: %+v, %v", id, run, err)
				}
			}
			if phase == "later failure transaction" || phase == "ended notification" {
				if !slices.Equal(f.ended, []uuid.UUID{laterScript.ID}) || f.stateRefCount(laterScript.ID) != 0 || f.get(f.owner, laterScript.ID).Run.Status != store.RunFailed {
					t.Fatal("later live failure did not commit normally")
				}
			} else if len(f.ended) != 0 {
				t.Fatalf("filtering sent an ended notification: %v", f.ended)
			}
		})
	}
}

func TestClaimDeliveryFiltersBatchWithoutScriptTools(t *testing.T) {
	f := newFixture(t)
	owner := f.user("alice")
	task := f.apply(owner, "plain", allowOverlap())
	runner := f.runner("r")
	first, second := f.start(owner, task.Task.ID), f.start(owner, task.Task.ID)
	waited := false
	bus := &claimDeliveryBus{Local: events.NewLocal()}
	bus.beforePublish = func(event events.Event) {
		if event.Kind == events.RunChanged && event.RunID == second.ID {
			waited = true
			cf := &claimDeliveryFixture{fixture: f}
			cf.expire(first.ID)
		}
	}
	f.runs.WithEvents(bus, true)
	got, err := f.coord.Claim(f.ctx, runner, 2)
	if err != nil || !waited || len(got) != 1 || got[0].RunID != second.ID || !got[0].HasState || f.stateRefCount(first.ID) != 1 || f.get(owner, first.ID).Run.Status != store.RunPreparing {
		t.Fatalf("plain batch = %+v, %v; waited=%t", got, err, waited)
	}
}
