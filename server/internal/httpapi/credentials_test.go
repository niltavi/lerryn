package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/secrets"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

type credClient struct {
	*taskClient
	accounts *service.Accounts
	creds    *service.Credentials
}

func newCredClient(t *testing.T) *credClient {
	t.Helper()
	db := storetest.Open(t)
	blobs, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, _ := secrets.GenerateKey()
	kr, _ := secrets.NewLocalKeyring(key)
	authz := service.NewAuthorizer(db)
	runs := service.NewRuns(db, authz, nil)
	accounts := service.NewAccounts(db, nil)
	creds := service.NewCredentials(db, authz, kr, nil)
	h := New(Options{
		PublicURL:   testOrigin,
		Version:     "dev",
		Accounts:    accounts,
		Tasks:       service.NewTasks(db, authz, blobs, nil),
		Runs:        runs,
		Credentials: creds,
		Idempotency: service.NewIdempotency(db, nil),
	})
	tc := &taskClient{apiClient: &apiClient{t: t, h: h}, coord: service.NewCoordinator(db, blobs, runs, nil)}
	return &credClient{taskClient: tc, accounts: accounts, creds: creds}
}

func credTaskRequest() map[string]any {
	r := digestRequest()
	r["manifest"].(map[string]any)["credentials"] = []any{
		map[string]any{"name": "LLM_API_KEY"},
		map[string]any{"name": "CRM", "as": "file", "optional": true},
	}
	return r
}

func TestCredentialRequestAPI(t *testing.T) {
	c := newCredClient(t)
	token, project := c.agent("alice")
	rec := c.as(token, http.MethodPut, "/api/v1/projects/"+project.String()+"/tasks/digest", credTaskRequest())
	c.want(rec, http.StatusOK, "")
	task := decode[ApplyTaskResult](t, rec).Task

	// The task cannot run without its credential: the answer says how to
	// get one.
	rec = c.as(token, http.MethodPost, "/api/v1/tasks/"+task.ID.String()+"/runs", map[string]any{"test": true})
	c.want(rec, http.StatusUnprocessableEntity, ErrorCodeCredentialMissing)
	p := decode[Problem](t, rec)
	if p.NextActions == nil || (*p.NextActions)[0].Action != "credential_request" || !strings.Contains(*p.Detail, "LLM_API_KEY") {
		t.Fatalf("problem = %+v", p)
	}

	// listTaskCredentials shows what is missing.
	rec = c.as(token, http.MethodGet, "/api/v1/tasks/"+task.ID.String()+"/credentials", nil)
	c.want(rec, http.StatusOK, "")
	list := decode[TaskCredentialList](t, rec)
	if len(list.Items) != 2 || list.Items[0].Status != "missing" || list.Items[1].As != "file" || !list.Items[1].Optional {
		t.Fatalf("task credentials = %+v", list)
	}

	body := map[string]any{
		"task_id": task.ID, "name": "LLM_API_KEY", "purpose": "调用模型",
		"fields": []any{map[string]any{"name": "LLM_API_KEY", "label": "密钥"}},
	}
	rec = c.call(http.MethodPost, "/api/v1/credential-requests", body, "Authorization", token, "Idempotency-Key", "req-1")
	c.want(rec, http.StatusCreated, "")
	req := decode[CredentialRequest](t, rec)
	if req.URL != testOrigin+"/c/"+req.ID.String() || req.Status != "pending" || req.Client == nil || *req.Client != "agent" ||
		req.Task == nil || req.Task.ID != task.ID || req.Fields[0].Secret == nil || !*req.Fields[0].Secret {
		t.Fatalf("request = %+v", req)
	}
	// Same key, same body: the same request. Different body: conflict.
	rec = c.call(http.MethodPost, "/api/v1/credential-requests", body, "Authorization", token, "Idempotency-Key", "req-1")
	c.want(rec, http.StatusCreated, "")
	if again := decode[CredentialRequest](t, rec); again.ID != req.ID {
		t.Fatalf("retry created %s, want %s", again.ID, req.ID)
	}
	body["purpose"] = "别的用途"
	rec = c.call(http.MethodPost, "/api/v1/credential-requests", body, "Authorization", token, "Idempotency-Key", "req-1")
	c.want(rec, http.StatusConflict, ErrorCodeIdempotencyConflict)

	rec = c.as(token, http.MethodGet, "/api/v1/tasks/"+task.ID.String()+"/credentials", nil)
	if l := decode[TaskCredentialList](t, rec); l.Items[0].PendingRequestID == nil || *l.Items[0].PendingRequestID != req.ID {
		t.Fatalf("pending request not shown: %+v", l.Items[0])
	}

	// The user fills it in on the fill page (service call here).
	session := c.session("alice")
	cred, err := c.creds.Fill(context.Background(), session, req.ID, map[string]string{"LLM_API_KEY": "sk-api-test-value"})
	if err != nil {
		t.Fatal(err)
	}

	// The agent sees only the status and the credential ID.
	rec = c.as(token, http.MethodGet, "/api/v1/credential-requests/"+req.ID.String(), nil)
	c.want(rec, http.StatusOK, "")
	if strings.Contains(rec.Body.String(), "sk-api-test-value") {
		t.Fatal("request response contains the value")
	}
	got := decode[CredentialRequest](t, rec)
	if got.Status != "fulfilled" || got.CredentialID == nil || *got.CredentialID != cred.ID || got.FulfilledAt == nil {
		t.Fatalf("fulfilled request = %+v", got)
	}

	rec = c.as(token, http.MethodGet, "/api/v1/credentials", nil)
	c.want(rec, http.StatusOK, "")
	creds := decode[CredentialPage](t, rec)
	if len(creds.Items) != 1 || creds.Items[0].Version != 1 || len(creds.Items[0].Grants) != 1 || creds.Items[0].Grants[0].Name != "LLM_API_KEY" ||
		strings.Contains(rec.Body.String(), "sk-api-test-value") {
		t.Fatalf("credentials = %s", rec.Body)
	}

	// Now the run is accepted.
	rec = c.as(token, http.MethodPost, "/api/v1/tasks/"+task.ID.String()+"/runs", map[string]any{"test": true})
	c.want(rec, http.StatusAccepted, "")

	// Revoke: the next run is refused with credential_unavailable.
	rec = c.as(token, http.MethodDelete, "/api/v1/credentials/"+cred.ID.String(), nil)
	c.want(rec, http.StatusOK, "")
	if r := decode[Credential](t, rec); r.Status != "revoked" || r.RevokedAt == nil {
		t.Fatalf("revoked = %+v", r)
	}
	rec = c.as(token, http.MethodPost, "/api/v1/tasks/"+task.ID.String()+"/runs", map[string]any{"test": true})
	c.want(rec, http.StatusUnprocessableEntity, ErrorCodeCredentialUnavailable)
}

