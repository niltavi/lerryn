package ops

import (
	"bytes"
	"compress/gzip"
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
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

func gzipTestBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func ungzipTestBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func integrityArchive(t *testing.T, format string) ([]byte, snapshot) {
	t.Helper()
	source := openSQLite(t, filepath.Join(t.TempDir(), "lerryn.db"))
	if _, err := store.Migrate(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	storage := t.TempDir()
	sourceBlobs := newLocal(t, storage)
	seed(t, source, sourceBlobs)
	archive := filepath.Join(t.TempDir(), format+".tar.gz")
	var err error
	switch format {
	case "backup":
		_, err = Backup(t.Context(), BackupOptions{DB: source, StoragePath: storage, Output: archive})
	case "export":
		_, err = Export(t.Context(), ExportOptions{DB: source, Blobs: sourceBlobs, Output: archive})
	default:
		t.Fatalf("unknown archive format %q", format)
	}
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(archive) // #nosec G304 -- archive was created in this test's temporary directory
	if err != nil {
		t.Fatal(err)
	}
	return data, readSnapshot(t, source)
}

func damagedArchiveBytes(t *testing.T, good []byte, damage string) ([]byte, error) {
	t.Helper()
	data := append([]byte(nil), good...)
	switch damage {
	case "crc":
		data[len(data)-8] ^= 0xff
		return data, gzip.ErrChecksum
	case "truncated trailer":
		return data[:len(data)-8], io.ErrUnexpectedEOF
	case "trailing garbage":
		return append(data, []byte("not-a-gzip-member")...), gzip.ErrHeader
	case "corrupt trailing member":
		tail := gzipTestBytes(t, bytes.Repeat([]byte{0}, 512))
		tail[len(tail)-8] ^= 0xff
		return append(data, tail...), gzip.ErrChecksum
	case "truncated trailing member":
		tail := gzipTestBytes(t, bytes.Repeat([]byte{0}, 512))
		return append(data, tail[:len(tail)-8]...), io.ErrUnexpectedEOF
	default:
		t.Fatalf("unknown archive damage %q", damage)
		return nil, nil
	}
}

func writeIntegrityArchive(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRestoreRejectsDamagedGzipBeforeExchangingTargets(t *testing.T) {
	archive, _ := integrityArchive(t, "backup")
	for _, damage := range []string{"crc", "truncated trailer", "trailing garbage", "corrupt trailing member", "truncated trailing member"} {
		t.Run(damage, func(t *testing.T) {
			data, want := damagedArchiveBytes(t, archive, damage)
			opts, root := restoreFailureFixture(t, writeIntegrityArchive(t, data))
			before := restoreTargetTree(t, root)
			res, err := Restore(t.Context(), opts)
			if !errors.Is(err, want) {
				t.Fatalf("Restore error = %v, want %v", err, want)
			}
			if len(res.MovedAside) > 0 {
				t.Errorf("Restore moved data before verifying gzip: %v", res.MovedAside)
			}
			if after := restoreTargetTree(t, root); !reflect.DeepEqual(after, before) {
				t.Fatal("damaged archive changed the restore targets")
			}
		})
	}
}

func TestImportRollsBackRowsWhenGzipTailIsDamaged(t *testing.T) {
	good, _ := integrityArchive(t, "export")
	for _, damage := range []string{"crc", "truncated trailer", "trailing garbage", "corrupt trailing member", "truncated trailing member"} {
		t.Run(damage, func(t *testing.T) {
			data, want := damagedArchiveBytes(t, good, damage)
			target := storetest.OpenEmpty(t)
			_, err := Import(t.Context(), ImportOptions{
				DB: target, Blobs: newLocal(t, t.TempDir()), Input: writeIntegrityArchive(t, data),
			})
			if !errors.Is(err, want) {
				t.Fatalf("Import error = %v, want %v", err, want)
			}
			counts, err := TableCounts(t.Context(), target)
			if err != nil {
				t.Fatal(err)
			}
			for table, rows := range counts {
				if rows != 0 {
					t.Errorf("corrupt archive committed %d rows in %s", rows, table)
				}
			}
		})
	}
}

func TestArchiveCompressionCompatibility(t *testing.T) {
	for _, format := range []string{"backup", "export"} {
		t.Run(format, func(t *testing.T) {
			good, want := integrityArchive(t, format)
			raw := ungzipTestBytes(t, good)
			split := len(raw) / 2
			encodings := map[string][]byte{
				"original": good,
				"tar record padding": gzipTestBytes(t, append(append([]byte(nil), raw...),
					bytes.Repeat([]byte{0}, 20*512)...)),
				"tar split across gzip members": append(gzipTestBytes(t, raw[:split]), gzipTestBytes(t, raw[split:])...),
				"trailing gzip padding":         append(append([]byte(nil), good...), gzipTestBytes(t, bytes.Repeat([]byte{0}, 512))...),
				// Tar has always ignored data after its end marker. Keep
				// that behavior while checking every gzip member's CRC.
				"trailing compressed data": append(append([]byte(nil), good...), gzipTestBytes(t, []byte("ignored data"))...),
			}
			for name, data := range encodings {
				t.Run(name, func(t *testing.T) {
					input := writeIntegrityArchive(t, data)
					storage := t.TempDir()
					var target *store.DB
					if format == "backup" {
						dsn := filepath.Join(t.TempDir(), "lerryn.db")
						if _, err := Restore(t.Context(), RestoreOptions{
							Input: input, Database: config.Database{Driver: config.DatabaseSQLite, DSN: dsn}, StoragePath: storage,
						}); err != nil {
							t.Fatal(err)
						}
						target = openSQLite(t, dsn)
					} else {
						target = storetest.OpenEmpty(t)
						if _, err := Import(t.Context(), ImportOptions{DB: target, Blobs: newLocal(t, storage), Input: input}); err != nil {
							t.Fatal(err)
						}
					}
					if got := readSnapshot(t, target); !reflect.DeepEqual(got, want) {
						t.Fatal("valid encoding changed the restored database")
					}
					hash := want.Files[0].BlobHash
					if content, err := blob.ReadAll(t.Context(), newLocal(t, storage), hash); err != nil || blob.Sum(content) != hash {
						t.Fatalf("restored object does not match %s: %v", hash, err)
					}
				})
			}
		})
	}
}

type cancelArchiveTailReader struct {
	r      io.Reader
	cancel context.CancelFunc
}

func (r cancelArchiveTailReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.cancel()
	return n, err
}

func TestArchiveTailValidationHonorsContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.tar.gz")
	a, err := createArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.addBytes("payload", []byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := a.close(); err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(path) // #nosec G304 -- path is this test's temporary source archive
	if err != nil {
		t.Fatal(err)
	}
	raw := ungzipTestBytes(t, good)
	padded := gzipTestBytes(t, append(raw, bytes.Repeat([]byte{0}, 64<<10)...))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ar, err := openArchive(ctx, writeIntegrityArchive(t, padded))
	if err != nil {
		t.Fatal(err)
	}
	defer ar.close()
	_, _, payload, err := ar.next()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, payload); err != nil {
		t.Fatal(err)
	}
	// Tar still reads its end marker through the original stream. Cancel
	// only after compression-tail validation has begun reading padding.
	ar.body = cancelArchiveTailReader{r: ar.body, cancel: cancel}
	if _, _, _, err := ar.next(); !errors.Is(err, context.Canceled) {
		t.Fatalf("tail validation error = %v, want context cancellation", err)
	}
}
