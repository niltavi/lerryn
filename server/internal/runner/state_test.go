package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

func stateExec(t *testing.T) *runExec {
	t.Helper()
	return &runExec{dir: t.TempDir(), run: &activeRun{stop: make(chan struct{})}}
}

func stateSnapshot(entries ...runproto.StateEntry) runproto.StateSnapshot {
	return runproto.StateSnapshot{Version: runproto.StateFormatVersion, Entries: entries}
}

func TestStateFilesystemRoundTrip(t *testing.T) {
	x := stateExec(t)
	want := stateSnapshot(
		runproto.StateEntry{Path: "nested/cursor", Content: []byte{0, 1, 255}},
		runproto.StateEntry{Path: "empty", Directory: true},
		runproto.StateEntry{Path: "nested", Directory: true},
		runproto.StateEntry{Path: "运行.sh", Executable: true, Content: []byte("#!/bin/sh\n")},
		runproto.StateEntry{Path: "zero"},
	)
	if err := x.restoreState(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	got, err := x.collectState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := runproto.EncodeState(want)
	b, _, _ := runproto.EncodeState(got)
	if !bytes.Equal(a, b) {
		t.Fatal("collected state differs from the complete installed snapshot")
	}
	fileMode, execMode, directoryMode := fs.FileMode(0o600), fs.FileMode(0o700), fs.FileMode(0o700)
	if uid := os.Geteuid(); uid != 0 && uid != taskUID {
		fileMode, execMode, directoryMode = 0o666, 0o777, 0o777
	}
	for name, mode := range map[string]fs.FileMode{"zero": fileMode, "nested/cursor": fileMode, "运行.sh": execMode, "empty": directoryMode, ".": directoryMode} {
		info, err := os.Stat(filepath.Join(x.dir, stateDirName, name))
		if err != nil || info.Mode().Perm() != mode || info.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
			t.Errorf("state entry %q has unexpected access mode, stat error=%v", name, err)
		}
	}
}

