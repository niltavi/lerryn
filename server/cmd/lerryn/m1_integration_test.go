//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/executor"
)

// The M1 acceptance scenarios from the implementation plan, run against
// the all-in-one role as `lerryn all-in-one` starts it: runServer with the
// embedded runner, a SQLite database, the local object store and the
// Docker daemon from the environment.
//
//	go test -tags integration -run TestM1 -v ./server/cmd/lerryn/
//
// LERRYN_TEST_PYTHON_IMAGE overrides the runtime image, and
// LERRYN_TEST_MOUNT_ROOT places the runner's data directory where the
// daemon can mount it, as for the runner's own integration tests.

// acceptanceTask prints, sleeps with a child process or fails, depending
// on the run's params.
const acceptanceTask = `import subprocess, sys, time
import sbx

mode = sbx.params().get("mode")
if mode == "lines":
    with sbx.stage("emit"):
        out = sys.stdout
        for i in range(100000):
            out.write(f"line {i}\n")
    sbx.result("ok", "printed 100000 lines", {"count": 100000})
elif mode == "sleep":
    with sbx.stage("wait"):
        child = subprocess.Popen(["sleep", "600"])
        print(f"child started {child.pid}", flush=True)
        time.sleep(600)
else:
    print("unknown mode", mode, file=sys.stderr)
    sys.exit(3)
`

func acceptanceRequest(timeout string) map[string]any {
	return map[string]any{
		"manifest": map[string]any{
			"description": "M1 验收",
			"runtime":     map[string]any{"language": "python", "version": "3.12"},
			"entrypoint":  []string{"python", "main.py"},
			"stages":      []map[string]any{{"id": "emit", "name": "输出"}, {"id": "wait", "name": "等待"}},
			"limits":      map[string]any{"timeout": timeout, "memory": "256Mi"},
		},
		"files": []map[string]any{{"path": "main.py", "content": acceptanceTask}},
	}
}

// allInOne is a running `lerryn all-in-one` on fixed directories, so that
// a second start sees the same database, objects and runner ID.
type allInOne struct {
	// t is the test that makes the calls; owner started the process.
	t, owner *testing.T
	cfg      config.Config
	cancel   context.CancelFunc
	stopOnce sync.Once
	done     chan error
	base     string
	token    string
}

func acceptanceConfig(t *testing.T) config.Config {
	t.Helper()
	root := os.Getenv("LERRYN_TEST_MOUNT_ROOT")
	dir, err := os.MkdirTemp(root, "lerryn-m1-")
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

	cfg := config.Default()
	cfg.Server.Listen = addr
	cfg.Server.PublicURL = "http://" + addr
	cfg.Database.DSN = filepath.Join(dir, "lerryn.db")
	cfg.Storage.Path = filepath.Join(dir, "storage")
	cfg.Secrets.MasterKeyFile = filepath.Join(dir, "master.key")
	cfg.Runner.DataDir = filepath.Join(dir, "runner")
	cfg.Runner.Slots = 2
	if img := os.Getenv("LERRYN_TEST_PYTHON_IMAGE"); img != "" {
		cfg.Runner.PythonImage = img
	}
	return cfg
}

