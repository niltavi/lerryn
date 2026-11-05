package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

type triggerClient struct {
	*taskClient
	db    *store.DB
	hooks http.Handler
}

func newTriggerClient(t *testing.T) *triggerClient {
	t.Helper()
	db := storetest.Open(t)
	blobs, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authz := service.NewAuthorizer(db)
	runs := service.NewRuns(db, authz, nil)
	// Activation without a test run: these tests are about triggers.
	tasks := service.NewTasks(db, authz, blobs, nil).
		WithTestBeforeActivate(func(context.Context) (bool, error) { return false, nil })
	opts := Options{
		PublicURL: testOrigin,
		Version:   "dev",
		Accounts:  service.NewAccounts(db, nil),
		Tasks:     tasks,
		Runs:      runs,
		Triggers:  service.NewTriggers(db, authz, blobs, nil, service.TriggerOptions{}),
	}
	return &triggerClient{
		taskClient: &taskClient{apiClient: &apiClient{t: t, h: New(opts)}, coord: service.NewCoordinator(db, blobs, runs, nil)},
		db:         db,
		hooks:      NewHooks(opts),
	}
}

// activate makes the task's latest version active through the API.
func (c *triggerClient) activate(token string, taskID uuid.UUID) {
	c.t.Helper()
	c.want(c.as(token, http.MethodPost, "/api/v1/tasks/"+taskID.String()+"/activation", map[string]any{"active": true}), http.StatusOK, "")
}

func (c *triggerClient) hook(path, token, key, body string) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	c.hooks.ServeHTTP(rec, req)
	return rec
}

