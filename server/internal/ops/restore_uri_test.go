package ops

import (
	"bytes"
	"context"
	"errors"
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

// The configured URI must open the database Restore installed, including
// paths whose encoded spelling names a different file on the same disk.
func TestRestoreSQLiteFileURI(t *testing.T) {
	ctx := context.Background()
	src := openSQLite(t, filepath.Join(t.TempDir(), "source.db"))
	if _, err := store.Migrate(ctx, src); err != nil {
		t.Fatal(err)
	}
	sourceStorage := filepath.Join(t.TempDir(), "source-storage")
	content := seed(t, src, newLocal(t, sourceStorage))
	want := readSnapshot(t, src)
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if _, err := Backup(ctx, BackupOptions{DB: src, StoragePath: sourceStorage, Output: archive}); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{"single slash", "empty authority", "localhost", "relative", "fragment", "encoded parent", "plain filename"} {
		t.Run(kind, func(t *testing.T) {
			filename := "data %?#中文.db"
			if kind == "plain filename" {
				// A raw ? starts driver parameters even without file:.
				filename = "data %20#中文.db"
			}
			actual := filepath.Join(t.TempDir(), filename)
			if kind == "encoded parent" {
				actual = filepath.Join(filepath.Dir(actual), "parent %?#中文", filename)
			}
			u := &url.URL{Scheme: "file", Path: filepath.ToSlash(actual)}
			dsn := u.String()
			switch kind {
			case "single slash":
				dsn = "file:" + u.EscapedPath()
			case "localhost":
				u.Host = "localhost"
				dsn = u.String()
			case "relative":
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				rel, err := filepath.Rel(cwd, actual)
				if err != nil {
					t.Fatal(err)
				}
				dsn = "file:" + (&url.URL{Path: filepath.ToSlash(rel)}).EscapedPath()
			case "fragment":
				dsn += "#ignored"
			case "plain filename":
				dsn = actual
			}
			cfg := config.Database{Driver: config.DatabaseSQLite, DSN: dsn + "?cache=shared"}
			targetStorage := filepath.Join(t.TempDir(), "storage")
			opts := RestoreOptions{Input: archive, Database: cfg, StoragePath: targetStorage}
			if _, err := Restore(ctx, opts); err != nil {
				t.Fatalf("Restore(%q): %v", cfg.DSN, err)
			}
			if _, err := os.Stat(actual); err != nil {
				t.Fatalf("configured database was not installed: %v", err)
			}
			openConfigured := func() *store.DB {
				t.Helper()
				db, err := store.Open(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				return db
			}
			db := openConfigured()
			if got := readSnapshot(t, db); !reflect.DeepEqual(got, want) {
				t.Fatal("configured database does not hold the restored rows")
			}
			if err := store.CheckMigrations(ctx, db); err != nil {
				t.Fatal(err)
			}
			// Existing-data protection and --force must use the decoded
			// path too, preserving the actual database the server opened.
			if _, err := db.NewUpdate().Model((*store.User)(nil)).Set("display_name = ?", "changed after restore").Where("id = ?", want.Users[0].ID).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(actual) // #nosec G304 -- a database in t.TempDir
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Restore(ctx, opts); !errors.Is(err, ErrTargetNotEmpty) {
				t.Fatalf("second Restore = %v, want ErrTargetNotEmpty", err)
			}
			opts.Force = true
			res, err := Restore(ctx, opts)
			if err != nil {
				t.Fatalf("forced Restore: %v", err)
			}
			preserved := false
			for _, saved := range res.MovedAside {
				if filepath.Base(saved) == filename {
					old, err := os.ReadFile(saved) // #nosec G304 -- Restore's preserved file inside t.TempDir
					if err != nil || !bytes.Equal(old, before) {
						t.Fatalf("preserved configured database differs: %v", err)
					}
					preserved = true
				}
			}
			if !preserved {
				t.Fatalf("actual database not preserved: %v", res.MovedAside)
			}
			db = openConfigured()
			if got := readSnapshot(t, db); !reflect.DeepEqual(got, want) {
				t.Fatal("forced restore did not replace the configured database")
			}
			if data, err := blob.ReadAll(ctx, newLocal(t, targetStorage), want.Files[0].BlobHash); err != nil || !bytes.Equal(data, content) {
				t.Fatalf("restored object = %q, %v", data, err)
			}
		})
	}
}

func TestRestoreSQLiteFileURIRejectsOverlappingStorage(t *testing.T) {
	ctx := context.Background()
	src := openSQLite(t, filepath.Join(t.TempDir(), "source.db"))
	if _, err := store.Migrate(ctx, src); err != nil {
		t.Fatal(err)
	}
	sourceStorage := filepath.Join(t.TempDir(), "source-storage")
	seed(t, src, newLocal(t, sourceStorage))
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if _, err := Backup(ctx, BackupOptions{DB: src, StoragePath: sourceStorage, Output: archive}); err != nil {
		t.Fatal(err)
	}
	storage := filepath.Join(t.TempDir(), "storage with spaces")
	if err := os.Mkdir(storage, 0o750); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(storage, "original")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	actual := filepath.Join(storage, "database.db")
	dsn := "file:" + (&url.URL{Path: filepath.ToSlash(actual)}).EscapedPath()
	if !strings.Contains(dsn, "%20") {
		t.Fatal("test URI does not encode its overlapping parent")
	}
	_, err := Restore(ctx, RestoreOptions{
		Input: archive, Database: config.Database{Driver: config.DatabaseSQLite, DSN: dsn}, StoragePath: storage, Force: true,
	})
	if !errors.Is(err, ErrOverlappingTargets) {
		t.Fatalf("Restore = %v, want ErrOverlappingTargets", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" { // #nosec G304 -- a marker in t.TempDir
		t.Fatalf("overlap rejection changed existing storage: %q, %v", data, err)
	}
	if _, err := os.Stat(actual); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overlap rejection created a database: %v", err)
	}
}
