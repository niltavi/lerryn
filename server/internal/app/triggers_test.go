package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/niltavi/lerryn/api"
	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/mcp"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

// trigger_set through MCP previews, saves and guards with if_match, and
// the webhook it creates is served at /hooks/ on the root handler.
func TestTriggerSetToolAndWebhookRoute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tools, err := mcp.Load(ctx, api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	db := storetest.Open(t)
	blobs, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	accounts := service.NewAccounts(db, nil)
	authz := service.NewAuthorizer(db)
	// Activation without a test run: this test is about triggers.
	tasks := service.NewTasks(db, authz, blobs, nil).
		WithTestBeforeActivate(func(context.Context) (bool, error) { return false, nil })
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
	applied, err := tasks.Apply(ctx, p, service.ApplyTaskInput{
		ProjectID: p.PersonalProjectID, Name: "digest",
		Manifest: service.Manifest{Runtime: service.RuntimeSpec{Language: "python", Version: "3.12"}, Entrypoint: []string{"python", "main.py"},
			Workflow: []service.WorkflowStep{{Title: "Execute script", Description: "Run the Python script and capture its output."}}},
		Files: []service.FileInput{{Path: "main.py", Content: []byte("print(1)\n")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	task := applied.Task
	srv := httptest.NewServer(New(discardLogger()).WithAccounts(accounts).WithMCP(tools).
		WithRuns(tasks, service.NewRuns(db, authz, nil)).
		WithTriggers(service.NewTriggers(db, authz, blobs, nil, service.TriggerOptions{})).Handler())
	t.Cleanup(srv.Close)

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "lerryn-test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint: srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token: token}}, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	call := func(args map[string]any) (map[string]any, bool) {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "trigger_set", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(res.StructuredContent)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return out, res.IsError
	}
	triggers := []any{
		map[string]any{"name": "weekday-morning", "type": "cron", "schedule": "0 8 * * 1-5", "timezone": "Asia/Shanghai"},
		map[string]any{"name": "crm-push", "type": "webhook"},
	}
	taskID := task.Task.ID.String()
	preview, isErr := call(map[string]any{"task_id": taskID, "dry_run": true, "triggers": triggers})
	if isErr || preview["dry_run"] != true {
		t.Fatalf("preview = %v", preview)
	}
	set, isErr := call(map[string]any{"task_id": taskID, "triggers": triggers})
	if isErr || set["revision"] != float64(1) {
		t.Fatalf("set = %v", set)
	}
	if _, isErr := call(map[string]any{"task_id": taskID, "triggers": triggers}); !isErr {
		t.Fatal("a change without if_match must fail")
	}
	if again, isErr := call(map[string]any{"task_id": taskID, "if_match": "1", "triggers": triggers}); isErr || again["revision"] != float64(2) {
		t.Fatalf("again = %v", again)
	}

	hook := set["triggers"].([]any)[1].(map[string]any)["webhook"].(map[string]any)
	hookToken := hook["token"].(string)
	hookURL := strings.Replace(hook["url"].(string), "http://localhost:33333", srv.URL, 1)
	if _, err := tasks.SetActivation(ctx, p, task.Task.ID, true, nil); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, hookURL, strings.NewReader(`{"a":1}`))
	req.Header.Set("Authorization", "Bearer "+hookToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || resp.Header.Get("X-Request-Id") == "" {
		t.Fatalf("webhook status = %d", resp.StatusCode)
	}
}
