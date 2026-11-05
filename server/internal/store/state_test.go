package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestStateLeaseUsesBackendClock(t *testing.T) {
	q := newQueue(t)
	ctx := context.Background()
	run := q.mustInsertRun()
	q.mustClaim(q.newRunner(), 1)
	if live, err := q.db.RunLeaseLive(ctx, q.db, run.ID); err != nil || !live {
		t.Fatalf("fresh lease = %t, %v", live, err)
	}
	// PostgreSQL must ignore the caller's skewed application clock.
	live, err := q.db.Native().RunLeaseLive(ctx, q.db, run.ID, store.Now().Add(24*time.Hour))
	if err != nil || live != q.isPostgres() {
		t.Fatalf("lease with skewed application clock = %t, %v", live, err)
	}
	if _, err := q.db.NewUpdate().Model((*store.Run)(nil)).Set("lease_expires_at = ?", store.Now().Add(-time.Hour)).Where("id = ?", run.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if live, err := q.db.RunLeaseLive(ctx, q.db, run.ID); err != nil || live {
		t.Fatalf("expired unswept lease = %t, %v", live, err)
	}
}

func TestStateReferenceConstraints(t *testing.T) {
	q := newQueue(t)
	ctx := context.Background()
	run := q.mustInsertRun()
	hash := blob.Sum([]byte("state"))
	for _, tt := range []struct {
		name string
		ref  store.RunStateRef
	}{
		{"missing output hash", store.RunStateRef{Role: store.RunStateOutput}},
		{"unready input", store.RunStateRef{Role: store.RunStateInput}},
		{"nonempty missing input", store.RunStateRef{Role: store.RunStateInput, Ready: true, Size: 1}},
		{"unknown role", store.RunStateRef{Role: "other", BlobHash: hash}},
		{"negative size", store.RunStateRef{Role: store.RunStateOutput, BlobHash: hash, Size: -1}},
		{"oversized", store.RunStateRef{Role: store.RunStateOutput, BlobHash: hash, Size: runproto.MaxStateBytes + 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.ref.RunID = run.ID
			if _, err := q.db.NewInsert().Model(&tt.ref).Exec(ctx); err == nil {
				t.Fatal("invalid state reference was accepted")
			}
		})
	}
	for _, ref := range []store.RunStateRef{
		{RunID: run.ID, Role: store.RunStateInput, Ready: true},
		{RunID: run.ID, Role: store.RunStateOutput, BlobHash: hash, Size: runproto.MaxStateBytes},
	} {
		if _, err := q.db.NewInsert().Model(&ref).Exec(ctx); err != nil {
			t.Fatalf("valid state reference: %v", err)
		}
	}
	head := store.TaskState{TaskID: q.task.ID, BlobHash: hash, Size: runproto.MaxStateBytes + 1, UpdatedAt: store.Now()}
	if _, err := q.db.NewInsert().Model(&head).Exec(ctx); err == nil {
		t.Fatal("oversized task state was accepted")
	}
	head.Size = runproto.MaxStateBytes
	if _, err := q.db.NewInsert().Model(&head).Exec(ctx); err != nil {
		t.Fatal(err)
	}
}
