package runner

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/niltavi/lerryn/server/internal/executor"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

// A run with an input gets it read-only at /sbx/in/body, with its media
// type in LERRYN_INPUT_CONTENT_TYPE (tech design 7.4).
func TestRunWithInput(t *testing.T) {
	h := newHarness(t, nil)
	var (
		spec executor.Spec
		body []byte
		mode os.FileMode
	)
	h.ex.behave = func(c *fakeContainer) {
		spec = c.spec
		p := filepath.Join(c.mountSource(inMount), inputFileName)
		body, _ = os.ReadFile(p) // #nosec G304 -- the run directory of this test
		if fi, err := os.Stat(p); err == nil {
			mode = fi.Mode().Perm()
		}
		c.exit(0, false)
	}
	a := newAssignment(pyManifest())
	a.HasInput = true
	h.coord.inputs[a.RunID] = runproto.Input{ContentType: "application/json\r\nX=1", Content: []byte(`{"event":"push"}`)}
	h.coord.enqueue(a, mainPy)
	h.start()
	if f := h.coord.waitFinish(t, a.RunID, testTimeout); f.Status != runproto.StatusCompleted {
		t.Fatalf("finish = %+v", f)
	}
	if string(body) != `{"event":"push"}` || mode != 0o644 {
		t.Fatalf("body = %q mode %v", body, mode)
	}
	if !slices.Contains(spec.Env, "LERRYN_INPUT_CONTENT_TYPE=application/jsonX=1") {
		t.Errorf("env = %q", spec.Env)
	}
	var in executor.Mount
	for _, m := range spec.Mounts {
		if m.Target == inMount {
			in = m
		}
	}
	if !in.ReadOnly || in.Source != filepath.Join(h.dir, runsDirName, a.RunID.String(), inDirName) {
		t.Errorf("input mount = %+v", in)
	}
	if i, j := h.log.index("input "), h.log.index("start "); i < 0 || j < 0 || i > j {
		t.Fatalf("input fetched at %d, container started at %d", i, j)
	}
}

// A run without an input has no /sbx/in and no media type variable, and
// the runner does not ask for one.
func TestRunWithoutInput(t *testing.T) {
	h := newHarness(t, nil)
	var spec executor.Spec
	h.ex.behave = func(c *fakeContainer) { spec = c.spec; c.exit(0, false) }
	a := newAssignment(pyManifest())
	h.coord.enqueue(a, mainPy)
	h.start()
	h.coord.waitFinish(t, a.RunID, testTimeout)
	for _, m := range spec.Mounts {
		if m.Target == inMount {
			t.Errorf("unexpected input mount %+v", m)
		}
	}
	for _, e := range spec.Env {
		if len(e) > 25 && e[:25] == "LERRYN_INPUT_CONTENT_TYPE" {
			t.Errorf("unexpected %q", e)
		}
	}
	if h.log.index("input ") >= 0 {
		t.Error("Input called for a run without input")
	}
}

// An input the server no longer hands out (the lease was lost) ends the
// run without starting a container.
func TestRunInputLeaseLost(t *testing.T) {
	h := newHarness(t, nil)
	a := newAssignment(pyManifest())
	a.HasInput = true
	h.coord.enqueue(a, mainPy)
	h.coord.mu.Lock()
	h.coord.lost[a.RunID] = true
	h.coord.mu.Unlock()
	h.start()
	waitFor(t, testTimeout, "slot freed", func() bool { return h.log.index("input ") >= 0 && h.r.activeCount() == 0 })
	if h.log.index("start ") >= 0 {
		t.Fatal("container started after the input was refused")
	}
	if _, err := os.Stat(filepath.Join(h.dir, runsDirName, a.RunID.String(), inDirName, inputFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("input file left behind: %v", err)
	}
}
