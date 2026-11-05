package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/notify"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

type notifyClient struct {
	*taskClient
	notifications *service.Notifications
}

// newNotifyClient serves the API with a notification service whose guard
// allows loopback, so that it reaches receivers started by the test.
func newNotifyClient(t *testing.T) *notifyClient {
	t.Helper()
	db := storetest.Open(t)
	blobs, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authz := service.NewAuthorizer(db)
	runs := service.NewRuns(db, authz, nil)
	guard, err := notify.NewGuard([]string{"127.0.0.0/8", "::1/128"})
	if err != nil {
		t.Fatal(err)
	}
	n := service.NewNotifications(db, authz, service.NotificationsOptions{PublicURL: testOrigin, Guard: guard})
	h := New(Options{
		PublicURL:     testOrigin,
		Version:       "dev",
		Accounts:      service.NewAccounts(db, nil),
		Tasks:         service.NewTasks(db, authz, blobs, nil),
		Runs:          runs,
		Notifications: n,
	})
	return &notifyClient{
		taskClient:    &taskClient{apiClient: &apiClient{t: t, h: h}, coord: service.NewCoordinator(db, blobs, runs, nil)},
		notifications: n,
	}
}

// hookReceiver records the requests a channel sends.
type hookReceiver struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []hookRequest
}

type hookRequest struct {
	header http.Header
	body   []byte
}

func newHookReceiver(t *testing.T) *hookReceiver {
	t.Helper()
	r := &hookReceiver{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.reqs = append(r.reqs, hookRequest{header: req.Header.Clone(), body: body})
		r.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *hookReceiver) requests() []hookRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hookRequest(nil), r.reqs...)
}

