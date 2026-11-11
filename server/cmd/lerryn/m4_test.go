package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/niltavi/lerryn/server/internal/auth/oidctest"
	"github.com/niltavi/lerryn/server/internal/config"
)

// a18 is a deployment started with the server's own assembly code and a
// local fake OIDC provider, driven over HTTP like a browser, the mobile
// app and an agent would.
type a18 struct {
	t   *testing.T
	url string
	idp *oidctest.Provider
}

func newA18(t *testing.T) *a18 {
	t.Helper()
	idp := oidctest.New(t)
	dir := t.TempDir()
	secret := filepath.Join(dir, "oidc-secret")
	if err := os.WriteFile(secret, []byte(idp.ClientSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(nil)
	cfg := config.Default()
	cfg.Server.PublicURL = "http://" + ts.Listener.Addr().String()
	cfg.Database.DSN = filepath.Join(dir, "lerryn.db")
	cfg.Storage.Path = filepath.Join(dir, "storage")
	cfg.Secrets.MasterKeyFile = filepath.Join(dir, "master.key")
	cfg.Auth.OIDC = config.OIDC{Issuer: idp.Issuer(), ClientID: idp.ClientID, ClientSecretFile: secret, UsernameClaim: "preferred_username"}
	st, err := buildServer(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.db.Close() })
	ts.Config.Handler = st.srv.Handler()
	ts.Start()
	t.Cleanup(ts.Close)
	return &a18{t: t, url: cfg.Server.PublicURL, idp: idp}
}

var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// do sends a request with optional JSON body and headers given as pairs,
// checks the status, decodes the response into v when it is not nil and
// returns the response headers.
func (d *a18) do(method, path string, body any, want int, v any, header ...string) http.Header {
	d.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			d.t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, d.url+path, &buf)
	if err != nil {
		d.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Add(header[i], header[i+1])
	}
	resp, err := noRedirects.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		d.t.Fatalf("%s %s = %d, want %d: %s", method, path, resp.StatusCode, want, b)
	}
	if v != nil {
		if err := json.Unmarshal(b, v); err != nil {
			d.t.Fatalf("decode %s: %v", b, err)
		}
	}
	return resp.Header
}

