//go:build integration

package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/executor"
	"github.com/niltavi/lerryn/server/internal/executor/docker"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

// Environment variables understood by these tests.
const (
	// envTestImage overrides the Python runtime image.
	envTestImage = "LERRYN_TEST_PYTHON_IMAGE"
	// envTestMountRoot is where data directories are created. When the
	// tests run in a container against the host's daemon, it must be a
	// directory mounted at the same path inside and outside.
	envTestMountRoot = "LERRYN_TEST_MOUNT_ROOT"
	// envTestPyPIMirror is passed to the runner as its PyPI mirror.
	envTestPyPIMirror = "LERRYN_TEST_PYPI_MIRROR"
	// envTestNodeImage and envTestShellImage override the Node and Shell
	// runtime images; envTestNPMMirror is the runner's npm registry.
	envTestNodeImage  = "LERRYN_TEST_NODE_IMAGE"
	envTestShellImage = "LERRYN_TEST_SHELL_IMAGE"
	envTestNPMMirror  = "LERRYN_TEST_NPM_MIRROR"
)

// testLabel marks every container and network these tests create, so
// cleanup never touches anything else on the daemon.
const testLabel = "lerryn.test"

var (
	itImage      = config.Default().Runner.PythonImage
	itNodeImage  = config.Default().Runner.NodeImage
	itShellImage = config.Default().Runner.ShellImage
	itID         string
	itExec       *docker.Executor
	itAPI        *client.Client
	itSeq        atomic.Int64
)

func TestMain(m *testing.M) {
	os.Exit(runIntegration(m))
}

func runIntegration(m *testing.M) int {
	if v := os.Getenv(envTestImage); v != "" {
		itImage = v
	}
	if v := os.Getenv(envTestNodeImage); v != "" {
		itNodeImage = v
	}
	if v := os.Getenv(envTestShellImage); v != "" {
		itShellImage = v
	}
	itID = fmt.Sprintf("m1-%d-%d", time.Now().Unix(), os.Getpid())

	cli, err := client.New(client.FromEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "docker client:", err)
		return 1
	}
	itAPI = cli
	defer func() { _ = cli.Close() }()

	itExec, err = docker.New(docker.Options{
		NetworkName: "lerryn-test-" + itID,
		Labels:      map[string]string{testLabel: itID},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "executor:", err)
		return 1
	}
	defer func() { _ = itExec.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if _, err := itExec.Check(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "daemon check:", err)
		return 1
	}
	// Pull once up front so the first test's timing does not include it.
	for _, img := range []string{itImage, itNodeImage, itShellImage} {
		if _, err := itExec.EnsureImage(ctx, img); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	code := m.Run()
	if err := cleanupLabelled(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "cleanup:", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

// cleanupLabelled removes every container and network carrying this
// process's test label, and fails if anything survives.
func cleanupLabelled(ctx context.Context) error {
	f := make(client.Filters).Add("label", testLabel+"="+itID)
	var errs []error
	cs, err := itAPI.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	for _, c := range cs.Items {
		if _, err := itAPI.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !cerrdefs.IsNotFound(err) {
			errs = append(errs, err)
		}
	}
	ns, err := itAPI.NetworkList(ctx, client.NetworkListOptions{Filters: f})
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("list networks: %w", err))...)
	}
	for _, n := range ns.Items {
		if _, err := itAPI.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
			errs = append(errs, err)
		}
	}
	if cs, err := itAPI.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f}); err == nil && len(cs.Items) > 0 {
		errs = append(errs, fmt.Errorf("%d test containers survived cleanup", len(cs.Items)))
	}
	return errors.Join(errs...)
}

