package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

type afterBackupSnapshot struct{ after func() }

func (h *afterBackupSnapshot) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (h *afterBackupSnapshot) AfterQuery(_ context.Context, event *bun.QueryEvent) {
	if event.Err == nil && strings.HasPrefix(event.Query, "VACUUM INTO ") && h.after != nil {
		after := h.after
		h.after = nil
		after()
	}
}

func TestBackupKeepsObjectsReleasedAfterSnapshot(t *testing.T) {
	ctx := t.Context()
	src := t.TempDir()
	db := openSQLite(t, filepath.Join(src, "lerryn.db"))
	if _, err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	storage := filepath.Join(src, "storage")
	bs := newLocal(t, storage)
	content := seed(t, db, bs)
	hash := blob.Sum(content)
	retention := service.NewRetention(db, bs, service.RetentionPolicy{}, nil)
	interleaved := false
	db.DB = db.WithQueryHook(&afterBackupSnapshot{after: func() {
		interleaved = true
		// The snapshot still references this object. The live instance no
		// longer does, so retention would otherwise remove it before copy.
		if _, err := db.NewDelete().Model((*store.TaskFile)(nil)).Where("blob_hash = ?", hash).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := db.NewInsert().Model(&store.BlobRelease{ID: store.NewID(), Hash: hash, ReleasedAt: store.Now()}).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if got, err := retention.Cleanup(ctx); err != nil || got.Objects != 0 {
			t.Fatalf("cleanup while snapshot needs the object = %+v, %v", got, err)
		}
	}})
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if _, err := Backup(ctx, BackupOptions{DB: db, StoragePath: storage, Output: archive}); err != nil {
		t.Fatal(err)
	}
	if !interleaved {
		t.Fatal("the collector did not run between the database snapshot and the object copy")
	}
	// Protection ends with the backup, so the next cleanup reclaims it.
	if got, err := retention.Cleanup(ctx); err != nil || got.Objects != 1 {
		t.Fatalf("cleanup after backup = %+v, %v", got, err)
	}
	dst := t.TempDir()
	restoredStorage := filepath.Join(dst, "storage")
	target := config.Database{Driver: config.DatabaseSQLite, DSN: filepath.Join(dst, "lerryn.db")}
	if _, err := Restore(ctx, RestoreOptions{Input: archive, Database: target, StoragePath: restoredStorage}); err != nil {
		t.Fatal(err)
	}
	if got, err := blob.ReadAll(ctx, newLocal(t, restoredStorage), hash); err != nil || string(got) != string(content) {
		t.Fatalf("restored snapshot object = %q, %v", got, err)
	}
}

func TestBackupRejectsMissingSnapshotObject(t *testing.T) {
	ctx := t.Context()
	src := t.TempDir()
	db := openSQLite(t, filepath.Join(src, "lerryn.db"))
	if _, err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	storage := filepath.Join(src, "storage")
	bs := newLocal(t, storage)
	content := seed(t, db, bs)
	if err := bs.Delete(ctx, blob.Sum(content)); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	_, err := Backup(ctx, BackupOptions{DB: db, StoragePath: storage, Output: archive})
	if err == nil || !strings.Contains(err.Error(), "missing object") {
		t.Fatalf("backup with a missing snapshot object = %v", err)
	}
	if _, err := os.Stat(archive); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete archive remains: %v", err)
	}
	// An output placed at the missing object's path must not masquerade
	// as that object and be read back into the archive itself.
	hash := blob.Sum(content)
	objectPath := filepath.Join(storage, "blobs", "sha256", hash[:2], hash)
	if _, err := Backup(ctx, BackupOptions{DB: db, StoragePath: storage, Output: objectPath}); err == nil {
		t.Fatal("backup accepted its own output as a missing object")
	}
	if _, err := os.Stat(objectPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete archive remains at the object path: %v", err)
	}
}

func TestBackupRejectsCorruptObject(t *testing.T) {
	ctx := t.Context()
	src := t.TempDir()
	db := openSQLite(t, filepath.Join(src, "lerryn.db"))
	if _, err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	storage := filepath.Join(src, "storage")
	content := seed(t, db, newLocal(t, storage))
	hash := blob.Sum(content)
	if err := os.WriteFile(filepath.Join(storage, "blobs", "sha256", hash[:2], hash), []byte("corrupted content"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if _, err := Backup(ctx, BackupOptions{DB: db, StoragePath: storage, Output: archive}); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("backup accepted corrupted object: %v", err)
	}
	if _, err := os.Stat(archive); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupted archive remains: %v", err)
	}
}

