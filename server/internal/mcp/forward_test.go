package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNewRequest(t *testing.T) {
	reg := loadTestRegistry(t)
	start, _ := reg.Tool("run_start")
	get, _ := reg.Tool("run_get")
	inbound := http.Header{"Authorization": {"Bearer tok"}, "Cookie": {"session=secret"}, "Mcp-Session-Id": {"x"}}

	tests := []struct {
		name       string
		tool       *Tool
		args       string
		wantURL    string
		wantHeader map[string]string
		wantBody   string
		wantErr    string
	}{
		{
			name:       "path parameter is escaped",
			tool:       get,
			args:       `{"run_id":"run_a/b c"}`,
			wantURL:    "/api/v1/runs/run_a%2Fb%20c",
			wantHeader: map[string]string{"Authorization": "Bearer tok", "Content-Type": ""},
		},
		{
			name:    "all locations",
			tool:    start,
			args:    `{"task_id":"task_1","wait":30,"idempotency_key":"k-1","test":true,"inputs":{"city":"上海"},"reason":null}`,
			wantURL: "/api/v1/tasks/task_1/runs?wait=30",
			wantHeader: map[string]string{
				"Idempotency-Key": "k-1",
				"Authorization":   "Bearer tok",
				"Content-Type":    "application/json",
				"Cookie":          "",
				"Mcp-Session-Id":  "",
			},
			wantBody: `{"inputs":{"city":"上海"},"reason":null,"test":true}`,
		},
		{
			name:     "optional query and header omitted",
			tool:     start,
			args:     `{"task_id":"task_1"}`,
			wantURL:  "/api/v1/tasks/task_1/runs",
			wantBody: `{}`,
		},
		{name: "missing path argument", tool: get, args: `{}`, wantErr: `missing required argument "run_id"`},
		{name: "dot-dot path segment", tool: get, args: `{"run_id":".."}`, wantErr: "not a valid path segment"},
		{name: "empty path segment", tool: get, args: `{"run_id":""}`, wantErr: "not a valid path segment"},
		{name: "unknown argument", tool: get, args: `{"run_id":"r","runId":"r"}`, wantErr: `unknown argument "runId"`},
		{name: "object in path", tool: get, args: `{"run_id":{"a":1}}`, wantErr: "want a string"},
		{name: "header injection", tool: start, args: `{"task_id":"t","idempotency_key":"a\r\nX-Evil: 1"}`, wantErr: "line breaks"},
		{name: "not an object", tool: get, args: `[1]`, wantErr: "must be a JSON object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := tt.tool.newRequest(context.Background(), json.RawMessage(tt.args), inbound)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if req.Body == nil {
				t.Error("Body must be non-nil, as on requests net/http hands to handlers")
			}
			if got := req.URL.RequestURI(); got != tt.wantURL {
				t.Errorf("url = %q, want %q", got, tt.wantURL)
			}
			for k, want := range tt.wantHeader {
				if got := req.Header.Get(k); got != want {
					t.Errorf("header %s = %q, want %q", k, got, want)
				}
			}
			if tt.wantBody != "" {
				b, _ := io.ReadAll(req.Body)
				if string(b) != tt.wantBody {
					t.Errorf("body = %s, want %s", b, tt.wantBody)
				}
			}
		})
	}
}

