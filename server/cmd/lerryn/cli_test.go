package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/niltavi/lerryn/server/internal/cli"
	"github.com/niltavi/lerryn/server/internal/config"
)

// cliResult is what one CLI invocation printed and its exit code.
type cliResult struct {
	stdout, stderr string
	code           int
}

func (r cliResult) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &m); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%s\nstderr: %s", err, r.stdout, r.stderr)
	}
	return m
}

// runCLI runs the lerryn binary's command tree in-process, as main does.
func runCLI(t *testing.T, stdin string, args ...string) cliResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out, errw bytes.Buffer
	cmd := newRootCmd()
	cmd.SetArgs(args)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&errw)
	err := cmd.ExecuteContext(ctx)
	if err != nil && !cli.Reported(err) {
		errw.WriteString("error: " + err.Error() + "\n")
	}
	return cliResult{stdout: out.String(), stderr: errw.String(), code: cli.ExitCode(err)}
}

func (r cliResult) ok(t *testing.T) cliResult {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	return r
}

// startTestServer starts the in-process server with a fresh SQLite
// database and no runner, so runs stay queued.
func startTestServer(t *testing.T) (*httptest.Server, config.Config) {
	t.Helper()
	cfg := config.Default()
	dir := t.TempDir()
	cfg.Database.DSN = filepath.Join(dir, "lerryn.db")
	cfg.Storage.Path = filepath.Join(dir, "storage")
	cfg.Secrets.MasterKeyFile = filepath.Join(dir, "master.key")
	ts := httptest.NewUnstartedServer(nil)
	// Console links and the session Origin check use the public URL.
	cfg.Server.PublicURL = "http://" + ts.Listener.Addr().String()
	st, err := buildServer(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.db.Close() })
	ts.Config.Handler = st.srv.Handler()
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, cfg
}

