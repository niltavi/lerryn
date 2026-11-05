package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Real agent clients against the stateless endpoint. These tests start the
// endpoint on a random loopback port, point the client at it through a
// throwaway MCP config (the user's own config is never touched) and ask the
// model to call run_get twice: once successfully and once for a run that
// returns a problem. They spend model tokens and need the CLIs to be logged
// in, so they only run when asked:
//
//	LERRYN_MCP_REAL_CLIENTS=claude,codex go test -run TestRealClient -v ./server/internal/mcp/
//
// LERRYN_MCP_JSON_RESPONSE=1 switches the endpoint to application/json
// responses; LERRYN_MCP_CLAUDE_MODEL, LERRYN_MCP_CODEX_MODEL and
// LERRYN_MCP_CODEX_USER_CONFIG=1 adjust the clients.
const realClientsEnv = "LERRYN_MCP_REAL_CLIENTS"

const realClientPrompt = `Use the lerryn MCP server. Call the run_get tool twice: first with run_id "run_01REAL", ` +
	`then with run_id "run_missing". Do not run shell commands. Then reply with exactly two lines and nothing else:
line 1: the task_id field from the first result
line 2: the error code from the second result, a space, and the action of its first next_actions entry`

// wireLog records what a client sends to the endpoint, for the compatibility
// matrix.
type wireLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *wireLog) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var msg struct {
			Method string `json:"method"`
			Params struct {
				ProtocolVersion string         `json:"protocolVersion"`
				ClientInfo      map[string]any `json:"clientInfo"`
				Meta            map[string]any `json:"_meta"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &msg)
		rec := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		entry := fmt.Sprintf("%s %s header=%q", r.Method, msg.Method, r.Header.Get("Mcp-Protocol-Version"))
		if msg.Params.ProtocolVersion != "" {
			entry += fmt.Sprintf(" init=%s client=%v", msg.Params.ProtocolVersion, msg.Params.ClientInfo)
		}
		if v, ok := msg.Params.Meta["io.modelcontextprotocol/protocolVersion"]; ok {
			entry += fmt.Sprintf(" meta=%v client=%v", v, msg.Params.Meta["io.modelcontextprotocol/clientInfo"])
		}
		entry += fmt.Sprintf(" -> %d", rec.status)
		l.mu.Lock()
		l.entries = append(l.entries, entry)
		l.mu.Unlock()
	})
}

func (l *wireLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return "  " + strings.Join(l.entries, "\n  ")
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type realClient struct {
	name string
	run  func(ctx context.Context, endpoint, dir string) ([]byte, error)
}

var realClients = []realClient{
	{name: "claude", run: runClaude},
	{name: "codex", run: runCodex},
}

func TestRealClient(t *testing.T) {
	want := os.Getenv(realClientsEnv)
	if want == "" {
		t.Skipf("set %s=claude,codex to run against real agent clients", realClientsEnv)
	}
	reg := loadTestRegistry(t)
	for _, c := range realClients {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(want, c.name) {
				t.Skipf("%s not listed in %s", c.name, realClientsEnv)
			}
			if _, err := exec.LookPath(c.name); err != nil {
				t.Skipf("%s not installed", c.name)
			}
			api := newStubAPI()
			wire := &wireLog{}
			mux := http.NewServeMux()
			opts := Options{JSONResponse: os.Getenv("LERRYN_MCP_JSON_RESPONSE") != ""}
			mux.Handle("/mcp", wire.wrap(NewHandler(reg, api, opts)))
			srv := httptest.NewServer(mux)
			defer srv.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			out, err := c.run(ctx, srv.URL+"/mcp", t.TempDir())
			t.Logf("%s wire log:\n%s", c.name, wire)
			t.Logf("%s output:\n%s", c.name, out)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}

			var sawOK, sawMissing bool
			api.mu.Lock()
			for _, r := range api.reqs {
				if r.Header.Get("Authorization") != testToken {
					t.Errorf("API request %s without the configured token", r.URI)
				}
				sawOK = sawOK || r.URI == "/api/v1/runs/run_01REAL"
				sawMissing = sawMissing || r.URI == "/api/v1/runs/run_missing"
			}
			api.mu.Unlock()
			if !sawOK || !sawMissing {
				t.Fatalf("API calls: ok=%v missing=%v", sawOK, sawMissing)
			}
			if !bytes.Contains(out, []byte(realClientTaskID)) {
				t.Errorf("answer does not contain %s from the success result", realClientTaskID)
			}
			if !bytes.Contains(out, []byte("not_found")) || !bytes.Contains(out, []byte("run_list")) {
				t.Errorf("answer does not contain the problem code and next action")
			}
		})
	}
}

// runClaude uses Claude Code's print mode with a strict, throwaway MCP
// config: no user or project MCP servers are loaded and no session is saved.
func runClaude(ctx context.Context, endpoint, dir string) ([]byte, error) {
	cfg := map[string]any{"mcpServers": map[string]any{"lerryn": map[string]any{
		"type": "http", "url": endpoint, "headers": map[string]string{"Authorization": testToken},
	}}}
	path := filepath.Join(dir, "mcp.json")
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, err
	}
	model := os.Getenv("LERRYN_MCP_CLAUDE_MODEL")
	if model == "" {
		model = "haiku"
	}
	//nolint:gosec // Test-only: fixed binary and flags, gated behind an env var.
	cmd := exec.CommandContext(ctx, "claude", "-p", realClientPrompt,
		"--mcp-config", path, "--strict-mcp-config",
		"--allowedTools", "mcp__lerryn__run_get",
		"--no-session-persistence",
		"--model", model,
	)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// runCodex uses codex exec without the user's config.toml, so only the
// server given with -c is configured. The token comes from an env var, which
// is how Codex reads bearer tokens for HTTP MCP servers.
func runCodex(ctx context.Context, endpoint, dir string) ([]byte, error) {
	args := []string{"exec",
		"--skip-git-repo-check", "--ephemeral", "--sandbox", "read-only",
		"-c", fmt.Sprintf("mcp_servers.lerryn.url=%q", endpoint),
		"-c", `mcp_servers.lerryn.bearer_token_env_var="LERRYN_TEST_TOKEN"`,
		"-c", `approval_policy="never"`,
	}
	if os.Getenv("LERRYN_MCP_CODEX_USER_CONFIG") == "" {
		args = append(args, "--ignore-user-config")
	}
	if m := os.Getenv("LERRYN_MCP_CODEX_MODEL"); m != "" {
		args = append(args, "-m", m)
	}
	args = append(args, realClientPrompt)
	//nolint:gosec // Test-only: fixed binary and flags, gated behind an env var.
	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LERRYN_TEST_TOKEN="+strings.TrimPrefix(testToken, "Bearer "))
	cmd.Stdin = strings.NewReader("")
	return cmd.CombinedOutput()
}
