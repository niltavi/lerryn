package dialect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/schema"
)

// Postgres is the PostgreSQL dialect, driven by pgx through database/sql.
type Postgres struct{}

var _ Dialect = Postgres{}

// Name implements Dialect.
func (Postgres) Name() string { return PostgresName }

// Bun implements Dialect.
func (Postgres) Bun() schema.Dialect { return pgdialect.New() }

// Goose implements Dialect.
func (Postgres) Goose() goose.Dialect { return goose.DialectPostgres }

// OpenDB implements Dialect.
func (Postgres) OpenDB(dsn string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// pgx masks the password in parse errors.
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}
	// Bun formats every argument into the SQL text itself and sends no
	// bind parameters. Under pgx's default mode each distinct text would
	// be prepared and cached as a server-side statement; the simple
	// protocol sends it in one round trip instead.
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	// Bun escapes string literals by doubling quotes only, which is safe
	// only while backslashes are literal.
	cfg.RuntimeParams["standard_conforming_strings"] = "on"
	cfg.RuntimeParams["timezone"] = "UTC"
	if cfg.RuntimeParams["application_name"] == "" {
		cfg.RuntimeParams["application_name"] = "lerryn"
	}
	return stdlib.OpenDB(*cfg), nil
}

// Check implements Dialect. A connection pooler can drop startup
// parameters, so the escaping precondition is verified, not assumed.
func (Postgres) Check(ctx context.Context, db *sql.DB) error {
	var scs string
	if err := db.QueryRowContext(ctx, "SHOW standard_conforming_strings").Scan(&scs); err != nil {
		return fmt.Errorf("read standard_conforming_strings: %w", err)
	}
	if scs != "on" {
		return fmt.Errorf("postgres: standard_conforming_strings is %q, lerryn requires on", scs)
	}
	return nil
}

// MigrationLocker implements Dialect with a session advisory lock, so that
// several servers starting at once apply migrations one at a time
// (tech design 15.4). The lock ID is derived from the current schema:
// advisory locks are per database, and a fixed ID would serialise
// unrelated schemas, such as parallel test schemas. The original lock
// namespace stays stable so a product rename cannot bypass existing locks.
func (Postgres) MigrationLocker(ctx context.Context, db *sql.DB) (lock.SessionLocker, error) {
	var current sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT current_schema()").Scan(&current); err != nil {
		return nil, fmt.Errorf("read current schema: %w", err)
	}
	if !current.Valid {
		return nil, errors.New("postgres: no current schema; check search_path")
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte("waleng-migrations:" + current.String))
	id := int64(h.Sum64() >> 1) // #nosec G115 -- shifted into the positive int64 range
	// Retry every second for up to five minutes, long enough for another
	// server to finish a slow migration.
	return lock.NewPostgresSessionLocker(lock.WithLockID(id), lock.WithLockTimeout(1, 300))
}

// AcquireLease implements Dialect. Expiry is computed and compared with
// the database clock (tech design 4.4). The upsert also creates the lease
// row on first use, which a plain UPDATE would not.
func (Postgres) AcquireLease(ctx context.Context, db bun.IDB, req LeaseRequest) (expiresAt time.Time, ok bool, err error) {
	q := db.NewRaw(`
		INSERT INTO leases (id, name, holder, expires_at)
		VALUES (?, ?, ?, now() + make_interval(secs => ?))
		ON CONFLICT (name) DO UPDATE
		SET holder = excluded.holder, expires_at = excluded.expires_at
		WHERE leases.holder = excluded.holder OR leases.expires_at < now()
		RETURNING expires_at`,
		req.ID, req.Name, req.Holder, req.TTL.Seconds())
	return scanLease(ctx, q)
}

// SingleWriter implements Dialect: row locks let writers run in parallel.
func (Postgres) SingleWriter() bool { return false }

// LockAccounts implements Dialect with a transaction-level advisory lock,
// released at commit or rollback. Under READ COMMITTED the count that
// follows the lock sees every account committed before it was granted. The
// key includes the current schema for the reason MigrationLocker gives;
// its original namespace stays stable across product renames as well.
func (Postgres) LockAccounts(ctx context.Context, db bun.IDB) error {
	if _, err := db.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('waleng-accounts:' || current_schema(), 0))"); err != nil {
		return fmt.Errorf("lock accounts: %w", err)
	}
	return nil
}

