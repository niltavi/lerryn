package ops

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/store"
)

func restoreTestArchive(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	db := openSQLite(t, filepath.Join(root, "lerryn.db"))
	if _, err := store.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	storage := filepath.Join(root, "storage")
	newLocal(t, storage)
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if _, err := Backup(t.Context(), BackupOptions{DB: db, StoragePath: storage, Output: archive}); err != nil {
		t.Fatal(err)
	}
	return archive
}

func restoreTargetTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		content := ""
		switch {
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path) // #nosec G304 G122 -- this test owns the temporary tree and makes no concurrent filesystem changes
			if err != nil {
				return err
			}
			content = string(data)
		case info.Mode()&os.ModeSymlink != 0:
			if content, err = os.Readlink(path); err != nil {
				return err
			}
		}
		tree[rel] = fmt.Sprintf("%s:%s", info.Mode(), content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestRestoreRejectsOverlappingTargetsBeforeChanges(t *testing.T) {
	archive := restoreTestArchive(t)
	for _, name := range []string{"absolute", "relative", "existing", "symlink", "storage symlink", "file symlink", "case alias", "missing case alias", "missing reverse case alias", "wal", "shm", "same path", "database contains storage"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dbPath := filepath.Join(root, "data", "lerryn.db")
			storagePath := filepath.Join(root, "data")
			switch name {
			case "relative":
				t.Chdir(root)
				dbPath = filepath.Join("data", "lerryn.db")
			case "existing":
				if err := os.MkdirAll(storagePath, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dbPath, []byte("original database"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink", "storage symlink", "file symlink":
				if err := os.MkdirAll(storagePath, 0o750); err != nil {
					t.Fatal(err)
				}
				alias := filepath.Join(root, "alias")
				if err := os.Symlink(storagePath, alias); err != nil {
					t.Skip("symlinks unavailable:", err)
				}
				dbPath = filepath.Join(alias, "nested", "lerryn.db")
				if name == "storage symlink" {
					dbPath, storagePath = filepath.Join(storagePath, "lerryn.db"), alias
				}
				if name == "file symlink" {
					original := filepath.Join(root, "original.db")
					if err := os.WriteFile(original, []byte("original database"), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(original, filepath.Join(storagePath, "lerryn.db")); err != nil {
						t.Fatal(err)
					}
					dbPath = filepath.Join(alias, "lerryn.db")
				}
			case "wal", "shm":
				storagePath = dbPath + "-" + name
			case "case alias", "missing case alias", "missing reverse case alias":
				actual := filepath.Join(root, "store")
				if err := os.Mkdir(actual, 0o750); err != nil {
					t.Fatal(err)
				}
				storagePath = filepath.Join(root, "STORE")
				actualInfo, err := os.Stat(actual)
				if err != nil {
					t.Fatal(err)
				}
				aliasInfo, err := os.Stat(storagePath)
				if err != nil || !os.SameFile(actualInfo, aliasInfo) {
					t.Skip("temporary filesystem distinguishes path case")
				}
				dbPath = filepath.Join(actual, "lerryn.db")
				if name != "case alias" {
					if err := os.Remove(actual); err != nil {
						t.Fatal(err)
					}
				}
				if name == "missing reverse case alias" {
					dbPath = actual
					storagePath = filepath.Join(storagePath, "storage")
				}
			case "same path":
				storagePath = dbPath
			case "database contains storage":
				storagePath = filepath.Join(dbPath, "storage")
			}
			before := restoreTargetTree(t, root)
			res, err := Restore(t.Context(), RestoreOptions{
				Input: archive, Database: config.Database{Driver: config.DatabaseSQLite, DSN: dbPath},
				StoragePath: storagePath, Force: name != "absolute",
			})
			if !errors.Is(err, ErrOverlappingTargets) {
				t.Errorf("Restore error = %v, want overlapping targets rejected", err)
			}
			if len(res.MovedAside) != 0 {
				t.Errorf("Restore moved existing paths: %v", res.MovedAside)
			}
			if after := restoreTargetTree(t, root); !reflect.DeepEqual(after, before) {
				t.Error("rejected restore changed its target directories")
			}
		})
	}
}

func TestRestoreKeepsInputArchiveInTargetPaths(t *testing.T) {
	archive, err := os.ReadFile(restoreTestArchive(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"parent", "storage", "database", "wal", "shm"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dbPath := filepath.Join(root, "lerryn.db")
			// A shared name prefix is not an overlapping directory.
			storagePath := dbPath + "-storage"
			input := filepath.Join(root, "backup.tar.gz")
			movedTarget := ""
			switch name {
			case "storage":
				input, movedTarget = filepath.Join(storagePath, "backup.tar.gz"), storagePath
			case "database":
				input, movedTarget = dbPath, dbPath
			case "wal", "shm":
				input, movedTarget = dbPath+"-"+name, dbPath+"-"+name
			}
			if err := os.MkdirAll(filepath.Dir(input), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(input, archive, 0o600); err != nil { // #nosec G703 -- every input path is below this test's temporary directory
				t.Fatal(err)
			}
			res, err := Restore(t.Context(), RestoreOptions{
				Input: input, Database: config.Database{Driver: config.DatabaseSQLite, DSN: dbPath},
				StoragePath: storagePath, Force: true,
			})
			if err != nil {
				t.Fatalf("Restore: %v", err)
			}
			preserved := input
			if movedTarget != "" {
				preserved = ""
				for _, moved := range res.MovedAside {
					if strings.HasPrefix(moved, movedTarget+".pre-restore-") {
						rel, err := filepath.Rel(movedTarget, input)
						if err != nil {
							t.Fatal(err)
						}
						preserved = filepath.Join(moved, rel)
					}
				}
			}
			if data, err := os.ReadFile(preserved); err != nil || !bytes.Equal(data, archive) { // #nosec G304 -- the restored archive stays in this test's temporary directory
				t.Fatalf("input archive was not preserved at %q: %v", preserved, err)
			}
			db := openSQLite(t, dbPath)
			if err := store.CheckMigrations(t.Context(), db); err != nil {
				t.Fatalf("restored schema: %v", err)
			}
		})
	}
}

func TestRestoreAllowsDistinctCaseSensitiveDirectories(t *testing.T) {
	archive := restoreTestArchive(t)
	root := t.TempDir()
	dbDir, storagePath := filepath.Join(root, "store"), filepath.Join(root, "STORE")
	if err := os.Mkdir(dbDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(storagePath, 0o750); errors.Is(err, os.ErrExist) {
		t.Skip("temporary filesystem does not distinguish path case")
	} else if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "lerryn.db")
	if _, err := Restore(t.Context(), RestoreOptions{
		Input: archive, Database: config.Database{Driver: config.DatabaseSQLite, DSN: dbPath},
		StoragePath: storagePath,
	}); err != nil {
		t.Fatalf("Restore rejected distinct directories: %v", err)
	}
	db := openSQLite(t, dbPath)
	if err := store.CheckMigrations(t.Context(), db); err != nil {
		t.Fatalf("restored schema: %v", err)
	}
}

func TestMoveAsidePreservesExistingBackupNames(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprintf("directory=%v", directory), func(t *testing.T) {
			original := filepath.Join(t.TempDir(), "lerryn")
			legacy := original + ".pre-restore-fixed"
			legacyFile := legacy
			if directory {
				if err := os.Mkdir(legacy, 0o750); err != nil {
					t.Fatal(err)
				}
				legacyFile = filepath.Join(legacy, "data")
			}
			if err := os.WriteFile(legacyFile, []byte("legacy"), 0o600); err != nil {
				t.Fatal(err)
			}
			write := func(content string) {
				t.Helper()
				file := original
				if directory {
					if err := os.Mkdir(original, 0o750); err != nil {
						t.Fatal(err)
					}
					file = filepath.Join(original, "data")
				}
				if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write("first")
			first, err := moveAside(original, ".pre-restore-fixed")
			if err != nil {
				t.Fatal(err)
			}
			write("second")
			second, err := moveAside(original, ".pre-restore-fixed")
			if err != nil {
				t.Fatal(err)
			}
			if first == second {
				t.Fatal("a repeated restore reused the existing preserved path")
			}
			for saved, want := range map[string]string{legacy: "legacy", first: "first", second: "second"} {
				file := saved
				if directory {
					file = filepath.Join(saved, "data")
				}
				if data, err := os.ReadFile(file); err != nil || string(data) != want { // #nosec G304 -- the test owns both temporary preserved paths
					t.Fatalf("preserved data at %s = %q, %v; want %q", saved, data, err, want)
				}
			}
		})
	}
}

func restoreFailureFixture(t *testing.T, archive string) (RestoreOptions, string) {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "database", "lerryn.db")
	storage := filepath.Join(root, "objects", "storage")
	for _, dir := range []string{filepath.Dir(dbPath), storage} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		dbPath: "old database", dbPath + "-wal": "old wal", dbPath + "-shm": "old shm",
		filepath.Join(storage, "old-object"): "old storage",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil { // #nosec G703 -- every fixture path is below the test's temporary directory
			t.Fatal(err)
		}
	}
	return RestoreOptions{
		Input: archive, Database: config.Database{Driver: config.DatabaseSQLite, DSN: dbPath},
		StoragePath: storage, Force: true,
	}, root
}

