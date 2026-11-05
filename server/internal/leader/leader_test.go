package leader

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/dialect"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

// candidate is an elector run in the background, recording how often it
// led.
type candidate struct {
	e      *Elector
	cancel context.CancelFunc
	done   chan struct{}
	terms  atomic.Int32
	// leading is set while lead runs.
	leading atomic.Bool
}

func startCandidate(t *testing.T, db *store.DB, holder string) *candidate {
	t.Helper()
	e := New(db, holder, nil)
	e.ttl, e.renew = 2*time.Second, 200*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	c := &candidate{e: e, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		e.Run(ctx, func(ctx context.Context) {
			c.terms.Add(1)
			c.leading.Store(true)
			<-ctx.Done()
			c.leading.Store(false)
		})
	}()
	t.Cleanup(c.stop)
	return c
}

func (c *candidate) stop() {
	c.cancel()
	<-c.done
}

func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %s", what, within)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestOneLeaderAndHandover(t *testing.T) {
	db := storetest.Open(t)
	if db.Native().Name() == dialect.SQLiteName {
		c := startCandidate(t, db, "only")
		eventually(t, time.Second, "leading on SQLite", c.leading.Load)
		if !c.e.IsLeader() {
			t.Fatal("IsLeader is false while leading")
		}
		c.stop()
		if c.e.IsLeader() {
			t.Fatal("IsLeader is true after stopping")
		}
		return
	}

	a := startCandidate(t, db, "a")
	eventually(t, 2*time.Second, "a leading", a.leading.Load)
	b := startCandidate(t, db, "b")
	time.Sleep(time.Second)
	if b.leading.Load() || !a.leading.Load() {
		t.Fatalf("a=%v b=%v, want only a", a.leading.Load(), b.leading.Load())
	}

	// A clean shutdown releases the lease; b takes over at its next try,
	// well before the TTL.
	start := time.Now()
	a.stop()
	eventually(t, time.Second, "b leading after a stopped", b.leading.Load)
	if took := time.Since(start); took >= b.e.ttl {
		t.Fatalf("handover took %s, as long as the TTL", took)
	}
}

func TestAllRunsEveryTaskUntilTheContextEnds(t *testing.T) {
	var running atomic.Int32
	task := func(ctx context.Context) {
		running.Add(1)
		<-ctx.Done()
		running.Add(-1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		All(task, task, task)(ctx)
		close(done)
	}()
	eventually(t, time.Second, "three tasks running", func() bool { return running.Load() == 3 })
	cancel()
	<-done
	if n := running.Load(); n != 0 {
		t.Fatalf("%d tasks still running after All returned", n)
	}
}

func TestStepsDownWhenTheLeaseIsTaken(t *testing.T) {
	db := storetest.Open(t)
	if db.Native().Name() == dialect.SQLiteName {
		t.Skip("SQLite has a single server, which always leads")
	}
	a := startCandidate(t, db, "a")
	eventually(t, 2*time.Second, "a leading", a.leading.Load)

	// Another server took the lease, as after a long pause of a.
	if _, err := db.NewUpdate().Model((*store.Lease)(nil)).Set("holder = ?", "intruder").
		Where("name = ?", LeaseName).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, "a stepping down", func() bool { return !a.leading.Load() })
	if a.e.IsLeader() {
		t.Fatal("IsLeader after stepping down")
	}
}

// Two servers competing for a while never lead at the same time.
func TestNeverTwoLeaders(t *testing.T) {
	db := storetest.Open(t)
	if db.Native().Name() == dialect.SQLiteName {
		t.Skip("SQLite has a single server, which always leads")
	}
	var cs []*candidate
	for _, h := range []string{"a", "b", "c"} {
		cs = append(cs, startCandidate(t, db, h))
	}
	deadline := time.Now().Add(1500 * time.Millisecond)
	for i := 0; time.Now().Before(deadline); i++ {
		n := 0
		for _, c := range cs {
			if c.leading.Load() {
				n++
			}
		}
		if n > 1 {
			t.Fatalf("%d leaders at once", n)
		}
		if i == 20 {
			// Stop whoever leads, to force a handover mid-test.
			for _, c := range cs {
				if c.leading.Load() {
					c.stop()
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}
