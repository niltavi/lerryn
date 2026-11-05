package service_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func stateSnapshot(content string) runproto.StateSnapshot {
	return runproto.StateSnapshot{Version: runproto.StateFormatVersion, Entries: []runproto.StateEntry{{Path: "cursor", Content: []byte(content)}}}
}

func (f *fixture) stateRun(p *auth.Principal, taskID uuid.UUID, runner string) store.Run {
	f.t.Helper()
	run := f.start(p, taskID)
	claimed := f.claim(runner, 1)
	if len(claimed) != 1 || claimed[0].RunID != run.ID || !claimed[0].HasState {
		f.t.Fatalf("state claim = %+v", claimed)
	}
	if err := f.coord.Started(f.ctx, runner, run.ID, "", store.Now()); err != nil {
		f.t.Fatal(err)
	}
	return run
}

func (f *fixture) publishState(runner string, runID uuid.UUID, snapshot runproto.StateSnapshot) {
	f.t.Helper()
	if err := f.coord.PutState(f.ctx, runner, runID, snapshot); err != nil {
		f.t.Fatalf("put state: %v", err)
	}
	zero := 0
	if err := f.coord.Finish(f.ctx, runner, runID, runproto.Finish{Status: runproto.StatusCompleted, ExitCode: &zero}); err != nil {
		f.t.Fatalf("publish state: %v", err)
	}
}

func (f *fixture) stateHead(taskID uuid.UUID) store.TaskState {
	f.t.Helper()
	var head store.TaskState
	err := f.db.NewSelect().Model(&head).Where("task_id = ?", taskID).Scan(f.ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		f.t.Fatal(err)
	}
	return head
}

