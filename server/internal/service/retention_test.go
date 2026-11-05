package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

const day = 24 * time.Hour

func (f *fixture) addArtifact(runner string, runID uuid.UUID, path, content string) store.Artifact {
	f.t.Helper()
	if err := f.coord.AddArtifact(f.ctx, runner, runID, runproto.Artifact{Path: path, Size: int64(len(content)), Content: strings.NewReader(content)}); err != nil {
		f.t.Fatalf("add artifact %s: %v", path, err)
	}
	var a store.Artifact
	if err := f.db.NewSelect().Model(&a).Where("run_id = ? AND path = ?", runID, path).Scan(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	return a
}

// age moves a run's end and its artifacts back by d, as if they were
// that much older.
func (f *fixture) age(runID uuid.UUID, d time.Duration) {
	f.t.Helper()
	var run store.Run
	if err := f.db.NewSelect().Model(&run).Where("id = ?", runID).Scan(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	if !run.FinishedAt.IsZero() {
		if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).Set("finished_at = ?", run.FinishedAt.Add(-d)).
			Where("id = ?", runID).Exec(f.ctx); err != nil {
			f.t.Fatal(err)
		}
	}
	var arts []store.Artifact
	if err := f.db.NewSelect().Model(&arts).Where("run_id = ?", runID).Scan(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	for _, a := range arts {
		if _, err := f.db.NewUpdate().Model((*store.Artifact)(nil)).Set("created_at = ?", a.CreatedAt.Add(-d)).
			Where("id = ?", a.ID).Exec(f.ctx); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *fixture) row(id uuid.UUID) store.Run {
	f.t.Helper()
	var run store.Run
	if err := f.db.NewSelect().Model(&run).Where("id = ?", id).Scan(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	return run
}

func (f *fixture) has(hash string) bool {
	f.t.Helper()
	ok, err := f.blobs.Has(f.ctx, hash)
	if err != nil {
		f.t.Fatal(err)
	}
	return ok
}

func (f *fixture) cleanup(ret *service.Retention) service.CleanupResult {
	f.t.Helper()
	res, err := ret.Cleanup(f.ctx)
	if err != nil {
		f.t.Fatalf("cleanup: %v", err)
	}
	return res
}

func (f *fixture) auditRecords(action string) []store.AuditEvent {
	f.t.Helper()
	var rows []store.AuditEvent
	if err := f.db.NewSelect().Model(&rows).Where("action = ?", action).Order("ts").Scan(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	return rows
}

// Logs and artifacts past their retention period are removed; the run
// records stay and say so. Objects that other rows still use are kept.
func TestRetentionExpiresLogsAndArtifacts(t *testing.T) {
	f := newFixture(t)
	f.runs.WithLogArchive(f.blobs)
	alice := f.user("alice")
	task := f.apply(alice, "digest", allowOverlap())
	runner := f.runner("r1")

	old := f.start(alice, task.Task.ID)
	recent := f.start(alice, task.Task.ID)
	active := f.start(alice, task.Task.ID)
	f.claim(runner, 3)
	shared := f.addArtifact(runner, old.ID, "report.md", "same every day\n")
	unique := f.addArtifact(runner, old.ID, "unique.txt", "only in the old run\n")
	// The same content as the task's main.py.
	copied := f.addArtifact(runner, old.ID, "copy.py", "print('hello')\n")
	f.addArtifact(runner, recent.ID, "report.md", "same every day\n")
	f.finishWithLines(runner, old.ID, 1500)
	f.finishWithLines(runner, recent.ID, 20)
	if err := f.coord.AppendLogs(f.ctx, runner, active.ID, f.logLines(1, 5, runproto.StreamStdout), runproto.Dropped{}); err != nil {
		t.Fatal(err)
	}
	inProgress := f.addArtifact(runner, active.ID, "partial.txt", "in progress\n")
	if n, err := f.runs.ArchiveLogs(f.ctx); err != nil || n != 2 {
		t.Fatalf("archived %d, %v", n, err)
	}
	archive := f.row(old.ID)
	if archive.LogBlob == "" || archive.LogBytes == nil || *archive.LogBytes == 0 || !f.has(archive.LogBlob) {
		t.Fatalf("archived run = blob %q, bytes %v", archive.LogBlob, archive.LogBytes)
	}
	f.age(old.ID, 10*day)
	f.age(active.ID, 10*day) // its artifact; the run has not ended

	ret := service.NewRetention(f.db, f.blobs, service.RetentionPolicy{Logs: 7 * day, Artifacts: 7 * day}, nil)
	res := f.cleanup(ret)
	if res.Logs != 1 || res.Artifacts != 3 || res.Runs != 0 || res.LogBytes != *archive.LogBytes ||
		res.ArtifactBytes != shared.Size+unique.Size+copied.Size || res.Objects != 2 {
		t.Fatalf("cleanup = %+v", res)
	}

	// The old run reads as expired, not as empty.
	got := f.get(alice, old.ID)
	if got.Run.LogsExpiredAt.IsZero() || got.Run.LogLines != 1500 {
		t.Fatalf("old run = expired %v, %d lines", got.Run.LogsExpiredAt, got.Run.LogLines)
	}
	_, err := f.runs.Logs(f.ctx, alice, old.ID, service.LogsInput{Tail: new(10)})
	wantErr(t, err, service.ErrExpired)
	_, err = f.runs.FollowLogs(f.ctx, alice, old.ID, service.FollowInput{})
	wantErr(t, err, service.ErrExpired)
	_, err = f.runs.DownloadLogs(f.ctx, alice, old.ID)
	wantErr(t, err, service.ErrExpired)
	if f.chunkCount(old.ID) != 0 || f.has(archive.LogBlob) {
		t.Fatal("the old run's log is still stored")
	}
	for _, a := range []store.Artifact{shared, unique, copied} {
		_, _, err := f.runs.OpenArtifact(f.ctx, alice, a.ID)
		wantErr(t, err, service.ErrExpired)
	}
	page, err := f.runs.Artifacts(f.ctx, alice, old.ID, nil, "")
	if err != nil || len(page.Artifacts) != 3 {
		t.Fatalf("expired artifacts are no longer listed: %+v, %v", page, err)
	}
	for _, a := range page.Artifacts {
		if a.ExpiredAt.IsZero() {
			t.Fatalf("artifact %s not marked expired", a.Path)
		}
	}
	// Content still used by the recent run and by the task file stays;
	// content only the old run had goes.
	if f.has(unique.BlobKey) || !f.has(shared.BlobKey) || !f.has(copied.BlobKey) {
		t.Fatalf("objects: unique %v, shared %v, task file copy %v", f.has(unique.BlobKey), f.has(shared.BlobKey), f.has(copied.BlobKey))
	}

	// The recent and the active run are untouched.
	if lines, err := f.runs.Logs(f.ctx, alice, recent.ID, service.LogsInput{}); err != nil || len(lines.Lines) != 20 {
		t.Fatalf("recent log = %d lines, %v", len(lines.Lines), err)
	}
	if lines, err := f.runs.Logs(f.ctx, alice, active.ID, service.LogsInput{}); err != nil || len(lines.Lines) != 5 {
		t.Fatalf("active log = %d lines, %v", len(lines.Lines), err)
	}
	if _, rc, err := f.runs.OpenArtifact(f.ctx, alice, inProgress.ID); err != nil {
		t.Fatalf("artifact of the active run: %v", err)
	} else {
		_ = rc.Close()
	}

	// One audit record with the totals; a second pass finds nothing and
	// records nothing.
	records := f.auditRecords("retention.cleanup")
	if len(records) != 1 || records[0].ActorKind != "system" || records[0].ResourceKind != "retention" {
		t.Fatalf("audit = %+v", records)
	}
	if d := records[0].Details; d["logs"] != float64(1) || d["artifacts"] != float64(3) || d["policy"] == nil {
		t.Fatalf("audit details = %v", d)
	}
	if res := f.cleanup(ret); !res.Empty() {
		t.Fatalf("second cleanup = %+v", res)
	}
	if n := len(f.auditRecords("retention.cleanup")); n != 1 {
		t.Fatalf("an empty cleanup was audited: %d records", n)
	}
	// The archiver leaves the expired log alone.
	if n, err := f.runs.ArchiveLogs(f.ctx); err != nil || n != 0 {
		t.Fatalf("archive after expiry = %d, %v", n, err)
	}
}

// Above a size cap the oldest logs and artifacts go first, until the rest
// fits.
func TestRetentionSizeCaps(t *testing.T) {
	f := newFixture(t)
	f.runs.WithLogArchive(f.blobs)
	alice := f.user("alice")
	task := f.apply(alice, "digest", allowOverlap())
	runner := f.runner("r1")

	var runs []store.Run
	var arts []store.Artifact
	for i := range 3 {
		run := f.start(alice, task.Task.ID)
		f.claim(runner, 1)
		arts = append(arts, f.addArtifact(runner, run.ID, "data.txt", strings.Repeat(string(rune('a'+i)), 100)))
		f.finishWithLines(runner, run.ID, 300)
		runs = append(runs, run)
	}
	if n, err := f.runs.ArchiveLogs(f.ctx); err != nil || n != 3 {
		t.Fatalf("archived %d, %v", n, err)
	}
	// Oldest first: the first run ended 3 days ago, the last 1 day ago.
	for i, run := range runs {
		f.age(run.ID, time.Duration(3-i)*day)
	}
	sizes := make([]int64, 3)
	for i, run := range runs {
		sizes[i] = *f.row(run.ID).LogBytes
	}

	// Room for a bit more than one log, and for one and a half artifacts.
	ret := service.NewRetention(f.db, f.blobs, service.RetentionPolicy{
		LogsMaxBytes: sizes[2] + sizes[2]/2, ArtifactsMaxBytes: 150,
	}, nil)
	res := f.cleanup(ret)
	if res.Logs != 2 || res.Artifacts != 2 || res.LogBytes != sizes[0]+sizes[1] || res.ArtifactBytes != 200 {
		t.Fatalf("cleanup = %+v", res)
	}
	for i, run := range runs {
		expired := !f.row(run.ID).LogsExpiredAt.IsZero()
		if expired != (i < 2) {
			t.Errorf("run %d: log expired = %v", i, expired)
		}
		var a store.Artifact
		if err := f.db.NewSelect().Model(&a).Where("id = ?", arts[i].ID).Scan(f.ctx); err != nil {
			t.Fatal(err)
		}
		if a.ExpiredAt.IsZero() != (i == 2) {
			t.Errorf("artifact %d: expired at %v", i, a.ExpiredAt)
		}
	}
	if res := f.cleanup(ret); !res.Empty() {
		t.Fatalf("second cleanup = %+v", res)
	}
}

// Runs past the run retention period are deleted with everything that
// belongs to them, except the latest run of each task; links from newer
// runs and trigger fires are cleared.
func TestRetentionDeletesRuns(t *testing.T) {
	f := newFixture(t)
	f.runs.WithLogArchive(f.blobs)
	alice := f.user("alice")
	task := f.apply(alice, "digest", allowOverlap())
	other := f.apply(alice, "other", allowOverlap())
	runner := f.runner("r1")

	first := f.start(alice, task.Task.ID)
	f.claim(runner, 1)
	firstArt := f.addArtifact(runner, first.ID, "only-first.txt", "first\n")
	f.finishWithLines(runner, first.ID, 10)
	second := f.start(alice, task.Task.ID)
	f.claim(runner, 1)
	f.finishWithLines(runner, second.ID, 10)
	// A test run is never a task's last run, so it goes even though it is
	// the newest.
	testRun, err := f.runs.Start(f.ctx, alice, task.Task.ID, service.StartRunInput{Test: true})
	if err != nil {
		t.Fatal(err)
	}
	f.claim(runner, 1)
	f.finishWithLines(runner, testRun.Run.ID, 3)
	// The only run of another task, as old as the others.
	lone := f.start(alice, other.Task.ID)
	f.claim(runner, 1)
	f.finishWithLines(runner, lone.ID, 3)
	// An old queued run is active and stays whatever its age.
	queued := f.start(alice, other.Task.ID)
	if n, err := f.runs.ArchiveLogs(f.ctx); err != nil || n != 4 {
		t.Fatalf("archived %d, %v", n, err)
	}
	firstLog := f.row(first.ID).LogBlob

	// Links to the first run from the second run and a trigger fire.
	if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).Set("rerun_of = ?", first.ID).Where("id = ?", second.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	trig := store.Trigger{
		ID: store.NewID(), TaskID: task.Task.ID, Name: "daily", Type: store.TriggerTypeCron, Enabled: true,
		Schedule: "0 9 * * *", Timezone: "UTC", CatchUp: "skip", Params: "{}", CreatedBy: alice.UserID,
		CreatedAt: store.Now(), UpdatedAt: store.Now(),
	}
	if _, err := f.db.NewInsert().Model(&trig).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	fire := store.TriggerFire{ID: store.NewID(), TriggerID: trig.ID, ScheduledAt: store.Now(), Outcome: store.FireQueued, RunID: first.ID, CreatedAt: store.Now()}
	if _, err := f.db.NewInsert().Model(&fire).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{first.ID, second.ID, testRun.Run.ID, lone.ID} {
		f.age(id, 40*day)
	}
	if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).Set("queued_at = ?", store.Now().Add(-40*day)).Where("id = ?", queued.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}

	ret := service.NewRetention(f.db, f.blobs, service.RetentionPolicy{Runs: 30 * day}, nil)
	res := f.cleanup(ret)
	if res.Runs != 2 || res.Logs != 2 || res.Artifacts != 1 {
		t.Fatalf("cleanup = %+v", res)
	}
	for _, id := range []uuid.UUID{first.ID, testRun.Run.ID} {
		_, err := f.runs.Get(f.ctx, alice, id)
		wantErr(t, err, service.ErrNotFound)
	}
	for _, id := range []uuid.UUID{second.ID, lone.ID, queued.ID} {
		f.get(alice, id)
	}
	if got := f.row(second.ID); got.RerunOf != uuid.Nil {
		t.Fatalf("the second run still points at the deleted one: %s", got.RerunOf)
	}
	var fireNow store.TriggerFire
	if err := f.db.NewSelect().Model(&fireNow).Where("id = ?", fire.ID).Scan(f.ctx); err != nil || fireNow.RunID != uuid.Nil {
		t.Fatalf("trigger fire = %+v, %v", fireNow, err)
	}
	for _, table := range []any{(*store.RunEvent)(nil), (*store.LogChunk)(nil), (*store.Artifact)(nil)} {
		n, err := f.db.NewSelect().Model(table).Where("run_id = ?", first.ID).Count(f.ctx)
		if err != nil || n != 0 {
			t.Fatalf("%T rows of the deleted run: %d, %v", table, n, err)
		}
	}
	if f.has(firstLog) || f.has(firstArt.BlobKey) {
		t.Fatal("objects of the deleted run are still stored")
	}
	// The task list still shows its last run.
	tasks, err := f.tasks.List(f.ctx, alice, service.TaskListInput{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range tasks.Tasks {
		if s.Task.ID == task.Task.ID && (s.LastRun == nil || s.LastRun.ID != second.ID) {
			t.Fatalf("last run of the task = %+v", s.LastRun)
		}
	}
}

// hookStore runs hook once, right after it stores content with the given
// hash, to interleave the collector with a writer.
type hookStore struct {
	*blob.Local
	mu   sync.Mutex
	hash string
	hook func()
}

func (h *hookStore) Put(ctx context.Context, r io.Reader) (string, error) {
	got, err := h.Local.Put(ctx, r)
	h.mu.Lock()
	hook := h.hook
	if got == h.hash {
		h.hook = nil
	} else {
		hook = nil
	}
	h.mu.Unlock()
	if err == nil && hook != nil {
		hook()
	}
	return got, err
}

// A writer that stored content just before the collector deleted it
// stores it again, so the row it commits never names a missing object.
func TestRetentionWriterRacesCollector(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "digest", allowOverlap())
	runner := f.runner("r1")
	run := f.start(alice, task.Task.ID)
	f.claim(runner, 1)
	content := "print('shared with a new version')\n"
	old := f.addArtifact(runner, run.ID, "copy.py", content)
	f.finishWithLines(runner, run.ID, 1)
	f.age(run.ID, 10*day)

	hooked := &hookStore{Local: f.blobs, hash: old.BlobKey}
	ret := service.NewRetention(f.db, hooked, service.RetentionPolicy{Artifacts: 7 * day}, nil)
	collected := 0
	hooked.hook = func() { collected = f.cleanup(ret).Objects }

	// A new version with the same file: its content is stored, then
	// retention expires the artifact and deletes the object before the
	// version commits.
	tasks := service.NewTasks(f.db, service.NewAuthorizer(f.db), hooked, nil)
	res, err := tasks.Apply(f.ctx, alice, service.ApplyTaskInput{
		ProjectID: alice.PersonalProjectID, Name: "digest", Manifest: allowOverlap(), IfMatch: &task.Task.LatestSeq,
		Files: []service.FileInput{{Path: "main.py", Content: []byte(content)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if collected != 1 {
		t.Fatalf("the collector deleted %d objects during the apply; the test did not interleave", collected)
	}
	if !f.has(old.BlobKey) {
		t.Fatal("the new version names an object the collector deleted")
	}
	if got, err := blob.ReadAll(f.ctx, f.blobs, res.Task.Version.Files[0].BlobHash); err != nil || string(got) != content {
		t.Fatalf("file content = %q, %v", got, err)
	}

	// The same for an artifact: the upload fails once and its retry
	// stores the content again.
	if _, err := f.db.NewUpdate().Model((*store.TaskFile)(nil)).Set("blob_hash = ?", strings.Repeat("0", 64)).
		Where("blob_hash = ?", old.BlobKey).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.NewUpdate().Model((*store.Artifact)(nil)).Set("expired_at = NULL").Set("created_at = ?", store.Now().Add(-10*day)).
		Where("id = ?", old.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	coord := service.NewCoordinator(f.db, hooked, f.runs, nil)
	next := f.start(alice, task.Task.ID)
	f.claim(runner, 1)
	hooked.mu.Lock()
	hooked.hash = old.BlobKey
	hooked.hook = func() { collected = f.cleanup(ret).Objects }
	hooked.mu.Unlock()
	collected = 0
	upload := func() error {
		return coord.AddArtifact(f.ctx, runner, next.ID, runproto.Artifact{Path: "copy.py", Size: int64(len(content)), Content: bytes.NewReader([]byte(content))})
	}
	if err := upload(); err == nil || errors.Is(err, service.ErrProtocol) {
		t.Fatalf("upload during collection = %v, want a retryable error", err)
	}
	if collected != 1 {
		t.Fatalf("collector deleted %d objects", collected)
	}
	if err := upload(); err != nil {
		t.Fatalf("retried upload: %v", err)
	}
	if !f.has(old.BlobKey) {
		t.Fatal("the artifact names an object the collector deleted")
	}
}

// Logs archived before runs.log_blob existed are filled in before any
// object is collected.
func TestRetentionBackfillsLogArchives(t *testing.T) {
	f := newFixture(t)
	f.runs.WithLogArchive(f.blobs)
	alice := f.user("alice")
	task := f.apply(alice, "digest", allowOverlap())
	runner := f.runner("r1")
	run := f.start(alice, task.Task.ID)
	f.claim(runner, 1)
	f.finishWithLines(runner, run.ID, 1200)
	if n, err := f.runs.ArchiveLogs(f.ctx); err != nil || n != 1 {
		t.Fatalf("archived %d, %v", n, err)
	}
	want := f.row(run.ID)
	if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).Set("log_blob = NULL").Set("log_bytes = NULL").
		Where("id = ?", run.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	// A release of the archive's object, as if another row that used it
	// had expired: the archive still uses it, which only the backfill
	// makes visible.
	if _, err := f.db.NewInsert().Model(&store.BlobRelease{ID: store.NewID(), Hash: want.LogBlob, ReleasedAt: store.Now()}).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	ret := service.NewRetention(f.db, f.blobs, service.RetentionPolicy{}, nil)
	if res := f.cleanup(ret); !res.Empty() {
		t.Fatalf("cleanup without a policy = %+v", res)
	}
	if !f.has(want.LogBlob) {
		t.Fatal("the collector deleted an archive the backfill had not named yet")
	}
	if n, err := f.db.NewSelect().Model((*store.BlobRelease)(nil)).Count(f.ctx); err != nil || n != 0 {
		t.Fatalf("releases left = %d, %v", n, err)
	}
	got := f.row(run.ID)
	if got.LogBlob != want.LogBlob || got.LogBytes == nil || *got.LogBytes != *want.LogBytes {
		t.Fatalf("backfilled %q %v, want %q %d", got.LogBlob, got.LogBytes, want.LogBlob, *want.LogBytes)
	}
	var a struct {
		Blob string `json:"blob"`
	}
	if err := json.Unmarshal([]byte(got.LogArchive), &a); err != nil || a.Blob != got.LogBlob {
		t.Fatalf("archive JSON %s", got.LogArchive)
	}
}
