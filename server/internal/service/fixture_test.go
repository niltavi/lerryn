package service_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

// fixture wires the task, run and coordinator services on a test database
// and a temporary blob store, as the all-in-one server does.
type fixture struct {
	t        *testing.T
	ctx      context.Context
	db       *store.DB
	blobs    *blob.Local
	accounts *service.Accounts
	tasks    *service.Tasks
	runs     *service.Runs
	coord    *service.Coordinator
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := storetest.Open(t)
	blobs, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authz := service.NewAuthorizer(db)
	runs := service.NewRuns(db, authz, nil)
	return &fixture{
		t:        t,
		ctx:      context.Background(),
		db:       db,
		blobs:    blobs,
		accounts: service.NewAccounts(db, nil),
		tasks:    service.NewTasks(db, authz, blobs, nil),
		runs:     runs,
		coord:    service.NewCoordinator(db, blobs, runs, nil),
	}
}

// user registers an account and returns its session principal.
func (f *fixture) user(name string) *auth.Principal {
	f.t.Helper()
	_, sess := register(f.t, f.accounts, name)
	return authenticate(f.t, f.accounts, sess.Token)
}

// grant gives user a role in the project.
func (f *fixture) grant(project uuid.UUID, user *auth.Principal, role string) {
	f.t.Helper()
	now := store.Now()
	if _, err := f.db.NewInsert().Model(&store.RoleBinding{
		ID: store.NewID(), ProjectID: project, UserID: user.UserID, Role: role, CreatedAt: now, UpdatedAt: now,
	}).Exec(f.ctx); err != nil {
		f.t.Fatal(err)
	}
}

func basicManifest() service.Manifest {
	return service.Manifest{
		Runtime:    service.RuntimeSpec{Language: "python", Version: "3.12"},
		Entrypoint: []string{"python", "main.py"},
	}
}

func mainPy() []service.FileInput {
	return []service.FileInput{{Path: "main.py", Content: []byte("print('hello')\n")}}
}

func (f *fixture) apply(p *auth.Principal, name string, m service.Manifest, files ...service.FileInput) service.TaskDetail {
	f.t.Helper()
	if len(files) == 0 {
		files = mainPy()
	}
	// Build on the latest version, as a client that read the task does.
	var ifMatch *int
	var current store.Task
	if err := f.db.NewSelect().Model(&current).
		Where("project_id = ? AND name = ? AND archived_at IS NULL", p.PersonalProjectID, name).Scan(f.ctx); err == nil {
		ifMatch = &current.LatestSeq
	}
	res, err := f.tasks.Apply(f.ctx, p, service.ApplyTaskInput{ProjectID: p.PersonalProjectID, Name: name, Manifest: m, Files: files, IfMatch: ifMatch})
	if err != nil {
		f.t.Fatalf("apply %s: %v", name, err)
	}
	if res.Task == nil || res.DryRun {
		f.t.Fatalf("apply %s: result %+v", name, res)
	}
	return *res.Task
}

func (f *fixture) start(p *auth.Principal, taskID uuid.UUID) store.Run {
	f.t.Helper()
	d, err := f.runs.Start(f.ctx, p, taskID, service.StartRunInput{})
	if err != nil {
		f.t.Fatalf("start run: %v", err)
	}
	return d.Run
}

// runner registers a runner and returns its ID.
func (f *fixture) runner(id string) string {
	f.t.Helper()
	if err := f.coord.Register(f.ctx, runproto.Registration{ID: id, Name: id + ".local", Slots: 4}); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) claim(runner string, n int) []runproto.Assignment {
	f.t.Helper()
	got, err := f.coord.Claim(f.ctx, runner, n)
	if err != nil {
		f.t.Fatalf("claim: %v", err)
	}
	return got
}

func (f *fixture) putEmptyState(runner string, id uuid.UUID) {
	f.t.Helper()
	if err := f.coord.PutState(f.ctx, runner, id, runproto.StateSnapshot{Version: runproto.StateFormatVersion}); err != nil {
		f.t.Fatalf("deliver empty task state: %v", err)
	}
}

func (f *fixture) get(p *auth.Principal, id uuid.UUID) service.RunDetail {
	f.t.Helper()
	d, err := f.runs.Get(f.ctx, p, id)
	if err != nil {
		f.t.Fatalf("get run: %v", err)
	}
	return d
}

func (f *fixture) logLines(seqFrom, n int64, stream string) []runproto.LogLine {
	lines := make([]runproto.LogLine, n)
	for i := range lines {
		seq := seqFrom + int64(i)
		lines[i] = runproto.LogLine{Seq: seq, TS: time.Now(), Stream: stream, Text: "line " + strconv.FormatInt(seq, 10)}
	}
	return lines
}

func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// fieldErrors returns the pointers or parameters of a validation error.
func fieldErrors(t *testing.T, err error) (map[string]string, bool) {
	t.Helper()
	var verr *service.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}
	out := map[string]string{}
	for _, f := range verr.Fields {
		key := f.Pointer
		if key == "" {
			key = "?" + f.Parameter
		}
		out[key] = f.Detail
	}
	return out, verr.Content
}