// Bytewise implements Dialect with the C collation, which compares bytes
// like SQLite instead of following the database locale.
func (Postgres) Bytewise(expr string) string { return expr + ` COLLATE "C"` }

// ClaimRuns implements RunQueue with FOR UPDATE SKIP LOCKED: rows another
// transaction is claiming are skipped, not waited for, so concurrent
// claimers on several servers never block each other. The CTE is
// MATERIALIZED so the locking SELECT runs exactly once; with
// "WHERE id IN (subquery)" the planner may run it again.
func (Postgres) ClaimRuns(ctx context.Context, db bun.IDB, req ClaimRequest) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := db.NewRaw(`
		WITH picked AS MATERIALIZED (
			SELECT id FROM runs
			WHERE status = 'queued' AND (not_before IS NULL OR not_before <= ?3) AND `+labelsMatch("?1")+`
				AND (?4 = 0 OR (SELECT count(*) FROM runs AS a
					WHERE a.project_id = runs.project_id AND a.status IN ('preparing', 'running', 'cancelling')) < ?4)
			ORDER BY queued_at, id
			LIMIT ?0
			FOR UPDATE SKIP LOCKED
		)
		UPDATE runs AS r
		SET status = 'preparing', runner_id = ?1, lease_expires_at = now() + make_interval(secs => ?2),
			claimed_at = ?3, runner_name = (SELECT name FROM runners WHERE id = ?1)
		FROM picked
		WHERE r.id = picked.id
		RETURNING r.id`,
		req.Limit, req.RunnerID, req.LeaseTTL.Seconds(), req.Now.UTC(), req.ProjectConcurrency,
	).Scan(ctx, &ids)
	if err != nil {
		return nil, fmt.Errorf("claim runs: %w", err)
	}
	return ids, nil
}

// RenewRunLeases implements RunQueue; expiry follows the database clock.
// A lease that has expired but not yet been swept is still renewed, so a
// short network hiccup does not interrupt a healthy run (tech design 4.4).
func (Postgres) RenewRunLeases(ctx context.Context, db bun.IDB, req RenewRequest) ([]RenewedRun, error) {
	if len(req.RunIDs) == 0 {
		return nil, nil
	}
	var renewed []RenewedRun
	err := db.NewRaw(`
		UPDATE runs
		SET lease_expires_at = now() + make_interval(secs => ?0)
		WHERE id IN (?1) AND runner_id = ?2 AND status IN ('preparing', 'running', 'cancelling')
		RETURNING id, status`,
		req.LeaseTTL.Seconds(), bun.List(req.RunIDs), req.RunnerID,
	).Scan(ctx, &renewed)
	if err != nil {
		return nil, fmt.Errorf("renew run leases: %w", err)
	}
	return renewed, nil
}

// ExpireRunLeases implements RunQueue; expiry follows the database clock.
// It is a single UPDATE, so it serialises with renewals on the row lock
// and whichever comes first wins.
func (Postgres) ExpireRunLeases(ctx context.Context, db bun.IDB, req ExpireRequest) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := db.NewRaw(`
		UPDATE runs
		SET status = 'interrupted', finished_at = ?0, error_code = ?1, error_message = ?2
		WHERE status IN ('preparing', 'running', 'cancelling') AND lease_expires_at < now()
		RETURNING id`,
		req.Now.UTC(), req.ErrorCode, req.ErrorMessage,
	).Scan(ctx, &ids)
	if err != nil {
		return nil, fmt.Errorf("expire run leases: %w", err)
	}
	return ids, nil
}

// RunLeaseLive uses the current database time, including time spent
// waiting for row locks since the transaction began.
func (Postgres) RunLeaseLive(ctx context.Context, db bun.IDB, id uuid.UUID, _ time.Time) (bool, error) {
	var live bool
	err := db.NewRaw(`SELECT EXISTS (SELECT 1 FROM runs WHERE id = ? AND lease_expires_at > clock_timestamp())`, id).Scan(ctx, &live)
	return live, err
}

// LiveClaims uses the current database clock without waiting for run row locks.
func (Postgres) LiveClaims(ctx context.Context, db *bun.DB, runnerID string, ids []uuid.UUID) ([]uuid.UUID, error) {
	var live []uuid.UUID
	err := db.NewRaw(`SELECT id FROM runs
		WHERE id IN (?) AND runner_id = ? AND status IN ('preparing', 'cancelling')
		AND lease_expires_at > clock_timestamp()`, bun.List(ids), runnerID).Scan(ctx, &live)
	return live, err
}
