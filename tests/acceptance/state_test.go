//go:build integration

package acceptance

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const stateCounterTask = `import os, pathlib, shutil, sys
import sbx

p = sbx.params()
root = pathlib.Path("/sbx/state")
assert root.is_dir(), "state is not mounted"
if p["expected"] == 0:
    assert not any(root.iterdir()), "initial state is not empty"
cursor = root / "nested" / "游标.txt"
before = int(cursor.read_text()) if cursor.exists() else 0
assert before == p["expected"], (before, p["expected"])
if before:
    assert (root / "empty").is_dir()
    assert (root / "nested" / "bytes.bin").read_bytes() == bytes(range(256))
    assert os.access(root / "nested" / "run.sh", os.X_OK)

if p.get("clear"):
    for child in root.iterdir():
        shutil.rmtree(child) if child.is_dir() else child.unlink()
    sbx.result("ok", "cleared task state", {"before": before, "after": 0})
else:
    (root / "nested").mkdir(exist_ok=True)
    (root / "empty").mkdir(exist_ok=True)
    cursor.write_text(str(before + 1))
    (root / "nested" / "bytes.bin").write_bytes(bytes(range(256)))
    executable = root / "nested" / "run.sh"
    executable.write_text("#!/bin/sh\nexit 0\n")
    executable.chmod(0o755)
    sbx.result("failed" if p.get("business_failed") else "ok", "state counter", {"before": before, "after": before + 1})
    if p.get("fail"):
        sys.exit(7)
`

const nodeStateCounter = `import { readFileSync, writeFileSync } from "node:fs";
import * as sbx from "@lerryn/sbx";
const cursor = "/sbx/state/nested/游标.txt";
const before = Number(readFileSync(cursor, "utf8"));
writeFileSync(cursor, String(before + 1));
sbx.result("ok", "node state counter", { before, after: before + 1 });
`

const shellStateCounter = `set -eu
cursor=/sbx/state/nested/游标.txt
before=$(cat "$cursor")
after=$((before + 1))
printf '%s' "$after" > "$cursor"
sbx result --status ok --summary "shell state counter" --data-json "{\"before\":$before,\"after\":$after}"
`

type stateRunResult struct {
	Status string `json:"status"`
	Runner *struct {
		ID string `json:"id"`
	} `json:"runner"`
	Result *struct {
		Status string `json:"status"`
		Data   struct {
			Before int `json:"before"`
			After  int `json:"after"`
		} `json:"data"`
	} `json:"result"`
}

func waitStateRun(t *testing.T, api *server, id string) stateRunResult {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var done stateRunResult
		api.call(http.MethodGet, "/api/v1/runs/"+id+"?wait=10", nil, http.StatusOK, &done)
		switch done.Status {
		case "completed", "failed", "interrupted", "timed_out", "cancelled":
			return done
		}
		if time.Now().After(deadline) {
			t.Fatalf("state run did not finish: %s", done.Status)
		}
	}
}