func register(t *testing.T, server, username, password string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	resp, err := http.Post(server+"/api/v1/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d", resp.StatusCode)
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// TestCLIEndToEnd drives the CLI against the in-process server: login,
// discover, apply a task from a directory, start, inspect, wait for and
// cancel a run, the generic api command, the stdio MCP proxy and logout.
// No runner is started, so it needs no Docker.
func TestCLIEndToEnd(t *testing.T) {
	ts, _ := startTestServer(t)
	cfgPath := filepath.Join(t.TempDir(), "cli", "cli.json")
	t.Setenv(cli.EnvConfig, cfgPath)
	t.Setenv(cli.EnvServer, "")
	t.Setenv(cli.EnvToken, "")
	register(t, ts.URL, "alice", "correct horse")

	// Login with a password creates an access token and stores it privately.
	login := runCLI(t, "correct horse\n", "login", ts.URL, "--username", "alice", "--password-stdin").ok(t)
	if m := login.json(t); m["username"] != "alice" || m["server"] != ts.URL {
		t.Fatalf("login output = %v", m)
	}
	if strings.Contains(login.stdout+login.stderr, "wl_pat_") {
		t.Fatal("login printed the access token")
	}
	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %04o, want 0600", info.Mode().Perm())
	}
	var tok struct {
		Token   string `json:"token"`
		TokenID string `json:"token_id"`
	}
	raw, _ := os.ReadFile(cfgPath) // #nosec G304 -- a test temp file
	_ = json.Unmarshal(raw, &tok)
	if !strings.HasPrefix(tok.Token, "wl_pat_") || tok.TokenID == "" {
		t.Fatalf("config = %s", raw)
	}

	disc := runCLI(t, "", "discover").ok(t).json(t)
	p, _ := disc["principal"].(map[string]any)
	if p == nil || p["username"] != "alice" || p["auth_method"] != "access_token" {
		t.Fatalf("principal = %v", disc["principal"])
	}
	if text := runCLI(t, "", "discover", "-o", "text").ok(t).stdout; !strings.Contains(text, "alice") {
		t.Fatalf("text discover = %s", text)
	}

	// Apply a task from a directory.
	dir := filepath.Join(t.TempDir(), "hello")
	writeFile(t, filepath.Join(dir, "lerryn.yaml"), `name: hello-cli
description: CLI end-to-end test
runtime: {language: python, version: "3.12"}
entrypoint: [python, main.py]
workflow:
  - title: Print greeting
    description: Write a greeting to the run log.
params: {who: world}
`, 0o600)
	writeFile(t, filepath.Join(dir, "main.py"), "print('hello')\n", 0o600)
	writeFile(t, filepath.Join(dir, "bin", "run.sh"), "#!/bin/sh\npython main.py\n", 0o700)
	writeFile(t, filepath.Join(dir, ".env"), "SECRET=1\n", 0o600)
	writeFile(t, filepath.Join(dir, "__pycache__", "main.cpython-312.pyc"), "x", 0o600)

	applied := runCLI(t, "", "tasks", "apply", dir).ok(t).json(t)
	if applied["created"] != true || applied["version"] != float64(1) {
		t.Fatalf("apply result = %v", applied)
	}
	task := applied["task"].(map[string]any)
	if task["name"] != "hello-cli" {
		t.Fatalf("task = %v", task)
	}
	latest := task["latest_version"].(map[string]any)
	var paths []string
	for _, f := range latest["files"].([]any) {
		fm := f.(map[string]any)
		paths = append(paths, fm["path"].(string))
		if fm["path"] == "bin/run.sh" && fm["executable"] != true {
			t.Errorf("bin/run.sh lost its executable bit")
		}
	}
	slices.Sort(paths)
	if !slices.Equal(paths, []string{"bin/run.sh", "main.py"}) {
		t.Fatalf("uploaded files = %v; hidden files and caches must be skipped", paths)
	}
	taskID := task["id"].(string)

	// Changing the task needs the version it builds on; a stale one is
	// refused with the current version, and a dry run saves nothing.
	if r := runCLI(t, "", "tasks", "apply", dir); r.code != cli.ExitAPI || r.json(t)["code"] != "precondition_required" {
		t.Fatalf("apply without --expected-version: exit %d\n%s", r.code, r.stdout)
	}
	preview := runCLI(t, "", "tasks", "apply", dir, "--dry-run", "--expected-version", "1").ok(t).json(t)
	if preview["dry_run"] != true || preview["version"] != float64(2) || preview["diff"] == nil {
		t.Fatalf("dry run = %v", preview)
	}
	previewText := runCLI(t, "", "tasks", "apply", dir, "--dry-run", "--expected-version", "1", "-o", "text").ok(t).stdout
	if !strings.Contains(previewText, "Dry run: nothing saved") || !strings.Contains(previewText, "No changes from version 1.") {
		t.Fatalf("text dry run = %s", previewText)
	}
	again := runCLI(t, "", "tasks", "apply", dir, "--expected-version", "1").ok(t).json(t)
	if seq := again["task"].(map[string]any)["latest_version"].(map[string]any)["seq"]; seq != float64(2) {
		t.Fatalf("second apply seq = %v; the dry run must not have saved a version", seq)
	}
	stale := runCLI(t, "", "tasks", "apply", dir, "--expected-version", "1")
	if p := stale.json(t); stale.code != cli.ExitAPI || p["code"] != "version_conflict" || p["current_version"] != float64(2) {
		t.Fatalf("stale apply: exit %d\n%s", stale.code, stale.stdout)
	}
	if got := runCLI(t, "", "tasks", "get", taskID).ok(t).json(t); got["name"] != "hello-cli" {
		t.Fatalf("tasks get = %v", got)
	}

	// Start a run, naming the task. Without a runner it stays queued.
	run := runCLI(t, "", "runs", "start", "hello-cli", "--test", "--param", "who=cli", "--param", "n=3").ok(t).json(t)
	runID := run["id"].(string)
	if run["status"] != "queued" || run["test"] != true || run["task_id"] != taskID {
		t.Fatalf("run = %v", run)
	}

	if got := runCLI(t, "", "runs", "get", runID).ok(t).json(t); got["status"] != "queued" {
		t.Fatalf("runs get = %v", got)
	}
	if r := runCLI(t, "", "runs", "wait", runID, "--timeout", "1s"); r.code != cli.ExitWaitTimeout || r.json(t)["status"] != "queued" {
		t.Fatalf("wait on a queued run: exit %d\n%s", r.code, r.stdout)
	}
	logs := runCLI(t, "", "runs", "logs", runID).ok(t).json(t)
	if _, ok := logs["next_cursor"]; !ok {
		t.Fatalf("logs page = %v", logs)
	}
	if got := runCLI(t, "", "runs", "cancel", runID).ok(t).json(t); got["status"] != "cancelled" {
		t.Fatalf("cancel = %v", got)
	}
	if r := runCLI(t, "", "runs", "wait", runID, "--timeout", "5s"); r.code != cli.ExitRunFailed {
		t.Fatalf("wait on a cancelled run: exit %d", r.code)
	}
	text := runCLI(t, "", "runs", "get", runID, "-o", "text").ok(t).stdout
	if !strings.Contains(text, "cancelled") || !strings.Contains(text, "(test)") {
		t.Fatalf("text run = %s", text)
	}

	// API errors exit 4 and print the problem.
	missing := runCLI(t, "", "runs", "get", "0193f6c2-0000-7000-8000-000000000000")
	if missing.code != cli.ExitAPI || missing.json(t)["code"] != "not_found" {
		t.Fatalf("missing run: exit %d\n%s", missing.code, missing.stdout)
	}
	if r := runCLI(t, "", "runs", "get"); r.code != cli.ExitUsage {
		t.Fatalf("missing argument: exit %d", r.code)
	}

	// The generic api command reaches every operation, including those
	// without a friendly command.
	list := runCLI(t, "", "api").ok(t).json(t)
	if list["count"].(float64) < 10 {
		t.Fatalf("api list = %v", list)
	}
	if got := runCLI(t, "", "api", "getRun", "--run-id", runID).ok(t).json(t); got["id"] != runID {
		t.Fatalf("api getRun = %v", got)
	}
	if got := runCLI(t, "", "api", "getRun", "--arg", "run_id="+runID, "--wait", "0").ok(t).json(t); got["status"] != "cancelled" {
		t.Fatalf("api getRun --arg = %v", got)
	}
	if got := runCLI(t, "", "api", "getMe").ok(t).json(t); got["username"] != "alice" {
		t.Fatalf("api getMe = %v", got)
	}
	desc := runCLI(t, "", "api", "createToken", "--describe").ok(t).json(t)
	if desc["session_only"] != true || desc["required_scope"] != "write" {
		t.Fatalf("describe createToken = %v", desc)
	}
	if r := runCLI(t, "", "api", "createToken", "--name", "x", "--scopes", "read"); r.code != cli.ExitAPI {
		t.Fatalf("createToken with an access token: exit %d; it must be refused", r.code)
	}
	if r := runCLI(t, "", "api", "noSuchOperation"); r.code != cli.ExitUsage {
		t.Fatalf("unknown operation: exit %d", r.code)
	}

	t.Run("mcp stdio", func(t *testing.T) { testStdioProxy(t, runID, taskID) })

	t.Run("stored token stays with its server", func(t *testing.T) {
		var mu sync.Mutex
		var auth []string
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			auth = append(auth, r.Header.Get("Authorization"))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"principal":null,"links":{"console":""}}`)
		}))
		defer other.Close()
		runCLI(t, "", "discover", "--server", other.URL).ok(t)
		mu.Lock()
		defer mu.Unlock()
		if len(auth) != 1 || auth[0] != "" {
			t.Fatalf("other server saw Authorization %q", auth)
		}
	})

	t.Run("config readable by others is refused", func(t *testing.T) {
		if err := os.Chmod(cfgPath, 0o644); err != nil { // #nosec G302 -- the test makes the file too open on purpose
			t.Fatal(err)
		}
		defer func() { _ = os.Chmod(cfgPath, 0o600) }()
		r := runCLI(t, "", "discover")
		if r.code != cli.ExitFailure || !strings.Contains(r.stderr, "chmod 600") {
			t.Fatalf("exit %d, stderr %s", r.code, r.stderr)
		}
	})

	// Logout revokes the token login created.
	out := runCLI(t, "", "logout").ok(t).json(t)
	if out["token_revoked"] != true {
		t.Fatalf("logout = %v", out)
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatalf("config still exists: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token: GET /me = %d", resp.StatusCode)
	}
	if r := runCLI(t, "", "runs", "get", runID, "--server", ts.URL); r.code != cli.ExitAuth {
		t.Fatalf("after logout: exit %d", r.code)
	}

	// An existing token can be stored with --token-stdin; a bad one is
	// refused with the auth exit code and nothing is stored.
	if r := runCLI(t, "wl_pat_notvalid\n", "login", ts.URL, "--token-stdin"); r.code != cli.ExitAuth {
		t.Fatalf("bad token: exit %d\n%s", r.code, r.stderr)
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatal("a rejected token was stored")
	}
	runCLI(t, "correct horse\n", "login", ts.URL, "-u", "alice", "--password-stdin").ok(t)
	raw, _ = os.ReadFile(cfgPath) // #nosec G304 -- a test temp file
	_ = json.Unmarshal(raw, &tok)
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	r := runCLI(t, tok.Token+"\n", "login", ts.URL, "--token-stdin").ok(t)
	if r.json(t)["username"] != "alice" {
		t.Fatalf("token login = %s", r.stdout)
	}
}

// testStdioProxy runs `lerryn mcp --stdio` with pipes for stdin and stdout
// and drives it with the go-sdk client.
func testStdioProxy(t *testing.T, runID, taskID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	clientToServer, serverIn := io.Pipe()
	serverOut, serverToClient := io.Pipe()
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		cmd := newRootCmd()
		cmd.SetArgs([]string{"mcp", "--stdio"})
		cmd.SetIn(clientToServer)
		cmd.SetOut(serverToClient)
		cmd.SetErr(&stderr)
		err := cmd.ExecuteContext(ctx)
		_ = serverToClient.Close()
		done <- err
	}()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &mcpsdk.IOTransport{Reader: serverOut, Writer: serverIn}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	for _, want := range []string{"api_call", "api_describe", "discover", "run_get", "task_apply"} {
		if !slices.Contains(names, want) {
			t.Errorf("tools %v lack %s", names, want)
		}
	}

	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "run_get", Arguments: map[string]any{"run_id": runID}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.StructuredContent.(map[string]any)["status"] != "cancelled" {
		t.Fatalf("run_get = %+v", res.Content)
	}
	res, err = cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "api_call", Arguments: map[string]any{
		"operation_id": "getTask", "arguments": map[string]any{"task_id": taskID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.StructuredContent.(map[string]any)["name"] != "hello-cli" {
		t.Fatalf("api_call getTask = %+v", res.Content)
	}
	res, err = cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "run_get", Arguments: map[string]any{"run_id": "0193f6c2-0000-7000-8000-000000000000"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || res.StructuredContent.(map[string]any)["code"] != "not_found" {
		t.Fatalf("missing run = %+v", res.Content)
	}

	_ = cs.Close()
	_ = serverIn.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("mcp --stdio: %v\n%s", err, stderr.String())
		}
	case <-ctx.Done():
		t.Fatal("mcp --stdio did not exit after the client closed")
	}
}
