package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/executor"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

const testTimeout = 10 * time.Second

type harness struct {
	t     *testing.T
	log   *callLog
	coord *fakeCoordinator
	ex    *fakeExecutor
	r     *Runner
	dir   string
}

func newHarness(t *testing.T, mutate func(*Options)) *harness {
	t.Helper()
	log := &callLog{}
	h := &harness{t: t, log: log, coord: newFakeCoordinator(log), ex: newFakeExecutor(log), dir: t.TempDir()}
	opts := Options{
		ID:          "runner-1",
		Name:        "test",
		Slots:       2,
		DataDir:     h.dir,
		PythonImage: "python:test",
		Executor:    h.ex,
		Coordinator: h.coord,
		StopGrace:   50 * time.Millisecond,
		Logger:      slog.New(slog.NewTextHandler(t.Output(), &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if mutate != nil {
		mutate(&opts)
	}
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	r.heartbeatInterval = 10 * time.Millisecond
	r.claimInterval = 5 * time.Millisecond
	r.logFlushInterval = 5 * time.Millisecond
	r.eventPollInterval = 5 * time.Millisecond
	r.retryBase = time.Millisecond
	r.retryMax = 10 * time.Millisecond
	r.reportTimeout = 5 * time.Second
	h.r = r
	h.ex.behave = func(c *fakeContainer) { c.exit(0, false) }
	return h
}

// start runs the runner until the test ends.
func (h *harness) start() (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.r.Run(ctx) }()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				h.t.Errorf("Run() = %v", err)
			}
		case <-time.After(testTimeout):
			h.t.Error("Run did not return after cancel")
		}
	}
	h.t.Cleanup(stop)
	return stop
}

func pyManifest() runproto.Manifest {
	return runproto.Manifest{
		Runtime:     runproto.Runtime{Language: "python", Version: "3.12"},
		Entrypoint:  []string{"python", "main.py"},
		Timeout:     time.Minute,
		MilliCPUs:   500,
		MemoryBytes: 256 << 20,
		Network:     "none",
	}
}

func newAssignment(m runproto.Manifest) runproto.Assignment {
	return runproto.Assignment{
		RunID:     uuid.New(),
		TaskID:    uuid.New(),
		VersionID: uuid.New(),
		Manifest:  m,
		Params:    json.RawMessage(`{"source": "crm"}`),
	}
}

var mainPy = []runproto.File{{Path: "main.py", Content: []byte("print('hi')\n")}}

func TestRunCompleted(t *testing.T) {
	h := newHarness(t, nil)
	h.ex.behave = func(c *fakeContainer) {
		c.stdout("hello\nwor")
		c.stdout("ld\r\n")
		c.event(`{"v":1,"ts":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","kind":"stage.start","stage":"fetch"}`)
		c.stderr("warning: slow\n")
		c.event(`{"v":1,"kind":"stage.done","stage":"fetch","message":"got 3"}`)
		c.event(`{"v":1,"kind":"result","status":"ok","summary":"done","data":{"n": 3}}`)
		c.stdout("no newline at the end")
		c.exit(0, false)
	}
	a := newAssignment(pyManifest())
	h.coord.enqueue(a, mainPy)
	h.start()

	f := h.coord.waitFinish(t, a.RunID, testTimeout)
	if f.Status != runproto.StatusCompleted || f.ExitCode == nil || *f.ExitCode != 0 || f.ErrorCode != "" {
		t.Fatalf("Finish = %+v", f)
	}
	h.coord.checkProtocol(t)

	texts := h.coord.logTexts(a.RunID)
	want := []string{"hello", "world", "warning: slow", "no newline at the end", "process exited with code 0"}
	if !slices.Equal(texts, want) {
		t.Fatalf("log texts = %q, want %q", texts, want)
	}
	lines := h.coord.logLines(a.RunID)
	var labels []string
	for _, l := range lines {
		labels = append(labels, l.Stream+":"+l.Stage)
	}
	// The stage starts after "world" and ends before the last line; output
	// is labelled by the events written before it.
	if want := []string{"stdout:", "stdout:", "stderr:fetch", "stdout:", "system:"}; !slices.Equal(labels, want) {
		t.Fatalf("stream:stage labels = %v, want %v", labels, want)
	}

	events := h.coord.runEvents(a.RunID)
	if len(events) != 3 {
		t.Fatalf("events = %+v", events)
	}
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Errorf("event %d has seq %d", i, e.Seq)
		}
	}
	if events[1].Kind != runproto.EventStageDone || events[1].Message != "got 3" {
		t.Errorf("event 2 = %+v", events[1])
	}
	if r := events[2].Result; r == nil || r.Status != "ok" || string(r.Data) != `{"n":3}` {
		t.Errorf("result event = %+v", events[2])
	}

	// Started before anything ended; container removed after Finish; run
	// directory gone.
	waitFor(t, testTimeout, "container removed", func() bool { return h.ex.wasRemoved("c1") })
	calls := h.log.snapshot()
	iStart, iFinish, iRemove := h.log.index("started"), h.log.index("finish"), h.log.index("remove")
	if iStart < 0 || iFinish < iStart || iRemove < iFinish {
		t.Fatalf("call order = %v", calls)
	}
	waitFor(t, testTimeout, "run directory removed", func() bool {
		_, err := os.Stat(filepath.Join(h.dir, runsDirName, a.RunID.String()))
		return errors.Is(err, os.ErrNotExist)
	})
	waitFor(t, testTimeout, "slot freed", func() bool { return h.r.activeCount() == 0 })
}

