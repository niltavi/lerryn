package store

import (
	"context"
	"fmt"
	"path"
	"slices"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/niltavi/lerryn/server/migrations"
)

// MigrateResult is the outcome of Migrate.
type MigrateResult struct {
	// Applied lists the migrations applied by this call, oldest first.
	// It is empty when the schema was already current.
	Applied []AppliedMigration
	// Version is the schema version after the call.
	Version int64
}

// AppliedMigration describes one migration applied by Migrate.
type AppliedMigration struct {
	Version  int64
	Name     string
	Duration time.Duration
}

// Migrate applies every pending migration. It is safe to run repeatedly
// and from several servers at once: on PostgreSQL they take turns under
// an advisory lock, and each migration runs in its own transaction.
//
// Migrate fails when the schema is newer than this binary, which happens
// when a server is rolled back without restoring the database.
func Migrate(ctx context.Context, db *DB) (MigrateResult, error) {
	p, err := db.migrationProvider(ctx, true)
	if err != nil {
		return MigrateResult{}, err
	}
	results, err := p.Up(ctx)
	if err != nil {
		return MigrateResult{}, fmt.Errorf("apply migrations: %w", err)
	}
	var res MigrateResult
	for _, r := range results {
		res.Applied = append(res.Applied, AppliedMigration{
			Version:  r.Source.Version,
			Name:     path.Base(r.Source.Path),
			Duration: r.Duration,
		})
	}
	// Up does not object to versions it does not know, so a schema ahead
	// of this binary is detected afterwards.
	current, latest, err := p.GetVersions(ctx)
	if err != nil {
		return res, fmt.Errorf("read migration version: %w", err)
	}
	if current > latest {
		return res, fmt.Errorf("database schema version %d is newer than this binary supports (%d); upgrade lerryn or restore the database", current, latest)
	}
	res.Version = current
	return res, nil
}

// MigrateTo applies the pending migrations up to and including version,
// and no further. Importing an export taken at an older schema uses it to
// load the data into the schema it was written from before migrating the
// rest of the way (tech design 15.5).
func MigrateTo(ctx context.Context, db *DB, version int64) error {
	p, err := db.migrationProvider(ctx, true)
	if err != nil {
		return err
	}
	if _, err := p.UpTo(ctx, version); err != nil {
		return fmt.Errorf("apply migrations up to %d: %w", version, err)
	}
	current, err := p.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}
	if current != version {
		return fmt.Errorf("database schema is at version %d, want %d", current, version)
	}
	return nil
}

// LatestVersion returns the newest migration embedded in this binary for
// db's dialect. It does not touch the database.
func LatestVersion(ctx context.Context, db *DB) (int64, error) {
	p, err := db.migrationProvider(ctx, false)
	if err != nil {
		return 0, err
	}
	var latest int64
	for _, s := range p.ListSources() {
		latest = max(latest, s.Version)
	}
	return latest, nil
}

// SchemaVersion returns the newest migration applied to db and the newest
// one embedded in this binary. Like CheckMigrations it never writes.
func SchemaVersion(ctx context.Context, db *DB) (current, latest int64, err error) {
	latest, err = LatestVersion(ctx, db)
	if err != nil {
		return 0, 0, err
	}
	err = db.NewSelect().
		Table(goose.DefaultTablename).
		ColumnExpr("coalesce(max(version_id), 0)").
		Where("is_applied").
		Scan(ctx, &current)
	if err != nil {
		return 0, latest, fmt.Errorf("read applied migrations: %w", err)
	}
	return current, latest, nil
}

// CheckMigrations returns an error unless every embedded migration has
// been applied and the database records none newer. It never writes, not
// even goose's version table, so it suits a readiness check that may run
// while another server is migrating.
func CheckMigrations(ctx context.Context, db *DB) error {
	p, err := db.migrationProvider(ctx, false)
	if err != nil {
		return err
	}
	var applied []int64
	err = db.NewSelect().
		Table(goose.DefaultTablename).
		Column("version_id").
		Where("version_id > 0").
		Where("is_applied").
		Scan(ctx, &applied)
	if err != nil {
		return fmt.Errorf("read applied migrations (has `lerryn migrate` run?): %w", err)
	}
	var latest int64
	for _, s := range p.ListSources() {
		if !slices.Contains(applied, s.Version) {
			return fmt.Errorf("migration %s is not applied; run `lerryn migrate`", path.Base(s.Path))
		}
		latest = max(latest, s.Version)
	}
	for _, v := range applied {
		if v > latest {
			return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", v, latest)
		}
	}
	return nil
}

// migrationProvider builds a goose provider over the embedded migrations
// of db's dialect. withLock adds the dialect's migration lock.
//
// The provider shares db's pool and must not be closed: Provider.Close
// closes the pool.
func (db *DB) migrationProvider(ctx context.Context, withLock bool) (*goose.Provider, error) {
	fsys, err := migrations.FS(db.dialect.Name())
	if err != nil {
		return nil, err
	}
	opts := []goose.ProviderOption{
		// Only the embedded SQL files count; Go migrations registered
		// globally elsewhere in the process are ignored.
		goose.WithDisableGlobalRegistry(true),
	}
	if withLock {
		locker, err := db.dialect.MigrationLocker(ctx, db.DB.DB)
		if err != nil {
			return nil, err
		}
		if locker != nil {
			opts = append(opts, goose.WithSessionLocker(locker))
		}
	}
	p, err := goose.NewProvider(db.dialect.Goose(), db.DB.DB, fsys, opts...)
	if err != nil {
		return nil, fmt.Errorf("load migrations: %w", err)
	}
	return p, nil
}
