package runner

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/executor"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

var depsFiles = []runproto.File{
	{Path: "main.py", Content: []byte("import six\n")},
	{Path: "pyproject.toml", Content: []byte("[project]\nname = \"t\"\nversion = \"0.1.0\"\ndependencies = [\"six\"]\n")},
	{Path: "uv.lock", Content: []byte("version = 1\n")},
}

func depsManifest() runproto.Manifest {
	m := pyManifest()
	m.Runtime.Lockfile = "uv.lock"
	return m
}

func TestDependencyEnvironment(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.PyPIMirror = "https://mirror.example/simple" })
	var installs atomic.Int32
	release := make(chan struct{})
	h.ex.behave = func(c *fakeContainer) {
		if c.isTask() {
			c.exit(0, false)
			return
		}
		installs.Add(1)
		// Hold the installation so the second run has to wait for it.
		<-release
		bin := filepath.Join(c.mountSource(envMount), "bin")
		if err := os.Mkdir(bin, 0o750); err != nil {
			panic(err)
		}
		c.stdout("Installed 1 package in 3ms\n")
		c.exit(0, false)
	}
	a1, a2 := newAssignment(depsManifest()), newAssignment(depsManifest())
	h.coord.enqueue(a1, depsFiles)
	h.coord.enqueue(a2, depsFiles)
	h.start()
	waitFor(t, testTimeout, "installation started", func() bool { return installs.Load() == 1 })
	time.Sleep(30 * time.Millisecond)
	close(release)

	f1 := h.coord.waitFinish(t, a1.RunID, testTimeout)
	f2 := h.coord.waitFinish(t, a2.RunID, testTimeout)
	if f1.Status != runproto.StatusCompleted || f2.Status != runproto.StatusCompleted {
		t.Fatalf("Finish = %+v, %+v", f1, f2)
	}
	h.coord.checkProtocol(t)
	if n := installs.Load(); n != 1 {
		t.Fatalf("%d installations for one environment", n)
	}

	envID := environmentID("sha256:fakeimage", uvSync, depsFiles[2].Content, depsFiles[1].Content)
	envDir := filepath.Join(h.dir, envsDirName, envID)
	if fi, err := os.Stat(filepath.Join(envDir, "bin")); err != nil || !fi.IsDir() {
		t.Fatalf("environment not stored at %s: %v", envDir, err)
	}
	h.coord.mu.Lock()
	gotEnvIDs := []string{h.coord.started[a1.RunID], h.coord.started[a2.RunID]}
	h.coord.mu.Unlock()
	if gotEnvIDs[0] != envID || gotEnvIDs[1] != envID {
		t.Fatalf("Started env IDs = %v, want %s", gotEnvIDs, envID)
	}

	var install executor.Spec
	var tasks []executor.Spec
	for _, s := range h.ex.startedSpecs() {
		if s.Command[0] == "uv" {
			install = s
		} else {
			tasks = append(tasks, s)
		}
	}
	if !slices.Equal(install.Command, []string{"uv", "sync", "--frozen", "--no-install-project", "--no-dev", "--project", "/sbx/task"}) {
		t.Errorf("install command = %v", install.Command)
	}
	if install.Network != executor.NetworkFull || !strings.HasSuffix(install.RunID, depsRunSuffix) {
		t.Errorf("install network %q, run id %q", install.Network, install.RunID)
	}
	for _, kv := range []string{
		"UV_PROJECT_ENVIRONMENT=/sbx/env", "UV_CACHE_DIR=/tmp/uv-cache", "UV_PYTHON_DOWNLOADS=never",
		"UV_LINK_MODE=copy", "HOME=/tmp", "UV_INDEX_URL=https://mirror.example/simple",
	} {
		if !slices.Contains(install.Env, kv) {
			t.Errorf("install env lacks %s: %q", kv, install.Env)
		}
	}
	if len(install.Mounts) != 2 || install.Mounts[0].Target != taskMount || !install.Mounts[0].ReadOnly ||
		filepath.Base(install.Mounts[0].Source) != depsDirName || install.Mounts[1].Target != envMount || install.Mounts[1].ReadOnly {
		t.Errorf("install mounts = %+v", install.Mounts)
	}

	for _, s := range tasks {
		i := slices.IndexFunc(s.Mounts, func(m executor.Mount) bool { return m.Target == envMount })
		if i < 0 || s.Mounts[i].Source != envDir || !s.Mounts[i].ReadOnly {
			t.Errorf("task mounts = %+v", s.Mounts)
		}
		if !slices.Contains(s.Env, "PATH=/sbx/env/bin:/sbx/sdk/bin:"+basePath) || !slices.Contains(s.Env, "VIRTUAL_ENV=/sbx/env") {
			t.Errorf("task env = %q", s.Env)
		}
	}

	// Installation output and progress are in the log of the run that
	// installed; the other run says it used the cache.
	var installer, waiter []string
	for _, id := range []string{a1.RunID.String(), a2.RunID.String()} {
		texts := h.coord.logTexts(mustParseUUID(t, id))
		if slices.Contains(texts, "Installed 1 package in 3ms") {
			installer = texts
		} else {
			waiter = texts
		}
	}
	if len(installer) == 0 || !strings.HasPrefix(installer[0], "installing dependencies from uv.lock") ||
		!slices.ContainsFunc(installer, func(s string) bool { return strings.Contains(s, "ready in") }) {
		t.Errorf("installing run's log = %q", installer)
	}
	if len(waiter) == 0 || !strings.HasPrefix(waiter[0], "using cached dependency environment") {
		t.Errorf("waiting run's log = %q", waiter)
	}

	// Nothing unfinished is left in envs/.
	entries, _ := os.ReadDir(filepath.Join(h.dir, envsDirName))
	if len(entries) != 1 {
		t.Errorf("envs/ holds %d entries", len(entries))
	}
}

