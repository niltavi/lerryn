// Package blob is the content-addressed object store (tech design 6.4):
// objects are named by the hex SHA-256 of their content, so storing the
// same content twice keeps one copy and a stored object never changes.
//
// Task file contents live here. The local implementation keeps objects on
// disk; an S3-compatible implementation follows for multi-node
// deployments.
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrNotFound is returned by Get for a hash that is not stored.
var ErrNotFound = errors.New("blob not found")

// ErrInvalidHash is returned for a name that is not 64 lowercase hex
// digits. Checking it keeps arbitrary strings out of storage paths.
var ErrInvalidHash = errors.New("blob hash must be 64 lowercase hex digits")

// Store is a content-addressed object store.
type Store interface {
	// Put stores the content read from r and returns its hex SHA-256.
	// Storing content that is already present succeeds and changes
	// nothing.
	Put(ctx context.Context, r io.Reader) (string, error)
	// Get opens the object with the given hash. The caller closes it.
	Get(ctx context.Context, hash string) (io.ReadCloser, error)
	// Has reports whether the object with the given hash is stored.
	Has(ctx context.Context, hash string) (bool, error)
}

// Deleter is a Store that can delete objects. Only the retention
// collector deletes, and only objects no row references any more (tech
// design 6.6); everything else treats stored objects as permanent.
type Deleter interface {
	Store
	// Delete removes the object with the given hash. Deleting an object
	// that is not stored succeeds.
	Delete(ctx context.Context, hash string) error
}

// ValidHash reports whether s is a hash as returned by Put.
func ValidHash(s string) bool {
	if len(s) != hex.EncodedLen(sha256.Size) {
		return false
	}
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Sum returns the hash Put would return for data.
func Sum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ReadAll returns the content of the object with the given hash.
func ReadAll(ctx context.Context, s Store, hash string) ([]byte, error) {
	rc, err := s.Get(ctx, hash)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", hash, err)
	}
	return data, nil
}

// Permissions of the local store. Objects hold task code, which may be
// confidential, so nothing is readable by other users; the group may read
// so that a backup agent in the service's group can copy the store.
const (
	dirMode  os.FileMode = 0o750
	fileMode os.FileMode = 0o640
)

// Local stores objects on the local disk, under
// <root>/blobs/sha256/<first two hex digits>/<hash>.
type Local struct {
	objects string
	tmp     string
}

var _ Deleter = (*Local)(nil)

// NewLocal returns a store rooted at root, the configured storage.path,
// creating its directories if needed.
func NewLocal(root string) (*Local, error) {
	if root == "" {
		return nil, errors.New("blob: empty storage path")
	}
	base := filepath.Join(root, "blobs")
	l := &Local{
		objects: filepath.Join(base, "sha256"),
		// Temporary files live next to the objects, on the same file
		// system, so that the final rename is atomic.
		tmp: filepath.Join(base, "tmp"),
	}
	for _, dir := range []string{l.objects, l.tmp} {
		if err := os.MkdirAll(dir, dirMode); err != nil {
			return nil, fmt.Errorf("blob: create %s: %w", dir, err)
		}
	}
	return l, nil
}

func (l *Local) path(hash string) string {
	return filepath.Join(l.objects, hash[:2], hash)
}

// Put implements Store. The content is written to a temporary file while
// it is hashed, flushed to disk, and then renamed into place, so a reader
// never sees a partial object and a crash leaves at most a stray
// temporary file.
func (l *Local) Put(ctx context.Context, r io.Reader) (hash string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(l.tmp, "put-*")
	if err != nil {
		return "", fmt.Errorf("blob: create temporary file: %w", err)
	}
	tmpName := f.Name()
	defer func() {
		if f != nil {
			_ = f.Close()
		}
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), r); err != nil {
		return "", fmt.Errorf("blob: write: %w", err)
	}
	if err := f.Chmod(fileMode); err != nil {
		return "", fmt.Errorf("blob: set permissions: %w", err)
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("blob: flush: %w", err)
	}
	if err := f.Close(); err != nil {
		f = nil
		return "", fmt.Errorf("blob: close: %w", err)
	}
	f = nil

	hash = hex.EncodeToString(h.Sum(nil))
	final := l.path(hash)
	if _, err := os.Stat(final); err == nil {
		// Same content already stored; it never changes.
		_ = os.Remove(tmpName)
		return hash, nil
	}
	dir := filepath.Dir(final)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return "", fmt.Errorf("blob: create %s: %w", dir, err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return "", fmt.Errorf("blob: store %s: %w", hash, err)
	}
	syncDir(dir)
	return hash, nil
}

// syncDir makes a rename into dir durable. Some file systems do not
// support syncing a directory; the object is then as durable as the file
// system makes it, which is not worth failing the write over.
func syncDir(dir string) {
	d, err := os.Open(dir) // #nosec G304 -- a directory below the configured storage path
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// Get implements Store.
func (l *Local) Get(ctx context.Context, hash string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ValidHash(hash) {
		return nil, ErrInvalidHash
	}
	f, err := os.Open(l.path(hash)) // #nosec G304 -- the name is a validated hex hash
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, hash)
	}
	if err != nil {
		return nil, fmt.Errorf("blob: open %s: %w", hash, err)
	}
	return f, nil
}

// Delete implements Deleter.
func (l *Local) Delete(ctx context.Context, hash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidHash(hash) {
		return ErrInvalidHash
	}
	err := os.Remove(l.path(hash))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("blob: delete %s: %w", hash, err)
	}
	if err == nil {
		syncDir(filepath.Dir(l.path(hash)))
	}
	return nil
}

// Has implements Store.
func (l *Local) Has(ctx context.Context, hash string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !ValidHash(hash) {
		return false, ErrInvalidHash
	}
	_, err := os.Stat(l.path(hash))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("blob: stat %s: %w", hash, err)
	}
}
