package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// testSpec exercises the flag mapping: path, query (scalar and array),
// header and body arguments of several types, an argument whose flag name
// is reserved, and an operation the CLI has no friendly command for.
const testSpec = `openapi: 3.1.0
info: {title: t, version: "1"}
servers: [{url: http://localhost:8080}]
security:
  - accessToken: []
paths:
  /api/v1/widgets/{widgetId}:
    get:
      operationId: getWidget
      summary: Read a widget
      x-operation-kind: read
      x-mcp: {expose: false}
      parameters:
        - {name: widgetId, in: path, required: true, schema: {type: string}}
        - {name: tag, in: query, schema: {type: array, items: {type: string}}}
        - {name: limit, in: query, schema: {type: integer}}
        - {name: verbose, in: query, schema: {type: boolean}}
        - {name: output, in: query, schema: {type: string}}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {type: object}
    put:
      operationId: putWidget
      summary: Replace a widget
      x-operation-kind: mutate
      x-mcp: {expose: false}
      parameters:
        - {name: widgetId, in: path, required: true, schema: {type: string}}
        - {name: If-Match, in: header, schema: {type: string}}
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [size]
              properties:
                size: {type: [number, "null"]}
                labels: {type: object}
                enabled: {type: boolean}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {type: object}
  /api/v1/runs/{runId}:
    get:
      operationId: getRun
      summary: Read a run
      x-operation-kind: read
      x-mcp: {expose: true, tool: run_get, description: d}
      parameters:
        - {name: runId, in: path, required: true, schema: {type: string}}
        - {name: wait, in: query, schema: {type: integer}}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {type: object}
  /api/v1/runs/{runId}/logs:
    get:
      operationId: readRunLogs
      summary: Logs
      x-operation-kind: read
      x-mcp: {expose: false}
      parameters:
        - {name: runId, in: path, required: true, schema: {type: string}}
        - {name: cursor, in: query, schema: {type: string}}
        - {name: tail, in: query, schema: {type: integer}}
        - {name: limit, in: query, schema: {type: integer}}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {type: object}
components:
  securitySchemes:
    accessToken: {type: http, scheme: bearer}
`

const testToken = "wl_pat_testtoken"

type seenRequest struct {
	Method, URI, Auth, IfMatch, Body string
}

// stubServer answers every request with a canned body and records it.
type stubServer struct {
	*httptest.Server
	mu      sync.Mutex
	reqs    []seenRequest
	handler func(w http.ResponseWriter, r *http.Request)
}

func newStub(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *stubServer {
	t.Helper()
	s := &stubServer{handler: handler}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, seenRequest{Method: r.Method, URI: r.URL.RequestURI(), Auth: r.Header.Get("Authorization"), IfMatch: r.Header.Get("If-Match"), Body: string(body)})
		s.mu.Unlock()
		s.handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *stubServer) last(t *testing.T) seenRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reqs) == 0 {
		t.Fatal("no request reached the server")
	}
	return s.reqs[len(s.reqs)-1]
}

