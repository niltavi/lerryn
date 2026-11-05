package ops

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

type statePersistenceRows struct {
	Heads []store.TaskState
	Refs  []store.RunStateRef
}

type statePersistenceFixture struct {
	Rows    statePersistenceRows
	Objects map[string][]byte
}

func statePersistenceObject(t *testing.T, bs blob.Store, cursor string) (string, []byte, int64) {
	t.Helper()
	data, size, err := runproto.EncodeState(runproto.StateSnapshot{Version: 1, Entries: []runproto.StateEntry{
		{Path: "checkpoint", Directory: true},
		{Path: "checkpoint/cursor.json", Content: []byte(cursor)},
		{Path: "resume.sh", Executable: true, Content: []byte("echo resume\n")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bs.Put(t.Context(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return hash, data, size
}

func readStatePersistenceRows(t *testing.T, db bun.IDB) statePersistenceRows {
	t.Helper()
	var rows statePersistenceRows
	if err := db.NewSelect().Model(&rows.Heads).Order("task_id").Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.NewSelect().Model(&rows.Refs).Order("run_id", "role").Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	utcTimes(reflect.ValueOf(&rows).Elem())
	return rows
}

func seedStatePersistence(t *testing.T, db *store.DB, bs blob.Store) statePersistenceFixture {
	t.Helper()
	file := seed(t, db, bs)
	var task store.Task
	if err := db.NewSelect().Model(&task).Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	var runs []store.Run
	if err := db.NewSelect().Model(&runs).Order("id").Scan(t.Context()); err != nil || len(runs) != 2 {
		t.Fatalf("seeded runs = %d, %v", len(runs), err)
	}
	head, headData, headSize := statePersistenceObject(t, bs, `{"cursor":"head 中文 🚀"}`)
	input, inputData, inputSize := statePersistenceObject(t, bs, `{"cursor":"input"}`)
	output, outputData, outputSize := statePersistenceObject(t, bs, `{"cursor":"output"}`)
	now := store.Now()
	if _, err := db.NewUpdate().Model((*store.Run)(nil)).
		Set("status = ?", store.RunRunning).Set("runner_id = ?", "runner-1").
		Set("finished_at = NULL").Where("task_id = ?", task.ID).Exec(t.Context()); err != nil {
		t.Fatal(err)
	}
	state := store.TaskState{TaskID: task.ID, BlobHash: head, Size: headSize, UpdatedAt: now}
	if _, err := db.NewInsert().Model(&state).Exec(t.Context()); err != nil {
		t.Fatal(err)
	}
	refs := []store.RunStateRef{
		{RunID: runs[0].ID, Role: store.RunStateInput, BlobHash: input, Size: inputSize, Ready: true},
		{RunID: runs[0].ID, Role: store.RunStateOutput, BlobHash: output, Size: outputSize, Ready: true},
		{RunID: runs[1].ID, Role: store.RunStateInput, Ready: true},
		{RunID: runs[1].ID, Role: store.RunStateOutput, BlobHash: head, Size: headSize, Ready: false},
	}
	if _, err := db.NewInsert().Model(&refs).Exec(t.Context()); err != nil {
		t.Fatal(err)
	}
	return statePersistenceFixture{Rows: readStatePersistenceRows(t, db), Objects: map[string][]byte{
		blob.Sum(file): file, head: headData, input: inputData, output: outputData,
	}}
}

func assertStatePersistence(t *testing.T, db bun.IDB, bs blob.Store, want statePersistenceFixture) {
	t.Helper()
	if got := readStatePersistenceRows(t, db); !reflect.DeepEqual(got, want.Rows) {
		t.Fatalf("state references differ:\n got %+v\nwant %+v", got, want.Rows)
	}
	for hash, content := range want.Objects {
		if got, err := blob.ReadAll(t.Context(), bs, hash); err != nil || !bytes.Equal(got, content) {
			t.Fatalf("state object %s = %q, %v; want %q", hash, got, err, content)
		}
	}
}

func TestStatePersistenceBackupRestore(t *testing.T) {
	src := t.TempDir()
	db := openSQLite(t, filepath.Join(src, "source.db"))
	if _, err := store.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	storage := filepath.Join(src, "storage")
	want := seedStatePersistence(t, db, newLocal(t, storage))
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	result, err := Backup(t.Context(), BackupOptions{DB: db, StoragePath: storage, Output: archive})
	if err != nil || result.StorageFiles != len(want.Objects) {
		t.Fatalf("Backup = %+v, %v; want %d distinct objects", result, err, len(want.Objects))
	}
	target := config.Database{Driver: config.DatabaseSQLite, DSN: filepath.Join(t.TempDir(), "restored.db")}
	restoredStorage := t.TempDir()
	if _, err := Restore(t.Context(), RestoreOptions{Input: archive, Database: target, StoragePath: restoredStorage}); err != nil {
		t.Fatal(err)
	}
	assertStatePersistence(t, openSQLite(t, target.DSN), newLocal(t, restoredStorage), want)
}

func TestStatePersistenceExportImport(t *testing.T) {
	newSQLite := func(t *testing.T) *store.DB { return openSQLite(t, filepath.Join(t.TempDir(), "lerryn.db")) }
	newSelected := func(t *testing.T) *store.DB { return storetest.OpenEmpty(t) }
	for _, tt := range []struct {
		name     string
		src, dst func(*testing.T) *store.DB
	}{
		{"sqlite to selected", newSQLite, newSelected},
		{"selected to sqlite", newSelected, newSQLite},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src := tt.src(t)
			if _, err := store.Migrate(t.Context(), src); err != nil {
				t.Fatal(err)
			}
			bs := newLocal(t, t.TempDir())
			want := seedStatePersistence(t, src, bs)
			archive := filepath.Join(t.TempDir(), "export.tar.gz")
			manifest, err := Export(t.Context(), ExportOptions{DB: src, Blobs: bs, Output: archive})
			if err != nil || manifest.Blobs != len(want.Objects) || len(manifest.MissingBlobs) != 0 {
				t.Fatalf("Export = %+v, %v; want %d distinct objects", manifest, err, len(want.Objects))
			}
			dst := tt.dst(t)
			dstBlobs := newLocal(t, t.TempDir())
			if _, err := Import(t.Context(), ImportOptions{DB: dst, Blobs: dstBlobs, Input: archive}); err != nil {
				t.Fatal(err)
			}
			assertStatePersistence(t, dst, dstBlobs, want)
			if got, err := TableCounts(t.Context(), dst); err != nil || got["task_states"] != 1 || got["run_state_refs"] != 4 {
				t.Fatalf("imported state table counts = %v, %v", got, err)
			}
		})
	}
}

func TestStatePersistenceOldSchemaImport(t *testing.T) {
	// Use a real pre-state schema and export, rather than removing tables
	// from a current archive whose schema version would promise them.
	const beforeState = 20261109010000
	src := openSQLite(t, filepath.Join(t.TempDir(), "old.db"))
	if err := store.MigrateTo(t.Context(), src, beforeState); err != nil {
		t.Fatal(err)
	}
	bs := newLocal(t, t.TempDir())
	seed(t, src, bs)
	want := readSnapshot(t, src)
	archive := filepath.Join(t.TempDir(), "old-export.tar.gz")
	manifest, err := Export(t.Context(), ExportOptions{DB: src, Blobs: bs, Output: archive})
	if err != nil || manifest.SchemaVersion != beforeState {
		t.Fatalf("old schema export = %+v, %v", manifest, err)
	}
	dst := storetest.OpenEmpty(t)
	result, err := Import(t.Context(), ImportOptions{DB: dst, Blobs: newLocal(t, t.TempDir()), Input: archive})
	if err != nil || len(result.Migrated) == 0 {
		t.Fatalf("Import = %+v, %v; want migrations after loading old rows", result, err)
	}
	if err := store.CheckMigrations(t.Context(), dst); err != nil {
		t.Fatal(err)
	}
	if got := readSnapshot(t, dst); !reflect.DeepEqual(got, want) {
		t.Fatal("old instance rows changed during import")
	}
	if rows := readStatePersistenceRows(t, dst); len(rows.Heads) != 0 || len(rows.Refs) != 0 {
		t.Fatalf("an old deployment acquired state references: %+v", rows)
	}
}

func TestStatePersistenceMissingObjects(t *testing.T) {
	for _, missing := range []string{"head", "input", "output"} {
		t.Run(missing, func(t *testing.T) {
			db := openSQLite(t, filepath.Join(t.TempDir(), "source.db"))
			if _, err := store.Migrate(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			storage := t.TempDir()
			bs := newLocal(t, storage)
			want := seedStatePersistence(t, db, bs)
			hash := want.Rows.Heads[0].BlobHash
			if missing != "head" {
				for _, ref := range want.Rows.Refs {
					if ref.Role == missing && ref.BlobHash != "" && ref.BlobHash != hash {
						hash = ref.BlobHash
						break
					}
				}
			}
			if err := bs.Delete(t.Context(), hash); err != nil {
				t.Fatal(err)
			}
			manifest, err := Export(t.Context(), ExportOptions{DB: db, Blobs: bs, Output: filepath.Join(t.TempDir(), "export.tar.gz")})
			if err != nil || manifest.Blobs != len(want.Objects)-1 || !reflect.DeepEqual(manifest.MissingBlobs, []string{hash}) {
				t.Fatalf("Export with missing %s = %+v, %v", missing, manifest, err)
			}
			archive := filepath.Join(t.TempDir(), "backup.tar.gz")
			if _, err := Backup(t.Context(), BackupOptions{DB: db, StoragePath: storage, Output: archive}); err == nil {
				t.Fatalf("Backup accepted missing %s state object", missing)
			}
			if _, err := os.Stat(archive); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("incomplete backup remains: %v", err)
			}
			if got := readStatePersistenceRows(t, db); !reflect.DeepEqual(got, want.Rows) {
				t.Fatal("missing objects changed the source references")
			}
		})
	}
}

func TestStatePersistenceBackupSnapshotGC(t *testing.T) {
	db := openSQLite(t, filepath.Join(t.TempDir(), "source.db"))
	if _, err := store.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	storage := t.TempDir()
	bs := newLocal(t, storage)
	want := seedStatePersistence(t, db, bs)
	replacement, replacementData, replacementSize := statePersistenceObject(t, bs, `{"cursor":"replacement"}`)
	retention := service.NewRetention(db, bs, service.RetentionPolicy{}, nil)
	interleaved := false
	db.DB = db.WithQueryHook(&afterBackupSnapshot{after: func() {
		interleaved = true
		// Finish and replace the live state after the database snapshot,
		// before the backup has opened any of its old objects.
		if err := db.WriteTx(t.Context(), func(ctx context.Context, tx bun.Tx) error {
			if _, err := tx.NewUpdate().Model((*store.TaskState)(nil)).
				Set("blob_hash = ?", replacement).Set("size = ?", replacementSize).
				Set("updated_at = ?", store.Now()).Where("task_id = ?", want.Rows.Heads[0].TaskID).Exec(ctx); err != nil {
				return err
			}
			if _, err := tx.NewUpdate().Model((*store.Run)(nil)).
				Set("status = ?", store.RunCompleted).Set("finished_at = ?", store.Now()).
				Where("task_id = ?", want.Rows.Heads[0].TaskID).Exec(ctx); err != nil {
				return err
			}
			if _, err := tx.NewDelete().Model((*store.RunStateRef)(nil)).
				Where("run_id IN (SELECT id FROM runs WHERE task_id = ?)", want.Rows.Heads[0].TaskID).Exec(ctx); err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, ref := range want.Rows.Refs {
				if ref.BlobHash == "" || seen[ref.BlobHash] {
					continue
				}
				seen[ref.BlobHash] = true
				if _, err := tx.NewInsert().Model(&store.BlobRelease{ID: store.NewID(), Hash: ref.BlobHash, ReleasedAt: store.Now()}).Exec(ctx); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if got, err := retention.Cleanup(t.Context()); err != nil || got.Objects != 0 {
			t.Fatalf("cleanup while the snapshot needs released state = %+v, %v", got, err)
		}
	}})
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if _, err := Backup(t.Context(), BackupOptions{DB: db, StoragePath: storage, Output: archive}); err != nil {
		t.Fatal(err)
	}
	if !interleaved {
		t.Fatal("state was not replaced between the snapshot and object copying")
	}
	if got, err := retention.Cleanup(t.Context()); err != nil || got.Objects != 3 {
		t.Fatalf("cleanup after releasing backup protection = %+v, %v; want three old state objects", got, err)
	}
	if got, err := blob.ReadAll(t.Context(), bs, replacement); err != nil || !bytes.Equal(got, replacementData) {
		t.Fatalf("replacement state was removed: %q, %v", got, err)
	}
	target := config.Database{Driver: config.DatabaseSQLite, DSN: filepath.Join(t.TempDir(), "restored.db")}
	restoredStorage := t.TempDir()
	if _, err := Restore(t.Context(), RestoreOptions{Input: archive, Database: target, StoragePath: restoredStorage}); err != nil {
		t.Fatal(err)
	}
	assertStatePersistence(t, openSQLite(t, target.DSN), newLocal(t, restoredStorage), want)
}
