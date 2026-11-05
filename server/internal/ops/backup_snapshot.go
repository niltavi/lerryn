package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"

	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/store"
)

// backupBlobHashes reads only object references and their expiration marks
// from the copied database. Reusing export's schema discovery also covers
// snapshots made before log_blob and other reference columns existed.
func backupBlobHashes(ctx context.Context, database string) (map[string]struct{}, error) {
	// Snapshot locations are filesystem paths, not configured DSNs.
	// Preserve punctuation in the output directory when reopening the copy.
	dsn := "file:" + (&url.URL{Path: filepath.ToSlash(database)}).EscapedPath()
	db, err := store.Open(ctx, config.Database{Driver: config.DatabaseSQLite, DSN: dsn})
	if err != nil {
		return nil, fmt.Errorf("open backup snapshot: %w", err)
	}
	defer func() { _ = db.Close() }()
	tables, err := exportedTables(ctx, db.Native(), db)
	if err != nil {
		return nil, err
	}
	hashes := map[string]struct{}{}
	for _, table := range tables {
		used := map[int]bool{}
		for _, ref := range tableBlobRefs(table) {
			used[ref.column] = true
			if ref.releasedBy >= 0 {
				used[ref.releasedBy] = true
			}
		}
		if len(used) == 0 {
			continue
		}
		columns := table.Columns
		table.Columns = nil
		for i, column := range columns {
			if used[i] {
				table.Columns = append(table.Columns, column)
			}
		}
		refs := tableBlobRefs(table)
		rows, err := db.NewSelect().Table(table.Name).Column(columnNames(table)...).Rows(ctx)
		if err != nil {
			return nil, fmt.Errorf("read snapshot references in %s: %w", table.Name, err)
		}
		values := make([]any, len(table.Columns))
		ptrs := make([]any, len(values))
		for i := range values {
			ptrs[i] = &values[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan snapshot references in %s: %w", table.Name, err)
			}
			for _, ref := range refs {
				if hash := ref.hash(values); hash != "" {
					hashes[hash] = struct{}{}
				}
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, fmt.Errorf("read snapshot references in %s: %w", table.Name, err)
		}
	}
	return hashes, nil
}

// backupObjects copies exactly the snapshot's objects. Walking the whole
// storage root would also copy unrelated files, temporary databases, or
// the output archive itself when the operator places it under storage.
func backupObjects(ctx context.Context, a *archiveWriter, storage string, hashes map[string]struct{}) (int, error) {
	root, err := os.OpenRoot(storage)
	if err != nil {
		return 0, fmt.Errorf("open backup object store: %w", err)
	}
	defer func() { _ = root.Close() }()
	output, err := a.f.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat backup archive: %w", err)
	}
	ordered := make([]string, 0, len(hashes))
	for hash := range hashes {
		ordered = append(ordered, hash)
	}
	slices.Sort(ordered)
	copied := 0
	for _, hash := range ordered {
		if err := ctx.Err(); err != nil {
			return copied, err
		}
		rel := filepath.Join("blobs", "sha256", hash[:2], hash)
		info, err := root.Lstat(rel)
		if err != nil {
			return copied, fmt.Errorf("backup snapshot references missing object %s: %w", hash, err)
		}
		if !info.Mode().IsRegular() || os.SameFile(output, info) {
			return copied, fmt.Errorf("backup object %s is not a stored regular file", hash)
		}
		f, err := root.Open(rel)
		if err != nil {
			return copied, fmt.Errorf("open backup object %s: %w", hash, err)
		}
		digest := sha256.New()
		err = a.addReader(path.Join(backupStoragePrefix, filepath.ToSlash(rel)),
			backupObjectReader{ctx, io.TeeReader(f, digest)}, info.Size(), info.ModTime())
		err = errors.Join(err, f.Close())
		if err != nil {
			return copied, err
		}
		if hex.EncodeToString(digest.Sum(nil)) != hash {
			return copied, fmt.Errorf("backup object %s: content hash mismatch", hash)
		}
		copied++
	}
	return copied, nil
}

type backupObjectReader struct {
	ctx context.Context
	r   io.Reader
}

func (r backupObjectReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
