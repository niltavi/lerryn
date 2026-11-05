package migrations

import (
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// fileName is a goose timestamp version followed by a snake_case name.
var fileName = regexp.MustCompile(`^[0-9]{14}_[a-z0-9_]+\.sql$`)

func TestDialectsInSync(t *testing.T) {
	pg := names(t, Postgres)
	lite := names(t, SQLite)
	if len(pg) == 0 {
		t.Fatal("no migrations embedded")
	}
	if !slices.Equal(pg, lite) {
		t.Fatalf("postgres and sqlite migrations differ; every migration must be added to both\npostgres: %v\nsqlite:   %v", pg, lite)
	}
}

func TestFileFormat(t *testing.T) {
	for _, dialect := range []string{Postgres, SQLite} {
		fsys, err := FS(dialect)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range names(t, dialect) {
			if !fileName.MatchString(name) {
				t.Errorf("%s/%s: name must be <14-digit UTC timestamp>_<snake_case>.sql", dialect, name)
			}
			data, err := fs.ReadFile(fsys, name)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "-- +goose Up") {
				t.Errorf("%s/%s: missing -- +goose Up annotation", dialect, name)
			}
		}
	}
}

func TestFSRejectsUnknownDialect(t *testing.T) {
	if _, err := FS("mysql"); err == nil {
		t.Fatal("expected an error for an unknown dialect")
	}
}

func names(t *testing.T, dialect string) []string {
	t.Helper()
	fsys, err := FS(dialect)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
