package dialect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/schema"

	// Pure Go driver, registered as "sqlite"; keeps CGO_ENABLED=0 builds working.
	_ "modernc.org/sqlite"
)

// SQLite is the SQLite dialect, driven by modernc.org/sqlite.
type SQLite struct{}

var _ Dialect = SQLite{}

// DefaultBusyTimeout is how long a connection waits for the write lock
// before failing with SQLITE_BUSY.
const DefaultBusyTimeout = 5 * time.Second

// Name implements Dialect.
func (SQLite) Name() string { return SQLiteName }

// Bun implements Dialect.
func (SQLite) Bun() schema.Dialect { return sqlitedialect.New() }

// Goose implements Dialect.
func (SQLite) Goose() goose.Dialect { return goose.DialectSQLite3 }

// OpenDB implements Dialect. dsn is a file path or a file: URI, optionally
// with modernc.org/sqlite query parameters.
func (SQLite) OpenDB(dsn string) (*sql.DB, error) {
	full, err := sqliteDSN(dsn)
	if err != nil {
		return nil, err
	}
	return sql.Open("sqlite", full)
}

// sqliteDSN adds the connection settings lerryn relies on to dsn, keeping
// any the operator set explicitly:
//
//   - journal_mode=WAL, so readers do not block the writer;
//   - foreign_keys=1, which SQLite leaves off by default;
//   - busy_timeout, so a second writer waits instead of failing at once;
//   - _txlock=immediate, so every read-write transaction takes the write
//     lock at BEGIN. A deferred transaction that reads first and writes
//     later fails with SQLITE_BUSY when another writer got in between,
//     and busy_timeout does not help in that case. This is what makes
//     SQLite a single-writer queue (tech design 4.4). Read-only
//     transactions (sql.TxOptions.ReadOnly) still begin deferred.
func sqliteDSN(dsn string) (string, error) {
	if dsn == "" {
		return "", errors.New("sqlite: empty dsn")
	}
	path, rawQuery, _ := strings.Cut(dsn, "?")
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("sqlite: parse dsn parameters: %w", err)
	}

	if v := q.Get("_txlock"); v != "" && !strings.EqualFold(v, "immediate") {
		return "", fmt.Errorf("sqlite: _txlock=%s is not supported, lerryn requires immediate", v)
	}
	q.Set("_txlock", "immediate")

	defaults := []struct {
		pragma  string
		aliases []string
		value   string
	}{
		{"busy_timeout", []string{"_busy_timeout", "_timeout"}, fmt.Sprintf("busy_timeout(%d)", DefaultBusyTimeout.Milliseconds())},
		{"foreign_keys", []string{"_foreign_keys", "_fk"}, "foreign_keys(1)"},
		{"journal_mode", []string{"_journal_mode", "_journal"}, "journal_mode(WAL)"},
	}
	for _, d := range defaults {
		if !hasPragma(q, d.pragma, d.aliases) {
			q.Add("_pragma", d.value)
		}
	}
	return path + "?" + q.Encode(), nil
}

func hasPragma(q url.Values, pragma string, aliases []string) bool {
	for _, a := range aliases {
		if q.Has(a) {
			return true
		}
	}
	for _, p := range q["_pragma"] {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(p)), pragma) {
			return true
		}
	}
	return false
}

