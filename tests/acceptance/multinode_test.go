//go:build integration

package acceptance

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The scenarios of a multi-node deployment: A14 with remote runners, B01
// with three server replicas, and B03's SSE resume across replicas. They
// share one cluster (newCluster): three servers on PostgreSQL behind a
// load balancer, a general runner and a runner restricted to network
// none.
//
//	go test -tags integration -run TestMultiNode -v ./tests/acceptance/
//
// LERRYN_B01_MINUTES sets how long B01 watches the schedule (default 4;
// the PRD's 24 hours is LERRYN_B01_MINUTES=1440).
func TestMultiNode(t *testing.T) {
	// B01 mostly waits for the clock; run it alongside the other
	// scheduling scenarios.
	t.Parallel()
	c := newCluster(t)
	servers := []*node{c.startServer("server-1"), c.startServer("server-2"), c.startServer("server-3")}
	c.startRunner("runner-general", 4)
	c.startRunner("runner-restricted", 1, "LERRYN_RUNNER_LABELS=restricted", "LERRYN_RUNNER_ALLOWED_NETWORKS=none")
	db := c.db()

	// Pin the reports of A14 to remote runners: the deployment has no
	// embedded runner, so every run below crosses the HTTP runner protocol.
	t.Run("A14 tools and isolation", func(t *testing.T) { testA14(t, c) })
	t.Run("B01 B03 schedule and SSE across a leader failure", func(t *testing.T) { testB01(t, c, db, servers) })
}

// probeTask tries to leave the sandbox in several ways and reports, for
// each attempt, "allowed" or the exception it met.
const probeTask = `import os, socket, subprocess
import sbx

p = sbx.params()
out = {}

def attempt(name, fn):
    try:
        fn()
        out[name] = "allowed"
    except Exception as e:
        out[name] = type(e).__name__

def write(path):
    with open(path, "w") as f:
        f.write("x")

attempt("write_root", lambda: write("/escape.txt"))
attempt("write_task_files", lambda: write("/sbx/task/escape.txt"))
attempt("write_etc", lambda: write("/etc/escape.txt"))
attempt("write_work", lambda: write("/sbx/work/ok.txt"))
attempt("setuid_root", lambda: os.setuid(0))
attempt("read_runner_dir", lambda: os.listdir(p["runner_dir"]))
attempt("docker_socket", lambda: os.stat("/var/run/docker.sock"))

def connect():
    socket.create_connection(("1.1.1.1", 53), timeout=3).close()
attempt("network", connect)

def exec_tmp():
    write("/tmp/run.sh")
    os.chmod("/tmp/run.sh", 0o755)
    subprocess.run(["/tmp/run.sh"], check=True)
attempt("exec_from_tmp", exec_tmp)

procs = []
def fork_many():
    for _ in range(400):
        procs.append(subprocess.Popen(["sleep", "60"]))
attempt("start_400_processes", fork_many)
out["processes_started"] = len(procs)
for pr in procs:
    pr.kill()
for pr in procs:
    pr.wait()

out["uid"] = os.getuid()
out["gid"] = os.getgid()
with open("/proc/self/status") as f:
    out["cap_eff"] = [l.split()[1] for l in f if l.startswith("CapEff")][0]
sbx.result("ok", "probed", out)
`