// newDockerHarness returns a runner on the real Docker executor with an
// in-memory coordinator. dataDir, when not empty, reuses a data directory
// to simulate a restart.
func newDockerHarness(t *testing.T, id, dataDir string, coord *fakeCoordinator) *harness {
	t.Helper()
	if dataDir == "" {
		dataDir = itDataDir(t)
	}
	if coord == nil {
		coord = newFakeCoordinator(&callLog{})
	}
	if id == "" {
		id = fmt.Sprintf("%s-r%d", itID, itSeq.Add(1))
	}
	h := &harness{t: t, log: coord.log, coord: coord, dir: dataDir}
	r, err := New(Options{
		ID:          id,
		Name:        t.Name(),
		Slots:       2,
		DataDir:     dataDir,
		PythonImage: itImage,
		NodeImage:   itNodeImage,
		ShellImage:  itShellImage,
		PyPIMirror:  os.Getenv(envTestPyPIMirror),
		NPMMirror:   os.Getenv(envTestNPMMirror),
		Executor:    itExec,
		Coordinator: coord,
		StopGrace:   2 * time.Second,
		Logger:      slog.New(slog.NewTextHandler(t.Output(), nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	r.heartbeatInterval = 100 * time.Millisecond
	r.claimInterval = 50 * time.Millisecond
	h.r = r
	return h
}

// itDataDir returns a data directory the daemon can mount. Files the task
// user created in it are deleted by a root container first, since the
// test process may not be allowed to delete them itself.
func itDataDir(t *testing.T) string {
	t.Helper()
	var dir string
	if root := os.Getenv(envTestMountRoot); root != "" {
		d, err := os.MkdirTemp(root, "runner-")
		if err != nil {
			t.Fatal(err)
		}
		dir = d
		t.Cleanup(func() { _ = os.RemoveAll(d) }) // #nosec G703 -- a temp dir this test created
	} else {
		dir = t.TempDir()
	}
	t.Cleanup(func() { purgeAsRoot(t, dir) })
	return dir
}

func purgeAsRoot(t *testing.T, dir string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	init := false
	created, err := itAPI.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:  itImage,
			User:   "0:0",
			Cmd:    []string{"find", "/d", "-mindepth", "1", "-delete"},
			Labels: map[string]string{testLabel: itID},
		},
		HostConfig: &container.HostConfig{
			NetworkMode: "none",
			Init:        &init,
			Mounts:      []mount.Mount{{Type: mount.TypeBind, Source: dir, Target: "/d"}},
		},
	})
	if err != nil {
		t.Logf("purge %s: %v", dir, err)
		return
	}
	defer func() {
		_, _ = itAPI.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := itAPI.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Logf("purge %s: %v", dir, err)
		return
	}
	wr := itAPI.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case <-wr.Result:
	case err := <-wr.Error:
		t.Logf("purge %s: %v", dir, err)
	}
}

func itManifest() runproto.Manifest {
	return runproto.Manifest{
		Runtime:     runproto.Runtime{Language: "python", Version: "3.12"},
		Entrypoint:  []string{"python", "main.py"},
		Stages:      []runproto.Stage{{ID: "fetch", Name: "Fetch"}, {ID: "save", Name: "Save", After: []string{"fetch"}}},
		Timeout:     2 * time.Minute,
		MilliCPUs:   1000,
		MemoryBytes: 256 << 20,
		Network:     "none",
	}
}

func pyFiles(main string) []runproto.File {
	return []runproto.File{{Path: "main.py", Content: []byte(main)}}
}

// assertContainersGone waits for the run's containers to be removed; the
// runner removes them right after Finish.
func assertContainersGone(t *testing.T, runID uuid.UUID) {
	t.Helper()
	for _, label := range []string{runID.String(), runID.String() + depsRunSuffix} {
		waitFor(t, 30*time.Second, "containers of run "+label+" removed", func() bool {
			cs, err := itExec.List(t.Context(), map[string]string{executor.LabelRunID: label})
			if err != nil {
				t.Fatal(err)
			}
			return len(cs) == 0
		})
	}
}

func waitForLog(t *testing.T, c *fakeCoordinator, id uuid.UUID, text string, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, fmt.Sprintf("log line %q", text), func() bool {
		return slices.Contains(c.logTexts(id), text)
	})
}

func linesOf(c *fakeCoordinator, id uuid.UUID, stream string) []runproto.LogLine {
	var out []runproto.LogLine
	for _, l := range c.logLines(id) {
		if l.Stream == stream {
			out = append(out, l)
		}
	}
	return out
}

const pythonTask = `
import json, os, subprocess, sys
import sbx

print("params", json.dumps(sbx.params(), sort_keys=True))
print("uid", os.getuid(), "run", os.environ["LERRYN_RUN_ID"])
subprocess.run(["sbx", "stage", "start", "fetch"], check=True)
for i in range(3):
    print(f"line {i}")
print("to stderr", file=sys.stderr)
subprocess.run(["sbx", "stage", "done", "fetch", "--message", "fetched 3"], check=True)
with sbx.stage("save"):
    with open("/sbx/work/out.txt", "w") as f:
        f.write("ok")
    try:
        open("/sbx/task/new.txt", "w")
        print("task dir is writable")
    except OSError:
        print("task dir is read-only")
sbx.result("ok", "saved 3 lines", {"lines": 3})
print("bye", end="")
`

