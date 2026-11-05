package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Collecting objects that retention released (tech design 6.6).
//
// The object store is content-addressed, so one object can back a task
// file, a script tool file, several artifacts, a webhook input and a log
// archive at the same time. Retention therefore never deletes an object
// directly: it records the hash in blob_releases, and the collector
// deletes the object only when no row references it any more.
//
// Writers store an object before they commit the row that references it,
// and storing content that is already present changes nothing. A writer
// can thus find an object present, and the collector delete it, before
// the writer's row commits. Two rules close that gap:
//
//   - The collector marks the release deleted, checks for references and
//     deletes the object in one transaction, and keeps the marked row as a
//     tombstone for blobTombstoneTTL.
//   - A writer calls claimBlobs last in the transaction that adds
//     references. It withdraws pending releases of its hashes, which
//     waits for a collector holding the same row, and returns the hashes
//     that have a tombstone. After committing, the writer stores those
//     again if they are missing (restoreBlobs).
//
// A writer's store-then-commit takes seconds, far less than the
// tombstone's lifetime, so a writer that saw an object the collector then
// deleted always finds the tombstone.

// blobTombstoneTTL is how long a deleted release is kept as a tombstone.
const blobTombstoneTTL = time.Hour

var errBlobBackupActive = errors.New("objects protected by an online backup")

// releaseBlobs records that rows referencing hashes were removed. It runs
// in the transaction that removes them. A hash released again after its
// object was deleted becomes pending again: a writer may have stored it
// anew since.
func releaseBlobs(ctx context.Context, tx bun.Tx, hashes []string, now time.Time) error {
	hashes = validHashes(hashes)
	if len(hashes) == 0 {
		return nil
	}
	rows := make([]store.BlobRelease, len(hashes))
	for i, h := range hashes {
		rows[i] = store.BlobRelease{ID: store.NewID(), Hash: h, ReleasedAt: now}
	}
	_, err := tx.NewInsert().Model(&rows).
		On("CONFLICT (hash) DO UPDATE").
		Set("released_at = EXCLUDED.released_at").
		Set("deleted_at = NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("release objects: %w", err)
	}
	return nil
}

// claimBlobs withdraws pending releases of hashes, which the caller's
// transaction is about to reference, and returns the hashes whose object
// the collector may have deleted. Call it last in that transaction, and
// after it commits, pass the result to restoreBlobs.
func claimBlobs(ctx context.Context, tx bun.Tx, hashes []string) ([]string, error) {
	hashes = validHashes(hashes)
	if len(hashes) == 0 {
		return nil, nil
	}
	if _, err := tx.NewDelete().Model((*store.BlobRelease)(nil)).
		Where("hash IN (?) AND deleted_at IS NULL", bun.List(hashes)).Exec(ctx); err != nil {
		return nil, fmt.Errorf("claim objects: %w", err)
	}
	var deleted []string
	if err := tx.NewSelect().Model((*store.BlobRelease)(nil)).Column("hash").
		Where("hash IN (?) AND deleted_at IS NOT NULL", bun.List(hashes)).Scan(ctx, &deleted); err != nil {
		return nil, fmt.Errorf("claim objects: %w", err)
	}
	return deleted, nil
}

// restoreBlobs stores again the claimed objects that the collector
// deleted. content returns the content of a hash, nil when the caller no
// longer has it; such a hash is returned in missing.
func restoreBlobs(ctx context.Context, bs blob.Store, hashes []string, content func(hash string) io.Reader) (missing []string, err error) {
	for _, h := range hashes {
		ok, err := bs.Has(ctx, h)
		if err != nil {
			return nil, fmt.Errorf("check object %s: %w", h, err)
		}
		if ok {
			continue
		}
		r := content(h)
		if r == nil {
			missing = append(missing, h)
			continue
		}
		got, err := bs.Put(ctx, r)
		if err != nil {
			return nil, fmt.Errorf("restore object %s: %w", h, err)
		}
		if got != h {
			return nil, fmt.Errorf("restore object %s: content hashes to %s", h, got)
		}
	}
	return missing, nil
}

