package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/niltavi/lerryn/api"
)

// The real contract must always load: a bad x-mcp annotation in api/ would
// otherwise only show up when the server starts.
func TestLoadContract(t *testing.T) {
	reg, err := Load(context.Background(), api.Spec)
	if err != nil {
		t.Fatalf("Load(api.Spec): %v", err)
	}
	tool, ok := reg.Tool("discover")
	if !ok {
		t.Fatal("discover is not registered")
	}
	if tool.Title != "发现平台能力" || tool.Method != "GET" || tool.Path != "/api/v1/discover" {
		t.Errorf("tool = %+v", tool)
	}
	if !tool.Annotations.ReadOnlyHint {
		t.Error("discover must be read-only")
	}

	// The main-flow tools of tech design 10.2: an agent builds, tests,
	// schedules, activates and repairs a task with these alone.
	for _, want := range []struct {
		name, operationID, method, path string
	}{
		{"task_get", "getTask", "GET", "/api/v1/tasks/{taskId}"},
		{"task_list", "listTasks", "GET", "/api/v1/tasks"},
		{"task_set_tags", "setTaskTags", "PUT", "/api/v1/tasks/{taskId}/tags"},
		{"task_apply", "applyTask", "PUT", "/api/v1/projects/{projectId}/tasks/{taskName}"},
		{"task_validate", "validateTaskVersion", "POST", "/api/v1/tasks/{taskId}/versions/{seq}/validate"},
		{"trigger_set", "setTriggers", "PUT", "/api/v1/tasks/{taskId}/triggers"},
		{"task_set_active", "setTaskActivation", "POST", "/api/v1/tasks/{taskId}/activation"},
		{"run_start", "startRun", "POST", "/api/v1/tasks/{taskId}/runs"},
		{"run_get", "getRun", "GET", "/api/v1/runs/{runId}"},
		{"run_logs", "readRunLogs", "GET", "/api/v1/runs/{runId}/logs"},
		{"run_cancel", "cancelRun", "POST", "/api/v1/runs/{runId}/cancel"},
		{"run_rerun", "rerunRun", "POST", "/api/v1/runs/{runId}/rerun"},
		{"credential_request", "createCredentialRequest", "POST", "/api/v1/credential-requests"},
	} {
		tool, ok := reg.Tool(want.name)
		if !ok {
			t.Errorf("%s is not registered", want.name)
			continue
		}
		if tool.OperationID != want.operationID || tool.Method != want.method || tool.Path != want.path {
			t.Errorf("%s = %s %s %s, want %s %s %s", want.name, tool.OperationID, tool.Method, tool.Path, want.operationID, want.method, want.path)
		}
	}
	// Everything else stays behind api_describe and api_call, so a new
	// client loads a short tool list (tech design 10.2).
	if n := len(reg.Tools()); n != 14 {
		names := make([]string, 0, n)
		for _, tool := range reg.Tools() {
			names = append(names, tool.Name)
		}
		t.Errorf("%d tools are exposed, want the 14 of tech design 10.2: %v", n, names)
	}
	// These tools replace whole sets, so they must be marked destructive.
	for _, name := range []string{"trigger_set", "task_set_tags"} {
		if tool, ok := reg.Tool(name); ok && (tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint) {
			t.Errorf("%s must be marked destructive", name)
		}
	}
	// The optimistic-concurrency and idempotency headers reach agents as
	// plain arguments.
	for tool, arg := range map[string]string{"task_apply": "if_match", "trigger_set": "if_match", "run_start": "idempotency_key"} {
		tl, ok := reg.Tool(tool)
		if !ok {
			continue
		}
		props, _ := tl.InputSchema["properties"].(map[string]any)
		if _, ok := props[arg]; !ok {
			t.Errorf("%s has no %s argument", tool, arg)
		}
	}
}

// api_describe answers for every operation of the real contract, and each
// answer fits in one tool result.
func TestDescribeContract(t *testing.T) {
	reg, err := Load(context.Background(), api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Operations()) < len(reg.Tools()) {
		t.Fatalf("%d operations for %d tools", len(reg.Operations()), len(reg.Tools()))
	}
	for _, o := range reg.Operations() {
		data, err := json.Marshal(o.Detail())
		if err != nil {
			t.Fatalf("%s: %v", o.OperationID, err)
		}
		if len(data) > DefaultMaxResultBytes {
			t.Errorf("api_describe %s is %d bytes, over the %d byte result cap", o.OperationID, len(data), DefaultMaxResultBytes)
		}
	}
	if _, ok := reg.Operation("getTask"); !ok {
		t.Error("getTask, reached only through api_call, is not indexed")
	}
}

// Operations that would let a leaked token mint credentials or admins are
// session-only, and api_describe says api_call cannot reach them.
func TestSessionOnlyContract(t *testing.T) {
	reg, err := Load(context.Background(), api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"createToken", "createRunnerToken", "adminCreateUser", "adminUpdateUser", "adminResetPassword"} {
		o, ok := reg.Operation(id)
		if !ok {
			t.Fatalf("%s is not indexed", id)
		}
		if d := o.Detail(); !d.SessionOnly || d.Callable || d.NotCallable == "" {
			t.Errorf("%s: session_only = %v, callable = %v, reason = %q", id, d.SessionOnly, d.Callable, d.NotCallable)
		}
	}
}
