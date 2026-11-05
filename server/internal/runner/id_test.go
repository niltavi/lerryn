package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadIDIsStable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runner")
	first, err := LoadID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !validID.MatchString(first) {
		t.Fatalf("generated id %q is not a valid runner id", first)
	}
	second, err := LoadID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("id changed across loads: %q then %q", first, second)
	}
}

func TestLoadIDRejectsAnInvalidFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, idFile), []byte("../x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadID(dir); err == nil {
		t.Fatal("LoadID accepted an invalid id")
	}
}