// blobReferenced reports whether any row still references hash.
func blobReferenced(ctx context.Context, idb bun.IDB, hash string) (bool, error) {
	checks := []*bun.SelectQuery{
		idb.NewSelect().Model((*store.TaskFile)(nil)).Where("blob_hash = ?", hash),
		idb.NewSelect().Model((*store.ToolFile)(nil)).Where("blob_hash = ?", hash),
		idb.NewSelect().Model((*store.TaskState)(nil)).Where("blob_hash = ?", hash),
		idb.NewSelect().Model((*store.RunStateRef)(nil)).Where("blob_hash = ?", hash),
		idb.NewSelect().Model((*store.Artifact)(nil)).Where("blob_key = ? AND expired_at IS NULL", hash),
		idb.NewSelect().Model((*store.Run)(nil)).Where("log_blob = ?", hash),
		idb.NewSelect().Model((*store.Run)(nil)).Where("input_key = ?", hash),
	}
	for _, q := range checks {
		ok, err := q.Exists(ctx)
		if err != nil {
			return false, fmt.Errorf("check references to %s: %w", hash, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// collectBlobs deletes up to retentionBatch released objects that nothing
// references and drops tombstones past their lifetime. It returns how
// many objects it deleted and whether a full batch was examined.
func (r *Retention) collectBlobs(ctx context.Context) (deleted int, full bool, err error) {
	del, ok := r.blobs.(blob.Deleter)
	if !ok {
		return 0, false, nil
	}
	// Log archives found before log_blob existed are references the
	// check cannot see yet; wait until the backfill has named them.
	pending, err := r.db.NewSelect().Model((*store.Run)(nil)).
		Where("log_archive IS NOT NULL AND log_bytes IS NULL").Exists(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("check log archive backfill: %w", err)
	}
	if pending {
		return 0, false, nil
	}
	now := r.now()
	if _, err := r.db.NewDelete().Model((*store.BlobRelease)(nil)).
		Where("deleted_at IS NOT NULL AND deleted_at < ?", now.Add(-blobTombstoneTTL)).Exec(ctx); err != nil {
		return 0, false, fmt.Errorf("drop object tombstones: %w", err)
	}
	var releases []store.BlobRelease
	if err := r.db.NewSelect().Model(&releases).Where("deleted_at IS NULL").
		Order("released_at").Limit(retentionBatch).Scan(ctx); err != nil {
		return 0, false, fmt.Errorf("find released objects: %w", err)
	}
	for _, rel := range releases {
		gone, err := r.collectOne(ctx, del, rel)
		if errors.Is(err, errBlobBackupActive) {
			// The same pending batch would otherwise be drained forever.
			return deleted, false, nil
		}
		if err != nil {
			return deleted, false, err
		}
		if gone {
			deleted++
		}
	}
	return deleted, len(releases) == retentionBatch, nil
}

// collectOne deletes one released object if nothing references it. The
// object is deleted inside the transaction that marks the release, so a
// writer claiming the same hash waits for the outcome (see claimBlobs).
func (r *Retention) collectOne(ctx context.Context, del blob.Deleter, rel store.BlobRelease) (bool, error) {
	gone := false
	err := r.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		gone = false
		// Pin acquisition is a SQLite write too: checking after BEGIN
		// IMMEDIATE serialises it with deletion, across server processes.
		pinned, err := r.db.BlobBackupsActive(ctx, tx)
		if err != nil {
			return err
		}
		if pinned {
			return errBlobBackupActive
		}
		res, err := tx.NewUpdate().Model((*store.BlobRelease)(nil)).
			Set("deleted_at = ?", r.now()).
			Where("id = ? AND deleted_at IS NULL", rel.ID).Exec(ctx)
		if err := changedOne(res, err); err != nil {
			if errors.Is(err, errNotChanged) {
				// Claimed by a writer meanwhile.
				return nil
			}
			return err
		}
		referenced, err := blobReferenced(ctx, tx, rel.Hash)
		if err != nil {
			return err
		}
		if referenced {
			_, err := tx.NewDelete().Model((*store.BlobRelease)(nil)).Where("id = ?", rel.ID).Exec(ctx)
			return err
		}
		if err := del.Delete(ctx, rel.Hash); err != nil {
			return fmt.Errorf("delete object %s: %w", rel.Hash, err)
		}
		gone = true
		return nil
	})
	return gone, err
}

// validHashes returns the distinct object hashes in hashes, dropping
// empty and malformed ones.
func validHashes(hashes []string) []string {
	out := make([]string, 0, len(hashes))
	for _, h := range hashes {
		if blob.ValidHash(h) && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// fileHashes returns the content hashes of task files.
func fileHashes(files []store.TaskFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.BlobHash
	}
	return out
}

// restoreFileBlobs stores again submitted file contents that retention
// deleted while the submission was committed (claimBlobs).
func restoreFileBlobs(ctx context.Context, bs blob.Store, hashes []string, files []FileInput) error {
	if len(hashes) == 0 {
		return nil
	}
	_, err := restoreBlobs(ctx, bs, hashes, func(h string) io.Reader {
		for _, f := range files {
			if blob.Sum(f.Content) == h {
				return bytes.NewReader(f.Content)
			}
		}
		return nil
	})
	return err
}