// validateEvent checks a webhook body against NotificationEvent in the
// contract's components.
func validateEvent(t *testing.T, body []byte) {
	t.Helper()
	doc, _ := loadSpec(t)
	ref := doc.Components.Schemas["NotificationEvent"]
	if ref == nil || ref.Value == nil {
		t.Fatal("the spec has no NotificationEvent schema")
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	// The contract is OpenAPI 3.1, so const and type arrays need JSON
	// Schema 2020-12 semantics, as the response validator uses.
	if err := ref.Value.VisitJSON(v, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatalf("payload does not match NotificationEvent: %v\n%s", err, body)
	}
}

// End to end through the API: a user adds a generic webhook channel,
// tests it, a subscribed task fails, and the receiver gets a signed
// NotificationEvent that matches the contract (A16).
func TestNotificationChannelsEndToEnd(t *testing.T) {
	c := newNotifyClient(t)
	token, project := c.agent("alice")
	rcv := newHookReceiver(t)

	rec := c.as(token, http.MethodPost, "/api/v1/notification-channels", map[string]any{
		"name": "值班", "kind": "webhook", "url": rcv.URL + "/hooks/lerryn?token=s3cr3t-bot-token",
		"subscriptions": []any{map[string]any{"project_id": project, "events": []string{"run_failed", "run_timed_out"}}},
	})
	c.want(rec, http.StatusCreated, "")
	created := decode[CreatedNotificationChannel](t, rec)
	if created.SigningSecret == nil || !strings.HasPrefix(*created.SigningSecret, notify.SigningSecretPrefix) {
		t.Fatalf("signing secret = %v", created.SigningSecret)
	}
	ch := created.Channel
	if strings.Contains(rec.Body.String(), "s3cr3t") || !strings.HasSuffix(ch.URLPreview, "/…/oken") {
		t.Fatalf("create response exposes the target: %s", rec.Body)
	}
	if len(ch.Subscriptions) != 1 || ch.Subscriptions[0].Project.ID != project || ch.Subscriptions[0].Task != nil || ch.LastDelivery != nil {
		t.Fatalf("channel = %+v", ch)
	}
	secret := *created.SigningSecret

	// Test send.
	rec = c.as(token, http.MethodPost, "/api/v1/notification-channels/"+ch.ID.String()+"/test", nil)
	c.want(rec, http.StatusOK, "")
	test := decode[NotificationDelivery](t, rec)
	if test.Status != NotificationDeliveryStatusDelivered || test.EventType != "test" || test.HTTPStatus == nil || *test.HTTPStatus != 202 {
		t.Fatalf("test delivery = %+v", test)
	}

	// A subscribed task fails.
	rec = c.as(token, http.MethodPut, "/api/v1/projects/"+project.String()+"/tasks/digest", digestRequest())
	c.want(rec, http.StatusOK, "")
	task := decode[ApplyTaskResult](t, rec).Task
	rec = c.as(token, http.MethodPost, "/api/v1/tasks/"+task.ID.String()+"/runs", map[string]any{})
	c.want(rec, http.StatusAccepted, "")
	run := decode[Run](t, rec)
	ctx := context.Background()
	if err := c.coord.Register(ctx, runproto.Registration{ID: "r1", Slots: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.coord.Claim(ctx, "r1", 1); err != nil {
		t.Fatal(err)
	}
	if err := c.coord.Started(ctx, "r1", run.ID, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := c.coord.Finish(ctx, "r1", run.ID, runproto.Finish{Status: runproto.StatusTimedOut}); err != nil {
		t.Fatal(err)
	}
	if err := c.notifications.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}

	reqs := rcv.requests()
	if len(reqs) != 2 {
		t.Fatalf("receiver got %d requests, want the test and the timeout", len(reqs))
	}
	for i, r := range reqs {
		validateEvent(t, r.body)
		if err := notify.Verify(secret, r.header.Get("X-Lerryn-Signature"), r.body, time.Now()); err != nil {
			t.Errorf("request %d: %v", i, err)
		}
	}
	var ev NotificationEvent
	if err := json.Unmarshal(reqs[1].body, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != NotificationEventKindRunTimedOut || ev.Run == nil || ev.Run.ID != run.ID || ev.Run.Status != RunStatusTimedOut ||
		ev.Run.ErrorCode == nil || *ev.Run.ErrorCode != ErrorCodeTimeout || ev.Task == nil || ev.Task.Name != "digest" ||
		ev.Link != testOrigin+"/runs/"+run.ID.String() {
		t.Fatalf("event = %+v", ev)
	}
	if reqs[1].header.Get("X-Lerryn-Delivery") != ev.ID.String() || reqs[1].header.Get("X-Lerryn-Event") != "run_timed_out" {
		t.Errorf("headers = %v", reqs[1].header)
	}

	// Deliveries, newest first, and the channel's last delivery.
	rec = c.as(token, http.MethodGet, "/api/v1/notification-channels/"+ch.ID.String()+"/deliveries?limit=1", nil)
	c.want(rec, http.StatusOK, "")
	page := decode[NotificationDeliveryPage](t, rec)
	if len(page.Items) != 1 || page.Items[0].ID != ev.ID || page.Items[0].Task == nil || page.Items[0].RunID == nil || page.NextCursor == nil {
		t.Fatalf("deliveries page = %+v", page)
	}
	rec = c.as(token, http.MethodGet, "/api/v1/notification-channels/"+ch.ID.String()+"/deliveries?cursor="+*page.NextCursor, nil)
	c.want(rec, http.StatusOK, "")
	if rest := decode[NotificationDeliveryPage](t, rec); len(rest.Items) != 1 || rest.Items[0].ID != test.ID || rest.NextCursor != nil {
		t.Fatalf("second page = %+v", rest)
	}
	rec = c.as(token, http.MethodGet, "/api/v1/notification-channels", nil)
	c.want(rec, http.StatusOK, "")
	list := decode[NotificationChannelList](t, rec)
	if len(list.Items) != 1 || list.Items[0].LastDelivery == nil || list.Items[0].LastDelivery.ID != ev.ID {
		t.Fatalf("list = %+v", list)
	}

	// Update, then delete.
	rec = c.as(token, http.MethodPatch, "/api/v1/notification-channels/"+ch.ID.String(), map[string]any{
		"enabled": false, "subscriptions": []any{map[string]any{"project_id": project, "task_id": task.ID, "events": []string{"credential_expiring"}}},
	})
	c.want(rec, http.StatusOK, "")
	up := decode[NotificationChannel](t, rec)
	if up.Enabled || len(up.Subscriptions) != 1 || up.Subscriptions[0].Task == nil || up.Subscriptions[0].Task.ID != task.ID {
		t.Fatalf("updated = %+v", up)
	}
	rec = c.as(token, http.MethodDelete, "/api/v1/notification-channels/"+ch.ID.String(), nil)
	c.want(rec, http.StatusNoContent, "")
	rec = c.as(token, http.MethodDelete, "/api/v1/notification-channels/"+ch.ID.String(), nil)
	c.want(rec, http.StatusNotFound, ErrorCodeNotFound)
}

func TestNotificationChannelErrors(t *testing.T) {
	c := newNotifyClient(t)
	alice, project := c.agent("alice")
	bob, bobProject := c.agent("bob")
	reader, _ := c.agent("carol", "read")

	rec := c.as(alice, http.MethodPost, "/api/v1/notification-channels", map[string]any{
		"name": "x", "kind": "slack", "url": "https://hooks.slack.com/services/T/B/x",
	})
	c.want(rec, http.StatusCreated, "")
	ch := decode[CreatedNotificationChannel](t, rec)
	if ch.SigningSecret != nil {
		t.Errorf("slack channel got a signing secret")
	}
	id := ch.Channel.ID.String()

	tests := []struct {
		name, token, method, path string
		body                      any
		status                    int
		code                      ErrorCode
	}{
		{"other user's channel", bob, http.MethodPatch, "/api/v1/notification-channels/" + id, map[string]any{"enabled": false}, 404, ErrorCodeNotFound},
		{"other user's test", bob, http.MethodPost, "/api/v1/notification-channels/" + id + "/test", nil, 404, ErrorCodeNotFound},
		{"other user's deliveries", bob, http.MethodGet, "/api/v1/notification-channels/" + id + "/deliveries", nil, 404, ErrorCodeNotFound},
		{"unknown channel", alice, http.MethodDelete, "/api/v1/notification-channels/" + uuid.NewString(), nil, 404, ErrorCodeNotFound},
		{"subscription to another user's project", alice, http.MethodPost, "/api/v1/notification-channels", map[string]any{
			"name": "x", "kind": "slack", "url": "https://hooks.slack.com/x",
			"subscriptions": []any{map[string]any{"project_id": bobProject, "events": []string{"run_failed"}}},
		}, 404, ErrorCodeNotFound},
		{"unknown kind", alice, http.MethodPost, "/api/v1/notification-channels", map[string]any{
			"name": "x", "kind": "email", "url": "https://x.example.com",
		}, 400, ErrorCodeValidationFailed},
		{"unknown event", alice, http.MethodPatch, "/api/v1/notification-channels/" + id, map[string]any{
			"subscriptions": []any{map[string]any{"project_id": project, "events": []string{"run_started"}}},
		}, 400, ErrorCodeValidationFailed},
		{"empty update", alice, http.MethodPatch, "/api/v1/notification-channels/" + id, map[string]any{}, 400, ErrorCodeValidationFailed},
		{"read-only token", reader, http.MethodPost, "/api/v1/notification-channels", map[string]any{
			"name": "x", "kind": "slack", "url": "https://hooks.slack.com/x",
		}, 403, ErrorCodeInsufficientScope},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c.want(c.as(tt.token, tt.method, tt.path, tt.body), tt.status, tt.code)
		})
	}
}

func TestNotificationChannelRefusesInternalTargets(t *testing.T) {
	c := newTaskClient(t)
	db := storetest.Open(t)
	// The default guard: no internal network allowed.
	n := service.NewNotifications(db, service.NewAuthorizer(db), service.NotificationsOptions{PublicURL: testOrigin})
	c.h = New(Options{PublicURL: testOrigin, Accounts: service.NewAccounts(db, nil), Notifications: n})
	token, _ := c.agent("alice")
	for _, url := range []string{"http://127.0.0.1:8080/hook", "http://localhost/hook", "http://169.254.169.254/latest", "http://[::1]/x", "http://10.0.0.8/x"} {
		rec := c.as(token, http.MethodPost, "/api/v1/notification-channels", map[string]any{"name": "x", "kind": "webhook", "url": url})
		c.want(rec, http.StatusUnprocessableEntity, ErrorCodeValidationFailed)
		if !strings.Contains(rec.Body.String(), "#/url") {
			t.Errorf("%s: error does not point at the url: %s", url, rec.Body)
		}
	}
}

// Every event kind the dispatcher produces matches the contract.
func TestNotificationEventPayloadsMatchContract(t *testing.T) {
	code, reason := "script_error", "进程以退出码 1 结束"
	project := notify.ProjectRef{ID: uuid.New(), Name: "运营"}
	task := &notify.TaskRef{ID: uuid.New(), Name: "digest", ProjectID: project.ID}
	run := &notify.Run{ID: uuid.New(), Status: "failed", ErrorCode: &code, Reason: &reason, Version: 2, Attempt: 1, Link: testOrigin + "/runs/x"}
	events := []notify.Event{
		{Type: notify.EventRunFailed, Task: task, Run: run, MergedCount: 3},
		{Type: notify.EventRunInterrupted, Task: task, Run: &notify.Run{ID: uuid.New(), Status: "interrupted", Version: 1, Attempt: 1, Link: testOrigin + "/runs/y"}, MergedCount: 1},
		{Type: notify.EventCredentialExpiring, Task: task, Credential: &notify.Credential{ID: uuid.New(), Name: "LLM_API_KEY", ExpiresAt: time.Now().UTC()}, MergedCount: 1},
		{Type: notify.EventTest, MergedCount: 1},
	}
	for _, ev := range events {
		ev.ID, ev.OccurredAt, ev.Project, ev.Link = uuid.New(), time.Now().UTC(), project, testOrigin+"/"
		req, err := notify.Build(notify.KindWebhook, ev, "secret", time.Now(), nil)
		if err != nil {
			t.Fatal(err)
		}
		validateEvent(t, req.Body)
	}
}