func TestTaskSpec(t *testing.T) {
	h := newHarness(t, nil)
	var spec executor.Spec
	var taskFiles []string
	h.ex.behave = func(c *fakeContainer) {
		spec = c.spec
		_ = filepath.WalkDir(c.mountSource(taskMount), func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				fi, _ := d.Info()
				rel, _ := filepath.Rel(c.mountSource(taskMount), p)
				taskFiles = append(taskFiles, rel+" "+fi.Mode().Perm().String())
			}
			return nil
		})
		c.exit(0, false)
	}
	a := newAssignment(pyManifest())
	h.coord.enqueue(a, []runproto.File{
		{Path: "main.py", Content: []byte("print(1)")},
		{Path: "bin/tool.sh", Content: []byte("#!/bin/sh"), Executable: true},
	})
	h.start()
	h.coord.waitFinish(t, a.RunID, testTimeout)

	runDir := filepath.Join(h.dir, runsDirName, a.RunID.String())
	if spec.RunID != a.RunID.String() || spec.RunnerID != "runner-1" || spec.Image != "sha256:fakeimage" {
		t.Errorf("ids/image = %q %q %q", spec.RunID, spec.RunnerID, spec.Image)
	}
	if !slices.Equal(spec.Command, []string{"/sbx/sdk/bin/sbx-exec", "--", "python", "main.py"}) || spec.WorkingDir != "/sbx/task" || spec.Stdin != nil {
		t.Errorf("command = %v in %q", spec.Command, spec.WorkingDir)
	}
	if spec.Network != executor.NetworkNone || spec.Resources.MilliCPUs != 500 || spec.Resources.MemoryBytes != 256<<20 {
		t.Errorf("network/resources = %q %+v", spec.Network, spec.Resources)
	}
	wantEnv := []string{
		"PATH=/sbx/sdk/bin:" + basePath,
		"HOME=/tmp",
		"LERRYN_RUN_ID=" + a.RunID.String(),
		`LERRYN_PARAMS={"source":"crm"}`,
		"LERRYN_EVENTS_FILE=/sbx/events/events.ndjson",
		"LERRYN_LOG_MARKER=" + markerToken(t, spec.Env),
		"LERRYN_SECRETS_DIR=/run/secrets",
		"PYTHONUNBUFFERED=1",
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONPATH=/sbx/sdk",
	}
	if !slices.Equal(spec.Env, wantEnv) {
		t.Errorf("env = %q\nwant %q", spec.Env, wantEnv)
	}
	mounts := map[string]executor.Mount{}
	for _, m := range spec.Mounts {
		mounts[m.Target] = m
	}
	for target, want := range map[string]executor.Mount{
		"/sbx/task":   {Source: filepath.Join(runDir, "task"), ReadOnly: true},
		"/sbx/events": {Source: filepath.Join(runDir, "events")},
		"/sbx/work":   {Source: filepath.Join(runDir, "work")},
		"/sbx/out":    {Source: filepath.Join(runDir, "out")},
	} {
		got := mounts[target]
		if got.Source != want.Source || got.ReadOnly != want.ReadOnly {
			t.Errorf("mount %s = %+v, want %+v", target, got, want)
		}
	}
	if sdk := mounts["/sbx/sdk"]; !sdk.ReadOnly || !strings.HasPrefix(sdk.Source, filepath.Join(h.dir, sdkDirName)+"/") {
		t.Errorf("sdk mount = %+v", sdk)
	}
	if len(spec.Mounts) != 5 {
		t.Errorf("mounts = %+v", spec.Mounts)
	}
	slices.Sort(taskFiles)
	if want := []string{"bin/tool.sh -rwxr-xr-x", "main.py -rw-r--r--"}; !slices.Equal(taskFiles, want) {
		t.Errorf("task files = %q, want %q", taskFiles, want)
	}
}