func TestIntegrationPythonTask(t *testing.T) {
	h := newDockerHarness(t, "", "", nil)
	a := newAssignment(itManifest())
	a.Params = []byte(`{"source": "crm", "n": 3}`)
	h.coord.enqueue(a, pyFiles(pythonTask))
	h.start()

	f := h.coord.waitFinish(t, a.RunID, 2*time.Minute)
	if f.Status != runproto.StatusCompleted || f.ExitCode == nil || *f.ExitCode != 0 {
		t.Fatalf("Finish = %+v; logs:\n%s", f, strings.Join(h.coord.logTexts(a.RunID), "\n"))
	}
	h.coord.checkProtocol(t)

	var stdout []string
	stages := map[string]string{}
	for _, l := range linesOf(h.coord, a.RunID, runproto.StreamStdout) {
		stdout = append(stdout, l.Text)
		stages[l.Text] = l.Stage
	}
	want := []string{
		`params {"n": 3, "source": "crm"}`,
		"uid 65532 run " + a.RunID.String(),
		"line 0", "line 1", "line 2",
		"task dir is read-only",
		"bye",
	}
	if !slices.Equal(stdout, want) {
		t.Fatalf("stdout = %q\nwant %q", stdout, want)
	}
	if stderr := linesOf(h.coord, a.RunID, runproto.StreamStderr); len(stderr) != 1 || stderr[0].Text != "to stderr" {
		t.Fatalf("stderr = %+v", stderr)
	}
	// Lines written after a stage started carry it.
	if stages["line 0"] != "fetch" || stages["task dir is read-only"] != "save" || stages["bye"] != "" {
		t.Fatalf("stage labels = %v", stages)
	}
	for i, l := range h.coord.logLines(a.RunID) {
		if l.Seq != int64(i+1) {
			t.Fatalf("line %d has seq %d", i, l.Seq)
		}
	}

	var got []string
	for _, e := range h.coord.runEvents(a.RunID) {
		s := strings.TrimSpace(e.Kind + " " + e.Stage + " " + e.Message)
		if e.Result != nil {
			s = e.Kind + " " + e.Result.Status + " " + e.Result.Summary + " " + string(e.Result.Data)
		}
		got = append(got, s)
	}
	wantEvents := []string{
		"stage.start fetch",
		"stage.done fetch fetched 3",
		"stage.start save",
		"stage.done save",
		`result ok saved 3 lines {"lines":3}`,
	}
	if !slices.Equal(got, wantEvents) {
		t.Fatalf("events = %q\nwant %q", got, wantEvents)
	}

	waitFor(t, 30*time.Second, "run cleaned up", func() bool {
		_, err := os.Stat(filepath.Join(h.dir, runsDirName, a.RunID.String()))
		return errors.Is(err, os.ErrNotExist)
	})
	assertContainersGone(t, a.RunID)
}

