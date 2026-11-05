//go:build integration

package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The server side of A17 and the role part of A18, end to end on the
// built binary. The OIDC login, registration settings and disabling of
// A18 are in server/cmd/lerryn/m4_test.go; the phone screens of A17 are
// verified in the console track.

// session sends a request with the account's browser session.
func (a *account) session(t *testing.T, method, path string, body any, cookie *http.Cookie) response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, a.s.base+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", a.s.base)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var data bytes.Buffer
	_, _ = data.ReadFrom(resp.Body)
	return response{status: resp.StatusCode, header: resp.Header, body: data.Bytes()}
}

// pairPhone pairs a phone from the account's console session and returns
// the phone's session cookie.
func (a *account) pairPhone(t *testing.T) *http.Cookie {
	t.Helper()
	r := a.session(t, http.MethodPost, "/api/v1/auth/pairing", nil, a.cookie)
	var code struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(r.body, &code); err != nil || r.status != http.StatusCreated {
		t.Fatalf("pairing = %d %s", r.status, r.body)
	}
	u, err := url.Parse(code.URI)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"server": u.Query().Get("server"), "code": u.Query().Get("code"),
		"device": map[string]string{"name": "iPhone", "platform": "ios"}})
	resp, err := http.Post(a.s.base+"/api/v1/auth/pairing/exchange", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == "lerryn_session" {
			return c
		}
	}
	t.Fatalf("exchange = %d without a session", resp.StatusCode)
	return nil
}

// A17 (server side): a paired phone goes from the overview to a run and
// reads its status, stages and log tail; every write is refused.
func TestA17PhoneReadsARunAndWritesNothing(t *testing.T) {
	s := startInstance(t)
	alice := s.signUp("alice")
	agent := alice.agent("agent")
	path := "/api/v1/projects/" + alice.project + "/tasks/report"
	applied := agent.want(t, http.StatusOK, http.MethodPut, path,
		map[string]any{"manifest": reportManifest(), "files": []map[string]any{{"path": "main.py", "content": reportTask}}})
	taskID := applied["task"].(map[string]any)["id"].(string)
	started := agent.want(t, http.StatusAccepted, http.MethodPost, "/api/v1/tasks/"+taskID+"/runs", map[string]any{"test": true})
	runID := started["id"].(string)
	agent.waitRun(t, runID, 5*time.Minute)

	phone := alice.pairPhone(t)
	read := func(path string) map[string]any {
		t.Helper()
		r := alice.session(t, http.MethodGet, path, nil, phone)
		if r.status != http.StatusOK {
			t.Fatalf("phone GET %s = %d %s", path, r.status, r.body)
		}
		return r.json(t)
	}
	overview := read("/api/v1/overview")
	if counts := overview["counts"].(map[string]any); counts["tasks"] != float64(1) {
		t.Fatalf("overview = %v", overview)
	}
	read("/api/v1/tasks/" + taskID)
	detail := read("/api/v1/runs/" + runID)
	if detail["status"] != "completed" || len(detail["stages"].([]any)) != 2 {
		t.Fatalf("run on the phone = %v", detail)
	}
	for _, st := range detail["stages"].([]any) {
		if st.(map[string]any)["status"] != "done" {
			t.Fatalf("stages on the phone = %v", detail["stages"])
		}
	}
	logs := read("/api/v1/runs/" + runID + "/logs?tail=20")
	if !strings.Contains(fmt.Sprint(logs["lines"]), "fetched 3 rows") {
		t.Fatalf("log tail on the phone = %v", logs)
	}
	read("/api/v1/runs/" + runID + "/events")

	for _, w := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/tasks/" + taskID + "/runs", map[string]any{"test": true}},
		{http.MethodPost, "/api/v1/runs/" + runID + "/rerun", map[string]any{}},
		{http.MethodPost, "/api/v1/runs/" + runID + "/cancel", nil},
		{http.MethodPut, path, map[string]any{"manifest": reportManifest(), "files": []map[string]any{{"path": "main.py", "content": reportTask}}}},
		{http.MethodPost, "/api/v1/tasks/" + taskID + "/activation", map[string]any{"active": true}},
		{http.MethodPut, "/api/v1/tasks/" + taskID + "/triggers", map[string]any{"triggers": []any{}}},
		{http.MethodPost, "/api/v1/credential-requests", map[string]any{"task_id": taskID, "name": "API_TOKEN", "purpose": "x", "fields": []any{map[string]any{"name": "API_TOKEN"}}}},
		{http.MethodPost, "/api/v1/projects", map[string]any{"name": "phone"}},
		{http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "phone", "scopes": []string{"read"}}},
	} {
		r := alice.session(t, w.method, w.path, w.body, phone)
		if r.status != http.StatusForbidden || !strings.Contains(string(r.body), "insufficient_scope") {
			t.Errorf("phone %s %s = %d %s", w.method, w.path, r.status, r.body)
		}
	}
	// Nothing changed: still one run and one version.
	if runs := agent.want(t, http.StatusOK, http.MethodGet, "/api/v1/runs?task_id="+taskID, nil)["items"].([]any); len(runs) != 1 {
		t.Fatalf("runs after the phone's attempts = %v", runs)
	}
}

