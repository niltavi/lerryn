package service_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/service"
)

type triggerQueryHookKey struct{}

// triggerQueryHook interleaves one operation after another has read its
// trigger snapshot, before it can start writing.
type triggerQueryHook struct {
	once  sync.Once
	after func()
}

func (h *triggerQueryHook) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (h *triggerQueryHook) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
	if ctx.Value(triggerQueryHookKey{}) == h && event.Err == nil && event.Operation() == "SELECT" &&
		strings.Contains(event.Query, `FROM "triggers"`) {
		h.once.Do(h.after)
	}
}

func TestSetTriggersPreservesConcurrentFire(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{})
	alice := f.user("alice")
	task := f.apply(alice, "digest", allowOverlap()).Task
	in := cron("every-5", "*/5 * * * *")
	set := f.set(alice, task.ID, nil, in)
	f.activate(task)

	hook := &triggerQueryHook{after: func() {
		if n := f.fireDue(t0.Add(5 * time.Minute)); n != 1 {
			t.Fatalf("interleaved fire recorded %d runs", n)
		}
	}}
	f.db.DB = f.db.WithQueryHook(hook)
	in.Params = map[string]any{"source": "erp"}
	ctx := context.WithValue(f.ctx, triggerQueryHookKey{}, hook)
	updated, err := f.triggers.Set(ctx, alice, task.ID, service.SetTriggersInput{Triggers: []service.TriggerInput{in}, IfMatch: &set.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if next := updated.Triggers[0].NextFireAt; !next.Equal(t0.Add(10 * time.Minute)) {
		t.Fatalf("Set moved next fire to %v, want :10", next)
	}
	if last := updated.Triggers[0].Trigger.LastFiredAt; !last.Equal(t0.Add(5 * time.Minute)) {
		t.Fatalf("Set returned last fire %v, want :05", last)
	}
	if n := f.fireDue(t0.Add(10 * time.Minute)); n != 1 {
		t.Fatalf("next scheduled fire recorded %d runs", n)
	}
	runs := f.runsOf(task.ID)
	if len(runs) != 2 || runs[0].Params != "{}" || runs[1].Params != `{"source":"erp"}` {
		t.Fatalf("runs after edit = %+v", runs)
	}
}

func TestFireUsesConcurrentTriggerEdit(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{})
	alice := f.user("alice")
	task := f.apply(alice, "digest", allowOverlap()).Task
	in := cron("every-5", "*/5 * * * *")
	set := f.set(alice, task.ID, nil, in)
	f.activate(task)

	hook := &triggerQueryHook{after: func() {
		in.Params = map[string]any{"source": "erp"}
		f.set(alice, task.ID, &set.Revision, in)
	}}
	f.db.DB = f.db.WithQueryHook(hook)
	ctx := context.WithValue(f.ctx, triggerQueryHookKey{}, hook)
	if n, err := f.triggers.FireDue(ctx, t0.Add(5*time.Minute)); err != nil || n != 1 {
		t.Fatalf("FireDue = %d, %v", n, err)
	}
	runs := f.runsOf(task.ID)
	if len(runs) != 1 || runs[0].Params != `{"source":"erp"}` {
		t.Fatalf("fire used stale trigger params: %+v", runs)
	}
}
