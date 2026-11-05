package runner

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/executor"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

// markerToken returns the LERRYN_LOG_MARKER value of a container's
// environment and checks it is long enough not to be guessed by accident.
func markerToken(t *testing.T, env []string) string {
	t.Helper()
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "LERRYN_LOG_MARKER="); ok {
			if len(v) < 20 {
				t.Fatalf("marker token %q is too short", v)
			}
			return v
		}
	}
	t.Fatalf("no LERRYN_LOG_MARKER in %q", env)
	return ""
}

func withRuntimeImages(o *Options) {
	o.NodeImage = "node:test"
	o.ShellImage = "bash:test"
	o.NPMMirror = "https://npm.example/"
}

func nodeManifest(lockfile string) runproto.Manifest {
	m := pyManifest()
	m.Runtime = runproto.Runtime{Language: "node", Version: "22", Lockfile: lockfile}
	m.Entrypoint = []string{"node", "main.ts"}
	return m
}

func shellManifest() runproto.Manifest {
	m := pyManifest()
	m.Runtime = runproto.Runtime{Language: "shell", Version: "5"}
	m.Entrypoint = []string{"bash", "main.sh"}
	return m
}

func mountAt(mounts []executor.Mount, target string) (executor.Mount, bool) {
	i := slices.IndexFunc(mounts, func(m executor.Mount) bool { return m.Target == target })
	if i < 0 {
		return executor.Mount{}, false
	}
	return mounts[i], true
}

func TestNodeDependencies(t *testing.T) {
	h := newHarness(t, withRuntimeImages)
	var install, task executor.Spec
	var depsSeen []string
	var mountPoint error
	h.ex.behave = func(c *fakeContainer) {
		if c.isTask() {
			task = c.spec
			// The run container mounts node_modules inside the read-only
			// task directory, so the mount point must already exist.
			_, mountPoint = os.Stat(filepath.Join(c.mountSource(taskMount), "node_modules"))
			c.exit(0, false)
			return
		}
		install = c.spec
		entries, _ := os.ReadDir(c.mountSource(envMount))
		for _, e := range entries {
			depsSeen = append(depsSeen, e.Name())
		}
		// No node_modules: npm ci creates none for a lockfile without
		// packages, and the runner must add it.
		c.exit(0, false)
	}
	files := []runproto.File{
		{Path: "main.ts", Content: []byte("console.log(1)")},
		{Path: "package.json", Content: []byte(`{"name":"t"}`)},
		{Path: "package-lock.json", Content: []byte(`{"lockfileVersion":3}`)},
	}
	a := newAssignment(nodeManifest("package-lock.json"))
	h.coord.enqueue(a, files)
	h.start()
	if f := h.coord.waitFinish(t, a.RunID, testTimeout); f.Status != runproto.StatusCompleted {
		t.Fatalf("Finish = %+v; logs %q", f, h.coord.logTexts(a.RunID))
	}

	if install.Image != "sha256:fakeimage" || !slices.Equal(install.Command, []string{"npm", "ci", "--omit=dev", "--no-audit", "--no-fund"}) ||
		install.WorkingDir != envMount || install.Network != executor.NetworkFull {
		t.Errorf("install = %+v", install)
	}
	if len(install.Mounts) != 1 || install.Mounts[0].Target != envMount || install.Mounts[0].ReadOnly {
		t.Errorf("install mounts = %+v", install.Mounts)
	}
	for _, kv := range []string{"npm_config_registry=https://npm.example/", "npm_config_cache=/tmp/npm-cache", "HOME=/tmp"} {
		if !slices.Contains(install.Env, kv) {
			t.Errorf("install env lacks %s: %q", kv, install.Env)
		}
	}
	// Only the two dependency files reach the installation.
	if slices.Sort(depsSeen); !slices.Equal(depsSeen, []string{"package-lock.json", "package.json"}) {
		t.Errorf("installation saw %q", depsSeen)
	}

	envID := environmentID("sha256:fakeimage", install.Command, files[2].Content, files[1].Content)
	envDir := filepath.Join(h.dir, envsDirName, envID)
	if fi, err := os.Stat(filepath.Join(envDir, "node_modules")); err != nil || !fi.IsDir() {
		t.Fatalf("environment lacks node_modules: %v", err)
	}
	if mountPoint != nil {
		t.Errorf("no node_modules mount point in the task directory: %v", mountPoint)
	}
	if m, ok := mountAt(task.Mounts, "/sbx/task/node_modules"); !ok || m.Source != filepath.Join(envDir, "node_modules") || !m.ReadOnly {
		t.Errorf("task mounts = %+v", task.Mounts)
	}
	if m, ok := mountAt(task.Mounts, "/sbx/node_modules"); !ok || !strings.HasPrefix(m.Source, filepath.Join(h.dir, sdkDirName)) || !m.ReadOnly {
		t.Errorf("sdk module mount missing: %+v", task.Mounts)
	}
	if !slices.Contains(task.Env, "PATH=/sbx/task/node_modules/.bin:/sbx/sdk/bin:"+basePath) {
		t.Errorf("task env = %q", task.Env)
	}
	h.coord.mu.Lock()
	started := h.coord.started[a.RunID]
	h.coord.mu.Unlock()
	if started != envID {
		t.Errorf("Started env ID = %q, want %q", started, envID)
	}
}

