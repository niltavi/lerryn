package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Retention (tech design 6.6). The leader periodically removes what the
// deployment's policy no longer keeps, oldest first and in bounded
// batches, each batch in its own transaction:
//
//  1. logs of runs that ended more than Logs ago, then the oldest logs
//     while the archived logs exceed LogsMaxBytes;
//  2. artifacts created more than Artifacts ago, then the oldest while
//     the kept artifacts exceed ArtifactsMaxBytes;
//  3. runs that ended more than Runs ago, with their events, logs and
//     artifacts, except the latest non-test run of each task, which the
//     task list shows as its last run;
//  4. objects that nothing references any more (blobgc.go).
//
// Active runs are never touched, nor ended runs whose automatic retry is
// still undecided. A removed log or artifact leaves its mark on the row
// (runs.logs_expired_at, artifacts.expired_at), so reading it answers
// "expired" instead of looking empty; each cleanup that removed anything
// writes one audit record with the totals.

// retentionBatch bounds the rows one transaction of the cleanup touches.
const retentionBatch = 200

// RetentionInterval is how often the leader runs the cleanup.
const RetentionInterval = 10 * time.Minute

// RetentionPolicy says what the deployment keeps. Zero keeps without
// limit.
type RetentionPolicy struct {
	Runs, Logs, Artifacts           time.Duration
	LogsMaxBytes, ArtifactsMaxBytes int64
}

// CleanupResult counts what one cleanup removed.
type CleanupResult struct {
	Runs      int `json:"runs"`
	Logs      int `json:"logs"`
	Artifacts int `json:"artifacts"`
	// LogBytes and ArtifactBytes are the sizes of the removed archives
	// and artifacts, before deduplication.
	LogBytes      int64 `json:"log_bytes"`
	ArtifactBytes int64 `json:"artifact_bytes"`
	// Objects is the number of objects deleted from the object store.
	Objects int `json:"objects"`
}

// Empty reports whether nothing was removed.
func (c CleanupResult) Empty() bool { return c == CleanupResult{} }

// Retention applies the retention policy.
type Retention struct {
	db     *store.DB
	blobs  blob.Store
	policy RetentionPolicy
	log    *slog.Logger
	now    func() time.Time
}

// NewRetention returns the cleanup for policy. blobs may be nil, which
// leaves objects in place.
func NewRetention(db *store.DB, blobs blob.Store, policy RetentionPolicy, log *slog.Logger) *Retention {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Retention{db: db, blobs: blobs, policy: policy, log: log, now: store.Now}
}

// WithClock replaces the clock, for tests.
func (r *Retention) WithClock(now func() time.Time) *Retention {
	r.now = now
	return r
}

// Cleanup removes everything the policy no longer keeps, batch by batch
// until nothing is left or ctx ends, and returns the totals.
func (r *Retention) Cleanup(ctx context.Context) (CleanupResult, error) {
	var res CleanupResult
	err := r.cleanup(ctx, &res)
	if !res.Empty() {
		r.log.InfoContext(ctx, "retention cleanup",
			"runs", res.Runs, "logs", res.Logs, "artifacts", res.Artifacts,
			"log_bytes", res.LogBytes, "artifact_bytes", res.ArtifactBytes, "objects", res.Objects)
		details := map[string]any{}
		data, _ := json.Marshal(res)
		_ = json.Unmarshal(data, &details)
		details["policy"] = r.policyDetails()
		// The audit record survives a cancelled context: the removals it
		// reports have committed.
		if aerr := writeAudit(context.WithoutCancel(ctx), r.db, auditEntry{
			actorKind: actorSystem, action: "retention.cleanup", resourceKind: "retention", details: details,
		}); aerr != nil && err == nil {
			err = aerr
		}
	}
	return res, err
}

func (r *Retention) policyDetails() map[string]any {
	days := func(d time.Duration) int { return int(d / (24 * time.Hour)) }
	return map[string]any{
		"runs_days": days(r.policy.Runs), "logs_days": days(r.policy.Logs), "artifacts_days": days(r.policy.Artifacts),
		"logs_max_bytes": r.policy.LogsMaxBytes, "artifacts_max_bytes": r.policy.ArtifactsMaxBytes,
	}
}

