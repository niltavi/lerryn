package service_test

import (
	"encoding/base64"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/secrets"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// plaintextFixture has a task "digest" of alice with testSecret bound to
// it as LLM_API_KEY, and every service checking submissions.
type plaintextFixture struct {
	*fixture
	key      []byte
	creds    *service.Credentials
	triggers *service.Triggers
	alice    *auth.Principal
	agent    *auth.Principal
	task     service.TaskDetail
}

func newPlaintextFixture(t *testing.T) *plaintextFixture {
	t.Helper()
	f := newFixture(t)
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	kr, err := secrets.NewLocalKeyring(key)
	if err != nil {
		t.Fatal(err)
	}
	creds := service.NewCredentials(f.db, service.NewAuthorizer(f.db), kr, nil)
	f.coord.UseCredentials(creds)
	f.tasks.WithPlaintextCheck(creds)
	f.runs.WithPlaintextCheck(creds)
	pf := &plaintextFixture{
		fixture:  f,
		key:      key,
		creds:    creds,
		triggers: service.NewTriggers(f.db, service.NewAuthorizer(f.db), f.blobs, nil, service.TriggerOptions{}).WithPlaintextCheck(creds),
		alice:    f.user("alice"),
	}
	pf.agent = f.agentOf(pf.alice)
	pf.task = f.apply(pf.alice, "digest", credManifest(service.CredentialSpec{Name: "LLM_API_KEY"}))
	f.fill(creds, pf.alice, llmRequest(pf.task.Task.ID), map[string]string{"LLM_API_KEY": testSecret})
	return pf
}

// applyAs submits a version of the task named name as p.
func (f *plaintextFixture) applyAs(p *auth.Principal, project uuid.UUID, name string, m service.Manifest, dryRun bool, files ...service.FileInput) error {
	if len(files) == 0 {
		files = mainPy()
	}
	var ifMatch *int
	var current store.Task
	if err := f.db.NewSelect().Model(&current).
		Where("project_id = ? AND name = ? AND archived_at IS NULL", project, name).Scan(f.ctx); err == nil {
		ifMatch = &current.LatestSeq
	}
	_, err := f.tasks.Apply(f.ctx, p, service.ApplyTaskInput{
		ProjectID: project, Name: name, Manifest: m, Files: files, IfMatch: ifMatch, DryRun: dryRun,
	})
	return err
}

// rejected asserts err is a plaintext rejection at exactly the pointers,
// naming exactly the credentials, and that nothing in it reveals a value.
func rejected(t *testing.T, err error, names []string, pointers ...string) {
	t.Helper()
	var perr *service.PlaintextCredentialError
	if !errors.As(err, &perr) {
		t.Fatalf("err = %v, want a plaintext rejection", err)
	}
	var verr *service.ValidationError
	if !errors.As(err, &verr) || !verr.Content {
		t.Fatalf("not a 422 validation error: %v", err)
	}
	if !slices.Equal(perr.Names, names) {
		t.Errorf("names = %v, want %v", perr.Names, names)
	}
	var got []string
	for _, f := range perr.Fields {
		got = append(got, f.Pointer)
	}
	if !slices.Equal(got, pointers) {
		t.Errorf("pointers = %v, want %v", got, pointers)
	}
	if strings.Contains(err.Error(), testSecret) || strings.Contains(err.Error(), "sk-test") {
		t.Errorf("the error carries the value: %v", err)
	}
}

func TestPlaintextInTaskSubmissions(t *testing.T) {
	f := newPlaintextFixture(t)
	project := f.alice.PersonalProjectID
	withParams := func(params map[string]any) service.Manifest {
		m := credManifest(service.CredentialSpec{Name: "LLM_API_KEY"})
		m.Params = params
		return m
	}
	tests := []struct {
		name     string
		manifest service.Manifest
		files    []service.FileInput
		dryRun   bool
		pointers []string
	}{
		{name: "manifest params", manifest: withParams(map[string]any{"api_key": testSecret}), pointers: []string{"#/manifest/params/api_key"}},
		{name: "embedded in a header", manifest: withParams(map[string]any{"headers": []any{"Authorization: Bearer " + testSecret}}),
			pointers: []string{"#/manifest/params/headers/0"}},
		// The pointer stops before a key that is itself the value.
		{name: "as a key", manifest: withParams(map[string]any{"h": map[string]any{testSecret: map[string]any{"x": testSecret}}}),
			pointers: []string{"#/manifest/params/h"}},
		{name: "dry run", manifest: withParams(map[string]any{"k": testSecret}), dryRun: true, pointers: []string{"#/manifest/params/k"}},
		{name: "file", manifest: withParams(nil),
			files:    []service.FileInput{{Path: "main.py", Content: []byte("KEY = '" + testSecret + "'\n")}},
			pointers: []string{"#/files/0/content"}},
		{name: "base64 in a basic header", manifest: withParams(nil),
			files: []service.FileInput{
				{Path: "main.py", Content: []byte("print(1)\n")},
				{Path: "auth.txt", Content: []byte("Basic " + base64.StdEncoding.EncodeToString([]byte("bot:"+testSecret)))},
			},
			pointers: []string{"#/files/1/content"}},
		{name: "url encoded", manifest: withParams(map[string]any{"url": "https://x.test/?key=" + url.QueryEscape(testSecret+"/x")}),
			pointers: []string{"#/manifest/params/url"}},
		{name: "two places", manifest: withParams(map[string]any{"a": testSecret}),
			files:    []service.FileInput{{Path: "main.py", Content: []byte(testSecret)}},
			pointers: []string{"#/manifest/params/a", "#/files/0/content"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := f.applyAs(f.agent, project, "digest", tt.manifest, tt.dryRun, tt.files...)
			rejected(t, err, []string{"LLM_API_KEY"}, tt.pointers...)
		})
	}

	// A new task has no bindings yet; the caller's own credential still
	// counts.
	err := f.applyAs(f.agent, project, "new-task", withParams(map[string]any{"k": testSecret}), false)
	rejected(t, err, []string{"LLM_API_KEY"}, "#/manifest/params/k")

	// Nothing was stored by the rejected submissions.
	var task store.Task
	if err := f.db.NewSelect().Model(&task).Where("id = ?", f.task.Task.ID).Scan(f.ctx); err != nil || task.LatestSeq != 1 {
		t.Fatalf("versions after rejections = %d, %v", task.LatestSeq, err)
	}
	if n, _ := f.db.NewSelect().Model((*store.Task)(nil)).Where("name = ?", "new-task").Count(f.ctx); n != 0 {
		t.Fatal("the rejected new task was created")
	}

	// Similar text, a shorter piece and a reference to the credential are
	// fine.
	ok := withParams(map[string]any{"note": testSecret[:len(testSecret)-1], "ref": "LLM_API_KEY"})
	if err := f.applyAs(f.agent, project, "digest", ok, false); err != nil {
		t.Fatalf("harmless submission rejected: %v", err)
	}

	// Rejections are audited with the credential, never the value.
	var events []store.AuditEvent
	if err := f.db.NewSelect().Model(&events).Where("action = ?", "credential.plaintext_rejected").Scan(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(events) != len(tests)+1 || events[0].Result != "denied" || events[0].ResourceID != f.task.Task.ID.String() {
		t.Fatalf("audit = %d events, first %+v", len(events), events)
	}
	assertNoPlaintext(t, f.fixture)
}

func TestPlaintextInRunParams(t *testing.T) {
	f := newPlaintextFixture(t)
	taskID := f.task.Task.ID

	_, err := f.runs.Start(f.ctx, f.agent, taskID, service.StartRunInput{Params: map[string]any{"auth": "Bearer " + testSecret}})
	rejected(t, err, []string{"LLM_API_KEY"}, "#/params/auth")
	_, err = f.runs.Start(f.ctx, f.agent, taskID, service.StartRunInput{Params: map[string]any{
		"nested": map[string]any{"list": []any{1, base64.URLEncoding.EncodeToString([]byte(testSecret))}},
	}})
	rejected(t, err, []string{"LLM_API_KEY"}, "#/params/nested/list/1")

	// A run with harmless params starts and can be rerun.
	d, err := f.runs.Start(f.ctx, f.agent, taskID, service.StartRunInput{Params: map[string]any{"day": "monday"}})
	if err != nil {
		t.Fatal(err)
	}
	f.finishRun(d.Run.ID, runproto.StatusCompleted, "")
	_, err = f.runs.Rerun(f.ctx, f.agent, d.Run.ID, service.RerunInput{Params: map[string]any{"k": testSecret}})
	rejected(t, err, []string{"LLM_API_KEY"}, "#/params/k")
	again, err := f.runs.Rerun(f.ctx, f.agent, d.Run.ID, service.RerunInput{})
	if err != nil {
		t.Fatalf("rerun with the original params: %v", err)
	}
	f.finishRun(again.Run.ID, runproto.StatusCompleted, "")

	// Params stored before the credential existed are checked on rerun.
	const later = "later-filled-token-7731"
	old, err := f.runs.Start(f.ctx, f.agent, taskID, service.StartRunInput{Params: map[string]any{"t": later}})
	if err != nil {
		t.Fatal(err)
	}
	f.finishRun(old.Run.ID, runproto.StatusCompleted, "")
	other := f.apply(f.alice, "other", basicManifest())
	f.fill(f.creds, f.alice, service.CreateRequestInput{
		TaskID: other.Task.ID, Name: "LATER", Purpose: "x", Fields: []store.CredentialField{{Name: "LATER", Secret: true}},
	}, map[string]string{"LATER": later})
	_, err = f.runs.Rerun(f.ctx, f.agent, old.Run.ID, service.RerunInput{})
	rejected(t, err, []string{"LATER"}, "#/params/t")
}

func TestPlaintextInTriggerParams(t *testing.T) {
	f := newPlaintextFixture(t)
	_, err := f.triggers.Set(f.ctx, f.agent, f.task.Task.ID, service.SetTriggersInput{Triggers: []service.TriggerInput{
		{Name: "daily", Type: store.TriggerTypeCron, Schedule: "0 8 * * *", Timezone: "Asia/Shanghai"},
		{Name: "hook", Type: store.TriggerTypeWebhook, Params: map[string]any{"token": testSecret}},
	}})
	rejected(t, err, []string{"LLM_API_KEY"}, "#/triggers/1/params/token")
	if _, err := f.triggers.Set(f.ctx, f.agent, f.task.Task.ID, service.SetTriggersInput{Triggers: []service.TriggerInput{
		{Name: "hook", Type: store.TriggerTypeWebhook, Params: map[string]any{"token": "LLM_API_KEY"}}, // #nosec G101 -- a reference name
	}}); err != nil {
		t.Fatalf("harmless trigger rejected: %v", err)
	}
}

// Which credentials count: the caller's own, and those bound to the task
// whoever owns them, reported by the reference name only.
func TestPlaintextCandidates(t *testing.T) {
	f := newPlaintextFixture(t)
	bob := f.user("bob")
	f.grant(f.alice.PersonalProjectID, bob, "maintainer")
	bobsTask := f.apply(bob, "bobs-task", basicManifest())
	const bobValue = "bob-private-value-99"
	bobCred := f.fill(f.creds, bob, service.CreateRequestInput{
		TaskID: bobsTask.Task.ID, Name: "BOB_KEY", Purpose: "x",
		Fields: []store.CredentialField{{Name: "BOB_KEY", Secret: true}},
	}, map[string]string{"BOB_KEY": bobValue})
	params := func(v string) service.Manifest {
		m := credManifest(service.CredentialSpec{Name: "LLM_API_KEY"}, service.CredentialSpec{Name: "SHARED", Optional: true})
		m.Params = map[string]any{"v": v}
		return m
	}
	project := f.alice.PersonalProjectID

	// Alice cannot see Bob's credential and it is not bound: her
	// submission is not checked against it, so nothing is learned.
	if err := f.applyAs(f.agent, project, "digest", params(bobValue), false); err != nil {
		t.Fatalf("value of an unrelated credential: %v", err)
	}
	// Bob's own value is rejected for Bob, under his credential's name.
	bobAgent := f.agentOf(bob)
	rejected(t, f.applyAs(bobAgent, project, "digest", params(bobValue), false), []string{"BOB_KEY"}, "#/manifest/params/v")

	// Once Bob binds it to Alice's task, it counts for Alice too, under
	// the reference name, without Bob's name or the credential's.
	if _, err := f.creds.Grant(f.ctx, bobAgent, bobCred.ID, f.task.Task.ID, "SHARED"); err != nil {
		t.Fatal(err)
	}
	err := f.applyAs(f.agent, project, "digest", params(bobValue), false)
	rejected(t, err, []string{"SHARED"}, "#/manifest/params/v")
	if s := err.Error(); strings.Contains(s, "bob") || strings.Contains(s, "BOB_KEY") || strings.Contains(s, bobCred.ID.String()) {
		t.Errorf("rejection reveals the owner or credential: %v", err)
	}

	// Revoked credentials no longer count.
	if _, err := f.creds.Revoke(f.ctx, bob, bobCred.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.applyAs(f.agent, project, "digest", params(bobValue), false); err != nil {
		t.Fatalf("value of a revoked credential: %v", err)
	}

	// Fields marked as not secret are not checked.
	f.fill(f.creds, f.alice, service.CreateRequestInput{
		TaskID: f.task.Task.ID, Name: "CRM", Purpose: "CRM",
		Fields: []store.CredentialField{{Name: "USER", Secret: false}, {Name: "PASSWORD", Secret: true}},
	}, map[string]string{"USER": "crm-robot-account", "PASSWORD": "hunter2-secret"})
	if err := f.applyAs(f.agent, project, "digest", params("crm-robot-account"), false); err != nil {
		t.Fatalf("non-secret field rejected: %v", err)
	}
	rejected(t, f.applyAs(f.agent, project, "digest", params("hunter2-secret"), false), []string{"CRM"}, "#/manifest/params/v")
}

// Versions stored without fingerprints, or under a master key since
// rotated, are fingerprinted on first use and still rejected.
func TestPlaintextFingerprintsAreRecomputed(t *testing.T) {
	f := newPlaintextFixture(t)
	if _, err := f.db.NewUpdate().Model((*store.CredentialVersion)(nil)).Set("fingerprints = NULL").Where("1 = 1").Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	start := func(creds *service.Credentials) error {
		f.runs.WithPlaintextCheck(creds)
		_, err := f.runs.Start(f.ctx, f.agent, f.task.Task.ID, service.StartRunInput{Params: map[string]any{"k": testSecret}})
		return err
	}
	rejected(t, start(f.creds), []string{"LLM_API_KEY"}, "#/params/k")
	var v store.CredentialVersion
	if err := f.db.NewSelect().Model(&v).Limit(1).Scan(f.ctx); err != nil || v.Fingerprints == "" {
		t.Fatalf("fingerprints not stored back: %q, %v", v.Fingerprints, err)
	}
	before := v.Fingerprints

	// After a rotation the old fingerprints are stale; the old key is
	// still in the keyring to decrypt the value.
	newKey, _ := secrets.GenerateKey()
	rotated, err := secrets.NewLocalKeyring(newKey, f.key)
	if err != nil {
		t.Fatal(err)
	}
	creds := service.NewCredentials(f.db, service.NewAuthorizer(f.db), rotated, nil)
	rejected(t, start(creds), []string{"LLM_API_KEY"}, "#/params/k")
	if err := f.db.NewSelect().Model(&v).Limit(1).Scan(f.ctx); err != nil || v.Fingerprints == before {
		t.Fatalf("fingerprints not refreshed after the rotation: %v", err)
	}

	// A version no key can open is skipped rather than failing the call.
	other, _ := secrets.GenerateKey()
	otherRing, err := secrets.NewLocalKeyring(other)
	if err != nil {
		t.Fatal(err)
	}
	stranger := service.NewCredentials(f.db, service.NewAuthorizer(f.db), otherRing, nil)
	if err := start(stranger); err != nil {
		t.Fatalf("unreadable credential failed the check: %v", err)
	}
}
