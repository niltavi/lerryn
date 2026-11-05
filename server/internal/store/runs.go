package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/store/dialect"
)

// The run queue: thin wrappers over the dialect's RunQueue. Mutations
// take a transaction to run in, normally one opened with WriteTx.

// ClaimRuns moves up to limit queued runs, oldest first, to preparing and
// assigns them to runnerID with a lease of ttl. It returns their IDs in no
// particular order.
func (db *DB) ClaimRuns(ctx context.Context, tx bun.IDB, runnerID string, limit int, ttl time.Duration) ([]uuid.UUID, error) {
	return db.ClaimRunsCapped(ctx, tx, runnerID, limit, ttl, 0)
}

// ClaimRunsCapped is ClaimRuns that also leaves queued the runs of
// projects with projectCap or more claimed runs; 0 means no cap (tech
// design 8.4). With a cap the runs are claimed one statement at a time,
// so that each statement counts the runs the previous ones claimed. On
// PostgreSQL, claimers on different servers do not see each other's
// uncommitted claims and may exceed the cap by one run each; it is a
// quota, not a lock.
func (db *DB) ClaimRunsCapped(ctx context.Context, tx bun.IDB, runnerID string, limit int, ttl time.Duration, projectCap int) ([]uuid.UUID, error) {
	if runnerID == "" || limit < 1 || ttl <= 0 || projectCap < 0 {
		return nil, errors.New("claim runs: runner, a positive limit, a positive ttl and a non-negative cap are required")
	}
	req := dialect.ClaimRequest{
		RunnerID:           runnerID,
		Limit:              limit,
		LeaseTTL:           ttl,
		ProjectConcurrency: projectCap,
		Now:                Now(),
	}
	if projectCap == 0 {
		return db.dialect.ClaimRuns(ctx, tx, req)
	}
	req.Limit = 1
	var ids []uuid.UUID
	for range limit {
		got, err := db.dialect.ClaimRuns(ctx, tx, req)
		if err != nil {
			return nil, err
		}
		if len(got) == 0 {
			break
		}
		ids = append(ids, got...)
	}
	return ids, nil
}

// RenewRunLeases extends by ttl the leases of those runs in ids that are
// still active and assigned to runnerID, and returns them with their
// status. On PostgreSQL expiry follows the database clock.
func (db *DB) RenewRunLeases(ctx context.Context, tx bun.IDB, runnerID string, ids []uuid.UUID, ttl time.Duration) ([]dialect.RenewedRun, error) {
	if ttl <= 0 {
		return nil, fmt.Errorf("renew run leases: ttl must be positive, got %s", ttl)
	}
	return db.dialect.RenewRunLeases(ctx, tx, dialect.RenewRequest{
		RunnerID: runnerID,
		RunIDs:   ids,
		LeaseTTL: ttl,
		Now:      Now(),
	})
}

// ExpireRunLeases marks active runs whose lease has expired as
// interrupted, recording errorCode and message as the reason, and returns
// their IDs.
func (db *DB) ExpireRunLeases(ctx context.Context, tx bun.IDB, errorCode, message string) ([]uuid.UUID, error) {
	return db.dialect.ExpireRunLeases(ctx, tx, dialect.ExpireRequest{
		Now:          Now(),
		ErrorCode:    errorCode,
		ErrorMessage: message,
	})
}

// RunLeaseLive fences a state operation against lease expiry. Call it
// after taking the run's row lock and after any other lock waits.
func (db *DB) RunLeaseLive(ctx context.Context, tx bun.IDB, id uuid.UUID) (bool, error) {
	live, err := db.dialect.RunLeaseLive(ctx, tx, id, Now())
	if err != nil {
		return false, fmt.Errorf("check run lease: %w", err)
	}
	return live, nil
}

// LiveClaims filters a claim batch after its external IO and notifications.
// A new autocommit statement avoids a snapshot held across that work; it
// neither locks nor renews runs. The result is in no particular order.
func (db *DB) LiveClaims(ctx context.Context, runnerID string, ids []uuid.UUID) ([]uuid.UUID, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	live, err := db.dialect.LiveClaims(ctx, db.DB, runnerID, ids)
	if err != nil {
		return nil, fmt.Errorf("read live claims: %w", err)
	}
	return live, nil
}
