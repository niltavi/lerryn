package service_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/events"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// finishWithLines runs a task to completion with n log lines, sent in
// batches of 700 so that chunks and archive frames do not line up.
func (f *fixture) finishWithLines(runner string, runID uuid.UUID, n int64) {
	f.t.Helper()
	if err := f.coord.Started(f.ctx, runner, runID, "", time.Now()); err != nil {
		f.t.Fatal(err)
	}
	for from := int64(1); from <= n; from += 700 {
		if err := f.coord.AppendLogs(f.ctx, runner, runID, f.logLines(from, min(700, n-from+1), runproto.StreamStdout), runproto.Dropped{}); err != nil {
			f.t.Fatal(err)
		}
	}
	zero := 0
	f.putEmptyState(runner, runID)
	if err := f.coord.Finish(f.ctx, runner, runID, runproto.Finish{Status: runproto.StatusCompleted, ExitCode: &zero}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) chunkCount(runID uuid.UUID) int {
	f.t.Helper()
	n, err := f.db.NewSelect().Model((*store.LogChunk)(nil)).Where("run_id = ?", runID).Count(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return int(n)
}

func TestLogArchive(t *testing.T) {
	f := newFixture(t)
	f.runs.WithLogArchive(f.blobs)
	alice := f.user("alice")
	allow := basicManifest()
	allow.Concurrency = service.ConcurrencyAllow
	task := f.apply(alice, "t", allow)
	runner := f.runner("r1")

	long := f.start(alice, task.Task.ID)
	empty := f.start(alice, task.Task.ID)
	active := f.start(alice, task.Task.ID)
	f.claim(runner, 3)
	f.finishWithLines(runner, long.ID, 2500)
	f.finishWithLines(runner, empty.ID, 0)
	if err := f.coord.AppendLogs(f.ctx, runner, active.ID, f.logLines(1, 5, runproto.StreamStderr), runproto.Dropped{}); err != nil {
		t.Fatal(err)
	}

	// Reading before the archive and after it gives the same lines.
	before, err := f.runs.Logs(f.ctx, alice, long.ID, service.LogsInput{Limit: new(1000)})
	if err != nil {
		t.Fatal(err)
	}
	n, err := f.runs.ArchiveLogs(f.ctx)
	if err != nil || n != 2 {
		t.Fatalf("archived %d runs, %v; want the two ended runs", n, err)
	}
	if f.chunkCount(long.ID) != 0 || f.chunkCount(empty.ID) != 0 {
		t.Fatal("chunks of archived runs were kept")
	}
	if f.chunkCount(active.ID) == 0 {
		t.Fatal("chunks of an active run were archived")
	}
	if n, err := f.runs.ArchiveLogs(f.ctx); err != nil || n != 0 {
		t.Fatalf("second archive pass archived %d, %v", n, err)
	}

	var seq int64
	cursor := ""
	for page := 0; ; page++ {
		got, err := f.runs.Logs(f.ctx, alice, long.ID, service.LogsInput{Cursor: cursor, Limit: new(333)})
		if err != nil {
			t.Fatal(err)
		}
		if page == 0 && (len(got.Lines) != 333 || got.Lines[0] != before.Lines[0] || got.Lines[332] != before.Lines[332]) {
			t.Fatalf("first archived page differs from the chunked one")
		}
		for _, l := range got.Lines {
			seq++
			if l.Seq != seq || l.Text != "line "+strconv.FormatInt(seq, 10) || l.Stream != runproto.StreamStdout {
				t.Fatalf("line %d = %+v", seq, l)
			}
		}
		cursor = got.NextCursor
		if got.Complete {
			break
		}
		if len(got.Lines) == 0 || page > 20 {
			t.Fatalf("paging stopped at line %d", seq)
		}
	}
	if seq != 2500 {
		t.Fatalf("read %d lines, want 2500", seq)
	}
	tail, err := f.runs.Logs(f.ctx, alice, long.ID, service.LogsInput{Tail: new(3)})
	if err != nil || len(tail.Lines) != 3 || tail.Lines[2].Seq != 2500 || !tail.Complete {
		t.Fatalf("tail = %+v, %v", tail, err)
	}
	if got, err := f.runs.Logs(f.ctx, alice, empty.ID, service.LogsInput{}); err != nil || len(got.Lines) != 0 || !got.Complete {
		t.Fatalf("empty run log = %+v, %v", got, err)
	}
}

// A request waiting on one server wakes as soon as a coordinator on
// another server finishes the run, through the shared bus, without
// waiting for the poll.
func TestWaitWakesAcrossServers(t *testing.T) {
	f := newFixture(t)
	bus := events.NewLocal()
	// Two servers on one database: f.runs and f.coord are server A, other
	// is server B, which only hears about the run through the bus.
	f.runs.WithEvents(bus, true)
	other := service.NewRuns(f.db, service.NewAuthorizer(f.db), nil).WithEvents(bus, true)
	go other.Listen(t.Context())

	alice := f.user("alice")
	task := f.apply(alice, "t", basicManifest())
	run := f.start(alice, task.Task.ID)
	runner := f.runner("r1")
	f.claim(runner, 1)
	if err := f.coord.Started(f.ctx, runner, run.ID, "", time.Now()); err != nil {
		t.Fatal(err)
	}

	done := make(chan service.RunDetail, 1)
	go func() {
		d, err := other.Wait(f.ctx, alice, run.ID, 30*time.Second)
		if err != nil {
			t.Error(err)
		}
		done <- d
	}()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	zero := 0
	f.putEmptyState(runner, run.ID)
	if err := f.coord.Finish(f.ctx, runner, run.ID, runproto.Finish{Status: runproto.StatusCompleted, ExitCode: &zero}); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-done:
		if d.Run.Status != store.RunCompleted {
			t.Fatalf("status = %s", d.Run.Status)
		}
		// The poll with a cross-node bus is 5 seconds.
		if waited := time.Since(start); waited > 2*time.Second {
			t.Fatalf("woke after %s", waited)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not return")
	}
}

// Start announces queued runs, so held claims can wake.
func TestStartPublishesQueued(t *testing.T) {
	f := newFixture(t)
	bus := events.NewLocal()
	f.runs.WithEvents(bus, false)
	ch, stop := bus.Subscribe()
	defer stop()
	alice := f.user("alice")
	task := f.apply(alice, "t", basicManifest())
	run := f.start(alice, task.Task.ID)
	select {
	case e := <-ch:
		if e.Kind != events.RunQueued || e.RunID != run.ID {
			t.Fatalf("event = %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no run.queued event")
	}
	if _, err := f.runs.Cancel(f.ctx, alice, run.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-ch:
		if e.Kind != events.RunEnded || e.RunID != run.ID {
			t.Fatalf("event = %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no run.ended event after cancelling a queued run")
	}
}