// testA14: an agent discovers what the deployment offers and uses it;
// leaving the sandbox, using a credential that was not granted and going
// over a resource limit are stopped by the platform, not by the task's
// good will. Every run executes on a remote runner.
func testA14(t *testing.T, c *cluster) {
	cs := &credServer{t: t, base: c.lb.URL, dir: c.dir}
	alice := cs.signUp("alice")
	bob := cs.signUp("bob")

	// Discovery over MCP: the tools and the runtimes and network policies
	// of this deployment.
	agent := (&client{s: &instance{t: t, base: c.lb.URL}, token: alice.token, project: alice.project}).mcp(t)
	tools, err := agent.cs.ListTools(agent.ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"discover", "task_apply", "run_start", "run_get", "run_logs", "credential_request", "api_describe", "api_call"} {
		if !names[want] {
			t.Errorf("MCP tools lack %s: %v", want, names)
		}
	}
	disc := agent.ok(t, "discover", nil)
	if fmt.Sprint(disc["capabilities"].(map[string]any)["network_policies"]) != "[none full]" || len(disc["runtimes"].([]any)) != 3 {
		t.Fatalf("discover = %v", disc)
	}

	t.Run("tools", func(t *testing.T) {
		// Each user registers a tool in their own project. The agent finds
		// only alice's through api_call.
		var tool struct {
			ID string `json:"id"`
		}
		alice.api(http.MethodPost, "/api/v1/projects/"+alice.project+"/tools", map[string]any{
			"name": "price-api", "kind": "http", "description": "查询商品价格：GET /prices?sku=…", "url": "https://prices.example.com/v1",
		}, http.StatusOK, &tool)
		bob.api(http.MethodPost, "/api/v1/projects/"+bob.project+"/tools", map[string]any{
			"name": "bob-crm", "kind": "mcp", "description": "bob 的 CRM", "url": "https://crm.example.com/mcp",
		}, http.StatusOK, nil)
		found, _ := json.Marshal(agent.ok(t, "api_call", map[string]any{"operation_id": "listTools", "arguments": map[string]any{}}))
		if !strings.Contains(string(found), "price-api") || strings.Contains(string(found), "bob-crm") {
			t.Fatalf("listTools for alice = %s", found)
		}

		// A task that names the tool gets its address on the runner.
		uses := alice.applyTask("uses-tool", "import json, os\nprint('url', os.environ['LERRYN_TOOL_PRICE_API_URL'])\n"+
			"print('tools', [t['name'] for t in json.loads(os.environ['LERRYN_TOOLS'])])\n",
			map[string]any{"tools": []string{"price-api"}, "credentials": []any{}})
		r := alice.startRun(uses.ID)
		got, body := alice.waitRun(r.ID, true)
		_, lines := alice.runLogs(r.ID)
		if got.Status != "completed" || !contains(lines, "url https://prices.example.com/v1") || !contains(lines, "tools ['price-api']") {
			t.Fatalf("run with a tool = %s\nlogs: %q", body, lines)
		}

		// A tool of another project, or one nobody registered, cannot be
		// used: the version does not validate and cannot be activated.
		for _, name := range []string{"bob-crm", "crm-search"} {
			tk := alice.applyTask("uses-"+name, "print('hi')\n", map[string]any{"tools": []string{name}, "credentials": []any{}})
			var report struct {
				Activatable bool `json:"activatable"`
				Errors      []struct {
					Pointer string `json:"pointer"`
				} `json:"errors"`
			}
			alice.api(http.MethodPost, "/api/v1/tasks/"+tk.ID+"/versions/1/validate", nil, http.StatusOK, &report)
			if report.Activatable || len(report.Errors) == 0 || report.Errors[0].Pointer != "#/manifest/tools/0" {
				t.Errorf("validation of tool %s = %+v", name, report)
			}
		}

		// Once removed, the tool is not handed to a run that was valid.
		alice.api(http.MethodDelete, "/api/v1/tools/"+tool.ID, nil, http.StatusNoContent, nil)
		r = alice.startRun(uses.ID)
		got, body = alice.waitRun(r.ID, true)
		_, lines = alice.runLogs(r.ID)
		if got.Status != "failed" || got.Error == nil || got.Error.Code != "validation_failed" || len(lines) > 0 && strings.HasPrefix(lines[0], "url") {
			t.Fatalf("run after the tool was removed = %s\nlogs: %q", body, lines)
		}
	})

	t.Run("sandbox boundaries", func(t *testing.T) {
		tk := alice.applyTask("probe", probeTask, map[string]any{"credentials": []any{}, "limits": map[string]any{"timeout": "2m", "memory": "256Mi"}})
		var r run
		alice.api(http.MethodPost, "/api/v1/tasks/"+tk.ID+"/runs", map[string]any{"test": true,
			"params": map[string]any{"runner_dir": c.runners["runner-general"]}}, http.StatusAccepted, &r)
		final, body := alice.waitRun(r.ID, true)
		var detail struct {
			Result struct {
				Data map[string]any `json:"data"`
			} `json:"result"`
		}
		_ = json.Unmarshal([]byte(body), &detail)
		got := detail.Result.Data
		if final.Status != "completed" || got == nil {
			_, lines := alice.runLogs(r.ID)
			t.Fatalf("probe run = %s\nlogs: %q", body, lines)
		}
		want := map[string]string{
			"write_root": "OSError", "write_task_files": "OSError", "write_etc": "OSError", "write_work": "allowed",
			"setuid_root": "PermissionError", "read_runner_dir": "FileNotFoundError", "docker_socket": "FileNotFoundError",
			"network": "OSError", "exec_from_tmp": "PermissionError", "start_400_processes": "BlockingIOError",
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s = %v, want %s", k, got[k], v)
			}
		}
		if n, _ := got["processes_started"].(float64); n < 50 || n >= 400 {
			t.Errorf("processes started = %v, want the pids limit to stop it below 400", got["processes_started"])
		}
		if got["uid"] != float64(65532) || got["gid"] != float64(65532) || got["cap_eff"] != "0000000000000000" {
			t.Errorf("identity = uid %v gid %v caps %v", got["uid"], got["gid"], got["cap_eff"])
		}
	})

	t.Run("resource limits", func(t *testing.T) {
		hog := alice.applyTask("hog", `import sys, time
import sbx
mode = sbx.params()["mode"]
if mode == "memory":
    chunks = []
    for _ in range(64):
        chunks.append(b"\x01" * (16 << 20))
elif mode == "time":
    time.sleep(120)
elif mode == "output":
    for i in range(210000):
        sys.stdout.write("line %d\n" % i)
print("done", flush=True)
`, map[string]any{"credentials": []any{}, "limits": map[string]any{"timeout": "5s", "memory": "128Mi"}})
		start := func(mode string) run {
			var r run
			alice.api(http.MethodPost, "/api/v1/tasks/"+hog.ID+"/runs", map[string]any{"test": true, "params": map[string]any{"mode": mode}},
				http.StatusAccepted, &r)
			return r
		}
		mem, slow, loud := start("memory"), start("time"), start("output")
		if got, body := alice.waitRun(mem.ID, true); got.Status != "failed" || got.Error == nil || got.Error.Code != "out_of_memory" {
			t.Errorf("memory hog = %s", body)
		}
		if got, body := alice.waitRun(slow.ID, true); got.Status != "timed_out" {
			t.Errorf("slow run = %s", body)
		}
		got, body := alice.waitRun(loud.ID, true)
		var events struct {
			Events []struct {
				Kind string `json:"kind"`
			} `json:"events"`
		}
		alice.api(http.MethodGet, "/api/v1/runs/"+loud.ID+"/events", nil, http.StatusOK, &events)
		truncated := false
		for _, e := range events.Events {
			truncated = truncated || e.Kind == "log.truncated"
		}
		var detail struct {
			DroppedLogLines int64 `json:"dropped_log_lines"`
		}
		_ = json.Unmarshal([]byte(body), &detail)
		// 210 000 lines in 5 seconds may also time out; the log cap
		// applies either way.
		if (got.Status != "completed" && got.Status != "timed_out") || !truncated || detail.DroppedLogLines == 0 {
			t.Errorf("output flood = %s, log.truncated %v", body, truncated)
		}
	})

	t.Run("network policy of the deployment", func(t *testing.T) {
		// The restricted runner allows network none only; a task that asks
		// for full network on it fails before any code runs.
		tk := alice.applyTask("wants-network", "print('started')\n", map[string]any{"credentials": []any{}, "network": "full",
			"runner_labels": []string{"restricted"}})
		r := alice.startRun(tk.ID)
		got, body := alice.waitRun(r.ID, true)
		_, lines := alice.runLogs(r.ID)
		if got.Status != "failed" || got.Error == nil || got.Error.Code != "validation_failed" || contains(lines, "started") {
			t.Fatalf("full network on a none-only runner = %s\nlogs: %q", body, lines)
		}
	})

	t.Run("credentials", func(t *testing.T) {
		const secret = "a14-remote-runner-secret-value" // #nosec G101 -- a test value
		// Alice's own credential, granted to her task, reaches it on a
		// remote runner and is redacted there.
		own := alice.applyTask("own-token", "import os\nprint('token', os.environ['API_TOKEN'], len(os.environ['API_TOKEN']))\n", nil)
		req := alice.requestCredential(own.ID)
		alice.fill(req.URL, map[string]string{"API_TOKEN": secret})
		r := alice.startRun(own.ID)
		got, body := alice.waitRun(r.ID, true)
		logsBody, lines := alice.runLogs(r.ID)
		if got.Status != "completed" || !contains(lines, fmt.Sprintf("token [REDACTED] %d", len(secret))) ||
			strings.Contains(body+logsBody, secret) {
			t.Fatalf("run with a granted credential = %s\nlogs: %q", body, lines)
		}

		// Bob's credential cannot be granted to alice's task, nor read.
		bobTask := bob.applyTask("bob-token", "print('bob')\n", nil)
		bobReq := bob.requestCredential(bobTask.ID)
		bob.fill(bobReq.URL, map[string]string{"API_TOKEN": "value-of-bob"})
		var bobView credentialRequest
		bob.api(http.MethodGet, "/api/v1/credential-requests/"+bobReq.ID, nil, http.StatusOK, &bobView)
		ungranted := alice.applyTask("not-granted", "print('started', flush=True)\n", nil)
		alice.api(http.MethodPut, "/api/v1/credentials/"+*bobView.CredentialID+"/grants/"+ungranted.ID, map[string]any{"name": "API_TOKEN"},
			http.StatusNotFound, nil)
		alice.api(http.MethodGet, "/api/v1/credentials/"+*bobView.CredentialID, nil, http.StatusNotFound, nil)

		// Without a grant an API start is refused up front ...
		refused := alice.api(http.MethodPost, "/api/v1/tasks/"+ungranted.ID+"/runs", map[string]any{"test": true}, http.StatusUnprocessableEntity, nil)
		if !strings.Contains(refused, "credential_missing") {
			t.Fatalf("start without a grant = %s", refused)
		}
		// ... and a webhook run, which is not checked up front, fails on
		// the runner before the task's code runs.
		var set triggerSet
		alice.api(http.MethodPut, "/api/v1/tasks/"+ungranted.ID+"/triggers", map[string]any{"triggers": []any{
			map[string]any{"name": "push", "type": "webhook"},
		}}, http.StatusOK, &set)
		alice.api(http.MethodPost, "/api/v1/tasks/"+ungranted.ID+"/activation", map[string]any{"active": true}, http.StatusOK, nil)
		hook, _ := http.NewRequest(http.MethodPost, c.lb.URL+"/hooks/"+set.Triggers[0].ID, strings.NewReader(`{}`))
		hook.Header.Set("Authorization", "Bearer "+*set.Triggers[0].Webhook.Token)
		hook.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(hook)
		if err != nil {
			t.Fatal(err)
		}
		var accepted struct {
			RunID string `json:"run_id"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&accepted)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("webhook = %d", resp.StatusCode)
		}
		got, body = alice.waitRun(accepted.RunID, true)
		_, lines = alice.runLogs(accepted.RunID)
		if got.Status != "failed" || got.Error == nil || got.Error.Code != "credential_missing" || contains(lines, "started") {
			t.Fatalf("webhook run without a grant = %s\nlogs: %q", body, lines)
		}
	})
}

// ticksTask prints a numbered line every 250 ms.
const ticksTask = `import time
import sbx
n = int(sbx.params().get("n", 0))
for i in range(n):
    print(f"tick {i}", flush=True)
    time.sleep(0.25)
`

// testB01: with three server replicas on one database, an every-minute
// schedule produces exactly one run per scheduled minute, also across a
// crash of the leader, which runs the scheduler. While the leader goes
// down, a log stream that was following a run on it continues from
// another replica with Last-Event-ID, without a gap (B03).
func testB01(t *testing.T, c *cluster, db *sql.DB, servers []*node) {
	minutes := 4
	if v := os.Getenv("LERRYN_B01_MINUTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 3 {
			t.Fatalf("LERRYN_B01_MINUTES=%q: want a number of minutes, at least 3", v)
		}
		minutes = n
	}
	api := c.api()
	project := api.signUp("scheduler")
	var tasks, triggers []string
	for _, name := range []string{"minutely-a", "minutely-b"} {
		id := api.applyTask(project, name, "print('scheduled run', flush=True)\n", map[string]any{"network": "none"})
		var set triggerSet
		api.call(http.MethodPut, "/api/v1/tasks/"+id+"/triggers", map[string]any{"triggers": []any{
			map[string]any{"name": "every-minute", "type": "cron", "schedule": "* * * * *", "timezone": "Asia/Shanghai"},
		}}, http.StatusOK, &set)
		api.activate(id)
		tasks, triggers = append(tasks, id), append(triggers, set.Triggers[0].ID)
	}
	started := time.Now()
	ticks := api.applyTask(project, "ticks", ticksTask, map[string]any{"network": "none", "concurrency": "allow"})

	// Let the schedule settle, then crash the leader shortly before a
	// minute boundary, while a log stream follows a run through it.
	waitFor(t, 2*time.Minute, "first scheduled runs completed", func() bool {
		for _, task := range tasks {
			if completedRuns(t, api, task) == 0 {
				return false
			}
		}
		return true
	})
	leader := c.leader(db)
	var other *node
	for _, s := range servers {
		if s != leader {
			other = s
			break
		}
	}
	// Start the followed run about 20 s before the next minute, so the
	// crash lands inside it and shortly before a scheduled fire.
	boundary := time.Now().Truncate(time.Minute).Add(time.Minute)
	if time.Until(boundary) < 25*time.Second {
		boundary = boundary.Add(time.Minute)
	}
	time.Sleep(time.Until(boundary.Add(-20 * time.Second)))
	var r apiRun
	api.call(http.MethodPost, "/api/v1/tasks/"+ticks+"/runs", map[string]any{"test": true, "params": map[string]any{"n": 120}}, http.StatusAccepted, &r)
	stream := openStream(t, leader.url, api.token, r.ID, "")
	seen := stream.readLines(t, 10)
	time.Sleep(time.Until(boundary.Add(-8 * time.Second)))
	leader.kill()
	killedAt := time.Now()
	t.Logf("killed leader %s at %s", leader.name, killedAt.Format(time.TimeOnly))
	stream.close()
	// The client reconnects to another replica and resumes after the last
	// line it got.
	resumed := openStream(t, other.url, api.token, r.ID, strconv.FormatInt(seen[len(seen)-1].Seq, 10))
	rest := resumed.readToEnd(t)
	// Line numbers run on without a gap across the reconnect; the task's
	// own lines are all there, followed by the runner's closing line.
	var ticked []string
	for i, l := range append(seen, rest...) {
		if l.Seq != int64(i+1) {
			t.Fatalf("line %d after resuming on %s has number %d", i+1, other.name, l.Seq)
		}
		if l.Stream == "stdout" {
			ticked = append(ticked, l.Text)
		}
	}
	if len(ticked) != 120 {
		t.Fatalf("read %d task lines across the failure, want 120", len(ticked))
	}
	for i, text := range ticked {
		if text != fmt.Sprintf("tick %d", i) {
			t.Fatalf("task line %d = %q", i, text)
		}
	}

	// Another replica takes over the schedule.
	if next := c.leader(db); next == leader {
		t.Fatal("the killed server still leads")
	}
	end := started.Add(time.Duration(minutes) * time.Minute)
	for time.Now().Before(end) {
		time.Sleep(5 * time.Second)
	}
	// The last fire may still be running; wait until every run has ended.
	waitFor(t, 2*time.Minute, "scheduled runs ended", func() bool {
		for _, task := range tasks {
			for _, run := range listTaskRuns(t, api, task) {
				if run.Status == "queued" || run.Status == "preparing" || run.Status == "running" {
					return false
				}
			}
		}
		return true
	})

	for i, task := range tasks {
		runs := listTaskRuns(t, api, task)
		byMinute := map[time.Time]int{}
		var first, last time.Time
		for _, run := range runs {
			at := run.Trigger.ScheduledAt
			if run.Trigger.Kind != "cron" || run.Trigger.TriggerID != triggers[i] || at == nil {
				t.Fatalf("run %+v is not a run of the trigger", run)
			}
			if run.Status != "completed" {
				t.Errorf("run %s at %s ended %s (%s)", run.ID, at.Format(time.TimeOnly), run.Status, run.SkipReason)
			}
			byMinute[*at]++
			if first.IsZero() || at.Before(first) {
				first = *at
			}
			if at.After(last) {
				last = *at
			}
		}
		for m := first; !m.After(last); m = m.Add(time.Minute) {
			if byMinute[m] != 1 {
				t.Errorf("task %d: %d runs for %s, want exactly one", i, byMinute[m], m.Format(time.TimeOnly))
			}
		}
		if got := int(last.Sub(first)/time.Minute) + 1; got < minutes-1 {
			t.Errorf("task %d: scheduled minutes %s to %s, want at least %d", i, first.Format(time.TimeOnly), last.Format(time.TimeOnly), minutes-1)
		}
		t.Logf("task %d: %d scheduled minutes, %s to %s, one run each", i, len(byMinute), first.Format(time.TimeOnly), last.Format(time.TimeOnly))
	}
	// The database agrees: no scheduled time has two runs.
	var dup int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM (SELECT trigger_id, scheduled_at FROM runs
		WHERE trigger_id IS NOT NULL GROUP BY trigger_id, scheduled_at HAVING count(*) > 1) d`).Scan(&dup); err != nil {
		t.Fatal(err)
	}
	if dup != 0 {
		t.Fatalf("%d scheduled times have more than one run", dup)
	}
}

// listTaskRuns lists every run of a task through the API.
func listTaskRuns(t *testing.T, s *server, taskID string) []apiRun {
	t.Helper()
	var out []apiRun
	cursor := ""
	for {
		path := "/api/v1/runs?limit=100&task_id=" + taskID
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var page struct {
			Items      []apiRun `json:"items"`
			NextCursor *string  `json:"next_cursor"`
		}
		s.call(http.MethodGet, path, nil, http.StatusOK, &page)
		out = append(out, page.Items...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return out
		}
		cursor = *page.NextCursor
	}
}

func completedRuns(t *testing.T, s *server, taskID string) int {
	t.Helper()
	n := 0
	for _, r := range listTaskRuns(t, s, taskID) {
		if r.Status == "completed" {
			n++
		}
	}
	return n
}

// logEvent is a line of a log stream.
type logEvent struct {
	Seq    int64  `json:"seq"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

// sseStream reads a run's log stream.
type sseStream struct {
	resp *http.Response
	sc   *bufio.Scanner
}

func openStream(t *testing.T, base, token, runID, lastEventID string) *sseStream {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/runs/"+runID+"/logs/stream", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req) // #nosec G704 -- a server of this test
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("stream = %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	return &sseStream{resp: resp, sc: sc}
}

// next returns the next event's name and data; ok is false at the end of
// the connection.
func (s *sseStream) next() (event, data string, ok bool) {
	for s.sc.Scan() {
		line := s.sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case line == "" && event != "":
			return event, data, true
		}
	}
	return "", "", false
}

// readLines reads n log lines.
func (s *sseStream) readLines(t *testing.T, n int) []logEvent {
	t.Helper()
	var out []logEvent
	for len(out) < n {
		ev, data, ok := s.next()
		if !ok {
			t.Fatalf("the stream ended after %d lines", len(out))
		}
		if ev == "line" {
			var l logEvent
			if err := json.Unmarshal([]byte(data), &l); err != nil {
				t.Fatal(err)
			}
			out = append(out, l)
		}
	}
	return out
}

// readToEnd reads lines until the end event.
func (s *sseStream) readToEnd(t *testing.T) []logEvent {
	t.Helper()
	defer s.close()
	var out []logEvent
	for {
		ev, data, ok := s.next()
		if !ok {
			t.Fatalf("the stream ended without an end event after %d lines", len(out))
		}
		switch ev {
		case "end":
			return out
		case "line":
			var l logEvent
			if err := json.Unmarshal([]byte(data), &l); err != nil {
				t.Fatal(err)
			}
			out = append(out, l)
		}
	}
}

func (s *sseStream) close() { _ = s.resp.Body.Close() }
