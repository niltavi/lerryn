package console

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":          {Data: []byte("<!doctype html><title>Lerryn</title>")},
		"favicon.svg":         {Data: []byte("<svg/>")},
		"assets/index-ab1.js": {Data: []byte("console.log(1)")},
		"assets/app-cd2.css":  {Data: []byte("body{}")},
		".keep":               {Data: nil},
	}
}

func do(h http.Handler, method, target string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServesFilesAndFallsBackToThePage(t *testing.T) {
	h := newHandler(testFS())
	tests := []struct {
		name, method, path string
		status             int
		body               string
		cache              string
		ctype              string
		csp                bool
	}{
		{"root", http.MethodGet, "/", 200, "<!doctype html>", noCache, "text/html", true},
		{"index by name", http.MethodGet, "/index.html", 200, "<!doctype html>", noCache, "text/html", true},
		{"client route", http.MethodGet, "/runs/0192f3a1-7c2b-7d4e-9a1b-3c4d5e6f7a8b", 200, "<!doctype html>", noCache, "text/html", true},
		{"nested client route", http.MethodGet, "/settings/tokens?new=1", 200, "<!doctype html>", noCache, "text/html", true},
		{"hashed asset", http.MethodGet, "/assets/index-ab1.js", 200, "console.log(1)", immutableCache, "javascript", false},
		{"stylesheet", http.MethodGet, "/assets/app-cd2.css", 200, "body{}", immutableCache, "text/css", false},
		{"static file", http.MethodGet, "/favicon.svg", 200, "<svg/>", shortCache, "image/svg+xml", false},
		{"head", http.MethodHead, "/", 200, "", noCache, "text/html", true},
		{"missing asset is not the page", http.MethodGet, "/assets/missing-00.js", 404, "", "", "", false},
		{"dotfiles stay hidden", http.MethodGet, "/.keep", 404, "", "", "", false},
		{"traversal stays inside", http.MethodGet, "/assets/../../etc/hosts.txt", 404, "", "", "", false},
		{"traversal without extension gets the page", http.MethodGet, "/../../etc/passwd", 200, "<!doctype html>", noCache, "text/html", true},
		{"writes are refused", http.MethodPost, "/", 405, "", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(h, tt.method, tt.path)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if tt.body != "" && !strings.HasPrefix(rec.Body.String(), tt.body) {
				t.Errorf("body = %q, want prefix %q", rec.Body.String(), tt.body)
			}
			if got := rec.Header().Get("Cache-Control"); tt.cache != "" && got != tt.cache {
				t.Errorf("Cache-Control = %q, want %q", got, tt.cache)
			}
			if got := rec.Header().Get("Content-Type"); !strings.Contains(got, tt.ctype) {
				t.Errorf("Content-Type = %q, want %q", got, tt.ctype)
			}
			if got := rec.Header().Get("Content-Security-Policy"); (got != "") != tt.csp {
				t.Errorf("Content-Security-Policy = %q, want present = %v", got, tt.csp)
			}
			if rec.Code == 200 && rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Error("missing X-Content-Type-Options: nosniff")
			}
		})
	}
}

// The page must not be framed or load scripts from elsewhere.
func TestPageSecurityHeaders(t *testing.T) {
	rec := do(newHandler(testFS()), http.MethodGet, "/")
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'", "object-src 'none'", "base-uri 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(strings.SplitN(strings.SplitN(csp, "script-src", 2)[1], ";", 2)[0], "unsafe") {
		t.Errorf("script-src allows unsafe sources: %q", csp)
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("missing X-Frame-Options: DENY")
	}
}

func TestRevalidatesWithETag(t *testing.T) {
	h := newHandler(testFS())
	first := do(h, http.MethodGet, "/")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	if rec := do(h, http.MethodGet, "/tasks/x", "If-None-Match", etag); rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
}

// Paths owned by the API, MCP and probes never get the console page, even
// when their handler is not mounted.
func TestLeavesReservedPathsAlone(t *testing.T) {
	h := newHandler(testFS())
	for _, p := range []string{"/api/v1/unknown", "/api/v2/runs", "/api/", "/mcp", "/mcp/x", "/healthz", "/readyz", "/metrics"} {
		if rec := do(h, http.MethodGet, p); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "<!doctype") {
			t.Errorf("%s: status = %d, body = %q", p, rec.Code, rec.Body.String())
		}
	}
	// A route that only starts with a reserved word is a console route.
	if rec := do(h, http.MethodGet, "/apis"); rec.Code != http.StatusOK {
		t.Errorf("/apis: status = %d", rec.Code)
	}
}

// A Go-only build has no index.html; console routes say so instead of
// failing, and the binary still builds and serves the API.
func TestWithoutBuild(t *testing.T) {
	h := newHandler(fstest.MapFS{".keep": {}})
	if h.Built() {
		t.Fatal("Built() = true without index.html")
	}
	rec := do(h, http.MethodGet, "/runs/x")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "make web") {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestEmbeddedBuildLoads(t *testing.T) {
	// Whatever webdist holds (only .keep in a Go-only checkout), New must not panic.
	_ = New()
}
