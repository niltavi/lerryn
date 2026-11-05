package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const testToken = "Bearer wl_test_token" //nolint:gosec // Fixture checked by the stub API, not a real credential.

// realClientTaskID is a value an agent can only report back if it really
// read the tool result.
const realClientTaskID = "task_7f3k9q"

// protocolVersions are the versions exercised against the stateless
// endpoint: the current one, which is stateless by design, and the older
// ones that still start with an initialize handshake.
var protocolVersions = []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26"}

// stubAPI stands in for the REST handler chain. It checks the access token
// the way the real middleware will, and records what reached it.
type stubAPI struct {
	mux  *http.ServeMux
	mu   sync.Mutex
	reqs []apiRequest
}

type apiRequest struct {
	Method, URI string
	Header      http.Header
	Body        string
	Via         ToolCall
	ViaMCP      bool
}

func newStubAPI() *stubAPI {
	s := &stubAPI{mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /api/v1/runs/{runId}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("runId")
		switch id {
		case "run_missing":
			writeProblem(w, http.StatusNotFound, "not_found", "运行不存在或无权访问", []map[string]string{
				{"action": "run_list", "hint": "List recent runs to find the right ID."},
			})
		case "run_huge":
			writeJSON(w, http.StatusOK, map[string]any{"object": "run", "id": id, "log_tail": strings.Repeat("输出", 40000)})
		default:
			writeJSON(w, http.StatusOK, runJSON(id, realClientTaskID, "running", false))
		}
	})
	s.mux.HandleFunc("POST /api/v1/tasks/{taskId}/runs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Test bool `json:"test"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeProblem(w, http.StatusUnprocessableEntity, "validation_failed", err.Error(), nil)
			return
		}
		if r.PathValue("taskId") == "task_nocred" {
			writeProblem(w, http.StatusUnprocessableEntity, "credential_missing", "任务引用的凭证 LLM_API_KEY 尚未填写", []map[string]string{
				{"action": "credential_request", "hint": "为该任务创建凭证填写请求，并把链接交给用户"},
			})
			return
		}
		writeJSON(w, http.StatusAccepted, runJSON("run_new", r.PathValue("taskId"), "queued", body.Test))
	})
	return s
}

func (s *stubAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	via, ok := ToolCallFromContext(r.Context())
	s.mu.Lock()
	s.reqs = append(s.reqs, apiRequest{Method: r.Method, URI: r.URL.RequestURI(), Header: r.Header.Clone(), Body: string(body), Via: via, ViaMCP: ok})
	s.mu.Unlock()
	if r.Header.Get("Authorization") != testToken {
		writeProblem(w, http.StatusUnauthorized, "unauthenticated", "缺少或无效的访问令牌", []map[string]string{
			{"action": "configure_token", "hint": "Create an access token in the console and put it in the Authorization header."},
		})
		return
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	s.mux.ServeHTTP(w, r)
}

func (s *stubAPI) last(t *testing.T) apiRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reqs) == 0 {
		t.Fatal("no request reached the API")
	}
	return s.reqs[len(s.reqs)-1]
}

func (s *stubAPI) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

