package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/store"
)

// SetArchived moves an ended run into or out of the user's archive. Logs
// are staged before taking the write lock, so object-store reads never
// block other database writers. Retention shares the same run lock.
func (r *Runs) SetArchived(ctx context.Context, p *auth.Principal, id uuid.UUID, archived bool) (RunDetail, error) {
	run, err := r.load(ctx, p, id, ActionControlRun)
	if err != nil {
		return RunDetail{}, err
	}
	if archived == !run.ArchivedAt.IsZero() {
		return r.detail(ctx, run)
	}
	if err := archivableRun(run, archived); err != nil {
		return RunDetail{}, err
	}
	var spool *os.File
	if archived {
		spool, err = r.stageDatabaseLogs(ctx, run)
		if err != nil {
			return RunDetail{}, err
		}
		defer func() { _ = spool.Close() }()
	}
	expectedLines := run.LogLines
	changed := false
	err = r.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if err := tx.NewUpdate().Model(&run).Set("archived_at = archived_at").WherePK().Returning("*").Scan(ctx); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if archived == !run.ArchivedAt.IsZero() {
			return nil
		}
		if err := archivableRun(run, archived); err != nil {
			return err
		}
		now := store.Now()
		if archived {
			if run.LogLines != expectedLines {
				return fmt.Errorf("log of run %s changed while staging the archive", id)
			}
			if _, err := spool.Seek(0, io.SeekStart); err != nil {
				return fmt.Errorf("rewind staged log: %w", err)
			}
			if _, err := tx.NewDelete().Model((*store.LogChunk)(nil)).Where("run_id = ?", id).Exec(ctx); err != nil {
				return err
			}
			decoder := json.NewDecoder(spool)
			for {
				var chunk store.LogChunk
				if err := decoder.Decode(&chunk); errors.Is(err, io.EOF) {
					break
				} else if err != nil {
					return fmt.Errorf("decode staged log: %w", err)
				}
				chunk.ID, chunk.RunID, chunk.CreatedAt = store.NewID(), id, now
				if _, err := tx.NewInsert().Model(&chunk).Exec(ctx); err != nil {
					return fmt.Errorf("store archived log in database: %w", err)
				}
			}
			var a logArchive
			if run.LogArchive != "" {
				if err := json.Unmarshal([]byte(run.LogArchive), &a); err != nil {
					return fmt.Errorf("decode previous log archive: %w", err)
				}
			}
			if err := releaseBlobs(ctx, tx, []string{run.LogBlob, a.Blob}, now); err != nil {
				return err
			}
			if _, err := tx.NewUpdate().Model((*store.Run)(nil)).
				Set("archived_at = ?", now).Set("log_archive = NULL").Set("log_blob = NULL").Set("log_bytes = 0").Where("id = ?", id).Exec(ctx); err != nil {
				return err
			}
			run.ArchivedAt, run.LogArchive, run.LogBlob, run.LogBytes = now, "", "", new(int64)
		} else {
			if _, err := tx.NewUpdate().Model((*store.Run)(nil)).Set("archived_at = NULL").Where("id = ?", id).Exec(ctx); err != nil {
				return err
			}
			run.ArchivedAt = time.Time{}
		}
		kind, actor := actorOf(p)
		changed = true
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor, action: "run.archive", resourceKind: "run", resourceID: id.String(),
			result: resultSuccess, details: map[string]any{"project_id": run.ProjectID.String(), "task_id": run.TaskID.String(), "archived": archived},
		})
	})
	if err != nil {
		return RunDetail{}, err
	}
	if changed {
		r.changed(ctx, id)
	}
	return r.detail(ctx, run)
}

func archivableRun(run store.Run, archived bool) error {
	if store.RunActive(run.Status) || run.FinishedAt.IsZero() {
		v := validation{unprocessable: true}
		v.content("#/archived", "只能归档已结束的运行，请等待运行结束")
		return v.err()
	}
	if archived && !run.LogsExpiredAt.IsZero() {
		return ErrExpired
	}
	return nil
}

// stageDatabaseLogs bounds memory to one frame and unlinks the temporary
// file immediately, because it contains the run's redacted log contents.
func (r *Runs) stageDatabaseLogs(ctx context.Context, run store.Run) (*os.File, error) {
	spool, err := os.CreateTemp("", "lerryn-user-log-archive-*")
	if err != nil {
		return nil, fmt.Errorf("create staged log: %w", err)
	}
	_ = os.Remove(spool.Name())
	encoder := json.NewEncoder(spool)
	for from := int64(1); from <= run.LogLines; from += archiveFrameLines {
		to := min(from+archiveFrameLines-1, run.LogLines)
		lines, err := r.readLines(ctx, run, from, to)
		if err == nil && (int64(len(lines)) != to-from+1 || lines[0].Seq != from || lines[len(lines)-1].Seq != to) {
			err = fmt.Errorf("incomplete log lines %d to %d of run %s", from, to, run.ID)
		}
		if err != nil {
			_ = spool.Close()
			fresh, loadErr := r.reload(ctx, run.ID)
			if loadErr != nil {
				return nil, loadErr
			}
			if !fresh.LogsExpiredAt.IsZero() {
				return nil, ErrExpired
			}
			return nil, err
		}
		var data bytes.Buffer
		enc := json.NewEncoder(&data)
		var size int64
		for _, line := range lines {
			if err := enc.Encode(line); err != nil {
				_ = spool.Close()
				return nil, fmt.Errorf("encode database log: %w", err)
			}
			size += int64(len(line.Text))
		}
		if err := encoder.Encode(store.LogChunk{SeqFrom: from, SeqTo: to, Data: data.String(), Bytes: size}); err != nil {
			_ = spool.Close()
			return nil, fmt.Errorf("stage database log: %w", err)
		}
	}
	return spool, nil
}
