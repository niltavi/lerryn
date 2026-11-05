//go:build unix

package runner

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

func mustParseUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestWriteTaskFiles(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	dir := filepath.Join(t.TempDir(), "task")
	if err := mkdirReadable(dir); err != nil {
		t.Fatal(err)
	}
	err := writeTaskFiles(dir, []runproto.File{
		{Path: "main.py", Content: []byte("print(1)")},
		{Path: "lib/util/helpers.py", Content: []byte("x = 1")},
		{Path: "run.sh", Content: []byte("#!/bin/sh"), Executable: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]fs.FileMode{
		".": 0o755, "main.py": 0o644, "lib": 0o755, "lib/util": 0o755, "lib/util/helpers.py": 0o644, "run.sh": 0o755,
	}
	for name, mode := range want {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != mode {
			t.Errorf("%s has mode %v, want %v despite umask 077", name, fi.Mode().Perm(), mode)
		}
	}

	for _, bad := range []string{"../escape.py", "/abs.py", "a//b.py", "a/./b.py", ".", "", "a\\b.py"} {
		d := t.TempDir()
		if err := writeTaskFiles(d, []runproto.File{{Path: bad, Content: []byte("x")}}); err == nil {
			t.Errorf("writeTaskFiles accepted path %q", bad)
		}
	}
}

func TestWritableDirs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "work")
	if err := mkdirWritable(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := fs.FileMode(0o777)
	if euid := os.Geteuid(); euid == 0 || euid == taskUID {
		want = 0o700
	}
	if fi.Mode().Perm() != want {
		t.Fatalf("mode %v, want %v for euid %d", fi.Mode().Perm(), want, os.Geteuid())
	}
}
