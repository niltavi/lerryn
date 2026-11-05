//go:build integration

package runner

import (
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

func TestIntegrationWritableTaskState(t *testing.T) {
	h := newDockerHarness(t, "", "", nil)
	a := newAssignment(itManifest())
	a.HasState = true
	h.coord.stateInputs[a.RunID] = stateSnapshot(
		runproto.StateEntry{Path: "nested", Directory: true},
		runproto.StateEntry{Path: "nested/cursor", Content: []byte("before")},
	)
	h.coord.enqueue(a, pyFiles(`
from pathlib import Path
p = Path("/sbx/state/nested/cursor")
assert p.read_text() == "before"
p.write_text("after")
Path("/sbx/state/empty").mkdir()
`))
	h.start()
	requireCompleted(t, h, a.RunID, 2*time.Minute)
	h.coord.mu.Lock()
	snapshot := h.coord.stateOutputs[a.RunID]
	h.coord.mu.Unlock()
	seenCursor, seenEmpty := false, false
	for _, entry := range snapshot.Entries {
		if entry.Path == "nested/cursor" && string(entry.Content) == "after" {
			seenCursor = true
		}
		if entry.Path == "empty" && entry.Directory {
			seenEmpty = true
		}
	}
	if !seenCursor || !seenEmpty {
		t.Fatal("task UID could not update the downloaded state")
	}
}
