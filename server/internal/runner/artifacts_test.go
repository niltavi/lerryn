package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

// Files the task leaves in /sbx/out reach the server before Finish; links
// and anything else that is not a regular file are skipped, so a task
// cannot make the runner read files outside its output directory.
func TestArtifactsUploadedBeforeFinish(t *testing.T) {
	h := newHarness(t, nil)
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("host file"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.ex.behave = func(c *fakeContainer) {
		out := c.mountSource(outMount)
		must := func(err error) {
			if err != nil {
				t.Error(err)
			}
		}
		must(os.WriteFile(filepath.Join(out, "report.json"), []byte(`{"ok":true}`), 0o600))
		must(os.MkdirAll(filepath.Join(out, "charts"), 0o750))
		must(os.WriteFile(filepath.Join(out, "charts", "a.csv"), []byte("x,y\n1,2\n"), 0o600))
		must(os.Symlink(secret, filepath.Join(out, "leak")))
		must(os.Symlink(filepath.Dir(secret), filepath.Join(out, "leakdir")))
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
	got := h.coord.runArtifacts(a.RunID)
	if len(got) != 2 || got["report.json"] != `{"ok":true}` || got["charts/a.csv"] != "x,y\n1,2\n" {
		t.Fatalf("artifacts = %v", got)
	}
	if iArt, iFinish := h.log.index("artifact"), h.log.index("finish"); iArt < 0 || iArt > iFinish {
		t.Fatalf("call order = %v", h.log.snapshot())
	}
	logs := strings.Join(h.coord.logTexts(a.RunID), "\n")
	for _, name := range []string{"leak", "leakdir"} {
		if !strings.Contains(logs, "artifact "+name+" skipped") {
			t.Errorf("no log line about skipping %s:\n%s", name, logs)
		}
	}
}

// A run whose process never started has no output directory to collect.
func TestNoArtifactsWithoutContainer(t *testing.T) {
	h := newHarness(t, nil)
	m := pyManifest()
	m.Network = "full"
	h.r.allowed = map[string]bool{"none": true}
	a := newAssignment(m)
	h.coord.enqueue(a, mainPy)
	h.start()
	h.coord.waitFinish(t, a.RunID, testTimeout)
	if got := h.coord.runArtifacts(a.RunID); len(got) != 0 {
		t.Fatalf("artifacts = %v", got)
	}
}