func (s *stubServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

func jsonHandler(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
}

type result struct {
	stdout, stderr string
	code           int
}

// run executes the CLI with testSpec against server, with the token in
// the environment and no config file.
func run(t *testing.T, server string, args ...string) result {
	t.Helper()
	return runWith(t, map[string]string{EnvServer: server, EnvToken: testToken}, testSpec, "", args...)
}

func runWith(t *testing.T, env map[string]string, spec, stdin string, args ...string) result {
	t.Helper()
	if _, ok := env[EnvConfig]; !ok {
		env[EnvConfig] = filepath.Join(t.TempDir(), "cli.json")
	}
	root := &cobra.Command{Use: "lerryn", SilenceErrors: true, SilenceUsage: true}
	AddCommands(root, Options{Spec: []byte(spec), Getenv: envMap(env)})
	var out, errw bytes.Buffer
	root.SetArgs(args)
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&errw)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := root.ExecuteContext(ctx)
	if err != nil && !Reported(err) {
		errw.WriteString("error: " + err.Error() + "\n")
	}
	return result{stdout: out.String(), stderr: errw.String(), code: ExitCode(err)}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAPICommandFlags(t *testing.T) {
	srv := newStub(t, jsonHandler(`{"ok":true}`))
	tests := []struct {
		name    string
		args    []string
		wantURI string
		body    string
		ifMatch string
	}{
		{
			name:    "path, repeated query, integer and boolean",
			args:    []string{"api", "getWidget", "--widget-id", "w/1", "--tag", "a", "--tag", "b", "--limit", "5", "--verbose"},
			wantURI: "/api/v1/widgets/w%2F1?limit=5&tag=a&tag=b&verbose=true",
		},
		{
			name:    "reserved flag name goes through --arg",
			args:    []string{"api", "getWidget", "--widget-id", "w1", "--arg", "output=wide"},
			wantURI: "/api/v1/widgets/w1?output=wide",
		},
		{
			name:    "body properties and a header",
			args:    []string{"api", "putWidget", "--widget-id", "w1", "--size", "2.5", "--labels", `{"a":"b"}`, "--enabled=false", "--if-match", "7"},
			wantURI: "/api/v1/widgets/w1",
			body:    `{"enabled":false,"labels":{"a":"b"},"size":2.5}`,
			ifMatch: "7",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := run(t, srv.URL, tt.args...)
			if r.code != 0 {
				t.Fatalf("exit %d: %s", r.code, r.stderr)
			}
			if strings.TrimSpace(r.stdout) != "{\n  \"ok\": true\n}" {
				t.Errorf("stdout = %q", r.stdout)
			}
			got := srv.last(t)
			if got.URI != tt.wantURI || got.Auth != "Bearer "+testToken || got.IfMatch != tt.ifMatch {
				t.Errorf("server saw %+v", got)
			}
			if tt.body != "" && got.Body != tt.body {
				t.Errorf("body = %s, want %s", got.Body, tt.body)
			}
		})
	}
}

func TestAPICommandInput(t *testing.T) {
	srv := newStub(t, jsonHandler(`{}`))
	file := filepath.Join(t.TempDir(), "in.json")
	write(t, file, `{"widget_id": "w1", "size": 1, "labels": {"x": 1}}`, 0o600)
	// Flags win over --input.
	if r := run(t, srv.URL, "api", "putWidget", "--input", file, "--size", "3"); r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if got := srv.last(t).Body; got != `{"labels":{"x":1},"size":3}` {
		t.Errorf("body = %s", got)
	}
	r := runWith(t, map[string]string{EnvServer: srv.URL, EnvToken: testToken}, testSpec, `{"widget_id":"w2"}`, "api", "getWidget", "--input", "-")
	if r.code != 0 || srv.last(t).URI != "/api/v1/widgets/w2" {
		t.Fatalf("stdin input: exit %d, %s", r.code, srv.last(t).URI)
	}
}

func TestAPICommandUsageErrors(t *testing.T) {
	srv := newStub(t, jsonHandler(`{}`))
	for _, args := range [][]string{
		{"api", "getWidget", "--widget-id", "w1", "--limit", "five"},
		{"api", "getWidget"}, // missing path argument
		{"api", "getWidget", "--widget-id", "w1", "--arg", "nope=1"},
		{"api", "putWidget", "--widget-id", "w1", "--labels", "{bad"},
		{"api", "getWidget", "--no-such-flag"},
		{"api", "noSuchOperation"},
		{"runs", "get", "r1", "--output", "yaml"},
	} {
		before := srv.count()
		r := run(t, srv.URL, args...)
		if r.code != ExitUsage {
			t.Errorf("%v: exit %d, want %d (%s)", args, r.code, ExitUsage, r.stderr)
		}
		if srv.count() != before {
			t.Errorf("%v: a usage error reached the server", args)
		}
	}
}

func TestCollectArgsRejectsNullInput(t *testing.T) {
	for _, source := range []string{"file", "stdin"} {
		for _, override := range []string{"none", "pair", "flag"} {
			t.Run(source+"/"+override, func(t *testing.T) {
				cmd := &cobra.Command{}
				cmd.SetIn(strings.NewReader("null"))
				input := "-"
				if source == "file" {
					input = filepath.Join(t.TempDir(), "input.json")
					write(t, input, "null", 0o600)
				}
				var pairs []string
				var values map[string]argValue
				var names map[string]string
				switch override {
				case "pair":
					pairs = []string{"widget_id=w1"}
				case "flag":
					cmd.Flags().String("widget-id", "", "")
					if err := cmd.Flags().Set("widget-id", "w1"); err != nil {
						t.Fatal(err)
					}
					values = map[string]argValue{"widget_id": func() (any, error) { return "w1", nil }}
					names = map[string]string{"widget_id": "widget-id"}
				}
				if _, err := collectArgs(cmd, input, pairs, values, names); ExitCode(err) != ExitUsage {
					t.Fatalf("collectArgs error = %v, want a usage error", err)
				}
			})
		}
	}
}

