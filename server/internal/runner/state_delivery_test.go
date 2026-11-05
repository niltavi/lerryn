package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

type stateFaultCoordinator struct {
	*fakeCoordinator
	get func(context.Context, string, uuid.UUID) (runproto.StateSnapshot, error)
	put func(context.Context, string, uuid.UUID, runproto.StateSnapshot) error
}

func (c *stateFaultCoordinator) GetState(ctx context.Context, runner string, id uuid.UUID) (runproto.StateSnapshot, error) {
	if c.get != nil {
		return c.get(ctx, runner, id)
	}
	return c.fakeCoordinator.GetState(ctx, runner, id)
}

func (c *stateFaultCoordinator) PutState(ctx context.Context, runner string, id uuid.UUID, snapshot runproto.StateSnapshot) error {
	if c.put != nil {
		return c.put(ctx, runner, id, snapshot)
	}
	return c.fakeCoordinator.PutState(ctx, runner, id, snapshot)
}

func TestStateRunnerMountAndCompleteSnapshot(t *testing.T) {
	h := newHarness(t, nil)
	a := newAssignment(pyManifest())
	a.HasState = true
	h.coord.stateInputs[a.RunID] = stateSnapshot(
		runproto.StateEntry{Path: "empty", Directory: true},
		runproto.StateEntry{Path: "cursor", Content: []byte("before")},
		runproto.StateEntry{Path: "old", Content: []byte("remove")},
	)
	h.ex.behave = func(c *fakeContainer) {
		state := c.mountSource(stateMount)
		for _, mount := range c.spec.Mounts {
			if mount.Target == stateMount && mount.ReadOnly {
				t.Error("task state mount is read-only")
			}
		}
		data, err := os.ReadFile(filepath.Join(state, "cursor")) // #nosec G304 -- the harness's private state mount
		if err != nil || string(data) != "before" {
			t.Error("the pinned state was not installed before Start")
		}
		if err := os.WriteFile(filepath.Join(state, "cursor"), []byte("after"), 0o600); err != nil {
			t.Error(err)
		}
		if err := os.Remove(filepath.Join(state, "old")); err != nil {
			t.Error(err)
		}
		writeDeliveryFiles(t, c, map[string]string{"report": "artifact"})
		c.exit(0, false)
	}
	executeArtifactAssignment(t, h, t.Context(), a)
	got, _, err := runproto.EncodeState(h.coord.stateOutputs[a.RunID])
	want, _, _ := runproto.EncodeState(stateSnapshot(runproto.StateEntry{Path: "empty", Directory: true}, runproto.StateEntry{Path: "cursor", Content: []byte("after")}))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("complete output state differs: %v", err)
	}
	if h.log.index("state get") > h.log.index("start ") || h.log.index("state put") < h.log.index("artifact ") || h.log.index("state put") > h.log.index("finish ") {
		t.Fatalf("state protocol order: %v", h.log.snapshot())
	}
}

func TestStatePutRetriesStableSnapshot(t *testing.T) {
	h := newHarness(t, nil)
	a := newAssignment(pyManifest())
	a.HasState = true
	h.ex.behave = func(c *fakeContainer) {
		if err := os.WriteFile(filepath.Join(c.mountSource(stateMount), "cursor"), []byte("frozen"), 0o600); err != nil {
			t.Error(err)
		}
		c.exit(0, false)
	}
	attempts := 0
	var first []byte
	h.r.coord = &stateFaultCoordinator{fakeCoordinator: h.coord, put: func(ctx context.Context, runner string, id uuid.UUID, snapshot runproto.StateSnapshot) error {
		attempts++
		encoded, _, err := runproto.EncodeState(snapshot)
		if err != nil {
			return err
		}
		if first == nil {
			first = encoded
			// Retrying a ready candidate must not rescan a mutable local
			// directory and send a different hash for the same run.
			if err := os.WriteFile(filepath.Join(h.dir, runsDirName, id.String(), stateDirName, "cursor"), []byte("late mutation"), 0o600); err != nil {
				t.Error(err)
			}
		} else if !bytes.Equal(first, encoded) {
			t.Error("state retry changed the candidate snapshot")
		}
		if attempts <= 4 {
			return errors.New("temporary state upload failure")
		}
		return h.coord.PutState(ctx, runner, id, snapshot)
	}}
	executeArtifactAssignment(t, h, t.Context(), a)
	if finish, ok := h.coord.finish(a.RunID); !ok || finish.Status != runproto.StatusCompleted || attempts != 5 {
		t.Fatalf("state delivery recovery: finish=%+v present=%t attempts=%d", finish, ok, attempts)
	}
	if len(h.ex.startedSpecs()) != 1 {
		t.Fatal("state retry reran the script")
	}
}

