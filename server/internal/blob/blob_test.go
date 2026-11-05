package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func newLocal(t *testing.T) (*Local, string) {
	t.Helper()
	root := t.TempDir()
	l, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	return l, root
}

func TestLocalPutGetHas(t *testing.T) {
	l, root := newLocal(t)
	ctx := context.Background()
	content := []byte("print('hello')\n")

	hash, err := l.Put(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	// Known SHA-256 of the content, so the naming is pinned.
	if want := Sum(content); hash != want || !ValidHash(hash) {
		t.Fatalf("hash = %s, want %s", hash, want)
	}
	path := filepath.Join(root, "blobs", "sha256", hash[:2], hash)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("object not at %s: %v", path, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != fileMode {
		t.Errorf("object mode = %v, want %v", info.Mode().Perm(), fileMode)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && dir.Mode().Perm() != dirMode {
		t.Errorf("directory mode = %v, want %v", dir.Mode().Perm(), dirMode)
	}

	got, err := ReadAll(ctx, l, hash)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("ReadAll = %q, %v", got, err)
	}
	if ok, err := l.Has(ctx, hash); err != nil || !ok {
		t.Fatalf("Has = %v, %v", ok, err)
	}

	// Storing the same content again is a no-op that returns the same hash.
	again, err := l.Put(ctx, bytes.NewReader(content))
	if err != nil || again != hash {
		t.Fatalf("second Put = %s, %v", again, err)
	}
	// The empty object is an object too.
	empty, err := l.Put(ctx, strings.NewReader(""))
	if err != nil || empty != Sum(nil) {
		t.Fatalf("empty Put = %s, %v", empty, err)
	}
	// No temporary files are left behind.
	if entries, err := os.ReadDir(filepath.Join(root, "blobs", "tmp")); err != nil || len(entries) != 0 {
		t.Fatalf("tmp entries = %v, %v", entries, err)
	}
}

func TestLocalMissingAndInvalid(t *testing.T) {
	l, _ := newLocal(t)
	ctx := context.Background()
	missing := Sum([]byte("never stored"))
	if _, err := l.Get(ctx, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing: %v", err)
	}
	if ok, err := l.Has(ctx, missing); err != nil || ok {
		t.Fatalf("Has missing = %v, %v", ok, err)
	}
	for _, bad := range []string{"", "../../etc/passwd", strings.ToUpper(missing), missing[:63], missing + "0", strings.Repeat("g", 64)} {
		if _, err := l.Get(ctx, bad); !errors.Is(err, ErrInvalidHash) {
			t.Errorf("Get(%q): %v", bad, err)
		}
		if _, err := l.Has(ctx, bad); !errors.Is(err, ErrInvalidHash) {
			t.Errorf("Has(%q): %v", bad, err)
		}
	}
	if _, err := NewLocal(""); err == nil {
		t.Error("NewLocal with an empty path must fail")
	}
}

func TestLocalDelete(t *testing.T) {
	l, _ := newLocal(t)
	ctx := context.Background()
	keep, err := l.Put(ctx, strings.NewReader("task file"))
	if err != nil {
		t.Fatal(err)
	}
	gone, err := l.Put(ctx, strings.NewReader("expired artifact"))
	if err != nil {
		t.Fatal(err)
	}
	// Deleting twice, or deleting what was never stored, succeeds.
	for _, h := range []string{gone, gone, Sum([]byte("never stored"))} {
		if err := l.Delete(ctx, h); err != nil {
			t.Fatalf("Delete(%s): %v", h, err)
		}
	}
	if ok, err := l.Has(ctx, gone); err != nil || ok {
		t.Fatalf("Has after delete = %v, %v", ok, err)
	}
	if ok, err := l.Has(ctx, keep); err != nil || !ok {
		t.Fatalf("Delete removed another object: %v, %v", ok, err)
	}
	// Content stored again after a delete comes back.
	if again, err := l.Put(ctx, strings.NewReader("expired artifact")); err != nil || again != gone {
		t.Fatalf("Put after delete = %s, %v", again, err)
	}
	if got, err := ReadAll(ctx, l, gone); err != nil || string(got) != "expired artifact" {
		t.Fatalf("read after re-put = %q, %v", got, err)
	}
	for _, bad := range []string{"", "../../etc/passwd", strings.Repeat("g", 64)} {
		if err := l.Delete(ctx, bad); !errors.Is(err, ErrInvalidHash) {
			t.Errorf("Delete(%q): %v", bad, err)
		}
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk on fire") }

func TestLocalPutFailureLeavesNothing(t *testing.T) {
	l, root := newLocal(t)
	if _, err := l.Put(context.Background(), io.MultiReader(strings.NewReader("partial"), failingReader{})); err == nil {
		t.Fatal("Put must fail when the reader fails")
	}
	for _, dir := range []string{"tmp", "sha256"} {
		entries, err := os.ReadDir(filepath.Join(root, "blobs", dir))
		if err != nil || len(entries) != 0 {
			t.Fatalf("%s entries after a failed Put = %v, %v", dir, entries, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.Put(ctx, strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put with a cancelled context: %v", err)
	}
}

func TestLocalConcurrentPutsOfSameContent(t *testing.T) {
	l, _ := newLocal(t)
	content := bytes.Repeat([]byte("lerryn "), 10_000)
	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range errs {
		wg.Go(func() {
			var hash string
			hash, errs[i] = l.Put(context.Background(), bytes.NewReader(content))
			if errs[i] == nil && hash != Sum(content) {
				errs[i] = errors.New("wrong hash " + hash)
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	got, err := ReadAll(context.Background(), l, Sum(content))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("content after concurrent puts differs: %v", err)
	}
}
