// Package ops implements the maintenance commands of tech design 15.5:
// the online backup and restore of a single-node SQLite deployment, and
// the export and import of a whole instance in a portable format that
// also moves a SQLite deployment to PostgreSQL.
//
// The commands work on the database and the object store directly and
// run while the server is stopped, except Backup, which is safe to run
// against a live server.
package ops

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Permissions of what ops writes. Archives and restored data hold task
// code, password hashes and credential ciphertext, so nothing is
// readable by other users.
const (
	dirMode  os.FileMode = 0o750
	fileMode os.FileMode = 0o640
)

// maxEntryBytes caps a single archive entry when reading, as a guard
// against a corrupt or hostile archive filling the disk. It is far above
// any database or object lerryn writes.
const maxEntryBytes = 1 << 40

// archiveWriter writes a gzip-compressed tar file.
type archiveWriter struct {
	f   *os.File
	gz  *gzip.Writer
	tw  *tar.Writer
	now time.Time
}

// createArchive creates the archive at path, which must not exist yet: a
// backup never overwrites another one.
func createArchive(path string) (*archiveWriter, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode) // #nosec G304 -- the operator chooses the output path
	if err != nil {
		return nil, fmt.Errorf("create archive: %w", err)
	}
	gz := gzip.NewWriter(f)
	return &archiveWriter{f: f, gz: gz, tw: tar.NewWriter(gz), now: time.Now().UTC()}, nil
}

// addBytes adds a regular file with the given content.
func (a *archiveWriter) addBytes(name string, data []byte) error {
	hdr := &tar.Header{Name: name, Mode: int64(fileMode), Size: int64(len(data)), ModTime: a.now, Typeflag: tar.TypeReg}
	if err := a.tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("archive %s: %w", name, err)
	}
	if _, err := a.tw.Write(data); err != nil {
		return fmt.Errorf("archive %s: %w", name, err)
	}
	return nil
}

// addFile adds the regular file at src under name.
func (a *archiveWriter) addFile(name, src string) error {
	f, err := os.Open(src) // #nosec G304 -- a file below a directory the operator configured
	if err != nil {
		return fmt.Errorf("archive %s: %w", name, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("archive %s: %w", name, err)
	}
	return a.addReader(name, f, info.Size(), info.ModTime())
}

// addReader adds a regular file of the given size read from r.
func (a *archiveWriter) addReader(name string, r io.Reader, size int64, mtime time.Time) error {
	hdr := &tar.Header{Name: name, Mode: int64(fileMode), Size: size, ModTime: mtime.UTC(), Typeflag: tar.TypeReg}
	if err := a.tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("archive %s: %w", name, err)
	}
	n, err := io.Copy(a.tw, r)
	if err != nil {
		return fmt.Errorf("archive %s: %w", name, err)
	}
	if n != size {
		return fmt.Errorf("archive %s: read %d bytes, expected %d (file changed while copying)", name, n, size)
	}
	return nil
}

// close finishes the archive and flushes it to disk. On error the
// partial file is removed.
func (a *archiveWriter) close() error {
	err := errors.Join(a.tw.Close(), a.gz.Close(), a.f.Sync())
	err = errors.Join(err, a.f.Close())
	if err != nil {
		_ = os.Remove(a.f.Name())
		return fmt.Errorf("finish archive: %w", err)
	}
	return nil
}

// abort removes the partial archive after a failure.
func (a *archiveWriter) abort() {
	_ = a.f.Close()
	_ = os.Remove(a.f.Name())
}

// archiveReader iterates over the entries of a gzip-compressed tar file.
type archiveReader struct {
	f    *os.File
	gz   *gzip.Reader
	tr   *tar.Reader
	body io.Reader
}

func openArchive(ctx context.Context, path string) (*archiveReader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path) // #nosec G304 -- the operator chooses the archive
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	gz, err := gzip.NewReader(archiveContextReader{ctx: ctx, r: f})
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("open archive %s: %w", path, err)
	}
	body := archiveContextReader{ctx: ctx, r: gz}
	return &archiveReader{f: f, gz: gz, tr: tar.NewReader(body), body: body}, nil
}

// next returns the next regular file entry and its cleaned name, or
// io.EOF. Directory entries are skipped; anything else is rejected.
func (a *archiveReader) next() (string, *tar.Header, io.Reader, error) {
	for {
		hdr, err := a.tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Tar ends at its zero blocks, before gzip necessarily reads
				// its checksum and length trailer. Validate every member;
				// Close alone does not check the compressed stream.
				n, err := io.Copy(io.Discard, io.LimitReader(a.body, maxEntryBytes+1))
				if err != nil {
					return "", nil, nil, fmt.Errorf("verify archive compression: %w", err)
				}
				if n > maxEntryBytes {
					return "", nil, nil, fmt.Errorf("archive trailing data exceeds %d bytes", maxEntryBytes)
				}
				return "", nil, nil, io.EOF
			}
			return "", nil, nil, fmt.Errorf("read archive: %w", err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return "", nil, nil, fmt.Errorf("archive entry %q: unsupported type %q", hdr.Name, hdr.Typeflag)
		}
		name, err := cleanEntryName(hdr.Name)
		if err != nil {
			return "", nil, nil, err
		}
		if hdr.Size < 0 || hdr.Size > maxEntryBytes {
			return "", nil, nil, fmt.Errorf("archive entry %q: size %d out of range", name, hdr.Size)
		}
		return name, hdr, io.LimitReader(a.tr, hdr.Size), nil
	}
}

type archiveContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r archiveContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func (a *archiveReader) close() {
	_ = a.gz.Close()
	_ = a.f.Close()
}

// cleanEntryName rejects names that would escape the extraction
// directory: absolute paths, "..", and backslashes.
func cleanEntryName(name string) (string, error) {
	clean := path.Clean(name)
	if name == "" || path.IsAbs(name) || strings.Contains(name, `\`) ||
		clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("archive entry %q: unsafe path", name)
	}
	return clean, nil
}

// writeFileFrom writes r to dst, creating parent directories, and flushes
// it to disk.
func writeFileFrom(dst string, r io.Reader, size int64) error {
	if err := os.MkdirAll(filepath.Dir(dst), dirMode); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode) // #nosec G304 -- below the restore target, name checked by cleanEntryName
	if err != nil {
		return err
	}
	n, err := io.Copy(f, r)
	if err == nil && n != size {
		err = fmt.Errorf("short archive entry: %d of %d bytes", n, size)
	}
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		_ = os.Remove(dst)
	}
	return err
}
