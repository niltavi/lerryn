//go:build integration

package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const bundleSourceSecret = "bundle-source-secret-0123" // #nosec G101 -- a test value
const bundleTargetSecret = "bundle-target-secret-4567" // #nosec G101 -- a test value

// A task exported from one deployment is imported into another by a
// different account and runs there: the bundle carries the task and its
// triggers but nothing of the source deployment (IDs, credentials,
// tokens), the importer binds their own credential, and the imported
// version must pass a test run before it can be activated.
func TestTaskExportImportAcrossInstances(t *testing.T) {
	t.Parallel()
	source, target := startServer(t, 2), startServer(t, 2)

	alice := source.signUp("alice")
	tk := alice.applyTask("nightly", tokenTask, map[string]any{"description": "每晚同步价格"})
	alice.fill(alice.requestCredential(tk.ID).URL, map[string]string{"API_TOKEN": bundleSourceSecret})
	if r, _ := alice.waitRun(alice.startRun(tk.ID).ID, true); r.Status != "completed" {
		t.Fatalf("source run = %+v", r)
	}
	var set triggerSet
	alice.api(http.MethodPut, "/api/v1/tasks/"+tk.ID+"/triggers", map[string]any{"triggers": []any{
		map[string]any{"name": "new-year", "type": "cron", "schedule": "0 3 1 1 *", "timezone": "Asia/Shanghai"},
		map[string]any{"name": "push", "type": "webhook"},
	}}, http.StatusOK, &set)
	alice.api(http.MethodPost, "/api/v1/tasks/"+tk.ID+"/activation", map[string]any{"active": true}, http.StatusOK, nil)
	var sourceToken string
	for _, tr := range set.Triggers {
		if tr.Webhook != nil {
			sourceToken = *tr.Webhook.Token
		}
	}

	bundle := alice.api(http.MethodGet, "/api/v1/tasks/"+tk.ID+"/export?history=true", nil, http.StatusOK, nil)
	for _, leak := range []string{bundleSourceSecret, sourceToken, tk.ID, alice.project, source.base} {
		if strings.Contains(bundle, leak) {
			t.Fatalf("the bundle contains %q:\n%s", leak, bundle)
		}
	}

	// Another deployment, another account.
	bob := target.signUp("bob")
	var raw map[string]any
	if err := json.Unmarshal([]byte(bundle), &raw); err != nil {
		t.Fatal(err)
	}
	var imported struct {
		Task struct {
			ID        string `json:"id"`
			ProjectID string `json:"project_id"`
		} `json:"task"`
		Credentials []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"credentials"`
		Triggers []struct {
			ID      string `json:"id"`
			Webhook *struct {
				URL   string  `json:"url"`
				Token *string `json:"token"`
			} `json:"webhook"`
		} `json:"triggers"`
	}
	bob.api(http.MethodPost, "/api/v1/projects/"+bob.project+"/task-imports", map[string]any{"bundle": raw}, http.StatusOK, &imported)
	if imported.Task.ProjectID != bob.project || len(imported.Credentials) != 1 || imported.Credentials[0].Name != "API_TOKEN" ||
		imported.Credentials[0].Status != "missing" || len(imported.Triggers) != 2 {
		t.Fatalf("import = %+v", imported)
	}
	taskID := imported.Task.ID
	var hookURL, hookToken string
	for _, tr := range imported.Triggers {
		if tr.Webhook != nil {
			hookURL, hookToken = tr.Webhook.URL, *tr.Webhook.Token
		}
	}
	if !strings.HasPrefix(hookURL, target.base+"/hooks/") || hookToken == "" || hookToken == sourceToken {
		t.Fatalf("webhook = %s, token %q", hookURL, hookToken)
	}

	// The imported version is untested and its credential unbound.
	problem := bob.api(http.MethodPost, "/api/v1/tasks/"+taskID+"/activation", map[string]any{"active": true}, http.StatusConflict, nil)
	if !strings.Contains(problem, "version_not_tested") {
		t.Fatalf("activation before a test = %s", problem)
	}
	problem = bob.api(http.MethodPost, "/api/v1/tasks/"+taskID+"/runs", map[string]any{"test": true}, http.StatusUnprocessableEntity, nil)
	if !strings.Contains(problem, "credential_missing") {
		t.Fatalf("run before binding = %s", problem)
	}

	// Bob fills his own credential, tests, activates and calls the webhook.
	bob.fill(bob.requestCredential(taskID).URL, map[string]string{"API_TOKEN": bundleTargetSecret})
	r, _ := bob.waitRun(bob.startRun(taskID).ID, true)
	if r.Status != "completed" {
		t.Fatalf("imported test run = %+v", r)
	}
	_, lines := bob.runLogs(r.ID)
	want := fmt.Sprintf("token [REDACTED] %d", len(bundleTargetSecret))
	if !strings.Contains(strings.Join(lines, "\n"), want) {
		t.Fatalf("logs = %v, want %q", lines, want)
	}
	bob.api(http.MethodPost, "/api/v1/tasks/"+taskID+"/activation", map[string]any{"active": true}, http.StatusOK, nil)

	req, _ := http.NewRequest(http.MethodPost, hookURL, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+hookToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var accepted struct {
		RunID string `json:"run_id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&accepted)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook = %d: %v", resp.StatusCode, err)
	}
	if r, _ := bob.waitRun(accepted.RunID, true); r.Status != "completed" {
		t.Fatalf("webhook run = %+v", r)
	}

	// Nothing on the target carries the source's credential.
	if strings.Contains(target.output(), bundleSourceSecret) {
		t.Fatal("the target's output contains the source credential")
	}
}
