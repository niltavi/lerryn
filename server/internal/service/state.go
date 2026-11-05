package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/store"
)

// GetState returns the snapshot pinned when the run was claimed, not the
// task's head at request time. Other runs can publish while this one prepares.
func (c *Coordinator) GetState(ctx context.Context, runnerID string, runID uuid.UUID) (runproto.StateSnapshot, error) {
	var input store.RunStateRef
	err := c.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if _, err = c.lockStateRun(ctx, tx, runnerID, runID); err != nil {
			return err
		}
		input, err = stateInput(ctx, tx, runID)
		return err
	})
	if err != nil {
		return runproto.StateSnapshot{}, err
	}
	snapshot := runproto.StateSnapshot{Version: runproto.StateFormatVersion, Entries: []runproto.StateEntry{}}
	if input.BlobHash != "" {
		data, err := c.readStateBlob(ctx, input.BlobHash)
		if err != nil {
			return runproto.StateSnapshot{}, err
		}
		var size int64
		snapshot, size, err = runproto.DecodeState(data)
		if err != nil {
			return runproto.StateSnapshot{}, fmt.Errorf("decode input task state: %w", err)
		}
		if size != input.Size {
			return runproto.StateSnapshot{}, errors.New("input task state size differs from its reference")
		}
		canonical, _, err := runproto.EncodeState(snapshot)
		if err != nil || !bytes.Equal(canonical, data) {
			return runproto.StateSnapshot{}, errors.New("input task state is not canonical")
		}
	}
	// A long object read must not return private state after the run lost
	// its lease or another request settled it.
	err = c.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		_, err := c.lockStateRun(ctx, tx, runnerID, runID)
		return err
	})
	if err != nil {
		return runproto.StateSnapshot{}, err
	}
	return snapshot, nil
}

// PutState prepares a complete output candidate. Publishing it is left
// to Finish, after blob storage and tombstone recovery have both succeeded.
func (c *Coordinator) PutState(ctx context.Context, runnerID string, runID uuid.UUID, snapshot runproto.StateSnapshot) (err error) {
	// Refuse unassigned and legacy runs before doing object-store work.
	err = c.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if _, err := c.lockStateRun(ctx, tx, runnerID, runID); err != nil {
			return err
		}
		_, err := stateInput(ctx, tx, runID)
		return err
	})
	if err != nil {
		return err
	}
	data, size, err := runproto.EncodeState(snapshot)
	if err != nil {
		return fmt.Errorf("%w: invalid task state: %w", ErrProtocol, err)
	}
	hash := blob.Sum(data)
	// A failed upload can leave a stored object without a reference. A
	// release is safe even when this hash is still used by another row.
	defer func() {
		if err == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		cleanupErr := c.db.WriteTx(cleanupCtx, func(ctx context.Context, tx bun.Tx) error {
			return releaseBlobs(ctx, tx, []string{hash}, store.Now())
		})
		if cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("release failed state upload: %w", cleanupErr))
		}
	}()
	got, err := c.blobs.Put(ctx, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("store output task state: %w", err)
	}
	if got != hash {
		return errors.New("stored output task state has an unexpected hash")
	}
	var restore []string
	err = c.retry(ctx, func(ctx context.Context, tx bun.Tx) error {
		restore = nil
		if _, err := c.lockStateRun(ctx, tx, runnerID, runID); err != nil {
			return err
		}
		if _, err := stateInput(ctx, tx, runID); err != nil {
			return err
		}
		var output store.RunStateRef
		err := tx.NewSelect().Model(&output).Where("run_id = ? AND role = ?", runID, store.RunStateOutput).Scan(ctx)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			output = store.RunStateRef{RunID: runID, Role: store.RunStateOutput, BlobHash: hash, Size: size}
			if _, err := tx.NewInsert().Model(&output).Exec(ctx); err != nil {
				return fmt.Errorf("store task state candidate: %w", err)
			}
		case err != nil:
			return fmt.Errorf("read task state candidate: %w", err)
		case output.BlobHash != hash || output.Size != size:
			return fmt.Errorf("%w: a run cannot replace its task state candidate", ErrProtocol)
		}
		restore, err = claimBlobs(ctx, tx, []string{hash})
		if err != nil {
			return err
		}
		return c.requireStateLease(ctx, tx, runID)
	})
	if err != nil {
		return err
	}
	if _, err := restoreBlobs(ctx, c.blobs, restore, func(string) io.Reader { return bytes.NewReader(data) }); err != nil {
		return err
	}
	stored, err := c.readStateBlob(ctx, hash)
	if err != nil {
		return err
	}
	if !bytes.Equal(stored, data) {
		return errors.New("stored output task state differs from the submitted snapshot")
	}
	return c.retry(ctx, func(ctx context.Context, tx bun.Tx) error {
		if _, err := c.lockStateRun(ctx, tx, runnerID, runID); err != nil {
			return err
		}
		if _, err := stateInput(ctx, tx, runID); err != nil {
			return err
		}
		res, err := tx.NewUpdate().Model((*store.RunStateRef)(nil)).Set("ready = ?", true).
			Where("run_id = ? AND role = ? AND blob_hash = ? AND size = ?", runID, store.RunStateOutput, hash, size).Exec(ctx)
		if err := changedOne(res, err); err != nil {
			return fmt.Errorf("mark task state ready: %w", err)
		}
		return c.requireStateLease(ctx, tx, runID)
	})
}

