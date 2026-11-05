package runner

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

type delayedStartedCoordinator struct {
	*fakeCoordinator
	mu      sync.Mutex
	times   []time.Time
	entered chan struct{}
}

func (c *delayedStartedCoordinator) Started(ctx context.Context, runnerID string, runID uuid.UUID, envID string, at time.Time) error {
	c.mu.Lock()
	c.times = append(c.times, at)
	first := len(c.times) == 1
	c.mu.Unlock()
	if first {
		close(c.entered)
		<-ctx.Done()
		return ctx.Err()
	}
	return c.fakeCoordinator.Started(ctx, runnerID, runID, envID, at)
}

func TestStartedReportDoesNotDelaySupervision(t *testing.T) {
	for _, status := range []string{runproto.StatusCompleted, runproto.StatusCancelled, runproto.StatusTimedOut} {
		t.Run(status, func(t *testing.T) {
			h := newHarness(t, nil)
			coord := &delayedStartedCoordinator{fakeCoordinator: h.coord, entered: make(chan struct{})}
			h.r.coord = coord
			m := pyManifest()
			if status == runproto.StatusTimedOut {
				m.Timeout = 50 * time.Millisecond
			}
			a := newAssignment(m)
			h.ex.behave = func(c *fakeContainer) {
				<-coord.entered
				if status == runproto.StatusCompleted {
					c.exit(0, false)
					return
				}
				if status == runproto.StatusCancelled {
					h.coord.requestCancel(a.RunID)
				}
				c.exitOnStop()
			}
			h.coord.enqueue(a, mainPy)
			h.start()

			f := h.coord.waitFinish(t, a.RunID, time.Second)
			if f.Status != status {
				t.Fatalf("Finish = %+v, want %s", f, status)
			}
			coord.mu.Lock()
			times := append([]time.Time(nil), coord.times...)
			coord.mu.Unlock()
			if len(times) != 2 || !times[0].Equal(times[1]) {
				t.Fatalf("start reports = %v; want the original start time retried", times)
			}
			if h.log.index("started") < 0 || h.log.index("started") > h.log.index("finish") {
				t.Fatalf("Finish preceded the recovered start report: %v", h.log.snapshot())
			}
			h.coord.checkProtocol(t)
		})
	}
}

type failingStartedCoordinator struct {
	*fakeCoordinator
	calls    int
	rejected chan struct{}
}

func (c *failingStartedCoordinator) Started(ctx context.Context, runnerID string, runID uuid.UUID, envID string, at time.Time) error {
	c.calls++
	if c.calls <= 3 {
		if c.calls == 3 {
			close(c.rejected)
		}
		return errors.New("start report temporarily unavailable")
	}
	return c.fakeCoordinator.Started(ctx, runnerID, runID, envID, at)
}

func TestFinishRecoversFailedStartedReport(t *testing.T) {
	h := newHarness(t, nil)
	coord := &failingStartedCoordinator{fakeCoordinator: h.coord, rejected: make(chan struct{})}
	h.r.coord = coord
	h.ex.behave = func(c *fakeContainer) {
		<-coord.rejected
		c.exit(0, false)
	}
	a := newAssignment(pyManifest())
	h.coord.enqueue(a, mainPy)
	h.start()

	f := h.coord.waitFinish(t, a.RunID, time.Second)
	if f.Status != runproto.StatusCompleted {
		t.Fatalf("Finish = %+v", f)
	}
	if h.log.index("started") < 0 || h.log.index("started") > h.log.index("finish") {
		t.Fatalf("Finish preceded the recovered start report: %v", h.log.snapshot())
	}
	h.coord.checkProtocol(t)
}
