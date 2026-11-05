package store_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/store"
)

// Retries wait in the queue until not_before (tech design 8.5).
func TestClaimSkipsRunsBeforeNotBefore(t *testing.T) {
	t.Parallel()
	q := newQueue(t)
	waiting := q.mustInsertRun(func(r *store.Run) { r.NotBefore = store.Now().Add(time.Hour) })
	due := q.mustInsertRun(func(r *store.Run) { r.NotBefore = store.Now().Add(-time.Second) })
	plain := q.mustInsertRun()
	runner := q.newRunner()
	got := q.mustClaim(runner, 10)
	slices.SortFunc(got, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	want := []uuid.UUID{due.ID, plain.ID}
	slices.SortFunc(want, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	if !slices.Equal(got, want) {
		t.Fatalf("claimed %v, want %v", got, want)
	}
	if q.get(waiting.ID).Status != store.RunQueued {
		t.Fatal("a run before its not_before was claimed")
	}
}

// A project at its concurrency cap keeps its runs queued while other
// projects' runs are claimed (tech design 8.4).
func TestClaimHonoursProjectConcurrency(t *testing.T) {
	t.Parallel()
	q := newQueue(t)
	ctx := context.Background()
	other := newProject(q.user.ID, "other", false)
	otherTask := store.Task{ID: store.NewID(), ProjectID: other.ID, Name: "o", LatestSeq: 1, CreatedBy: q.user.ID, CreatedAt: store.Now(), UpdatedAt: store.Now()}
	otherVersion := store.TaskVersion{ID: store.NewID(), TaskID: otherTask.ID, Seq: 1, Manifest: "{}", TreeHash: "x", CreatedBy: q.user.ID, CreatedAt: store.Now()}
	for _, m := range []any{&other, &otherTask, &otherVersion} {
		if _, err := q.db.NewInsert().Model(m).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	first := q.mustInsertRun()
	q.mustInsertRun()
	q.mustInsertRun()
	foreign := q.mustInsertRun(func(r *store.Run) {
		r.ProjectID, r.TaskID, r.VersionID = other.ID, otherTask.ID, otherVersion.ID
	})
	runner := q.newRunner()
	claim := func(limit int) []uuid.UUID {
		t.Helper()
		var ids []uuid.UUID
		err := q.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
			var err error
			ids, err = q.db.ClaimRunsCapped(ctx, tx, runner, limit, leaseTTL, 2)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	// One batch of four: two of the project, then the other project's run.
	got := claim(4)
	if len(got) != 3 || !slices.Contains(got, first.ID) || !slices.Contains(got, foreign.ID) {
		t.Fatalf("claimed %v", got)
	}
	if again := claim(4); len(again) != 0 {
		t.Fatalf("claimed %v beyond the cap", again)
	}
	if err := q.setStatus(first.ID, store.RunCompleted); err != nil {
		t.Fatal(err)
	}
	if freed := claim(4); len(freed) != 1 {
		t.Fatalf("after a run ended claimed %v, want one", freed)
	}
	if _, err := q.db.ClaimRunsCapped(ctx, q.db, runner, 1, leaseTTL, -1); err == nil {
		t.Fatal("a negative cap must be rejected")
	}
}
