package runnerhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/events"
	"github.com/niltavi/lerryn/server/internal/runnerhttp"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

const staticToken = "registration-token-from-the-config-file"

// env is a server with the runner API on a test database.
type env struct {
	t       *testing.T
	ctx     context.Context
	db      *store.DB
	blobs   blob.Store
	srv     *httptest.Server
	runs    *service.Runs
	tasks   *service.Tasks
	coord   *service.Coordinator
	runners *service.Runners
	user    *auth.Principal
	handler *runnerhttp.Handler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := storetest.Open(t)
	blobs, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewLocal()
	authz := service.NewAuthorizer(db)
	runs := service.NewRuns(db, authz, nil).WithEvents(bus, false)
	coord := service.NewCoordinator(db, blobs, runs, nil)
	runners := service.NewRunners(db, coord, nil).WithStaticRegistrationToken(staticToken)
	mux := http.NewServeMux()
	handler := runnerhttp.NewHandler(runnerhttp.Options{Coordinator: coord, Runners: runners, Events: bus})
	mux.Handle(runnerhttp.BasePath, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx := context.Background()
	accounts := service.NewAccounts(db, nil)
	_, sess, err := accounts.Register(ctx, service.RegisterInput{Username: "alice", Password: "correct horse"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := accounts.Authenticate(ctx, sess.Token)
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, ctx: ctx, db: db, blobs: blobs, srv: srv, runs: runs, tasks: service.NewTasks(db, authz, blobs, nil),
		coord: coord, runners: runners, user: p, handler: handler}
}

func (e *env) client(dir, token string) *runnerhttp.Client {
	e.t.Helper()
	c, err := runnerhttp.NewClient(runnerhttp.ClientOptions{
		ServerURL:         e.srv.URL,
		RegistrationToken: token,
		CredentialFile:    filepath.Join(dir, "runner-credential"),
		ClaimWait:         2 * time.Second,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) startRun(labels ...string) uuid.UUID {
	e.t.Helper()
	m := service.Manifest{
		Runtime:      service.RuntimeSpec{Language: "python", Version: "3.12"},
		Entrypoint:   []string{"python", "main.py"},
		Concurrency:  service.ConcurrencyAllow,
		RunnerLabels: labels,
	}
	d, err := e.tasks.Apply(e.ctx, e.user, service.ApplyTaskInput{
		ProjectID: e.user.PersonalProjectID, Name: "t" + strings.Join(labels, "-"), Manifest: m,
		Files: []service.FileInput{{Path: "main.py", Content: []byte("print('hi')\n")}},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	run, err := e.runs.Start(e.ctx, e.user, d.Task.Task.ID, service.StartRunInput{})
	if err != nil {
		e.t.Fatal(err)
	}
	return run.Run.ID
}

func TestProtocolOverHTTP(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	c := e.client(dir, staticToken)
	reg := runproto.Registration{ID: "remote-1", Name: "host", Slots: 2, Labels: []string{"default"}}
	if err := c.Register(e.ctx, reg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "runner-credential"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file mode = %v, want 0600", info.Mode().Perm())
	}

	// Long polling: the claim is held until a run is queued.
	queued := make(chan uuid.UUID, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		queued <- e.startRun()
	}()
	start := time.Now()
	got, err := c.Claim(e.ctx, reg.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	runID := <-queued
	if len(got) != 1 || got[0].RunID != runID {
		t.Fatalf("claimed %+v, want %s", got, runID)
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Fatalf("the held claim returned after %s; the queued event should wake it", took)
	}
	a := got[0]
	if a.Manifest.Timeout != 10*time.Minute || a.Manifest.Runtime.Language != "python" || a.Manifest.Network != "full" || string(a.Params) != "{}" {
		t.Fatalf("assignment = %+v", a)
	}

	// An idle claim returns empty after the wait.
	start = time.Now()
	if got, err := c.Claim(e.ctx, reg.ID, 2); err != nil || len(got) != 0 {
		t.Fatalf("idle claim = %+v, %v", got, err)
	}
	if took := time.Since(start); took < 1500*time.Millisecond {
		t.Fatalf("idle claim returned after %s, before the wait", took)
	}

	files, err := c.Files(e.ctx, a.VersionID)
	if err != nil || len(files) != 1 || string(files[0].Content) != "print('hi')\n" {
		t.Fatalf("files = %+v, %v", files, err)
	}
	if _, err := c.Files(e.ctx, uuid.New()); !errors.Is(err, runproto.ErrLeaseLost) {
		t.Fatalf("files of a version without an assignment: %v, want ErrLeaseLost", err)
	}
	if err := c.Started(e.ctx, reg.ID, runID, "env-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	lines := []runproto.LogLine{{Seq: 1, TS: time.Now(), Stream: runproto.StreamStdout, Text: "hello"}, {Seq: 2, Stream: runproto.StreamStderr, Stage: "s", Text: "world"}}
	if err := c.AppendLogs(e.ctx, reg.ID, runID, lines, runproto.Dropped{Lines: 1, Bytes: 10}); err != nil {
		t.Fatal(err)
	}
	gap := []runproto.LogLine{{Seq: 5, Stream: runproto.StreamStdout, Text: "x"}}
	if err := c.AppendLogs(e.ctx, reg.ID, runID, gap, runproto.Dropped{}); !errors.Is(err, runproto.ErrRejected) {
		t.Fatalf("a gap in log lines: %v, want ErrRejected", err)
	}
	evs := []runproto.Event{
		{Seq: 1, Kind: runproto.EventStageStart, Stage: "s"},
		{Seq: 2, Kind: runproto.EventResult, Result: &runproto.Result{Status: "ok", Summary: "done", Data: json.RawMessage(`{"n":1}`)}},
	}
	if err := c.AppendEvents(e.ctx, reg.ID, runID, evs); err != nil {
		t.Fatal(err)
	}
	reply, err := c.Heartbeat(e.ctx, reg.ID, []uuid.UUID{runID, uuid.New()})
	if err != nil || len(reply.Lost) != 1 || len(reply.Cancel) != 0 {
		t.Fatalf("heartbeat = %+v, %v", reply, err)
	}
	// Artifacts stream as raw bodies; the path may need escaping.
	report := `{"rows": 3}`
	if err := c.AddArtifact(e.ctx, reg.ID, runID, runproto.Artifact{Path: "out/报告 1.json", Size: int64(len(report)), Content: strings.NewReader(report)}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddArtifact(e.ctx, reg.ID, runID, runproto.Artifact{Path: "../x", Size: 1, Content: strings.NewReader("x")}); !errors.Is(err, runproto.ErrRejected) {
		t.Fatalf("bad artifact path: %v, want ErrRejected", err)
	}
	arts, err := e.runs.Artifacts(e.ctx, e.user, runID, nil, "")
	if err != nil || len(arts.Artifacts) != 1 || arts.Artifacts[0].Path != "out/报告 1.json" || arts.Artifacts[0].Size != int64(len(report)) {
		t.Fatalf("artifacts = %+v, %v", arts, err)
	}
	zero := 0
	if err := c.PutState(e.ctx, reg.ID, runID, runproto.StateSnapshot{Version: runproto.StateFormatVersion}); err != nil {
		t.Fatal(err)
	}
	if err := c.Finish(e.ctx, reg.ID, runID, runproto.Finish{Status: runproto.StatusCompleted, ExitCode: &zero, FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := c.AppendLogs(e.ctx, reg.ID, runID, []runproto.LogLine{{Seq: 3, Stream: runproto.StreamStdout}}, runproto.Dropped{}); !errors.Is(err, runproto.ErrLeaseLost) {
		t.Fatalf("logs after finish: %v, want ErrLeaseLost", err)
	}
	d, err := e.runs.Get(e.ctx, e.user, runID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Run.Status != store.RunCompleted || d.Run.LogLines != 2 || d.Run.DroppedLines != 1 || d.Run.EnvID != "env-1" || d.Result == nil || d.Result.Summary != "done" {
		t.Fatalf("run = %+v, result %+v", d.Run, d.Result)
	}

	// A restarted runner uses the stored credential, without the token.
	again := e.client(dir, "")
	if err := again.Register(e.ctx, reg); err != nil {
		t.Fatalf("register with the stored credential: %v", err)
	}
}

// The server takes the runner's identity from its credential: a runner
// cannot act on another runner's run, nor register under another ID.
func TestRunnersCannotImpersonateEachOther(t *testing.T) {
	e := newEnv(t)
	a := e.client(t.TempDir(), staticToken)
	b := e.client(t.TempDir(), staticToken)
	if err := a.Register(e.ctx, runproto.Registration{ID: "a", Slots: 1}); err != nil {
		t.Fatal(err)
	}
	if err := b.Register(e.ctx, runproto.Registration{ID: "b", Slots: 1}); err != nil {
		t.Fatal(err)
	}
	runID := e.startRun()
	got, err := a.Claim(e.ctx, "a", 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("claim = %+v, %v", got, err)
	}
	// b names a as the runner, but the server goes by b's credential.
	if err := b.Started(e.ctx, "a", runID, "", time.Now()); !errors.Is(err, runproto.ErrLeaseLost) {
		t.Fatalf("b started a's run: %v", err)
	}
	if _, err := b.Files(e.ctx, got[0].VersionID); !errors.Is(err, runproto.ErrLeaseLost) {
		t.Fatalf("b read the files of a's run: %v", err)
	}
	if err := b.Register(e.ctx, runproto.Registration{ID: "a", Slots: 1}); !errors.Is(err, runproto.ErrRejected) {
		t.Fatalf("b registered as a with its own credential: %v, want ErrRejected", err)
	}
}

func TestRegistrationFailures(t *testing.T) {
	e := newEnv(t)
	c := e.client(t.TempDir(), "wrong")
	if err := c.Register(e.ctx, runproto.Registration{ID: "x", Slots: 1}); !errors.Is(err, runproto.ErrRejected) {
		t.Fatalf("wrong registration token: %v, want ErrRejected", err)
	}
	good := e.client(t.TempDir(), staticToken)
	if err := good.Register(e.ctx, runproto.Registration{ID: "x", Slots: 1, Labels: []string{"NOT VALID"}}); !errors.Is(err, runproto.ErrRejected) {
		t.Fatalf("invalid label: %v, want ErrRejected", err)
	}
	if _, err := runnerhttp.NewClient(runnerhttp.ClientOptions{ServerURL: e.srv.URL, CredentialFile: filepath.Join(t.TempDir(), "c")}); err == nil {
		t.Fatal("a client without credential or token was created")
	}
}

// quiet makes a runner look as if it stopped heartbeating a while ago,
// which is when the registration token may give its ID a new credential.
func (e *env) quiet(id string) {
	e.t.Helper()
	if _, err := e.db.NewUpdate().Model((*store.Runner)(nil)).
		Set("last_seen_at = ?", store.Now().Add(-2*runproto.LeaseTTL)).
		Where("id = ?", id).Exec(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

// When the server no longer accepts the credential, for example after the
// runner was registered again elsewhere once it had gone quiet, the client
// registers again with its token and repeats the call.
func TestClientRenewsItsCredential(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	c := e.client(dir, staticToken)
	reg := runproto.Registration{ID: "r", Slots: 1}
	if err := c.Register(e.ctx, reg); err != nil {
		t.Fatal(err)
	}
	old, _ := os.ReadFile(filepath.Join(dir, "runner-credential")) // #nosec G304 -- a temp dir of this test

	// A restart with the stored credential keeps it.
	if err := e.client(dir, staticToken).Register(e.ctx, reg); err != nil {
		t.Fatalf("register after a restart: %v", err)
	}
	if again, _ := os.ReadFile(filepath.Join(dir, "runner-credential")); string(again) != string(old) { // #nosec G304 -- a temp dir of this test
		t.Fatal("a restart replaced the stored credential")
	}

	// The token does not take over the ID of a live runner; the server
	// answers with a conflict the runner retries.
	_, err := e.runners.Exchange(e.ctx, staticToken, reg, service.ProtocolVersion)
	if !errors.Is(err, service.ErrRunnerIDInUse) {
		t.Fatalf("exchange for a live runner: %v, want ErrRunnerIDInUse", err)
	}
	thief := e.client(t.TempDir(), staticToken)
	if err := thief.Register(e.ctx, reg); err == nil || errors.Is(err, runproto.ErrRejected) {
		t.Fatalf("register under a live runner's ID: %v, want a transient error", err)
	}

	// Once the runner has gone quiet its ID is registered elsewhere; when
	// that registration goes quiet too, the first client takes it back.
	e.quiet("r")
	if _, err := e.runners.Exchange(e.ctx, staticToken, reg, service.ProtocolVersion); err != nil {
		t.Fatal(err)
	}
	e.quiet("r")
	if _, err := c.Heartbeat(e.ctx, "r", nil); err != nil {
		t.Fatalf("heartbeat after the credential was replaced: %v", err)
	}
	renewed, _ := os.ReadFile(filepath.Join(dir, "runner-credential")) // #nosec G304 -- a temp dir of this test
	if string(renewed) == string(old) {
		t.Fatal("the stored credential was not renewed")
	}

	// Without a registration token the client cannot recover.
	stale := e.client(dir, "")
	e.quiet("r")
	if _, err := e.runners.Exchange(e.ctx, staticToken, reg, service.ProtocolVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := stale.Heartbeat(e.ctx, "r", nil); err == nil || errors.Is(err, runproto.ErrRejected) {
		t.Fatalf("heartbeat with a stale credential: %v, want a transient error", err)
	}
}

func TestServerRejectsBadRequests(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name, protocol, auth string
		want                 int
	}{
		{"no protocol header", "", "", http.StatusUnprocessableEntity},
		{"future protocol", "99", "", http.StatusUnprocessableEntity},
		{"no credential", "1", "", http.StatusUnauthorized},
		{"registration token as credential", "1", "Bearer " + staticToken, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/runner/v1/heartbeat", strings.NewReader(`{"active":[]}`))
			if tc.protocol != "" {
				req.Header.Set(runnerhttp.ProtocolHeader, tc.protocol)
			}
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// Errors that are not protocol errors, such as a proxy's 502, are
// transient; unknown endpoints are not.
func TestClientErrorClasses(t *testing.T) {
	status := http.StatusBadGateway
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runner-credential"), []byte("wl_rnr_x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := runnerhttp.NewClient(runnerhttp.ClientOptions{ServerURL: srv.URL, CredentialFile: filepath.Join(dir, "runner-credential")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Heartbeat(context.Background(), "r", nil); err == nil || errors.Is(err, runproto.ErrRejected) || errors.Is(err, runproto.ErrLeaseLost) {
		t.Fatalf("502: %v, want a transient error", err)
	}
	status = http.StatusNotFound
	if _, err := c.Heartbeat(context.Background(), "r", nil); !errors.Is(err, runproto.ErrRejected) {
		t.Fatalf("404: %v, want ErrRejected", err)
	}
}

// A server that shuts down answers held claims at once.
func TestDrainAnswersHeldClaims(t *testing.T) {
	e := newEnv(t)
	c := e.client(t.TempDir(), staticToken)
	if err := c.Register(e.ctx, runproto.Registration{ID: "r", Slots: 1}); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		e.handler.Drain()
	}()
	start := time.Now()
	got, err := c.Claim(e.ctx, "r", 1)
	if err != nil || len(got) != 0 {
		t.Fatalf("claim = %+v, %v", got, err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("held claim answered %s after starting, not at the drain", took)
	}
}

func TestClientSpeaksTheServerProtocol(t *testing.T) {
	if err := service.CheckProtocol(1); err != nil {
		t.Fatalf("the server does not accept the client's protocol version: %v", err)
	}
}
