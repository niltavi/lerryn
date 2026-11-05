// Package store is the data access layer. It opens PostgreSQL or SQLite
// through Bun, applies the embedded goose migrations and defines the table
// models.
//
// Queries in this package and its callers are written once with Bun's
// query builder and must run unchanged on both databases. The few queries
// that cannot be portable live in the dialect subpackage. Run the
// contract tests on both databases with `make test` and `make test-pg`.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/store/dialect"
)

// DB is a Bun database bound to one dialect.
type DB struct {
	*bun.DB
	dialect dialect.Dialect
	// writeGate serialises WriteTx within the process on databases with a
	// single writer; nil when the database has row locks.
	writeGate chan struct{}
}

// Open connects to the database described by cfg and verifies the session
// settings. It does not apply migrations; see Migrate.
func Open(ctx context.Context, cfg config.Database) (*DB, error) {
	d, err := dialect.For(cfg.Driver)
	if err != nil {
		return nil, err
	}
	sqldb, err := d.OpenDB(cfg.DSN)
	if err != nil {
		return nil, err
	}
	if !d.SingleWriter() {
		// database/sql keeps two idle connections by default. Under load
		// every further request then opens and closes a connection of its
		// own: the capacity test ran the client host out of ephemeral
		// ports at 50 concurrent runs. Keep the pool warm, and bounded so
		// that several servers stay within max_connections.
		n := cfg.MaxOpenConns
		if n == 0 {
			n = config.DefaultPostgresMaxOpenConns
		}
		sqldb.SetMaxOpenConns(n)
		sqldb.SetMaxIdleConns(n)
		sqldb.SetConnMaxIdleTime(5 * time.Minute)
	}
	if err := sqldb.PingContext(ctx); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("connect to %s database: %w", d.Name(), err)
	}
	if err := d.Check(ctx, sqldb); err != nil {
		_ = sqldb.Close()
		return nil, err
	}
	db := &DB{DB: bun.NewDB(sqldb, d.Bun()), dialect: d}
	if d.SingleWriter() {
		db.writeGate = make(chan struct{}, 1)
	}
	return db, nil
}

// Native returns the dialect the database was opened with, which holds
// the database-specific queries. It is not called Dialect because that
// would shadow bun.DB.Dialect and stop *DB from satisfying bun.IDB.
func (db *DB) Native() dialect.Dialect { return db.dialect }

// WriteTx runs fn in a read-write transaction and commits it unless fn
// returns an error. It is the entry point for write transactions on hot
// paths such as claiming runs and appending logs (tech design 6.1).
//
// On SQLite, write transactions of this process first queue on an
// in-process gate and only then BEGIN IMMEDIATE. Without the gate,
// writers that lose the race for the database lock sleep in SQLite's busy
// handler with growing back-off and are often still asleep when the lock
// is released; S1 measured an order of magnitude less throughput and
// large swings. busy_timeout remains the fallback for writers in other
// processes, such as `lerryn migrate`. On PostgreSQL the transaction
// begins at once and row locks do the ordering.
//
// fn must not call WriteTx itself: the gate is not reentrant.
func (db *DB) WriteTx(ctx context.Context, fn func(ctx context.Context, tx bun.Tx) error) error {
	if db.writeGate != nil {
		select {
		case db.writeGate <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		defer func() { <-db.writeGate }()
	}
	return db.RunInTx(ctx, nil, fn)
}

// NewID returns a new UUIDv7 primary key. UUIDv7 is time-ordered, so
// inserts stay index-friendly and IDs work as pagination cursors.
func NewID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

// Now returns the current UTC time truncated to microseconds, the
// precision both databases store. Writing truncated values means a row
// read back compares equal to what was written.
func Now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// IsUniqueViolation reports whether err comes from a unique constraint.
func IsUniqueViolation(err error) bool { return dialect.IsUniqueViolation(err) }

// IsForeignKeyViolation reports whether err comes from a foreign key.
func IsForeignKeyViolation(err error) bool { return dialect.IsForeignKeyViolation(err) }

// AcquireLease takes the named lease for holder, or extends it if holder
// already has it. It returns ok=false while another holder's lease is
// live. Call it again before expiresAt to keep the lease; on PostgreSQL
// expiry follows the database clock (tech design 4.4).
func (db *DB) AcquireLease(ctx context.Context, name, holder string, ttl time.Duration) (expiresAt time.Time, ok bool, err error) {
	if name == "" || holder == "" {
		return time.Time{}, false, errors.New("acquire lease: name and holder are required")
	}
	if ttl <= 0 {
		return time.Time{}, false, fmt.Errorf("acquire lease: ttl must be positive, got %s", ttl)
	}
	return db.dialect.AcquireLease(ctx, db.DB, dialect.LeaseRequest{
		ID:     NewID().String(),
		Name:   name,
		Holder: holder,
		TTL:    ttl,
		Now:    Now(),
	})
}

// ReleaseLease gives up the named lease if holder still has it, so another
// holder can take it without waiting for expiry.
func (db *DB) ReleaseLease(ctx context.Context, name, holder string) error {
	_, err := db.NewDelete().Model((*Lease)(nil)).
		Where("name = ?", name).
		Where("holder = ?", holder).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("release lease %s: %w", name, err)
	}
	return nil
}