// State crosses both the HTTP protocol and distinct runner-local disks.
// Labels select the runner; task versions still share the same TaskID.
func TestTaskStateAcrossRunners(t *testing.T) {
	c := newCluster(t)
	c.startServer("state-server-a")
	c.startServer("state-server-b")
	c.startRunner("state-runner-a", 1, "LERRYN_RUNNER_LABELS=state-a")
	c.startRunner("state-runner-b", 1, "LERRYN_RUNNER_LABELS=state-b")
	api := c.api()
	project := api.signUp("state-owner")
	db := c.db()
	var taskID string
	version := 0
	type program struct {
		language, version, file, source string
		entrypoint                      []string
	}
	python := program{"python", "3.12", "main.py", stateCounterTask, []string{"python", "main.py"}}
	apply := func(label string, source program) {
		t.Helper()
		var out struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
		}
		manifest := map[string]any{
			"runtime": map[string]any{"language": source.language, "version": source.version}, "entrypoint": source.entrypoint,
			"workflow": []map[string]any{{"title": "累加运行计数", "description": "读取上次保存的计数，加一并保存。"}},
			"network":  "none", "concurrency": "allow", "runner_labels": []string{label},
			"limits": map[string]any{"timeout": "30s", "memory": "128Mi"},
		}
		var header []string
		if version > 0 {
			header = []string{"If-Match", strconv.Itoa(version)}
		}
		api.call(http.MethodPut, "/api/v1/projects/"+project+"/tasks/state-counter", map[string]any{
			"manifest": manifest, "files": []map[string]any{{"path": source.file, "content": source.source}},
		}, http.StatusOK, &out, header...)
		if taskID != "" && out.Task.ID != taskID {
			t.Fatal("a new version changed the task identity")
		}
		taskID = out.Task.ID
		version++
	}
	check := func(label string, expected, after int, status string, flags map[string]any) {
		t.Helper()
		params := map[string]any{"expected": expected}
		for key, value := range flags {
			params[key] = value
		}
		var started struct {
			ID string `json:"id"`
		}
		api.call(http.MethodPost, "/api/v1/tasks/"+taskID+"/runs", map[string]any{"test": true, "params": params}, http.StatusAccepted, &started)
		done := waitStateRun(t, api, started.ID)
		resultStatus := "ok"
		if flags["business_failed"] == true {
			resultStatus = "failed"
		}
		if done.Status != status || done.Result == nil || done.Result.Status != resultStatus || done.Result.Data.Before != expected || done.Result.Data.After != after {
			t.Fatalf("state run status=%s result=%+v, want %s %d→%d", done.Status, done.Result, status, expected, after)
		}
		id, err := os.ReadFile(filepath.Join(c.runners["state-runner-"+label], "runner-id")) // #nosec G304 -- this test's runner directory
		if err != nil || done.Runner == nil || done.Runner.ID != strings.TrimSpace(string(id)) {
			t.Fatalf("run did not use the selected runner %s: %v", label, err)
		}
		var refs int
		if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM run_state_refs WHERE run_id = $1", started.ID).Scan(&refs); err != nil || refs != 0 {
			t.Fatalf("terminal run retains %d state references: %v", refs, err)
		}
	}

	apply("state-a", python)
	// Test executions and business-failed results still publish on exit 0.
	check("a", 0, 1, "completed", map[string]any{"business_failed": true})
	apply("state-b", python)
	check("b", 1, 2, "completed", nil)
	check("b", 2, 3, "failed", map[string]any{"fail": true})
	apply("state-a", python)
	check("a", 2, 3, "completed", nil)
	check("a", 3, 0, "completed", map[string]any{"clear": true})
	apply("state-b", python)
	check("b", 0, 1, "completed", nil)

	oldID := taskID
	api.call(http.MethodDelete, "/api/v1/tasks/"+oldID, nil, http.StatusOK, nil)
	taskID, version = "", 0
	apply("state-a", python)
	if taskID == oldID {
		t.Fatal("a recreated task reused the archived identity")
	}
	check("a", 0, 1, "completed", nil)
	apply("state-b", program{"node", "22", "main.mjs", nodeStateCounter, []string{"node", "main.mjs"}})
	check("b", 1, 2, "completed", nil)
	apply("state-a", program{"shell", "5", "main.sh", shellStateCounter, []string{"bash", "main.sh"}})
	check("a", 2, 3, "completed", nil)
	apply("state-b", python)
	check("b", 3, 4, "completed", nil)
	var archivedHeads int
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM task_states WHERE task_id = $1", oldID).Scan(&archivedHeads); err != nil || archivedHeads != 1 {
		t.Fatalf("archiving removed task state: heads=%d err=%v", archivedHeads, err)
	}
}

func TestTaskStateSurvivesBackupAndRestore(t *testing.T) {
	old := newServer(t)
	project := old.signUp("state-backup-owner")
	taskID := old.applyTask(project, "state-backup", stateCounterTask, map[string]any{"network": "none"})
	step := func(api *server, expected int) stateRunResult {
		t.Helper()
		var started struct {
			ID string `json:"id"`
		}
		api.call(http.MethodPost, "/api/v1/tasks/"+taskID+"/runs", map[string]any{
			"test": true, "params": map[string]any{"expected": expected},
		}, http.StatusAccepted, &started)
		done := waitStateRun(t, api, started.ID)
		if done.Status != "completed" || done.Result == nil || done.Result.Status != "ok" || done.Result.Data.Before != expected || done.Result.Data.After != expected+1 {
			t.Fatalf("restored state counter: status=%s result=%+v", done.Status, done.Result)
		}
		return done
	}
	first := step(old, 0)
	archive := filepath.Join(t.TempDir(), "state-backup.tar.gz")
	backup := exec.Command(binary, "backup", "--output", archive) // #nosec G204 -- the binary this package built
	backup.Env = old.env
	if out, err := backup.CombinedOutput(); err != nil {
		t.Fatalf("backup state: %v\n%s", err, out)
	}
	old.stop()

	fresh := prepareServer(t)
	key, err := os.ReadFile(filepath.Join(old.dir, "master.key")) // #nosec G304 -- this test's master key
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fresh.dir, "master.key"), key, 0o600); err != nil { // #nosec G703 -- a temp directory this test owns
		t.Fatal(err)
	}
	restore := exec.Command(binary, "restore", "--input", archive) // #nosec G204 -- the binary this package built
	restore.Env = fresh.env
	if out, err := restore.CombinedOutput(); err != nil {
		t.Fatalf("restore state: %v\n%s", err, out)
	}
	fresh.start()
	fresh.token = old.token
	second := step(fresh, 1)
	if first.Runner == nil || second.Runner == nil || first.Runner.ID == second.Runner.ID {
		t.Fatal("restore did not run on a fresh local runner")
	}
}
