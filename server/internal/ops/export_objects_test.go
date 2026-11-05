package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

func TestExportValidatesSourceObjects(t *testing.T) {
	source := openSQLite(t, filepath.Join(t.TempDir(), "source.db"))
	if _, err := store.Migrate(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	content := seed(t, source, newLocal(t, t.TempDir()))
	hash := blob.Sum(content)
	want := readSnapshot(t, source)
	for _, kind := range []string{"correct", "missing", "wrong content", "short", "long"} {
		t.Run(kind, func(t *testing.T) {
			storage := t.TempDir()
			bs := newLocal(t, storage)
			if kind != "missing" {
				if _, err := bs.Put(t.Context(), bytes.NewReader(content)); err != nil {
					t.Fatal(err)
				}
				data := append([]byte(nil), content...)
				switch kind {
				case "wrong content":
					data[0] ^= 1
				case "short":
					data = data[:len(data)-1]
				case "long":
					data = append(data, '!')
				}
				path := filepath.Join(storage, "blobs", "sha256", hash[:2], hash)
				if err := os.WriteFile(path, data, 0o600); err != nil { // #nosec G703 -- simulates corruption only in a temporary source store
					t.Fatal(err)
				}
			}
			dir := t.TempDir()
			archive := filepath.Join(dir, "export.tar.gz")
			m, err := Export(t.Context(), ExportOptions{DB: source, Blobs: bs, Output: archive})
			if kind != "correct" && kind != "missing" {
				if err == nil || !strings.Contains(err.Error(), "content hash") {
					t.Fatalf("Export error = %v, want source integrity failure", err)
				}
				if len(m.MissingBlobs) != 0 {
					t.Fatalf("corrupt object was reported missing: %v", m.MissingBlobs)
				}
				assertExportFiles(t, dir)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			assertExportFiles(t, dir, "export.tar.gz")
			if kind == "missing" {
				if m.Blobs != 0 || !reflect.DeepEqual(m.MissingBlobs, []string{hash}) {
					t.Fatalf("missing object manifest: %+v", m)
				}
			} else if m.Blobs != 1 || len(m.MissingBlobs) != 0 {
				t.Fatalf("healthy object manifest: %+v", m)
			}
			target := storetest.OpenEmpty(t)
			targetBlobs := newLocal(t, t.TempDir())
			if _, err := Import(t.Context(), ImportOptions{DB: target, Blobs: targetBlobs, Input: archive}); err != nil {
				t.Fatal(err)
			}
			if got := readSnapshot(t, target); !reflect.DeepEqual(got, want) {
				t.Fatal("imported rows differ from source")
			}
			data, err := blob.ReadAll(t.Context(), targetBlobs, hash)
			if kind == "missing" {
				if !errors.Is(err, blob.ErrNotFound) {
					t.Fatalf("missing imported object: %v", err)
				}
			} else if err != nil || !bytes.Equal(data, content) {
				t.Fatalf("healthy imported object: %v", err)
			}
		})
	}
}

func assertExportFiles(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("output directory contains %v, want %v", names, want)
	}
}

type exportObjectStore struct {
	blob.Store
	get func(context.Context, string) (io.ReadCloser, error)
}

func (s exportObjectStore) Get(ctx context.Context, hash string) (io.ReadCloser, error) {
	return s.get(ctx, hash)
}

type exportObjectBody struct {
	io.Reader
	readErr   error
	closeErr  error
	afterRead func(int, error)
	onClose   func()
	closed    bool
	read      int64
	maxChunk  int
}

func (r *exportObjectBody) Read(p []byte) (int, error) {
	r.maxChunk = max(r.maxChunk, len(p))
	n, err := r.Reader.Read(p)
	r.read += int64(n)
	if r.afterRead != nil {
		r.afterRead(n, err)
	}
	if errors.Is(err, io.EOF) && r.readErr != nil {
		err = r.readErr
	}
	return n, err
}

func (r *exportObjectBody) Close() error {
	r.closed = true
	if r.onClose != nil {
		r.onClose()
	}
	return r.closeErr
}

func TestExportRejectsFailedSourceReads(t *testing.T) {
	source := openSQLite(t, filepath.Join(t.TempDir(), "source.db"))
	if _, err := store.Migrate(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	content := seed(t, source, newLocal(t, t.TempDir()))
	readFailure := errors.New("source read failed")
	closeFailure := errors.New("source close failed")
	for _, kind := range []string{"get", "read", "close", "read and close", "cancel during read", "cancel at EOF", "cancel at close"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			body := &exportObjectBody{Reader: bytes.NewReader(content)}
			want := readFailure
			switch kind {
			case "read", "read and close":
				body.readErr = readFailure
			case "close":
				body.closeErr, want = closeFailure, closeFailure
			case "cancel during read", "cancel at EOF":
				want = context.Canceled
				body.afterRead = func(n int, err error) {
					if kind == "cancel during read" && n > 0 || kind == "cancel at EOF" && errors.Is(err, io.EOF) {
						cancel()
					}
				}
			case "cancel at close":
				body.onClose, want = cancel, context.Canceled
			}
			if kind == "read and close" {
				body.closeErr = closeFailure
			}
			bs := exportObjectStore{get: func(context.Context, string) (io.ReadCloser, error) {
				if kind == "get" {
					return nil, want
				}
				return body, nil
			}}
			dir := t.TempDir()
			m, err := Export(ctx, ExportOptions{DB: source, Blobs: bs, Output: filepath.Join(dir, "export.tar.gz")})
			if !errors.Is(err, want) || kind == "read and close" && !errors.Is(err, closeFailure) {
				t.Fatalf("Export error = %v, want %v", err, want)
			}
			if len(m.MissingBlobs) != 0 {
				t.Fatalf("failed object was reported missing: %v", m.MissingBlobs)
			}
			if kind != "get" && !body.closed {
				t.Fatal("source object was not closed")
			}
			assertExportFiles(t, dir)
		})
	}
}

