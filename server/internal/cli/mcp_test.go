package cli

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/niltavi/lerryn/server/internal/mcp"
)

// proxySession serves the stdio proxy over an in-memory transport against
// server and returns the client side.
func proxySession(t *testing.T, server string) *mcpsdk.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	reg, err := mcp.Load(ctx, []byte(testSpec))
	if err != nil {
		t.Fatal(err)
	}
	c, err := newClient(server, testToken, nil, reg)
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcpsdk.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- serveMCP(ctx, c, st, slog.New(slog.DiscardHandler)) }()
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cs.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serveMCP: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("serveMCP did not return after the client closed")
		}
	})
	return cs
}

func TestStdioProxy(t *testing.T) {
	srv := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req_abc")
		if strings.HasSuffix(r.URL.Path, "/missing") {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":"not_found","title":"运行不存在","status":404}`)
			return
		}
		jsonHandler(`{"id":"r1","status":"running"}`)(w, r)
	})
	cs := proxySession(t, srv.URL+"/prefix")
	ctx := context.Background()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	// The same tool set as /mcp: the contract's tools plus the generic two.
	if got := strings.Join(names, " "); got != "api_call api_describe run_get" {
		t.Fatalf("tools = %s", got)
	}

	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "run_get", Arguments: map[string]any{"run_id": "r1", "wait": 5}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.StructuredContent.(map[string]any)["status"] != "running" {
		t.Fatalf("run_get = %+v", res)
	}
	got := srv.last(t)
	if got.URI != "/prefix/api/v1/runs/r1?wait=5" || got.Auth != "Bearer "+testToken {
		t.Fatalf("server saw %+v", got)
	}

	res, err = cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "api_call", Arguments: map[string]any{
		"operation_id": "getWidget", "arguments": map[string]any{"widget_id": "w1", "tag": []string{"x"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || srv.last(t).URI != "/prefix/api/v1/widgets/w1?tag=x" {
		t.Fatalf("api_call = %+v, server saw %s", res, srv.last(t).URI)
	}

	// Problems from the server stay tool errors with their code.
	res, err = cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "run_get", Arguments: map[string]any{"run_id": "missing"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || res.StructuredContent.(map[string]any)["code"] != "not_found" {
		t.Fatalf("missing = %+v", res)
	}
}

func TestStdioProxyUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	cs := proxySession(t, url)
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "run_get", Arguments: map[string]any{"run_id": "r1"}})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := res.StructuredContent.(map[string]any)
	if !res.IsError || p["status"] != float64(http.StatusBadGateway) || p["code"] != "internal_error" {
		t.Fatalf("result = %+v", res)
	}
}

// The proxy authenticates with the CLI's own token only. Requests that
// reach it carry no client credential (stdio has no headers), and even an
// Authorization header on the in-process request is replaced.
func TestRemoteAPIUsesOwnToken(t *testing.T) {
	srv := newStub(t, jsonHandler(`{}`))
	reg, err := mcp.Load(context.Background(), []byte(testSpec))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := newClient(srv.URL, testToken, nil, reg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/r1", nil)
	req.Header.Set("Authorization", "Bearer wl_pat_someone_else")
	req.Header.Set("Cookie", "lerryn_session=stolen")
	rec := httptest.NewRecorder()
	remoteAPI{c: c}.ServeHTTP(rec, req)
	if got := srv.last(t).Auth; got != "Bearer "+testToken {
		t.Fatalf("Authorization = %q", got)
	}
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response = %d %v", rec.Code, rec.Header())
	}
}

func TestMCPNeedsStdioAndToken(t *testing.T) {
	r := runWith(t, map[string]string{EnvServer: "http://127.0.0.1:1", EnvToken: testToken}, testSpec, "", "mcp")
	if r.code != ExitUsage {
		t.Errorf("without --stdio: exit %d", r.code)
	}
	r = runWith(t, map[string]string{EnvServer: "http://127.0.0.1:1"}, testSpec, "", "mcp", "--stdio")
	if r.code != ExitAuth || r.stdout != "" {
		t.Errorf("without a token: exit %d, stdout %q", r.code, r.stdout)
	}
}
