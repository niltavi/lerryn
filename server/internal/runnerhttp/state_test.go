package runnerhttp_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestTaskStateOverHTTP(t *testing.T) {
	e := newEnv(t)
	ownerDir := t.TempDir()
	owner := e.client(ownerDir, staticToken)
	other := e.client(t.TempDir(), staticToken)
	if err := owner.Register(e.ctx, runproto.Registration{ID: "state-owner", Slots: 3}); err != nil {
		t.Fatal(err)
	}
	if err := other.Register(e.ctx, runproto.Registration{ID: "state-other", Slots: 1}); err != nil {
		t.Fatal(err)
	}
	id := e.startRun()
	if _, err := owner.GetState(e.ctx, "state-owner", id); !errors.Is(err, runproto.ErrLeaseLost) {
		t.Fatalf("state before claim: %v", err)
	}
	assignments, err := owner.Claim(e.ctx, "state-owner", 1)
	if err != nil || len(assignments) != 1 || !assignments[0].HasState {
		t.Fatalf("state assignment: %+v, %v", assignments, err)
	}
	if _, err := other.GetState(e.ctx, "state-owner", id); !errors.Is(err, runproto.ErrLeaseLost) {
		t.Fatalf("another runner reads state: %v", err)
	}
	initial, err := owner.GetState(e.ctx, "state-owner", id)
	if err != nil || initial.Version != runproto.StateFormatVersion || len(initial.Entries) != 0 {
		t.Fatalf("initial state: %+v, %v", initial, err)
	}
	credential, err := os.ReadFile(filepath.Join(ownerDir, "runner-credential")) // #nosec G304 -- the test client's credential file
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(e.ctx, http.MethodGet, e.srv.URL+"/runner/v1/runs/"+id.String()+"/state", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(credential)))
	request.Header.Set("Lerryn-Runner-Protocol", strconv.Itoa(runproto.StateProtocolVersion))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("private state response is cacheable or unsuccessful")
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	output := runproto.StateSnapshot{Version: runproto.StateFormatVersion, Entries: []runproto.StateEntry{
		{Path: "目录", Directory: true}, {Path: "目录/cursor", Content: []byte{0, 1, 255}}, {Path: "empty", Directory: true},
	}}
	if err := owner.Started(e.ctx, "state-owner", id, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := owner.PutState(e.ctx, "state-owner", id, output); err != nil {
		t.Fatal(err)
	}
	if err := owner.PutState(e.ctx, "state-owner", id, output); err != nil {
		t.Fatalf("same candidate retry: %v", err)
	}
	if pinned, err := owner.GetState(e.ctx, "state-owner", id); err != nil || len(pinned.Entries) != 0 {
		t.Fatalf("output candidate changed pinned input: %+v, %v", pinned, err)
	}
	second, err := e.runs.Start(e.ctx, e.user, assignments[0].TaskID, service.StartRunInput{})
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := owner.Claim(e.ctx, "state-owner", 1); err != nil || len(claimed) != 1 || claimed[0].RunID != second.Run.ID {
		t.Fatalf("concurrent state assignment: %+v, %v", claimed, err)
	}
	zero := 0
	if err := owner.Finish(e.ctx, "state-owner", id, runproto.Finish{Status: runproto.StatusCompleted, ExitCode: &zero}); err != nil {
		t.Fatal(err)
	}
	if pinned, err := owner.GetState(e.ctx, "state-owner", second.Run.ID); err != nil || len(pinned.Entries) != 0 {
		t.Fatalf("published state changed an existing input pin: %+v, %v", pinned, err)
	}
	third, err := e.runs.Start(e.ctx, e.user, assignments[0].TaskID, service.StartRunInput{Test: true})
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := other.Claim(e.ctx, "state-other", 1); err != nil || len(claimed) != 1 || claimed[0].RunID != third.Run.ID {
		t.Fatalf("next runner assignment: %+v, %v", claimed, err)
	}
	pinned, err := other.GetState(e.ctx, "state-other", third.Run.ID)
	got, _, encodeErr := runproto.EncodeState(pinned)
	want, _, _ := runproto.EncodeState(output)
	if err != nil || encodeErr != nil || !bytes.Equal(got, want) {
		t.Fatalf("cross-runner published state: get=%v encode=%v", err, encodeErr)
	}
	if err := other.Started(e.ctx, "state-other", third.Run.ID, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := other.PutState(e.ctx, "state-other", third.Run.ID, initial); err != nil {
		t.Fatal(err)
	}
	if err := other.Finish(e.ctx, "state-other", third.Run.ID, runproto.Finish{Status: runproto.StatusCompleted, ExitCode: &zero}); err != nil {
		t.Fatalf("successful test run did not publish state: %v", err)
	}
	if err := owner.PutState(e.ctx, "state-owner", id, output); !errors.Is(err, runproto.ErrLeaseLost) {
		t.Fatalf("terminal run rewrote its candidate: %v", err)
	}
	var head store.TaskState
	if err := e.db.NewSelect().Model(&head).Where("task_id = ?", assignments[0].TaskID).Scan(e.ctx); err != nil {
		t.Fatal(err)
	}
	empty, _, _ := runproto.EncodeState(initial)
	if head.Size != 0 || head.BlobHash != blob.Sum(empty) {
		t.Fatal("empty successful output did not clear task state")
	}
}

func TestStateProtocolVersionGate(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	client := e.client(dir, staticToken)
	if err := client.Register(e.ctx, runproto.Registration{ID: "version-owner", Slots: 2}); err != nil {
		t.Fatal(err)
	}
	id := e.startRun()
	credential, err := os.ReadFile(filepath.Join(dir, "runner-credential")) // #nosec G304 -- the test client's credential file
	if err != nil {
		t.Fatal(err)
	}
	for _, protocol := range []int{1, runproto.StateProtocolVersion} {
		req, err := http.NewRequestWithContext(e.ctx, http.MethodPost, e.srv.URL+"/runner/v1/claim", strings.NewReader(`{"max":1}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(credential)))
		req.Header.Set("Lerryn-Runner-Protocol", strconv.Itoa(protocol))
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if protocol == 1 && response.StatusCode != http.StatusUnprocessableEntity || protocol == runproto.StateProtocolVersion && response.StatusCode != http.StatusOK {
			t.Fatalf("protocol %d claim status=%d", protocol, response.StatusCode)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		var run store.Run
		if err := e.db.NewSelect().Model(&run).Where("id = ?", id).Scan(e.ctx); err != nil {
			t.Fatal(err)
		}
		if protocol == 1 && run.Status != store.RunQueued {
			t.Fatal("v1 request claimed a run using a registered v2 credential")
		}
	}
	// The body must retain field presence until the canonical decoder
	// validates it; missing/null entries must not silently clear state.
	for _, body := range []string{`{"version":1}`, `{"version":1,"entries":null}`, `{"version":1,"entries":[],"extra":1}`} {
		req, err := http.NewRequestWithContext(e.ctx, http.MethodPut, e.srv.URL+"/runner/v1/runs/"+id.String()+"/state", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(credential)))
		req.Header.Set("Lerryn-Runner-Protocol", strconv.Itoa(runproto.StateProtocolVersion))
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var failure struct{ Code string }
		if err := json.NewDecoder(response.Body).Decode(&failure); err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusUnprocessableEntity || failure.Code != "rejected" {
			t.Fatal("malformed state was not permanently rejected")
		}
	}
}
