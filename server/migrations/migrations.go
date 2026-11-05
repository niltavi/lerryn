// Package migrations embeds the SQL schema migrations, one directory per
// database dialect. Both directories must contain the same versions with
// the same names; TestDialectsInSync enforces it.
//
// Create a new pair of migrations with the pinned goose CLI, which names
// files with a UTC timestamp:
//
//	go tool goose -dir server/migrations/postgres create <name> sql
//	go tool goose -dir server/migrations/sqlite create <name> sql
//
// Then rename the second file so both share the same timestamp. Apply
// migrations with `lerryn migrate`, or store.Migrate from code.
package migrations

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed postgres/*.sql sqlite/*.sql
var files embed.FS

// Directory names inside the embedded file system, matching the
// config.Database driver names.
const (
	Postgres = "postgres"
	SQLite   = "sqlite"
)

// FS returns the migrations of one dialect, Postgres or SQLite.
func FS(dialect string) (fs.FS, error) {
	if dialect != Postgres && dialect != SQLite {
		return nil, fmt.Errorf("migrations: unknown dialect %q", dialect)
	}
	return fs.Sub(files, dialect)
}