func TestRestoreRollsBackEveryRenameFailure(t *testing.T) {
	archive := restoreTestArchive(t)
	for _, phase := range []string{"preserve database", "preserve wal", "preserve shm", "preserve storage", "install database", "install storage"} {
		t.Run(phase, func(t *testing.T) {
			opts, root := restoreFailureFixture(t, archive)
			before := restoreTargetTree(t, root)
			injected := errors.New("injected rename failure")
			failed := false
			rename := func(from, to string) error {
				// These parents represent independent file systems. No move
				// may cross between them, including during rollback.
				for _, parent := range []string{filepath.Dir(opts.Database.DSN), filepath.Dir(opts.StoragePath)} {
					contains := func(path string) bool { return strings.HasPrefix(path, parent+string(filepath.Separator)) }
					if contains(from) != contains(to) {
						return errors.New("restore rename crosses target file systems")
					}
				}
				match := false
				switch phase {
				case "preserve database":
					match = from == opts.Database.DSN
				case "preserve wal":
					match = from == opts.Database.DSN+"-wal"
				case "preserve shm":
					match = from == opts.Database.DSN+"-shm"
				case "preserve storage":
					match = from == opts.StoragePath
				case "install database":
					match = to == opts.Database.DSN && strings.HasPrefix(filepath.Base(filepath.Dir(from)), ".lerryn-restore-")
				case "install storage":
					match = to == opts.StoragePath && strings.HasPrefix(filepath.Base(from), ".lerryn-restore-storage-")
				}
				if match && !failed {
					failed = true
					return injected
				}
				return os.Rename(from, to)
			}
			res, err := restoreWithRename(t.Context(), opts, rename)
			if !failed || !errors.Is(err, injected) || errors.Is(err, errRestoreRollback) {
				t.Fatalf("Restore = %+v, %v; want the injected failure with complete rollback", res, err)
			}
			if len(res.MovedAside) != 0 {
				t.Errorf("rolled-back targets are still reported as preserved: %v", res.MovedAside)
			}
			if after := restoreTargetTree(t, root); !reflect.DeepEqual(after, before) {
				t.Fatal("failed restore did not restore the complete previous target tree")
			}
		})
	}
}