func TestBackupCopiesOnlySnapshotObjects(t *testing.T) {
	ctx := t.Context()
	src := t.TempDir()
	db := openSQLite(t, filepath.Join(src, "lerryn.db"))
	if _, err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	storage := filepath.Join(src, "storage")
	bs := newLocal(t, storage)
	content := seed(t, db, bs)
	if _, err := bs.Put(ctx, strings.NewReader("unreferenced object")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storage, "master.key"), []byte("unrelated secret file"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The output and its staging directory also live in storage. Neither
	// belongs to the database snapshot's content-addressed objects.
	archive := filepath.Join(storage, "backups", "backup.tar.gz")
	res, err := Backup(ctx, BackupOptions{DB: db, StoragePath: storage, Output: archive})
	if err != nil || res.StorageFiles != 1 {
		t.Fatalf("backup within storage = %+v, %v", res, err)
	}
	dst := t.TempDir()
	restored := filepath.Join(dst, "storage")
	target := config.Database{Driver: config.DatabaseSQLite, DSN: filepath.Join(dst, "lerryn.db")}
	if _, err := Restore(ctx, RestoreOptions{Input: archive, Database: target, StoragePath: restored}); err != nil {
		t.Fatal(err)
	}
	if got, err := blob.ReadAll(ctx, newLocal(t, restored), blob.Sum(content)); err != nil || string(got) != string(content) {
		t.Fatalf("restored object = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(restored, "master.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unrelated secret file was included: %v", err)
	}
}

func TestBackupCancellationReleasesProtection(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	src := t.TempDir()
	db := openSQLite(t, filepath.Join(src, "lerryn.db"))
	if _, err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	storage := filepath.Join(src, "storage")
	seed(t, db, newLocal(t, storage))
	db.DB = db.WithQueryHook(&afterBackupSnapshot{after: cancel})
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if _, err := Backup(ctx, BackupOptions{DB: db, StoragePath: storage, Output: archive}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled backup = %v", err)
	}
	if _, err := os.Stat(archive); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled archive remains: %v", err)
	}
	if pinned, err := db.BlobBackupsActive(t.Context(), db); err != nil || pinned {
		t.Fatalf("protection after cancelled backup = %v, %v", pinned, err)
	}
}

func TestBackupProtectionStopsFullCollectorBatch(t *testing.T) {
	ctx := t.Context()
	db := openSQLite(t, filepath.Join(t.TempDir(), "lerryn.db"))
	if _, err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	bs := newLocal(t, t.TempDir())
	var releases []store.BlobRelease
	for i := range 201 {
		releases = append(releases, store.BlobRelease{ID: store.NewID(), Hash: fmt.Sprintf("%064x", i), ReleasedAt: store.Now()})
	}
	if _, err := db.NewInsert().Model(&releases).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	pin, err := db.PinBlobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pin.Close() }()
	retention := service.NewRetention(db, bs, service.RetentionPolicy{}, nil)
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if got, err := retention.Cleanup(bounded); err != nil || got.Objects != 0 {
		t.Fatalf("full protected batch = %+v, %v", got, err)
	}
	if err := pin.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := retention.Cleanup(ctx); err != nil || got.Objects != len(releases) {
		t.Fatalf("batch after protection ends = %+v, %v", got, err)
	}
}

func TestBackupChecksEverySnapshotObjectKind(t *testing.T) {
	ctx := t.Context()
	src := t.TempDir()
	db := openSQLite(t, filepath.Join(src, "lerryn.db"))
	if _, err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	storage := filepath.Join(src, "storage")
	bs := newLocal(t, storage)
	content := seed(t, db, bs)
	required := map[string]string{blob.Sum(content): string(content)}
	put := func(content string) string {
		t.Helper()
		hash, err := bs.Put(ctx, strings.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		required[hash] = content
		return hash
	}
	current, legacy, input, artifact := put("current log"), put("legacy log"), put("webhook input"), put("report")
	var done store.Run
	if err := db.NewSelect().Model(&done).Where("status = ?", store.RunCompleted).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.NewUpdate().Model((*store.Run)(nil)).Set("log_blob = ?", current).
		Set("log_archive = ?", `{"blob":"`+current+`"}`).Set("input_key = ?", input).Where("id = ?", done.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	old := done
	old.ID = store.NewID()
	old.LogArchive = `{"blob":"` + legacy + `"}`
	if _, err := db.NewInsert().Model(&old).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	expired := old
	expired.ID, expired.LogsExpiredAt = store.NewID(), store.Now()
	expired.LogArchive = `{"blob":"` + strings.Repeat("f", 64) + `"}`
	if _, err := db.NewInsert().Model(&expired).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	artifacts := []store.Artifact{
		{ID: store.NewID(), RunID: done.ID, Path: "report", BlobKey: artifact, SHA256: artifact, Size: 6, ContentType: "text/plain", CreatedAt: store.Now()},
		{ID: store.NewID(), RunID: done.ID, Path: "expired", BlobKey: strings.Repeat("e", 64), SHA256: strings.Repeat("e", 64), Size: 1, ContentType: "text/plain", CreatedAt: store.Now(), ExpiredAt: store.Now()},
	}
	if _, err := db.NewInsert().Model(&artifacts).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	archiveDir := t.TempDir()
	if res, err := Backup(ctx, BackupOptions{DB: db, StoragePath: storage, Output: filepath.Join(archiveDir, "complete.tar.gz")}); err != nil || res.StorageFiles != len(required) {
		t.Fatalf("complete snapshot = %+v, %v", res, err)
	}
	for hash, content := range required {
		t.Run(content[:min(12, len(content))], func(t *testing.T) {
			if err := bs.Delete(ctx, hash); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(archiveDir, hash+".tar.gz")
			if _, err := Backup(ctx, BackupOptions{DB: db, StoragePath: storage, Output: archive}); err == nil {
				t.Fatal("backup accepted a missing referenced object")
			}
			if _, err := os.Stat(archive); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("incomplete archive remains: %v", err)
			}
			if _, err := bs.Put(ctx, strings.NewReader(content)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