func startAllInOne(t *testing.T, cfg config.Config) *allInOne {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a := &allInOne{t: t, owner: t, cfg: cfg, cancel: cancel, done: make(chan error, 1), base: cfg.Server.PublicURL}
	log := slog.New(slog.NewTextHandler(t.Output(), &slog.HandlerOptions{Level: slog.LevelWarn}))
	go func() { a.done <- runServer(ctx, cfg, log, true) }()
	t.Cleanup(a.stop)

	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-a.done:
			a.done <- err
			t.Fatalf("all-in-one exited during startup: %v", err)
		default:
		}
		resp, err := http.Get(a.base + "/readyz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return a
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("all-in-one did not become ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stop shuts the process down the way SIGTERM does. It is safe to call
// more than once.
func (a *allInOne) stop() {
	a.stopOnce.Do(func() {
		a.cancel()
		select {
		case err := <-a.done:
			if err != nil {
				a.owner.Errorf("all-in-one: %v", err)
			}
		case <-time.After(time.Minute):
			a.owner.Error("all-in-one did not stop")
		}
	})
}

// call sends a JSON request with the agent token and decodes the answer
// into v when it is not nil.
func (a *allInOne) call(method, path string, body any, want int, v any) {
	a.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			a.t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, a.base+path, &buf)
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		a.t.Fatalf("%s %s = %d, want %d: %s", method, path, resp.StatusCode, want, data)
	}
	if v != nil {
		if err := json.Unmarshal(data, v); err != nil {
			a.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
}

// signUp registers a user, creates an access token for it and returns the
// user's personal project.
func (a *allInOne) signUp(username string) string {
	a.t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": "correct horse"})
	resp, err := http.Post(a.base+"/api/v1/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}
	var me struct {
		PersonalProjectID string `json:"personal_project_id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&me)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusCreated {
		a.t.Fatalf("register = %d: %v", resp.StatusCode, err)
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "lerryn_session" {
			session = c
		}
	}
	body, _ = json.Marshal(map[string]any{"name": "acceptance", "scopes": []string{"read", "write", "execute"}})
	req, _ := http.NewRequest(http.MethodPost, a.base+"/api/v1/me/tokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", a.base)
	req.AddCookie(session)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	var created struct {
		Token string `json:"token"`
	}
	err = json.NewDecoder(resp.Body).Decode(&created)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusCreated {
		a.t.Fatalf("create token = %d: %v", resp.StatusCode, err)
	}
	a.token = created.Token
	return me.PersonalProjectID
}

type apiRun struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Code    string `json:"code"`
		Stage   string `json:"stage"`
		Message string `json:"message"`
	} `json:"error"`
	Result *struct {
		Status  string          `json:"status"`
		Summary string          `json:"summary"`
		Data    json.RawMessage `json:"data"`
	} `json:"result"`
	Stages []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"stages"`
	CancelRequested bool `json:"cancel_requested"`
}

type logPage struct {
	Lines []struct {
		Seq    int64  `json:"seq"`
		Stream string `json:"stream"`
		Stage  string `json:"stage"`
		Text   string `json:"text"`
	} `json:"lines"`
	NextCursor   string `json:"next_cursor"`
	Complete     bool   `json:"complete"`
	DroppedLines int64  `json:"dropped_lines"`
}

func (a *allInOne) applyTask(project, name, timeout string) string {
	a.t.Helper()
	var applied struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	a.call(http.MethodPut, "/api/v1/projects/"+project+"/tasks/"+name, acceptanceRequest(timeout), http.StatusOK, &applied)
	return applied.Task.ID
}

func (a *allInOne) startRun(taskID, mode string) apiRun {
	a.t.Helper()
	var run apiRun
	a.call(http.MethodPost, "/api/v1/tasks/"+taskID+"/runs", map[string]any{"params": map[string]any{"mode": mode}},
		http.StatusAccepted, &run)
	return run
}

// waitRun waits until the run is no longer active.
func (a *allInOne) waitRun(id string, within time.Duration) apiRun {
	a.t.Helper()
	deadline := time.Now().Add(within)
	for {
		var run apiRun
		a.call(http.MethodGet, "/api/v1/runs/"+id+"?wait=30", nil, http.StatusOK, &run)
		switch run.Status {
		case "queued", "preparing", "running", "cancelling":
		default:
			return run
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("run %s still %s after %s", id, run.Status, within)
		}
	}
}

// waitForLog waits until a line containing text is readable.
func (a *allInOne) waitForLog(id, text string, within time.Duration) {
	a.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		var page logPage
		a.call(http.MethodGet, "/api/v1/runs/"+id+"/logs?tail=50", nil, http.StatusOK, &page)
		for _, l := range page.Lines {
			if strings.Contains(l.Text, text) {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	a.t.Fatalf("run %s: no log line with %q within %s", id, text, within)
}

// runContainers lists the containers of a run, running or not.
func runContainers(t *testing.T, runID string) []string {
	t.Helper()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()
	res, err := cli.ContainerList(context.Background(), client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", executor.LabelRunID+"="+runID),
	})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range res.Items {
		out = append(out, c.ID[:12]+" "+string(c.State))
	}
	return out
}

// containersStoppedThenGone checks a run that just ended: its container
// stopped before the run was reported (A09), and the runner removes it
// shortly after.
func containersStoppedThenGone(t *testing.T, runID string) {
	t.Helper()
	for _, c := range runContainers(t, runID) {
		if strings.HasSuffix(c, " running") {
			t.Fatalf("container still running after the run ended: %s", c)
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		left := runContainers(t, runID)
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("containers not removed: %v", left)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestM1Acceptance(t *testing.T) {
	cfg := acceptanceConfig(t)
	a := startAllInOne(t, cfg)
	project := a.signUp("alice")
	task := a.applyTask(project, "acceptance", "10m")

	t.Run("A13 large log", func(t *testing.T) {
		a.t = t
		run := a.waitRun(a.startRun(task, "lines").ID, 5*time.Minute)
		if run.Status != "completed" || run.Result == nil || run.Result.Status != "ok" {
			t.Fatalf("run = %+v", run)
		}
		if len(run.Stages) == 0 || run.Stages[0].ID != "emit" || run.Stages[0].Status != "done" {
			t.Fatalf("stages = %+v", run.Stages)
		}

		// Page through everything; stdout lines must be gapless and in order.
		var (
			cursor  string
			next    int
			pages   int
			resumed string
			resumeN int
		)
		for {
			path := "/api/v1/runs/" + run.ID + "/logs?limit=1000"
			if cursor != "" {
				path += "&cursor=" + cursor
			}
			var page logPage
			a.call(http.MethodGet, path, nil, http.StatusOK, &page)
			pages++
			if pages == 7 {
				resumed, resumeN = cursor, next
			}
			for _, l := range page.Lines {
				if l.Stream != "stdout" {
					continue
				}
				// Stage markers on stdout label every line of the burst
				// exactly (runtimes/sdk/python/README.md).
				if want := fmt.Sprintf("line %d", next); l.Text != want || l.Stage != "emit" {
					t.Fatalf("page %d: got %q in stage %q, want %q in emit", pages, l.Text, l.Stage, want)
				}
				next++
			}
			if page.DroppedLines != 0 {
				t.Fatalf("dropped %d lines", page.DroppedLines)
			}
			if page.Complete {
				break
			}
			if page.NextCursor == "" || pages > 200 {
				t.Fatalf("page %d: no cursor and not complete", pages)
			}
			cursor = page.NextCursor
		}
		if next != 100000 {
			t.Fatalf("read %d stdout lines, want 100000", next)
		}

		// A reconnecting reader resumes from its last cursor without gaps
		// or repeats.
		var page logPage
		a.call(http.MethodGet, "/api/v1/runs/"+run.ID+"/logs?limit=1000&cursor="+resumed, nil, http.StatusOK, &page)
		first := ""
		for _, l := range page.Lines {
			if l.Stream == "stdout" {
				first = l.Text
				break
			}
		}
		if want := fmt.Sprintf("line %d", resumeN); first != want {
			t.Fatalf("resumed at %q, want %q", first, want)
		}

		// The tail ends with the last line the task printed.
		a.call(http.MethodGet, "/api/v1/runs/"+run.ID+"/logs?tail=20", nil, http.StatusOK, &page)
		last := ""
		for _, l := range page.Lines {
			if l.Stream == "stdout" {
				last = l.Text
			}
		}
		if len(page.Lines) != 20 || last != "line 99999" {
			t.Fatalf("tail = %d lines ending %q", len(page.Lines), last)
		}
	})

	t.Run("A09 cancel", func(t *testing.T) {
		a.t = t
		id := a.startRun(task, "sleep").ID
		a.waitForLog(id, "child started", 2*time.Minute)
		var run apiRun
		a.call(http.MethodPost, "/api/v1/runs/"+id+"/cancel", nil, http.StatusAccepted, &run)
		if run.Status == "cancelled" {
			t.Fatal("run reported cancelled before the container was confirmed stopped")
		}
		if !run.CancelRequested {
			t.Fatalf("cancel_requested not set: %+v", run)
		}
		run = a.waitRun(id, time.Minute)
		if run.Status != "cancelled" {
			t.Fatalf("run = %+v", run)
		}
		containersStoppedThenGone(t, id)
	})

	t.Run("A09 timeout", func(t *testing.T) {
		a.t = t
		short := a.applyTask(project, "acceptance-timeout", "5s")
		run := a.waitRun(a.startRun(short, "sleep").ID, 2*time.Minute)
		if run.Status != "timed_out" || run.Error == nil || run.Error.Code != "timeout" {
			t.Fatalf("run = %+v", run)
		}
		containersStoppedThenGone(t, run.ID)
	})

	t.Run("A10 restart", func(t *testing.T) {
		a.t = t
		id := a.startRun(task, "sleep").ID
		a.waitForLog(id, "child started", 2*time.Minute)

		// Shutting down leaves the run's container to the next start.
		a.stop()
		if left := runContainers(t, id); len(left) != 1 {
			t.Fatalf("containers after shutdown = %v, want the run's container", left)
		}

		b := startAllInOne(t, cfg)
		b.token = a.token
		run := b.waitRun(id, time.Minute)
		if run.Status != "interrupted" || run.Error == nil || run.Error.Code != "run_interrupted" {
			t.Fatalf("run = %+v", run)
		}
		containersStoppedThenGone(t, id)
		// The history from before the restart is still readable.
		b.waitForLog(id, "child started", time.Second)
		var page logPage
		b.call(http.MethodGet, "/api/v1/runs/"+id+"/logs", nil, http.StatusOK, &page)
		if !page.Complete {
			t.Fatalf("log of an interrupted run is not complete: %+v", page)
		}
		// The restarted process takes new runs.
		if run := b.waitRun(b.startRun(task, "lines").ID, 5*time.Minute); run.Status != "completed" {
			t.Fatalf("run after restart = %+v", run)
		}
	})
}

// TestM1MCPFlow creates and runs a Python task through the MCP tools only,
// as an agent would.
func TestM1MCPFlow(t *testing.T) {
	a := startAllInOne(t, acceptanceConfig(t))
	a.signUp("agent")

	ctx := context.Background()
	cl := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "m1-acceptance", Version: "1"}, nil)
	cs, err := cl.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint:   a.base + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{token: a.token}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	for _, want := range []string{"discover", "task_apply", "run_start", "run_get", "run_logs", "run_cancel"} {
		if !strings.Contains(" "+strings.Join(names, " ")+" ", " "+want+" ") {
			t.Fatalf("tools = %v, missing %s", names, want)
		}
	}

	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.IsError {
			t.Fatalf("%s failed: %s", name, toolText(res))
		}
		out, _ := res.StructuredContent.(map[string]any)
		if out == nil {
			t.Fatalf("%s: no structured content: %s", name, toolText(res))
		}
		return out
	}

	who := call("discover", nil)["principal"].(map[string]any)
	project := who["personal_project_id"].(string)
	task := call("task_apply", map[string]any{
		"project_id": project,
		"task_name":  "hello",
		"manifest": map[string]any{
			"runtime":    map[string]any{"language": "python", "version": "3.12"},
			"entrypoint": []string{"python", "main.py"},
		},
		"files": []map[string]any{{"path": "main.py", "content": "import sbx\nprint('hello from mcp')\nsbx.result('ok', 'said hello', {'n': 1})\n"}},
	})
	run := call("run_start", map[string]any{"task_id": task["task"].(map[string]any)["id"]})
	got := call("run_get", map[string]any{"run_id": run["id"], "wait": 60})
	for range 5 {
		if got["status"] == "completed" || got["status"] == "failed" {
			break
		}
		got = call("run_get", map[string]any{"run_id": run["id"], "wait": 60})
	}
	if got["status"] != "completed" {
		t.Fatalf("run = %v", got)
	}
	if res, _ := got["result"].(map[string]any); res["summary"] != "said hello" {
		t.Fatalf("result = %v", got["result"])
	}
	logs := call("run_logs", map[string]any{"run_id": run["id"], "tail": 20})
	if !strings.Contains(fmt.Sprint(logs["lines"]), "hello from mcp") {
		t.Fatalf("logs = %v", logs)
	}

	// Cancelling a finished run changes nothing.
	if got := call("run_cancel", map[string]any{"run_id": run["id"]}); got["status"] != "completed" || got["cancel_requested"] != false {
		t.Fatalf("run_cancel on a finished run = %v", got)
	}
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func toolText(res *mcpsdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