func TestFinishFromHeartbeatCancel(t *testing.T) {
	h := newHarness(t, nil)
	h.ex.behave = func(c *fakeContainer) {
		c.stdout("working\n")
		c.exitOnStop()
	}
	a := newAssignment(pyManifest())
	h.coord.enqueue(a, mainPy)
	h.start()
	h.coord.waitStarted(t, a.RunID, testTimeout)
	h.coord.requestCancel(a.RunID)

	f := h.coord.waitFinish(t, a.RunID, testTimeout)
	if f.Status != runproto.StatusCancelled || f.ErrorCode != "" || f.ExitCode == nil || *f.ExitCode != 143 {
		t.Fatalf("Finish = %+v", f)
	}
	h.coord.checkProtocol(t)
	if !h.ex.wasStopped("c1") {
		t.Fatal("container was not stopped")
	}
	waitFor(t, testTimeout, "container removed", func() bool { return h.ex.wasRemoved("c1") })
	// Cancelled after the stop was confirmed: the stop precedes Finish.
	if h.log.index("stop c1") > h.log.index("finish") {
		t.Fatalf("Finish before the container stopped: %v", h.log.snapshot())
	}
}

func TestCancelBeforeStart(t *testing.T) {
	h := newHarness(t, nil)
	h.ex.blockImage = true
	a := newAssignment(pyManifest())
	h.coord.enqueue(a, mainPy)
	h.coord.requestCancel(a.RunID)
	h.start()

	f := h.coord.waitFinish(t, a.RunID, testTimeout)
	if f.Status != runproto.StatusCancelled || f.ExitCode != nil {
		t.Fatalf("Finish = %+v", f)
	}
	if specs := h.ex.startedSpecs(); len(specs) != 0 {
		t.Fatalf("containers started: %+v", specs)
	}
}

func TestLostFromHeartbeat(t *testing.T) {
	h := newHarness(t, nil)
	h.ex.behave = func(c *fakeContainer) {
		c.stdout("working\n")
		c.exitOnStop()
	}
	a := newAssignment(pyManifest())
	h.coord.enqueue(a, mainPy)
	h.start()
	h.coord.waitStarted(t, a.RunID, testTimeout)
	h.coord.markLost(a.RunID)

	waitFor(t, testTimeout, "lost run's container removed", func() bool { return h.ex.wasRemoved("c1") })
	waitFor(t, testTimeout, "slot freed", func() bool { return h.r.activeCount() == 0 })
	if !h.ex.wasStopped("c1") {
		t.Fatal("container was not stopped")
	}
	if f, ok := h.coord.finish(a.RunID); ok {
		t.Fatalf("lost run was finished: %+v", f)
	}
	h.coord.checkProtocol(t)
}

