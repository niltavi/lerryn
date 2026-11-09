package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/niltavi/lerryn/skills"
)

func TestDiscover(t *testing.T) {
	tests := []struct {
		name          string
		opts          Options
		host          string
		header        http.Header
		wantConsole   string
		wantBuildDate *time.Time
		wantLogin     []LoginMethod
		wantNetworks  []NetworkPolicy
	}{
		{
			name: "release build",
			opts: Options{
				PublicURL: "https://lerryn.example.com",
				Version:   "v0.1.0",
				Commit:    "4a78fd0",
				BuildDate: "2026-11-02T08:00:00Z",
				Capabilities: Capabilities{
					LoginMethods:              []LoginMethod{LoginMethodPassword, LoginMethodOidc},
					RegistrationOpen:          true,
					NetworkPolicies:           []NetworkPolicy{NetworkPolicyNone, NetworkPolicyFull},
					RequireTestBeforeActivate: true,
				},
			},
			wantConsole:   "https://lerryn.example.com/",
			wantBuildDate: new(time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)),
			wantLogin:     []LoginMethod{LoginMethodPassword, LoginMethodOidc},
			wantNetworks:  []NetworkPolicy{NetworkPolicyNone, NetworkPolicyFull},
		},
		{
			name: "dev build under a path prefix with empty capabilities",
			opts: Options{
				PublicURL: "https://intranet.example.com/lerryn/",
				Version:   "dev",
				Commit:    "none",
				BuildDate: "unknown",
			},
			wantConsole:  "https://intranet.example.com/lerryn/",
			wantLogin:    []LoginMethod{},
			wantNetworks: []NetworkPolicy{},
		},
		{
			// Opened through a tunnel in front of a LAN deployment: the
			// console's link is the address it was opened at.
			name: "extra origin",
			opts: Options{
				PublicURL:    "http://192.168.50.50:33333/lerryn/",
				ExtraOrigins: []string{"https://Lerryn.Example.com"},
				Version:      "dev",
				Commit:       "none",
			},
			host:         "lerryn.example.com",
			wantConsole:  "https://lerryn.example.com/lerryn/",
			wantLogin:    []LoginMethod{},
			wantNetworks: []NetworkPolicy{},
		},
		{
			// A Host that is none of the deployment's origins gets
			// public_url, so the console warns about it.
			name: "unknown host",
			opts: Options{
				PublicURL:    "http://192.168.50.50:33333",
				ExtraOrigins: []string{"https://lerryn.example.com"},
				Version:      "dev",
				Commit:       "none",
			},
			host:         "evil.example.com",
			wantConsole:  "http://192.168.50.50:33333/",
			wantLogin:    []LoginMethod{},
			wantNetworks: []NetworkPolicy{},
		},
		{
			// A stale session cookie is ignored, so discover still answers
			// anonymously; principal resolution is covered in accounts_test.go.
			name:         "stale session cookie",
			opts:         Options{PublicURL: "http://localhost:8080", Version: "dev", Commit: "none"},
			header:       http.Header{"Cookie": {SessionCookieName + "=wl_ses_expired"}},
			wantConsole:  "http://localhost:8080/",
			wantLogin:    []LoginMethod{},
			wantNetworks: []NetworkPolicy{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/discover", nil)
			if tt.host != "" {
				req.Host = tt.host
			}
			for k, v := range tt.header {
				req.Header[k] = v
			}
			rec := httptest.NewRecorder()
			New(tt.opts).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
			if id := rec.Header().Get(RequestIDHeader); !strings.HasPrefix(id, "req_") {
				t.Errorf("%s = %q", RequestIDHeader, id)
			}
			assertMatchesSpec(t, req, rec)

			// The generated struct cannot show whether a nullable field was
			// sent as null or left out, so look at the raw JSON as well.
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatal(err)
			}
			if got := string(raw["principal"]); got != "null" {
				t.Errorf("principal = %s, want null", got)
			}

			var got Discovery
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Server.Version != tt.opts.Version || got.Server.Commit != tt.opts.Commit {
				t.Errorf("server = %+v", got.Server)
			}
			if !equalTime(got.Server.BuildDate, tt.wantBuildDate) {
				t.Errorf("build_date = %v, want %v", got.Server.BuildDate, tt.wantBuildDate)
			}
			if got.API != (APIInfo{Version: "v1", SpecVersion: SpecVersion}) {
				t.Errorf("api = %+v", got.API)
			}
			if got.Links.Console != tt.wantConsole {
				t.Errorf("links.console = %q, want %q", got.Links.Console, tt.wantConsole)
			}
			var skillNames []string
			for _, s := range got.Skills.Items {
				if s.Description == "" {
					t.Errorf("skill %s has no description", s.Name)
				}
				skillNames = append(skillNames, s.Name)
			}
			if got.Skills.Version != tt.opts.Version || !slices.Equal(skillNames, skills.Names) {
				t.Errorf("skills = %+v", got.Skills)
			}
			var runtimes []string
			for _, r := range got.Runtimes {
				lock := "-"
				if r.Lockfile != nil {
					lock = *r.Lockfile
				}
				runtimes = append(runtimes, r.Language+" "+r.Version+" "+lock)
			}
			if want := []string{"python 3.12 uv.lock", "node 22 package-lock.json", "shell 5 -"}; !slices.Equal(runtimes, want) {
				t.Errorf("runtimes = %q, want %q", runtimes, want)
			}
			if !slices.Equal(got.Capabilities.LoginMethods, tt.wantLogin) ||
				!slices.Equal(got.Capabilities.NetworkPolicies, tt.wantNetworks) ||
				got.Capabilities.RegistrationOpen != tt.opts.Capabilities.RegistrationOpen ||
				got.Capabilities.RequireTestBeforeActivate != tt.opts.Capabilities.RequireTestBeforeActivate {
				t.Errorf("capabilities = %+v", got.Capabilities)
			}
		})
	}
}

