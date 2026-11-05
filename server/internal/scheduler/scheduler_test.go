package scheduler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock advances only when told to. An After channel fires once the
// clock has moved to or past its deadline.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, fakeWaiter{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *fakeClock) waiting() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
			continue
		}
		kept = append(kept, w)
	}
	c.waiters = kept
}

type fakeFirer struct {
	mu     sync.Mutex
	fires  []time.Time
	sweeps []time.Time
	fail   bool
}

func (f *fakeFirer) FireDue(_ context.Context, now time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fires = append(f.fires, now)
	if f.fail {
		return 0, errors.New("database down")
	}
	return 1, nil
}

func (f *fakeFirer) RetryFailed(context.Context, time.Time) (int, error) { return 0, nil }

func (f *fakeFirer) Sweep(_ context.Context, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweeps = append(f.sweeps, now)
	return nil
}

func (f *fakeFirer) snapshot() (fires, sweeps []time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.fires...), append([]time.Time(nil), f.sweeps...)
}

type switchLeader struct{ on atomic.Bool }

func (l *switchLeader) IsLeader() bool { return l.on.Load() }

func TestTickRunsOnlyOnTheLeader(t *testing.T) {
	start := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	clock := newFakeClock(start)
	firer := &fakeFirer{}
	leader := &switchLeader{}
	s := New(firer, Options{Clock: clock, Leader: leader, SweepInterval: time.Hour})
	ctx := context.Background()

	s.Tick(ctx)
	if fires, _ := firer.snapshot(); len(fires) != 0 {
		t.Fatalf("a follower fired: %v", fires)
	}
	leader.on.Store(true)
	s.Tick(ctx)
	clock.Advance(30 * time.Minute)
	s.Tick(ctx)
	clock.Advance(31 * time.Minute)
	s.Tick(ctx)
	leader.on.Store(false)
	clock.Advance(time.Hour)
	s.Tick(ctx)

	fires, sweeps := firer.snapshot()
	want := []time.Time{start, start.Add(30 * time.Minute), start.Add(61 * time.Minute)}
	if len(fires) != len(want) {
		t.Fatalf("fires at %v, want %v", fires, want)
	}
	for i := range want {
		if !fires[i].Equal(want[i]) {
			t.Fatalf("fire %d at %v, want %v: the loop must pass its clock", i, fires[i], want[i])
		}
	}
	// The first leader tick sweeps, the next only an hour later.
	if len(sweeps) != 2 || !sweeps[0].Equal(start) || !sweeps[1].Equal(start.Add(61*time.Minute)) {
		t.Fatalf("sweeps at %v", sweeps)
	}
}

func TestRunTicksEveryIntervalUntilCancelled(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC))
	// Errors are logged and the loop goes on.
	firer := &fakeFirer{fail: true}
	s := New(firer, Options{Clock: clock, Interval: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	for i := 1; i <= 3; i++ {
		waitFor(t, func() bool { return clock.waiting() == 1 })
		if fires, _ := firer.snapshot(); len(fires) != i {
			t.Fatalf("after %d intervals the loop ticked %d times", i-1, len(fires))
		}
		clock.Advance(time.Second)
	}
	waitFor(t, func() bool { fires, _ := firer.snapshot(); return len(fires) == 4 })
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