func TestFinishMapping(t *testing.T) {
	m := pyManifest()
	m.Timeout = 3 * time.Minute
	m.MemoryBytes = 64 << 20
	now := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	exitAt := now.Add(-time.Second)
	tests := []struct {
		name    string
		res     executor.Result
		stages  []runproto.Event
		want    runproto.Finish
		wantLog string
	}{
		{
			name: "exit 0",
			res:  executor.Result{Outcome: executor.OutcomeExited, Exit: executor.ExitStatus{FinishedAt: exitAt}},
			want: runproto.Finish{Status: runproto.StatusCompleted, ExitCode: ptr(0), FinishedAt: exitAt},
		},
		{
			name:    "exit 0 with a child killed by the OOM killer",
			res:     executor.Result{Outcome: executor.OutcomeExited, Exit: executor.ExitStatus{OOMKilled: true}},
			want:    runproto.Finish{Status: runproto.StatusCompleted, ExitCode: ptr(0), FinishedAt: now},
			wantLog: "killed for exceeding the memory limit of 64 MiB",
		},
		{
			name:   "non-zero exit inside a stage",
			res:    executor.Result{Outcome: executor.OutcomeExited, Exit: executor.ExitStatus{Code: 2}},
			stages: []runproto.Event{{Kind: runproto.EventStageStart, Stage: "fetch"}},
			want: runproto.Finish{Status: runproto.StatusFailed, ExitCode: ptr(2), ErrorCode: "script_error", ErrorStage: "fetch",
				ErrorMessage: "the process exited with code 2", FinishedAt: now},
		},
		{
			name: "non-zero exit after a stage reported failing",
			res:  executor.Result{Outcome: executor.OutcomeExited, Exit: executor.ExitStatus{Code: 1}},
			stages: []runproto.Event{
				{Kind: runproto.EventStageStart, Stage: "fetch"}, {Kind: runproto.EventStageDone, Stage: "fetch"},
				{Kind: runproto.EventStageStart, Stage: "save"}, {Kind: runproto.EventStageFail, Stage: "save"},
			},
			want: runproto.Finish{Status: runproto.StatusFailed, ExitCode: ptr(1), ErrorCode: "script_error", ErrorStage: "save",
				ErrorMessage: "the process exited with code 1", FinishedAt: now},
		},
		{
			name: "non-zero exit outside any stage",
			res:  executor.Result{Outcome: executor.OutcomeExited, Exit: executor.ExitStatus{Code: 1}},
			stages: []runproto.Event{
				{Kind: runproto.EventStageStart, Stage: "fetch"}, {Kind: runproto.EventStageDone, Stage: "fetch"},
			},
			want: runproto.Finish{Status: runproto.StatusFailed, ExitCode: ptr(1), ErrorCode: "script_error",
				ErrorMessage: "the process exited with code 1", FinishedAt: now},
		},
		{
			name: "out of memory",
			res:  executor.Result{Outcome: executor.OutcomeExited, Exit: executor.ExitStatus{Code: 137, OOMKilled: true}},
			want: runproto.Finish{Status: runproto.StatusFailed, ExitCode: ptr(137), ErrorCode: "out_of_memory",
				ErrorMessage: "a process exceeded the memory limit of 64 MiB and was killed; the main process exited with code 137", FinishedAt: now},
		},
		{
			// cgroup v2 can lose the OOM event when the kill ends the
			// container at once; a SIGKILL exit the runner did not cause
			// is reported as out of memory, marked as inferred.
			name: "killed without the OOM flag",
			res:  executor.Result{Outcome: executor.OutcomeExited, Exit: executor.ExitStatus{Code: 137}},
			want: runproto.Finish{Status: runproto.StatusFailed, ExitCode: ptr(137), ErrorCode: "out_of_memory",
				ErrorMessage: "the main process was killed by SIGKILL (exit code 137) without a stop from the runner; " +
					"this is almost always the kernel enforcing the memory limit of 64 MiB", FinishedAt: now},
		},
		{
			name:   "timed out",
			res:    executor.Result{Outcome: executor.OutcomeTimedOut, Exit: executor.ExitStatus{Code: 143}},
			stages: []runproto.Event{{Kind: runproto.EventStageStart, Stage: "summarize"}},
			want: runproto.Finish{Status: runproto.StatusTimedOut, ExitCode: ptr(143), ErrorCode: "timeout", ErrorStage: "summarize",
				ErrorMessage: "the run exceeded its time limit of 3m0s and was stopped", FinishedAt: now},
		},
		{
			name: "cancelled",
			res:  executor.Result{Outcome: executor.OutcomeCanceled, Exit: executor.ExitStatus{Code: 137}, Escalated: true},
			want: runproto.Finish{Status: runproto.StatusCancelled, ExitCode: ptr(137), FinishedAt: now},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stages := &stageTracker{}
			for _, e := range tc.stages {
				stages.apply(e)
			}
			r := &Runner{now: func() time.Time { return now }}
			logs := newLogPipeline(defaultLogLimits(), stages, nil, r.now)
			x := &runExec{r: r, run: &activeRun{a: runproto.Assignment{Manifest: m}}, stages: stages, logs: logs}
			got := *x.finishFor(tc.res)
			if !finishEqual(got, tc.want) {
				t.Fatalf("finishFor() = %+v (exit %v)\nwant %+v (exit %v)", got, deref(got.ExitCode), tc.want, deref(tc.want.ExitCode))
			}
			if tc.wantLog != "" {
				lines, _, _ := logs.batch()
				if !slices.ContainsFunc(lines, func(l runproto.LogLine) bool { return strings.Contains(l.Text, tc.wantLog) }) {
					t.Fatalf("system log %+v lacks %q", lines, tc.wantLog)
				}
			}
		})
	}
}