func (c *credClient) session(username string) *auth.Principal {
	c.t.Helper()
	_, sess, err := c.accounts.Login(context.Background(), username, "correct horse")
	if err != nil {
		c.t.Fatal(err)
	}
	p, err := c.accounts.Authenticate(context.Background(), sess.Token)
	if err != nil {
		c.t.Fatal(err)
	}
	return p
}

func TestCredentialAPIAuthorization(t *testing.T) {
	c := newCredClient(t)
	alice, project := c.agent("alice")
	bob, _ := c.agent("bob")
	readOnly, _ := c.agent("carol", "read")
	rec := c.as(alice, http.MethodPut, "/api/v1/projects/"+project.String()+"/tasks/digest", credTaskRequest())
	task := decode[ApplyTaskResult](t, rec).Task
	rec = c.as(alice, http.MethodPost, "/api/v1/credential-requests", map[string]any{
		"task_id": task.ID, "name": "LLM_API_KEY", "purpose": "调用模型", "fields": []any{map[string]any{"name": "LLM_API_KEY"}},
	})
	req := decode[CredentialRequest](t, rec)
	cred, err := c.creds.Fill(context.Background(), c.session("alice"), req.ID, map[string]string{"LLM_API_KEY": "sk-api-test-value"})
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/api/v1/credential-requests/" + req.ID.String(),
		"/api/v1/credentials/" + cred.ID.String(),
		"/api/v1/tasks/" + task.ID.String() + "/credentials",
	} {
		c.want(c.as(bob, http.MethodGet, path, nil), http.StatusNotFound, ErrorCodeNotFound)
	}
	c.want(c.as(bob, http.MethodDelete, "/api/v1/credentials/"+cred.ID.String(), nil), http.StatusNotFound, ErrorCodeNotFound)
	c.want(c.as(bob, http.MethodPut, "/api/v1/credentials/"+cred.ID.String()+"/grants/"+task.ID.String(), map[string]any{"name": "X"}),
		http.StatusNotFound, ErrorCodeNotFound)
	c.want(c.as(bob, http.MethodPost, "/api/v1/credential-requests", map[string]any{
		"task_id": task.ID, "name": "LLM_API_KEY", "purpose": "x", "fields": []any{map[string]any{"name": "LLM_API_KEY"}},
	}), http.StatusNotFound, ErrorCodeNotFound)
	// Creating requests and revoking need the write scope.
	c.want(c.as(readOnly, http.MethodPost, "/api/v1/credential-requests", map[string]any{
		"credential_id": cred.ID, "purpose": "x", "fields": []any{map[string]any{"name": "LLM_API_KEY"}},
	}), http.StatusForbidden, ErrorCodeInsufficientScope)

	// The owner can move the grant and withdraw it.
	rec = c.as(alice, http.MethodPut, "/api/v1/credentials/"+cred.ID.String()+"/grants/"+task.ID.String(), map[string]any{"name": "OTHER_KEY"})
	c.want(rec, http.StatusOK, "")
	if g := decode[CredentialGrant](t, rec); g.Name != "OTHER_KEY" || g.Task.ID != task.ID {
		t.Fatalf("grant = %+v", g)
	}
	c.want(c.as(alice, http.MethodDelete, "/api/v1/credentials/"+cred.ID.String()+"/grants/"+task.ID.String(), nil), http.StatusNoContent, "")
	c.want(c.as(alice, http.MethodDelete, "/api/v1/credentials/"+cred.ID.String()+"/grants/"+task.ID.String(), nil), http.StatusNotFound, ErrorCodeNotFound)

	// Cancel an open request.
	rec = c.as(alice, http.MethodPost, "/api/v1/credential-requests", map[string]any{
		"credential_id": cred.ID, "purpose": "更新", "fields": []any{map[string]any{"name": "LLM_API_KEY"}},
	})
	c.want(rec, http.StatusCreated, "")
	update := decode[CredentialRequest](t, rec)
	if update.Task != nil || update.Name != "LLM_API_KEY" {
		t.Fatalf("update request = %+v", update)
	}
	rec = c.as(alice, http.MethodDelete, "/api/v1/credential-requests/"+update.ID.String(), nil)
	c.want(rec, http.StatusOK, "")
	if r := decode[CredentialRequest](t, rec); r.Status != "cancelled" {
		t.Fatalf("cancelled = %+v", r)
	}
	rec = c.as(alice, http.MethodGet, "/api/v1/credential-requests?status=cancelled", nil)
	c.want(rec, http.StatusOK, "")
	if page := decode[CredentialRequestPage](t, rec); len(page.Items) != 1 {
		t.Fatalf("cancelled requests = %+v", page)
	}
}

