package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

type artifactAttemptCoordinator struct {
	*fakeCoordinator
	attempt func(context.Context, string, uuid.UUID, runproto.Artifact) error
}

func (c *artifactAttemptCoordinator) AddArtifact(ctx context.Context, runnerID string, runID uuid.UUID, a runproto.Artifact) error {
	return c.attempt(ctx, runnerID, runID, a)
}

func executeArtifactAssignment(t *testing.T, h *harness, ctx context.Context, a runproto.Assignment) {
	t.Helper()
	h.coord.enqueue(a, mainPy)
	claimed, err := h.coord.Claim(ctx, h.r.opts.ID, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("Claim = %v, %v", claimed, err)
	}
	if err := h.r.ensureLayout(); err != nil {
		t.Fatal(err)
	}
	run := h.r.track(claimed[0])
	defer h.r.untrack(run)
	h.r.execute(ctx, run)
	h.coord.checkProtocol(t)
}

func writeDeliveryFiles(t *testing.T, c *fakeContainer, files map[string]string) {
	t.Helper()
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(c.mountSource(outMount), path), []byte(content), 0o600); err != nil {
			t.Error(err)
		}
	}
}

// Retry beyond the old three-attempt cap, consuming the reader on every
// failed request. Already delivered paths are not resent while a later file retries.
func TestArtifactDeliveryRecovers(t *testing.T) {
	h := newHarness(t, nil)
	files := map[string]string{"a.txt": "first", "b.txt": strings.Repeat("whole file ", 100)}
	h.ex.behave = func(c *fakeContainer) {
		writeDeliveryFiles(t, c, files)
		c.exit(0, false)
	}
	calls := map[string]int{}
	h.r.coord = &artifactAttemptCoordinator{fakeCoordinator: h.coord, attempt: func(ctx context.Context, runnerID string, runID uuid.UUID, a runproto.Artifact) error {
		calls[a.Path]++
		data, err := io.ReadAll(a.Content)
		if err != nil || string(data) != files[a.Path] {
			t.Errorf("attempt %d for %s read %q: %v", calls[a.Path], a.Path, data, err)
		}
		if a.Path == "b.txt" && calls[a.Path] <= 4 {
			if f, ok := h.coord.finish(runID); ok {
				t.Errorf("Finish before eligible artifact delivery: %+v", f)
			}
			return errors.New("temporary upload failure")
		}
		a.Content = bytes.NewReader(data)
		return h.coord.AddArtifact(ctx, runnerID, runID, a)
	}}
	a := newAssignment(pyManifest())
	executeArtifactAssignment(t, h, t.Context(), a)
	if f, ok := h.coord.finish(a.RunID); !ok || f.Status != runproto.StatusCompleted {
		t.Fatalf("Finish = %+v, %t", f, ok)
	}
	if got := h.coord.runArtifacts(a.RunID); !reflect.DeepEqual(got, files) {
		t.Fatalf("artifacts = %v, want %v", got, files)
	}
	if want := map[string]int{"a.txt": 1, "b.txt": 5}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("attempts = %v, want %v", calls, want)
	}
	if specs := h.ex.startedSpecs(); len(specs) != 1 {
		t.Fatalf("upload retry reran the script: %d starts", len(specs))
	}
}

func TestArtifactDeliveryRunnerShutdown(t *testing.T) {
	for _, mode := range []string{"backoff", "in-flight"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, nil)
			// Cancel once collection is reached, independently of SDK extraction
			// and container preparation. Shutdown must leave data for reconciliation.
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls int
			h.r.coord = &artifactAttemptCoordinator{fakeCoordinator: h.coord, attempt: func(ctx context.Context, _ string, _ uuid.UUID, a runproto.Artifact) error {
				calls++
				if _, err := io.ReadAll(a.Content); err != nil {
					t.Error(err)
				}
				if calls == 1 {
					time.AfterFunc(30*time.Millisecond, cancel)
				}
				if mode == "in-flight" {
					<-ctx.Done()
					return ctx.Err()
				}
				return errors.New("temporary upload failure")
			}}
			h.ex.behave = func(c *fakeContainer) {
				writeDeliveryFiles(t, c, map[string]string{"a.txt": "keep for reconciliation"})
				c.exit(0, false)
			}
			a := newAssignment(pyManifest())
			executeArtifactAssignment(t, h, ctx, a)
			if f, ok := h.coord.finish(a.RunID); ok {
				t.Fatalf("cancelled report sent Finish: %+v", f)
			}
			if calls < 1 || mode == "in-flight" && calls != 1 {
				t.Fatalf("shutdown attempts = %d", calls)
			}
			if h.ex.wasRemoved("c1") {
				t.Fatal("runner shutdown removed the container before reconciliation")
			}
			if _, err := os.Stat(filepath.Join(h.dir, runsDirName, a.RunID.String(), outDirName, "a.txt")); err != nil {
				t.Fatalf("runner shutdown lost the pending artifact: %v", err)
			}
		})
	}
}

