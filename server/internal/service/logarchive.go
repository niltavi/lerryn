package service

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Log archives (tech design 6.5). Once a run has ended, the leader merges
// its log chunks into one object in the object store and deletes the
// chunks. The object is a sequence of independent gzip members, one per
// frame of archiveFrameLines lines, each holding the same NDJSON records
// as the chunks. The frame index lives in runs.log_archive, so reading
// any position decompresses only the frames that cover it.
//
// Objects are content-addressed like task files: the run row names the
// object by its hash.

// archiveFrameLines is the number of lines per compressed frame.
const archiveFrameLines = 1000

// ArchiveBatch is how many runs one ArchiveLogs call archives at most.
const ArchiveBatch = 20

// logArchive is the JSON stored in runs.log_archive.
type logArchive struct {
	// Blob is the hash of the archive object; empty for a run without
	// log lines.
	Blob   string     `json:"blob,omitempty"`
	Lines  int64      `json:"lines"`
	Frames []logFrame `json:"frames"`
}

// logFrame is one compressed frame of an archive.
type logFrame struct {
	From   int64 `json:"from"`
	To     int64 `json:"to"`
	Offset int64 `json:"offset"`
	Size   int64 `json:"size"`
}

// ArchiveLogs archives the logs of up to ArchiveBatch ended runs whose log
// is still in chunks, oldest first, and returns how many it archived. The
// leader calls it periodically; without an object store it does nothing.
// It is safe to run on two servers at once: a run is archived once, and a
// second archive of the same content is the same object.
func (r *Runs) ArchiveLogs(ctx context.Context) (int, error) {
	if r.blobs == nil {
		return 0, nil
	}
	var runs []store.Run
	err := r.db.NewSelect().Model(&runs).
		Column("id", "status", "log_lines").
		Where("log_archive IS NULL AND finished_at IS NOT NULL AND logs_expired_at IS NULL").
		Where("status NOT IN (?)", bun.List(activeStatuses)).
		Order("finished_at").Limit(ArchiveBatch).Scan(ctx)
	if err != nil {
		return 0, fmt.Errorf("find runs to archive: %w", err)
	}
	n := 0
	var errs []error
	for _, run := range runs {
		if err := r.archiveRun(ctx, run); err != nil {
			if ctx.Err() != nil {
				return n, ctx.Err()
			}
			// One damaged run must not hold up the others.
			errs = append(errs, fmt.Errorf("archive log of run %s: %w", run.ID, err))
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// archiveRun writes one run's log to the object store and swaps the
// chunks for the archive.
func (r *Runs) archiveRun(ctx context.Context, run store.Run) error {
	archive := logArchive{Lines: run.LogLines, Frames: []logFrame{}}
	// spool holds the archive until it is committed, in case it has to be
	// stored again (claimBlobs).
	var spool *os.File
	if run.LogLines > 0 {
		tmp, err := os.CreateTemp("", "lerryn-log-archive-*")
		if err != nil {
			return fmt.Errorf("create spool file: %w", err)
		}
		defer func() { _ = tmp.Close() }()
		spool = tmp
		_ = os.Remove(tmp.Name()) // unlinked at once; the open handle keeps the data

		var offset int64
		for from := int64(1); from <= run.LogLines; from += archiveFrameLines {
			to := min(from+archiveFrameLines-1, run.LogLines)
			lines, err := readLogLines(ctx, r.db, run.ID, from, to)
			if err != nil {
				return err
			}
			if int64(len(lines)) != to-from+1 || lines[0].Seq != from || lines[len(lines)-1].Seq != to {
				return fmt.Errorf("log lines %d to %d are incomplete: found %d", from, to, len(lines))
			}
			size, err := writeFrame(tmp, lines)
			if err != nil {
				return err
			}
			archive.Frames = append(archive.Frames, logFrame{From: from, To: to, Offset: offset, Size: size})
			offset += size
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind spool file: %w", err)
		}
		if archive.Blob, err = r.blobs.Put(ctx, bufio.NewReader(tmp)); err != nil {
			return fmt.Errorf("store archive: %w", err)
		}
	}
	data, err := json.Marshal(archive)
	if err != nil {
		return err
	}
	var restore []string
	err = r.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		restore = nil
		res, err := tx.NewUpdate().Model((*store.Run)(nil)).
			Set("log_archive = ?", string(data)).
			Set("log_blob = ?", nullString(archive.Blob)).
			Set("log_bytes = ?", archiveSize(archive)).
			Where("id = ? AND log_archive IS NULL AND logs_expired_at IS NULL AND log_lines = ?", run.ID, run.LogLines).
			Exec(ctx)
		if err := changedOne(res, err); err != nil {
			if errors.Is(err, errNotChanged) {
				// Archived meanwhile by another server, or expired.
				return nil
			}
			return err
		}
		if _, err := tx.NewDelete().Model((*store.LogChunk)(nil)).Where("run_id = ?", run.ID).Exec(ctx); err != nil {
			return err
		}
		restore, err = claimBlobs(ctx, tx, []string{archive.Blob})
		return err
	})
	if err != nil || len(restore) == 0 {
		return err
	}
	_, err = restoreBlobs(ctx, r.blobs, restore, func(string) io.Reader {
		if _, err := spool.Seek(0, io.SeekStart); err != nil {
			return nil
		}
		return bufio.NewReader(spool)
	})
	return err
}

// writeFrame appends lines as one gzip member and returns its size.
func writeFrame(w io.Writer, lines []store.LogRecord) (int64, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	enc := json.NewEncoder(zw)
	enc.SetEscapeHTML(false)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			return 0, fmt.Errorf("encode log line %d: %w", l.Seq, err)
		}
	}
	if err := zw.Close(); err != nil {
		return 0, fmt.Errorf("compress log frame: %w", err)
	}
	n, err := w.Write(buf.Bytes())
	if err != nil {
		return 0, fmt.Errorf("write log frame: %w", err)
	}
	return int64(n), nil
}

