package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// genericSpec has one exposed operation and several reachable only through
// api_call, including ones that api_call must refuse.
const genericSpec = `openapi: 3.1.0
info: {title: t, version: "1"}
servers: [{url: /api/v1}]
security:
  - accessToken: []
  - sessionCookie: []
paths:
  /runs/{runId}:
    get:
      operationId: getRun
      summary: 读取运行
      x-operation-kind: read
      x-mcp: {expose: true, tool: run_get, description: 读取运行。}
      parameters:
        - {name: runId, in: path, required: true, schema: {type: string}}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {$ref: "#/components/schemas/Run"}
  /tasks/{taskId}:
    get:
      operationId: getTask
      summary: 读取任务
      description: 返回任务和它的最新版本。
      tags: [tasks]
      x-operation-kind: read
      x-mcp: {expose: false}
      parameters:
        - {name: taskId, in: path, required: true, schema: {type: string}}
        - {name: include, in: query, schema: {type: string}}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  id: {type: string, readOnly: true}
                  secret: {type: string, writeOnly: true}
                  name: {type: string}
    delete:
      operationId: deleteTask
      summary: 删除任务
      x-operation-kind: mutate
      x-mcp: {expose: false}
      parameters:
        - {name: taskId, in: path, required: true, schema: {type: string}}
      responses:
        "204": {description: deleted}
  /runs/{runId}/logs/stream:
    get:
      operationId: streamRunLogs
      summary: 日志流
      x-operation-kind: read
      x-mcp: {expose: false}
      parameters:
        - {name: runId, in: path, required: true, schema: {type: string}}
      responses:
        "200":
          description: stream
          content:
            text/event-stream:
              schema: {type: string}
  /uploads:
    post:
      operationId: upload
      summary: 上传文件
      x-operation-kind: mutate
      x-mcp: {expose: false}
      requestBody:
        content:
          application/octet-stream:
            schema: {type: string, format: binary}
      responses:
        "201": {description: created}
  /me/tokens:
    post:
      operationId: createToken
      summary: 创建访问令牌
      x-operation-kind: mutate
      x-mcp: {expose: false}
      security:
        - sessionCookie: []
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name: {type: string}
      responses:
        "201": {description: created}
  /discover:
    get:
      operationId: discover
      summary: 发现
      x-operation-kind: read
      security:
        - {}
      responses:
        "200": {description: ok}
components:
  securitySchemes:
    accessToken: {type: http, scheme: bearer}
    sessionCookie: {type: apiKey, in: cookie, name: lerryn_session}
  schemas:
    Run:
      type: object
      properties:
        id: {type: string, readOnly: true}
        status: {type: string}
`

type recordingAPI struct {
	mu   sync.Mutex
	reqs []apiRequest
}

func (a *recordingAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	via, ok := ToolCallFromContext(r.Context())
	a.mu.Lock()
	a.reqs = append(a.reqs, apiRequest{Method: r.Method, URI: r.URL.RequestURI(), Header: r.Header.Clone(), Body: string(body), Via: via, ViaMCP: ok})
	a.mu.Unlock()
	if r.URL.Path == "/api/v1/tasks/missing" {
		writeProblem(w, http.StatusNotFound, "not_found", "任务不存在", []map[string]string{{"action": "api_describe", "hint": "x"}})
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": "t1", "name": "digest"})
}

func (a *recordingAPI) requests() []apiRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]apiRequest(nil), a.reqs...)
}

