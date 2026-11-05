package ops

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Output is a filesystem path, so punctuation in its parents must not be
// interpreted as parameters when opening the staged database snapshot.
func TestBackupOutputPathPreservesSnapshotObjects(t *testing.T) {
	ctx := context.Background()
	src := openSQLite(t, filepath.Join(t.TempDir(), "source.db"))
	if _, err := store.Migrate(ctx, src); err != nil {
		t.Fatal(err)
	}
	sourceStorage := filepath.Join(t.TempDir(), "source-storage")
	content := seed(t, src, newLocal(t, sourceStorage))
	want := readSnapshot(t, src)
	hash := want.Files[0].BlobHash
	objectEntry := "storage/blobs/sha256/" + hash[:2] + "/" + hash
	for _, parent := range []string{"backups?local", "backups#local", "backups%20local", "backups %?#中文"} {
		t.Run(parent, func(t *testing.T) {
			outputRoot := t.TempDir()
			output := filepath.Join(outputRoot, parent, "backup.tar.gz")
			res, err := Backup(ctx, BackupOptions{DB: src, StoragePath: sourceStorage, Output: output})
			if err != nil {
				t.Fatalf("Backup: %v", err)
			}
			if !res.Manifest.StorageIncluded || res.StorageFiles != 1 {
				t.Fatalf("backup omitted the snapshot's referenced object: included=%t files=%d, want true/1", res.Manifest.StorageIncluded, res.StorageFiles)
			}
			entries, err := os.ReadDir(outputRoot)
			if err != nil || len(entries) != 1 || entries[0].Name() != parent || !entries[0].IsDir() {
				t.Fatalf("backup created a stray database beside its output parent: %v, %v", entries, err)
			}
			ar, err := openArchive(ctx, output)
			if err != nil {
				t.Fatal(err)
			}
			objects := 0
			for {
				name, _, r, err := ar.next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					ar.close()
					t.Fatal(err)
				}
				if name == objectEntry {
					data, err := io.ReadAll(r)
					if err != nil || !bytes.Equal(data, content) {
						ar.close()
						t.Fatalf("archived object=%q, %v", data, err)
					}
					objects++
				}
			}
			ar.close()
			if objects != 1 {
				t.Fatalf("archive contains %d expected objects, want1", objects)
			}

			restoredPath := filepath.Join(t.TempDir(), "restored.db")
			restoredStorage := filepath.Join(t.TempDir(), "restored-storage")
			restored, err := Restore(ctx, RestoreOptions{Input: output, Database: config.Database{Driver: config.DatabaseSQLite, DSN: restoredPath}, StoragePath: restoredStorage})
			if err != nil {
				t.Fatalf("Restore: %v", err)
			}
			if restored.StorageFiles != 1 {
				t.Fatalf("restored object count=%d, want1", restored.StorageFiles)
			}
			db := openSQLite(t, restoredPath)
			if got := readSnapshot(t, db); !reflect.DeepEqual(got, want) {
				t.Fatal("restored rows differ from the source snapshot")
			}
			if data, err := blob.ReadAll(ctx, newLocal(t, restoredStorage), hash); err != nil || !bytes.Equal(data, content) {
				t.Fatalf("restored object=%q, %v", data, err)
			}
		})
	}
}