func (f *fixture) stateRefCount(runID uuid.UUID) int64 {
	f.t.Helper()
	n, err := f.db.NewSelect().Model((*store.RunStateRef)(nil)).Where("run_id = ?", runID).Count(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestTaskStateCrossRunnerFlow(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "state", allowOverlap())
	r1, r2 := f.runner("r1"), f.runner("r2")
	first := f.stateRun(alice, task.Task.ID, r1)
	empty := runproto.StateSnapshot{Version: runproto.StateFormatVersion, Entries: []runproto.StateEntry{}}
	if got, err := f.coord.GetState(f.ctx, r1, first.ID); err != nil || !reflect.DeepEqual(got, empty) {
		t.Fatalf("initial state = %+v, %v", got, err)
	}
	snapshot := stateSnapshot("42")
	snapshot.Entries = append(snapshot.Entries, runproto.StateEntry{Path: "empty", Directory: true}, runproto.StateEntry{Path: "zero"})
	if err := f.coord.PutState(f.ctx, r1, first.ID, snapshot); err != nil {
		t.Fatal(err)
	}
	if head := f.stateHead(task.Task.ID); head.BlobHash != "" {
		t.Fatal("PutState published before Finish")
	}
	if got, err := f.coord.GetState(f.ctx, r1, first.ID); err != nil || !reflect.DeepEqual(got, empty) {
		t.Fatalf("output changed the pinned input: %+v, %v", got, err)
	}
	f.publishState(r1, first.ID, snapshot)
	encoded, size, err := runproto.EncodeState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	head := f.stateHead(task.Task.ID)
	if head.BlobHash != blob.Sum(encoded) || head.Size != size || f.stateRefCount(first.ID) != 0 {
		t.Fatalf("published head = %+v", head)
	}
	second := f.stateRun(alice, task.Task.ID, r2)
	got, err := f.coord.GetState(f.ctx, r2, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	read, _, _ := runproto.EncodeState(got)
	if !bytes.Equal(read, encoded) {
		t.Fatal("another runner did not read the complete published snapshot")
	}
}

func TestTaskStateRequiresReadyCandidate(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "state", allowOverlap())
	runner := f.runner("r")
	run := f.stateRun(alice, task.Task.ID, runner)
	finish := runproto.Finish{Status: runproto.StatusCompleted}
	wantErr(t, f.coord.Finish(f.ctx, runner, run.ID, finish), service.ErrProtocol)
	if got := f.get(alice, run.ID); got.Run.Status != store.RunRunning || f.stateHead(task.Task.ID).BlobHash != "" {
		t.Fatal("missing output committed a terminal state or head")
	}
	if err := f.coord.PutState(f.ctx, runner, run.ID, stateSnapshot("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.NewUpdate().Model((*store.RunStateRef)(nil)).Set("ready = ?", false).
		Where("run_id = ? AND role = ?", run.ID, store.RunStateOutput).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	wantErr(t, f.coord.Finish(f.ctx, runner, run.ID, finish), service.ErrProtocol)
	if got := f.get(alice, run.ID); got.Run.Status != store.RunRunning {
		t.Fatal("unready output committed the terminal update")
	}
	wantErr(t, f.coord.PutState(f.ctx, runner, run.ID, stateSnapshot("different")), service.ErrProtocol)
	f.publishState(runner, run.ID, stateSnapshot("first"))
}

func TestTaskStateEmptyClearsAndFailuresPreserve(t *testing.T) {
	for _, status := range []string{runproto.StatusFailed, runproto.StatusCancelled, runproto.StatusTimedOut, runproto.StatusInterrupted, runproto.StatusCompleted} {
		t.Run(status, func(t *testing.T) {
			f := newFixture(t)
			alice := f.user("alice")
			task := f.apply(alice, "state", allowOverlap())
			runner := f.runner("r")
			first := f.stateRun(alice, task.Task.ID, runner)
			f.publishState(runner, first.ID, stateSnapshot("old"))
			old := f.stateHead(task.Task.ID)
			run := f.stateRun(alice, task.Task.ID, runner)
			f.putEmptyState(runner, run.ID)
			if err := f.coord.Finish(f.ctx, runner, run.ID, runproto.Finish{Status: status}); err != nil {
				t.Fatal(err)
			}
			head := f.stateHead(task.Task.ID)
			if status == runproto.StatusCompleted {
				if head.BlobHash == old.BlobHash || head.Size != 0 {
					t.Fatal("an explicitly empty snapshot did not clear the task state")
				}
			} else if head.BlobHash != old.BlobHash {
				t.Fatal("a failed execution replaced the old state")
			}
			if f.stateRefCount(run.ID) != 0 {
				t.Fatal("terminal run retained state references")
			}
		})
	}
}

func TestTaskStateLeaseAndOwnership(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "state", allowOverlap())
	runner, other := f.runner("r"), f.runner("other")
	first := f.stateRun(alice, task.Task.ID, runner)
	f.publishState(runner, first.ID, stateSnapshot("old"))
	old := f.stateHead(task.Task.ID)
	run := f.stateRun(alice, task.Task.ID, runner)
	_, err := f.coord.GetState(f.ctx, other, run.ID)
	wantErr(t, err, runproto.ErrLeaseLost)
	wantErr(t, f.coord.PutState(f.ctx, other, run.ID, stateSnapshot("foreign")), runproto.ErrLeaseLost)
	if err := f.coord.PutState(f.ctx, runner, run.ID, stateSnapshot("pending")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).Set("lease_expires_at = ?", store.Now().Add(-time.Minute)).Where("id = ?", run.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	_, err = f.coord.GetState(f.ctx, runner, run.ID)
	wantErr(t, err, runproto.ErrLeaseLost)
	wantErr(t, f.coord.PutState(f.ctx, runner, run.ID, stateSnapshot("pending")), runproto.ErrLeaseLost)
	wantErr(t, f.coord.Finish(f.ctx, runner, run.ID, runproto.Finish{Status: runproto.StatusCompleted}), runproto.ErrLeaseLost)
	if f.get(alice, run.ID).Run.Status != store.RunRunning || f.stateHead(task.Task.ID).BlobHash != old.BlobHash {
		t.Fatal("expired lease committed a new terminal state or task head")
	}
	ids, err := f.runs.Reconcile(f.ctx)
	if err != nil || !slices.Equal(ids, []uuid.UUID{run.ID}) || f.stateRefCount(run.ID) != 0 {
		t.Fatalf("expired state references were not reconciled: %v, %v", ids, err)
	}
}

func TestTaskStateLastWriterAndReplay(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "state", allowOverlap())
	runner := f.runner("r")
	first, second := f.stateRun(alice, task.Task.ID, runner), f.stateRun(alice, task.Task.ID, runner)
	for i, run := range []store.Run{first, second} {
		if err := f.coord.PutState(f.ctx, runner, run.ID, stateSnapshot(strings.Repeat("x", i+1))); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.coord.Finish(f.ctx, runner, first.ID, runproto.Finish{Status: runproto.StatusCompleted, FinishedAt: store.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := f.coord.Finish(f.ctx, runner, second.ID, runproto.Finish{Status: runproto.StatusCompleted, FinishedAt: store.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	latest := f.stateHead(task.Task.ID)
	wantErr(t, f.coord.Finish(f.ctx, runner, first.ID, runproto.Finish{Status: runproto.StatusCompleted}), runproto.ErrLeaseLost)
	if f.stateHead(task.Task.ID).BlobHash != latest.BlobHash || latest.Size != 2 {
		t.Fatal("an older finish replay or timestamp overrode the last committed state")
	}
}

func TestTaskStateInputPinAndCandidateProtectGC(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "state", allowOverlap())
	runner := f.runner("r")
	first := f.stateRun(alice, task.Task.ID, runner)
	f.publishState(runner, first.ID, stateSnapshot("old"))
	old := f.stateHead(task.Task.ID).BlobHash
	pinned, writer := f.stateRun(alice, task.Task.ID, runner), f.stateRun(alice, task.Task.ID, runner)
	if err := f.coord.PutState(f.ctx, runner, pinned.ID, stateSnapshot("candidate")); err != nil {
		t.Fatal(err)
	}
	candidate, _, err := runproto.EncodeState(stateSnapshot("candidate"))
	if err != nil {
		t.Fatal(err)
	}
	candidateHash := blob.Sum(candidate)
	if _, err := f.db.NewUpdate().Model((*store.RunStateRef)(nil)).Set("ready = ?", false).
		Where("run_id = ? AND role = ?", pinned.ID, store.RunStateOutput).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.NewInsert().Model(&store.BlobRelease{ID: store.NewID(), Hash: candidateHash, ReleasedAt: store.Now()}).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.publishState(runner, writer.ID, stateSnapshot("new"))
	ret := service.NewRetention(f.db, f.blobs, service.RetentionPolicy{}, nil)
	f.cleanup(ret)
	if !f.has(old) || !f.has(candidateHash) {
		t.Fatal("GC removed an active input pin or unready output candidate")
	}
	if got, err := f.coord.GetState(f.ctx, runner, pinned.ID); err != nil || string(got.Entries[0].Content) != "old" {
		t.Fatalf("pinned state = %+v, %v", got, err)
	}
	if err := f.coord.Finish(f.ctx, runner, pinned.ID, runproto.Finish{Status: runproto.StatusFailed}); err != nil {
		t.Fatal(err)
	}
	f.cleanup(ret)
	if f.has(old) {
		t.Fatal("GC kept an unreferenced old snapshot")
	}
	if !f.has(f.stateHead(task.Task.ID).BlobHash) {
		t.Fatal("GC removed the current task head")
	}
}

type stateFaultStore struct {
	blob.Store
	afterPut func(string)
	putError error
	get      func(context.Context, string) (io.ReadCloser, error)
}

func (s *stateFaultStore) Put(ctx context.Context, r io.Reader) (string, error) {
	if s.putError != nil {
		return "", s.putError
	}
	hash, err := s.Store.Put(ctx, r)
	if err == nil && s.afterPut != nil {
		s.afterPut(hash)
	}
	return hash, err
}

func (s *stateFaultStore) Get(ctx context.Context, hash string) (io.ReadCloser, error) {
	if s.get != nil {
		return s.get(ctx, hash)
	}
	return s.Store.Get(ctx, hash)
}

func TestTaskStatePutRepairsCollectorTombstoneBeforeReady(t *testing.T) {
	for _, failRepair := range []bool{false, true} {
		t.Run(map[bool]string{false: "repair succeeds", true: "repair fails then retries"}[failRepair], func(t *testing.T) {
			f := newFixture(t)
			alice := f.user("alice")
			task := f.apply(alice, "state", allowOverlap())
			runner := f.runner("r")
			run := f.stateRun(alice, task.Task.ID, runner)
			bs := &stateFaultStore{Store: f.blobs}
			bs.afterPut = func(hash string) {
				bs.afterPut = nil
				if _, err := f.db.NewInsert().Model(&store.BlobRelease{ID: store.NewID(), Hash: hash, ReleasedAt: store.Now()}).Exec(f.ctx); err != nil {
					t.Fatal(err)
				}
				f.cleanup(service.NewRetention(f.db, f.blobs, service.RetentionPolicy{}, nil))
				if f.has(hash) {
					t.Fatal("fixture did not delete the object between Put and candidate reference")
				}
				if failRepair {
					bs.putError = errors.New("temporary object repair failure")
				}
			}
			coord := service.NewCoordinator(f.db, bs, f.runs, nil)
			err := coord.PutState(f.ctx, runner, run.ID, stateSnapshot("checkpoint"))
			if failRepair {
				if err == nil {
					t.Fatal("failed repair was accepted")
				}
				wantErr(t, coord.Finish(f.ctx, runner, run.ID, runproto.Finish{Status: runproto.StatusCompleted}), service.ErrProtocol)
				bs.putError = nil
				err = coord.PutState(f.ctx, runner, run.ID, stateSnapshot("checkpoint"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if f.stateHead(task.Task.ID).BlobHash != "" {
				t.Fatal("candidate repair published the task head early")
			}
			if err := coord.Finish(f.ctx, runner, run.ID, runproto.Finish{Status: runproto.StatusCompleted}); err != nil {
				t.Fatal(err)
			}
			if !f.has(f.stateHead(task.Task.ID).BlobHash) {
				t.Fatal("Finish published a missing object")
			}
		})
	}
}

func TestTaskStateInputRejectsDamagedObjects(t *testing.T) {
	for _, mode := range []string{"missing", "different valid JSON", "close error", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			alice := f.user("alice")
			task := f.apply(alice, "state", allowOverlap())
			runner := f.runner("r")
			first := f.stateRun(alice, task.Task.ID, runner)
			f.publishState(runner, first.ID, stateSnapshot("old"))
			head := f.stateHead(task.Task.ID)
			run := f.stateRun(alice, task.Task.ID, runner)
			bs := &stateFaultStore{Store: f.blobs}
			bs.get = func(ctx context.Context, hash string) (io.ReadCloser, error) {
				switch mode {
				case "missing":
					return nil, blob.ErrNotFound
				case "different valid JSON":
					body, _, _ := runproto.EncodeState(stateSnapshot("tampered"))
					return io.NopCloser(bytes.NewReader(body)), nil
				case "oversized":
					return io.NopCloser(io.LimitReader(infiniteStateReader{}, runproto.MaxStateWireBytes+1)), nil
				default:
					rc, err := f.blobs.Get(ctx, hash)
					return &stateCloseError{ReadCloser: rc}, err
				}
			}
			coord := service.NewCoordinator(f.db, bs, f.runs, nil)
			if _, err := coord.GetState(f.ctx, runner, run.ID); err == nil {
				t.Fatal("a referenced damaged object was treated as valid or initially empty")
			}
			if f.stateHead(task.Task.ID).BlobHash != head.BlobHash {
				t.Fatal("failed download changed the head")
			}
		})
	}
}

type stateCloseError struct{ io.ReadCloser }

func (r *stateCloseError) Close() error {
	return errors.Join(r.ReadCloser.Close(), errors.New("state object close failed"))
}

type infiniteStateReader struct{}

func (infiniteStateReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestTaskStateLegacyRunnerCannotClaimButCanFinish(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "state", allowOverlap())
	runner := f.runner("legacy")
	if _, err := f.db.NewUpdate().Model((*store.Runner)(nil)).Set("protocol_version = ?", 1).Where("id = ?", runner).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	run := f.start(alice, task.Task.ID)
	_, err := f.coord.Claim(f.ctx, runner, 1)
	wantErr(t, err, service.ErrProtocol)
	if f.get(alice, run.ID).Run.Status != store.RunQueued {
		t.Fatal("legacy runner claimed new work")
	}
	// Simulate an already claimed v1 run at the upgrade boundary.
	var ids []uuid.UUID
	err = f.db.WriteTx(f.ctx, func(ctx context.Context, tx bun.Tx) error {
		var err error
		ids, err = f.db.ClaimRuns(ctx, tx, runner, 1, runproto.LeaseTTL)
		return err
	})
	if err != nil || !slices.Equal(ids, []uuid.UUID{run.ID}) {
		t.Fatalf("legacy claim fixture = %v, %v", ids, err)
	}
	if err := f.coord.Started(f.ctx, runner, run.ID, "", store.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.coord.Finish(f.ctx, runner, run.ID, runproto.Finish{Status: runproto.StatusCompleted}); err != nil {
		t.Fatal(err)
	}
	if f.stateHead(task.Task.ID).BlobHash != "" {
		t.Fatal("legacy Finish created task state")
	}
	report, err := f.tasks.Validate(f.ctx, alice, task.Task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(report.Warnings, func(issue service.Issue) bool { return issue.Code == "runner_unavailable" }) {
		t.Fatal("legacy-only capacity was advertised as runnable")
	}
}

func TestTaskStateRunRetentionReleasesLeftoverRefs(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "state", allowOverlap())
	runner := f.runner("r")
	first := f.stateRun(alice, task.Task.ID, runner)
	f.publishState(runner, first.ID, stateSnapshot("current"))
	head := f.stateHead(task.Task.ID)
	old := f.stateRun(alice, task.Task.ID, runner)
	snapshot := stateSnapshot("orphan candidate")
	if err := f.coord.PutState(f.ctx, runner, old.ID, snapshot); err != nil {
		t.Fatal(err)
	}
	encoded, _, err := runproto.EncodeState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	candidate := blob.Sum(encoded)
	// Model a terminal row left by an older or interrupted cleanup path.
	if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).Set("status = ?", store.RunFailed).
		Set("finished_at = ?", store.Now().Add(-3*day)).Where("id = ?", old.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.start(alice, task.Task.ID)
	f.cleanup(service.NewRetention(f.db, f.blobs, service.RetentionPolicy{Runs: day}, nil))
	if f.stateRefCount(old.ID) != 0 || f.has(candidate) || !f.has(head.BlobHash) || f.stateHead(task.Task.ID).BlobHash != head.BlobHash {
		t.Fatal("run retention leaked its candidate or collected the persistent task head")
	}
}