// readLines returns the lines numbered from to to of run, from its chunks
// or its archive. A run read before the leader archived it may find its
// chunks gone; it is then read again and served from the archive.
func (r *Runs) readLines(ctx context.Context, run store.Run, from, to int64) ([]store.LogRecord, error) {
	if !run.LogsExpiredAt.IsZero() {
		return nil, ErrExpired
	}
	if from > to {
		return nil, nil
	}
	if run.LogArchive == "" {
		lines, err := readLogLines(ctx, r.db, run.ID, from, to)
		if err != nil || int64(len(lines)) == to-from+1 || store.RunActive(run.Status) {
			return lines, err
		}
		fresh, err := r.reload(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		if !fresh.LogsExpiredAt.IsZero() {
			return nil, ErrExpired
		}
		if fresh.LogArchive == "" {
			return lines, nil
		}
		run = fresh
	}
	return r.readArchived(ctx, run.ID, run.LogArchive, from, to)
}

// readArchived reads lines from to to from a run's archive.
func (r *Runs) readArchived(ctx context.Context, runID uuid.UUID, raw string, from, to int64) ([]store.LogRecord, error) {
	var a logArchive
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("decode log archive of run %s: %w", runID, err)
	}
	to = min(to, a.Lines)
	if from > to {
		return nil, nil
	}
	if r.blobs == nil {
		return nil, fmt.Errorf("log of run %s is archived, but no object store is configured", runID)
	}
	lines := make([]store.LogRecord, 0, to-from+1)
	for _, f := range a.Frames {
		if f.To < from || f.From > to {
			continue
		}
		data, err := blob.ReadRange(ctx, r.blobs, a.Blob, f.Offset, f.Size)
		if err != nil {
			return nil, fmt.Errorf("read log archive of run %s: %w", runID, err)
		}
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("open log frame %d of run %s: %w", f.From, runID, err)
		}
		dec := json.NewDecoder(zr)
		for {
			var rec store.LogRecord
			if err := dec.Decode(&rec); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, fmt.Errorf("decode log frame %d of run %s: %w", f.From, runID, err)
			}
			if rec.Seq >= from && rec.Seq <= to {
				lines = append(lines, rec)
			}
		}
	}
	return lines, nil
}