func TestIntegrationDependencies(t *testing.T) {
	pyproject, err := os.ReadFile("testdata/deps/pyproject.toml")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.ReadFile("testdata/deps/uv.lock")
	if err != nil {
		t.Fatal(err)
	}
	files := []runproto.File{
		{Path: "main.py", Content: []byte("import sys, six\nprint('six', six.__version__)\nprint('prefix', sys.prefix)\n")},
		{Path: "pyproject.toml", Content: pyproject},
		{Path: "uv.lock", Content: lock},
	}
	m := itManifest()
	m.Runtime.Lockfile = "uv.lock"

	h := newDockerHarness(t, "", "", nil)
	first, second := newAssignment(m), newAssignment(m)
	h.coord.enqueue(first, files)
	h.start()

	f := h.coord.waitFinish(t, first.RunID, 5*time.Minute)
	logs := h.coord.logTexts(first.RunID)
	if f.Status != runproto.StatusCompleted {
		t.Fatalf("Finish = %+v; logs:\n%s", f, strings.Join(logs, "\n"))
	}
	for _, want := range []string{"six 1.17.0", "prefix /sbx/env"} {
		if !slices.Contains(logs, want) {
			t.Fatalf("log lacks %q:\n%s", want, strings.Join(logs, "\n"))
		}
	}
	if !strings.HasPrefix(logs[0], "installing dependencies from uv.lock") ||
		!slices.ContainsFunc(logs, func(s string) bool { return strings.Contains(s, "+ six==1.17.0") }) {
		t.Fatalf("installation not logged:\n%s", strings.Join(logs, "\n"))
	}

	// The second run reuses the environment.
	h.coord.enqueue(second, files)
	f = h.coord.waitFinish(t, second.RunID, 2*time.Minute)
	logs = h.coord.logTexts(second.RunID)
	if f.Status != runproto.StatusCompleted || !strings.HasPrefix(logs[0], "using cached dependency environment") || !slices.Contains(logs, "six 1.17.0") {
		t.Fatalf("second run: Finish = %+v; logs:\n%s", f, strings.Join(logs, "\n"))
	}
	h.coord.mu.Lock()
	envIDs := []string{h.coord.started[first.RunID], h.coord.started[second.RunID]}
	h.coord.mu.Unlock()
	if envIDs[0] == "" || envIDs[0] != envIDs[1] {
		t.Fatalf("env IDs = %q", envIDs)
	}
	h.coord.checkProtocol(t)
	assertContainersGone(t, first.RunID)
	assertContainersGone(t, second.RunID)
}

// marker returns a number to embed in process command lines so the host
// process scan finds exactly this test's processes.
func marker() string {
	return fmt.Sprintf("%d", 70_000_000+rand.IntN(9_999_999)) //nolint:gosec // a test marker, not a secret
}

const processTreeTask = `
import subprocess, sys, time
import sbx

m = sbx.params()["marker"]
subprocess.Popen(["sh", "-c", f"sleep {m} & sleep {m} & wait"])
subprocess.Popen(["sleep", m])
time.sleep(0.5)
sbx.stage_start("fetch")
print("ready")
time.sleep(3600)
`

// TestIntegrationCancelStopsProcessTree is acceptance scenario A09: after
// a cancel the container and every process in it are gone, and the run is
// reported cancelled only then.
func TestIntegrationCancelStopsProcessTree(t *testing.T) {
	h := newDockerHarness(t, "", "", nil)
	mk := marker()
	a := newAssignment(itManifest())
	a.Params = []byte(`{"marker": "` + mk + `"}`)
	h.coord.enqueue(a, pyFiles(processTreeTask))
	h.start()

	waitForLog(t, h.coord, a.RunID, "ready", time.Minute)
	if procs := hostProcesses(t, "sleep "+mk); len(procs) < 3 {
		t.Fatalf("expected at least 3 task processes before cancel, found %q", procs)
	}
	h.coord.requestCancel(a.RunID)
	f := h.coord.waitFinish(t, a.RunID, time.Minute)
	if f.Status != runproto.StatusCancelled || f.ErrorCode != "" || f.ExitCode == nil {
		t.Fatalf("Finish = %+v", f)
	}
	// Finish is only sent after the stop was confirmed; by now the runner
	// has also removed the container.
	assertContainersGone(t, a.RunID)
	if procs := hostProcesses(t, "sleep "+mk); len(procs) != 0 {
		t.Fatalf("processes survived the cancel: %q", procs)
	}
	h.coord.checkProtocol(t)
}

func TestIntegrationTimeout(t *testing.T) {
	h := newDockerHarness(t, "", "", nil)
	m := itManifest()
	m.Timeout = 2 * time.Second
	a := newAssignment(m)
	h.coord.enqueue(a, pyFiles("import sbx, time\nsbx.stage_start('fetch')\nprint('waiting', flush=True)\ntime.sleep(600)\n"))
	h.start()

	f := h.coord.waitFinish(t, a.RunID, time.Minute)
	if f.Status != runproto.StatusTimedOut || f.ErrorCode != "timeout" || f.ErrorStage != "fetch" || f.ExitCode == nil {
		t.Fatalf("Finish = %+v", f)
	}
	if logs := h.coord.logTexts(a.RunID); !slices.Contains(logs, "waiting") {
		t.Fatalf("logs = %q", logs)
	}
	h.coord.checkProtocol(t)
	assertContainersGone(t, a.RunID)
}

