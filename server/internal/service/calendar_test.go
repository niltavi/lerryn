package service_test

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Schedules that fire every minute over a 31-day window are expanded only
// as far as the calendar shows: the earliest 2000 items, from every
// trigger, in order.
func TestCalendarBoundsPlannedFires(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	allow := basicManifest()
	allow.Concurrency = service.ConcurrencyAllow
	task := f.apply(alice, "busy", allow).Task.ID
	f.finishRun(f.startTest(alice, task, 1).ID, runproto.StatusCompleted, "")
	if _, err := f.tasks.SetActivation(f.ctx, alice, task, true, nil); err != nil {
		t.Fatal(err)
	}
	now := store.Now()
	var ids []uuid.UUID
	for _, name := range []string{"a", "b"} {
		tr := store.Trigger{ID: store.NewID(), TaskID: task, Name: name, Type: store.TriggerTypeCron, Enabled: true,
			Schedule: "* * * * *", Timezone: "UTC", NextFireAt: now.Truncate(time.Minute).Add(time.Minute),
			CreatedBy: alice.UserID, CreatedAt: now, UpdatedAt: now}
		if _, err := f.db.NewInsert().Model(&tr).Exec(f.ctx); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, tr.ID)
	}
	from, to := now, now.Add(31*24*time.Hour)
	cal, err := f.runs.Calendar(f.ctx, alice, service.CalendarInput{From: &from, To: &to})
	if err != nil {
		t.Fatal(err)
	}
	if !cal.Truncated || len(cal.Items) != 2000 {
		t.Fatalf("items = %d, truncated %v", len(cal.Items), cal.Truncated)
	}
	if !slices.IsSortedFunc(cal.Items, func(a, b service.CalendarItem) int { return a.Start.Compare(b.Start) }) {
		t.Fatal("items are not in order")
	}
	seen := map[uuid.UUID]int{}
	for _, it := range cal.Items {
		seen[it.TriggerID]++
	}
	// Both triggers fire each minute: 1000 minutes of each.
	if seen[ids[0]] != 1000 || seen[ids[1]] != 1000 {
		t.Fatalf("fires per trigger = %v", seen)
	}
	if last := cal.Items[len(cal.Items)-1].Start; last.Sub(now) > 1001*time.Minute {
		t.Fatalf("last item at %v, more than 1000 minutes ahead", last)
	}
}