func TestStateGetRecoversBeforeStart(t *testing.T) {
	h := newHarness(t, nil)
	a := newAssignment(pyManifest())
	a.HasState = true
	attempts := 0
	h.r.coord = &stateFaultCoordinator{fakeCoordinator: h.coord, get: func(ctx context.Context, runner string, id uuid.UUID) (runproto.StateSnapshot, error) {
		attempts++
		if attempts < 3 {
			return runproto.StateSnapshot{}, errors.New("temporary state download failure")
		}
		return h.coord.GetState(ctx, runner, id)
	}}
	h.ex.behave = func(c *fakeContainer) {
		if attempts != 3 {
			t.Error("execution started before the state input recovered")
		}
		c.exit(0, false)
	}
	executeArtifactAssignment(t, h, t.Context(), a)
	if finish, ok := h.coord.finish(a.RunID); !ok || finish.Status != runproto.StatusCompleted || len(h.ex.startedSpecs()) != 1 {
		t.Fatal("state download recovery did not complete exactly one execution")
	}
}

func TestStatePutShutdownPreservesLocalState(t *testing.T) {
	h := newHarness(t, nil)
	a := newAssignment(pyManifest())
	a.HasState = true
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h.ex.behave = func(c *fakeContainer) {
		if err := os.WriteFile(filepath.Join(c.mountSource(stateMount), "cursor"), []byte("pending"), 0o600); err != nil {
			t.Error(err)
		}
		c.exit(0, false)
	}
	calls := 0
	h.r.coord = &stateFaultCoordinator{fakeCoordinator: h.coord, put: func(ctx context.Context, _ string, _ uuid.UUID, _ runproto.StateSnapshot) error {
		calls++
		cancel()
		return ctx.Err()
	}}
	executeArtifactAssignment(t, h, ctx, a)
	if calls != 1 || h.log.index("finish ") >= 0 || h.ex.wasRemoved("c1") {
		t.Fatal("runner shutdown changed state retry, Finish or reconciliation cleanup")
	}
	if _, err := os.Stat(filepath.Join(h.dir, runsDirName, a.RunID.String(), stateDirName, "cursor")); err != nil {
		t.Fatal("runner shutdown discarded its pending local state")
	}
}

func TestStatePublishFollowsExecutionVerdict(t *testing.T) {
	for _, mode := range []string{"completed", "result failed", "result partial", "empty", "failed", "oom", "cancelled", "timed out"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, nil)
			m := pyManifest()
			if mode == "timed out" {
				m.Timeout = 5 * time.Millisecond
			}
			a := newAssignment(m)
			a.HasState = true
			h.ex.behave = func(c *fakeContainer) {
				if mode != "empty" {
					if err := os.WriteFile(filepath.Join(c.mountSource(stateMount), "cursor"), []byte("changed"), 0o600); err != nil {
						t.Error(err)
					}
				}
				switch mode {
				case "result failed", "result partial":
					c.event(`{"v":1,"kind":"result","status":"` + strings.TrimPrefix(mode, "result ") + `","summary":"business outcome"}`)
					c.exit(0, false)
				case "failed":
					c.exit(7, false)
				case "oom":
					c.exit(137, true)
				case "cancelled":
					h.r.lookup(a.RunID).requestCancel()
					c.exitOnStop()
				case "timed out":
					c.exitOnStop()
				default:
					c.exit(0, false)
				}
			}
			executeArtifactAssignment(t, h, t.Context(), a)
			finish, ok := h.coord.finish(a.RunID)
			if !ok {
				t.Fatal("run did not finish")
			}
			_, published := h.coord.stateOutputs[a.RunID]
			if published != (finish.Status == runproto.StatusCompleted) {
				t.Fatalf("state publication=%t for status %s", published, finish.Status)
			}
			if mode == "empty" && len(h.coord.stateOutputs[a.RunID].Entries) != 0 {
				t.Fatal("an empty directory did not publish an explicit empty snapshot")
			}
		})
	}
}