func (r *Retention) cleanup(ctx context.Context, res *CleanupResult) error {
	if err := drain(ctx, r.backfillLogBlobs); err != nil {
		return err
	}
	p := r.policy
	if p.Logs > 0 {
		if err := drain(ctx, func(ctx context.Context) (bool, error) { return r.expireOldLogs(ctx, res) }); err != nil {
			return err
		}
	}
	if p.LogsMaxBytes > 0 {
		if err := drain(ctx, func(ctx context.Context) (bool, error) { return r.capLogs(ctx, res) }); err != nil {
			return err
		}
	}
	if p.Artifacts > 0 {
		if err := drain(ctx, func(ctx context.Context) (bool, error) { return r.expireOldArtifacts(ctx, res) }); err != nil {
			return err
		}
	}
	if p.ArtifactsMaxBytes > 0 {
		if err := drain(ctx, func(ctx context.Context) (bool, error) { return r.capArtifacts(ctx, res) }); err != nil {
			return err
		}
	}
	if p.Runs > 0 {
		if err := drain(ctx, func(ctx context.Context) (bool, error) { return r.deleteOldRuns(ctx, res) }); err != nil {
			return err
		}
	}
	if r.blobs != nil {
		return drain(ctx, func(ctx context.Context) (bool, error) {
			n, more, err := r.collectBlobs(ctx)
			res.Objects += n
			return more, err
		})
	}
	return nil
}

// drain calls step until it reports that nothing is left.
func drain(ctx context.Context, step func(context.Context) (bool, error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		more, err := step(ctx)
		if err != nil || !more {
			return err
		}
	}
}

// endedRun selects ended runs: never an active one, nor one whose
// automatic retry the scheduler has yet to decide.
func endedRun(q *bun.SelectQuery) *bun.SelectQuery {
	return q.Where("run.status NOT IN (?)", bun.List(activeStatuses)).
		Where("run.archived_at IS NULL").
		Where("run.finished_at IS NOT NULL").
		Where("NOT (run.retry_decided_at IS NULL AND run.attempt < run.max_attempts)")
}

