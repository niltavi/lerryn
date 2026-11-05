//go:build integration

package acceptance

// The M2 scenarios drive the binary that TestMain (harness_test.go)
// builds through the API and MCP with several agent sessions, so they use
// an instance with accounts and clients rather than the single-token
// server of harness_test.go. LERRYN_TEST_PYTHON_IMAGE overrides the
// Python runtime image.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// instance is a running `lerryn all-in-one`.
type instance struct {
	t    *testing.T
	base string
	cmd  *exec.Cmd
	done chan error
	once sync.Once
}

// startInstance starts all-in-one on a fresh data directory and waits until
// it is ready. It is stopped when the test ends.
func startInstance(t *testing.T) *instance {
	t.Helper()
	dir, err := os.MkdirTemp(os.Getenv("LERRYN_TEST_MOUNT_ROOT"), "lerryn-acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	// The task user owns files under the runner's directory; removal is
	// best effort.
	t.Cleanup(func() { _ = os.RemoveAll(dir) }) // #nosec G703 -- a temp dir this test created
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	image := os.Getenv("LERRYN_TEST_PYTHON_IMAGE")
	cfg := fmt.Sprintf(`server:
  listen: %q
  public_url: %q
database:
  driver: sqlite
  dsn: %q
storage:
  path: %q
secrets:
  master_key_file: %q
runner:
  mode: embedded
  data_dir: %q
  slots: 2
`, addr, "http://"+addr, filepath.Join(dir, "lerryn.db"), filepath.Join(dir, "storage"), filepath.Join(dir, "master.key"), filepath.Join(dir, "runner"))
	if image != "" {
		cfg += fmt.Sprintf("  python_image: %q\n", image)
	}
	cfgPath := filepath.Join(dir, "lerryn.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil { // #nosec G703 -- a path in a temp dir this test created
		t.Fatal(err)
	}

	cmd := exec.Command(binary, "--config", cfgPath, "all-in-one") // #nosec G204 G702 -- the binary this package built, a config it wrote
	cmd.Stdout, cmd.Stderr = t.Output(), t.Output()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &instance{t: t, base: "http://" + addr, cmd: cmd, done: make(chan error, 1)}
	go func() { s.done <- cmd.Wait() }()
	t.Cleanup(s.stop)

	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case err := <-s.done:
			s.done <- err
			t.Fatalf("all-in-one exited during startup: %v", err)
		default:
		}
		if resp, err := http.Get(s.base + "/readyz"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("all-in-one did not become ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// stop shuts the server down as SIGTERM does.
func (s *instance) stop() {
	s.once.Do(func() {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-s.done:
			// SIGTERM ends the process with status 0 after a clean stop.
			if err != nil {
				s.t.Errorf("all-in-one exited with %v", err)
			}
		case <-time.After(time.Minute):
			_ = s.cmd.Process.Kill()
			s.t.Error("all-in-one did not stop after SIGTERM")
		}
	})
}

// account is a registered user with its session cookie.
type account struct {
	s       *instance
	cookie  *http.Cookie
	project string
}

func (s *instance) signUp(username string) *account {
	s.t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": "correct horse"})
	resp, err := http.Post(s.base+"/api/v1/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var me struct {
		PersonalProjectID string `json:"personal_project_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil || resp.StatusCode != http.StatusCreated {
		s.t.Fatalf("register = %d: %v", resp.StatusCode, err)
	}
	a := &account{s: s, project: me.PersonalProjectID}
	for _, c := range resp.Cookies() {
		if c.Name == "lerryn_session" {
			a.cookie = c
		}
	}
	return a
}

// agent creates an access token, one per agent session.
func (a *account) agent(name string) *client {
	a.s.t.Helper()
	body, _ := json.Marshal(map[string]any{"name": name, "scopes": []string{"read", "write", "execute"}})
	req, _ := http.NewRequest(http.MethodPost, a.s.base+"/api/v1/me/tokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", a.s.base)
	req.AddCookie(a.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var created struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil || resp.StatusCode != http.StatusCreated {
		a.s.t.Fatalf("create token = %d: %v", resp.StatusCode, err)
	}
	return &client{s: a.s, token: created.Token, project: a.project}
}

// client calls the API with one access token.
type client struct {
	s       *instance
	token   string
	project string
}

// response is an API answer.
type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) json(t *testing.T) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(r.body, &v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
	return v
}

// do sends a request with JSON body (nil for none) and header pairs.
func (c *client) do(t *testing.T, method, path string, body any, header ...string) response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.s.base+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: data}
}

// want sends a request and fails unless the status matches.
func (c *client) want(t *testing.T, status int, method, path string, body any, header ...string) map[string]any {
	t.Helper()
	r := c.do(t, method, path, body, header...)
	if r.status != status {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, r.status, status, r.body)
	}
	if len(r.body) == 0 || r.header.Get("Content-Type") == "text/plain; charset=utf-8" {
		return nil
	}
	return r.json(t)
}

// waitRun waits for a run to end and returns it.
func (c *client) waitRun(t *testing.T, id string, within time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		run := c.want(t, http.StatusOK, http.MethodGet, "/api/v1/runs/"+id+"?wait=30", nil)
		switch run["status"] {
		case "queued", "preparing", "running", "cancelling":
		default:
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s still %s after %s", id, run["status"], within)
		}
	}
}

// mcp connects an MCP session with the client's token.
func (c *client) mcp(t *testing.T) *mcpSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	cl := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "acceptance", Version: "1"}, nil)
	cs, err := cl.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint:   c.s.base + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{token: c.token}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &mcpSession{ctx: ctx, cs: cs}
}

type mcpSession struct {
	ctx context.Context
	cs  *mcpsdk.ClientSession
}

// call invokes a tool and returns its structured result and whether it
// is a tool error.
func (m *mcpSession) call(t *testing.T, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	res, err := m.cs.CallTool(m.ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		var text strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcpsdk.TextContent); ok {
				text.WriteString(tc.Text)
			}
		}
		t.Fatalf("%s: no structured content: %s", name, text.String())
	}
	return out, res.IsError
}

func (m *mcpSession) ok(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	out, isErr := m.call(t, name, args)
	if isErr {
		t.Fatalf("%s failed: %v", name, out)
	}
	return out
}

// waitRun polls run_get until the run ends.
func (m *mcpSession) waitRun(t *testing.T, id any) map[string]any {
	t.Helper()
	for range 20 {
		run := m.ok(t, "run_get", map[string]any{"run_id": id, "wait": 60})
		switch run["status"] {
		case "queued", "preparing", "running", "cancelling":
			continue
		}
		return run
	}
	t.Fatalf("run %v did not end", id)
	return nil
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
