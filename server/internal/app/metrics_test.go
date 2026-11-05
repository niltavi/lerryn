package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/metrics"
)

func TestMetricsMountedBehindToken(t *testing.T) {
	m := metrics.New()
	h := New(discardLogger()).WithMetrics(m, true, "scrape-token").Handler()

	get := func(path, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	get("/healthz", "")
	get("/api/v1/discover", "")
	get("/api/v1/tasks/0190f5a4-0000-7000-8000-000000000000", "")
	get("/api/v1/no/such/path", "")
	get("/wp-login.php", "")

	if rec := get("/metrics", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("/metrics without token: status %d", rec.Code)
	}
	rec := get("/metrics", "Bearer scrape-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics with token: status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`operation="healthz"`,
		`operation="discover"`,
		`operation="getTask"`,
		`code="401",operation="metrics"`,
		`code="404",operation="unknown"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
	// Paths with IDs must not become labels.
	if strings.Contains(body, "0190f5a4") || strings.Contains(body, "wp-login") {
		t.Error("a request path leaked into a label")
	}
}

func TestMetricsNotMountedByDefault(t *testing.T) {
	for name, s := range map[string]*Server{
		"no metrics":        New(discardLogger()),
		"separate listener": New(discardLogger()).WithMetrics(metrics.New(), false, ""),
		"no token":          New(discardLogger()).WithMetrics(metrics.New(), true, ""),
	} {
		t.Run(name, func(t *testing.T) {
			if rec := serve(t, s, "/metrics"); rec.Code != http.StatusNotFound {
				t.Fatalf("/metrics status = %d, want 404", rec.Code)
			}
		})
	}
}