// backfillLogBlobs copies the object hash and size out of log archives
// written before runs.log_blob existed.
func (r *Retention) backfillLogBlobs(ctx context.Context) (bool, error) {
	var runs []store.Run
	if err := r.db.NewSelect().Model(&runs).Column("id", "log_archive").
		Where("log_archive IS NOT NULL AND log_bytes IS NULL").
		Limit(retentionBatch).Scan(ctx); err != nil {
		return false, fmt.Errorf("find log archives to backfill: %w", err)
	}
	if len(runs) == 0 {
		return false, nil
	}
	err := r.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		for _, run := range runs {
			var a logArchive
			if err := json.Unmarshal([]byte(run.LogArchive), &a); err != nil {
				return fmt.Errorf("decode log archive of run %s: %w", run.ID, err)
			}
			size := archiveSize(a)
			if _, err := tx.NewUpdate().Model((*store.Run)(nil)).
				Set("log_blob = ?", nullString(a.Blob)).Set("log_bytes = ?", size).
				Where("id = ? AND log_bytes IS NULL", run.ID).Exec(ctx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("backfill log archives: %w", err)
	}
	return len(runs) == retentionBatch, nil
}

// archiveSize is the size of an archive object: its frames end to end.
func archiveSize(a logArchive) int64 {
	var n int64
	for _, f := range a.Frames {
		n = max(n, f.Offset+f.Size)
	}
	return n
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// expireOldLogs removes the logs of runs that ended before the log
// retention period.
func (r *Retention) expireOldLogs(ctx context.Context, res *CleanupResult) (bool, error) {
	var ids []uuid.UUID
	err := endedRun(r.db.NewSelect().Model((*store.Run)(nil)).Column("run.id")).
		Where("run.logs_expired_at IS NULL").
		Where("run.finished_at < ?", r.now().Add(-r.policy.Logs)).
		Order("run.finished_at").Limit(retentionBatch).Scan(ctx, &ids)
	if err != nil {
		return false, fmt.Errorf("find expired logs: %w", err)
	}
	if err := r.expireLogs(ctx, ids, res); err != nil {
		return false, err
	}
	return len(ids) == retentionBatch, nil
}

// capLogs removes the oldest archived logs while the kept archives
// exceed the size cap. Logs still in chunks are not counted: the leader
// archives them within seconds of the run's end.
func (r *Retention) capLogs(ctx context.Context, res *CleanupResult) (bool, error) {
	var total int64
	if err := r.db.NewSelect().Model((*store.Run)(nil)).ColumnExpr("COALESCE(SUM(log_bytes), 0)").
		Where("logs_expired_at IS NULL AND archived_at IS NULL").Scan(ctx, &total); err != nil {
		return false, fmt.Errorf("sum log sizes: %w", err)
	}
	excess := total - r.policy.LogsMaxBytes
	if excess <= 0 {
		return false, nil
	}
	var runs []store.Run
	err := endedRun(r.db.NewSelect().Model(&runs).Column("run.id", "run.log_bytes")).
		Where("run.logs_expired_at IS NULL AND run.log_bytes > 0").
		Order("run.finished_at").Limit(retentionBatch).Scan(ctx)
	if err != nil {
		return false, fmt.Errorf("find logs over the size cap: %w", err)
	}
	var ids []uuid.UUID
	for _, run := range runs {
		if excess <= 0 {
			break
		}
		ids = append(ids, run.ID)
		excess -= *run.LogBytes
	}
	if err := r.expireLogs(ctx, ids, res); err != nil {
		return false, err
	}
	return excess > 0 && len(runs) == retentionBatch, nil
}

// expireLogs removes the logs of the given ended runs: their chunks and
// their archive object, if nothing else references it.
func (r *Retention) expireLogs(ctx context.Context, ids []uuid.UUID, res *CleanupResult) error {
	if len(ids) == 0 {
		return nil
	}
	var n int
	var size int64
	err := r.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		n, size = 0, 0
		now := r.now()
		var runs []store.Run
		if err := lockRetentionRuns(ctx, tx, ids); err != nil {
			return err
		}
		// Read again inside the transaction: the run may have been
		// expired or deleted meanwhile.
		err := endedRun(tx.NewSelect().Model(&runs).Column("run.id", "run.log_blob", "run.log_bytes")).
			Where("run.id IN (?) AND run.logs_expired_at IS NULL", bun.List(ids)).Scan(ctx)
		if err != nil || len(runs) == 0 {
			return err
		}
		keep := make([]uuid.UUID, len(runs))
		hashes := make([]string, 0, len(runs))
		for i, run := range runs {
			keep[i] = run.ID
			hashes = append(hashes, run.LogBlob)
			if run.LogBytes != nil {
				size += *run.LogBytes
			}
		}
		// A log never archived gets an empty archive, so the archiver
		// leaves it alone.
		if _, err := tx.NewUpdate().Model((*store.Run)(nil)).
			Set("logs_expired_at = ?", now).
			Set("log_archive = COALESCE(log_archive, ?)", `{"lines":0,"frames":[]}`).
			Set("log_blob = NULL").Set("log_bytes = 0").
			Where("id IN (?)", bun.List(keep)).Exec(ctx); err != nil {
			return err
		}
		if _, err := tx.NewDelete().Model((*store.LogChunk)(nil)).Where("run_id IN (?)", bun.List(keep)).Exec(ctx); err != nil {
			return err
		}
		n = len(runs)
		return releaseBlobs(ctx, tx, hashes, now)
	})
	if err != nil {
		return fmt.Errorf("expire logs: %w", err)
	}
	res.Logs += n
	res.LogBytes += size
	return nil
}

// expireOldArtifacts removes artifacts created before the artifact
// retention period.
func (r *Retention) expireOldArtifacts(ctx context.Context, res *CleanupResult) (bool, error) {
	var ids []uuid.UUID
	err := r.keptArtifacts(r.db.NewSelect().Model((*store.Artifact)(nil)).Column("artifact.id")).
		Where("artifact.created_at < ?", r.now().Add(-r.policy.Artifacts)).
		Order("artifact.created_at").Limit(retentionBatch).Scan(ctx, &ids)
	if err != nil {
		return false, fmt.Errorf("find expired artifacts: %w", err)
	}
	if err := r.expireArtifacts(ctx, ids, res); err != nil {
		return false, err
	}
	return len(ids) == retentionBatch, nil
}

