package ops

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestPreRenameBackupRestore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	db := openSQLite(t, source)
	if _, err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	user := store.User{ID: store.NewID(), Username: "before-rename", CreatedAt: store.Now(), UpdatedAt: store.Now()}
	if _, err := db.NewInsert().Model(&user).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "before-rename.tar.gz")
	a, err := createArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	// Literal names and metadata from the pre-rename format, independent
	// of today's writer constants and JSON struct tags.
	if err := a.addBytes("waleng-backup.json", []byte(`{"format":"waleng-backup","format_version":1,"created_at":"2026-11-05T00:00:00Z","waleng_version":"0.9.0","database":"sqlite","schema_version":0,"storage_included":false}`)); err != nil {
		a.abort()
		t.Fatal(err)
	}
	if err := a.addFile("database/waleng.db", source); err != nil {
		a.abort()
		t.Fatal(err)
	}
	if err := a.close(); err != nil {
		t.Fatal(err)
	}
	target := config.Database{Driver: config.DatabaseSQLite, DSN: filepath.Join(root, "restored.db")}
	res, err := Restore(ctx, RestoreOptions{Input: archive, Database: target})
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.LerrynVersion != "0.9.0" {
		t.Fatal("pre-rename backup version was lost")
	}
	restored := openSQLite(t, target.DSN)
	var got store.User
	if err := restored.NewSelect().Model(&got).Where("id = ?", user.ID).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if got.Username != user.Username {
		t.Fatal("pre-rename backup data changed")
	}
}

func TestPreRenameExportManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "before-rename.tar.gz")
	a, err := createArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.addBytes("waleng-export.json", []byte(`{"format":"waleng-export","format_version":1,"created_at":"2026-11-05T00:00:00Z","waleng_version":"0.9.0"}`)); err != nil {
		a.abort()
		t.Fatal(err)
	}
	if err := a.close(); err != nil {
		t.Fatal(err)
	}
	ar, err := openArchive(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer ar.close()
	m, err := readExportManifest(ar)
	if err != nil {
		t.Fatal(err)
	}
	if m.LerrynVersion != "0.9.0" {
		t.Fatal("pre-rename export version was lost")
	}
}
