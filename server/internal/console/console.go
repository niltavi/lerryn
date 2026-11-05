// Package console serves the web console, the single-page app built from
// apps/web, from files embedded in the binary (tech design 13.3), so every
// server replica can serve it without a separate web server.
//
// `make web` builds the app and copies it into webdist/. A Go-only build
// embeds just webdist/.keep; the handler then answers console pages with a
// short notice instead of failing, so Go workflows and tests do not need
// Node.
package console

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:webdist
var embedded embed.FS

// Paths owned by other handlers. The root mux routes them away already;
// these checks keep a request that slips through (for example /mcp when MCP
// is off, /metrics without a token, or an unknown API version) from getting
// the console page.
var reserved = []string{"/api/", "/mcp", "/healthz", "/readyz", "/metrics"}

// contentSecurityPolicy allows only the console's own origin. Styles allow
// 'unsafe-inline' because components set style attributes; scripts do not.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; object-src 'none'; " +
	"base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

const (
	// Vite puts content-hashed files under assets/, so they never change.
	immutableCache = "public, max-age=31536000, immutable"
	// Other static files keep their name across releases.
	shortCache = "public, max-age=3600"
	// The page itself must be revalidated, so a new release is picked up.
	noCache = "no-cache"
)

type file struct {
	body []byte
	etag string
}

// Handler serves the console. It is safe to mount at "/".
type Handler struct {
	files map[string]file
	index *file
}

// New returns a handler over the embedded build.
func New() *Handler {
	sub, err := fs.Sub(embedded, "webdist")
	if err != nil {
		// The embed pattern guarantees the directory exists.
		panic(err)
	}
	return newHandler(sub)
}

func newHandler(fsys fs.FS) *Handler {
	h := &Handler{files: map[string]file{}}
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(path.Base(p), ".") {
			return nil
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(body)
		h.files["/"+p] = file{body: body, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
		return nil
	})
	if f, ok := h.files["/index.html"]; ok {
		h.index = &f
	}
	return h
}

// Built reports whether the console build is embedded.
func (h *Handler) Built() bool { return h.index != nil }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	for _, prefix := range reserved {
		if p == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(p, prefix) {
			http.NotFound(w, r)
			return
		}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")

	clean := path.Clean("/" + p)
	if clean != "/index.html" {
		if f, ok := h.files[clean]; ok {
			cache := shortCache
			if strings.HasPrefix(clean, "/assets/") {
				cache = immutableCache
			}
			h.serve(w, r, clean, f, cache)
			return
		}
	}
	// A missing file with an extension is a real 404; answering it with the
	// page would hide broken asset links behind an HTML response.
	if path.Ext(clean) != "" && clean != "/index.html" {
		http.NotFound(w, r)
		return
	}
	h.servePage(w, r)
}

// servePage answers every console route with index.html; the app's router
// picks the page from the URL.
func (h *Handler) servePage(w http.ResponseWriter, r *http.Request) {
	hdr := w.Header()
	hdr.Set("Content-Security-Policy", contentSecurityPolicy)
	hdr.Set("X-Frame-Options", "DENY")
	hdr.Set("Referrer-Policy", "same-origin")
	hdr.Set("Cross-Origin-Opener-Policy", "same-origin")
	hdr.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if h.index == nil {
		hdr.Set("Content-Type", "text/plain; charset=utf-8")
		hdr.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("The web console is not part of this build. Run `make web` before `go build`, or use `make build`.\n"))
		return
	}
	h.serve(w, r, "/index.html", *h.index, noCache)
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request, name string, f file, cache string) {
	hdr := w.Header()
	hdr.Set("Cache-Control", cache)
	hdr.Set("ETag", f.etag)
	if ctype := mime.TypeByExtension(path.Ext(name)); ctype != "" {
		hdr.Set("Content-Type", ctype)
	}
	// ServeContent answers If-None-Match with 304 and handles HEAD and ranges.
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(f.body))
}