func TestExportKeepsExistingOutput(t *testing.T) {
	source := openSQLite(t, filepath.Join(t.TempDir(), "source.db"))
	if _, err := store.Migrate(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	content := seed(t, source, newLocal(t, t.TempDir()))
	for _, kind := range []string{"correct", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			data := append([]byte(nil), content...)
			if kind == "corrupt" {
				data[0] ^= 1
			}
			bs := exportObjectStore{get: func(context.Context, string) (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(data)), nil
			}}
			dir := t.TempDir()
			path := filepath.Join(dir, "export.tar.gz")
			const original = "an existing export must be kept"
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Export(t.Context(), ExportOptions{DB: source, Blobs: bs, Output: path}); err == nil {
				t.Fatal("Export accepted an occupied output path")
			}
			got, err := os.ReadFile(path) // #nosec G304 -- reads only this test's temporary output file
			if err != nil || string(got) != original {
				t.Fatalf("existing output changed: %q, %v", got, err)
			}
			assertExportFiles(t, dir, "export.tar.gz")
		})
	}
}

func TestSpoolBlobHonorsContextBeforeReading(t *testing.T) {
	for _, phase := range []string{"before get", "after get", "get reports missing after cancellation"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if phase == "before get" {
				cancel()
			}
			body := &exportObjectBody{Reader: strings.NewReader("object")}
			bs := exportObjectStore{get: func(context.Context, string) (io.ReadCloser, error) {
				if phase == "before get" {
					t.Error("opened source after cancellation")
				}
				cancel()
				if phase == "get reports missing after cancellation" {
					return nil, blob.ErrNotFound
				}
				return body, nil
			}}
			ok, err := spoolBlob(ctx, bs, blob.Sum([]byte("object")), filepath.Join(t.TempDir(), "object"))
			if ok || !errors.Is(err, context.Canceled) || body.read != 0 {
				t.Fatalf("spoolBlob = %v, %v; read %d bytes after cancellation", ok, err, body.read)
			}
			if phase == "after get" && !body.closed {
				t.Fatal("source object was not closed after cancellation")
			}
		})
	}
}

func TestSpoolBlobEmptyObject(t *testing.T) {
	body := &exportObjectBody{Reader: bytes.NewReader(nil)}
	bs := exportObjectStore{get: func(context.Context, string) (io.ReadCloser, error) { return body, nil }}
	path := filepath.Join(t.TempDir(), "empty")
	if ok, err := spoolBlob(t.Context(), bs, blob.Sum(nil), path); err != nil || !ok || !body.closed {
		t.Fatalf("empty object: copied=%v closed=%v error=%v", ok, body.closed, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != 0 {
		t.Fatalf("empty staged object: %v, %v", info, err)
	}
}

func TestSpoolBlobClosesSourceWhenStageCreationFails(t *testing.T) {
	closeFailure := errors.New("source close failed")
	body := &exportObjectBody{Reader: strings.NewReader("object"), closeErr: closeFailure}
	bs := exportObjectStore{get: func(context.Context, string) (io.ReadCloser, error) { return body, nil }}
	path := filepath.Join(t.TempDir(), "missing", "object")
	ok, err := spoolBlob(t.Context(), bs, blob.Sum([]byte("object")), path)
	if ok || !errors.Is(err, os.ErrNotExist) || !errors.Is(err, closeFailure) || !body.closed || body.read != 0 {
		t.Fatalf("failed stage creation: copied=%v closed=%v read=%d error=%v", ok, body.closed, body.read, err)
	}
}

func TestSpoolBlobStreamsLargeObjects(t *testing.T) {
	const size = 32 << 20
	digest := sha256.New()
	if _, err := io.Copy(digest, &generatedImportObject{remaining: size}); err != nil {
		t.Fatal(err)
	}
	hash := hex.EncodeToString(digest.Sum(nil))
	body := &exportObjectBody{Reader: &generatedImportObject{remaining: size}}
	bs := exportObjectStore{get: func(context.Context, string) (io.ReadCloser, error) { return body, nil }}
	path := filepath.Join(t.TempDir(), "object")
	if ok, err := spoolBlob(t.Context(), bs, hash, path); err != nil || !ok {
		t.Fatalf("spoolBlob = %v, %v", ok, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != size || !body.closed || body.read != size || body.maxChunk > 64<<10 {
		t.Fatalf("large object copy: size=%d closed=%v read=%d max_chunk=%d", info.Size(), body.closed, body.read, body.maxChunk)
	}
}
