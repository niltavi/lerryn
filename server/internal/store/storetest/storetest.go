// Package storetest gives each test its own empty database on the backend
// selected by the environment, so the same storage contract tests run on
// both SQLite and PostgreSQL:
//
//	LERRYN_TEST_DATABASE=sqlite    (default) a temporary file per test
//	LERRYN_TEST_DATABASE=postgres  a new schema per test in the database
//	                               at LERRYN_TEST_POSTGRES_DSN
//
// `make test` and `make test-pg` set these variables. Databases are removed
// when the test ends.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Environment variables read by this package.
const (
	EnvDatabase    = "LERRYN_TEST_DATABASE"
	EnvPostgresDSN = "LERRYN_TEST_POSTGRES_DSN"
)

// schemaPrefix marks schemas created here, so leftovers from an
// interrupted run are easy to find and drop.
const schemaPrefix = "lerryn_test_"

// setupTimeout bounds creating and dropping a test database.
const setupTimeout = 30 * time.Second

// Driver returns the database driver selected for this test run.
func Driver(t testing.TB) string {
	t.Helper()
	switch v := os.Getenv(EnvDatabase); v {
	case "", config.DatabaseSQLite:
		return config.DatabaseSQLite
	case config.DatabasePostgres:
		return config.DatabasePostgres
	default:
		t.Fatalf("%s=%q: want %q or %q", EnvDatabase, v, config.DatabaseSQLite, config.DatabasePostgres)
		return ""
	}
}

// NewDatabase creates an empty, isolated database and returns the config
// that points at it. It is dropped when the test ends.
func NewDatabase(t testing.TB) config.Database {
	t.Helper()
	switch Driver(t) {
	case config.DatabasePostgres:
		return newPostgresSchema(t)
	default:
		return config.Database{
			Driver: config.DatabaseSQLite,
			DSN:    filepath.Join(t.TempDir(), "lerryn.db"),
		}
	}
}

// OpenEmpty opens a new isolated database without applying migrations.
func OpenEmpty(t testing.TB) *store.DB {
	t.Helper()
	cfg := NewDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	db, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open %s test database: %v", cfg.Driver, err)
	}
	// Registered after NewDatabase's cleanup, so it runs first: the pool
	// must be closed before the schema or file is removed.
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Open opens a new isolated database with every migration applied.
func Open(t testing.TB) *store.DB {
	t.Helper()
	db := OpenEmpty(t)
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	if _, err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return db
}

func newPostgresSchema(t testing.TB) config.Database {
	t.Helper()
	dsn := os.Getenv(EnvPostgresDSN)
	if dsn == "" {
		t.Fatalf("%s=postgres requires %s", EnvDatabase, EnvPostgresDSN)
	}
	// The name is built from hex digits only, so quoting it is enough.
	schema := schemaPrefix + randomHex(t, 8)
	if err := adminExec(dsn, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatalf("create test schema (is PostgreSQL at %s reachable?): %v", EnvPostgresDSN, err)
	}
	t.Cleanup(func() {
		if err := adminExec(dsn, `DROP SCHEMA "`+schema+`" CASCADE`); err != nil {
			t.Errorf("drop test schema %s: %v", schema, err)
		}
	})
	scoped, err := withSearchPath(dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	return config.Database{Driver: config.DatabasePostgres, DSN: scoped}
}

// adminExec runs one statement on a short-lived connection, so tests do
// not hold an extra connection each while they run in parallel.
func adminExec(dsn, query string) error {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse %s: %w", EnvPostgresDSN, err)
	}
	db := stdlib.OpenDB(*cfg)
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	_, err = db.ExecContext(ctx, query)
	return err
}

// withSearchPath points every connection opened with dsn at schema. Both
// URL and keyword/value DSNs are accepted, as pgx accepts both.
func withSearchPath(dsn, schema string) (string, error) {
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", fmt.Errorf("parse %s: %w", EnvPostgresDSN, err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	return dsn + " search_path=" + schema, nil
}

func randomHex(t testing.TB, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
