package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

const testOrigin = "https://lerryn.example.com"

// apiClient drives the real handler with the real account service on a
// test database. Every response is checked against the spec.
type apiClient struct {
	t *testing.T
	h http.Handler
}

func newAPIClient(t *testing.T) *apiClient {
	t.Helper()
	accounts := service.NewAccounts(storetest.Open(t), nil)
	return &apiClient{t: t, h: New(Options{PublicURL: testOrigin, Version: "dev", Accounts: accounts})}
}

// call sends a request. Header pairs follow the body, for example
// "Authorization", "Bearer ..."; a "Host" pair sets the request's host.
func (c *apiClient) call(method, path string, body any, header ...string) *httptest.ResponseRecorder {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		if header[i] == "Host" {
			req.Host = header[i+1]
			continue
		}
		req.Header.Add(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	assertMatchesSpec(c.t, req, rec)
	return rec
}

func (c *apiClient) want(rec *httptest.ResponseRecorder, status int, code ErrorCode) {
	c.t.Helper()
	if rec.Code != status {
		c.t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body)
	}
	if code == "" {
		return
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		c.t.Fatal(err)
	}
	if p.Code != code {
		c.t.Fatalf("code = %s, want %s: %s", p.Code, code, rec.Body)
	}
}

// sessionCookie returns the session cookie set by rec as a Cookie header value.
func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName {
			if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
				t.Errorf("session cookie attributes: %+v", c)
			}
			return c.Name + "=" + c.Value
		}
	}
	t.Fatalf("no %s cookie in %v", SessionCookieName, rec.Header().Values("Set-Cookie"))
	return ""
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %T: %v: %s", v, err, rec.Body)
	}
	return v
}

// TestSessionWriteOrigins pins which Origin a session write may come from
// (public_url's or one of the extra origins, matched on scheme and host),
// and that the session cookie is Secure only at an https address.
func TestSessionWriteOrigins(t *testing.T) {
	const lan = "http://192.168.50.50:33333"
	accounts := service.NewAccounts(storetest.Open(t), nil)
	c := &apiClient{t: t, h: New(Options{
		PublicURL:    testOrigin,
		ExtraOrigins: []string{lan},
		Version:      "dev",
		Accounts:     accounts,
	})}
	// Signed in at public_url: Secure, which sessionCookie checks.
	cookie := sessionCookie(t, c.call(http.MethodPost, "/api/v1/auth/register", map[string]any{"username": "alice", "password": "correct horse"}))

	// Signed in at the plain-http LAN address: a Secure cookie would never
	// come back to it.
	rec := c.call(http.MethodPost, "/api/v1/auth/login", map[string]any{"username": "alice", "password": "correct horse"},
		"Host", "192.168.50.50:33333", "Origin", lan)
	c.want(rec, http.StatusOK, "")
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == SessionCookieName && (ck.Secure || !ck.HttpOnly) {
			t.Errorf("session cookie at the http address: %+v", ck)
		}
	}

	tokenReq := map[string]any{"name": "laptop", "scopes": []string{"read"}}
	tests := []struct {
		origin string
		want   int
	}{
		{testOrigin, http.StatusCreated},
		{lan, http.StatusCreated},
		{"HTTP://192.168.50.50:33333", http.StatusCreated},
		{"http://lerryn.example.com", http.StatusForbidden},
		{"https://192.168.50.50:33333", http.StatusForbidden},
		{"http://192.168.50.50", http.StatusForbidden},
		{"https://lerryn.example.com.evil.example", http.StatusForbidden},
		{"", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.origin, func(t *testing.T) {
			header := []string{"Cookie", cookie}
			if tt.origin != "" {
				header = append(header, "Origin", tt.origin)
			}
			rec := c.call(http.MethodPost, "/api/v1/me/tokens", tokenReq, header...)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body)
			}
		})
	}
}