// Check implements Dialect. It reads the settings back from one pooled
// connection, so an operator override that breaks an assumption fails at
// startup rather than as a data race later.
func (SQLite) Check(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("sqlite: get connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	var journalMode string
	var foreignKeys, busyTimeout int
	for _, p := range []struct {
		name string
		dest any
	}{
		{"journal_mode", &journalMode},
		{"foreign_keys", &foreignKeys},
		{"busy_timeout", &busyTimeout},
	} {
		if err := conn.QueryRowContext(ctx, "PRAGMA "+p.name).Scan(p.dest); err != nil {
			return fmt.Errorf("sqlite: read %s: %w", p.name, err)
		}
	}
	var errs []error
	if !strings.EqualFold(journalMode, "wal") {
		errs = append(errs, fmt.Errorf("journal_mode is %q, lerryn requires wal (in-memory databases are not supported)", journalMode))
	}
	if foreignKeys != 1 {
		errs = append(errs, errors.New("foreign_keys is off, lerryn requires it on"))
	}
	if busyTimeout <= 0 {
		errs = append(errs, errors.New("busy_timeout is 0, lerryn requires a positive timeout"))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("sqlite: %w", err)
	}
	return nil
}

// MigrationLocker implements Dialect. SQLite has a single server process;
// two migrators racing would still serialise on the write lock, and the
// loser fails on the already created objects without changing anything.
func (SQLite) MigrationLocker(context.Context, *sql.DB) (lock.SessionLocker, error) {
	return nil, nil
}

// AcquireLease implements Dialect. With a single server the application
// clock is the database clock.
func (SQLite) AcquireLease(ctx context.Context, db bun.IDB, req LeaseRequest) (expiresAt time.Time, ok bool, err error) {
	now := req.Now.UTC()
	q := db.NewRaw(`
		INSERT INTO leases (id, name, holder, expires_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE
		SET holder = excluded.holder, expires_at = excluded.expires_at
		WHERE leases.holder = excluded.holder OR leases.expires_at < ?
		RETURNING expires_at`,
		req.ID, req.Name, req.Holder, now.Add(req.TTL), now)
	return scanLease(ctx, q)
}

// SingleWriter implements Dialect: SQLite has one writer at a time.
func (SQLite) SingleWriter() bool { return true }

// LockAccounts implements Dialect as a no-op: every read-write transaction
// begins IMMEDIATE and holds the database write lock, so account creations
// are serialised already.
func (SQLite) LockAccounts(context.Context, bun.IDB) error { return nil }

// Bytewise implements Dialect. SQLite compares text with BINARY unless told
// otherwise.
func (SQLite) Bytewise(expr string) string { return expr }

// ClaimRuns implements RunQueue. SQLite has no row locks; the caller's
// transaction begins IMMEDIATE (see sqliteDSN) and store.DB.WriteTx
// queues writers in the process, so each claimer sees the previous one's
// result. Leases use the application clock: one server, one clock.
func (SQLite) ClaimRuns(ctx context.Context, db bun.IDB, req ClaimRequest) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := db.NewRaw(`
		UPDATE runs
		SET status = 'preparing', runner_id = ?0, lease_expires_at = ?1,
			claimed_at = ?3, runner_name = (SELECT name FROM runners WHERE id = ?0)
		WHERE id IN (
			SELECT id FROM runs
			WHERE status = 'queued' AND (not_before IS NULL OR not_before <= ?3) AND `+labelsMatch("?0")+`
				AND (?4 = 0 OR (SELECT count(*) FROM runs AS a
					WHERE a.project_id = runs.project_id AND a.status IN ('preparing', 'running', 'cancelling')) < ?4)
			ORDER BY queued_at, id
			LIMIT ?2)
		RETURNING id`,
		req.RunnerID, req.Now.UTC().Add(req.LeaseTTL), req.Limit, req.Now.UTC(), req.ProjectConcurrency,
	).Scan(ctx, &ids)
	if err != nil {
		return nil, fmt.Errorf("claim runs: %w", err)
	}
	return ids, nil
}

// RenewRunLeases implements RunQueue.
func (SQLite) RenewRunLeases(ctx context.Context, db bun.IDB, req RenewRequest) ([]RenewedRun, error) {
	if len(req.RunIDs) == 0 {
		return nil, nil
	}
	var renewed []RenewedRun
	err := db.NewRaw(`
		UPDATE runs
		SET lease_expires_at = ?0
		WHERE id IN (?1) AND runner_id = ?2 AND status IN ('preparing', 'running', 'cancelling')
		RETURNING id, status`,
		req.Now.UTC().Add(req.LeaseTTL), bun.List(req.RunIDs), req.RunnerID,
	).Scan(ctx, &renewed)
	if err != nil {
		return nil, fmt.Errorf("renew run leases: %w", err)
	}
	return renewed, nil
}

// ExpireRunLeases implements RunQueue. The comparison is textual; it is
// correct because Bun writes every time as UTC in one fixed layout.
func (SQLite) ExpireRunLeases(ctx context.Context, db bun.IDB, req ExpireRequest) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	now := req.Now.UTC()
	err := db.NewRaw(`
		UPDATE runs
		SET status = 'interrupted', finished_at = ?0, error_code = ?1, error_message = ?2
		WHERE status IN ('preparing', 'running', 'cancelling') AND lease_expires_at < ?0
		RETURNING id`,
		now, req.ErrorCode, req.ErrorMessage,
	).Scan(ctx, &ids)
	if err != nil {
		return nil, fmt.Errorf("expire run leases: %w", err)
	}
	return ids, nil
}

// RunLeaseLive follows the application clock used by SQLite leases.
func (SQLite) RunLeaseLive(ctx context.Context, db bun.IDB, id uuid.UUID, now time.Time) (bool, error) {
	var live bool
	err := db.NewRaw(`SELECT EXISTS (SELECT 1 FROM runs WHERE id = ? AND lease_expires_at > ?)`, id, now.UTC()).Scan(ctx, &live)
	return live, err
}

// LiveClaims samples the application clock after Scan, so connection-pool
// or query waits cannot make an already expired claim appear live.
func (SQLite) LiveClaims(ctx context.Context, db *bun.DB, runnerID string, ids []uuid.UUID) ([]uuid.UUID, error) {
	var candidates []struct {
		ID             uuid.UUID `bun:"id"`
		LeaseExpiresAt time.Time `bun:"lease_expires_at"`
	}
	if err := db.NewRaw(`SELECT id, lease_expires_at FROM runs
		WHERE id IN (?) AND runner_id = ? AND status IN ('preparing', 'cancelling')
		AND lease_expires_at IS NOT NULL`, bun.List(ids), runnerID).Scan(ctx, &candidates); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	var live []uuid.UUID
	for _, run := range candidates {
		if run.LeaseExpiresAt.After(now) {
			live = append(live, run.ID)
		}
	}
	return live, nil
}
