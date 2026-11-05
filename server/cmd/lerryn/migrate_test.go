package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

func TestMigrateIsRepeatable(t *testing.T) {
	db := storetest.NewDatabase(t)
	t.Setenv("LERRYN_LOG_LEVEL", "error")
	t.Setenv("LERRYN_DATABASE_DRIVER", db.Driver)
	t.Setenv("LERRYN_DATABASE_DSN", db.DSN)

	if out := runMigrateCmd(t); strings.HasPrefix(out, "applied 0 ") {
		t.Fatalf("first run applied nothing: %q", out)
	}
	if out := runMigrateCmd(t); !strings.HasPrefix(out, "applied 0 ") {
		t.Fatalf("second run should apply nothing: %q", out)
	}
	assertMigrated(t, db)
}

func TestMigrateReadsConfigFile(t *testing.T) {
	db := storetest.NewDatabase(t)
	t.Setenv("LERRYN_LOG_LEVEL", "error")
	path := filepath.Join(t.TempDir(), "lerryn.yaml")
	yaml := "database:\n  driver: " + db.Driver + "\n  dsn: '" + db.DSN + "'\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	runMigrateCmd(t, "--config", path)
	assertMigrated(t, db)
}

func TestMigrateReportsConnectionErrors(t *testing.T) {
	t.Setenv("LERRYN_LOG_LEVEL", "error")
	t.Setenv("LERRYN_DATABASE_DRIVER", "sqlite")
	t.Setenv("LERRYN_DATABASE_DSN", filepath.Join(t.TempDir(), "missing-dir", "lerryn.db"))
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"migrate"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for a database in a missing directory")
	}
}

func assertMigrated(t *testing.T, cfg config.Database) {
	t.Helper()
	db, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := store.CheckMigrations(context.Background(), db); err != nil {
		t.Fatalf("schema not current after migrate: %v", err)
	}
}

func runMigrateCmd(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetArgs(append(args, "migrate"))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("lerryn %v migrate: %v", args, err)
	}
	return out.String()
}
