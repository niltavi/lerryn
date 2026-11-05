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

func importObjectArchive(t *testing.T) (string, []byte, snapshot) {
	t.Helper()
	source := openSQLite(t, filepath.Join(t.TempDir(), "lerryn.db"))
	if _, err := store.Migrate(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	bs := newLocal(t, t.TempDir())
	content := seed(t, source, bs)
	archive := filepath.Join(t.TempDir(), "export.tar.gz")
	if _, err := Export(t.Context(), ExportOptions{DB: source, Blobs: bs, Output: archive}); err != nil {
		t.Fatal(err)
	}
	return archive, content, readSnapshot(t, source)
}

func TestImportVerifiesPersistedObjectsBeforeCommittingRows(t *testing.T) {
	archive, content, want := importObjectArchive(t)
	hash := blob.Sum(content)
	for _, existing := range []string{"none", "correct", "wrong content", "short", "long"} {
		t.Run(existing, func(t *testing.T) {
			target := storetest.OpenEmpty(t)
			storage := t.TempDir()
			bs := newLocal(t, storage)
			path := filepath.Join(storage, "blobs", "sha256", hash[:2], hash)
			var before os.FileInfo
			var damaged []byte
			if existing != "none" {
				if _, err := bs.Put(t.Context(), bytes.NewReader(content)); err != nil {
					t.Fatal(err)
				}
				damaged = append([]byte(nil), content...)
				switch existing {
				case "wrong content":
					damaged[0] ^= 1
				case "short":
					damaged = damaged[:len(damaged)-1]
				case "long":
					damaged = append(damaged, '!')
				}
				if err := os.WriteFile(path, damaged, 0o600); err != nil { // #nosec G703 -- simulates disk corruption only in the test's temporary object store
					t.Fatal(err)
				}
				var err error
				before, err = os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err := Import(t.Context(), ImportOptions{DB: target, Blobs: bs, Input: archive})
			if existing == "none" || existing == "correct" {
				if err != nil {
					t.Fatal(err)
				}
				if got := readSnapshot(t, target); !reflect.DeepEqual(got, want) {
					t.Fatal("successful import changed the source rows")
				}
				if data, err := blob.ReadAll(t.Context(), bs, hash); err != nil || !bytes.Equal(data, content) {
					t.Fatalf("imported object = %q, %v", data, err)
				}
				if before != nil {
					after, err := os.Stat(path)
					if err != nil || !os.SameFile(before, after) {
						t.Fatalf("correct existing object was replaced: %v", err)
					}
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "stored object") {
				t.Fatalf("Import error = %v, want persisted object integrity failure", err)
			}
			counts, err := TableCounts(t.Context(), target)
			if err != nil {
				t.Fatal(err)
			}
			for table, rows := range counts {
				if rows != 0 {
					t.Errorf("failed import committed %d rows in %s", rows, table)
				}
			}
			if data, err := blob.ReadAll(t.Context(), bs, hash); err != nil || !bytes.Equal(data, damaged) {
				t.Fatalf("rejected import changed the existing object: %v", err)
			}
		})
	}
}

type importObjectStore struct {
	put        func(context.Context, io.Reader) (string, error)
	get        func(context.Context, string) (io.ReadCloser, error)
	puts, gets int
}

func (s *importObjectStore) Put(ctx context.Context, r io.Reader) (string, error) {
	s.puts++
	if s.put != nil {
		return s.put(ctx, r)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (s *importObjectStore) Get(ctx context.Context, hash string) (io.ReadCloser, error) {
	s.gets++
	return s.get(ctx, hash)
}

func (*importObjectStore) Has(context.Context, string) (bool, error) { return false, nil }

type importObjectBody struct {
	io.Reader
	closeErr error
	closed   bool
	read     int64
	maxChunk int
}

func (r *importObjectBody) Read(p []byte) (int, error) {
	r.maxChunk = max(r.maxChunk, len(p))
	n, err := r.Reader.Read(p)
	r.read += int64(n)
	return n, err
}

func (r *importObjectBody) Close() error {
	r.closed = true
	return r.closeErr
}

type importReadError struct{ err error }

func (r importReadError) Read([]byte) (int, error) { return 0, r.err }

type fragmentedImportReader struct{ r io.Reader }

func (r fragmentedImportReader) Read(p []byte) (int, error) {
	return r.r.Read(p[:min(len(p), 1)])
}

type generatedImportObject struct{ remaining int64 }

func (r *generatedImportObject) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), r.remaining))
	clear(p[:n])
	r.remaining -= int64(n)
	return n, nil
}

func TestImportBlobChecksStoredReadAndClose(t *testing.T) {
	content := []byte("stored object")
	hash := blob.Sum(content)
	injected := errors.New("object read or close failed")
	for _, kind := range []string{"correct", "fragmented", "empty", "short", "long", "wrong content", "get failure", "read failure", "close failure"} {
		t.Run(kind, func(t *testing.T) {
			data, expected := content, hash
			body := &importObjectBody{Reader: bytes.NewReader(content)}
			var want error
			switch kind {
			case "fragmented":
				body.Reader = fragmentedImportReader{r: bytes.NewReader(content)}
			case "empty":
				data, expected = nil, blob.Sum(nil)
				body.Reader = bytes.NewReader(nil)
			case "short":
				body.Reader = bytes.NewReader(content[:len(content)-1])
			case "long":
				body.Reader = io.MultiReader(bytes.NewReader(content), &generatedImportObject{remaining: 1 << 30})
			case "wrong content":
				body.Reader = bytes.NewReader(bytes.Repeat([]byte{'x'}, len(content)))
			case "get failure":
				want = blob.ErrNotFound
			case "read failure":
				body.Reader = io.MultiReader(bytes.NewReader(content[:3]), importReadError{err: injected})
				want = injected
			case "close failure":
				body.closeErr, want = injected, injected
			}
			bs := &importObjectStore{get: func(context.Context, string) (io.ReadCloser, error) {
				if kind == "get failure" {
					return nil, want
				}
				return body, nil
			}}
			err := importBlob(t.Context(), bs, expected, fragmentedImportReader{r: bytes.NewReader(data)}, int64(len(data)))
			valid := kind == "correct" || kind == "fragmented" || kind == "empty"
			if (err == nil) != valid || want != nil && !errors.Is(err, want) {
				t.Fatalf("importBlob error = %v; valid=%v, want cause %v", err, valid, want)
			}
			if kind != "get failure" && !body.closed {
				t.Fatal("stored object was not closed")
			}
			if body.read > int64(len(data))+1 {
				t.Fatalf("read %d bytes from storage beyond size %d+1", body.read, len(data))
			}
			if kind == "long" && body.read != int64(len(data))+1 {
				t.Fatalf("oversized object read = %d, want the bounded extra byte", body.read)
			}
		})
	}
}

func TestImportBlobValidatesInputBeforeStoredRead(t *testing.T) {
	content := []byte("archive object")
	hash := blob.Sum(content)
	injected := errors.New("cannot store object")
	for _, kind := range []string{"invalid hash", "negative size", "excessive size", "short input", "wrong input", "put failure"} {
		t.Run(kind, func(t *testing.T) {
			expected, size, data := hash, int64(len(content)), content
			bs := &importObjectStore{get: func(context.Context, string) (io.ReadCloser, error) {
				t.Error("invalid input reached persisted object verification")
				return io.NopCloser(bytes.NewReader(content)), nil
			}}
			switch kind {
			case "invalid hash":
				expected = "invalid"
			case "negative size":
				size = -1
			case "excessive size":
				size = maxEntryBytes + 1
			case "short input":
				data = content[:len(content)-1]
			case "wrong input":
				data = bytes.Repeat([]byte{'x'}, len(content))
			case "put failure":
				bs.put = func(context.Context, io.Reader) (string, error) { return "", injected }
			}
			if err := importBlob(t.Context(), bs, expected, bytes.NewReader(data), size); err == nil || kind == "put failure" && !errors.Is(err, injected) {
				t.Fatalf("importBlob error = %v, want input failure", err)
			}
			if bs.gets != 0 {
				t.Fatalf("Get calls = %d after invalid input", bs.gets)
			}
			if (kind == "invalid hash" || kind == "negative size" || kind == "excessive size") && bs.puts != 0 {
				t.Fatalf("Put calls = %d after invalid metadata", bs.puts)
			}
		})
	}
}

type cancellingImportReader struct {
	r      io.Reader
	cancel context.CancelFunc
}

func (r cancellingImportReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.cancel()
	return n, err
}

func TestImportBlobHonorsContext(t *testing.T) {
	content := []byte("object")
	for _, phase := range []string{"before input", "during input", "during stored read"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			input := io.Reader(bytes.NewReader(content))
			body := &importObjectBody{Reader: bytes.NewReader(content)}
			switch phase {
			case "before input":
				cancel()
			case "during input":
				input = cancellingImportReader{r: input, cancel: cancel}
			case "during stored read":
				body.Reader = cancellingImportReader{r: body.Reader, cancel: cancel}
			}
			bs := &importObjectStore{get: func(context.Context, string) (io.ReadCloser, error) { return body, nil }}
			if err := importBlob(ctx, bs, blob.Sum(content), input, int64(len(content))); !errors.Is(err, context.Canceled) {
				t.Fatalf("importBlob error = %v, want context cancellation", err)
			}
			if phase == "before input" && bs.puts != 0 || phase != "during stored read" && bs.gets != 0 {
				t.Fatalf("unexpected store access after cancellation: %d Put, %d Get", bs.puts, bs.gets)
			}
			if phase == "during stored read" && !body.closed {
				t.Fatal("cancelled stored read was not closed")
			}
		})
	}
}

func TestImportBlobStreamsLargeStoredObject(t *testing.T) {
	const size = 32 << 20
	digest := sha256.New()
	if _, err := io.Copy(digest, &generatedImportObject{remaining: size}); err != nil {
		t.Fatal(err)
	}
	hash := hex.EncodeToString(digest.Sum(nil))
	body := &importObjectBody{Reader: &generatedImportObject{remaining: size}}
	bs := &importObjectStore{get: func(context.Context, string) (io.ReadCloser, error) { return body, nil }}
	if err := importBlob(t.Context(), bs, hash, &generatedImportObject{remaining: size}, size); err != nil {
		t.Fatal(err)
	}
	if !body.closed || body.read != size || body.maxChunk > 64<<10 {
		t.Fatalf("large object read: closed=%v bytes=%d max_chunk=%d", body.closed, body.read, body.maxChunk)
	}
}