func TestDependencyInstallFails(t *testing.T) {
	h := newHarness(t, nil)
	h.ex.behave = func(c *fakeContainer) {
		if c.isTask() {
			panic("task started despite a failed installation")
		}
		c.stderr("error: Failed to fetch: `https://pypi.org/simple/six/`\n")
		c.exit(2, false)
	}
	a := newAssignment(depsManifest())
	h.coord.enqueue(a, depsFiles)
	h.start()

	f := h.coord.waitFinish(t, a.RunID, testTimeout)
	if f.Status != runproto.StatusFailed || f.ErrorCode != "dependency_install_failed" || f.ExitCode != nil || f.ErrorStage != "" ||
		!strings.Contains(f.ErrorMessage, "uv sync exited with code 2") {
		t.Fatalf("Finish = %+v", f)
	}
	texts := h.coord.logTexts(a.RunID)
	if !slices.ContainsFunc(texts, func(s string) bool { return strings.Contains(s, "Failed to fetch") }) {
		t.Fatalf("installation output missing from the log: %q", texts)
	}
	waitFor(t, testTimeout, "installation container removed", func() bool { return h.ex.wasRemoved("c1") })
	entries, _ := os.ReadDir(filepath.Join(h.dir, envsDirName))
	if len(entries) != 0 {
		t.Fatalf("envs/ not cleaned up: %v", entries)
	}
}

func TestDependencyFilesMissing(t *testing.T) {
	h := newHarness(t, nil)
	a := newAssignment(depsManifest())
	h.coord.enqueue(a, depsFiles[:2])
	h.start()
	f := h.coord.waitFinish(t, a.RunID, testTimeout)
	if f.Status != runproto.StatusFailed || f.ErrorCode != "dependency_install_failed" || !strings.Contains(f.ErrorMessage, "uv.lock and pyproject.toml") {
		t.Fatalf("Finish = %+v", f)
	}
	if len(h.ex.startedSpecs()) != 0 {
		t.Fatal("container started without a lock file")
	}
}

func TestCancelDuringInstall(t *testing.T) {
	h := newHarness(t, nil)
	h.ex.behave = func(c *fakeContainer) {
		if c.isTask() {
			panic("task started after cancel")
		}
		c.exitOnStop()
	}
	a := newAssignment(depsManifest())
	h.coord.enqueue(a, depsFiles)
	h.start()
	waitFor(t, testTimeout, "installation started", func() bool { return len(h.ex.startedSpecs()) == 1 })
	h.coord.requestCancel(a.RunID)

	f := h.coord.waitFinish(t, a.RunID, testTimeout)
	if f.Status != runproto.StatusCancelled || f.ExitCode != nil {
		t.Fatalf("Finish = %+v", f)
	}
	if !h.ex.wasStopped("c1") {
		t.Fatal("installation container not stopped")
	}
}

// uvSync is the Python installation command. The environment ID of an
// existing Python environment must not change with runner upgrades that
// leave it alone, or every cache is rebuilt.
var uvSync = []string{"uv", "sync", "--frozen", "--no-install-project", "--no-dev", "--project", "/sbx/task"}

func TestEnvironmentID(t *testing.T) {
	id := func(image string, cmd []string, files ...string) string {
		parts := make([][]byte, len(files))
		for i, f := range files {
			parts[i] = []byte(f)
		}
		return environmentID(image, cmd, parts...)
	}
	base := id("sha256:img", uvSync, "lock", "pyproject")
	if len(base) != 64 || base != id("sha256:img", uvSync, "lock", "pyproject") {
		t.Fatalf("environmentID not a stable SHA-256: %q", base)
	}
	// Pinned from M1, see uvSync.
	if base != "6f62e4080a331e8f60bdab750f9b8f6cfc02fdeef031ea80d260e94322099aab" {
		t.Errorf("environment ID changed: %s", base)
	}
	for name, other := range map[string]string{
		"image":     id("sha256:other", uvSync, "lock", "pyproject"),
		"command":   id("sha256:img", []string{"npm", "ci"}, "lock", "pyproject"),
		"lock":      id("sha256:img", uvSync, "lock2", "pyproject"),
		"pyproject": id("sha256:img", uvSync, "lock", "pyproject2"),
		"boundary":  id("sha256:img", uvSync, "lockp", "yproject"),
	} {
		if other == base {
			t.Errorf("changing %s keeps the environment ID", name)
		}
	}
	if !slices.Equal((&Runner{}).newRuntimes()[langPython].installCommand, uvSync) {
		t.Error("the Python install command changed")
	}
}