// The console's fill dialog submits values through the API. Only the
// user's browser session may do so; an agent with the user's token may
// not, and no answer quotes a value.
func TestFillCredentialRequestAPI(t *testing.T) {
	c := newCredClient(t)
	token, project := c.agent("alice")
	c.agent("bob")
	rec := c.call(http.MethodPost, "/api/v1/auth/login", map[string]any{"username": "alice", "password": "correct horse"})
	c.want(rec, http.StatusOK, "")
	cookie := sessionCookie(t, rec)
	rec = c.call(http.MethodPost, "/api/v1/auth/login", map[string]any{"username": "bob", "password": "correct horse"})
	bobCookie := sessionCookie(t, rec)
	web := func(cookie string, id uuid.UUID, values map[string]any, headers ...string) *httptest.ResponseRecorder {
		t.Helper()
		headers = append([]string{"Cookie", cookie, "Origin", testOrigin}, headers...)
		return c.call(http.MethodPost, "/api/v1/credential-requests/"+id.String()+"/values", map[string]any{"values": values}, headers...)
	}

	rec = c.as(token, http.MethodPut, "/api/v1/projects/"+project.String()+"/tasks/digest", credTaskRequest())
	task := decode[ApplyTaskResult](t, rec).Task
	rec = c.as(token, http.MethodPost, "/api/v1/credential-requests", map[string]any{
		"task_id": task.ID, "name": "LLM_API_KEY", "purpose": "调用模型",
		"fields": []any{map[string]any{"name": "LLM_API_KEY"}, map[string]any{"name": "REGION", "secret": false, "optional": true}},
	})
	c.want(rec, http.StatusCreated, "")
	req := decode[CredentialRequest](t, rec)
	path := "/api/v1/credential-requests/" + req.ID.String() + "/values"

	// The agent's token, even with every scope, cannot fill in.
	rec = c.as(token, http.MethodPost, path, map[string]any{"values": map[string]any{"LLM_API_KEY": "sk-agent-typed-value"}})
	c.want(rec, http.StatusForbidden, ErrorCodeInsufficientScope)
	// A cookie write must come from the console's origin.
	rec = c.call(http.MethodPost, path, map[string]any{"values": map[string]any{"LLM_API_KEY": "sk-cross-site-value"}}, "Cookie", cookie)
	c.want(rec, http.StatusForbidden, ErrorCodeOriginNotAllowed)
	// Someone else's request does not exist for them.
	c.want(web(bobCookie, req.ID, map[string]any{"LLM_API_KEY": "sk-bob-typed-value"}), http.StatusNotFound, ErrorCodeNotFound)

	// A rejected submission names the field, never the value.
	rec = web(cookie, req.ID, map[string]any{"LLM_API_KEY": "sk-line-one\nsk-line-two"})
	c.want(rec, http.StatusUnprocessableEntity, ErrorCodeValidationFailed)
	if strings.Contains(rec.Body.String(), "sk-line") {
		t.Fatalf("problem quotes the value: %s", rec.Body)
	}
	if p := decode[Problem](t, rec); p.Errors == nil || len(*p.Errors) != 1 || (*p.Errors)[0].Pointer == nil || *(*p.Errors)[0].Pointer != "#/values/LLM_API_KEY" {
		t.Fatalf("problem = %s", rec.Body)
	}
	if again := decode[CredentialRequest](t, c.as(token, http.MethodGet, "/api/v1/credential-requests/"+req.ID.String(), nil)); again.Status != "pending" {
		t.Fatalf("request after a rejected submission = %+v", again)
	}

	rec = web(cookie, req.ID, map[string]any{"LLM_API_KEY": " sk-api-test-value ", "UNKNOWN": "ignored"})
	c.want(rec, http.StatusOK, "")
	if strings.Contains(rec.Body.String(), "sk-api-test-value") {
		t.Fatalf("response quotes the value: %s", rec.Body)
	}
	got := decode[CredentialRequest](t, rec)
	if got.Status != "fulfilled" || got.CredentialID == nil || got.FulfilledAt == nil {
		t.Fatalf("filled request = %+v", got)
	}
	// The task can run with it now.
	c.want(c.as(token, http.MethodPost, "/api/v1/tasks/"+task.ID.String()+"/runs", map[string]any{"test": true}), http.StatusAccepted, "")

	// A request is filled once.
	c.want(web(cookie, req.ID, map[string]any{"LLM_API_KEY": "sk-second-value"}), http.StatusConflict, ErrorCodeRequestClosed)

	// Updating from the console: a new version of the same credential.
	rec = c.call(http.MethodPost, "/api/v1/credential-requests", map[string]any{
		"credential_id": got.CredentialID, "purpose": "调用模型", "fields": []any{map[string]any{"name": "LLM_API_KEY"}},
	}, "Cookie", cookie, "Origin", testOrigin)
	c.want(rec, http.StatusCreated, "")
	update := decode[CredentialRequest](t, rec)
	rec = web(cookie, update.ID, map[string]any{"LLM_API_KEY": "sk-rotated-value"})
	c.want(rec, http.StatusOK, "")
	if r := decode[CredentialRequest](t, rec); r.CredentialID == nil || *r.CredentialID != *got.CredentialID {
		t.Fatalf("update = %+v", r)
	}
	rec = c.as(token, http.MethodGet, "/api/v1/credentials/"+got.CredentialID.String(), nil)
	if cred := decode[Credential](t, rec); cred.Version != 2 {
		t.Fatalf("credential after update = %+v", cred)
	}
}

func TestCredentialRequestValidationAPI(t *testing.T) {
	c := newCredClient(t)
	token, _ := c.agent("alice")
	rec := c.as(token, http.MethodPost, "/api/v1/credential-requests", map[string]any{
		"purpose": "调用模型", "fields": []any{map[string]any{"name": "LLM_API_KEY"}},
	})
	c.want(rec, http.StatusUnprocessableEntity, ErrorCodeValidationFailed)
	rec = c.as(token, http.MethodPost, "/api/v1/credential-requests", map[string]any{
		"task_id": uuid.New(), "name": "A", "purpose": "x", "fields": []any{map[string]any{"name": "PATH"}},
	})
	c.want(rec, http.StatusUnprocessableEntity, ErrorCodeValidationFailed)
}
