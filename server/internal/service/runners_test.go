package service_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func (f *fixture) runnerService() *service.Runners {
	return service.NewRunners(f.db, f.coord, nil)
}

func TestRunnerLabelsSelectRuns(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	m := basicManifest()
	m.RunnerLabels = []string{"intranet", "intranet"}
	m.Concurrency = service.ConcurrencyAllow
	inner := f.apply(alice, "inner", m)
	plain := f.apply(alice, "plain", basicManifest())
	innerRun := f.start(alice, inner.Task.ID)
	plainRun := f.start(alice, plain.Task.ID)

	if err := f.coord.Register(f.ctx, runproto.Registration{ID: "edge", Slots: 4, Labels: []string{"default"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.coord.Register(f.ctx, runproto.Registration{ID: "dc", Slots: 4, Labels: []string{"intranet", "default"}}); err != nil {
		t.Fatal(err)
	}
	got := f.claim("edge", 4)
	if len(got) != 1 || got[0].RunID != plainRun.ID {
		t.Fatalf("edge runner claimed %+v, want only %s", got, plainRun.ID)
	}
	got = f.claim("dc", 4)
	if len(got) != 1 || got[0].RunID != innerRun.ID {
		t.Fatalf("intranet runner claimed %+v, want only %s", got, innerRun.ID)
	}

	// Registering again replaces the labels.
	if err := f.coord.Register(f.ctx, runproto.Registration{ID: "edge", Slots: 4, Labels: []string{"intranet"}}); err != nil {
		t.Fatal(err)
	}
	again := f.start(alice, inner.Task.ID)
	if got := f.claim("edge", 4); len(got) != 1 || got[0].RunID != again.ID {
		t.Fatalf("relabelled runner claimed %+v, want %s", got, again.ID)
	}
}

func TestManifestRejectsInvalidRunnerLabels(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	m := basicManifest()
	m.RunnerLabels = []string{"ok", "Bad Label", "-x"}
	_, err := f.tasks.Apply(f.ctx, alice, service.ApplyTaskInput{ProjectID: alice.PersonalProjectID, Name: "bad", Manifest: m, Files: mainPy()})
	fields, content := fieldErrors(t, err)
	if !content || fields["#/manifest/runner_labels/1"] == "" || fields["#/manifest/runner_labels/2"] == "" || fields["#/manifest/runner_labels/0"] != "" {
		t.Fatalf("fields = %v", fields)
	}
}

func TestRegisterRejectsInvalidLabels(t *testing.T) {
	f := newFixture(t)
	for _, labels := range [][]string{{"UPPER"}, {""}, {strings.Repeat("a", 64)}, make([]string, runproto.MaxLabels+1)} {
		err := f.coord.Register(f.ctx, runproto.Registration{ID: "r", Slots: 1, Labels: labels})
		wantErr(t, err, service.ErrProtocol)
	}
}

func TestRegistrationTokens(t *testing.T) {
	f := newFixture(t)
	admin := f.user("admin") // the first account is the admin
	bob := f.user("bob")
	runners := f.runnerService()

	if _, _, err := runners.CreateRegistrationToken(f.ctx, bob, "nope", time.Hour); err == nil {
		t.Fatal("a non-admin created a registration token")
	}
	for _, ttl := range []time.Duration{0, time.Minute, 721 * time.Hour} {
		_, _, err := runners.CreateRegistrationToken(f.ctx, admin, "", ttl)
		if fields, _ := fieldErrors(t, err); fields["#/expires_in_hours"] == "" {
			t.Fatalf("ttl %s: fields = %v", ttl, fields)
		}
	}
	token, info, err := runners.CreateRegistrationToken(f.ctx, admin, "rack 1", service.DefaultRunnerTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, service.RunnerRegistrationTokenPrefix) || !strings.HasPrefix(token, info.Prefix) ||
		info.ExpiresAt.Sub(info.CreatedAt) != service.DefaultRunnerTokenTTL {
		t.Fatalf("token %q, info %+v", token[:10], info)
	}
	var stored store.RunnerToken
	if err := f.db.NewSelect().Model(&stored).Where("id = ?", info.ID).Scan(f.ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.TokenHash, token) || stored.TokenHash == token {
		t.Fatal("the token is stored in plain text")
	}

	reg := runproto.Registration{ID: "rack1-a", Name: "a", Slots: 2, Labels: []string{"default"}, Version: "1.2.3"}
	if _, err := runners.Exchange(f.ctx, token+"x", reg, service.ProtocolVersion); err == nil {
		t.Fatal("a wrong registration token was accepted")
	}
	// An invalid registration does not use the token up.
	_, err = runners.Exchange(f.ctx, token, runproto.Registration{ID: "bad id", Slots: 1}, service.ProtocolVersion)
	wantErr(t, err, service.ErrProtocol)
	cred, err := runners.Exchange(f.ctx, token, reg, service.ProtocolVersion)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cred, service.RunnerCredentialPrefix) {
		t.Fatalf("credential %q has no prefix", cred[:8])
	}
	if id, err := runners.Authenticate(f.ctx, cred); err != nil || id != reg.ID {
		t.Fatalf("authenticate = %q, %v", id, err)
	}
	// A registration token is not a credential.
	_, err = runners.Authenticate(f.ctx, token)
	wantErr(t, err, service.ErrUnauthenticated)

	// An admin's token works once.
	_, err = runners.Exchange(f.ctx, token, reg, service.ProtocolVersion)
	wantErr(t, err, service.ErrUnauthenticated)

	// A new token does not take over the ID while the runner is live, and
	// the refused exchange keeps the token.
	token2, _, err := runners.CreateRegistrationToken(f.ctx, admin, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runners.Exchange(f.ctx, token2, reg, service.ProtocolVersion)
	wantErr(t, err, service.ErrRunnerIDInUse)
	if id, err := runners.Authenticate(f.ctx, cred); err != nil || id != reg.ID {
		t.Fatalf("authenticate after a refused takeover = %q, %v", id, err)
	}

	// Once the runner has gone quiet, registering it again replaces its
	// credential: a runner that lost its credential file recovers.
	f.quietRunner(reg.ID)
	cred2, err := runners.Exchange(f.ctx, token2, reg, service.ProtocolVersion)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runners.Authenticate(f.ctx, cred)
	wantErr(t, err, service.ErrUnauthenticated)
	if id, err := runners.Authenticate(f.ctx, cred2); err != nil || id != reg.ID {
		t.Fatalf("authenticate new credential = %q, %v", id, err)
	}

	// Revoked tokens register nothing; issued credentials stay valid.
	token3, info3, err := runners.CreateRegistrationToken(f.ctx, admin, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := runners.RevokeRegistrationToken(f.ctx, bob, info3.ID); err == nil {
		t.Fatal("a non-admin revoked a token")
	}
	if err := runners.RevokeRegistrationToken(f.ctx, admin, info3.ID); err != nil {
		t.Fatal(err)
	}
	_, err = runners.Exchange(f.ctx, token3, runproto.Registration{ID: "rack1-b", Slots: 1}, service.ProtocolVersion)
	wantErr(t, err, service.ErrUnauthenticated)
	if _, err := runners.Authenticate(f.ctx, cred2); err != nil {
		t.Fatalf("credential after revoking a registration token: %v", err)
	}

	// Expired tokens register nothing.
	expiring, expInfo, err := runners.CreateRegistrationToken(f.ctx, admin, "short", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.NewUpdate().Model((*store.RunnerToken)(nil)).Set("expires_at = ?", store.Now().Add(-time.Second)).
		Where("id = ?", expInfo.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	_, err = runners.Exchange(f.ctx, expiring, runproto.Registration{ID: "rack1-c", Slots: 1}, service.ProtocolVersion)
	wantErr(t, err, service.ErrUnauthenticated)

	// Invalid registrations are protocol errors, after the token check.
	static := f.runnerService().WithStaticRegistrationToken("from-the-config-file")
	_, err = static.Exchange(f.ctx, "from-the-config-file", runproto.Registration{ID: "bad id", Slots: 1}, service.ProtocolVersion)
	wantErr(t, err, service.ErrProtocol)
	if _, err := static.Exchange(f.ctx, "from-the-config-file", runproto.Registration{ID: "static-1", Slots: 1}, service.ProtocolVersion); err != nil {
		t.Fatalf("static registration token: %v", err)
	}
	_, err = f.runnerService().Exchange(f.ctx, "from-the-config-file", runproto.Registration{ID: "static-2", Slots: 1}, service.ProtocolVersion)
	wantErr(t, err, service.ErrUnauthenticated)
}

func TestRunnerAdministration(t *testing.T) {
	f := newFixture(t)
	admin := f.user("admin")
	bob := f.user("bob")
	runners := f.runnerService().WithStaticRegistrationToken("static")
	if _, err := runners.Exchange(f.ctx, "static", runproto.Registration{ID: "remote-1", Name: "host-1", Slots: 3, Labels: []string{"b", "a"}}, service.ProtocolVersion); err != nil {
		t.Fatal(err)
	}
	f.runner("embedded")
	allow := basicManifest()
	allow.Concurrency = service.ConcurrencyAllow
	task := f.apply(admin, "t", allow)
	run := f.start(admin, task.Task.ID)
	if got := f.claim("remote-1", 1); len(got) != 1 {
		t.Fatalf("claimed %d", len(got))
	}

	if _, _, err := runners.List(f.ctx, bob, service.ListRunnersInput{}); err == nil {
		t.Fatal("a non-admin listed runners")
	}
	list, next, err := runners.List(f.ctx, admin, service.ListRunnersInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || next != "" {
		t.Fatalf("listed %d runners", len(list))
	}
	byID := map[string]service.RunnerInfo{}
	for _, r := range list {
		byID[r.ID] = r
	}
	remote, embedded := byID["remote-1"], byID["embedded"]
	if !remote.Remote || remote.Name != "host-1" || remote.Slots != 3 || !slices.Equal(remote.Labels, []string{"a", "b"}) ||
		!remote.Online || remote.ActiveRuns != 1 || remote.ProtocolVersion != service.ProtocolVersion || !remote.DisabledAt.IsZero() || remote.Status != service.RunnerOnline || remote.Version != "" {
		t.Fatalf("remote runner = %+v", remote)
	}
	if embedded.Remote || embedded.ActiveRuns != 0 || len(embedded.Labels) != 0 {
		t.Fatalf("embedded runner = %+v", embedded)
	}

	// A disabled runner keeps its run but claims no more.
	if _, err := runners.SetDisabled(f.ctx, bob, "remote-1", true); err == nil {
		t.Fatal("a non-admin disabled a runner")
	}
	info, err := runners.SetDisabled(f.ctx, admin, "remote-1", true)
	if err != nil || info.DisabledAt.IsZero() {
		t.Fatalf("disable = %+v, %v", info, err)
	}
	f.start(admin, task.Task.ID)
	if got := f.claim("remote-1", 4); len(got) != 0 {
		t.Fatalf("a disabled runner claimed %d runs", len(got))
	}
	if reply, err := f.coord.Heartbeat(f.ctx, "remote-1", []uuid.UUID{run.ID}); err != nil || len(reply.Lost) != 0 {
		t.Fatalf("heartbeat of a disabled runner = %+v, %v", reply, err)
	}
	// Status filter and paging.
	if _, err := f.db.NewUpdate().Model((*store.Runner)(nil)).Set("last_seen_at = ?", store.Now().Add(-time.Hour)).
		Where("id = ?", "embedded").Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	for status, want := range map[string]string{service.RunnerDisabled: "remote-1", service.RunnerOffline: "embedded"} {
		got, _, err := runners.List(f.ctx, admin, service.ListRunnersInput{Status: status})
		if err != nil || len(got) != 1 || got[0].ID != want || got[0].Status != status {
			t.Fatalf("status %s: %+v, %v", status, got, err)
		}
	}
	if got, _, err := runners.List(f.ctx, admin, service.ListRunnersInput{Status: service.RunnerOnline}); err != nil || len(got) != 0 {
		t.Fatalf("online runners: %+v, %v", got, err)
	}
	first, next, err := runners.List(f.ctx, admin, service.ListRunnersInput{Limit: new(1)})
	if err != nil || len(first) != 1 || first[0].ID != "embedded" || next == "" {
		t.Fatalf("first page: %+v, %q, %v", first, next, err)
	}
	second, last, err := runners.List(f.ctx, admin, service.ListRunnersInput{Limit: new(1), Cursor: next})
	if err != nil || len(second) != 1 || second[0].ID != "remote-1" || last != "" {
		t.Fatalf("second page: %+v, %q, %v", second, last, err)
	}
	_, _, err = runners.List(f.ctx, admin, service.ListRunnersInput{Status: "lost", Cursor: "%%%"})
	if fields, _ := fieldErrors(t, err); fields["?status"] == "" || fields["?cursor"] == "" {
		t.Fatalf("invalid list input: %v", fields)
	}
	if info, err := runners.SetDisabled(f.ctx, admin, "remote-1", false); err != nil || !info.DisabledAt.IsZero() {
		t.Fatalf("enable = %+v, %v", info, err)
	}
	if got := f.claim("remote-1", 4); len(got) != 1 {
		t.Fatalf("an enabled runner claimed %d runs", len(got))
	}
	_, err = runners.SetDisabled(f.ctx, admin, "missing", true)
	wantErr(t, err, service.ErrNotFound)
}

func TestFilesForChecksTheAssignment(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "t", basicManifest())
	run := f.start(alice, task.Task.ID)
	f.runner("mine")
	f.runner("other")
	version := task.Version.Version.ID

	_, err := f.coord.FilesFor(f.ctx, "mine", version)
	wantErr(t, err, service.ErrNotFound)
	f.claim("mine", 1)
	if files, err := f.coord.FilesFor(f.ctx, "mine", version); err != nil || len(files) != 1 {
		t.Fatalf("files = %+v, %v", files, err)
	}
	_, err = f.coord.FilesFor(f.ctx, "other", version)
	wantErr(t, err, service.ErrNotFound)

	if err := f.coord.Finish(f.ctx, "mine", run.ID, runproto.Finish{Status: runproto.StatusFailed, ErrorCode: "script_error"}); err != nil {
		t.Fatal(err)
	}
	_, err = f.coord.FilesFor(f.ctx, "mine", version)
	wantErr(t, err, service.ErrNotFound)
}

func TestCheckProtocol(t *testing.T) {
	for v, ok := range map[int]bool{0: false, service.ProtocolVersion: true, service.ProtocolVersion + 1: false} {
		if err := service.CheckProtocol(v); (err == nil) != ok {
			t.Errorf("CheckProtocol(%d) = %v", v, err)
		}
	}
}
