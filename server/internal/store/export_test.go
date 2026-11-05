package store

import (
	"context"
	"fmt"
)

// ResetMigrations runs every down migration, so tests can check that the
// down sections mirror the up sections. Production code never migrates
// down: rolling back is a restore (tech design 15.4).
func ResetMigrations(ctx context.Context, db *DB) error {
	p, err := db.migrationProvider(ctx, true)
	if err != nil {
		return err
	}
	if _, err := p.DownTo(ctx, 0); err != nil {
		return fmt.Errorf("migrate down: %w", err)
	}
	return nil
}
