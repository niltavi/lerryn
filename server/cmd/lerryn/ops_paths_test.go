package main

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/niltavi/lerryn/server/internal/config"
)

func TestSameDatabaseSQLitePathForms(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	for _, dir := range []string{"a", "b/child"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	actual := filepath.Join(root, "b", "target.db")
	other := filepath.Join(root, "a", "target.db")
	for _, p := range []string{actual, other, "file:invalid%zz.db"} {
		if err := os.WriteFile(p, []byte("existing database"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(root, "alias.db")
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "b", "child"), filepath.Join(root, "a", "link")); err != nil {
		t.Fatal(err)
	}
	uri := "file:" + (&url.URL{Path: filepath.ToSlash(actual)}).EscapedPath()
	traversal := "file:" + (&url.URL{Path: filepath.ToSlash(root)}).EscapedPath() + "/a/link/%2e%2e/target.db"
	for _, tt := range []struct {
		name, a, b string
		want       bool
	}{
		{"identical", actual, actual, true},
		{"relative and absolute", "b/target.db", actual, true},
		{"file URI", uri, actual, true},
		{"symlink alias", alias, actual, true},
		{"symlink before parent same file", traversal, actual, true},
		{"symlink before parent different file", traversal, other, false},
		{"different existing files", actual, other, false},
		{"different missing files", "missing-a.db", "missing-b.db", false},
		{"same missing path spelling", "missing.db", "file:missing.db", true},
		{"failed parse never falls back to raw DSN", "file:invalid%zz.db", "./file:invalid%zz.db", false},
		{"same invalid DSN", "file:invalid%zz.db", "file:invalid%zz.db", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := config.Database{Driver: config.DatabaseSQLite, DSN: tt.a}
			b := config.Database{Driver: config.DatabaseSQLite, DSN: tt.b}
			if got := sameDatabase(a, b); got != tt.want {
				t.Fatalf("sameDatabase(%q,%q)=%t, want %t", tt.a, tt.b, got, tt.want)
			}
			if got := sameDatabase(b, a); got != tt.want {
				t.Fatalf("reverse comparison=%t, want %t", got, tt.want)
			}
		})
	}
	t.Run("hard link", func(t *testing.T) {
		hard := filepath.Join(root, "hard.db")
		if err := os.Link(actual, hard); err != nil {
			t.Skip("hard links unavailable:", err)
		}
		if !sameDatabase(config.Database{Driver: config.DatabaseSQLite, DSN: actual}, config.Database{Driver: config.DatabaseSQLite, DSN: hard}) {
			t.Fatal("same database inode was treated as a different database")
		}
	})
}

func TestSameDatabaseKeepsOtherDriverSemantics(t *testing.T) {
	for _, tt := range []struct {
		name string
		a, b config.Database
		want bool
	}{
		{"same postgres DSN", config.Database{Driver: config.DatabasePostgres, DSN: "postgres://localhost/lerryn"}, config.Database{Driver: config.DatabasePostgres, DSN: "postgres://localhost/lerryn"}, true},
		{"different postgres DSNs", config.Database{Driver: config.DatabasePostgres, DSN: "postgres://localhost/lerryn"}, config.Database{Driver: config.DatabasePostgres, DSN: "postgres://localhost/other"}, false},
		{"different drivers", config.Database{Driver: config.DatabaseSQLite, DSN: "same"}, config.Database{Driver: config.DatabasePostgres, DSN: "same"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameDatabase(tt.a, tt.b); got != tt.want {
				t.Fatalf("sameDatabase=%t, want %t", got, tt.want)
			}
		})
	}
}