func TestTriggerOperations(t *testing.T) {
	c := newTriggerClient(t)
	agent, project := c.agent("alice")
	task := decode[ApplyTaskResult](t, c.as(agent, http.MethodPut, "/api/v1/projects/"+project.String()+"/tasks/digest", digestRequest())).Task
	path := "/api/v1/tasks/" + task.ID.String() + "/triggers"
	body := map[string]any{"triggers": []any{
		map[string]any{"name": "weekday-morning", "type": "cron", "schedule": "0 8 * * 1-5", "timezone": "Asia/Shanghai"},
		map[string]any{"name": "crm-push", "type": "webhook", "params": map[string]any{"source": "push"}},
	}}

	rec := c.as(agent, http.MethodPut, path+"?dry_run=true", body)
	c.want(rec, http.StatusOK, "")
	preview := decode[TriggerSet](t, rec)
	cron := preview.Triggers[0].Cron
	if !preview.DryRun || preview.Revision != 0 || cron == nil || cron.ScheduleText != "每个工作日 08:00（Asia/Shanghai）" || len(cron.NextFires) != 5 {
		t.Fatalf("preview = %+v", preview)
	}

	rec = c.as(agent, http.MethodPut, path, body)
	c.want(rec, http.StatusOK, "")
	set := decode[TriggerSet](t, rec)
	hook := set.Triggers[1]
	if set.Revision != 1 || set.TaskActive || hook.Webhook == nil || hook.Webhook.Token == nil ||
		hook.Webhook.URL != testOrigin+"/hooks/"+hook.ID.String() || hook.NextFireAt != nil || set.Triggers[0].NextFireAt != nil {
		t.Fatalf("set = %+v", set)
	}
	if hook.Params == nil || (*hook.Params)["source"] != "push" || set.Triggers[0].Params != nil {
		t.Fatalf("params = %v / %v", hook.Params, set.Triggers[0].Params)
	}
	token := *hook.Webhook.Token

	rec = c.as(agent, http.MethodGet, path, nil)
	c.want(rec, http.StatusOK, "")
	if listed := decode[TriggerSet](t, rec); listed.Triggers[0].Name != "crm-push" || listed.Triggers[0].Webhook.Token != nil {
		t.Fatal("list returned a token")
	}
	c.want(c.as(agent, http.MethodPut, path, body), http.StatusPreconditionRequired, ErrorCodePreconditionRequired)
	rec = c.call(http.MethodPut, path, body, "Authorization", agent, "If-Match", `"0"`)
	c.want(rec, http.StatusPreconditionFailed, ErrorCodeVersionConflict)
	if p := decode[Problem](t, rec); p.CurrentVersion == nil || *p.CurrentVersion != 1 {
		t.Fatalf("problem = %+v", p)
	}
	c.want(c.call(http.MethodPut, path, body, "Authorization", agent, "If-Match", "1"), http.StatusOK, "")

	bad := map[string]any{"triggers": []any{map[string]any{"name": "a", "type": "cron", "schedule": "0 8 * * *"}}}
	c.want(c.call(http.MethodPut, path, bad, "Authorization", agent, "If-Match", "2"), http.StatusUnprocessableEntity, ErrorCodeValidationFailed)

	// Webhook calls: inactive task, then with and without the token.
	hookPath := "/hooks/" + hook.ID.String()
	c.want(c.hook(hookPath, token, "", "x"), http.StatusNotFound, ErrorCodeNotFound)
	c.activate(agent, task.ID)
	c.want(c.hook(hookPath, "", "", "x"), http.StatusUnauthorized, ErrorCodeUnauthenticated)
	c.want(c.hook(hookPath, strings.TrimPrefix(agent, "Bearer "), "", "x"), http.StatusUnauthorized, ErrorCodeUnauthenticated)
	rec = c.hook(hookPath, token, "d-1", "hello")
	c.want(rec, http.StatusAccepted, "")
	accepted := decode[WebhookAccepted](t, rec)
	if accepted.Status != RunStatusQueued || accepted.Duplicate {
		t.Fatalf("accepted = %+v", accepted)
	}
	rec = c.hook(hookPath, token, "d-1", "hello")
	if again := decode[WebhookAccepted](t, rec); rec.Code != http.StatusAccepted || !again.Duplicate || again.RunID != accepted.RunID {
		t.Fatalf("repeat = %d %s", rec.Code, rec.Body)
	}
	c.want(c.hook(hookPath, token, "d-1", "other"), http.StatusConflict, ErrorCodeIdempotencyConflict)
	c.want(c.hook(hookPath, token, "", strings.Repeat("x", 1<<20+1)), http.StatusRequestEntityTooLarge, ErrorCodeValidationFailed)

	run := decode[Run](t, c.as(agent, http.MethodGet, "/api/v1/runs/"+accepted.RunID.String(), nil))
	if run.Trigger.Kind != TriggerKindWebhook || run.Trigger.TriggerID == nil || *run.Trigger.TriggerID != hook.ID || run.Params["source"] != "push" {
		t.Fatalf("run = %+v", run)
	}

	// Rotation invalidates the old token.
	rec = c.as(agent, http.MethodPost, "/api/v1/triggers/"+hook.ID.String()+"/rotate-token", nil)
	c.want(rec, http.StatusOK, "")
	rotated := decode[Trigger](t, rec)
	if rotated.Webhook.Token == nil || *rotated.Webhook.Token == token {
		t.Fatalf("rotated = %+v", rotated)
	}
	c.want(c.hook(hookPath, token, "", "x"), http.StatusUnauthorized, ErrorCodeUnauthenticated)
	c.want(c.hook(hookPath, *rotated.Webhook.Token, "", "x"), http.StatusAccepted, "")

	// The API handler does not serve webhooks, and the hooks handler
	// serves nothing else.
	c.want(c.call(http.MethodPost, hookPath, nil, "Authorization", "Bearer "+*rotated.Webhook.Token), http.StatusUnauthorized, ErrorCodeUnauthenticated)
	rec = httptest.NewRecorder()
	c.hooks.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/discover", nil))
	c.want(rec, http.StatusNotFound, ErrorCodeNotFound)
}

func TestStartRunQueueFull(t *testing.T) {
	db := storetest.Open(t)
	blobs, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authz := service.NewAuthorizer(db)
	c := &taskClient{apiClient: &apiClient{t: t, h: New(Options{
		PublicURL: testOrigin, Version: "dev",
		Accounts: service.NewAccounts(db, nil),
		Tasks:    service.NewTasks(db, authz, blobs, nil),
		Runs:     service.NewRuns(db, authz, nil).WithQueueLimits(service.QueueLimits{MaxQueued: 1}),
	})}}
	agent, project := c.agent("alice")
	req := digestRequest()
	req["manifest"].(map[string]any)["concurrency"] = "allow"
	task := decode[ApplyTaskResult](t, c.as(agent, http.MethodPut, "/api/v1/projects/"+project.String()+"/tasks/digest", req)).Task
	c.want(c.as(agent, http.MethodPost, "/api/v1/tasks/"+task.ID.String()+"/runs", nil), http.StatusAccepted, "")
	c.want(c.as(agent, http.MethodPost, "/api/v1/tasks/"+task.ID.String()+"/runs", nil), http.StatusTooManyRequests, ErrorCodeQueueFull)
}