// connectInMemory serves reg over an in-memory transport, the same way the
// stdio proxy serves it.
func connectInMemory(t *testing.T, reg *Registry, api http.Handler) *mcpsdk.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	srv := NewServer(reg, api, Options{})
	st, ct := mcpsdk.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func loadGeneric(t *testing.T) *Registry {
	t.Helper()
	reg, err := Load(context.Background(), []byte(genericSpec))
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestOperationIndex(t *testing.T) {
	reg := loadGeneric(t)
	var ids []string
	for _, o := range reg.Operations() {
		ids = append(ids, o.OperationID)
	}
	want := "createToken deleteTask discover getRun getTask streamRunLogs upload"
	if got := strings.Join(ids, " "); got != want {
		t.Fatalf("operations = %s, want %s", got, want)
	}
	tests := []struct {
		id                                       string
		tool, scope                              string
		anonymous, sessionOnly, streaming, unsup bool
	}{
		{id: "getRun", tool: "run_get", scope: "read"},
		{id: "getTask", scope: "read"},
		{id: "deleteTask", scope: "write"},
		{id: "discover", scope: "read", anonymous: true},
		{id: "createToken", scope: "write", sessionOnly: true},
		{id: "streamRunLogs", scope: "read", streaming: true},
		{id: "upload", scope: "write", unsup: true},
	}
	for _, tt := range tests {
		o, ok := reg.Operation(tt.id)
		if !ok {
			t.Fatalf("%s missing", tt.id)
		}
		if o.ToolName != tt.tool || o.Scope() != tt.scope || o.Anonymous != tt.anonymous ||
			o.SessionOnly != tt.sessionOnly || o.Streaming != tt.streaming || (o.Unsupported != "") != tt.unsup {
			t.Errorf("%s = tool %q scope %s anonymous %v session %v streaming %v unsupported %q",
				tt.id, o.ToolName, o.Scope(), o.Anonymous, o.SessionOnly, o.Streaming, o.Unsupported)
		}
		if callable := o.NotCallableReason() == ""; callable == (tt.sessionOnly || tt.streaming || tt.unsup) {
			t.Errorf("%s callable = %v", tt.id, callable)
		}
	}
	if _, ok := reg.Operation("nope"); ok {
		t.Error("unknown operation found")
	}
	// The exposed tool and the index share one operation.
	tool, _ := reg.Tool("run_get")
	op, _ := reg.Operation("getRun")
	if tool.Operation != op {
		t.Error("run_get does not share its operation with the index")
	}
}

func TestSearch(t *testing.T) {
	reg := loadGeneric(t)
	tests := map[string]string{
		"":             "createToken deleteTask discover getRun getTask streamRunLogs upload",
		"task":         "deleteTask getTask",
		"TASK delete":  "deleteTask",
		"/logs/stream": "streamRunLogs",
		"run_get":      "getRun",
		"最新版本":         "getTask",
		"nothing here": "",
	}
	for q, want := range tests {
		var ids []string
		for _, o := range reg.Search(q) {
			ids = append(ids, o.OperationID)
		}
		if got := strings.Join(ids, " "); got != want {
			t.Errorf("Search(%q) = %q, want %q", q, got, want)
		}
	}
}

func TestLoadIndexRules(t *testing.T) {
	head := "openapi: 3.1.0\ninfo: {title: t, version: \"1\"}\npaths:\n"
	tests := []struct {
		name, paths, wantErr string
	}{
		{
			name: "kind contradicts method on an unexposed operation",
			paths: `  /a:
    delete:
      operationId: a
      x-operation-kind: read
      x-mcp: {expose: false}
      responses: {"204": {description: ok}}
`,
			wantErr: "contradicts",
		},
		{
			name: "exposed operation with an unmappable body",
			paths: `  /a:
    post:
      operationId: a
      x-operation-kind: mutate
      x-mcp: {expose: true, tool: a_up, description: d}
      requestBody:
        content:
          text/plain:
            schema: {type: string}
      responses: {"204": {description: ok}}
`,
			wantErr: "only application/json",
		},
		{
			name: "reserved tool name",
			paths: `  /a:
    get:
      operationId: a
      x-operation-kind: read
      x-mcp: {expose: true, tool: api_call, description: d}
      responses: {"200": {description: ok}}
`,
			wantErr: "reserved",
		},
		{
			name: "unexposed operation with an unmappable body still loads",
			paths: `  /a:
    post:
      operationId: a
      x-operation-kind: mutate
      x-mcp: {expose: false}
      requestBody:
        content:
          text/plain:
            schema: {type: string}
      responses: {"204": {description: ok}}
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(context.Background(), []byte(head+tt.paths))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestAPIDescribe(t *testing.T) {
	reg := loadGeneric(t)
	cs := connectInMemory(t, reg, &recordingAPI{})

	t.Run("list", func(t *testing.T) {
		res := callTool(t, cs, "api_describe", nil)
		if res.IsError {
			t.Fatal(text(res))
		}
		m := structured(t, res)
		if m["count"] != float64(7) {
			t.Fatalf("count = %v", m["count"])
		}
		first := m["operations"].([]any)[0].(map[string]any)
		if first["operation_id"] != "createToken" || first["tool"] != nil || first["kind"] != "mutate" {
			t.Fatalf("first = %v", first)
		}
	})

	t.Run("query", func(t *testing.T) {
		m := structured(t, callTool(t, cs, "api_describe", map[string]any{"query": "run"}))
		var ids []string
		for _, o := range m["operations"].([]any) {
			ids = append(ids, o.(map[string]any)["operation_id"].(string))
		}
		if got := strings.Join(ids, " "); got != "getRun streamRunLogs" {
			t.Fatalf("ids = %s", got)
		}
	})

	t.Run("detail", func(t *testing.T) {
		res := callTool(t, cs, "api_describe", map[string]any{"operation_id": "getTask"})
		m := structured(t, res)
		if m["method"] != "GET" || m["path"] != "/api/v1/tasks/{taskId}" || m["required_scope"] != "read" ||
			m["callable"] != true || m["description"] != "返回任务和它的最新版本。" {
			t.Fatalf("detail = %v", m)
		}
		props := m["input_schema"].(map[string]any)["properties"].(map[string]any)
		if _, ok := props["task_id"]; !ok {
			t.Errorf("input_schema lacks task_id: %v", props)
		}
		resp := m["response"].(map[string]any)
		schema := resp["schema"].(map[string]any)["properties"].(map[string]any)
		// A response keeps readOnly properties and never shows writeOnly ones.
		if _, ok := schema["id"]; !ok {
			t.Errorf("response schema dropped readOnly id: %v", schema)
		}
		if _, ok := schema["secret"]; ok {
			t.Errorf("response schema shows writeOnly secret: %v", schema)
		}
		if resp["status"] != float64(200) || resp["media_type"] != "application/json" {
			t.Errorf("response = %v", resp)
		}
	})

	t.Run("not callable", func(t *testing.T) {
		m := structured(t, callTool(t, cs, "api_describe", map[string]any{"operation_id": "createToken"}))
		if m["callable"] != false || m["session_only"] != true || m["not_callable_reason"] == "" {
			t.Fatalf("detail = %v", m)
		}
	})

	t.Run("unknown operation", func(t *testing.T) {
		res := callTool(t, cs, "api_describe", map[string]any{"operation_id": "nope"})
		if !res.IsError || structured(t, res)["code"] != "not_found" {
			t.Fatalf("result = %s", text(res))
		}
	})
}

func TestAPICall(t *testing.T) {
	reg := loadGeneric(t)
	api := &recordingAPI{}
	cs := connectInMemory(t, reg, api)

	t.Run("unexposed operation", func(t *testing.T) {
		res := callTool(t, cs, "api_call", map[string]any{
			"operation_id": "getTask",
			"arguments":    map[string]any{"task_id": "t1", "include": "versions"},
		})
		if res.IsError {
			t.Fatal(text(res))
		}
		if structured(t, res)["name"] != "digest" {
			t.Fatalf("result = %s", text(res))
		}
		if res.Meta[metaOperationID] != "getTask" || res.Meta[metaHTTPStatus] != float64(200) {
			t.Errorf("meta = %v", res.Meta)
		}
		reqs := api.requests()
		last := reqs[len(reqs)-1]
		if last.Method != http.MethodGet || last.URI != "/api/v1/tasks/t1?include=versions" {
			t.Fatalf("API saw %s %s", last.Method, last.URI)
		}
		if !last.ViaMCP || last.Via != (ToolCall{Tool: "api_call", OperationID: "getTask"}) {
			t.Errorf("tool call context = %+v", last.Via)
		}
	})

	t.Run("exposed operation", func(t *testing.T) {
		res := callTool(t, cs, "api_call", map[string]any{"operation_id": "getRun", "arguments": map[string]any{"run_id": "r1"}})
		if res.IsError {
			t.Fatal(text(res))
		}
		reqs := api.requests()
		if last := reqs[len(reqs)-1]; last.URI != "/api/v1/runs/r1" || last.Via.OperationID != "getRun" {
			t.Fatalf("API saw %+v", last)
		}
	})

	t.Run("empty response", func(t *testing.T) {
		res := callTool(t, cs, "api_call", map[string]any{"operation_id": "deleteTask", "arguments": map[string]any{"task_id": "t1"}})
		if res.IsError || res.Meta[metaHTTPStatus] != float64(204) {
			t.Fatalf("result = %s %v", text(res), res.Meta)
		}
	})

	t.Run("api problem", func(t *testing.T) {
		res := callTool(t, cs, "api_call", map[string]any{"operation_id": "getTask", "arguments": map[string]any{"task_id": "missing"}})
		if !res.IsError || structured(t, res)["code"] != "not_found" {
			t.Fatalf("result = %s", text(res))
		}
	})

	refused := []struct {
		name   string
		args   map[string]any
		code   string
		action string
	}{
		{"unknown argument", map[string]any{"operation_id": "getTask", "arguments": map[string]any{"task_id": "t1", "bogus": 1}}, "validation_failed", "api_describe"},
		{"missing path argument", map[string]any{"operation_id": "getTask"}, "validation_failed", "api_describe"},
		{"unknown operation", map[string]any{"operation_id": "nope"}, "not_found", "api_describe"},
		{"missing operation id", map[string]any{}, "validation_failed", "api_describe"},
		{"unknown top-level field", map[string]any{"operation_id": "getTask", "args": map[string]any{}}, "validation_failed", "api_call"},
		{"event stream", map[string]any{"operation_id": "streamRunLogs", "arguments": map[string]any{"run_id": "r1"}}, "validation_failed", "api_describe"},
		{"non-JSON body", map[string]any{"operation_id": "upload"}, "validation_failed", "api_describe"},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			before := len(api.requests())
			res := callTool(t, cs, "api_call", tt.args)
			if !res.IsError {
				t.Fatalf("expected a tool error, got %s", text(res))
			}
			p := structured(t, res)
			if p["code"] != tt.code {
				t.Errorf("code = %v, want %s", p["code"], tt.code)
			}
			next, _ := p["next_actions"].([]any)
			if len(next) != 1 || next[0].(map[string]any)["action"] != tt.action {
				t.Errorf("next_actions = %v", p["next_actions"])
			}
			if n := len(api.requests()); n != before {
				t.Errorf("a refused call reached the API")
			}
		})
	}
}

func TestGenericToolAnnotations(t *testing.T) {
	if !describeToolDef.Annotations.ReadOnlyHint {
		t.Error("api_describe must be read-only")
	}
	a := callToolDef.Annotations
	if a.ReadOnlyHint || !*a.DestructiveHint || a.IdempotentHint || !*a.OpenWorldHint {
		t.Errorf("api_call annotations = %+v; it can reach any operation and must be the most cautious", a)
	}
	// The tool definitions are valid JSON for tools/list.
	if _, err := json.Marshal(callToolDef); err != nil {
		t.Fatal(err)
	}
}
