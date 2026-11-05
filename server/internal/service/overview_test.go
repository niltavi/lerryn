package service_test

import (
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestOverviewLatestOutcome(t *testing.T) {
	for _, tt := range []struct {
		name               string
		first, second      string
		sameQueuedAt       bool
		firstFinishesLater bool
		wantAttention      bool
		wantFirst          bool
	}{
		{name: "recovered", first: runproto.StatusFailed, second: runproto.StatusCompleted},
		{name: "failed again", first: runproto.StatusCompleted, second: runproto.StatusFailed, wantAttention: true},
		{name: "repeated failure", first: runproto.StatusFailed, second: runproto.StatusFailed, wantAttention: true},
		{name: "same queue time recovered", first: runproto.StatusFailed, second: runproto.StatusCompleted, sameQueuedAt: true},
		{name: "same queue time failed", first: runproto.StatusCompleted, second: runproto.StatusFailed, sameQueuedAt: true, wantAttention: true},
		{name: "earlier run finishes last", first: runproto.StatusFailed, second: runproto.StatusCompleted, firstFinishesLater: true, wantAttention: true, wantFirst: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			alice := f.user("alice")
			task := f.apply(alice, "digest", basicManifest()).Task.ID
			first := f.start(alice, task)
			f.finishRun(first.ID, tt.first, "")
			second := f.start(alice, task)
			f.finishRun(second.ID, tt.second, "")

			// Different runners may report the same finish timestamp; the
			// later queue time, then UUID, selects one outcome in that case.
			finished := store.Now().Add(-time.Minute)
			for i, run := range []store.Run{first, second} {
				queued := finished.Add(-time.Duration(2-i) * time.Minute)
				if tt.sameQueuedAt {
					queued = finished.Add(-2 * time.Minute)
				}
				at := finished
				if i == 0 && tt.firstFinishesLater {
					at = at.Add(time.Second)
				}
				if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).
					Set("queued_at = ?", queued).Set("finished_at = ?", at).
					Where("id = ?", run.ID).Exec(f.ctx); err != nil {
					t.Fatal(err)
				}
			}

			o, err := f.runs.Overview(f.ctx, alice, nil)
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 0
			if tt.wantAttention {
				wantCount = 1
			}
			if o.Counts.Attention != wantCount || len(o.Attention) != wantCount {
				t.Fatalf("attention count = %d, items = %+v; want %d", o.Counts.Attention, o.Attention, wantCount)
			}
			if tt.wantAttention {
				wantID := second.ID
				if tt.wantFirst {
					wantID = first.ID
				}
				if item := o.Attention[0]; item.RunID != wantID || item.Kind != service.AttentionRunFailed {
					t.Fatalf("attention = %+v; want failed run %s", item, wantID)
				}
			}
		})
	}
}
