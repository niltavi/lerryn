package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/store"
)

// lockStateTask serialises a task's head reads and replacements on both
// databases, including the first snapshot when task_states has no row.
// Run rows are always locked before task rows; batches sort task IDs.
func lockStateTask(ctx context.Context, tx bun.Tx, id uuid.UUID) error {
	res, err := tx.NewUpdate().Model((*store.Task)(nil)).Set("updated_at = updated_at").Where("id = ?", id).Exec(ctx)
	if err := changedOne(res, err); err != nil {
		return fmt.Errorf("lock task state: %w", err)
	}
	return nil
}

func pinStateInputs(ctx context.Context, tx bun.Tx, assignments []runproto.Assignment) error {
	ids := make([]uuid.UUID, 0, len(assignments))
	for _, a := range assignments {
		if !slices.Contains(ids, a.TaskID) {
			ids = append(ids, a.TaskID)
		}
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	for _, id := range ids {
		if err := lockStateTask(ctx, tx, id); err != nil {
			return err
		}
	}
	for i := range assignments {
		a := &assignments[i]
		var head store.TaskState
		err := tx.NewSelect().Model(&head).Where("task_id = ?", a.TaskID).Scan(ctx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read task state at claim: %w", err)
		}
		ref := store.RunStateRef{RunID: a.RunID, Role: store.RunStateInput, BlobHash: head.BlobHash, Size: head.Size, Ready: true}
		if _, err := tx.NewInsert().Model(&ref).Exec(ctx); err != nil {
			return fmt.Errorf("pin run input state: %w", err)
		}
		a.HasState = true
	}
	return nil
}

// finishTaskState runs after the guarded terminal UPDATE has locked the
// run. A ready candidate remains a live reference until the same
// transaction makes it the task head, so publication needs no blob IO.
func (c *Coordinator) finishTaskState(ctx context.Context, tx bun.Tx, run store.Run, status string) (bool, error) {
	var input store.RunStateRef
	err := tx.NewSelect().Model(&input).Where("run_id = ? AND role = ?", run.ID, store.RunStateInput).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		// Already claimed v1 runs predate the state session. Their
		// terminal reports preserve any newer task state.
		return false, releaseRunStateRefs(ctx, tx, []uuid.UUID{run.ID}, store.Now())
	}
	if err != nil {
		return true, fmt.Errorf("read state session at finish: %w", err)
	}
	var old string
	if status == runproto.StatusCompleted {
		if err := lockStateTask(ctx, tx, run.TaskID); err != nil {
			return true, err
		}
		var output store.RunStateRef
		err := tx.NewSelect().Model(&output).Where("run_id = ? AND role = ?", run.ID, store.RunStateOutput).Scan(ctx)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !output.Ready {
			return true, fmt.Errorf("%w: a completed run must deliver its task state", ErrProtocol)
		}
		if err != nil {
			return true, fmt.Errorf("read output state at finish: %w", err)
		}
		var head store.TaskState
		err = tx.NewSelect().Model(&head).Where("task_id = ?", run.TaskID).Scan(ctx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return true, fmt.Errorf("read previous task state: %w", err)
		}
		old = head.BlobHash
		head = store.TaskState{TaskID: run.TaskID, BlobHash: output.BlobHash, Size: output.Size, UpdatedAt: store.Now()}
		if _, err := tx.NewInsert().Model(&head).On("CONFLICT (task_id) DO UPDATE").
			Set("blob_hash = EXCLUDED.blob_hash").Set("size = EXCLUDED.size").Set("updated_at = EXCLUDED.updated_at").Exec(ctx); err != nil {
			return true, fmt.Errorf("publish task state: %w", err)
		}
	}
	if err := releaseRunStateRefs(ctx, tx, []uuid.UUID{run.ID}, store.Now(), old); err != nil {
		return true, err
	}
	// Check after lock waits and release-row conflicts. An expired lease
	// rolls back the terminal state and the head replacement together.
	return true, c.requireStateLease(ctx, tx, run.ID)
}

// releaseRunStateRefs removes the run's pins and candidates in the same
// transaction that ends or deletes it. Cascading deletes alone would
// leave their objects outside the retention collector's release queue.
func releaseRunStateRefs(ctx context.Context, tx bun.Tx, ids []uuid.UUID, now time.Time, extra ...string) error {
	hashes := slices.Clone(extra)
	if len(ids) > 0 {
		var refs []store.RunStateRef
		if err := tx.NewSelect().Model(&refs).Column("blob_hash").Where("run_id IN (?)", bun.List(ids)).Scan(ctx); err != nil {
			return fmt.Errorf("read run state references: %w", err)
		}
		for _, ref := range refs {
			hashes = append(hashes, ref.BlobHash)
		}
		if _, err := tx.NewDelete().Model((*store.RunStateRef)(nil)).Where("run_id IN (?)", bun.List(ids)).Exec(ctx); err != nil {
			return fmt.Errorf("remove run state references: %w", err)
		}
	}
	// Different tasks can share several hashes. Lock their release rows
	// in the same order when concurrent runs replace those snapshots.
	slices.Sort(hashes)
	return releaseBlobs(ctx, tx, hashes, now)
}
