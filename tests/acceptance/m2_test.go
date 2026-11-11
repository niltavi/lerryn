//go:build integration

package acceptance

import (
	"bufio"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The M2 acceptance scenarios (implementation plan, M2 Agent 入口).

const reportTask = `import json, os
import sbx

rows = sbx.params().get("rows", 3)
with sbx.stage("fetch"):
    print(f"fetched {rows} rows", flush=True)
with sbx.stage("save"):
    with open("/sbx/out/report.json", "w") as f:
        json.dump({"rows": rows}, f)
sbx.result("ok", f"saved {rows} rows", {"rows": rows})
`

func reportManifest() map[string]any {
	return map[string]any{
		"description": "汇总并保存报告",
		"runtime":     map[string]any{"language": "python", "version": "3.12"},
		"entrypoint":  []string{"python", "main.py"},
		"workflow":    []map[string]any{{"title": "生成报告", "description": "按参数汇总指定行数，并保存报告。"}},
		"params":      map[string]any{"rows": 3},
		"stages":      []map[string]any{{"id": "fetch", "name": "获取"}, {"id": "save", "name": "保存", "after": []string{"fetch"}}},
		"limits":      map[string]any{"timeout": "5m", "memory": "256Mi"},
		"network":     "none",
	}
}

// A01: an agent builds a task through MCP alone: it previews and submits
// the task, tests it, checks the result and artifacts, validates and
// activates it, without any console page.
func TestA01AgentBuildsATask(t *testing.T) {
	s := startInstance(t)
	agent := s.signUp("alice").agent("Claude Code · 笔记本")
	m := agent.mcp(t)

	project := m.ok(t, "discover", nil)["principal"].(map[string]any)["personal_project_id"]
	args := map[string]any{
		"project_id": project, "task_name": "weekday-report", "manifest": reportManifest(),
		"files": []map[string]any{{"path": "main.py", "content": reportTask}},
	}
	preview := m.ok(t, "task_apply", withArgs(args, "dry_run", true))
	if preview["dry_run"] != true || preview["created"] != true || preview["task"] != nil {
		t.Fatalf("preview = %v", preview)
	}
	if list := agent.want(t, http.StatusOK, http.MethodGet, "/api/v1/tasks?name=weekday-report", nil); len(list["items"].([]any)) != 0 {
		t.Fatalf("the dry run created a task: %v", list)
	}
	applied := m.ok(t, "task_apply", args)
	task := applied["task"].(map[string]any)
	taskID := task["id"]
	if client := task["latest_version"].(map[string]any)["client"]; client != "Claude Code · 笔记本" {
		t.Fatalf("version client = %v", client)
	}

	if out, isErr := m.call(t, "task_set_active", map[string]any{"task_id": taskID, "active": true}); !isErr || out["code"] != "version_not_tested" {
		t.Fatalf("activating an untested version = %v", out)
	}
	run := m.ok(t, "run_start", map[string]any{"task_id": taskID, "test": true, "idempotency_key": "a01-test-1"})
	if retry := m.ok(t, "run_start", map[string]any{"task_id": taskID, "test": true, "idempotency_key": "a01-test-1"}); retry["id"] != run["id"] {
		t.Fatalf("a retried run_start started a second run: %v and %v", run["id"], retry["id"])
	}
	done := m.waitRun(t, run["id"])
	if done["status"] != "completed" || done["result"].(map[string]any)["summary"] != "saved 3 rows" {
		t.Fatalf("test run = %v", done)
	}
	for _, st := range done["stages"].([]any) {
		if st.(map[string]any)["status"] != "done" {
			t.Fatalf("stages = %v", done["stages"])
		}
	}
	runPath := fmt.Sprintf("/api/v1/runs/%v", run["id"])

	// The run left its report as an artifact.
	arts := agent.want(t, http.StatusOK, http.MethodGet, runPath+"/artifacts", nil)["items"].([]any)
	if len(arts) != 1 || arts[0].(map[string]any)["path"] != "report.json" || arts[0].(map[string]any)["preview"] != "json" {
		t.Fatalf("artifacts = %v", arts)
	}
	link := strings.TrimPrefix(arts[0].(map[string]any)["links"].(map[string]any)["download"].(string), s.base)
	if r := agent.do(t, http.MethodGet, link, nil); r.status != http.StatusOK || strings.TrimSpace(string(r.body)) != `{"rows": 3}` {
		t.Fatalf("artifact download = %d %q", r.status, r.body)
	}
	// The timeline has every status change and stage, in order.
	events := agent.want(t, http.StatusOK, http.MethodGet, runPath+"/events", nil)
	var kinds []string
	for _, e := range events["events"].([]any) {
		ev := e.(map[string]any)
		k := ev["kind"].(string)
		if k == "status" {
			k += ":" + ev["data"].(map[string]any)["to"].(string)
		}
		kinds = append(kinds, k)
	}
	if got := strings.Join(kinds, " "); events["complete"] != true ||
		!strings.HasPrefix(got, "status:queued status:preparing status:running stage.start stage.done stage.start") ||
		!strings.HasSuffix(got, "status:completed") || !strings.Contains(got, "artifact") || !strings.Contains(got, "result") {
		t.Fatalf("events = %s", got)
	}
	// The log stream replays the finished log and ends.
	lines, end := readLogStream(t, agent, runPath+"/logs/stream", "")
	if !strings.Contains(strings.Join(lines, "\n"), "fetched 3 rows") || !strings.Contains(end, `"status":"completed"`) {
		t.Fatalf("stream lines %q, end %s", lines, end)
	}

	report := m.ok(t, "task_validate", map[string]any{"task_id": taskID, "seq": 1})
	if report["valid"] != true || report["activatable"] != true {
		t.Fatalf("validation = %v", report)
	}
	active := m.ok(t, "task_set_active", map[string]any{"task_id": taskID, "active": true})
	if active["state"] != "active" || active["active_version"] != float64(1) {
		t.Fatalf("activated = %v", active)
	}
	if link := active["links"].(map[string]any)["console"]; link != fmt.Sprintf("%s/tasks/%v", s.base, taskID) {
		t.Fatalf("console link = %v", link)
	}
	overview := agent.want(t, http.StatusOK, http.MethodGet, "/api/v1/overview", nil)
	if counts := overview["counts"].(map[string]any); counts["tasks"] != float64(1) || counts["active_tasks"] != float64(1) {
		t.Fatalf("overview counts = %v", counts)
	}
}

// A11: an agent finds out why a run failed from its ID alone: status,
// error class, stage, a bounded slice of the log, then fixes the task and
// reruns, and the new run is linked to the failed one.
func TestA11FailureDiagnosis(t *testing.T) {
	s := startInstance(t)
	agent := s.signUp("alice").agent("agent")
	m := agent.mcp(t)

	broken := `import sbx
with sbx.stage("fetch"):
    print("calling crm", flush=True)
    raise RuntimeError("crm returned 500")
`
	manifest := reportManifest()
	apply := func(content string, extra ...any) map[string]any {
		args := map[string]any{"project_id": agent.project, "task_name": "digest", "manifest": manifest,
			"files": []map[string]any{{"path": "main.py", "content": content}}}
		for i := 0; i+1 < len(extra); i += 2 {
			args[extra[i].(string)] = extra[i+1]
		}
		return m.ok(t, "task_apply", args)
	}
	taskID := apply(broken)["task"].(map[string]any)["id"]
	run := m.ok(t, "run_start", map[string]any{"task_id": taskID, "test": true, "params": map[string]any{"rows": 7}})
	failed := m.waitRun(t, run["id"])
	runErr, _ := failed["error"].(map[string]any)
	if failed["status"] != "failed" || runErr["code"] != "script_error" || runErr["stage"] != "fetch" || failed["exit_code"] != float64(1) {
		t.Fatalf("failed run = %v", failed)
	}
	logs := m.ok(t, "run_logs", map[string]any{"run_id": run["id"], "tail": 20})
	text := fmt.Sprint(logs["lines"])
	if !strings.Contains(text, "RuntimeError: crm returned 500") || logs["complete"] != true {
		t.Fatalf("log tail = %v", logs)
	}
	// The same failure as the console's list would show it.
	page := agent.want(t, http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/runs?status=failed&task_id=%v", taskID), nil)
	if items := page["items"].([]any); len(items) != 1 || items[0].(map[string]any)["error"].(map[string]any)["code"] != "script_error" {
		t.Fatalf("failed runs = %v", page)
	}

	// Fix and rerun: the rerun uses the new version and the old params.
	fixed := apply(reportTask, "if_match", "1")
	if fixed["version"] != float64(2) || fixed["diff"] == nil {
		t.Fatalf("fix = %v", fixed)
	}
	rerun := m.ok(t, "run_rerun", map[string]any{"run_id": run["id"], "idempotency_key": "a11-rerun"})
	if rerun["rerun_of"] != run["id"] || rerun["version"] != float64(2) || rerun["params"].(map[string]any)["rows"] != float64(7) {
		t.Fatalf("rerun = %v", rerun)
	}
	done := m.waitRun(t, rerun["id"])
	if done["status"] != "completed" || done["result"].(map[string]any)["summary"] != "saved 7 rows" {
		t.Fatalf("rerun result = %v", done)
	}
	linked := agent.want(t, http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/runs?rerun_of=%v", run["id"]), nil)
	if items := linked["items"].([]any); len(items) != 1 || items[0].(map[string]any)["id"] != rerun["id"] {
		t.Fatalf("reruns of the failed run = %v", linked)
	}
	// The failed run still records the version it ran.
	if old := m.ok(t, "run_get", map[string]any{"run_id": run["id"]}); old["version"] != float64(1) || old["status"] != "failed" {
		t.Fatalf("original run = %v", old)
	}
}

// A12: two agent sessions edit the same task. The one that submits on a
// stale version is refused with the current version, and a run started
// on the old version keeps its configuration.
func TestA12VersionConflict(t *testing.T) {
	s := startInstance(t)
	alice := s.signUp("alice")
	first, second := alice.agent("session-1"), alice.agent("session-2")
	path := "/api/v1/projects/" + alice.project + "/tasks/digest"
	body := func(rows int) map[string]any {
		mf := reportManifest()
		mf["params"] = map[string]any{"rows": rows}
		return map[string]any{"manifest": mf, "files": []map[string]any{{"path": "main.py", "content": reportTask}}}
	}
	created := first.want(t, http.StatusOK, http.MethodPut, path, body(3))
	taskID := created["task"].(map[string]any)["id"].(string)

	// Both sessions read version 1; a run starts on it.
	for _, c := range []*client{first, second} {
		if v := c.want(t, http.StatusOK, http.MethodGet, "/api/v1/tasks/"+taskID, nil)["latest_version"].(map[string]any)["seq"]; v != float64(1) {
			t.Fatalf("read version %v", v)
		}
	}
	run := first.want(t, http.StatusAccepted, http.MethodPost, "/api/v1/tasks/"+taskID+"/runs", nil)

	first.want(t, http.StatusOK, http.MethodPut, path, body(5), "If-Match", "1")
	conflict := second.do(t, http.MethodPut, path, body(9), "If-Match", "1")
	p := conflict.json(t)
	if conflict.status != http.StatusPreconditionFailed || p["code"] != "version_conflict" || p["current_version"] != float64(2) {
		t.Fatalf("stale submission = %d %s", conflict.status, conflict.body)
	}
	if r := second.do(t, http.MethodPut, path, body(9)); r.status != http.StatusPreconditionRequired {
		t.Fatalf("submission without If-Match = %d %s", r.status, r.body)
	}
	// The second session reads what changed, then resubmits on top of it.
	diff := second.want(t, http.StatusOK, http.MethodGet, "/api/v1/tasks/"+taskID+"/versions/2/diff", nil)
	if patch, _ := diff["manifest_patch"].(string); !strings.Contains(patch, `"rows": 5`) {
		t.Fatalf("diff = %v", diff)
	}
	second.want(t, http.StatusOK, http.MethodPut, path, body(9), "If-Match", "2")

	done := first.waitRun(t, run["id"].(string), 5*time.Minute)
	if done["status"] != "completed" || done["version"] != float64(1) || done["params"].(map[string]any)["rows"] != float64(3) ||
		done["result"].(map[string]any)["summary"] != "saved 3 rows" {
		t.Fatalf("run on version 1 = %v", done)
	}
	v1 := first.want(t, http.StatusOK, http.MethodGet, "/api/v1/tasks/"+taskID+"/versions/1", nil)
	if rows := v1["manifest"].(map[string]any)["params"].(map[string]any)["rows"]; rows != float64(3) {
		t.Fatalf("version 1 changed: %v", v1["manifest"])
	}
	versions := first.want(t, http.StatusOK, http.MethodGet, "/api/v1/tasks/"+taskID+"/versions", nil)["items"].([]any)
	if len(versions) != 3 {
		t.Fatalf("versions = %v", versions)
	}
}

// readLogStream reads a log stream until its end event and returns the
// text of each line and the end event's data.
func readLogStream(t *testing.T, c *client, path, lastEventID string) ([]string, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, c.s.base+path, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream = %d %s", resp.StatusCode, resp.Header)
	}
	var (
		lines []string
		event string
	)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data := strings.TrimPrefix(line, "data: ")
			if event == "end" {
				return lines, data
			}
			if event == "line" {
				lines = append(lines, data)
			}
		}
	}
	t.Fatalf("the stream ended without an end event: %v", sc.Err())
	return nil, ""
}

func withArgs(args map[string]any, kv ...any) map[string]any {
	out := make(map[string]any, len(args)+len(kv)/2)
	for k, v := range args {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}