// cookie returns the named cookie set in h as a Cookie header value.
func cookie(t *testing.T, h http.Header, name string) string {
	t.Helper()
	for _, line := range h.Values("Set-Cookie") {
		c, err := http.ParseSetCookie(line)
		if err == nil && c.Name == name && c.MaxAge >= 0 {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatalf("no %s cookie in %v", name, h.Values("Set-Cookie"))
	return ""
}

type problem struct {
	Code string `json:"code"`
}

func (d *a18) wantCode(method, path string, body any, status int, code string, header ...string) {
	d.t.Helper()
	var p problem
	d.do(method, path, body, status, &p, header...)
	if p.Code != code {
		d.t.Fatalf("%s %s: code = %s, want %s", method, path, p.Code, code)
	}
}

// TestA18AccountsAndAgentAuthorization is acceptance scenario A18: a new
// user registers without an email and logs in; once the admin closes
// registration nobody can sign up; a member logs in through the
// company's OIDC; an agent works with the member's access token and gets
// no more than the member may do; disabling the member ends their session
// and token at once.
func TestA18AccountsAndAgentAuthorization(t *testing.T) {
	d := newA18(t)

	// Registration without an email logs in; the first account is the admin.
	var me struct {
		ID                string  `json:"id"`
		Username          string  `json:"username"`
		Email             *string `json:"email"`
		IsAdmin           bool    `json:"is_admin"`
		PersonalProjectID string  `json:"personal_project_id"`
	}
	resp := d.do(http.MethodPost, "/api/v1/auth/register", map[string]string{"username": "alice", "password": "correct horse"},
		http.StatusCreated, &me)
	admin := cookie(t, resp, "lerryn_session")
	if me.Email != nil || !me.IsAdmin {
		t.Fatalf("alice = %+v", me)
	}
	aliceProject := me.PersonalProjectID
	d.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "ALICE", "password": "correct horse"}, http.StatusOK, nil)

	// The admin closes open registration; self sign-up is refused.
	origin := []string{"Cookie", admin, "Origin", d.url}
	d.do(http.MethodPatch, "/api/v1/admin/settings", map[string]any{"open_registration": false}, http.StatusOK, nil, origin...)
	d.wantCode(http.MethodPost, "/api/v1/auth/register", map[string]string{"username": "bob", "password": "correct horse"},
		http.StatusForbidden, "registration_disabled")
	var disc struct {
		Capabilities struct {
			RegistrationOpen bool     `json:"registration_open"`
			FirstAccount     bool     `json:"first_account"`
			LoginMethods     []string `json:"login_methods"`
		} `json:"capabilities"`
	}
	d.do(http.MethodGet, "/api/v1/discover", nil, http.StatusOK, &disc)
	if disc.Capabilities.RegistrationOpen || disc.Capabilities.FirstAccount || len(disc.Capabilities.LoginMethods) != 2 {
		t.Fatalf("capabilities = %+v", disc.Capabilities)
	}

	// A member logs in through OIDC, which creates the account even with
	// registration closed.
	resp = d.do(http.MethodGet, "/api/v1/auth/oidc/start?return_to=/tasks", nil, http.StatusFound, nil)
	state := cookie(t, resp, "lerryn_oidc")
	cb, err := url.Parse(d.idp.Authorize(resp.Get("Location"), oidctest.User{Subject: "u-42", PreferredUsername: "lin", Email: "lin@corp.example"}))
	if err != nil {
		t.Fatal(err)
	}
	resp = d.do(http.MethodGet, cb.Path+"?"+cb.RawQuery, nil, http.StatusFound, nil, "Cookie", state)
	if resp.Get("Location") != d.url+"/tasks" {
		t.Fatalf("callback redirect = %s", resp.Get("Location"))
	}
	lin := cookie(t, resp, "lerryn_session")
	d.do(http.MethodGet, "/api/v1/me", nil, http.StatusOK, &me, "Cookie", lin)
	if me.Username != "lin" || me.IsAdmin {
		t.Fatalf("lin = %+v", me)
	}
	linID := me.ID

	// The agent works with lin's token and is exactly lin: it sees lin's
	// identity, cannot reach alice's project, and a read token cannot write.
	var tok struct {
		Token string `json:"token"`
	}
	d.do(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "claude-code", "scopes": []string{"read", "write", "execute"}},
		http.StatusCreated, &tok, "Cookie", lin, "Origin", d.url)
	agent := []string{"Authorization", "Bearer " + tok.Token}
	var who struct {
		Principal struct {
			Username string `json:"username"`
			IsAdmin  bool   `json:"is_admin"`
		} `json:"principal"`
	}
	d.do(http.MethodGet, "/api/v1/discover", nil, http.StatusOK, &who, agent...)
	if who.Principal.Username != "lin" || who.Principal.IsAdmin {
		t.Fatalf("agent principal = %+v", who.Principal)
	}
	task := map[string]any{
		"manifest": map[string]any{"description": "汇总", "runtime": map[string]any{"language": "python", "version": "3.12"},
			"entrypoint": []string{"python", "main.py"}, "workflow": []map[string]any{{"title": "输出问候", "description": "在日志中输出一条问候。"}}},
		"files": []map[string]any{{"path": "main.py", "content": "print('hi')\n"}},
	}
	d.wantCode(http.MethodPut, "/api/v1/projects/"+aliceProject+"/tasks/digest", task, http.StatusNotFound, "not_found", agent...)
	d.wantCode(http.MethodGet, "/api/v1/admin/users", nil, http.StatusForbidden, "permission_denied", agent...)
	// Tokens cannot mint tokens.
	d.wantCode(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "more", "scopes": []string{"read"}},
		http.StatusForbidden, "insufficient_scope", agent...)

	// The admin disables lin: the session and the token stop at once.
	d.do(http.MethodPatch, "/api/v1/admin/users/"+linID, map[string]any{"disabled": true}, http.StatusOK, nil, origin...)
	d.wantCode(http.MethodGet, "/api/v1/discover", nil, http.StatusUnauthorized, "unauthenticated", agent...)
	d.wantCode(http.MethodGet, "/api/v1/me", nil, http.StatusUnauthorized, "unauthenticated", "Cookie", lin)

	// Every step is in the audit log, without secrets.
	var events struct {
		Items []struct {
			Action string `json:"action"`
			Result string `json:"result"`
		} `json:"items"`
	}
	d.do(http.MethodGet, "/api/v1/admin/audit-events?limit=200", nil, http.StatusOK, &events, origin...)
	seen := map[string]bool{}
	for _, e := range events.Items {
		seen[e.Action+"/"+e.Result] = true
	}
	for _, want := range []string{"register/success", "login/success", "updateSettings/success", "register/failure",
		"oidcCallback/success", "createToken/success", "adminUpdateUser/success"} {
		if !seen[want] {
			t.Errorf("audit log lacks %s: %v", want, seen)
		}
	}
}

// TestA17MobileSessionIsReadOnly covers the server side of A17: a phone
// paired from the console reads but cannot change anything.
func TestA17MobileSessionIsReadOnly(t *testing.T) {
	d := newA18(t)
	resp := d.do(http.MethodPost, "/api/v1/auth/register", map[string]string{"username": "alice", "password": "correct horse"}, http.StatusCreated, nil)
	web := cookie(t, resp, "lerryn_session")
	var code struct {
		Code string `json:"code"`
		URI  string `json:"uri"`
	}
	d.do(http.MethodPost, "/api/v1/auth/pairing", nil, http.StatusCreated, &code, "Cookie", web, "Origin", d.url)
	u, err := url.Parse(code.URI)
	if err != nil {
		t.Fatal(err)
	}
	resp = d.do(http.MethodPost, "/api/v1/auth/pairing/exchange", map[string]any{
		"server": u.Query().Get("server"), "code": u.Query().Get("code"), "device": map[string]string{"name": "iPhone", "platform": "ios"},
	}, http.StatusOK, nil)
	phone := cookie(t, resp, "lerryn_session")
	d.do(http.MethodGet, "/api/v1/me", nil, http.StatusOK, nil, "Cookie", phone)
	d.do(http.MethodGet, "/api/v1/projects", nil, http.StatusOK, nil, "Cookie", phone)
	d.wantCode(http.MethodPost, "/api/v1/projects", map[string]string{"name": "x"}, http.StatusForbidden, "insufficient_scope", "Cookie", phone)
	d.wantCode(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "x", "scopes": []string{"read"}},
		http.StatusForbidden, "insufficient_scope", "Cookie", phone, "Origin", d.url)
	// The code worked once.
	d.wantCode(http.MethodPost, "/api/v1/auth/pairing/exchange", map[string]any{
		"server": u.Query().Get("server"), "code": code.Code, "device": map[string]string{"name": "iPad", "platform": "ios"},
	}, http.StatusUnauthorized, "unauthenticated")
}
