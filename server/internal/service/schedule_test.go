package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/scheduler"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestPlanFireMissedCountCap(t *testing.T) {
	c, err := scheduler.ParseCron("0 * * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	last := first.Add(time.Duration(maxCountedMisses+1) * time.Hour)
	for _, catchUp := range []string{store.CatchUpSkip, store.CatchUpLatest} {
		t.Run(catchUp, func(t *testing.T) {
			p, err := planFire(t.Context(), c, first, last.Add(2*time.Minute), time.Minute, catchUp)
			if err != nil {
				t.Fatal(err)
			}
			if !p.capped || p.missed != maxCountedMisses || !p.missedFrom.Equal(first) {
				t.Fatalf("missed summary = %+v", p)
			}
			wantEnd := last
			if catchUp == store.CatchUpLatest {
				wantEnd = last.Add(-time.Hour)
				if len(p.runs) != 1 || !p.runs[0].Equal(last) {
					t.Fatalf("catch-up runs = %v, want %v", p.runs, last)
				}
			} else if len(p.runs) != 0 {
				t.Fatalf("skip produced runs: %v", p.runs)
			}
			if !p.missedAt.Equal(wantEnd) || !p.last().Equal(last) {
				t.Fatalf("missed end = %v, last = %v; want %v and %v", p.missedAt, p.last(), wantEnd, last)
			}
		})
	}
}

func TestPlanFireExactMissedCount(t *testing.T) {
	c, err := scheduler.ParseCron("0 * * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, count := range []int{1, 2, maxCountedMisses} {
		last := first.Add(time.Duration(count-1) * time.Hour)
		p, err := planFire(t.Context(), c, first, last.Add(2*time.Minute), time.Minute, store.CatchUpLatest)
		if err != nil {
			t.Fatal(err)
		}
		if p.capped || p.missed != count-1 || len(p.runs) != 1 || !p.runs[0].Equal(last) {
			t.Fatalf("%d missed fires: %+v", count, p)
		}
		if count == 1 {
			if !p.missedAt.IsZero() || !p.missedFrom.IsZero() {
				t.Fatalf("no skipped times should remain: %+v", p)
			}
		} else if !p.missedFrom.Equal(first) || !p.missedAt.Equal(last.Add(-time.Hour)) {
			t.Fatalf("skipped range = %v to %v", p.missedFrom, p.missedAt)
		}
	}
}

func TestPlanFireLongOutageKeepsTimelyFire(t *testing.T) {
	c, err := scheduler.ParseCron("0 * * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	last := first.Add(time.Duration(10*maxCountedMisses) * time.Hour)
	// The current fire is exactly at the grace boundary, so latest must
	// leave all earlier times skipped and run this one once.
	p, err := planFire(t.Context(), c, first, last.Add(time.Minute), time.Minute, store.CatchUpLatest)
	if err != nil {
		t.Fatal(err)
	}
	if !p.capped || p.missed != maxCountedMisses || !p.missedFrom.Equal(first) ||
		!p.missedAt.Equal(last.Add(-time.Hour)) || len(p.runs) != 1 || !p.runs[0].Equal(last) {
		t.Fatalf("long outage plan = %+v", p)
	}
}

func TestPlanFireCancellation(t *testing.T) {
	c, err := scheduler.ParseCron("0 * * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, cancelBefore := range []bool{true, false} {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		want := context.DeadlineExceeded
		if cancelBefore {
			cancel()
			want = context.Canceled
		}
		p, err := planFire(ctx, c, first, first.Add(100*365*24*time.Hour), time.Minute, store.CatchUpLatest)
		cancel()
		if !errors.Is(err, want) || p.missed != 0 || len(p.runs) != 0 {
			t.Fatalf("canceled plan = %+v, %v; want %v", p, err, want)
		}
	}
}
