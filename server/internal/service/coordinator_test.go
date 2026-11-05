package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// TestCoordinatorFlow walks one run through the whole protocol, as the
// embedded runner drives it.
func TestCoordinatorFlow(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	m := basicManifest()
	m.Params = map[string]any{"source": "crm"}
	m.Stages = []service.StageSpec{
		{ID: "fetch", Name: "获取输入"},
		{ID: "summarize", Name: "调用模型", After: []string{"fetch"}},
	}
	task := f.apply(alice, "digest", m,
		service.FileInput{Path: "main.py", Content: []byte("print(1)\n")},
		service.FileInput{Path: "bin/run.sh", Content: []byte("#!/bin/sh\n"), Executable: true},
	)
	started, err := f.runs.Start(f.ctx, alice, task.Task.ID, service.StartRunInput{Params: map[string]any{"source": "erp"}})
	if err != nil {
		t.Fatal(err)
	}
	runID := started.Run.ID
	runner := f.runner("runner-1")

	// Claim hands out the run with the manifest's defaults filled in.
	got := f.claim(runner, 4)
	if len(got) != 1 {
		t.Fatalf("claimed %d runs", len(got))
	}
	a := got[0]
	if a.RunID != runID || a.TaskID != task.Task.ID || a.VersionID != task.Version.Version.ID || string(a.Params) != `{"source":"erp"}` {
		t.Fatalf("assignment = %+v", a)
	}
	spec := a.Manifest
	if spec.Runtime.Language != "python" || spec.Runtime.Version != "3.12" || !slices.Equal(spec.Entrypoint, []string{"python", "main.py"}) ||
		spec.Timeout != 10*time.Minute || spec.MilliCPUs != 1000 || spec.MemoryBytes != 1<<30 || spec.Network != "full" ||
		len(spec.Stages) != 2 || spec.Stages[1].After[0] != "fetch" {
		t.Fatalf("manifest = %+v", spec)
	}
	if got := f.claim(runner, 4); len(got) != 0 {
		t.Fatalf("second claim returned %d runs", len(got))
	}
	if d := f.get(alice, runID); d.Run.Status != store.RunPreparing || d.Run.RunnerID != runner {
		t.Fatalf("claimed run = %+v", d.Run)
	}

	files, err := f.coord.Files(f.ctx, a.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "bin/run.sh" || !files[0].Executable || string(files[1].Content) != "print(1)\n" || files[1].Executable {
		t.Fatalf("files = %+v", files)
	}

	at := time.Date(2026, 11, 3, 1, 2, 3, 456789123, time.FixedZone("CST", 8*3600))
	if err := f.coord.Started(f.ctx, runner, runID, "env-abc", at); err != nil {
		t.Fatal(err)
	}
	if d := f.get(alice, runID); d.Run.Status != store.RunRunning || d.Run.EnvID != "env-abc" || !d.Run.StartedAt.Equal(at.Truncate(time.Microsecond)) {
		t.Fatalf("started run = %+v", d.Run)
	}
	// A repeated Started keeps the first start time.
	if err := f.coord.Started(f.ctx, runner, runID, "env-abc", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if d := f.get(alice, runID); !d.Run.StartedAt.Equal(at.Truncate(time.Microsecond)) {
		t.Fatalf("start time moved to %v", d.Run.StartedAt)
	}

	reply, err := f.coord.Heartbeat(f.ctx, runner, []uuid.UUID{runID, runID})
	if err != nil || len(reply.Cancel) != 0 || len(reply.Lost) != 0 {
		t.Fatalf("heartbeat = %+v, %v", reply, err)
	}

	// Logs: consecutive, resendable, and gaps are refused.
	batch1 := f.logLines(1, 3, runproto.StreamStdout)
	batch1[1].Stage = "fetch"
	if err := f.coord.AppendLogs(f.ctx, runner, runID, batch1, runproto.Dropped{}); err != nil {
		t.Fatal(err)
	}
	if err := f.coord.AppendLogs(f.ctx, runner, runID, batch1, runproto.Dropped{}); err != nil {
		t.Fatalf("resent batch: %v", err)
	}
	// Overlapping batch: lines 2 and 3 are stored already, 4 and 5 are new.
	if err := f.coord.AppendLogs(f.ctx, runner, runID, f.logLines(2, 4, runproto.StreamStderr), runproto.Dropped{Lines: 2, Bytes: 10}); err != nil {
		t.Fatal(err)
	}
	wantErr(t, f.coord.AppendLogs(f.ctx, runner, runID, f.logLines(7, 1, runproto.StreamStdout), runproto.Dropped{}), service.ErrProtocol)
	gappy := append(f.logLines(6, 1, runproto.StreamStdout), f.logLines(8, 1, runproto.StreamStdout)...)
	wantErr(t, f.coord.AppendLogs(f.ctx, runner, runID, gappy, runproto.Dropped{}), service.ErrProtocol)
	// An older running total of dropped lines does not lower the count.
	if err := f.coord.AppendLogs(f.ctx, runner, runID, nil, runproto.Dropped{Lines: 1}); err != nil {
		t.Fatal(err)
	}
	page, err := f.runs.Logs(f.ctx, alice, runID, service.LogsInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 5 || page.DroppedLines != 2 || page.Lines[1].Stage != "fetch" || page.Lines[2].Stream != "stdout" || page.Lines[3].Stream != "stderr" {
		t.Fatalf("log page = %+v", page)
	}
	if chunks, err := f.db.NewSelect().Model((*store.LogChunk)(nil)).Where("run_id = ?", runID).Count(f.ctx); err != nil || chunks != 2 {
		t.Fatalf("log chunks = %d, %v; want one per batch with new lines", chunks, err)
	}

	// Events: the same rules, and they shape the stages and result.
	events := []runproto.Event{
		{Seq: 1, TS: at, Kind: runproto.EventStageStart, Stage: "fetch", Message: "fetching"},
		{Seq: 2, TS: at.Add(time.Second), Kind: runproto.EventStageDone, Stage: "fetch"},
		{Seq: 3, TS: at.Add(2 * time.Second), Kind: runproto.EventStageStart, Stage: "extra", Message: "not declared"},
		{Seq: 4, TS: at.Add(3 * time.Second), Kind: runproto.EventResult, Result: &runproto.Result{Status: "ok", Summary: "120 条", Data: json.RawMessage(`{"n":120}`)}},
	}
	if err := f.coord.AppendEvents(f.ctx, runner, runID, events[:2]); err != nil {
		t.Fatal(err)
	}
	if err := f.coord.AppendEvents(f.ctx, runner, runID, events); err != nil {
		t.Fatalf("overlapping events: %v", err)
	}
	if err := f.coord.AppendEvents(f.ctx, runner, runID, events); err != nil {
		t.Fatalf("resent events: %v", err)
	}
	wantErr(t, f.coord.AppendEvents(f.ctx, runner, runID, []runproto.Event{{Seq: 6, Kind: runproto.EventStageDone, Stage: "extra"}}), service.ErrProtocol)
	// The server's own events (status changes, log truncation) share the
	// numbering but are not the runner's.
	if n, err := f.db.NewSelect().Model((*store.RunEvent)(nil)).
		Where("run_id = ? AND kind IN (?, ?, ?)", runID, store.EventStageStart, store.EventStageDone, store.EventResult).Count(f.ctx); err != nil || n != 4 {
		t.Fatalf("stored runner events = %d, %v", n, err)
	}

	zero := 0
	f.putEmptyState(runner, runID)
	if err := f.coord.Finish(f.ctx, runner, runID, runproto.Finish{Status: runproto.StatusCompleted, ExitCode: &zero, ErrorCode: "script_error"}); err != nil {
		t.Fatal(err)
	}
	d := f.get(alice, runID)
	if d.Run.Status != store.RunCompleted || d.Run.ExitCode == nil || *d.Run.ExitCode != 0 || d.Run.ErrorCode != "" || d.Run.FinishedAt.IsZero() {
		t.Fatalf("finished run = %+v", d.Run)
	}
	wantStages := []service.StageState{
		{ID: "fetch", Name: "获取输入", Declared: true, After: []string{}, Status: service.StageDone, StartedAt: at.UTC().Truncate(time.Microsecond), FinishedAt: at.Add(time.Second).UTC().Truncate(time.Microsecond), Message: "fetching"},
		{ID: "summarize", Name: "调用模型", Declared: true, After: []string{"fetch"}, Status: service.StagePending},
		{ID: "extra", Name: "extra", After: []string{}, Status: service.StageRunning, StartedAt: at.Add(2 * time.Second).UTC().Truncate(time.Microsecond), Message: "not declared"},
	}
	if len(d.Stages) != len(wantStages) {
		t.Fatalf("stages = %+v", d.Stages)
	}
	for i, s := range d.Stages {
		w := wantStages[i]
		if s.ID != w.ID || s.Name != w.Name || s.Declared != w.Declared || s.After == nil || !slices.Equal(s.After, w.After) || s.Status != w.Status || !s.StartedAt.Equal(w.StartedAt) || !s.FinishedAt.Equal(w.FinishedAt) || s.Message != w.Message {
			t.Errorf("stage %d = %+v, want %+v", i, s, w)
		}
	}
	if d.Result == nil || d.Result.Status != "ok" || d.Result.Summary != "120 条" || string(d.Result.Data) != `{"n":120}` {
		t.Fatalf("result = %+v", d.Result)
	}

	// After the end nothing more is accepted from the runner.
	wantErr(t, f.coord.AppendLogs(f.ctx, runner, runID, f.logLines(6, 1, runproto.StreamStdout), runproto.Dropped{}), runproto.ErrLeaseLost)
	wantErr(t, f.coord.AppendEvents(f.ctx, runner, runID, events), runproto.ErrLeaseLost)
	wantErr(t, f.coord.Started(f.ctx, runner, runID, "", time.Now()), runproto.ErrLeaseLost)
	wantErr(t, f.coord.Finish(f.ctx, runner, runID, runproto.Finish{Status: runproto.StatusCompleted}), runproto.ErrLeaseLost)
	reply, err = f.coord.Heartbeat(f.ctx, runner, []uuid.UUID{runID})
	if err != nil || !slices.Equal(reply.Lost, []uuid.UUID{runID}) {
		t.Fatalf("heartbeat after the end = %+v, %v", reply, err)
	}
}

func TestCoordinatorRejectsOtherRunners(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	allow := basicManifest()
	allow.Concurrency = service.ConcurrencyAllow
	task := f.apply(alice, "mine", allow)
	claimed, queued := f.start(alice, task.Task.ID), f.start(alice, task.Task.ID)
	owner, other := f.runner("owner"), f.runner("other")
	if got := f.claim(owner, 1); len(got) != 1 || got[0].RunID != claimed.ID {
		t.Fatalf("claim = %+v", got)
	}
	exit := 1
	for _, tt := range []struct {
		runner string
		run    uuid.UUID
	}{{other, claimed.ID}, {owner, queued.ID}, {owner, store.NewID()}} {
		wantErr(t, f.coord.Started(f.ctx, tt.runner, tt.run, "", time.Now()), runproto.ErrLeaseLost)
		wantErr(t, f.coord.AppendLogs(f.ctx, tt.runner, tt.run, f.logLines(1, 1, runproto.StreamStdout), runproto.Dropped{}), runproto.ErrLeaseLost)
		wantErr(t, f.coord.AppendEvents(f.ctx, tt.runner, tt.run, []runproto.Event{{Seq: 1, Kind: runproto.EventStageStart, Stage: "x"}}), runproto.ErrLeaseLost)
		wantErr(t, f.coord.Finish(f.ctx, tt.runner, tt.run, runproto.Finish{Status: runproto.StatusFailed, ExitCode: &exit}), runproto.ErrLeaseLost)
	}
	reply, err := f.coord.Heartbeat(f.ctx, other, []uuid.UUID{claimed.ID})
	if err != nil || !slices.Equal(reply.Lost, []uuid.UUID{claimed.ID}) {
		t.Fatalf("heartbeat of another runner = %+v, %v", reply, err)
	}
	if d := f.get(alice, claimed.ID); d.Run.Status != store.RunPreparing || d.Run.LogLines != 0 {
		t.Fatalf("run changed by another runner: %+v", d.Run)
	}
}

func TestFinishRules(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	allow := basicManifest()
	allow.Concurrency = service.ConcurrencyAllow
	task := f.apply(alice, "finish", allow)
	runner := f.runner("runner-1")
	exit := func(n int) *int { return &n }

	tests := []struct {
		name      string
		start     bool
		finish    runproto.Finish
		wantErr   error
		wantCode  string
		wantStage string
	}{
		{"completed before start", false, runproto.Finish{Status: runproto.StatusCompleted, ExitCode: exit(0)}, service.ErrProtocol, "", ""},
		{"timed out before start", false, runproto.Finish{Status: runproto.StatusTimedOut}, service.ErrProtocol, "", ""},
		{"unknown status", true, runproto.Finish{Status: "succeeded"}, service.ErrProtocol, "", ""},
		{"server-set status", true, runproto.Finish{Status: store.RunSkipped}, service.ErrProtocol, "", ""},
		{"preparation failed", false, runproto.Finish{Status: runproto.StatusFailed, ErrorCode: "dependency_install_failed", ErrorMessage: "uv sync failed"}, nil, "dependency_install_failed", ""},
		{"script error by default", true, runproto.Finish{Status: runproto.StatusFailed, ExitCode: exit(2), ErrorStage: "fetch"}, nil, "script_error", "fetch"},
		{"out of memory", true, runproto.Finish{Status: runproto.StatusFailed, ExitCode: exit(137), ErrorCode: "out_of_memory"}, nil, "out_of_memory", ""},
		{"refused by the runner", false, runproto.Finish{Status: runproto.StatusFailed, ErrorCode: "validation_failed", ErrorMessage: `network "full" is not allowed on this runner`}, nil, "validation_failed", ""},
		{"unknown code", true, runproto.Finish{Status: runproto.StatusFailed, ExitCode: exit(1), ErrorCode: "gremlins"}, nil, "internal_error", ""},
		{"timeout by default", true, runproto.Finish{Status: runproto.StatusTimedOut, ExitCode: exit(143)}, nil, "timeout", ""},
		{"interrupted", true, runproto.Finish{Status: runproto.StatusInterrupted, ErrorMessage: "container vanished"}, nil, "run_interrupted", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := f.start(alice, task.Task.ID)
			if got := f.claim(runner, 1); len(got) != 1 {
				t.Fatalf("claimed %d", len(got))
			}
			if tt.start {
				if err := f.coord.Started(f.ctx, runner, run.ID, "", time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			err := f.coord.Finish(f.ctx, runner, run.ID, tt.finish)
			if tt.wantErr != nil {
				wantErr(t, err, tt.wantErr)
				// Leave the queue empty for the next case.
				if err := f.coord.Finish(f.ctx, runner, run.ID, runproto.Finish{Status: runproto.StatusInterrupted}); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			d := f.get(alice, run.ID)
			if d.Run.Status != tt.finish.Status || d.Run.ErrorCode != tt.wantCode || d.Run.ErrorStage != tt.wantStage || d.Run.ErrorMessage == "" {
				t.Fatalf("run = %+v", d.Run)
			}
			if tt.finish.ErrorMessage != "" && d.Run.ErrorMessage != tt.finish.ErrorMessage {
				t.Fatalf("message = %q", d.Run.ErrorMessage)
			}
		})
	}
}

func TestLogLimits(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "limits", basicManifest())
	run := f.start(alice, task.Task.ID)
	runner := f.runner("runner-1")
	f.claim(runner, 1)

	long := f.logLines(1, 1, runproto.StreamStdout)
	long[0].Text = strings.Repeat("x", runproto.MaxLineBytes+1)
	wantErr(t, f.coord.AppendLogs(f.ctx, runner, run.ID, long, runproto.Dropped{}), service.ErrProtocol)
	wantErr(t, f.coord.AppendLogs(f.ctx, runner, run.ID, f.logLines(1, 1, "stdin"), runproto.Dropped{}), service.ErrProtocol)
	wantErr(t, f.coord.AppendLogs(f.ctx, runner, run.ID, f.logLines(0, 1, runproto.StreamStdout), runproto.Dropped{}), service.ErrProtocol)

	// A run at the line limit takes no more lines.
	if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).Set("log_lines = ?", runproto.MaxRunLines-1).Where("id = ?", run.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	wantErr(t, f.coord.AppendLogs(f.ctx, runner, run.ID, f.logLines(runproto.MaxRunLines, 2, runproto.StreamStdout), runproto.Dropped{}), service.ErrProtocol)
	if err := f.coord.AppendLogs(f.ctx, runner, run.ID, f.logLines(runproto.MaxRunLines, 1, runproto.StreamStdout), runproto.Dropped{}); err != nil {
		t.Fatalf("last line within the limit: %v", err)
	}

	// The byte limit counts what is stored: here 2000 lines of 52 KiB
	// each, short of the limit by 10 bytes.
	if _, err := f.db.NewDelete().Model((*store.LogChunk)(nil)).Where("run_id = ?", run.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.NewInsert().Model(&store.LogChunk{
		ID: store.NewID(), RunID: run.ID, SeqFrom: 1, SeqTo: 2000, Data: "", Bytes: runproto.MaxRunBytes - 10, CreatedAt: store.Now(),
	}).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).Set("log_lines = ?", 2000).Where("id = ?", run.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	over := f.logLines(2001, 1, runproto.StreamStdout)
	over[0].Text = strings.Repeat("y", 11)
	wantErr(t, f.coord.AppendLogs(f.ctx, runner, run.ID, over, runproto.Dropped{}), service.ErrProtocol)
	over[0].Text = strings.Repeat("y", 10)
	if err := f.coord.AppendLogs(f.ctx, runner, run.ID, over, runproto.Dropped{}); err != nil {
		t.Fatalf("bytes up to the limit: %v", err)
	}

	for _, e := range []runproto.Event{
		{Seq: 1, Kind: "stage.skip", Stage: "a"},
		{Seq: 1, Kind: runproto.EventStageStart},
		{Seq: 1, Kind: runproto.EventResult},
		{Seq: 1, Kind: runproto.EventResult, Result: &runproto.Result{Status: "great"}},
		{Seq: 1, Kind: runproto.EventResult, Result: &runproto.Result{Status: "ok", Data: json.RawMessage("{not json")}},
	} {
		wantErr(t, f.coord.AppendEvents(f.ctx, runner, run.ID, []runproto.Event{e}), service.ErrProtocol)
	}
}

func TestRegisterAndClaimChecks(t *testing.T) {
	f := newFixture(t)
	for _, r := range []runproto.Registration{{ID: "", Slots: 1}, {ID: "bad id", Slots: 1}, {ID: "ok", Slots: 0}} {
		wantErr(t, f.coord.Register(f.ctx, r), service.ErrProtocol)
	}
	if err := f.coord.Register(f.ctx, runproto.Registration{ID: "r.1_a-b", Slots: 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.coord.Register(f.ctx, runproto.Registration{ID: "r.1_a-b", Name: "host-2", Slots: 8}); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	var r store.Runner
	if err := f.db.NewSelect().Model(&r).Where("id = ?", "r.1_a-b").Scan(f.ctx); err != nil || r.Slots != 8 || r.Name != "host-2" {
		t.Fatalf("runner = %+v, %v", r, err)
	}
	_, err := f.coord.Claim(f.ctx, "never-registered", 1)
	wantErr(t, err, service.ErrProtocol)
	if got, err := f.coord.Claim(f.ctx, "r.1_a-b", 0); err != nil || len(got) != 0 {
		t.Fatalf("claim of zero = %v, %v", got, err)
	}
}

func TestClaimFailsUnreadableRuns(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "broken", basicManifest())
	run := f.start(alice, task.Task.ID)
	if _, err := f.db.NewUpdate().Model((*store.TaskVersion)(nil)).Set("manifest = ?", "{broken").Where("id = ?", task.Version.Version.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	runner := f.runner("runner-1")
	if got := f.claim(runner, 1); len(got) != 0 {
		t.Fatalf("claimed %+v", got)
	}
	var stored store.Run
	if err := f.db.NewSelect().Model(&stored).Where("id = ?", run.ID).Scan(f.ctx); err != nil {
		t.Fatal(err)
	}
	if stored.Status != store.RunFailed || stored.ErrorCode != "internal_error" {
		t.Fatalf("run = %+v", stored)
	}
}

func TestConcurrentClaimsAcrossRunners(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	allow := basicManifest()
	allow.Concurrency = service.ConcurrencyAllow
	task := f.apply(alice, "fanout", allow)
	const runs, runners = 60, 8
	want := map[uuid.UUID]bool{}
	for range runs {
		want[f.start(alice, task.Task.ID).ID] = true
	}
	var (
		mu   sync.Mutex
		got  = map[uuid.UUID]string{}
		errs []error
		wg   sync.WaitGroup
	)
	for i := range runners {
		id := f.runner("runner-" + string(rune('a'+i)))
		wg.Go(func() {
			for {
				batch, err := f.coord.Claim(context.Background(), id, 1+i%3)
				mu.Lock()
				if err != nil {
					errs = append(errs, err)
				}
				for _, a := range batch {
					if prev, dup := got[a.RunID]; dup {
						errs = append(errs, errors.New("run "+a.RunID.String()+" claimed by "+prev+" and "+id))
					}
					got[a.RunID] = id
				}
				mu.Unlock()
				if err != nil || len(batch) == 0 {
					return
				}
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	if len(got) != runs {
		t.Fatalf("claimed %d of %d runs", len(got), runs)
	}
	for id := range want {
		if _, ok := got[id]; !ok {
			t.Fatalf("run %s never claimed", id)
		}
	}
}