func TestGetEnvironmentAccessAndDefault(t *testing.T) {
	c := newAPIClient(t)
	const path = "/api/v1/environment"
	c.want(c.call(http.MethodGet, path, nil), http.StatusUnauthorized, ErrorCodeUnauthenticated)

	rec := c.call(http.MethodPost, "/api/v1/auth/register", map[string]any{"username": "alice", "password": "correct horse"})
	c.want(rec, http.StatusCreated, "")
	cookie := sessionCookie(t, rec)
	for _, tt := range []struct {
		scope  string
		status int
		code   ErrorCode
	}{
		{"read", http.StatusOK, ""},
		{"write", http.StatusForbidden, ErrorCodeInsufficientScope},
	} {
		t.Run(tt.scope, func(t *testing.T) {
			tokenReq := map[string]any{"name": tt.scope, "scopes": []string{tt.scope}}
			rec := c.call(http.MethodPost, "/api/v1/me/tokens", tokenReq, "Cookie", cookie, "Origin", testOrigin)
			c.want(rec, http.StatusCreated, "")
			token := decode[CreatedToken](t, rec).Token

			rec = c.call(http.MethodGet, path, nil, "Authorization", "Bearer "+token)
			c.want(rec, tt.status, tt.code)
			if tt.status == http.StatusOK {
				if got := decode[EnvironmentInfo](t, rec).Environment; got != EnvironmentInfoEnvironmentUnspecified {
					t.Errorf("environment = %q, want unspecified", got)
				}
			}
		})
	}
}

// The handler must not hand out its own slices: a caller mutating one
// response would otherwise change what later requests see.
func TestDiscoverDoesNotAliasOptions(t *testing.T) {
	h := &handler{capabilities: Capabilities{NetworkPolicies: []NetworkPolicy{NetworkPolicyNone}}}
	c := h.cloneCapabilities()
	c.NetworkPolicies[0] = NetworkPolicyFull
	if h.capabilities.NetworkPolicies[0] != NetworkPolicyNone {
		t.Fatal("cloneCapabilities shares the backing array")
	}
}

func equalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