func stateInput(ctx context.Context, tx bun.IDB, runID uuid.UUID) (store.RunStateRef, error) {
	var input store.RunStateRef
	err := tx.NewSelect().Model(&input).Where("run_id = ? AND role = ?", runID, store.RunStateInput).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return input, fmt.Errorf("%w: the run has no task state session", ErrProtocol)
	}
	return input, err
}

// lockStateRun fences state IO against concurrent Finish and Reconcile.
// An ordinary SELECT would leave a gap before the candidate row is written.
func (c *Coordinator) lockStateRun(ctx context.Context, tx bun.Tx, runnerID string, runID uuid.UUID) (store.Run, error) {
	res, err := tx.NewUpdate().Model((*store.Run)(nil)).Set("lease_expires_at = lease_expires_at").
		Where("id = ? AND runner_id = ? AND status IN (?)", runID, runnerID, bun.List(claimedStatuses)).Exec(ctx)
	if err := changedOne(res, err); err != nil {
		if errors.Is(err, errNotChanged) {
			return store.Run{}, runproto.ErrLeaseLost
		}
		return store.Run{}, err
	}
	run, err := ownedRun(ctx, tx, runnerID, runID)
	if err != nil {
		return store.Run{}, err
	}
	if err := c.requireStateLease(ctx, tx, runID); err != nil {
		return store.Run{}, err
	}
	return run, nil
}

func (c *Coordinator) requireStateLease(ctx context.Context, tx bun.IDB, runID uuid.UUID) error {
	live, err := c.db.RunLeaseLive(ctx, tx, runID)
	if err != nil {
		return err
	}
	if !live {
		return runproto.ErrLeaseLost
	}
	return nil
}

func (c *Coordinator) readStateBlob(ctx context.Context, hash string) ([]byte, error) {
	rc, err := c.blobs.Get(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("open task state object: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(&stateContextReader{ctx: ctx, r: rc}, runproto.MaxStateWireBytes+1))
	err = errors.Join(err, rc.Close())
	if err != nil {
		return nil, fmt.Errorf("read task state object: %w", err)
	}
	if len(data) > runproto.MaxStateWireBytes || blob.Sum(data) != hash {
		return nil, errors.New("task state object exceeds its limit or has an invalid hash")
	}
	return data, nil
}

type stateContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *stateContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