// capArtifacts removes the oldest artifacts while the kept artifacts
// exceed the size cap. Sizes add up per artifact: two artifacts with the
// same content count twice, though they share one object.
func (r *Retention) capArtifacts(ctx context.Context, res *CleanupResult) (bool, error) {
	var total int64
	if err := r.db.NewSelect().Model((*store.Artifact)(nil)).ColumnExpr("COALESCE(SUM(size), 0)").
		Where("expired_at IS NULL AND run_id IN (SELECT id FROM runs WHERE archived_at IS NULL)").Scan(ctx, &total); err != nil {
		return false, fmt.Errorf("sum artifact sizes: %w", err)
	}
	excess := total - r.policy.ArtifactsMaxBytes
	if excess <= 0 {
		return false, nil
	}
	var rows []store.Artifact
	err := r.keptArtifacts(r.db.NewSelect().Model(&rows).Column("artifact.id", "artifact.size")).
		Order("artifact.created_at").Limit(retentionBatch).Scan(ctx)
	if err != nil {
		return false, fmt.Errorf("find artifacts over the size cap: %w", err)
	}
	var ids []uuid.UUID
	for _, a := range rows {
		if excess <= 0 {
			break
		}
		ids = append(ids, a.ID)
		excess -= a.Size
	}
	if err := r.expireArtifacts(ctx, ids, res); err != nil {
		return false, err
	}
	return excess > 0 && len(rows) == retentionBatch, nil
}

// keptArtifacts selects the unexpired artifacts of ended runs.
func (r *Retention) keptArtifacts(q *bun.SelectQuery) *bun.SelectQuery {
	return q.Where("artifact.expired_at IS NULL").
		Where("EXISTS (?)", endedRun(r.db.NewSelect().Model((*store.Run)(nil)).ColumnExpr("1").
			Where("run.id = artifact.run_id")))
}

// expireArtifacts marks the given artifacts expired and releases their
// objects.
func (r *Retention) expireArtifacts(ctx context.Context, ids []uuid.UUID, res *CleanupResult) error {
	if len(ids) == 0 {
		return nil
	}
	var n int
	var size int64
	err := r.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		n, size = 0, 0
		now := r.now()
		var rows []store.Artifact
		var runIDs []uuid.UUID
		if err := tx.NewSelect().Model((*store.Artifact)(nil)).Column("run_id").Where("id IN (?)", bun.List(ids)).Scan(ctx, &runIDs); err != nil {
			return err
		}
		if err := lockRetentionRuns(ctx, tx, runIDs); err != nil {
			return err
		}
		if err := r.keptArtifacts(tx.NewSelect().Model(&rows).Column("artifact.id", "artifact.blob_key", "artifact.size")).
			Where("artifact.id IN (?)", bun.List(ids)).Scan(ctx); err != nil || len(rows) == 0 {
			return err
		}
		keep := make([]uuid.UUID, len(rows))
		hashes := make([]string, len(rows))
		for i, a := range rows {
			keep[i], hashes[i] = a.ID, a.BlobKey
			size += a.Size
		}
		if _, err := tx.NewUpdate().Model((*store.Artifact)(nil)).Set("expired_at = ?", now).
			Where("id IN (?)", bun.List(keep)).Exec(ctx); err != nil {
			return err
		}
		n = len(rows)
		return releaseBlobs(ctx, tx, hashes, now)
	})
	if err != nil {
		return fmt.Errorf("expire artifacts: %w", err)
	}
	res.Artifacts += n
	res.ArtifactBytes += size
	return nil
}

// deletableRuns selects ended runs that may be deleted: all but the
// latest non-test run of each task.
func deletableRuns(q *bun.SelectQuery, idb bun.IDB) *bun.SelectQuery {
	newer := idb.NewSelect().TableExpr("runs AS r2").ColumnExpr("1").
		Where("r2.task_id = run.task_id AND r2.is_test = ? AND r2.archived_at IS NULL", false).
		Where("(r2.queued_at > run.queued_at OR (r2.queued_at = run.queued_at AND r2.id > run.id))")
	return endedRun(q).WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("run.is_test = ?", true).WhereOr("EXISTS (?)", newer)
	})
}