func TestStateRestoreRejectsWholeSnapshot(t *testing.T) {
	for _, entries := range [][]runproto.StateEntry{
		{{Path: "../escape", Content: []byte("bad")}},
		{{Path: "/escape"}},
		{{Path: "a/b"}},
		{{Path: "a"}, {Path: "a/b"}},
		{{Path: "duplicate"}, {Path: "duplicate"}},
		{{Path: "dir", Directory: true, Content: []byte("bad")}},
		{{Path: "dir", Directory: true, Executable: true}},
	} {
		x := stateExec(t)
		if err := x.restoreState(t.Context(), stateSnapshot(entries...)); !errors.Is(err, runproto.ErrRejected) {
			t.Errorf("invalid state snapshot was not rejected: %v", err)
		}
		left, err := os.ReadDir(x.dir)
		if err != nil || len(left) != 0 {
			t.Fatalf("invalid state left files or staging behind: %d, %v", len(left), err)
		}
	}
	x := stateExec(t)
	if err := os.Mkdir(filepath.Join(x.dir, stateDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(x.dir, stateDirName, "keep")
	if err := os.WriteFile(keep, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := x.restoreState(t.Context(), stateSnapshot(runproto.StateEntry{Path: "new"})); err == nil {
		t.Fatal("state installation replaced an existing destination")
	}
	data, err := os.ReadFile(keep) // #nosec G304 -- a file in this test's private state directory
	if err != nil || string(data) != "original" {
		t.Fatal("failed installation changed the existing directory")
	}
}

func TestStateFilesystemAliases(t *testing.T) {
	for _, names := range [][2]string{{"Case", "case"}, {"é", "e\u0301"}} {
		x := stateExec(t)
		probe := filepath.Join(x.dir, names[0])
		if err := os.WriteFile(probe, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_, aliasErr := os.Stat(filepath.Join(x.dir, names[1]))
		if err := os.Remove(probe); err != nil {
			t.Fatal(err)
		}
		err := x.restoreState(t.Context(), stateSnapshot(runproto.StateEntry{Path: names[0]}, runproto.StateEntry{Path: names[1]}))
		if aliasErr == nil {
			if !errors.Is(err, runproto.ErrRejected) {
				t.Fatal("filesystem alias did not reject the complete state")
			}
			if _, err := os.Lstat(filepath.Join(x.dir, stateDirName)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal("filesystem alias installed a partial state")
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func TestStateCollectionBoundaries(t *testing.T) {
	for _, extra := range []int64{0, 1} {
		t.Run(fmt.Sprintf("bytes+%d", extra), func(t *testing.T) {
			x := stateExec(t)
			if err := x.restoreState(t.Context(), stateSnapshot()); err != nil {
				t.Fatal(err)
			}
			file, err := os.Create(filepath.Join(x.dir, stateDirName, "data")) // #nosec G304 -- a sparse fixture in the test's state directory
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(int64(runproto.MaxStateBytes) + extra); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			snapshot, err := x.collectState(t.Context())
			if extra == 0 {
				if err != nil || len(snapshot.Entries) != 1 || len(snapshot.Entries[0].Content) != runproto.MaxStateBytes {
					t.Fatalf("exact content limit: %v", err)
				}
			} else if !errors.Is(err, runproto.ErrRejected) {
				t.Fatalf("content over limit: %v", err)
			}
		})
	}
	for _, count := range []int{runproto.MaxStateEntries, runproto.MaxStateEntries + 1} {
		t.Run(fmt.Sprintf("entries=%d", count), func(t *testing.T) {
			x := stateExec(t)
			if err := x.restoreState(t.Context(), stateSnapshot()); err != nil {
				t.Fatal(err)
			}
			for i := range count {
				if err := os.WriteFile(filepath.Join(x.dir, stateDirName, fmt.Sprintf("file-%04d", i)), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, err := x.collectState(t.Context())
			if count == runproto.MaxStateEntries && (err != nil || len(snapshot.Entries) != count) {
				t.Fatalf("exact entry limit: %v", err)
			}
			if count > runproto.MaxStateEntries && !errors.Is(err, runproto.ErrRejected) {
				t.Fatalf("entry count over limit: %v", err)
			}
		})
	}
	x := stateExec(t)
	if err := x.restoreState(t.Context(), stateSnapshot(runproto.StateEntry{Path: "dir", Directory: true})); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(x.dir, stateDirName, "dir", strings.Repeat("x", runproto.MaxStatePathBytes)), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := x.collectState(t.Context()); !errors.Is(err, runproto.ErrRejected) {
		t.Fatalf("path over limit: %v", err)
	}
}

func TestStateCollectionRejectsUnsafeAndMissing(t *testing.T) {
	for _, mode := range []string{"empty", "deleted", "file", "link inside", "link outside", "link missing"} {
		t.Run(mode, func(t *testing.T) {
			x := stateExec(t)
			if err := x.restoreState(t.Context(), stateSnapshot(runproto.StateEntry{Path: "keep", Content: []byte("safe")})); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(x.dir, stateDirName)
			switch mode {
			case "empty":
				if err := os.Remove(filepath.Join(dir, "keep")); err != nil {
					t.Fatal(err)
				}
			case "deleted", "file":
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
				if mode == "file" {
					if err := os.WriteFile(dir, nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			default:
				target := "keep"
				switch mode {
				case "link outside":
					target = filepath.Join(t.TempDir(), "outside")
					if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "link missing":
					target = "missing"
				}
				if err := os.Symlink(target, filepath.Join(dir, "link")); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, err := x.collectState(t.Context())
			if mode == "empty" {
				if err != nil || len(snapshot.Entries) != 0 {
					t.Fatalf("empty complete state: %v", err)
				}
			} else if err == nil {
				t.Fatal("unsafe or missing state was accepted as complete")
			}
		})
	}
}

func TestStateFilesystemCancellationAndLease(t *testing.T) {
	x := stateExec(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := x.restoreState(ctx, stateSnapshot()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled restoration: %v", err)
	}
	if _, err := x.collectState(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled collection: %v", err)
	}
	x.run.markLost()
	if err := x.restoreState(t.Context(), stateSnapshot()); !errors.Is(err, runproto.ErrLeaseLost) {
		t.Fatalf("lost lease restoration: %v", err)
	}
	entries, err := os.ReadDir(x.dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("cancelled or lost state installation had side effects")
	}
}