// A18 (roles): an agent works with a member's access token and can do no
// more than the member: a viewer's agent reads but cannot change or run,
// a promotion takes effect at once, a read-only token cannot write even
// for the owner, and removing the member hides the project again.
func TestA18AgentIsBoundByTheMembersRole(t *testing.T) {
	s := startInstance(t)
	alice, bob := s.signUp("alice"), s.signUp("bob")
	owner := alice.agent("owner-agent")
	project := owner.want(t, http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]any{"name": "team"})["id"].(string)
	bobID := bob.agent("whoami").want(t, http.StatusOK, http.MethodGet, "/api/v1/me", nil)["id"].(string)
	member := "/api/v1/projects/" + project + "/members/" + bobID
	owner.want(t, http.StatusOK, http.MethodPut, member, map[string]any{"role": "viewer"})
	path := "/api/v1/projects/" + project + "/tasks/report"
	body := map[string]any{"manifest": reportManifest(), "files": []map[string]any{{"path": "main.py", "content": reportTask}}}
	taskID := owner.want(t, http.StatusOK, http.MethodPut, path, body)["task"].(map[string]any)["id"].(string)

	agent := bob.agent("bob-agent")
	agent.want(t, http.StatusOK, http.MethodGet, "/api/v1/tasks/"+taskID, nil)
	for _, w := range []struct {
		method, path string
		body         any
		header       []string
	}{
		{http.MethodPut, path, body, []string{"If-Match", "1"}},
		{http.MethodPost, "/api/v1/tasks/" + taskID + "/runs", map[string]any{"test": true}, nil},
		{http.MethodPost, "/api/v1/tasks/" + taskID + "/activation", map[string]any{"active": false}, nil},
		{http.MethodPut, member, map[string]any{"role": "owner"}, nil},
	} {
		r := agent.do(t, w.method, w.path, w.body, w.header...)
		if r.status != http.StatusForbidden || r.json(t)["code"] != "permission_denied" {
			t.Errorf("viewer's agent %s %s = %d %s", w.method, w.path, r.status, r.body)
		}
	}
	m := agent.mcp(t)
	if out, isErr := m.call(t, "run_start", map[string]any{"task_id": taskID, "test": true}); !isErr || out["code"] != "permission_denied" {
		t.Errorf("viewer's agent run_start over MCP = %v", out)
	}

	// Promoted to maintainer, the same token may run the task.
	owner.want(t, http.StatusOK, http.MethodPut, member, map[string]any{"role": "maintainer"})
	agent.want(t, http.StatusAccepted, http.MethodPost, "/api/v1/tasks/"+taskID+"/runs", map[string]any{"test": true})

	// A read-only token of the owner cannot write.
	r := alice.session(t, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "reader", "scopes": []string{"read"}}, alice.cookie)
	reader := &client{s: s, token: r.json(t)["token"].(string), project: alice.project}
	if r := reader.do(t, http.MethodPost, "/api/v1/tasks/"+taskID+"/runs", map[string]any{"test": true}); r.status != http.StatusForbidden || r.json(t)["code"] != "insufficient_scope" {
		t.Errorf("read-only token starts a run = %d %s", r.status, r.body)
	}

	// Removed from the project, bob's agent no longer sees it.
	owner.want(t, http.StatusNoContent, http.MethodDelete, member, nil)
	if r := agent.do(t, http.MethodGet, "/api/v1/tasks/"+taskID, nil); r.status != http.StatusNotFound {
		t.Errorf("removed member reads the task = %d %s", r.status, r.body)
	}
}
