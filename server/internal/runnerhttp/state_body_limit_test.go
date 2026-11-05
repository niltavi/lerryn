package runnerhttp_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestStateHTTPRejectsOversizedBody(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	client := e.client(dir, staticToken)
	if err := client.Register(e.ctx, runproto.Registration{ID: "state-limit", Slots: 1}); err != nil {
		t.Fatal(err)
	}
	id := e.startRun()
	if claimed, err := client.Claim(e.ctx, "state-limit", 1); err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v, %v", claimed, err)
	}
	credential, err := os.ReadFile(filepath.Join(dir, "runner-credential")) // #nosec G304 -- the test client's credential file
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(e.ctx, http.MethodPut, e.srv.URL+"/runner/v1/runs/"+id.String()+"/state", strings.NewReader(strings.Repeat(" ", 17<<20)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(credential)))
	request.Header.Set("Lerryn-Runner-Protocol", strconv.Itoa(runproto.StateProtocolVersion))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var failure struct{ Code string }
	if err := json.NewDecoder(response.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusRequestEntityTooLarge || failure.Code != "rejected" {
		t.Fatalf("oversized state status=%d code=%s; want permanent rejection", response.StatusCode, failure.Code)
	}
	count, err := e.db.NewSelect().Model((*store.RunStateRef)(nil)).Where("run_id = ? AND role = ?", id, store.RunStateOutput).Count(e.ctx)
	if err != nil || count != 0 {
		t.Fatalf("oversized state created a candidate: count=%d, %v", count, err)
	}
}

func TestStateV1RegistrationCannotClaimWithV2Header(t *testing.T) {
	e := newEnv(t)
	credential, err := e.runners.Exchange(e.ctx, staticToken, runproto.Registration{ID: "legacy-state", Slots: 1}, 1)
	if err != nil {
		t.Fatal(err)
	}
	id := e.startRun()
	request, err := http.NewRequestWithContext(e.ctx, http.MethodPost, e.srv.URL+"/runner/v1/claim", strings.NewReader(`{"max":1}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("Lerryn-Runner-Protocol", strconv.Itoa(runproto.StateProtocolVersion))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("v1 registered runner claimed through v2 header: %d", response.StatusCode)
	}
	var run store.Run
	if err := e.db.NewSelect().Model(&run).Where("id = ?", id).Scan(e.ctx); err != nil {
		t.Fatal(err)
	}
	if run.Status != store.RunQueued {
		t.Fatal("v1 registered runner acquired a new lease")
	}
}

func TestStateHEADDoesNotStageCandidate(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	client := e.client(dir, staticToken)
	if err := client.Register(e.ctx, runproto.Registration{ID: "state-head", Slots: 1}); err != nil {
		t.Fatal(err)
	}
	id := e.startRun()
	if claimed, err := client.Claim(e.ctx, "state-head", 1); err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v, %v", claimed, err)
	}
	credential, err := os.ReadFile(filepath.Join(dir, "runner-credential")) // #nosec G304 -- the test client's credential file
	if err != nil {
		t.Fatal(err)
	}
	encoded, _, err := runproto.EncodeState(runproto.StateSnapshot{Version: runproto.StateFormatVersion, Entries: []runproto.StateEntry{{Path: "cursor", Content: []byte("HEAD must not upload")}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{nil, encoded} {
		request, err := http.NewRequestWithContext(e.ctx, http.MethodHead, e.srv.URL+"/runner/v1/runs/"+id.String()+"/state", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(credential)))
		request.Header.Set("Lerryn-Runner-Protocol", strconv.Itoa(runproto.StateProtocolVersion))
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("HEAD state status=%d, body bytes=%d", response.StatusCode, len(body))
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		count, err := e.db.NewSelect().Model((*store.RunStateRef)(nil)).Where("run_id = ? AND role = ?", id, store.RunStateOutput).Count(e.ctx)
		if err != nil || count != 0 {
			t.Errorf("HEAD state staged an output candidate: count=%d, %v", count, err)
		}
	}
}