func TestRestoreRollbackRemovesNewTargets(t *testing.T) {
	root := t.TempDir()
	opts := RestoreOptions{
		Input: restoreTestArchive(t), Database: config.Database{Driver: config.DatabaseSQLite, DSN: filepath.Join(root, "database", "lerryn.db")},
		StoragePath: filepath.Join(root, "objects", "storage"),
	}
	before := restoreTargetTree(t, root)
	injected := errors.New("cannot install storage")
	_, err := restoreWithRename(t.Context(), opts, func(from, to string) error {
		if to == opts.StoragePath {
			return injected
		}
		return os.Rename(from, to)
	})
	if !errors.Is(err, injected) || errors.Is(err, errRestoreRollback) {
		t.Fatalf("Restore error = %v, want complete rollback", err)
	}
	if after := restoreTargetTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatal("rollback left a new database, storage, or parent directory")
	}
}

func TestRestoreKeepsRecoveryDataWhenRollbackFails(t *testing.T) {
	archive := restoreTestArchive(t)
	for _, phase := range []string{"return database to staging", "restore previous database", "restore previous storage"} {
		t.Run(phase, func(t *testing.T) {
			opts, root := restoreFailureFixture(t, archive)
			installFailure := errors.New("cannot install storage")
			rollbackFailure := errors.New("cannot roll back a target")
			rename := func(from, to string) error {
				if to == opts.StoragePath && strings.HasPrefix(filepath.Base(from), ".lerryn-restore-storage-") {
					return installFailure
				}
				switch phase {
				case "return database to staging":
					if from == opts.Database.DSN && strings.HasPrefix(filepath.Base(filepath.Dir(to)), ".lerryn-restore-") {
						return rollbackFailure
					}
				case "restore previous database":
					if to == opts.Database.DSN && strings.HasPrefix(from, opts.Database.DSN+".pre-restore-") {
						return rollbackFailure
					}
				case "restore previous storage":
					if to == opts.StoragePath && strings.HasPrefix(from, opts.StoragePath+".pre-restore-") {
						return rollbackFailure
					}
				}
				return os.Rename(from, to)
			}
			res, err := restoreWithRename(t.Context(), opts, rename)
			if !errors.Is(err, installFailure) || !errors.Is(err, rollbackFailure) || !errors.Is(err, errRestoreRollback) {
				t.Fatalf("Restore = %+v, %v; want install and rollback failures", res, err)
			}
			if len(res.MovedAside) == 0 || !strings.Contains(err.Error(), "staging retained") {
				t.Fatalf("recovery paths were not reported: %+v, %v", res, err)
			}
			for original, want := range map[string]string{
				opts.Database.DSN: "old database", opts.Database.DSN + "-wal": "old wal", opts.Database.DSN + "-shm": "old shm",
				filepath.Join(opts.StoragePath, "old-object"): "old storage",
			} {
				candidates := []string{original}
				for _, saved := range res.MovedAside {
					for _, target := range []string{opts.Database.DSN, opts.Database.DSN + "-wal", opts.Database.DSN + "-shm", opts.StoragePath} {
						if strings.HasPrefix(saved, target+".pre-restore-") {
							rel, err := filepath.Rel(target, original)
							if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
								candidates = append(candidates, filepath.Join(saved, rel))
							}
						}
					}
				}
				found := false
				for _, path := range candidates {
					if data, err := os.ReadFile(path); err == nil && string(data) == want { // #nosec G304 -- all recovery paths originate in this test's temporary restore
						found = true
						break
					}
				}
				if !found {
					t.Errorf("previous data at %s was lost", original)
				}
			}
			stages := 0
			if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
				if err == nil && entry.IsDir() && strings.HasPrefix(entry.Name(), ".lerryn-restore-") {
					stages++
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if stages != 2 {
				t.Fatalf("kept staging directories = %d, want database and storage", stages)
			}
		})
	}
}