func TestIntegrationOutOfMemory(t *testing.T) {
	h := newDockerHarness(t, "", "", nil)
	m := itManifest()
	m.MemoryBytes = 64 << 20
	a := newAssignment(m)
	h.coord.enqueue(a, pyFiles("print('allocating', flush=True)\nb = b'x' * (512 << 20)\nprint('survived')\n"))
	h.start()

	f := h.coord.waitFinish(t, a.RunID, time.Minute)
	if f.Status != runproto.StatusFailed || f.ErrorCode != "out_of_memory" || f.ExitCode == nil || *f.ExitCode != 137 {
		t.Fatalf("Finish = %+v; logs:\n%s", f, strings.Join(h.coord.logTexts(a.RunID), "\n"))
	}
	if logs := h.coord.logTexts(a.RunID); !slices.Contains(logs, "allocating") || slices.Contains(logs, "survived") {
		t.Fatalf("logs = %q", logs)
	}
	h.coord.checkProtocol(t)
	assertContainersGone(t, a.RunID)
}

// TestIntegrationRestartReconciles stops a runner while its task runs and
// starts it again: the container left behind is stopped and removed, and
// the run is finished as interrupted (tech design 7.6).
func TestIntegrationRestartReconciles(t *testing.T) {
	first := newDockerHarness(t, "", "", nil)
	a := newAssignment(itManifest())
	first.coord.enqueue(a, pyFiles("import time\nprint('running', flush=True)\ntime.sleep(600)\n"))
	stop := first.start()
	waitForLog(t, first.coord, a.RunID, "running", time.Minute)
	stop()

	cs, err := itExec.List(t.Context(), map[string]string{executor.LabelRunID: a.RunID.String()})
	if err != nil || len(cs) != 1 || !cs[0].Running {
		t.Fatalf("after shutdown: containers %+v, %v; want the task still running", cs, err)
	}
	if _, ok := first.coord.finish(a.RunID); ok {
		t.Fatal("run finished on shutdown")
	}

	second := newDockerHarness(t, first.r.opts.ID, first.dir, first.coord)
	second.start()
	f := second.coord.waitFinish(t, a.RunID, time.Minute)
	if f.Status != runproto.StatusInterrupted {
		t.Fatalf("Finish = %+v", f)
	}
	assertContainersGone(t, a.RunID)
	// Reconciliation reports the run first and removes leftover run
	// directories after that.
	waitFor(t, 10*time.Second, "run directory removed", func() bool {
		_, err := os.Stat(filepath.Join(first.dir, runsDirName, a.RunID.String()))
		return errors.Is(err, os.ErrNotExist)
	})
}

// hostProcesses lists processes anywhere on the daemon's host whose command
// line contains needle. It runs a short-lived root container in the host
// PID namespace, which sees processes of every container, including ones
// that escaped a container's cgroup or PID namespace. On macOS the "host"
// is the Linux VM that runs the daemon.
func hostProcesses(t *testing.T, needle string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	// The needle comes in through the environment so this script's own
	// command line never matches it.
	script := `for f in /proc/[0-9]*/cmdline; do
  c=$(tr '\0' ' ' < "$f" 2>/dev/null) || continue
  case "$c" in *"$NEEDLE"*) echo "${f%/cmdline} $c";; esac
done`
	init := false
	created, err := itAPI.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:  itImage,
			User:   "0:0",
			Env:    []string{"NEEDLE=" + needle},
			Cmd:    []string{"sh", "-c", script},
			Labels: map[string]string{testLabel: itID},
		},
		HostConfig: &container.HostConfig{
			PidMode:     container.PidMode("host"),
			NetworkMode: "none",
			Init:        &init,
		},
	})
	if err != nil {
		t.Fatalf("create process scanner: %v", err)
	}
	defer func() {
		_, _ = itAPI.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := itAPI.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start process scanner: %v", err)
	}
	wr := itAPI.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case <-wr.Result:
	case err := <-wr.Error:
		t.Fatalf("wait for process scanner: %v", err)
	}
	logs, err := itAPI.ContainerLogs(ctx, created.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		t.Fatalf("process scanner logs: %v", err)
	}
	defer func() { _ = logs.Close() }()
	var out, errOut bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &errOut, logs); err != nil {
		t.Fatalf("read process scanner logs: %v", err)
	}
	var procs []string
	for line := range strings.Lines(out.String()) {
		if line = strings.TrimSpace(line); line != "" {
			procs = append(procs, line)
		}
	}
	return procs
}
