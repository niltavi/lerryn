package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/niltavi/lerryn/server/internal/config"
)

// TestM0CompletionCriterion is the end-to-end check from the implementation
// plan's M0: on a freshly built server, register a built-in account, create
// an access token, and call discover with it.
func TestM0CompletionCriterion(t *testing.T) {
	cfg := config.Default()
	dir := t.TempDir()
	cfg.Database.DSN = filepath.Join(dir, "lerryn.db")
	cfg.Storage.Path = filepath.Join(dir, "storage")
	cfg.Secrets.MasterKeyFile = filepath.Join(dir, "master.key")
	st, err := buildServer(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.db.Close() })
	srv := st.srv
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// call sends a JSON request, checks the status, decodes the body into v
	// when it is not nil, and returns the cookies the response set.
	call := func(method, path string, body any, header map[string]string, want int, v any) []*http.Cookie {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&buf).Encode(body); err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequest(method, ts.URL+path, &buf)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		for k, val := range header {
			req.Header.Set(k, val)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != want {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s %s = %d, want %d: %s", method, path, resp.StatusCode, want, b)
		}
		if v != nil {
			if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
				t.Fatal(err)
			}
		}
		return resp.Cookies()
	}

	// 1. Register: username and password only.
	var session *http.Cookie
	for _, c := range call(http.MethodPost, "/api/v1/auth/register",
		map[string]string{"username": "alice", "password": "correct horse"}, nil, http.StatusCreated, nil) {
		if c.Name == "lerryn_session" {
			session = c
		}
	}
	if session == nil {
		t.Fatal("registration did not log in")
	}

	// 2. Create an access token from the console session.
	var created struct {
		Token string `json:"token"`
	}
	call(http.MethodPost, "/api/v1/me/tokens",
		map[string]any{"name": "claude-code", "scopes": []string{"read", "write", "execute"}},
		map[string]string{"Cookie": session.Name + "=" + session.Value, "Origin": cfg.Server.PublicURL},
		http.StatusCreated, &created)

	// 3. Call discover with the token.
	var disc struct {
		Principal *struct {
			Username   string   `json:"username"`
			IsAdmin    bool     `json:"is_admin"`
			AuthMethod string   `json:"auth_method"`
			Scopes     []string `json:"scopes"`
		} `json:"principal"`
	}
	call(http.MethodGet, "/api/v1/discover", nil, map[string]string{"Authorization": "Bearer " + created.Token}, http.StatusOK, &disc)
	if p := disc.Principal; p == nil || p.Username != "alice" || !p.IsAdmin || p.AuthMethod != "access_token" || len(p.Scopes) != 3 {
		t.Fatalf("principal = %+v", disc.Principal)
	}

	// The same token opens /mcp; without it, /mcp answers 401.
	call(http.MethodPost, "/mcp", map[string]any{}, nil, http.StatusUnauthorized, nil)
}
