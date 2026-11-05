//go:build unix

package runner

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

func TestStateRejectsFIFO(t *testing.T) {
	x := stateExec(t)
	if err := x.restoreState(t.Context(), stateSnapshot()); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(x.dir, stateDirName, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := x.collectState(t.Context()); !errors.Is(err, runproto.ErrRejected) {
		t.Fatalf("state collection accepted a FIFO: %v", err)
	}
}
