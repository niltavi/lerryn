package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/buildinfo"
	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/httpapi"
)

func TestHealthz(t *testing.T) {
	rec := serve(t, New(discardLogger()), "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestReadyz(t *testing.T) {
	ok := ReadinessCheck{Name: "ok", Check: func(context.Context) error { return nil }}
	broken := ReadinessCheck{Name: "database", Check: func(context.Context) error { return errors.New("connection refused") }}

	tests := []struct {
		name       string
		checks     []ReadinessCheck
		wantStatus int
		wantFailed []string
	}{
		{name: "no checks", wantStatus: http.StatusOK},
		{name: "all pass", checks: []ReadinessCheck{ok}, wantStatus: http.StatusOK},
		{name: "one fails", checks: []ReadinessCheck{ok, broken}, wantStatus: http.StatusServiceUnavailable, wantFailed: []string{"database"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, New(discardLogger(), tt.checks...), "/readyz")
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			var body struct {
				Failed []string `json:"failed"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body.Failed) != len(tt.wantFailed) || (len(body.Failed) > 0 && body.Failed[0] != tt.wantFailed[0]) {
				t.Fatalf("failed = %v, want %v", body.Failed, tt.wantFailed)
			}
		})
	}
}

func TestAPIIsMounted(t *testing.T) {
	oidc := config.Default()
	oidc.Server.PublicURL = "https://lerryn.example.com/"
	oidc.Auth.OIDC = config.OIDC{Issuer: "https://sso.example.com", ClientID: "lerryn"}
	oidc.Runner.AllowedNetworks = []string{config.NetworkNone}

	tests := []struct {
		name         string
		server       *Server
		wantConsole  string
		wantLogin    []httpapi.LoginMethod
		wantNetworks []httpapi.NetworkPolicy
	}{
		{
			name:         "defaults",
			server:       New(discardLogger()),
			wantConsole:  "http://localhost:33333/",
			wantLogin:    []httpapi.LoginMethod{httpapi.LoginMethodPassword},
			wantNetworks: []httpapi.NetworkPolicy{httpapi.NetworkPolicyNone, httpapi.NetworkPolicyFull},
		},
		{
			name:         "configured",
			server:       New(discardLogger()).WithConfig(oidc),
			wantConsole:  "https://lerryn.example.com/",
			wantLogin:    []httpapi.LoginMethod{httpapi.LoginMethodPassword, httpapi.LoginMethodOidc},
			wantNetworks: []httpapi.NetworkPolicy{httpapi.NetworkPolicyNone},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, tt.server, "/api/v1/discover")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body)
			}
			var got httpapi.Discovery
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if got.Server.Version != buildinfo.Version || got.Links.Console != tt.wantConsole {
				t.Errorf("server = %+v, links = %+v", got.Server, got.Links)
			}
			if !slices.Equal(got.Capabilities.LoginMethods, tt.wantLogin) {
				t.Errorf("login_methods = %v, want %v", got.Capabilities.LoginMethods, tt.wantLogin)
			}
			if !slices.Equal(got.Capabilities.NetworkPolicies, tt.wantNetworks) {
				t.Errorf("network_policies = %v, want %v", got.Capabilities.NetworkPolicies, tt.wantNetworks)
			}
		})
	}
}

// Routes outside the API keep their plain JSON responses; unknown API
// paths get a problem from the API handler.
func TestAPIOnlyOwnsItsPrefix(t *testing.T) {
	s := New(discardLogger())
	if rec := serve(t, s, "/healthz"); rec.Header().Get(httpapi.RequestIDHeader) != "" {
		t.Error("healthz went through the API handler")
	}
	rec := serve(t, s, "/api/v1/unknown")
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Errorf("status = %d, Content-Type = %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// The console is mounted at / and answers console routes, but the API,
// probes and /mcp keep their own handlers.
func TestConsoleDoesNotShadowOtherRoutes(t *testing.T) {
	s := New(discardLogger())
	for _, p := range []string{"/", "/runs/0192f3a1-7c2b-7d4e-9a1b-3c4d5e6f7a8b", "/settings/tokens"} {
		rec := serve(t, s, p)
		// 200 with the embedded build, 503 in a Go-only build without it.
		if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d", p, rec.Code)
		}
		if rec.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: console page without Content-Security-Policy", p)
		}
	}
	if rec := serve(t, s, "/healthz"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Errorf("/healthz: status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if rec := serve(t, s, "/api/v1/discover"); rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("/api/v1/discover: Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if rec := serve(t, s, "/api/v2/discover"); rec.Code != http.StatusNotFound || rec.Header().Get("Content-Security-Policy") != "" {
		t.Errorf("/api/v2/discover: status = %d, served by the console", rec.Code)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(discardLogger()).Run(ctx, ln) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/healthz") // #nosec G107 -- local test listener
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func serve(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