func TestStateDeliveryFailuresDoNotFinish(t *testing.T) {
	for _, mode := range []string{"unsafe", "deleted", "rejected", "deadline", "lease"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, nil)
			h.r.reportTimeout = 150 * time.Millisecond
			a := newAssignment(pyManifest())
			a.HasState = true
			h.ex.behave = func(c *fakeContainer) {
				state := c.mountSource(stateMount)
				switch mode {
				case "unsafe":
					if err := os.Symlink("missing", filepath.Join(state, "link")); err != nil {
						t.Error(err)
					}
				case "deleted":
					if err := os.Remove(state); err != nil {
						t.Error(err)
					}
				}
				c.exit(0, false)
			}
			calls := 0
			h.r.coord = &stateFaultCoordinator{fakeCoordinator: h.coord, put: func(ctx context.Context, _ string, id uuid.UUID, _ runproto.StateSnapshot) error {
				calls++
				switch mode {
				case "rejected":
					return fmt.Errorf("%w: invalid candidate", runproto.ErrRejected)
				case "lease":
					h.r.lookup(id).markLost()
					return nil
				default:
					<-ctx.Done()
					return ctx.Err()
				}
			}}
			executeArtifactAssignment(t, h, t.Context(), a)
			if h.log.index("finish ") >= 0 {
				t.Fatal("incomplete state submitted a terminal report")
			}
			if (mode == "unsafe" || mode == "deleted") && calls != 0 {
				t.Fatal("incomplete state was sent to the coordinator")
			}
			if (mode == "rejected" || mode == "lease") && calls != 1 {
				t.Fatalf("permanent or lease error attempts=%d", calls)
			}
			if !h.ex.wasRemoved("c1") {
				t.Fatal("failed state reporting did not clean up the stopped container")
			}
		})
	}
}

func TestStatePreparationFailureRedactsPaths(t *testing.T) {
	credential := "synthetic-state-private-value-123456789"
	var host bytes.Buffer
	h := newHarness(t, func(o *Options) { o.Logger = slog.New(slog.NewJSONHandler(&host, nil)) })
	m := pyManifest()
	m.Credentials = []runproto.CredentialRef{{Name: "STATE", As: runproto.InjectEnv}}
	a := newAssignment(m)
	a.HasState = true
	h.coord.secrets[a.RunID] = []runproto.Secret{{Name: "STATE", As: runproto.InjectEnv, Fields: []runproto.SecretField{{Name: "TOKEN", Value: credential, Sensitive: true}}}}
	h.r.coord = &stateFaultCoordinator{fakeCoordinator: h.coord, get: func(context.Context, string, uuid.UUID) (runproto.StateSnapshot, error) {
		return runproto.StateSnapshot{}, fmt.Errorf("%w: state path %s failed", runproto.ErrRejected, credential)
	}}
	executeArtifactAssignment(t, h, t.Context(), a)
	finish, ok := h.coord.finish(a.RunID)
	if !ok || finish.Status != runproto.StatusFailed || len(h.ex.startedSpecs()) != 0 {
		t.Fatal("unavailable state did not stop execution in preparation")
	}
	if strings.Contains(finish.ErrorMessage, credential) || !strings.Contains(finish.ErrorMessage, redactedMark) {
		t.Error("state preparation failure leaked the synthetic credential")
	}
	var entries []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(host.Bytes()))
	for decoder.More() {
		var entry map[string]any
		if err := decoder.Decode(&entry); err != nil {
			t.Fatal("invalid captured host JSON")
		}
		entries = append(entries, entry)
		for _, value := range entry {
			if text, ok := value.(string); ok && strings.Contains(text, credential) {
				t.Error("host state preparation failure leaked the synthetic credential")
			}
		}
	}
	if len(entries) == 0 || !strings.Contains(host.String(), redactedMark) {
		t.Fatal("host redaction boundary was not exercised")
	}
}