func TestAPIErrors(t *testing.T) {
	problem := func(status int, code string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": code, "title": "失败", "detail": "细节", "status": status, "request_id": "req_1",
				"next_actions": []map[string]string{{"action": "run_get", "hint": "再看看"}},
			})
		}
	}
	tests := []struct {
		status int
		code   string
		want   int
	}{
		{http.StatusUnauthorized, "unauthenticated", ExitAuth},
		{http.StatusNotFound, "not_found", ExitAPI},
		{http.StatusUnprocessableEntity, "validation_failed", ExitAPI},
	}
	for _, tt := range tests {
		srv := newStub(t, problem(tt.status, tt.code))
		r := run(t, srv.URL, "runs", "get", "r1", "-o", "json")
		if r.code != tt.want {
			t.Errorf("%d: exit %d, want %d", tt.status, r.code, tt.want)
		}
		var p map[string]any
		if err := json.Unmarshal([]byte(r.stdout), &p); err != nil || p["code"] != tt.code {
			t.Errorf("%d: stdout = %s", tt.status, r.stdout)
		}
		text := run(t, srv.URL, "runs", "get", "r1", "-o", "text")
		if text.stdout != "" || !strings.Contains(text.stderr, "req_1") || !strings.Contains(text.stderr, "next: run_get") {
			t.Errorf("%d text: stdout %q stderr %q", tt.status, text.stdout, text.stderr)
		}
	}

	// A server that cannot be reached is a local failure.
	srv := newStub(t, jsonHandler(`{}`))
	url := srv.URL
	srv.Close()
	if r := run(t, url, "runs", "get", "r1"); r.code != ExitFailure || !strings.Contains(r.stderr, "cannot reach") {
		t.Errorf("unreachable: exit %d, %s", r.code, r.stderr)
	}
}

func TestNoCredential(t *testing.T) {
	srv := newStub(t, jsonHandler(`{}`))
	r := runWith(t, map[string]string{EnvServer: srv.URL}, testSpec, "", "runs", "get", "r1")
	if r.code != ExitAuth || srv.count() != 0 {
		t.Fatalf("exit %d, %d requests", r.code, srv.count())
	}
	r = runWith(t, map[string]string{}, testSpec, "", "runs", "get", "r1")
	if r.code != ExitAuth || !strings.Contains(r.stderr, "lerryn login") {
		t.Fatalf("no server: exit %d, %s", r.code, r.stderr)
	}
}

func TestServerPathPrefix(t *testing.T) {
	srv := newStub(t, jsonHandler(`{"id":"r1","status":"running"}`))
	if r := run(t, srv.URL+"/lerryn/", "runs", "get", "r 1"); r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if got := srv.last(t).URI; got != "/lerryn/api/v1/runs/r%201" {
		t.Fatalf("URI = %s", got)
	}
}

func TestWaitRun(t *testing.T) {
	saved := logPollInterval
	logPollInterval = time.Millisecond
	t.Cleanup(func() { logPollInterval = saved })
	tests := []struct {
		name   string
		bodies []string
		want   int
	}{
		{"completed", []string{`{"status":"running"}`, `{"status":"completed","result":null}`}, ExitOK},
		{"partial result is success", []string{`{"status":"completed","result":{"status":"partial","summary":"s"}}`}, ExitOK},
		{"failed result", []string{`{"status":"completed","result":{"status":"failed","summary":"s"}}`}, ExitRunFailed},
		{"timed out run", []string{`{"status":"timed_out"}`}, ExitRunFailed},
		{"cancelled", []string{`{"status":"cancelling"}`, `{"status":"cancelled"}`}, ExitRunFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			n := 0
			srv := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				body := tt.bodies[min(n, len(tt.bodies)-1)]
				n++
				mu.Unlock()
				jsonHandler(body)(w, nil)
			})
			r := run(t, srv.URL, "runs", "wait", "r1", "--timeout", "10m")
			if r.code != tt.want {
				t.Fatalf("exit %d, want %d: %s", r.code, tt.want, r.stderr)
			}
			// Each poll asks the server to wait, at most 60 seconds.
			uri := srv.last(t).URI
			parsed, err := url.ParseRequestURI(uri)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := strconv.Atoi(parsed.Query().Get("wait"))
			if err != nil || parsed.Path != "/api/v1/runs/r1" || wait < 1 || wait > 60 {
				t.Errorf("URI = %s; want a server wait within the request's deadline", uri)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestWaitRunFractionalTimeout(t *testing.T) {
	reg, err := (&app{opts: Options{Spec: []byte(testSpec)}}).registry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		status string
		code   int
	}{
		{"completed", ExitOK},
		{"running", ExitWaitTimeout},
	} {
		t.Run(tt.status, func(t *testing.T) {
			calls := 0
			client, err := newClient("https://lerryn.example.com", testToken, &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.URL.Query().Has("wait") {
						t.Error("a fractional timeout must not be rounded up to a server-side wait")
					}
					status := "running"
					if calls > 1 {
						status = tt.status
					}
					body := `{"status":"` + status + `"}`
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				}),
			}, reg)
			if err != nil {
				t.Fatal(err)
			}
			s := &session{Client: client, json: true, out: io.Discard}
			const timeout = 50 * time.Millisecond
			started := time.Now()
			if err := s.waitRun(t.Context(), "r1", timeout); ExitCode(err) != tt.code {
				t.Fatalf("waitRun error = %v, want exit code %d", err, tt.code)
			}
			if elapsed := time.Since(started); tt.code == ExitWaitTimeout && elapsed < timeout {
				t.Fatalf("waitRun returned after %s, before %s elapsed", elapsed, timeout)
			}
			if calls != 2 {
				t.Fatalf("getRun calls = %d, want an initial read and a poll before the deadline", calls)
			}
		})
	}
}

