package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/store"
)

func traversalArchive(t *testing.T) (string, snapshot, []byte) {
	t.Helper()
	db := openSQLite(t, filepath.Join(t.TempDir(), "source.db"))
	if _, err := store.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	storage := t.TempDir()
	content := seed(t, db, newLocal(t, storage))
	want := readSnapshot(t, db)
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if _, err := Backup(t.Context(), BackupOptions{DB: db, StoragePath: storage, Output: archive}); err != nil {
		t.Fatal(err)
	}
	return archive, want, content
}

func traversalLayout(t *testing.T, relative bool) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"a", "b/child/nested", "b/child/%2e%2e", "b/deep"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(root, "b", "child")
	if relative {
		target = "../b/child"
	}
	if err := os.Symlink(target, filepath.Join(root, "a", "link")); err != nil {
		t.Fatal(err)
	}
	return root
}

func traversalConfig(root, tail string) config.Database {
	prefix := (&url.URL{Path: filepath.ToSlash(root)}).EscapedPath()
	return config.Database{Driver: config.DatabaseSQLite, DSN: "file:" + prefix + "/" + tail}
}

func traversalOpen(t *testing.T, cfg config.Database) *store.DB {
	t.Helper()
	db, err := store.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func traversalRead(t *testing.T, cfg config.Database, storage string, want snapshot, content []byte) {
	t.Helper()
	db := traversalOpen(t, cfg)
	if got := readSnapshot(t, db); !reflect.DeepEqual(got, want) {
		t.Fatal("configured database does not contain the restored rows")
	}
	if err := store.CheckMigrations(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if data, err := blob.ReadAll(t.Context(), newLocal(t, storage), want.Files[0].BlobHash); err != nil || !bytes.Equal(data, content) {
		t.Fatalf("configured object store=%q, %v", data, err)
	}
}

func TestRestoreSQLiteParentTraversal(t *testing.T) {
	archive, want, content := traversalArchive(t)
	for _, tt := range []struct {
		name, tail string
		relative   bool
	}{
		{"literal parent after absolute symlink", "a/link/../target.db", false},
		{"encoded parent after absolute symlink", "a/link/%2e%2e/target.db", false},
		{"literal parent after relative symlink", "a/link/../target.db", true},
		{"encoded parent after relative symlink", "a/link/%2e%2e/target.db", true},
		{"encoded slash and parent", "a/link%2f%2e%2e%2ftarget.db", false},
		{"nested parent after symlink", "a/link/nested/../../target.db", false},
		{"parent before symlink", "a/../a/link/target.db", false},
		{"encoded dot after symlink", "a/link/%2e/target.db", false},
		{"double encoded parent stays literal", "a/link/%252e%252e/target.db", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := traversalLayout(t, tt.relative)
			cfg := traversalConfig(root, tt.tail)
			db := traversalOpen(t, cfg)
			if _, err := db.ExecContext(t.Context(), "CREATE TABLE original_data (value TEXT)"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(t.Context(), "INSERT INTO original_data VALUES ('keep before restore')"); err != nil {
				t.Fatal(err)
			}
			var actual string
			if err := db.NewRaw("SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(t.Context(), &actual); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			mapped, err := SQLitePath(cfg.DSN)
			if err != nil {
				t.Fatal(err)
			}
			mappedInfo, err := os.Stat(mapped)
			if err != nil {
				t.Fatal(err)
			}
			actualInfo, err := os.Stat(actual)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(mappedInfo, actualInfo) {
				t.Fatalf("SQLitePath=%q differs from actual SQLite database=%q", mapped, actual)
			}
			old, err := os.ReadFile(actual) // #nosec G304 -- actual is this test's SQLite filename
			if err != nil {
				t.Fatal(err)
			}
			originals := map[string][]byte{filepath.Base(actual): old, filepath.Base(actual) + "-wal": []byte("original wal"), filepath.Base(actual) + "-shm": []byte("original shm")}
			for suffix, body := range map[string]string{"-wal": "original wal", "-shm": "original shm"} {
				if err := os.WriteFile(actual+suffix, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			opts := RestoreOptions{Input: archive, Database: cfg, StoragePath: filepath.Join(root, "storage")}
			before := restoreTargetTree(t, root)
			if _, err := Restore(t.Context(), opts); !errors.Is(err, ErrTargetNotEmpty) {
				t.Fatalf("restore existing configured database=%v", err)
			}
			if after := restoreTargetTree(t, root); !reflect.DeepEqual(after, before) {
				t.Fatal("existing-data rejection changed target files")
			}
			opts.Force = true
			res, err := Restore(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			for _, saved := range res.MovedAside {
				base := filepath.Base(saved)
				if expected, ok := originals[base]; ok {
					got, err := os.ReadFile(saved) // #nosec G304 -- a preserved file in this test's temporary root
					if err != nil || !bytes.Equal(got, expected) {
						t.Fatalf("preserved %s differs: %v", base, err)
					}
					delete(originals, base)
				}
			}
			if len(originals) != 0 {
				t.Fatalf("actual SQLite files were not preserved: %v", originals)
			}
			traversalRead(t, cfg, opts.StoragePath, want, content)
		})
	}
}

func TestRestoreSQLiteTraversalNewParents(t *testing.T) {
	archive, want, content := traversalArchive(t)
	for _, kind := range []string{"new parents", "missing component canceled", "relative", "symlink target parents", "symlink target missing component"} {
		t.Run(kind, func(t *testing.T) {
			root := traversalLayout(t, false)
			tail := "a/link/../new/deep/target.db"
			switch kind {
			case "missing component canceled":
				tail = "a/link/missing/../new/deep/target.db"
			case "symlink target parents":
				if err := os.Symlink(filepath.Join(root, "b", "deep"), filepath.Join(root, "a", "pivot")); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(root, "a", "link")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../a/pivot/../child", filepath.Join(root, "a", "link")); err != nil {
					t.Fatal(err)
				}
			case "symlink target missing component":
				if err := os.Remove(filepath.Join(root, "a", "link")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../b/missing/../child", filepath.Join(root, "a", "link")); err != nil {
					t.Fatal(err)
				}
			}
			cfg := traversalConfig(root, tail)
			if kind == "relative" {
				t.Chdir(root)
				cfg.DSN = "file:" + tail
			}
			storage := filepath.Join(root, "storage")
			if _, err := Restore(t.Context(), RestoreOptions{Input: archive, Database: cfg, StoragePath: storage}); err != nil {
				t.Fatal(err)
			}
			traversalRead(t, cfg, storage, want, content)
			if _, err := os.Stat(filepath.Join(root, "b", "missing")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("canceled missing component was created: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "b", "child", "missing")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("canceled missing component below symlink was created: %v", err)
			}
		})
	}
}

func TestRestoreSQLiteTraversalPreservesFinalSymlink(t *testing.T) {
	archive, want, content := traversalArchive(t)
	root := traversalLayout(t, false)
	original := filepath.Join(root, "original.db")
	db := openSQLite(t, original)
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE original_data (value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(original) // #nosec G304 -- a database below t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(root, "b", "target.db")
	if err := os.Symlink(original, leaf); err != nil {
		t.Fatal(err)
	}
	cfg := traversalConfig(root, "a/link/../target.db")
	storage := filepath.Join(root, "storage")
	res, err := Restore(t.Context(), RestoreOptions{Input: archive, Database: cfg, StoragePath: storage, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	preserved := false
	for _, saved := range res.MovedAside {
		if filepath.Base(saved) == "target.db" {
			target, err := os.Readlink(saved)
			if err != nil || target != original {
				t.Fatalf("final symlink was not preserved: %q, %v", target, err)
			}
			preserved = true
		}
	}
	if !preserved {
		t.Fatal("no final symlink was preserved")
	}
	if data, err := os.ReadFile(original); err != nil || !bytes.Equal(data, old) { // #nosec G304 -- a database below t.TempDir
		t.Fatalf("original symlink target changed: %v", err)
	}
	traversalRead(t, cfg, storage, want, content)
}

func TestRestoreSQLiteTraversalRejectsOverlapsBeforeChanges(t *testing.T) {
	archive, _, _ := traversalArchive(t)
	for _, kind := range []string{"directory", "same file", "wal", "shm", "storage alias"} {
		t.Run(kind, func(t *testing.T) {
			root := traversalLayout(t, false)
			actual := filepath.Join(root, "b", "target.db")
			storage := filepath.Dir(actual)
			switch kind {
			case "same file":
				storage = actual
			case "wal", "shm":
				storage = actual + "-" + kind
			case "storage alias":
				storage = filepath.Join(root, "storage-link")
				if err := os.Symlink(filepath.Dir(actual), storage); err != nil {
					t.Fatal(err)
				}
			}
			before := restoreTargetTree(t, root)
			res, err := Restore(t.Context(), RestoreOptions{Input: archive, Database: traversalConfig(root, "a/link/%2e%2e/target.db"), StoragePath: storage, Force: true})
			if !errors.Is(err, ErrOverlappingTargets) || len(res.MovedAside) != 0 {
				t.Fatalf("overlap error=%v moved=%v", err, res.MovedAside)
			}
			if after := restoreTargetTree(t, root); !reflect.DeepEqual(after, before) {
				t.Fatal("overlap rejection changed target files")
			}
		})
	}
}

func TestRestoreStorageTraversalMatchesLocalStore(t *testing.T) {
	archive, want, content := traversalArchive(t)
	root := traversalLayout(t, false)
	storage := root + "/a/link/../storage" // blob.NewLocal cleans this before opening its objects.
	oldStore := newLocal(t, storage)
	oldContent := []byte("original object")
	hash, err := oldStore.Put(t.Context(), bytes.NewReader(oldContent))
	if err != nil {
		t.Fatal(err)
	}
	opts := RestoreOptions{Input: archive, Database: config.Database{Driver: config.DatabaseSQLite, DSN: filepath.Join(root, "target.db")}, StoragePath: storage}
	before := restoreTargetTree(t, root)
	if _, err := Restore(t.Context(), opts); !errors.Is(err, ErrTargetNotEmpty) {
		t.Fatalf("existing local store=%v", err)
	}
	if after := restoreTargetTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatal("local-store rejection changed files")
	}
	opts.Force = true
	res, err := Restore(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	preserved := false
	for _, saved := range res.MovedAside {
		if filepath.Base(saved) == "storage" {
			data, err := blob.ReadAll(t.Context(), newLocal(t, saved), hash)
			if err != nil || !bytes.Equal(data, oldContent) {
				t.Fatalf("old local store=%q, %v", data, err)
			}
			preserved = true
		}
	}
	if !preserved {
		t.Fatal("old local store was not preserved")
	}
	traversalRead(t, opts.Database, storage, want, content)
	if _, err := os.Stat(filepath.Join(root, "b", "storage")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore used SQLite traversal semantics for local storage: %v", err)
	}
}

func TestRestoreSQLiteTraversalRejectsInvalidParentsWithoutChanges(t *testing.T) {
	archive, _, _ := traversalArchive(t)
	for _, kind := range []string{"dangling", "loop", "file parent", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "a"), 0o750); err != nil {
				t.Fatal(err)
			}
			tail := "a/link/../new/target.db"
			switch kind {
			case "dangling":
				if err := os.Symlink("missing", filepath.Join(root, "a", "link")); err != nil {
					t.Fatal(err)
				}
			case "loop":
				if err := os.Symlink("link", filepath.Join(root, "a", "link")); err != nil {
					t.Fatal(err)
				}
			case "file parent":
				if err := os.WriteFile(filepath.Join(root, "a", "link"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
				tail = "a/link/child/../new/target.db"
			}
			ctx := t.Context()
			if kind == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before := restoreTargetTree(t, root)
			res, err := Restore(ctx, RestoreOptions{Input: archive, Database: traversalConfig(root, tail), StoragePath: filepath.Join(root, "storage"), Force: true})
			if err == nil || len(res.MovedAside) != 0 {
				t.Fatalf("invalid path error=%v moved=%v", err, res.MovedAside)
			}
			if kind == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled restore=%v", err)
			}
			if after := restoreTargetTree(t, root); !reflect.DeepEqual(after, before) {
				t.Fatal("rejected restore changed files")
			}
		})
	}
}

func TestSQLitePathKeepsRelativeParentTraversal(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, p := range []string{"relative.db", "../relative.db", "../../relative.db", "missing/../relative.db"} {
		got, err := SQLitePath("file:" + p)
		if err != nil || got != filepath.Clean(p) {
			t.Fatalf("SQLitePath(%q)=%q, %v", p, got, err)
		}
	}
	if got, err := SQLitePath("file:literal%252e%252e.db"); err != nil || !strings.Contains(got, "%2e%2e") {
		t.Fatalf("double-decoded filename=%q, %v", got, err)
	}
}

func TestRestoreSQLiteTraversalSymlinkLimit(t *testing.T) {
	archive, want, content := traversalArchive(t)
	for _, count := range []int{201, 202} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			// Remove platform aliases such as /var -> /private/var from the count.
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "target", "child"), 0o750); err != nil {
				t.Fatal(err)
			}
			for i := count - 1; i >= 0; i-- {
				target := fmt.Sprintf("link%d", i+1)
				if i == count-1 {
					target = "target/child"
				}
				if err := os.Symlink(target, filepath.Join(root, fmt.Sprintf("link%d", i))); err != nil {
					t.Fatal(err)
				}
			}
			cfg := traversalConfig(root, "link0/../target.db")
			db, openErr := store.Open(t.Context(), cfg)
			if count == 201 {
				if openErr != nil {
					t.Fatalf("actual SQLite no longer accepts 201 expansions: %v", openErr)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(root, "target", "target.db")); err != nil {
					t.Fatal(err)
				}
			} else if openErr == nil {
				_ = db.Close()
				t.Fatal("actual SQLite no longer rejects 202 expansions")
			}
			before := restoreTargetTree(t, root)
			storage := filepath.Join(root, "storage")
			res, err := Restore(t.Context(), RestoreOptions{Input: archive, Database: cfg, StoragePath: storage, Force: true})
			if count == 202 {
				if err == nil || len(res.MovedAside) != 0 {
					t.Fatalf("over-limit restore=%v moved=%v", err, res.MovedAside)
				}
				if after := restoreTargetTree(t, root); !reflect.DeepEqual(after, before) {
					t.Fatal("over-limit rejection changed files")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			traversalRead(t, cfg, storage, want, content)
		})
	}
}
