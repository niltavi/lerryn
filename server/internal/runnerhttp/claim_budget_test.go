package runnerhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

type budgetRoundTrip func(*http.Request) (*http.Response, error)

func (f budgetRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// maximumWireAssignment covers the public manifest and script file limits,
// including the six-byte JSON escaping of '<'. Content slices are shared so
// the fixture does not allocate twenty copies of each tool's MiB of files.
func maximumWireAssignment(t *testing.T) assignment {
	t.Helper()
	m := runproto.Manifest{
		Runtime: runproto.Runtime{Language: "node", Version: "22", Lockfile: "package-lock.json"},
		Timeout: 24 * time.Hour, MilliCPUs: 8000, MemoryBytes: 16 << 30, Network: "full",
	}
	for range 32 {
		m.Entrypoint = append(m.Entrypoint, strings.Repeat("<", 1024))
	}
	var after []string
	for i := range 50 {
		id := fmt.Sprintf("stage%02d", i) + strings.Repeat("a", 56)
		m.Stages = append(m.Stages, runproto.Stage{ID: id, Name: strings.Repeat("<", 100), After: append([]string(nil), after...)})
		after = append(after, id)
	}
	content := bytes.Repeat([]byte("x"), (1<<20)/100+1)
	for i := range 20 {
		m.Credentials = append(m.Credentials, runproto.CredentialRef{Name: fmt.Sprintf("C%02d", i) + strings.Repeat("A", 61), As: "file", Optional: true})
		tool := runproto.Tool{Name: fmt.Sprintf("tool-%02d", i) + strings.Repeat("a", 56), Kind: runproto.ToolScript, TreeHash: strings.Repeat("a", 64)}
		for j := range 100 {
			size := (1 << 20) / 100
			if j < (1<<20)%100 {
				size++
			}
			tool.Files = append(tool.Files, runproto.File{
				Path: fmt.Sprintf("%03d-", j) + strings.Repeat("<", 251), Content: content[:size], Executable: true,
			})
		}
		m.Tools = append(m.Tools, tool)
	}
	params, err := json.Marshal(map[string]string{"value": strings.Repeat("<", ((16<<10)-12)/6)})
	if err != nil {
		t.Fatal(err)
	}
	return toWireAssignment(runproto.Assignment{RunID: uuid.New(), TaskID: uuid.New(), VersionID: uuid.New(),
		Manifest: m, Params: params, HasInput: true})
}

func TestClaimBudgetForMaximumAssignments(t *testing.T) {
	wire := maximumWireAssignment(t)
	one, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	responseSize := func(count int) int {
		return len(`{"assignments":[]}`) + count*len(one) + max(count-1, 0) + 1 // encoder's trailing newline
	}
	if responseSize(10) <= maxResponseBytes {
		t.Fatal("fixture no longer reproduces the oversized ten-run claim")
	}
	if size := responseSize(maxClaimBatch); size > maxResponseBytes {
		t.Fatalf("eight maximum assignments need %d bytes, beyond %d; lower the HTTP claim cap", size, maxResponseBytes)
	}
	t.Logf("maximum assignment: %d bytes; %d-run response: %d of %d bytes", len(one), maxClaimBatch, responseSize(maxClaimBatch), maxResponseBytes)
	client, err := NewClient(ClientOptions{
		ServerURL: "https://lerryn.example.com", RegistrationToken: "registration", CredentialFile: filepath.Join(t.TempDir(), "credential"),
		HTTPClient: &http.Client{Transport: budgetRoundTrip(func(req *http.Request) (*http.Response, error) {
			var claim claimRequest
			if err := json.NewDecoder(req.Body).Decode(&claim); err != nil {
				t.Fatal(err)
			}
			if claim.Max != maxClaimBatch {
				t.Errorf("client requested %d runs, want at most eight even from an older server", claim.Max)
			}
			body := io.MultiReader(strings.NewReader(`{"assignments":[`), bytes.NewReader(one), strings.NewReader("]}"))
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(body)}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Claim(t.Context(), "runner", 64)
	if err != nil || len(got) != 1 || got[0].RunID != wire.RunID || len(got[0].Manifest.Tools) != 20 {
		t.Fatalf("maximum single assignment was lost: %d assignments, %v", len(got), err)
	}
	for _, tool := range got[0].Manifest.Tools {
		size := 0
		for _, f := range tool.Files {
			size += len(f.Content)
		}
		if len(tool.Files) != 100 || size != 1<<20 {
			t.Fatalf("maximum tool changed: %d files, %d bytes", len(tool.Files), size)
		}
	}
}

func TestClientRejectsDeclaredOversizedResponse(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNoContent, http.StatusUnauthorized} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			client, err := NewClient(ClientOptions{
				ServerURL: "https://lerryn.example.com", RegistrationToken: "registration", CredentialFile: filepath.Join(t.TempDir(), "credential"),
				HTTPClient: &http.Client{Transport: budgetRoundTrip(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: status, ContentLength: maxResponseBytes + 1, Body: io.NopCloser(strings.NewReader(""))}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			err = client.do(context.Background(), http.MethodPost, pathBeat, "", nil, nil, 0)
			if err == nil || !strings.Contains(err.Error(), "response is larger") || errors.Is(err, errUnauthenticated) || errors.Is(err, runproto.ErrRejected) {
				t.Fatalf("oversized response error = %v; want a transient size error", err)
			}
		})
	}
}

type countedResponseBody struct {
	io.Reader
	read int
}

func (r *countedResponseBody) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

func TestReadResponseBodyLimit(t *testing.T) {
	const limit = 32
	for _, tt := range []struct {
		name      string
		size      int
		declared  int64
		wantError bool
	}{
		{name: "exact length", size: limit, declared: limit},
		{name: "chunked at limit", size: limit, declared: -1},
		{name: "chunked too large", size: 1000, declared: -1, wantError: true},
		{name: "incorrect short length", size: 1000, declared: 2, wantError: true},
		{name: "declared too large", size: 1000, declared: limit + 1, wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &countedResponseBody{Reader: strings.NewReader(strings.Repeat(" ", tt.size))}
			resp := &http.Response{ContentLength: tt.declared, Body: io.NopCloser(r)}
			data, err := readResponseBody(resp, limit)
			if (err != nil) != tt.wantError {
				t.Fatalf("read response = %d bytes, %v", len(data), err)
			}
			if r.read > limit+1 || tt.declared > limit && r.read != 0 {
				t.Fatalf("read %d bytes beyond the response budget", r.read)
			}
		})
	}
}