func TestAccountFlow(t *testing.T) {
	c := newAPIClient(t)

	// Registration needs only a username and a password, and logs in.
	rec := c.call(http.MethodPost, "/api/v1/auth/register", map[string]any{"username": "alice", "password": "correct horse"})
	c.want(rec, http.StatusCreated, "")
	cookie := sessionCookie(t, rec)
	me := decode[Me](t, rec)
	if me.Username != "alice" || !me.IsAdmin || me.Email != nil || me.DisplayName != nil {
		t.Fatalf("me = %+v", me)
	}

	c.want(c.call(http.MethodPost, "/api/v1/auth/register", map[string]any{"username": "ALICE", "password": "whatever1"}),
		http.StatusConflict, ErrorCodeUsernameTaken)
	rec = c.call(http.MethodPost, "/api/v1/auth/register", map[string]any{"username": "x", "password": "short"})
	c.want(rec, http.StatusBadRequest, ErrorCodeValidationFailed)
	wantPointers(t, rec, "#/username", "#/password")
	if strings.Contains(rec.Body.String(), "short") {
		t.Fatalf("validation errors echo the password: %s", rec.Body)
	}

	// Reads with the session need no Origin.
	if got := decode[Me](t, c.call(http.MethodGet, "/api/v1/me", nil, "Cookie", cookie)); got.ID != me.ID {
		t.Fatalf("getMe = %+v", got)
	}

	// Writes with the session must come from the console's origin.
	tokenReq := map[string]any{"name": "laptop", "scopes": []string{"read"}}
	c.want(c.call(http.MethodPost, "/api/v1/me/tokens", tokenReq, "Cookie", cookie), http.StatusForbidden, ErrorCodeOriginNotAllowed)
	c.want(c.call(http.MethodPost, "/api/v1/me/tokens", tokenReq, "Cookie", cookie, "Origin", "https://evil.example.com"),
		http.StatusForbidden, ErrorCodeOriginNotAllowed)
	rec = c.call(http.MethodPost, "/api/v1/me/tokens", tokenReq, "Cookie", cookie, "Origin", testOrigin)
	c.want(rec, http.StatusCreated, "")
	readOnly := decode[CreatedToken](t, rec)
	if !strings.HasPrefix(readOnly.Token, readOnly.AccessToken.Prefix) {
		t.Fatalf("created = %+v", readOnly)
	}
	bearer := "Bearer " + readOnly.Token

	// Discover reports who the token belongs to.
	disc := decode[Discovery](t, c.call(http.MethodGet, "/api/v1/discover", nil, "Authorization", bearer))
	if disc.Principal == nil || disc.Principal.Username != "alice" || disc.Principal.AuthMethod != AuthMethodAccessToken ||
		len(disc.Principal.Scopes) != 1 || disc.Principal.Scopes[0] != TokenScopeRead {
		t.Fatalf("principal = %+v", disc.Principal)
	}

	// A token cannot mint tokens, and a read-only token cannot write.
	c.want(c.call(http.MethodPost, "/api/v1/me/tokens", tokenReq, "Authorization", bearer), http.StatusForbidden, ErrorCodeInsufficientScope)
	revokePath := "/api/v1/me/tokens/" + readOnly.AccessToken.ID.String()
	c.want(c.call(http.MethodDelete, revokePath, nil, "Authorization", bearer), http.StatusForbidden, ErrorCodeInsufficientScope)

	// Listing works with either credential.
	page := decode[AccessTokenPage](t, c.call(http.MethodGet, "/api/v1/me/tokens", nil, "Authorization", bearer))
	if len(page.Items) != 1 || page.NextCursor != nil || page.Items[0].RevokedAt != nil {
		t.Fatalf("page = %+v", page)
	}
	c.want(c.call(http.MethodGet, "/api/v1/me/tokens?limit=0", nil, "Cookie", cookie), http.StatusBadRequest, ErrorCodeValidationFailed)

	// Revoking ends the token at once.
	c.want(c.call(http.MethodDelete, revokePath, nil, "Cookie", cookie, "Origin", testOrigin), http.StatusNoContent, "")
	c.want(c.call(http.MethodGet, "/api/v1/discover", nil, "Authorization", bearer), http.StatusUnauthorized, ErrorCodeUnauthenticated)

	// Logout clears the cookie; the old cookie then counts as anonymous.
	rec = c.call(http.MethodPost, "/api/v1/auth/logout", nil, "Cookie", cookie, "Origin", testOrigin)
	c.want(rec, http.StatusNoContent, "")
	if sc := rec.Header().Get("Set-Cookie"); !strings.Contains(sc, "Max-Age=0") {
		t.Errorf("logout Set-Cookie = %q", sc)
	}
	c.want(c.call(http.MethodGet, "/api/v1/me", nil, "Cookie", cookie), http.StatusUnauthorized, ErrorCodeUnauthenticated)

	// Logging in again works even with the stale cookie still attached.
	c.want(c.call(http.MethodPost, "/api/v1/auth/login", map[string]any{"username": "alice", "password": "wrong password"}, "Cookie", cookie),
		http.StatusUnauthorized, ErrorCodeUnauthenticated)
	rec = c.call(http.MethodPost, "/api/v1/auth/login", map[string]any{"username": "Alice", "password": "correct horse"}, "Cookie", cookie)
	c.want(rec, http.StatusOK, "")
	sessionCookie(t, rec)
}

