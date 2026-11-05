package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/niltavi/lerryn/server/internal/config"
)

// buildServer migrates a fresh database, so the server is ready right away.
func TestBuildServerMigratesAndIsReady(t *testing.T) {
	cfg := config.Default()
	dir := t.TempDir()
	cfg.Database.DSN = filepath.Join(dir, "lerryn.db")
	cfg.Storage.Path = filepath.Join(dir, "storage")
	cfg.Secrets.MasterKeyFile = filepath.Join(dir, "master.key")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	st, err := buildServer(context.Background(), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.db.Close() })
	srv := st.srv

	for _, path := range []string{"/readyz", "/api/v1/discover"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d: %s", path, rec.Code, rec.Body)
		}
	}
}