func TestArtifactDeliveryReportTimeout(t *testing.T) {
	h := newHarness(t, nil)
	h.r.reportTimeout = 150 * time.Millisecond
	h.ex.behave = func(c *fakeContainer) {
		writeDeliveryFiles(t, c, map[string]string{"a.txt": "pending"})
		c.exit(0, false)
	}
	calls := 0
	h.r.coord = &artifactAttemptCoordinator{fakeCoordinator: h.coord, attempt: func(ctx context.Context, _ string, _ uuid.UUID, _ runproto.Artifact) error {
		calls++
		if calls == 1 {
			return errors.New("temporary upload failure")
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	a := newAssignment(pyManifest())
	executeArtifactAssignment(t, h, t.Context(), a)
	if f, ok := h.coord.finish(a.RunID); ok {
		t.Fatalf("expired report sent Finish: %+v", f)
	}
	if calls < 1 || calls > 2 || !h.ex.wasRemoved("c1") {
		t.Fatalf("timeout attempts = %d, cleanup = %t", calls, h.ex.wasRemoved("c1"))
	}
}

func TestArtifactDeliveryLeaseLost(t *testing.T) {
	for _, mode := range []string{"response", "heartbeat during request", "heartbeat during backoff"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, nil)
			h.r.retryBase = 100 * time.Millisecond
			h.r.retryMax = 100 * time.Millisecond
			h.ex.behave = func(c *fakeContainer) {
				writeDeliveryFiles(t, c, map[string]string{"a.txt": "pending", "b.txt": "must not send"})
				c.exit(0, false)
			}
			var calls atomic.Int32
			lost := make(chan struct{})
			var once atomic.Bool
			h.r.coord = &artifactAttemptCoordinator{fakeCoordinator: h.coord, attempt: func(ctx context.Context, _ string, runID uuid.UUID, _ runproto.Artifact) error {
				calls.Add(1)
				switch mode {
				case "response":
					return runproto.ErrLeaseLost
				case "heartbeat during request":
					h.coord.markLost(runID)
					h.r.heartbeat(ctx)
					close(lost)
					// An in-flight upload can still return nil after the
					// heartbeat revoked its lease; it must not allow Finish.
					return nil
				default:
					if once.CompareAndSwap(false, true) {
						time.AfterFunc(time.Millisecond, func() {
							h.coord.markLost(runID)
							h.r.heartbeat(t.Context())
							close(lost)
						})
					}
					return errors.New("temporary upload failure")
				}
			}}
			a := newAssignment(pyManifest())
			executeArtifactAssignment(t, h, t.Context(), a)
			if mode != "response" {
				<-lost
			}
			if f, ok := h.coord.finish(a.RunID); ok {
				t.Fatalf("lost lease sent Finish: %+v", f)
			}
			if calls.Load() != 1 || !h.ex.wasRemoved("c1") {
				t.Fatalf("lost lease attempts = %d, cleanup = %t", calls.Load(), h.ex.wasRemoved("c1"))
			}
		})
	}
}

func TestArtifactDeliveryKeepsExecutionVerdict(t *testing.T) {
	for _, status := range []string{runproto.StatusCancelled, runproto.StatusTimedOut} {
		t.Run(status, func(t *testing.T) {
			h := newHarness(t, nil)
			m := pyManifest()
			if status == runproto.StatusTimedOut {
				m.Timeout = 10 * time.Millisecond
			}
			h.ex.behave = func(c *fakeContainer) {
				writeDeliveryFiles(t, c, map[string]string{"a.txt": "partial output"})
				if status == runproto.StatusCancelled {
					runID := uuid.MustParse(c.spec.RunID)
					h.coord.requestCancel(runID)
					h.r.heartbeat(t.Context())
				}
				c.exitOnStop()
			}
			calls := 0
			h.r.coord = &artifactAttemptCoordinator{fakeCoordinator: h.coord, attempt: func(ctx context.Context, runnerID string, runID uuid.UUID, a runproto.Artifact) error {
				calls++
				if calls <= 4 {
					return errors.New("temporary upload failure")
				}
				return h.coord.AddArtifact(ctx, runnerID, runID, a)
			}}
			a := newAssignment(m)
			executeArtifactAssignment(t, h, t.Context(), a)
			if f, ok := h.coord.finish(a.RunID); !ok || f.Status != status {
				t.Fatalf("Finish = %+v, %t; want %s", f, ok, status)
			}
			if got := h.coord.runArtifacts(a.RunID); got["a.txt"] != "partial output" || calls != 5 {
				t.Fatalf("artifacts = %v, attempts = %d", got, calls)
			}
		})
	}
}