func TestRuntimeTaskSpecs(t *testing.T) {
	tests := []struct {
		name       string
		manifest   runproto.Manifest
		image      string
		wantMounts []string
		wantEnv    []string
		noEnv      []string
	}{
		{
			name:       "node without dependencies",
			manifest:   nodeManifest(""),
			image:      "node:test",
			wantMounts: []string{"/sbx/events", "/sbx/node_modules", "/sbx/out", "/sbx/sdk", "/sbx/task", "/sbx/work"},
			wantEnv:    []string{"PATH=/sbx/sdk/bin:" + basePath},
			noEnv:      []string{"PYTHONPATH", "VIRTUAL_ENV"},
		},
		{
			name:       "shell",
			manifest:   shellManifest(),
			image:      "bash:test",
			wantMounts: []string{"/sbx/events", "/sbx/out", "/sbx/sdk", "/sbx/task", "/sbx/work"},
			wantEnv:    []string{"PATH=/sbx/sdk/bin:" + basePath, "HOME=/tmp", "LERRYN_EVENTS_FILE=/sbx/events/events.ndjson"},
			noEnv:      []string{"PYTHONPATH", "VIRTUAL_ENV", "npm_config_cache"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, withRuntimeImages)
			var spec executor.Spec
			var sbx []byte
			h.ex.behave = func(c *fakeContainer) {
				spec = c.spec
				sbx, _ = os.ReadFile(filepath.Join(c.mountSource(sdkMount), "bin", "sbx"))
				c.exit(0, false)
			}
			a := newAssignment(tc.manifest)
			h.coord.enqueue(a, []runproto.File{{Path: "main", Content: []byte("x")}})
			h.start()
			if f := h.coord.waitFinish(t, a.RunID, testTimeout); f.Status != runproto.StatusCompleted {
				t.Fatalf("Finish = %+v", f)
			}
			var targets []string
			for _, m := range spec.Mounts {
				targets = append(targets, m.Target)
			}
			if slices.Sort(targets); !slices.Equal(targets, tc.wantMounts) {
				t.Errorf("mounts = %q, want %q", targets, tc.wantMounts)
			}
			for _, kv := range tc.wantEnv {
				if !slices.Contains(spec.Env, kv) {
					t.Errorf("env lacks %s: %q", kv, spec.Env)
				}
			}
			for _, name := range tc.noEnv {
				if slices.ContainsFunc(spec.Env, func(kv string) bool { return strings.HasPrefix(kv, name+"=") }) {
					t.Errorf("env has %s: %q", name, spec.Env)
				}
			}
			markerToken(t, spec.Env)
			if !strings.HasPrefix(string(sbx), "#!/bin/sh") {
				t.Errorf("sbx is not the sh command: %.40q", sbx)
			}
			h.ex.mu.Lock()
			ensured := slices.Clone(h.ex.ensured)
			h.ex.mu.Unlock()
			if !slices.Equal(ensured, []string{tc.image}) {
				t.Errorf("ensured images %q, want %s", ensured, tc.image)
			}
		})
	}
}

func TestRuntimeRejected(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Options)
		manifest runproto.Manifest
		want     string
	}{
		{"unknown language", withRuntimeImages, func() runproto.Manifest {
			m := pyManifest()
			m.Runtime.Language = "ruby"
			return m
		}(), `runtime "ruby" is not supported`},
		{"no image", nil, shellManifest(), "no image configured for the shell runtime"},
		{"python lockfile on node", withRuntimeImages, nodeManifest("uv.lock"), `lockfile "uv.lock" is not supported for the node runtime; use package-lock.json`},
		{"npm lockfile on python", withRuntimeImages, func() runproto.Manifest {
			m := pyManifest()
			m.Runtime.Lockfile = "package-lock.json"
			return m
		}(), "use uv.lock"},
		{"lockfile on shell", withRuntimeImages, func() runproto.Manifest {
			m := shellManifest()
			m.Runtime.Lockfile = "package-lock.json"
			return m
		}(), "does not install dependencies"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.mutate)
			a := newAssignment(tc.manifest)
			h.coord.enqueue(a, nil)
			h.start()
			f := h.coord.waitFinish(t, a.RunID, testTimeout)
			if f.Status != runproto.StatusFailed || f.ErrorCode != errValidationFailed || !strings.Contains(f.ErrorMessage, tc.want) {
				t.Fatalf("Finish = %+v, want validation_failed with %q", f, tc.want)
			}
			if len(h.ex.startedSpecs()) != 0 {
				t.Fatal("a container was started")
			}
		})
	}
}

func TestNodeDependenciesNeedBothFiles(t *testing.T) {
	h := newHarness(t, withRuntimeImages)
	a := newAssignment(nodeManifest("package-lock.json"))
	h.coord.enqueue(a, []runproto.File{{Path: "package-lock.json", Content: []byte("{}")}})
	h.start()
	f := h.coord.waitFinish(t, a.RunID, testTimeout)
	if f.ErrorCode != errDependencyInstallFailed || !strings.Contains(f.ErrorMessage, "package-lock.json and package.json") {
		t.Fatalf("Finish = %+v", f)
	}
}