func runJSON(id, task, status string, test bool) map[string]any {
	return map[string]any{
		"object": "run", "id": id, "task_id": task, "status": status, "test": test, "exit_code": nil,
		"links": map[string]string{"console": "https://lerryn.example.com/runs/" + id},
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeProblem(w http.ResponseWriter, status int, code, detail string, next []map[string]string) {
	p := map[string]any{
		"type": "urn:lerryn:error:" + code, "title": code, "status": status, "code": code,
		"detail": detail, "request_id": "req_test",
	}
	if next != nil {
		p["next_actions"] = next
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

// exchange is one HTTP round trip between the MCP client and the endpoint.
type exchange struct {
	Method, ProtocolHeader, RPCMethod string
	Status                            int
	ContentType, SessionID            string
}

// clientTransport sets the access token, spreads requests over several
// endpoints (simulating replicas behind a load balancer without affinity)
// and records the traffic.
type clientTransport struct {
	token     string
	endpoints []*url.URL
	mu        sync.Mutex
	n         int
	log       []exchange
}

func (c *clientTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	if c.token != "" {
		req.Header.Set("Authorization", c.token)
	}
	c.mu.Lock()
	target := c.endpoints[c.n%len(c.endpoints)]
	c.n++
	c.mu.Unlock()
	req.URL.Host = target.Host
	req.Host = target.Host

	rpcMethod := ""
	if req.Body != nil && req.GetBody != nil {
		if b, err := req.GetBody(); err == nil {
			var msg struct {
				Method string `json:"method"`
			}
			data, _ := io.ReadAll(b)
			_ = json.Unmarshal(data, &msg)
			rpcMethod = msg.Method
		}
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.log = append(c.log, exchange{
		Method: req.Method, ProtocolHeader: req.Header.Get("Mcp-Protocol-Version"), RPCMethod: rpcMethod,
		Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), SessionID: resp.Header.Get("Mcp-Session-Id"),
	})
	c.mu.Unlock()
	return resp, nil
}

func (c *clientTransport) exchanges() []exchange {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.log)
}

func connect(t *testing.T, version, token string, endpoints ...string) (*mcpsdk.ClientSession, *clientTransport) {
	t.Helper()
	ct := &clientTransport{token: token}
	for _, e := range endpoints {
		u, err := url.Parse(e)
		if err != nil {
			t.Fatal(err)
		}
		ct.endpoints = append(ct.endpoints, u)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "lerryn-test", Version: "0"}, nil)
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:   endpoints[0] + "/mcp",
		HTTPClient: &http.Client{Transport: ct},
		MaxRetries: -1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cs, err := client.Connect(ctx, transport, &mcpsdk.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatalf("connect with %s: %v", version, err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, ct
}

func startEndpoint(t *testing.T, reg *Registry, api http.Handler, opts Options) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/mcp", NewHandler(reg, api, opts))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func callTool(t *testing.T, cs *mcpsdk.ClientSession, name string, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

func structured(t *testing.T, res *mcpsdk.CallToolResult) map[string]any {
	t.Helper()
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content = %T %v", res.StructuredContent, res.StructuredContent)
	}
	return m
}

func text(res *mcpsdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// TestEndpoint drives the stateless endpoint with the go-sdk client for each
// protocol version and response mode. Every request goes to one of two
// independent handler instances in turn, standing in for two server replicas
// with no session affinity.
func TestEndpoint(t *testing.T) {
	reg := loadTestRegistry(t)
	for _, mode := range []struct {
		name string
		json bool
	}{{"sse", false}, {"json", true}} {
		for _, version := range protocolVersions {
			t.Run(mode.name+"/"+version, func(t *testing.T) {
				api := newStubAPI()
				opts := Options{JSONResponse: mode.json}
				a := startEndpoint(t, reg, api, opts)
				b := startEndpoint(t, reg, api, opts)
				cs, traffic := connect(t, version, testToken, a, b)
				if got := cs.InitializeResult().ProtocolVersion; got != version {
					t.Fatalf("negotiated %s, want %s", got, version)
				}

				t.Run("list tools", func(t *testing.T) {
					res, err := cs.ListTools(context.Background(), nil)
					if err != nil {
						t.Fatal(err)
					}
					var names []string
					for _, tl := range res.Tools {
						names = append(names, tl.Name)
						switch tl.Name {
						case "run_get":
							if !tl.Annotations.ReadOnlyHint || !tl.Annotations.IdempotentHint {
								t.Errorf("run_get annotations = %+v", tl.Annotations)
							}
						case "run_start":
							if tl.Annotations.ReadOnlyHint || !*tl.Annotations.DestructiveHint || !*tl.Annotations.OpenWorldHint {
								t.Errorf("run_start annotations = %+v", tl.Annotations)
							}
							schema, _ := tl.InputSchema.(map[string]any)
							if !equalJSON(schema["required"], []string{"task_id"}) {
								t.Errorf("run_start required = %v", schema["required"])
							}
						}
					}
					if !slices.Equal(names, []string{"api_call", "api_describe", "run_get", "run_start"}) {
						t.Fatalf("tools = %v", names)
					}
				})

				t.Run("call success", func(t *testing.T) {
					res := callTool(t, cs, "run_get", map[string]any{"run_id": "run_ok1"})
					if res.IsError {
						t.Fatalf("unexpected error: %s", text(res))
					}
					if got := structured(t, res)["id"]; got != "run_ok1" {
						t.Fatalf("id = %v", got)
					}
					if !strings.Contains(text(res), `"console"`) {
						t.Fatalf("text content lacks the JSON body: %s", text(res))
					}
					req := api.last(t)
					if req.Method != http.MethodGet || req.URI != "/api/v1/runs/run_ok1" {
						t.Fatalf("API saw %s %s", req.Method, req.URI)
					}
					if !req.ViaMCP || req.Via.Tool != "run_get" || req.Via.OperationID != "getRun" {
						t.Fatalf("tool call context = %+v (%v)", req.Via, req.ViaMCP)
					}
				})

				t.Run("call failure keeps code and next_actions", func(t *testing.T) {
					res := callTool(t, cs, "run_get", map[string]any{"run_id": "run_missing"})
					if !res.IsError {
						t.Fatal("want tool error")
					}
					p := structured(t, res)
					if p["code"] != "not_found" || p["next_actions"] == nil {
						t.Fatalf("problem = %v", p)
					}
					// Clients that only show text to the model must still see both.
					if s := text(res); !strings.Contains(s, `"code":"not_found"`) || !strings.Contains(s, `"next_actions"`) {
						t.Fatalf("text = %s", s)
					}
				})

				t.Run("execute forwards every argument", func(t *testing.T) {
					res := callTool(t, cs, "run_start", map[string]any{
						"task_id": "task_42", "wait": 5, "idempotency_key": "idem-1", "test": true, "inputs": map[string]any{"city": "上海"},
					})
					if res.IsError {
						t.Fatalf("unexpected error: %s", text(res))
					}
					if got := structured(t, res)["status"]; got != "queued" {
						t.Fatalf("status = %v", got)
					}
					req := api.last(t)
					if req.Method != http.MethodPost || req.URI != "/api/v1/tasks/task_42/runs?wait=5" {
						t.Fatalf("API saw %s %s", req.Method, req.URI)
					}
					if got := req.Header.Get("Idempotency-Key"); got != "idem-1" {
						t.Fatalf("Idempotency-Key = %q", got)
					}
					if req.Body != `{"inputs":{"city":"上海"},"test":true}` {
						t.Fatalf("body = %s", req.Body)
					}
				})

				t.Run("problem from execute", func(t *testing.T) {
					res := callTool(t, cs, "run_start", map[string]any{"task_id": "task_nocred"})
					p := structured(t, res)
					if !res.IsError || p["code"] != "credential_missing" {
						t.Fatalf("isError=%v problem=%v", res.IsError, p)
					}
					next, _ := p["next_actions"].([]any)
					if len(next) != 1 || next[0].(map[string]any)["action"] != "credential_request" {
						t.Fatalf("next_actions = %v", p["next_actions"])
					}
				})

				t.Run("authorization is passed through", func(t *testing.T) {
					callTool(t, cs, "run_get", map[string]any{"run_id": "run_ok1"})
					if got := api.last(t).Header.Get("Authorization"); got != testToken {
						t.Fatalf("Authorization = %q", got)
					}

					anon, _ := connect(t, version, "", a)
					res := callTool(t, anon, "run_get", map[string]any{"run_id": "run_ok1"})
					if !res.IsError || structured(t, res)["code"] != "unauthenticated" {
						t.Fatalf("anonymous call: isError=%v %s", res.IsError, text(res))
					}
					if got := api.last(t).Header.Get("Authorization"); got != "" {
						t.Fatalf("Authorization = %q, want none", got)
					}
				})

				t.Run("long result is truncated", func(t *testing.T) {
					res := callTool(t, cs, "run_get", map[string]any{"run_id": "run_huge"})
					if res.IsError || res.StructuredContent != nil {
						t.Fatalf("isError=%v structured=%v", res.IsError, res.StructuredContent != nil)
					}
					if res.Meta[metaTruncated] != true {
						t.Fatalf("meta = %v", res.Meta)
					}
					if n := len(text(res)); n > DefaultMaxResultBytes+512 {
						t.Fatalf("text is %d bytes", n)
					}
				})

				t.Run("argument errors do not reach the API", func(t *testing.T) {
					before := api.count()
					res := callTool(t, cs, "run_get", map[string]any{"runId": "run_ok1"})
					if !res.IsError || structured(t, res)["code"] != "validation_failed" {
						t.Fatalf("isError=%v %s", res.IsError, text(res))
					}
					if api.count() != before {
						t.Fatal("API was called")
					}
				})

				_ = cs.Close()
				ex := traffic.exchanges()
				hosts := map[string]bool{}
				for _, e := range ex {
					if e.SessionID != "" {
						t.Errorf("stateless endpoint returned Mcp-Session-Id %q", e.SessionID)
					}
				}
				for _, e := range traffic.endpoints {
					hosts[e.Host] = true
				}
				if len(hosts) != 2 {
					t.Fatalf("traffic did not alternate between replicas")
				}
				t.Logf("traffic for %s/%s:\n%s", mode.name, version, formatTraffic(ex))
			})
		}
	}
}

func formatTraffic(ex []exchange) string {
	var b strings.Builder
	for _, e := range ex {
		fmt.Fprintf(&b, "  %-6s %-24s proto=%-10s -> %d %s\n", e.Method, e.RPCMethod, e.ProtocolHeader, e.Status, e.ContentType)
	}
	return b.String()
}

// TestLegacyClientWithoutInitialize sends a tools/call with an old protocol
// version and no prior initialize, the way a request lands on a replica that
// never saw the handshake.
func TestLegacyClientWithoutInitialize(t *testing.T) {
	reg := loadTestRegistry(t)
	api := newStubAPI()
	base := startEndpoint(t, reg, api, Options{JSONResponse: true})
	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"run_get","arguments":{"run_id":"run_ok1"}}}`
	req, _ := http.NewRequest(http.MethodPost, base+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	req.Header.Set("Authorization", testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), `"run_ok1"`) {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	if resp.Header.Get("Mcp-Session-Id") != "" {
		t.Fatal("unexpected session id")
	}
}

func TestEndpointMethods(t *testing.T) {
	reg := loadTestRegistry(t)
	base := startEndpoint(t, reg, newStubAPI(), Options{})
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		req, _ := http.NewRequest(method, base+"/mcp", nil)
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Mcp-Session-Id", "anything")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "POST" {
			t.Errorf("%s: status %d, Allow %q", method, resp.StatusCode, resp.Header.Get("Allow"))
		}
	}
}

// TestLocalhostProtection documents the SDK's DNS rebinding guard: a request
// that reaches a loopback listener with a public Host header, as from a
// reverse proxy on the same machine, is refused unless the guard is off.
func TestLocalhostProtection(t *testing.T) {
	reg := loadTestRegistry(t)
	for _, tt := range []struct {
		disable bool
		want    int
	}{{false, http.StatusForbidden}, {true, http.StatusOK}} {
		base := startEndpoint(t, reg, newStubAPI(), Options{JSONResponse: true, DisableLocalhostProtection: tt.disable})
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
		req, _ := http.NewRequest(http.MethodPost, base+"/mcp", strings.NewReader(body))
		req.Host = "lerryn.example.com"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tt.want {
			t.Errorf("DisableLocalhostProtection=%v: status %d, want %d", tt.disable, resp.StatusCode, tt.want)
		}
	}
}

type outerKey struct{}

// TestOuterContextReachesAPI pins a property the design relies on: values a
// middleware puts on the /mcp request context (client address, request ID,
// an authenticated principal) are visible to the in-process API request.
// In stateless mode the SDK builds each request's session from the HTTP
// request context; an SDK upgrade that changes this must fail here.
func TestOuterContextReachesAPI(t *testing.T) {
	reg := loadTestRegistry(t)
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			seen := make(chan any, 1)
			api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Context().Value(outerKey{})
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			})
			h := NewHandler(reg, api, Options{})
			mw := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), outerKey{}, "req_outer")))
			})
			srv := httptest.NewServer(mw)
			defer srv.Close()
			cs, _ := connect(t, version, testToken, srv.URL)
			callTool(t, cs, "run_get", map[string]any{"run_id": "run_1"})
			if got := <-seen; got != "req_outer" {
				t.Fatalf("API saw %v", got)
			}
		})
	}
}