func TestArtifactDeliveryOutputDirectory(t *testing.T) {
	for _, mode := range []string{"empty", "deleted", "replaced"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, nil)
			h.r.reportTimeout = 150 * time.Millisecond
			if mode == "empty" {
				h.r.reportTimeout = time.Minute
			}
			h.r.retryBase = 10 * time.Millisecond
			h.r.retryMax = 10 * time.Millisecond
			h.ex.behave = func(c *fakeContainer) {
				out := c.mountSource(outMount)
				if mode != "empty" {
					if err := os.Remove(out); err != nil {
						t.Error(err)
					}
				}
				if mode == "replaced" {
					if err := os.WriteFile(out, []byte("not a directory"), 0o600); err != nil {
						t.Error(err)
					}
				}
				c.exit(0, false)
			}
			a := newAssignment(pyManifest())
			executeArtifactAssignment(t, h, t.Context(), a)
			f, finished := h.coord.finish(a.RunID)
			if finished != (mode == "empty") || finished && f.Status != runproto.StatusCompleted {
				t.Fatalf("Finish = %+v, %t; output directory = %s", f, finished, mode)
			}
			if !h.ex.wasRemoved("c1") {
				t.Fatal("report did not clean up the stopped container")
			}
		})
	}
}

func TestArtifactDeliveryIntentionalSkips(t *testing.T) {
	h := newHarness(t, nil)
	h.ex.behave = func(c *fakeContainer) {
		out := c.mountSource(outMount)
		oversized := filepath.Join(out, "000-too-large")
		f, err := os.Create(oversized) // #nosec G304 -- a sparse test file in the harness's temporary output directory
		if err != nil {
			t.Error(err)
		} else {
			if err := f.Truncate(runproto.MaxArtifactBytes + 1); err != nil {
				t.Error(err)
			}
			_ = f.Close()
		}
		for i := 0; i <= runproto.MaxArtifacts; i++ {
			writeDeliveryFiles(t, c, map[string]string{fmt.Sprintf("file-%03d", i): "ok"})
		}
		if err := os.Symlink(oversized, filepath.Join(out, "001-symlink")); err != nil {
			t.Error(err)
		}
		c.exit(0, false)
	}
	a := newAssignment(pyManifest())
	executeArtifactAssignment(t, h, t.Context(), a)
	if f, ok := h.coord.finish(a.RunID); !ok || f.Status != runproto.StatusCompleted {
		t.Fatalf("Finish = %+v, %t", f, ok)
	}
	got := h.coord.runArtifacts(a.RunID)
	if len(got) != runproto.MaxArtifacts || got["file-099"] != "ok" {
		t.Fatalf("kept artifacts = %d, last = %q", len(got), got["file-099"])
	}
	logs := strings.Join(h.coord.logTexts(a.RunID), "\n")
	for _, path := range []string{"000-too-large", "001-symlink", "file-100"} {
		if !strings.Contains(logs, "artifact "+path+" skipped") {
			t.Errorf("no intentional-skip warning for %s", path)
		}
	}
}

func TestArtifactDeliveryPermanentRejection(t *testing.T) {
	h := newHarness(t, nil)
	h.ex.behave = func(c *fakeContainer) {
		writeDeliveryFiles(t, c, map[string]string{"a.txt": "rejected"})
		c.exit(0, false)
	}
	calls := 0
	h.r.coord = &artifactAttemptCoordinator{fakeCoordinator: h.coord, attempt: func(context.Context, string, uuid.UUID, runproto.Artifact) error {
		calls++
		return fmt.Errorf("%w: invalid artifact", runproto.ErrRejected)
	}}
	a := newAssignment(pyManifest())
	executeArtifactAssignment(t, h, t.Context(), a)
	if f, ok := h.coord.finish(a.RunID); ok {
		t.Fatalf("permanent rejection sent Finish: %+v", f)
	}
	if calls != 1 || !h.ex.wasRemoved("c1") {
		t.Fatalf("rejection attempts = %d, cleanup = %t", calls, h.ex.wasRemoved("c1"))
	}
	if logs := strings.Join(h.coord.logTexts(a.RunID), "\n"); !strings.Contains(logs, "could not upload artifact a.txt") {
		t.Fatalf("permanent rejection lost its task warning: %s", logs)
	}
}

func TestArtifactDeliveryAlreadyCancelled(t *testing.T) {
	h := newHarness(t, nil)
	a := newAssignment(pyManifest())
	run := h.r.track(a)
	defer h.r.untrack(run)
	stages := &stageTracker{}
	logs := newLogPipeline(h.r.limits, stages, nil, h.r.now)
	x := &runExec{
		r: h.r, run: run, dir: t.TempDir(), log: h.r.log, logs: logs,
		events: newEventReader(t.TempDir(), stages, logs, h.r.now),
	}
	defer x.events.close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := x.uploadArtifacts(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("uploadArtifacts on a cancelled context = %v", err)
	}
	x.report(ctx, runproto.Finish{Status: runproto.StatusCompleted})
	if h.log.index("finish") >= 0 {
		t.Fatal("report called Finish after its context was already cancelled")
	}
}
