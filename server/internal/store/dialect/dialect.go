// Package dialect holds everything that differs between PostgreSQL and
// SQLite: driver setup, error classification, and the few hot-path
// queries that cannot be written once with Bun (tech design 6.2). Code
// outside this package writes portable Bun queries only.
package dialect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/schema"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Driver names, matching config.Database.Driver.
const (
	PostgresName = "postgres"
	SQLiteName   = "sqlite"
)

// Dialect is implemented by Postgres and SQLite.
type Dialect interface {
	// Name returns PostgresName or SQLiteName.
	Name() string
	// Bun returns the Bun dialect used to format queries.
	Bun() schema.Dialect
	// Goose returns the goose dialect used to run migrations.
	Goose() goose.Dialect
	// OpenDB opens a connection pool with the session settings lerryn
	// relies on. It does not connect; call Check after a successful ping.
	OpenDB(dsn string) (*sql.DB, error)
	// Check verifies that the session settings took effect.
	Check(ctx context.Context, db *sql.DB) error
	// MigrationLocker returns the lock that serialises concurrent
	// migrations, or nil when the database needs none.
	MigrationLocker(ctx context.Context, db *sql.DB) (lock.SessionLocker, error)
	// AcquireLease takes the named lease for req.Holder if it is free or
	// expired, or extends it if req.Holder already has it. It reports
	// false when another holder has a live lease.
	AcquireLease(ctx context.Context, db bun.IDB, req LeaseRequest) (expiresAt time.Time, ok bool, err error)
	// SingleWriter reports whether the database admits one writer at a
	// time, so that write transactions should queue inside the process
	// rather than on the database lock (tech design 6.1).
	SingleWriter() bool
	// LockAccounts blocks, until the end of the transaction db belongs to,
	// every other transaction that calls it. Account creation takes it
	// before counting users, so that two first accounts created at once
	// cannot both see an empty deployment and both become admins.
	LockAccounts(ctx context.Context, db bun.IDB) error
	// Bytewise returns expr compared byte by byte, so ORDER BY on text sorts
	// the same in both databases whatever the PostgreSQL locale.
	Bytewise(expr string) string

	RunQueue
	Introspector
}

// LeaseRequest is the input of Dialect.AcquireLease.
type LeaseRequest struct {
	// ID is the primary key used if the lease row does not exist yet.
	ID     string
	Name   string
	Holder string
	TTL    time.Duration
	// Now is the application clock. PostgreSQL ignores it and uses the
	// database clock so that servers with skewed clocks agree.
	Now time.Time
}

// For returns the dialect for a config.Database driver name.
func For(driver string) (Dialect, error) {
	switch driver {
	case PostgresName:
		return Postgres{}, nil
	case SQLiteName:
		return SQLite{}, nil
	default:
		return nil, fmt.Errorf("unknown database driver %q", driver)
	}
}

// IsUniqueViolation reports whether err comes from a unique index or
// primary key conflict on either database.
func IsUniqueViolation(err error) bool {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code == "23505"
	}
	if liteErr, ok := errors.AsType[*sqlite.Error](err); ok {
		code := liteErr.Code()
		return code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
	}
	return false
}

// IsForeignKeyViolation reports whether err comes from a foreign key
// constraint on either database.
func IsForeignKeyViolation(err error) bool {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code == "23503"
	}
	if liteErr, ok := errors.AsType[*sqlite.Error](err); ok {
		return liteErr.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY
	}
	return false
}

// scanLease reads the expiry returned by a lease query; no row means the
// lease belongs to someone else.
func scanLease(ctx context.Context, q *bun.RawQuery) (time.Time, bool, error) {
	var expiresAt time.Time
	if err := q.Scan(ctx, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("lease: %w", err)
	}
	return expiresAt, true, nil
}