// deleteOldRuns deletes runs that ended before the run retention period.
func (r *Retention) deleteOldRuns(ctx context.Context, res *CleanupResult) (bool, error) {
	var ids []uuid.UUID
	err := deletableRuns(r.db.NewSelect().Model((*store.Run)(nil)).Column("run.id"), r.db).
		Where("run.finished_at < ?", r.now().Add(-r.policy.Runs)).
		Order("run.finished_at").Limit(retentionBatch).Scan(ctx, &ids)
	if err != nil {
		return false, fmt.Errorf("find expired runs: %w", err)
	}
	if len(ids) == 0 {
		return false, nil
	}
	var n, logs, artifacts int
	var logBytes, artifactBytes int64
	err = r.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		n, logs, artifacts, logBytes, artifactBytes = 0, 0, 0, 0, 0
		now := r.now()
		var runs []store.Run
		if err := lockRetentionRuns(ctx, tx, ids); err != nil {
			return err
		}
		err := deletableRuns(tx.NewSelect().Model(&runs).
			Column("run.id", "run.log_blob", "run.log_bytes", "run.logs_expired_at", "run.input_key"), tx).
			Where("run.id IN (?)", bun.List(ids)).Scan(ctx)
		if err != nil || len(runs) == 0 {
			return err
		}
		gone := make([]uuid.UUID, len(runs))
		var hashes []string
		for i, run := range runs {
			gone[i] = run.ID
			hashes = append(hashes, run.LogBlob, run.InputKey)
			if run.LogsExpiredAt.IsZero() {
				logs++
				if run.LogBytes != nil {
					logBytes += *run.LogBytes
				}
			}
		}
		var kept []store.Artifact
		if err := tx.NewSelect().Model(&kept).Column("blob_key", "size").
			Where("run_id IN (?) AND expired_at IS NULL", bun.List(gone)).Scan(ctx); err != nil {
			return err
		}
		for _, a := range kept {
			hashes = append(hashes, a.BlobKey)
			artifactBytes += a.Size
		}
		artifacts = len(kept)
		// Links from other rows go first; they are not all foreign keys
		// with a delete action, and the newer runs keep their record.
		for _, col := range []string{"retry_of", "rerun_of"} {
			if _, err := tx.NewUpdate().Model((*store.Run)(nil)).Set(col+" = NULL").
				Where(col+" IN (?)", bun.List(gone)).Exec(ctx); err != nil {
				return err
			}
		}
		if _, err := tx.NewUpdate().Model((*store.TriggerFire)(nil)).Set("run_id = NULL").
			Where("run_id IN (?)", bun.List(gone)).Exec(ctx); err != nil {
			return err
		}
		if err := releaseRunStateRefs(ctx, tx, gone, now); err != nil {
			return err
		}
		// Events, log chunks, artifacts and required labels go with the
		// run (ON DELETE CASCADE).
		if _, err := tx.NewDelete().Model((*store.Run)(nil)).Where("id IN (?)", bun.List(gone)).Exec(ctx); err != nil {
			return err
		}
		n = len(runs)
		return releaseBlobs(ctx, tx, hashes, now)
	})
	if err != nil {
		return false, fmt.Errorf("delete expired runs: %w", err)
	}
	res.Runs += n
	res.Logs += logs
	res.LogBytes += logBytes
	res.Artifacts += artifacts
	res.ArtifactBytes += artifactBytes
	return len(ids) == retentionBatch, nil
}

// Serialise cleanup with manual archiving before rechecking eligibility.
// SQLite serialises writers; this no-op update supplies the row lock on PostgreSQL.
func lockRetentionRuns(ctx context.Context, tx bun.Tx, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := tx.NewUpdate().Model((*store.Run)(nil)).Set("archived_at = archived_at").
		Where("id IN (?)", bun.List(ids)).Exec(ctx)
	return err
}
