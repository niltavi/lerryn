package store_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/store"
)

// A claim records when and by which runner, and leaves retries that are
// held back by not_before in the queue.
func TestClaimRecordsRunnerAndRespectsNotBefore(t *testing.T) {
	t.Parallel()
	q := newQueue(t)
	later := q.mustInsertRun(func(r *store.Run) { r.NotBefore = store.Now().Add(time.Hour) })
	due := q.mustInsertRun(func(r *store.Run) { r.NotBefore = store.Now().Add(-time.Second) })
	runner := q.newRunner()
	before := store.Now()
	if got := q.mustClaim(runner, 10); !slices.Equal(got, []uuid.UUID{due.ID}) {
		t.Fatalf("claimed %v, want only the due run %v", got, due.ID)
	}
	run := q.get(due.ID)
	if run.RunnerName != "test" || run.ClaimedAt.Before(before) || run.ClaimedAt.After(store.Now()) {
		t.Fatalf("claimed run = name %q, claimed at %v", run.RunnerName, run.ClaimedAt)
	}
	if got := q.get(later.ID); got.Status != store.RunQueued {
		t.Fatalf("held-back run is %s", got.Status)
	}
}

func (q *queue) addRunnerLabels(runner string, labels ...string) {
	q.t.Helper()
	for _, l := range labels {
		row := store.RunnerLabel{ID: store.NewID(), RunnerID: runner, Label: l}
		if _, err := q.db.NewInsert().Model(&row).Exec(context.Background()); err != nil {
			q.t.Fatal(err)
		}
	}
}

func (q *queue) requireLabels(run uuid.UUID, labels ...string) {
	q.t.Helper()
	for _, l := range labels {
		row := store.RunRequiredLabel{ID: store.NewID(), RunID: run, Label: l}
		if _, err := q.db.NewInsert().Model(&row).Exec(context.Background()); err != nil {
			q.t.Fatal(err)
		}
	}
}

// A run is claimed only by a runner that has every label it requires;
// runs it cannot take do not block the runs behind them.
func TestClaimMatchesRunnerLabels(t *testing.T) {
	t.Parallel()
	q := newQueue(t)
	intranet := q.mustInsertRun()
	q.requireLabels(intranet.ID, "intranet")
	both := q.mustInsertRun()
	q.requireLabels(both.ID, "intranet", "gpu")
	plain := q.mustInsertRun()

	bare := q.newRunner()
	inner := q.newRunner()
	q.addRunnerLabels(inner, "default", "intranet")

	if got := q.mustClaim(bare, 10); !slices.Equal(got, []uuid.UUID{plain.ID}) {
		t.Fatalf("a runner without labels claimed %v, want only %v", got, plain.ID)
	}
	if got := q.mustClaim(inner, 10); !slices.Equal(got, []uuid.UUID{intranet.ID}) {
		t.Fatalf("the intranet runner claimed %v, want only %v", got, intranet.ID)
	}
	gpu := q.newRunner()
	q.addRunnerLabels(gpu, "intranet", "gpu")
	if got := q.mustClaim(gpu, 10); !slices.Equal(got, []uuid.UUID{both.ID}) {
		t.Fatalf("the gpu runner claimed %v, want %v", got, both.ID)
	}
}

func TestDisabledRunnerClaimsNothing(t *testing.T) {
	t.Parallel()
	q := newQueue(t)
	run := q.mustInsertRun()
	runner := q.newRunner()
	if _, err := q.db.NewUpdate().Model((*store.Runner)(nil)).Set("disabled_at = ?", store.Now()).
		Where("id = ?", runner).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := q.mustClaim(runner, 10); len(got) != 0 {
		t.Fatalf("a disabled runner claimed %v", got)
	}
	other := q.newRunner()
	if got := q.mustClaim(other, 10); !slices.Equal(got, []uuid.UUID{run.ID}) {
		t.Fatalf("an enabled runner claimed %v, want %v", got, run.ID)
	}
}
