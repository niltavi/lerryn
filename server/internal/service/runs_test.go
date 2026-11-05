package service_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestStartRun(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	m := basicManifest()
	m.Params = map[string]any{"source": "crm"}
	m.Concurrency = service.ConcurrencyAllow
	m.Stages = []service.StageSpec{{ID: "fetch", Name: "获取"}}
	task := f.apply(alice, "digest", m)
	m.Params = map[string]any{"source": "erp"}
	f.apply(alice, "digest", m)

	latest, err := f.runs.Start(f.ctx, alice, task.Task.ID, service.StartRunInput{})
	if err != nil {
		t.Fatal(err)
	}
	r := latest.Run
	if r.Status != store.RunQueued || r.VersionSeq != 2 || r.IsTest || r.TriggerKind != "manual" || r.CreatedBy != alice.UserID {
		t.Fatalf("run = %+v", r)
	}
	if r.Params != `{"source":"erp"}` || r.ConcurrencyKey != "" {
		t.Fatalf("params %s, key %q", r.Params, r.ConcurrencyKey)
	}
	if len(latest.Stages) != 1 || latest.Stages[0].Status != service.StagePending {
		t.Fatalf("stages = %+v", latest.Stages)
	}

	one := 1
	pinned, err := f.runs.Start(f.ctx, alice, task.Task.ID, service.StartRunInput{
		Version: &one, Params: map[string]any{"override": true}, Test: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Run.VersionSeq != 1 || pinned.Run.Params != `{"override":true}` || !pinned.Run.IsTest {
		t.Fatalf("pinned = %+v", pinned.Run)
	}
	// An explicit empty object replaces the manifest's params too.
	empty, err := f.runs.Start(f.ctx, alice, task.Task.ID, service.StartRunInput{Params: map[string]any{}})
	if err != nil || empty.Run.Params != "{}" {
		t.Fatalf("empty params = %q, %v", empty.Run.Params, err)
	}

	for _, v := range []int{0, 3} {
		_, err := f.runs.Start(f.ctx, alice, task.Task.ID, service.StartRunInput{Version: &v})
		if fields, _ := fieldErrors(t, err); fields["#/version"] == "" {
			t.Fatalf("version %d: %v", v, fields)
		}
	}
	_, err = f.runs.Start(f.ctx, alice, task.Task.ID, service.StartRunInput{Params: map[string]any{"x": string(make([]byte, 16<<10))}})
	if fields, content := fieldErrors(t, err); fields["#/params"] == "" || !content {
		t.Fatalf("large params: %v", fields)
	}
	n, err := f.db.NewSelect().Model((*store.AuditEvent)(nil)).Where("action = ?", "run.start").Count(f.ctx)
	if err != nil || n != 3 {
		t.Fatalf("run.start audit events = %d, %v", n, err)
	}
}

func TestStartRunForbidsOverlap(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "single", basicManifest()) // concurrency defaults to forbid
	first := f.start(alice, task.Task.ID)
	if first.ConcurrencyKey != task.Task.ID.String() {
		t.Fatalf("concurrency key = %q", first.ConcurrencyKey)
	}
	_, err := f.runs.Start(f.ctx, alice, task.Task.ID, service.StartRunInput{})
	wantErr(t, err, service.ErrRunInProgress)
	var inProgress *service.RunInProgressError
	if !errors.As(err, &inProgress) || inProgress.RunID != first.ID {
		t.Fatalf("err = %#v, want the active run %s", err, first.ID)
	}

	// Once the first run ends, the task can run again.
	if _, err := f.runs.Cancel(f.ctx, alice, first.ID); err != nil {
		t.Fatal(err)
	}
	f.start(alice, task.Task.ID)

	allow := basicManifest()
	allow.Concurrency = service.ConcurrencyAllow
	parallel := f.apply(alice, "parallel", allow)
	f.start(alice, parallel.Task.ID)
	f.start(alice, parallel.Task.ID)
}

func TestCancelRun(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	allow := basicManifest()
	allow.Concurrency = service.ConcurrencyAllow
	task := f.apply(alice, "cancel-me", allow)
	runner := f.runner("runner-1")

	// Queued: cancelled at once.
	queued := f.start(alice, task.Task.ID)
	d, err := f.runs.Cancel(f.ctx, alice, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Run.Status != store.RunCancelled || d.Run.CancelRequestedAt.IsZero() || d.Run.FinishedAt.IsZero() {
		t.Fatalf("queued run after cancel = %+v", d.Run)
	}

	// Preparing and running: cancelling until the runner confirms.
	preparing := f.start(alice, task.Task.ID)
	running := f.start(alice, task.Task.ID)
	if got := f.claim(runner, 2); len(got) != 2 {
		t.Fatalf("claimed %d", len(got))
	}
	if err := f.coord.Started(f.ctx, runner, running.ID, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{preparing.ID, running.ID} {
		d, err := f.runs.Cancel(f.ctx, alice, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Run.Status != store.RunCancelling || d.Run.CancelRequestedAt.IsZero() || !d.Run.FinishedAt.IsZero() {
			t.Fatalf("run after cancel = %+v", d.Run)
		}
	}
	// Cancelling again changes nothing.
	again, err := f.runs.Cancel(f.ctx, alice, running.ID)
	if err != nil || again.Run.Status != store.RunCancelling {
		t.Fatalf("second cancel = %s, %v", again.Run.Status, err)
	}

	// The runner learns of it from its heartbeat and confirms.
	reply, err := f.coord.Heartbeat(f.ctx, runner, []uuid.UUID{preparing.ID, running.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reply.Cancel, []uuid.UUID{preparing.ID, running.ID}) || len(reply.Lost) != 0 {
		t.Fatalf("heartbeat = %+v", reply)
	}
	for _, id := range []uuid.UUID{preparing.ID, running.ID} {
		if err := f.coord.Finish(f.ctx, runner, id, runproto.Finish{Status: runproto.StatusCancelled}); err != nil {
			t.Fatal(err)
		}
		d := f.get(alice, id)
		if d.Run.Status != store.RunCancelled || d.Run.ErrorCode != "" {
			t.Fatalf("finished run = %+v", d.Run)
		}
	}

	// Terminal runs stay as they are.
	ended, err := f.runs.Cancel(f.ctx, alice, running.ID)
	if err != nil || ended.Run.Status != store.RunCancelled {
		t.Fatalf("cancel of an ended run = %s, %v", ended.Run.Status, err)
	}
	n, err := f.db.NewSelect().Model((*store.AuditEvent)(nil)).Where("action = ?", "run.cancel").Count(f.ctx)
	if err != nil || n != 5 {
		t.Fatalf("run.cancel audit events = %d, %v", n, err)
	}
}

func TestRunLogs(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "chatty", basicManifest())
	run := f.start(alice, task.Task.ID)
	runner := f.runner("runner-1")
	f.claim(runner, 1)
	if err := f.coord.Started(f.ctx, runner, run.ID, "", time.Now()); err != nil {
		t.Fatal(err)
	}

	page, err := f.runs.Logs(f.ctx, alice, run.ID, service.LogsInput{})
	if err != nil || len(page.Lines) != 0 || page.Complete || page.NextCursor == "" {
		t.Fatalf("empty log = %+v, %v", page, err)
	}
	for from := int64(1); from <= 450; from += 150 {
		if err := f.coord.AppendLogs(f.ctx, runner, run.ID, f.logLines(from, 150, runproto.StreamStdout), runproto.Dropped{Lines: 3, Bytes: 30}); err != nil {
			t.Fatal(err)
		}
	}

	// Page through with cursors: 200, 200, 50, then nothing new.
	var seqs []int64
	cursor := ""
	for _, want := range []int{200, 200, 50, 0} {
		page, err := f.runs.Logs(f.ctx, alice, run.ID, service.LogsInput{Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Lines) != want || page.Complete || page.DroppedLines != 3 {
			t.Fatalf("page of %d lines, complete %v, dropped %d; want %d", len(page.Lines), page.Complete, page.DroppedLines, want)
		}
		for _, l := range page.Lines {
			seqs = append(seqs, l.Seq)
		}
		if want == 0 && page.NextCursor != cursor {
			t.Fatal("an empty page must hand back the cursor it was given")
		}
		cursor = page.NextCursor
	}
	for i, s := range seqs {
		if s != int64(i+1) {
			t.Fatalf("line %d has seq %d", i, s)
		}
	}

	limit := 7
	page, err = f.runs.Logs(f.ctx, alice, run.ID, service.LogsInput{Limit: &limit})
	if err != nil || len(page.Lines) != 7 || page.Lines[6].Text != "line 7" || page.Lines[0].Stream != "stdout" {
		t.Fatalf("limited page = %+v, %v", page.Lines, err)
	}

	tail := 10
	page, err = f.runs.Logs(f.ctx, alice, run.ID, service.LogsInput{Tail: &tail})
	if err != nil || len(page.Lines) != 10 || page.Lines[0].Seq != 441 || page.Lines[9].Seq != 450 || page.Complete {
		t.Fatalf("tail = %d lines from %d, complete %v, %v", len(page.Lines), page.Lines[0].Seq, page.Complete, err)
	}
	tailCursor := page.NextCursor

	// The run ends; the last page is complete.
	if err := f.coord.AppendLogs(f.ctx, runner, run.ID, f.logLines(451, 1, runproto.StreamStderr), runproto.Dropped{Lines: 3, Bytes: 30}); err != nil {
		t.Fatal(err)
	}
	zero := 0
	f.putEmptyState(runner, run.ID)
	if err := f.coord.Finish(f.ctx, runner, run.ID, runproto.Finish{Status: runproto.StatusCompleted, ExitCode: &zero}); err != nil {
		t.Fatal(err)
	}
	page, err = f.runs.Logs(f.ctx, alice, run.ID, service.LogsInput{Cursor: tailCursor})
	if err != nil || len(page.Lines) != 1 || page.Lines[0].Seq != 451 || page.Lines[0].Stream != "stderr" || !page.Complete {
		t.Fatalf("final page = %+v, %v", page, err)
	}
	page, err = f.runs.Logs(f.ctx, alice, run.ID, service.LogsInput{Tail: &tail})
	if err != nil || !page.Complete {
		t.Fatalf("tail of an ended run: complete %v, %v", page.Complete, err)
	}
	page, err = f.runs.Logs(f.ctx, alice, run.ID, service.LogsInput{})
	if err != nil || page.Complete {
		t.Fatalf("first page of an ended run must not be complete: %v, %v", page.Complete, err)
	}

	big, bad := 1001, 0
	for _, tt := range []struct {
		in   service.LogsInput
		want string
	}{
		{service.LogsInput{Tail: &tail, Cursor: tailCursor}, "?tail"},
		{service.LogsInput{Cursor: "not a cursor"}, "?cursor"},
		{service.LogsInput{Cursor: "bA"}, "?cursor"},
		{service.LogsInput{Limit: &big}, "?limit"},
		{service.LogsInput{Tail: &bad}, "?tail"},
	} {
		_, err := f.runs.Logs(f.ctx, alice, run.ID, tt.in)
		fields, content := fieldErrors(t, err)
		if _, ok := fields[tt.want]; !ok || content {
			t.Errorf("%+v: errors %v, content %v; want %s", tt.in, fields, content, tt.want)
		}
	}
}

func TestWait(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "slow", basicManifest())
	run := f.start(alice, task.Task.ID)
	runner := f.runner("runner-1")
	f.claim(runner, 1)

	// Times out while the run is active.
	start := time.Now()
	d, err := f.runs.Wait(f.ctx, alice, run.ID, 150*time.Millisecond)
	if err != nil || d.Run.Status != store.RunPreparing || time.Since(start) < 150*time.Millisecond {
		t.Fatalf("wait on an active run = %s after %s, %v", d.Run.Status, time.Since(start), err)
	}

	// Wakes as soon as the run ends, well before the poll interval.
	done := make(chan service.RunDetail, 1)
	go func() {
		d, err := f.runs.Wait(f.ctx, alice, run.ID, 30*time.Second)
		if err != nil {
			t.Error(err)
		}
		done <- d
	}()
	time.Sleep(50 * time.Millisecond)
	exit := 3
	finished := time.Now()
	if err := f.coord.Finish(f.ctx, runner, run.ID, runproto.Finish{Status: runproto.StatusFailed, ExitCode: &exit}); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-done:
		if d.Run.Status != store.RunFailed || d.Run.ErrorCode != "script_error" || *d.Run.ExitCode != 3 {
			t.Fatalf("waited run = %+v", d.Run)
		}
		if waited := time.Since(finished); waited > 900*time.Millisecond {
			t.Fatalf("wait returned %s after the run ended", waited)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not return after the run ended")
	}

	// An ended run returns at once.
	start = time.Now()
	if _, err := f.runs.Wait(f.ctx, alice, run.ID, service.MaxRunWait); err != nil || time.Since(start) > time.Second {
		t.Fatalf("wait on an ended run took %s, %v", time.Since(start), err)
	}
	_, err = f.runs.Wait(f.ctx, alice, run.ID, service.MaxRunWait+time.Second)
	if fields, _ := fieldErrors(t, err); fields["?wait"] == "" {
		t.Fatalf("wait above the maximum: %v", fields)
	}

	// A run that ends on another server, whose coordinator cannot wake
	// this process, is noticed by polling.
	elsewhere := service.NewCoordinator(f.db, f.blobs, nil, nil)
	other := f.start(alice, task.Task.ID)
	f.claim(runner, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		if err := elsewhere.Finish(f.ctx, runner, other.ID, runproto.Finish{Status: runproto.StatusInterrupted}); err != nil {
			t.Error(err)
		}
	}()
	start = time.Now()
	d, err = f.runs.Wait(f.ctx, alice, other.ID, 10*time.Second)
	if err != nil || d.Run.Status != store.RunInterrupted || time.Since(start) > 3*time.Second {
		t.Fatalf("wait for a run ended elsewhere = %s after %s, %v", d.Run.Status, time.Since(start), err)
	}
}

func TestReconcileInterruptsLostRuns(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	allow := basicManifest()
	allow.Concurrency = service.ConcurrencyAllow
	task := f.apply(alice, "lost", allow)
	lost, healthy := f.start(alice, task.Task.ID), f.start(alice, task.Task.ID)
	runner := f.runner("runner-1")
	f.claim(runner, 2)

	if ids, err := f.runs.Reconcile(f.ctx); err != nil || len(ids) != 0 {
		t.Fatalf("reconcile with live leases: %v, %v", ids, err)
	}
	if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).
		Set("lease_expires_at = ?", store.Now().Add(-time.Minute)).Where("id = ?", lost.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	ids, err := f.runs.Reconcile(f.ctx)
	if err != nil || !slices.Equal(ids, []uuid.UUID{lost.ID}) {
		t.Fatalf("reconciled %v, %v", ids, err)
	}
	d := f.get(alice, lost.ID)
	if d.Run.Status != store.RunInterrupted || d.Run.ErrorCode != "run_interrupted" || d.Run.ErrorMessage == "" || d.Run.FinishedAt.IsZero() {
		t.Fatalf("lost run = %+v", d.Run)
	}
	if got := f.get(alice, healthy.ID); got.Run.Status != store.RunPreparing {
		t.Fatalf("healthy run = %s", got.Run.Status)
	}

	// The runner is told to stop the lost run and its reports are refused.
	reply, err := f.coord.Heartbeat(f.ctx, runner, []uuid.UUID{lost.ID, healthy.ID})
	if err != nil || !slices.Equal(reply.Lost, []uuid.UUID{lost.ID}) || len(reply.Cancel) != 0 {
		t.Fatalf("heartbeat = %+v, %v", reply, err)
	}
	wantErr(t, f.coord.AppendLogs(f.ctx, runner, lost.ID, f.logLines(1, 1, runproto.StreamStdout), runproto.Dropped{}), runproto.ErrLeaseLost)
	wantErr(t, f.coord.Finish(f.ctx, runner, lost.ID, runproto.Finish{Status: runproto.StatusCompleted}), runproto.ErrLeaseLost)
}

func TestRunAccess(t *testing.T) {
	f := newFixture(t)
	alice, bob, viewer := f.user("alice"), f.user("bob"), f.user("viewer")
	f.grant(alice.PersonalProjectID, viewer, service.RoleViewer)
	task := f.apply(alice, "private", basicManifest())
	run := f.start(alice, task.Task.ID)

	_, err := f.runs.Start(f.ctx, bob, task.Task.ID, service.StartRunInput{})
	wantErr(t, err, service.ErrNotFound)
	_, err = f.runs.Get(f.ctx, bob, run.ID)
	wantErr(t, err, service.ErrNotFound)
	_, err = f.runs.Wait(f.ctx, bob, run.ID, time.Second)
	wantErr(t, err, service.ErrNotFound)
	_, err = f.runs.Cancel(f.ctx, bob, run.ID)
	wantErr(t, err, service.ErrNotFound)
	_, err = f.runs.Logs(f.ctx, bob, run.ID, service.LogsInput{})
	wantErr(t, err, service.ErrNotFound)
	_, err = f.runs.Get(f.ctx, alice, store.NewID())
	wantErr(t, err, service.ErrNotFound)
	_, err = f.runs.Start(f.ctx, alice, store.NewID(), service.StartRunInput{})
	wantErr(t, err, service.ErrNotFound)

	if _, err := f.runs.Get(f.ctx, viewer, run.ID); err != nil {
		t.Fatalf("viewer get: %v", err)
	}
	if _, err := f.runs.Logs(f.ctx, viewer, run.ID, service.LogsInput{}); err != nil {
		t.Fatalf("viewer logs: %v", err)
	}
	_, err = f.runs.Cancel(f.ctx, viewer, run.ID)
	wantErr(t, err, service.ErrForbidden)
	_, err = f.runs.Start(f.ctx, viewer, task.Task.ID, service.StartRunInput{})
	wantErr(t, err, service.ErrForbidden)
	if got := f.get(alice, run.ID); got.Run.Status != store.RunQueued {
		t.Fatalf("a refused cancel changed the run to %s", got.Run.Status)
	}
}

func TestResultIsKeptAsReported(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "result", basicManifest())
	run := f.start(alice, task.Task.ID)
	runner := f.runner("runner-1")
	f.claim(runner, 1)
	data := json.RawMessage(`{"count":9007199254740993}`)
	if err := f.coord.AppendEvents(f.ctx, runner, run.ID, []runproto.Event{
		{Seq: 1, Kind: runproto.EventResult, Result: &runproto.Result{Status: "partial", Summary: "half", Data: data}},
		{Seq: 2, Kind: runproto.EventResult, Result: &runproto.Result{Status: "ok", Summary: "done"}},
	}); err != nil {
		t.Fatal(err)
	}
	d := f.get(alice, run.ID)
	if d.Result == nil || d.Result.Status != "ok" || d.Result.Summary != "done" || string(d.Result.Data) != "null" {
		t.Fatalf("result = %+v", d.Result)
	}
	if err := f.coord.AppendEvents(f.ctx, runner, run.ID, []runproto.Event{
		{Seq: 3, Kind: runproto.EventResult, Result: &runproto.Result{Status: "partial", Summary: "again", Data: data}},
	}); err != nil {
		t.Fatal(err)
	}
	// Large integers in the data survive unchanged.
	if d := f.get(alice, run.ID); string(d.Result.Data) != string(data) {
		t.Fatalf("data = %s", d.Result.Data)
	}
}