func ptr(i int) *int { return &i }

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func finishEqual(a, b runproto.Finish) bool {
	if (a.ExitCode == nil) != (b.ExitCode == nil) || (a.ExitCode != nil && *a.ExitCode != *b.ExitCode) {
		return false
	}
	a.ExitCode, b.ExitCode = nil, nil
	return a == b
}

func TestNetworkNotAllowed(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.AllowedNetworks = []string{"none"} })
	m := pyManifest()
	m.Network = "full"
	a := newAssignment(m)
	h.coord.enqueue(a, mainPy)
	h.start()

	f := h.coord.waitFinish(t, a.RunID, testTimeout)
	if f.Status != runproto.StatusFailed || f.ErrorCode != "validation_failed" || f.ExitCode != nil ||
		!strings.Contains(f.ErrorMessage, `network "full" is not allowed`) {
		t.Fatalf("Finish = %+v", f)
	}
	if len(h.ex.startedSpecs()) != 0 {
		t.Fatal("container started for a disallowed network")
	}
	if texts := h.coord.logTexts(a.RunID); len(texts) != 1 || !strings.Contains(texts[0], "not allowed") {
		t.Fatalf("logs = %q", texts)
	}
}

func TestReconcileOnStart(t *testing.T) {
	h := newHarness(t, nil)
	running, installing, finished := uuid.New(), uuid.New(), uuid.New()
	h.ex.leftovers = []executor.Container{
		{ID: "old1", RunID: running.String(), RunnerID: "runner-1", Running: true},
		{ID: "old2", RunID: installing.String() + depsRunSuffix, RunnerID: "runner-1", Running: true},
		{ID: "old3", RunID: finished.String(), RunnerID: "runner-1"},
		{ID: "other", RunID: uuid.NewString(), RunnerID: "runner-2", Running: true},
	}
	// The first listing fails, as when the daemon is still starting.
	h.ex.listErrs = []error{errors.New("daemon not ready")}
	// The third run was already finished before the runner stopped.
	h.coord.finishErr[finished] = runproto.ErrLeaseLost

	// Leftovers on disk from the earlier instance.
	mustMkdir(t, filepath.Join(h.dir, runsDirName, running.String(), "work", "sub"))
	mustMkdir(t, filepath.Join(h.dir, envsDirName, tmpPrefix+"abc"))
	mustMkdir(t, filepath.Join(h.dir, envsDirName, "cached-env"))

	h.start()
	waitFor(t, testTimeout, "first claim", func() bool { return h.log.index("claim") >= 0 })

	for _, id := range []string{"old1", "old2", "old3"} {
		if !h.ex.wasStopped(id) || !h.ex.wasRemoved(id) {
			t.Errorf("leftover %s not stopped and removed", id)
		}
	}
	if h.ex.wasStopped("other") || h.ex.wasRemoved("other") {
		t.Error("touched another runner's container")
	}
	for _, id := range []uuid.UUID{running, installing} {
		f, ok := h.coord.finish(id)
		if !ok || f.Status != runproto.StatusInterrupted || f.ErrorMessage == "" {
			t.Errorf("run %s: Finish = %+v, %v", id, f, ok)
		}
	}
	calls := h.log.snapshot()
	if calls[0] != "register" || h.log.index("claim") < h.log.index("finish") {
		t.Fatalf("call order = %v", calls)
	}
	for _, gone := range []string{filepath.Join(runsDirName, running.String()), filepath.Join(envsDirName, tmpPrefix+"abc")} {
		if _, err := os.Stat(filepath.Join(h.dir, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s not removed (%v)", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(h.dir, envsDirName, "cached-env")); err != nil {
		t.Errorf("cached environment removed: %v", err)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
}

func TestAppendLogsRetried(t *testing.T) {
	h := newHarness(t, nil)
	var failures atomic.Int32
	h.coord.appendLogsErr = func(_ uuid.UUID, call int) error {
		// Fail every other call, including during the final flush.
		if call%2 == 1 {
			failures.Add(1)
			return errors.New("server busy")
		}
		return nil
	}
	h.ex.behave = func(c *fakeContainer) {
		for i := range 50 {
			c.stdout(strings.Repeat("x", i) + "\n")
			time.Sleep(time.Millisecond)
		}
		c.exit(0, false)
	}
	a := newAssignment(pyManifest())
	h.coord.enqueue(a, mainPy)
	h.start()

	f := h.coord.waitFinish(t, a.RunID, testTimeout)
	if f.Status != runproto.StatusCompleted {
		t.Fatalf("Finish = %+v", f)
	}
	h.coord.checkProtocol(t)
	lines := h.coord.logLines(a.RunID)
	if len(lines) != 51 {
		t.Fatalf("got %d lines, want 51", len(lines))
	}
	for i, l := range lines[:50] {
		if l.Seq != int64(i+1) || l.Text != strings.Repeat("x", i) {
			t.Fatalf("line %d = %+v", i, l)
		}
	}
	if failures.Load() == 0 {
		t.Fatal("no upload failed; the test did not exercise retries")
	}
}

func TestAppendLogsLeaseLost(t *testing.T) {
	h := newHarness(t, nil)
	h.coord.appendLogsErr = func(uuid.UUID, int) error { return runproto.ErrLeaseLost }
	h.ex.behave = func(c *fakeContainer) {
		c.stdout("working\n")
		c.exitOnStop()
	}
	a := newAssignment(pyManifest())
	h.coord.enqueue(a, mainPy)
	h.start()

	waitFor(t, testTimeout, "container removed", func() bool { return h.ex.wasRemoved("c1") })
	if !h.ex.wasStopped("c1") {
		t.Fatal("container not stopped after the lease was lost")
	}
	waitFor(t, testTimeout, "slot freed", func() bool { return h.r.activeCount() == 0 })
	if f, ok := h.coord.finish(a.RunID); ok {
		t.Fatalf("run finished after its lease was lost: %+v", f)
	}
}

func TestContainerDisappears(t *testing.T) {
	h := newHarness(t, nil)
	h.ex.behave = func(c *fakeContainer) {
		c.stdout("working\n")
		time.Sleep(20 * time.Millisecond)
		c.vanish()
	}
	a := newAssignment(pyManifest())
	h.coord.enqueue(a, mainPy)
	h.start()

	f := h.coord.waitFinish(t, a.RunID, testTimeout)
	if f.Status != runproto.StatusInterrupted || f.ErrorCode != "run_interrupted" || !strings.Contains(f.ErrorMessage, "disappeared") {
		t.Fatalf("Finish = %+v", f)
	}
	h.coord.checkProtocol(t)
	if texts := h.coord.logTexts(a.RunID); len(texts) != 2 || texts[0] != "working" {
		t.Fatalf("logs = %q", texts)
	}
}

func TestShutdownLeavesContainer(t *testing.T) {
	h := newHarness(t, nil)
	h.ex.behave = func(c *fakeContainer) { c.exitOnStop() }
	a := newAssignment(pyManifest())
	h.coord.enqueue(a, mainPy)
	stop := h.start()
	h.coord.waitStarted(t, a.RunID, testTimeout)

	stop()
	if h.ex.wasStopped("c1") || h.ex.wasRemoved("c1") {
		t.Fatal("container touched on shutdown")
	}
	if f, ok := h.coord.finish(a.RunID); ok {
		t.Fatalf("run finished on shutdown: %+v", f)
	}
	if _, err := os.Stat(filepath.Join(h.dir, runsDirName, a.RunID.String(), "work")); err != nil {
		t.Fatalf("run directory removed on shutdown: %v", err)
	}
}

func TestSlotsLimitClaims(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Slots = 1 })
	release := make(chan struct{})
	var running, maxRunning atomic.Int32
	h.ex.behave = func(c *fakeContainer) {
		n := running.Add(1)
		for {
			m := maxRunning.Load()
			if n <= m || maxRunning.CompareAndSwap(m, n) {
				break
			}
		}
		<-release
		running.Add(-1)
		c.exit(0, false)
	}
	a1, a2 := newAssignment(pyManifest()), newAssignment(pyManifest())
	h.coord.enqueue(a1, mainPy)
	h.coord.enqueue(a2, mainPy)
	h.start()
	h.coord.waitStarted(t, a1.RunID, testTimeout)
	time.Sleep(50 * time.Millisecond)
	close(release)
	h.coord.waitFinish(t, a1.RunID, testTimeout)
	h.coord.waitFinish(t, a2.RunID, testTimeout)
	if maxRunning.Load() != 1 {
		t.Fatalf("%d runs at once with one slot", maxRunning.Load())
	}
}

func TestNewValidates(t *testing.T) {
	ex, coord := newFakeExecutor(&callLog{}), newFakeCoordinator(&callLog{})
	good := Options{ID: "r1", Slots: 1, DataDir: "/var/lib/lerryn/runner", PythonImage: "python", Executor: ex, Coordinator: coord}
	if _, err := New(good); err != nil {
		t.Fatalf("New(good) = %v", err)
	}
	for name, mutate := range map[string]func(*Options){
		"bad id":           func(o *Options) { o.ID = "bad id" },
		"no slots":         func(o *Options) { o.Slots = 0 },
		"relative dir":     func(o *Options) { o.DataDir = "data" },
		"unclean dir":      func(o *Options) { o.DataDir = "/var/lib/../lib" },
		"no image":         func(o *Options) { o.PythonImage = "" },
		"no executor":      func(o *Options) { o.Executor = nil },
		"no coordinator":   func(o *Options) { o.Coordinator = nil },
		"unknown network":  func(o *Options) { o.AllowedNetworks = []string{"host"} },
		"negative timeout": func(o *Options) { o.InstallTimeout = -1 },
	} {
		o := good
		mutate(&o)
		if _, err := New(o); err == nil {
			t.Errorf("%s: New succeeded", name)
		}
	}
}

func TestTaskUserMatchesExecutorDefault(t *testing.T) {
	if got := fmt.Sprintf("%d:%d", taskUID, taskGID); got != executor.DefaultUser {
		t.Fatalf("task user %s, executor default %s", got, executor.DefaultUser)
	}
}
