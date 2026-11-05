//go:build unix

package runner

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestEventReaderRefusesNonRegularFiles(t *testing.T) {
	t.Run("symlink out of the directory", func(t *testing.T) {
		r, logs, _, path := newTestEventReader(t)
		defer r.close()
		outside := filepath.Join(t.TempDir(), "host-file")
		appendFile(t, outside, `{"kind":"stage.start","stage":"leak"}`+"\n")
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		r.poll(true)
		if len(r.pending) != 0 {
			t.Fatalf("read through a symlink: %+v", r.pending)
		}
		if w := texts(mustDrain(t, logs)); len(w) != 1 || !strings.Contains(w[0], "cannot open the events file") {
			t.Fatalf("warnings = %q", w)
		}
	})
	t.Run("fifo", func(t *testing.T) {
		r, logs, _, path := newTestEventReader(t)
		defer r.close()
		if err := syscall.Mkfifo(path, 0o644); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			r.poll(true)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("poll blocked on a FIFO")
		}
		if w := texts(mustDrain(t, logs)); len(w) != 1 || !strings.Contains(w[0], "not a regular file") {
			t.Fatalf("warnings = %q", w)
		}
	})
}
