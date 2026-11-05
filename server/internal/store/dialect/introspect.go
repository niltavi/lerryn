package dialect

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
)

// Table describes a table of the current schema, as needed to copy a
// whole database between dialects (tech design 15.5).
type Table struct {
	Name    string
	Columns []Column
	// References lists the tables this table's foreign keys point to,
	// without duplicates. A table referencing itself lists its own name.
	References []string
}

// Column is a column of a Table.
type Column struct {
	Name string
	// Type is the declared type in lower case, such as "text", "uuid" or
	// "timestamp with time zone" on PostgreSQL and "text" or "integer" on
	// SQLite.
	Type string
}

// Boolean reports whether the column holds booleans natively. SQLite
// stores them as INTEGER 0 or 1, so this is false for every SQLite column.
func (c Column) Boolean() bool { return c.Type == "boolean" }

// Introspector reads the schema the migrations created. It serves the
// export and import of a whole database, which must follow whatever
// tables later migrations add without being changed for each of them.
type Introspector interface {
	// Tables returns the tables of the current schema, sorted by name,
	// with their columns in declaration order.
	Tables(ctx context.Context, db bun.IDB) ([]Table, error)
	// SnapshotTxOptions returns the options of a read-only transaction in
	// which every query sees the same committed state.
	SnapshotTxOptions() *sql.TxOptions
}

// Tables implements Introspector.
func (SQLite) Tables(ctx context.Context, db bun.IDB) ([]Table, error) {
	var names []string
	err := db.NewRaw(`SELECT name FROM sqlite_schema WHERE type = 'table' AND substr(name, 1, 7) <> 'sqlite_' ORDER BY name`).
		Scan(ctx, &names)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	tables := make([]Table, 0, len(names))
	for _, name := range names {
		t := Table{Name: name}
		var cols []struct {
			Name string `bun:"name"`
			Type string `bun:"type"`
		}
		if err := db.NewRaw(`SELECT name, type FROM pragma_table_info(?) ORDER BY cid`, name).Scan(ctx, &cols); err != nil {
			return nil, fmt.Errorf("list columns of %s: %w", name, err)
		}
		for _, c := range cols {
			t.Columns = append(t.Columns, Column{Name: c.Name, Type: strings.ToLower(c.Type)})
		}
		if err := db.NewRaw(`SELECT DISTINCT "table" FROM pragma_foreign_key_list(?) ORDER BY 1`, name).Scan(ctx, &t.References); err != nil {
			return nil, fmt.Errorf("list foreign keys of %s: %w", name, err)
		}
		tables = append(tables, t)
	}
	return tables, nil
}

// SnapshotTxOptions implements Introspector. In WAL mode a read
// transaction sees the database as of its first read; the driver rejects
// isolation levels other than the default.
func (SQLite) SnapshotTxOptions() *sql.TxOptions {
	return &sql.TxOptions{ReadOnly: true}
}

// Tables implements Introspector. Only the current schema counts, which
// is the one search_path selects.
func (Postgres) Tables(ctx context.Context, db bun.IDB) ([]Table, error) {
	var names []string
	err := db.NewRaw(`
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'
		ORDER BY table_name`).Scan(ctx, &names)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	tables := make([]Table, 0, len(names))
	for _, name := range names {
		t := Table{Name: name}
		var cols []struct {
			Name string `bun:"column_name"`
			Type string `bun:"data_type"`
		}
		err := db.NewRaw(`
			SELECT column_name, data_type FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ?
			ORDER BY ordinal_position`, name).Scan(ctx, &cols)
		if err != nil {
			return nil, fmt.Errorf("list columns of %s: %w", name, err)
		}
		for _, c := range cols {
			t.Columns = append(t.Columns, Column{Name: c.Name, Type: strings.ToLower(c.Type)})
		}
		// pg_constraint rather than information_schema: constraint names
		// are unique per table, not per schema, so joining the
		// information_schema views by name can pair the wrong tables.
		err = db.NewRaw(`
			SELECT DISTINCT ref.relname
			FROM pg_constraint c
			JOIN pg_class tbl ON tbl.oid = c.conrelid
			JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
			JOIN pg_class ref ON ref.oid = c.confrelid
			WHERE c.contype = 'f' AND ns.nspname = current_schema() AND tbl.relname = ?
			ORDER BY 1`, name).Scan(ctx, &t.References)
		if err != nil {
			return nil, fmt.Errorf("list foreign keys of %s: %w", name, err)
		}
		tables = append(tables, t)
	}
	return tables, nil
}

// SnapshotTxOptions implements Introspector.
func (Postgres) SnapshotTxOptions() *sql.TxOptions {
	return &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead}
}

// VacuumInto writes a consistent copy of the open database to path, which
// must not exist. It runs as a read transaction, so the server keeps
// writing meanwhile (tech design 6.1, `lerryn backup`).
func (SQLite) VacuumInto(ctx context.Context, db bun.IDB, path string) error {
	if _, err := db.NewRaw(`VACUUM INTO ?`, path).Exec(ctx); err != nil {
		return fmt.Errorf("sqlite: copy database to %s: %w", path, err)
	}
	return nil
}