func TestQueryArray(t *testing.T) {
	got, err := queryValues(json.RawMessage(`["a", 2, true]`))
	if err != nil || strings.Join(got, ",") != "a,2,true" {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestResult(t *testing.T) {
	problem := `{"type":"urn:lerryn:error:credential_missing","title":"缺少凭证","status":422,"code":"credential_missing",` +
		`"next_actions":[{"action":"credential_request","hint":"create a request"}]}`
	tests := []struct {
		name           string
		status         int
		contentType    string
		body           string
		limit          int
		wantError      bool
		wantStructured bool
		wantText       string
		wantTruncated  bool
	}{
		{name: "json object", status: 200, contentType: "application/json", body: `{"id":"run_1"}`, wantStructured: true, wantText: `{"id":"run_1"}`},
		{name: "json array is text only", status: 200, contentType: "application/json", body: `[1,2]`, wantText: `[1,2]`},
		{name: "problem", status: 422, contentType: "application/problem+json", body: problem, wantError: true, wantStructured: true, wantText: `"next_actions"`},
		{name: "problem with 2xx status is still an error", status: 200, contentType: "application/problem+json", body: problem, wantError: true, wantStructured: true},
		{name: "plain error", status: 502, contentType: "text/plain; charset=utf-8", body: "bad gateway", wantError: true, wantText: "HTTP 502: bad gateway"},
		{name: "no content", status: 204, wantText: "HTTP 204 No Content, empty body."},
		{name: "binary", status: 200, contentType: "application/octet-stream", body: "\xff\xfe\x00\x01", wantText: "Binary content"},
		{
			name: "truncated json drops structured content", status: 200, contentType: "application/json",
			body: `{"log":"` + strings.Repeat("日志", 100) + `"}`, limit: 52, wantText: "[Truncated: showing 50 of 610 bytes", wantTruncated: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newRecorder(tt.limit)
			if tt.contentType != "" {
				rec.Header().Set("Content-Type", tt.contentType)
			}
			rec.WriteHeader(tt.status)
			// Write in two chunks to exercise the running limit.
			half := len(tt.body) / 2
			_, _ = rec.Write([]byte(tt.body[:half]))
			_, _ = rec.Write([]byte(tt.body[half:]))
			res := rec.result()

			if res.IsError != tt.wantError {
				t.Errorf("IsError = %v, want %v", res.IsError, tt.wantError)
			}
			if (res.StructuredContent != nil) != tt.wantStructured {
				t.Errorf("structured = %v, want present=%v", res.StructuredContent, tt.wantStructured)
			}
			text := res.Content[0].(*mcpsdk.TextContent).Text
			if !strings.Contains(text, tt.wantText) {
				t.Errorf("text = %q, want it to contain %q", text, tt.wantText)
			}
			if !utf8.ValidString(text) {
				t.Errorf("text is not valid UTF-8: %q", text)
			}
			if got := res.Meta[metaTruncated] == true; got != tt.wantTruncated {
				t.Errorf("truncated meta = %v, want %v", got, tt.wantTruncated)
			}
			if tt.wantTruncated && res.Meta[metaTotalBytes] != len(tt.body) {
				t.Errorf("total bytes = %v, want %d", res.Meta[metaTotalBytes], len(tt.body))
			}
		})
	}
}

func TestCallRecoversPanic(t *testing.T) {
	reg := loadTestRegistry(t)
	get, _ := reg.Tool("run_get")
	api := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	_, err := get.call(context.Background(), api, json.RawMessage(`{"run_id":"run_1"}`), nil, 0)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestCallArgErrorIsToolError(t *testing.T) {
	reg := loadTestRegistry(t)
	get, _ := reg.Tool("run_get")
	called := false
	api := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	res, err := get.call(context.Background(), api, json.RawMessage(`{}`), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if called || !res.IsError {
		t.Fatalf("called=%v isError=%v", called, res.IsError)
	}
	p := res.StructuredContent.(map[string]any)
	if p["code"] != "validation_failed" || p["title"] != "参数校验失败" {
		t.Fatalf("problem = %v", p)
	}
	// Same rules as API problems: a request ID, and a next action that is
	// a tool name the agent can call.
	if id, _ := p["request_id"].(string); !strings.HasPrefix(id, "req_") {
		t.Errorf("request_id = %v", p["request_id"])
	}
	if next, _ := p["next_actions"].([]map[string]string); len(next) != 1 || next[0]["action"] != "run_get" {
		t.Errorf("next_actions = %v", p["next_actions"])
	}
}
