package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/niltavi/lerryn/api"
	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/mcp"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

// TestMCPTaskLifecycle drives the M2 agent flow through the MCP tools
// only, with the coordinator standing in for the runner: preview, submit,
// test, fix a failure, rerun, validate and activate (A01, A11), and the
// conflict a stale session gets (A12).
func TestMCPTaskLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := storetest.Open(t)
	blobs, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tools, err := mcp.Load(ctx, api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	accounts := service.NewAccounts(db, nil)
	authz := service.NewAuthorizer(db)
	runs := service.NewRuns(db, authz, nil).WithBlobs(blobs)
	coord := service.NewCoordinator(db, blobs, runs, nil)
	srv := httptest.NewServer(New(discardLogger()).
		WithAccounts(accounts).
		WithRuns(service.NewTasks(db, authz, blobs, nil), runs).
		WithIdempotency(service.NewIdempotency(db, nil)).
		WithMCP(tools).Handler())
	t.Cleanup(srv.Close)

	_, sess, err := accounts.Register(ctx, service.RegisterInput{Username: "agent-owner", Password: "correct horse"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := accounts.Authenticate(ctx, sess.Token)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := accounts.CreateToken(ctx, p, service.CreateTokenInput{Name: "agent", Scopes: []string{"read", "write", "execute"}})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "m2", Version: "0"}, nil).Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{token: token}},
		MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	call := func(name string, args map[string]any) (map[string]any, bool) {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		raw, _ := json.Marshal(res.StructuredContent)
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil || out == nil {
			t.Fatalf("%s: structured content %s: %v", name, raw, err)
		}
		return out, res.IsError
	}
	ok := func(name string, args map[string]any) map[string]any {
		t.Helper()
		out, isErr := call(name, args)
		if isErr {
			t.Fatalf("%s failed: %v", name, out)
		}
		return out
	}
	// The coordinator plays the runner: claim, then end with exit code.
	play := func(runID string, exit int, result string) {
		t.Helper()
		id := uuid.MustParse(runID)
		if err := coord.Register(ctx, runproto.Registration{ID: "r1", Slots: 4}); err != nil {
			t.Fatal(err)
		}
		assignments, err := coord.Claim(ctx, "r1", 4)
		if err != nil {
			t.Fatal(err)
		}
		if err := coord.Started(ctx, "r1", id, "", time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := coord.AppendLogs(ctx, "r1", id, []runproto.LogLine{
			{Seq: 1, TS: time.Now(), Stream: runproto.StreamStderr, Stage: "fetch", Text: "KeyError: 'token'"},
		}, runproto.Dropped{}); err != nil {
			t.Fatal(err)
		}
		if err := coord.AppendEvents(ctx, "r1", id, []runproto.Event{
			{Seq: 1, Kind: runproto.EventStageStart, Stage: "fetch"},
			{Seq: 2, Kind: runproto.EventResult, Result: &runproto.Result{Status: result, Summary: "s"}},
		}); err != nil {
			t.Fatal(err)
		}
		status := runproto.StatusCompleted
		if exit != 0 {
			status = runproto.StatusFailed
		}
		for _, assignment := range assignments {
			if assignment.RunID == id && assignment.HasState && status == runproto.StatusCompleted {
				snapshot, err := coord.GetState(ctx, "r1", id)
				if err != nil {
					t.Fatal(err)
				}
				if err := coord.PutState(ctx, "r1", id, snapshot); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := coord.Finish(ctx, "r1", id, runproto.Finish{Status: status, ExitCode: &exit, ErrorStage: "fetch"}); err != nil {
			t.Fatal(err)
		}
	}

	project := ok("discover", nil)["principal"].(map[string]any)["personal_project_id"].(string)
	manifest := map[string]any{
		"title":      "工作日线索摘要",
		"runtime":    map[string]any{"language": "python", "version": "3.12"},
		"entrypoint": []string{"python", "main.py"},
		"stages":     []map[string]any{{"id": "fetch", "name": "获取"}},
	}
	apply := func(content string, extra map[string]any) (map[string]any, bool) {
		args := map[string]any{"project_id": project, "task_name": "digest", "manifest": manifest,
			"files": []map[string]any{{"path": "main.py", "content": content}}}
		for k, v := range extra {
			args[k] = v
		}
		return call("task_apply", args)
	}

	// Preview, then submit.
	preview, isErr := apply("print(cfg['token'])\n", map[string]any{"dry_run": true})
	if isErr || preview["dry_run"] != true || preview["task"] != nil {
		t.Fatalf("preview = %v", preview)
	}
	created, _ := apply("print(cfg['token'])\n", nil)
	task := created["task"].(map[string]any)
	if task["title"] != manifest["title"] {
		t.Fatalf("submitted title = %v, want %v", task["title"], manifest["title"])
	}
	taskID := task["id"].(string)

	// The test fails; the agent reads the error, stage and log.
	run := ok("run_start", map[string]any{"task_id": taskID, "test": true, "idempotency_key": "first-test"})
	if again := ok("run_start", map[string]any{"task_id": taskID, "test": true, "idempotency_key": "first-test"}); again["id"] != run["id"] {
		t.Fatalf("a retried run_start started %v, then %v", run["id"], again["id"])
	}
	play(run["id"].(string), 1, "failed")
	failed := ok("run_get", map[string]any{"run_id": run["id"], "wait": 5})
	if failed["status"] != "failed" || failed["error"].(map[string]any)["stage"] != "fetch" {
		t.Fatalf("failed run = %v", failed)
	}
	logs := ok("run_logs", map[string]any{"run_id": run["id"], "tail": 10})
	if !strings.Contains(logs["lines"].([]any)[0].(map[string]any)["text"].(string), "KeyError") {
		t.Fatalf("logs = %v", logs)
	}
	if out, isErr := call("task_set_active", map[string]any{"task_id": taskID, "active": true}); !isErr || out["code"] != "version_not_tested" {
		t.Fatalf("activation of an untested version = %v", out)
	}

	// Fix it: without if_match the server asks for it; a stale session
	// conflicts; with the version read it succeeds.
	if out, isErr := apply("print('fixed')\n", nil); !isErr || out["code"] != "precondition_required" {
		t.Fatalf("apply without if_match = %v", out)
	}
	fixed, isErr := apply("print('fixed')\n", map[string]any{"if_match": "1"})
	if isErr || fixed["version"] != float64(2) {
		t.Fatalf("fix = %v", fixed)
	}
	if out, isErr := apply("print('other session')\n", map[string]any{"if_match": "1"}); !isErr || out["code"] != "version_conflict" || out["current_version"] != float64(2) {
		t.Fatalf("stale session = %v", out)
	}
	diff := ok("api_call", map[string]any{"operation_id": "diffTaskVersion", "arguments": map[string]any{"task_id": taskID, "seq": 2}})
	if files, _ := diff["files"].([]any); len(files) != 1 {
		t.Fatalf("diff = %v", diff)
	}

	// Rerun the failed test on the fix; the new run links to the old one
	// and the old run keeps its version.
	rerun := ok("run_rerun", map[string]any{"run_id": run["id"]})
	if rerun["rerun_of"] != run["id"] || rerun["version"] != float64(2) || rerun["test"] != true {
		t.Fatalf("rerun = %v", rerun)
	}
	play(rerun["id"].(string), 0, "ok")
	if old := ok("run_get", map[string]any{"run_id": run["id"]}); old["version"] != float64(1) {
		t.Fatalf("original run = %v", old)
	}
	report := ok("task_validate", map[string]any{"task_id": taskID, "seq": 2})
	if report["valid"] != true || report["activatable"] != true {
		t.Fatalf("report = %v", report)
	}
	active := ok("task_set_active", map[string]any{"task_id": taskID, "active": true})
	if active["state"] != "active" || active["active_version"] != float64(2) || !strings.HasSuffix(active["links"].(map[string]any)["console"].(string), "/tasks/"+taskID) {
		t.Fatalf("activated = %v", active)
	}
	found := ok("api_call", map[string]any{"operation_id": "listTasks", "arguments": map[string]any{"name": "digest"}})
	if items, _ := found["items"].([]any); len(items) != 1 || items[0].(map[string]any)["id"] != taskID {
		t.Fatalf("listTasks = %v", found)
	}
}
