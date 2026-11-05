package dialect

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// RunQueue holds the hot-path queries on the runs table that cannot be
// written once for both databases (tech design 4.4, 6.2): claiming queued
// runs, renewing run leases, and the reconciler's sweep of expired
// leases. They were proven in the S1 spike.
//
// The status values in the SQL are those of store.Run; the active ones,
// preparing, running and cancelling, are the statuses a claimed run holds
// a lease in.
type RunQueue interface {
	// ClaimRuns moves up to req.Limit queued runs, oldest first, to
	// preparing, assigns them to req.RunnerID and gives them a lease of
	// req.LeaseTTL. It returns their IDs in no particular order. Runs
	// whose not_before lies in the future are left queued, as are runs of
	// projects that already have req.ProjectConcurrency claimed runs.
	// Call it inside a write transaction.
	ClaimRuns(ctx context.Context, db bun.IDB, req ClaimRequest) ([]uuid.UUID, error)
	// RenewRunLeases extends the leases of the runs in req.RunIDs that are
	// still active and assigned to req.RunnerID, and returns them with
	// their current status. Runs missing from the result are no longer
	// the runner's.
	RenewRunLeases(ctx context.Context, db bun.IDB, req RenewRequest) ([]RenewedRun, error)
	// ExpireRunLeases marks active runs whose lease has expired as
	// interrupted, with req's error, and returns their IDs.
	ExpireRunLeases(ctx context.Context, db bun.IDB, req ExpireRequest) ([]uuid.UUID, error)
	// RunLeaseLive checks expiry against the same clock used for leases.
	// The caller holds the run's row lock while fencing a state update.
	RunLeaseLive(ctx context.Context, db bun.IDB, id uuid.UUID, now time.Time) (bool, error)
	// LiveClaims returns candidates still preparing or cancelling for the
	// runner with a live lease. A DB, rather than a transaction, ensures a
	// fresh statement snapshot after the caller's external IO.
	LiveClaims(ctx context.Context, db *bun.DB, runnerID string, ids []uuid.UUID) ([]uuid.UUID, error)
}

// labelsMatch is the claim condition on runner labels (tech design 4.4): a
// queued run qualifies when none of its required labels is missing from
// the claiming runner. It is written with NOT EXISTS so that it runs the
// same on both databases, without arrays. It refers to the queued row as
// "runs"; runnerArg is the claim query's placeholder for the runner ID.
// A disabled runner matches nothing, so it claims no runs at all.
func labelsMatch(runnerArg string) string {
	return `NOT EXISTS (
				SELECT 1 FROM run_required_labels rl
				WHERE rl.run_id = runs.id AND NOT EXISTS (
					SELECT 1 FROM runner_labels l
					WHERE l.runner_id = ` + runnerArg + ` AND l.label = rl.label))
			AND NOT EXISTS (
				SELECT 1 FROM runners rn
				WHERE rn.id = ` + runnerArg + ` AND rn.disabled_at IS NOT NULL)`
}

// ClaimRequest is the input of RunQueue.ClaimRuns.
type ClaimRequest struct {
	RunnerID string
	Limit    int
	LeaseTTL time.Duration
	// ProjectConcurrency caps the claimed runs (preparing, running or
	// cancelling) per project; 0 means no cap. The cap is checked per
	// statement, so runs picked by the same statement do not see each
	// other: callers that set it claim one run per statement.
	ProjectConcurrency int
	// Now is the application clock. PostgreSQL ignores it and uses the
	// database clock, so that servers with skewed clocks agree.
	Now time.Time
}

// RenewRequest is the input of RunQueue.RenewRunLeases.
type RenewRequest struct {
	RunnerID string
	RunIDs   []uuid.UUID
	LeaseTTL time.Duration
	// Now is the application clock; PostgreSQL ignores it.
	Now time.Time
}

// RenewedRun is a run whose lease was renewed.
type RenewedRun struct {
	ID     uuid.UUID `bun:"id"`
	Status string    `bun:"status"`
}

// ExpireRequest is the input of RunQueue.ExpireRunLeases.
type ExpireRequest struct {
	// Now is the application clock. It is written as finished_at on both
	// databases; only PostgreSQL compares leases with its own clock.
	Now          time.Time
	ErrorCode    string
	ErrorMessage string
}