func TestAuthenticationErrors(t *testing.T) {
	c := newAPIClient(t)
	for _, tt := range []struct {
		name   string
		header []string
	}{
		{"no credentials", nil},
		{"basic auth", []string{"Authorization", "Basic YWxpY2U6eA=="}},
		{"unknown token", []string{"Authorization", "Bearer wl_pat_unknown"}},
		{"session token as bearer", []string{"Authorization", "Bearer wl_ses_whatever"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := c.call(http.MethodGet, "/api/v1/me", nil, tt.header...)
			c.want(rec, http.StatusUnauthorized, ErrorCodeUnauthenticated)
			if rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 without WWW-Authenticate")
			}
		})
	}
	// Anonymous operations stay open without credentials.
	c.want(c.call(http.MethodGet, "/api/v1/discover", nil), http.StatusOK, "")
}

func TestLoginRateLimited(t *testing.T) {
	c := newAPIClient(t)
	c.want(c.call(http.MethodPost, "/api/v1/auth/register", map[string]any{"username": "alice", "password": "correct horse"}), http.StatusCreated, "")
	for range 10 {
		c.call(http.MethodPost, "/api/v1/auth/login", map[string]any{"username": "alice", "password": "nope nope"})
	}
	c.want(c.call(http.MethodPost, "/api/v1/auth/login", map[string]any{"username": "alice", "password": "correct horse"}),
		http.StatusTooManyRequests, ErrorCodeRateLimited)
}

func TestRequireAccessToken(t *testing.T) {
	accounts := service.NewAccounts(storetest.Open(t), nil)
	ctx := t.Context()
	_, sess, err := accounts.Register(ctx, service.RegisterInput{Username: "alice", Password: "correct horse"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := accounts.Authenticate(ctx, sess.Token)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := accounts.CreateToken(ctx, p, service.CreateTokenInput{Name: "agent", Scopes: []string{"read"}})
	if err != nil {
		t.Fatal(err)
	}
	h := RequireAccessToken(accounts, auth.TrustedProxies{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	for _, tt := range []struct {
		name   string
		header [2]string
		want   int
	}{
		{"no credentials", [2]string{}, http.StatusUnauthorized},
		{"session cookie is not enough", [2]string{"Cookie", SessionCookieName + "=" + sess.Token}, http.StatusUnauthorized},
		{"bad token", [2]string{"Authorization", "Bearer wl_pat_bad"}, http.StatusUnauthorized},
		{"valid token", [2]string{"Authorization", "Bearer " + token}, http.StatusTeapot},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tt.header[0] != "" {
				req.Header.Set(tt.header[0], tt.header[1])
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body)
			}
		})
	}
}

// Every operation in the contract has access rules, and the rules match the
// spec's security declarations. The generated server names operations in Go
// style, so lookups must match those names too.
func TestOperationRules(t *testing.T) {
	doc, _ := loadSpec(t)
	n := 0
	for _, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			n++
			if _, ok := operations[operationKey(op.OperationID)]; !ok {
				t.Errorf("%s has no access rules", op.OperationID)
			}
		}
	}
	if n != len(operations) {
		t.Errorf("%d operations in the spec, %d with rules", n, len(operations))
	}
	for id, want := range map[string]operation{
		"discover":    {kind: "read", scope: "read", anonymous: true},
		"register":    {kind: "mutate", scope: "write", anonymous: true},
		"createToken": {kind: "mutate", scope: "write", sessionOnly: true},
		"logout":      {kind: "mutate", scope: "write", sessionOnly: true},
		// Admin account changes could otherwise turn a leaked admin token
		// into a login and new tokens.
		"adminCreateUser":    {kind: "mutate", scope: "write", sessionOnly: true},
		"adminUpdateUser":    {kind: "mutate", scope: "write", sessionOnly: true},
		"adminResetPassword": {kind: "mutate", scope: "write", sessionOnly: true},
		"getMe":              {kind: "read", scope: "read"},
		"revokeToken":        {kind: "mutate", scope: "write"},
	} {
		if got := operations[operationKey(id)]; got != want {
			t.Errorf("%s: rules = %+v, want %+v", id, got, want)
		}
	}
	for _, goName := range []string{"Discover", "GetMe", "ListTokens", "CreateToken", "RevokeToken", "Register", "Login", "Logout"} {
		if _, ok := operations[operationKey(goName)]; !ok {
			t.Errorf("generated name %s has no access rules", goName)
		}
	}
}