func TestLogsFollow(t *testing.T) {
	saved := logPollInterval
	logPollInterval = time.Millisecond
	t.Cleanup(func() { logPollInterval = saved })
	pages := []string{
		`{"lines":[{"stream":"stdout","text":"one"}],"next_cursor":"c1","complete":false}`,
		`{"lines":[],"next_cursor":"c1","complete":false}`,
		`{"lines":[{"stream":"system","text":"done"}],"next_cursor":"c2","complete":true}`,
	}
	var mu sync.Mutex
	var uris []string
	srv := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		i := len(uris)
		uris = append(uris, r.URL.RequestURI())
		mu.Unlock()
		jsonHandler(pages[min(i, len(pages)-1)])(w, r)
	})
	r := run(t, srv.URL, "runs", "logs", "r1", "--follow", "--tail", "10", "-o", "text")
	if r.code != 0 || r.stdout != "one\n[lerryn] done\n" {
		t.Fatalf("exit %d, stdout %q", r.code, r.stdout)
	}
	want := []string{"/api/v1/runs/r1/logs?tail=10", "/api/v1/runs/r1/logs?cursor=c1", "/api/v1/runs/r1/logs?cursor=c1"}
	if strings.Join(uris, " ") != strings.Join(want, " ") {
		t.Errorf("requests = %v", uris)
	}
	uris = nil
	r = run(t, srv.URL, "runs", "logs", "r1", "--follow", "-o", "json")
	if lines := strings.Split(strings.TrimSpace(r.stdout), "\n"); len(lines) != 2 || !strings.Contains(lines[1], `"done"`) {
		t.Fatalf("json follow = %q", r.stdout)
	}
}

func TestScalarAndHelpers(t *testing.T) {
	if v, err := scalar("12", "integer", "n"); err != nil || mustJSON(t, v) != "12" {
		t.Errorf("integer = %v, %v", v, err)
	}
	if _, err := scalar("1.5", "integer", "n"); ExitCode(err) != ExitUsage {
		t.Errorf("1.5 as integer: %v", err)
	}
	if v, _ := scalar("true", "boolean", "b"); v != true {
		t.Errorf("boolean = %v", v)
	}
	if v, _ := scalar("007", "string", "s"); v != "007" {
		t.Errorf("string = %v", v)
	}
	for in, want := range map[string]string{`3`: `3`, `abc`: `"abc"`, `{"a":1}`: `{"a":1}`, `"q"`: `"q"`, `[1`: `"[1"`} {
		if got := mustJSON(t, jsonOrString(in)); got != want {
			t.Errorf("jsonOrString(%s) = %s, want %s", in, got, want)
		}
	}
	for schema, want := range map[string]string{`{"type":"string"}`: "string", `{"type":["null","integer"]}`: "integer", `{}`: ""} {
		var m map[string]any
		_ = json.Unmarshal([]byte(schema), &m)
		if got := schemaType(m); got != want {
			t.Errorf("schemaType(%s) = %q", schema, got)
		}
	}
}
