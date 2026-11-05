package service_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/secrets"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// The value every test fills in; no stored row, response, audit record or
// error may contain it.
const testSecret = "sk-test-0123456789abcdef"

func (f *fixture) credentials() *service.Credentials {
	f.t.Helper()
	key, err := secrets.GenerateKey()
	if err != nil {
		f.t.Fatal(err)
	}
	kr, err := secrets.NewLocalKeyring(key)
	if err != nil {
		f.t.Fatal(err)
	}
	creds := service.NewCredentials(f.db, service.NewAuthorizer(f.db), kr, nil)
	f.coord.UseCredentials(creds)
	return creds
}

// agentOf returns an access-token principal of the same user, as an agent
// calling the API would have.
func (f *fixture) agentOf(p *auth.Principal) *auth.Principal {
	f.t.Helper()
	token, _, err := f.accounts.CreateToken(f.ctx, p, service.CreateTokenInput{Name: "Codex · 工作站", Scopes: []string{"read", "write", "execute"}})
	if err != nil {
		f.t.Fatal(err)
	}
	return authenticate(f.t, f.accounts, token)
}

func credManifest(specs ...service.CredentialSpec) service.Manifest {
	m := basicManifest()
	m.Credentials = specs
	return m
}

func llmRequest(taskID uuid.UUID) service.CreateRequestInput {
	return service.CreateRequestInput{
		TaskID: taskID, Name: "LLM_API_KEY", Purpose: "调用模型",
		Fields: []store.CredentialField{{Name: "LLM_API_KEY", Secret: true}},
	}
}

// fill creates a request as the user's agent and fills it in as the user.
func (f *fixture) fill(creds *service.Credentials, user *auth.Principal, in service.CreateRequestInput, values map[string]string) store.Credential {
	f.t.Helper()
	d, err := creds.CreateRequest(f.ctx, f.agentOf(user), in)
	if err != nil {
		f.t.Fatalf("create request: %v", err)
	}
	cred, err := creds.Fill(f.ctx, user, d.Request.ID, values)
	if err != nil {
		f.t.Fatalf("fill: %v", err)
	}
	return cred
}

func TestCredentialRequestLifecycle(t *testing.T) {
	f := newFixture(t)
	creds := f.credentials()
	alice := f.user("alice")
	agent := f.agentOf(alice)
	task := f.apply(alice, "digest", credManifest(service.CredentialSpec{Name: "LLM_API_KEY"}))

	d, err := creds.CreateRequest(f.ctx, agent, llmRequest(task.Task.ID))
	if err != nil {
		t.Fatal(err)
	}
	r := d.Request
	if r.Status != store.CredentialRequestPending || r.Client != "Codex · 工作站" || d.Task == nil || d.Task.Name != "digest" ||
		r.ExpiresAt.Sub(r.CreatedAt) != 24*time.Hour {
		t.Fatalf("request = %+v task %+v", r, d.Task)
	}

	// A token can neither submit values nor fill the request.
	if _, err := creds.Fill(f.ctx, agent, r.ID, map[string]string{"LLM_API_KEY": testSecret}); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("fill with a token: %v", err)
	}
	// Missing required field: no value echoed in the error.
	_, err = creds.Fill(f.ctx, alice, r.ID, map[string]string{"LLM_API_KEY": "  "})
	if fields, _ := fieldErrors(t, err); fields["#/LLM_API_KEY"] == "" {
		t.Fatalf("empty value accepted: %v", err)
	}

	cred, err := creds.Fill(f.ctx, alice, r.ID, map[string]string{"LLM_API_KEY": "  " + testSecret + "\n"})
	if err != nil {
		t.Fatal(err)
	}
	if cred.Name != "LLM_API_KEY" || cred.CurrentVersion != 1 || cred.OwnerUserID != alice.UserID {
		t.Fatalf("credential = %+v", cred)
	}
	if _, err := creds.Fill(f.ctx, alice, r.ID, map[string]string{"LLM_API_KEY": "again"}); !errors.Is(err, service.ErrRequestClosed) {
		t.Fatalf("second fill: %v", err)
	}
	got, err := creds.GetRequest(f.ctx, agent, r.ID)
	if err != nil || got.Request.Status != store.CredentialRequestFulfilled || got.Request.CredentialID != cred.ID {
		t.Fatalf("after fill: %+v, %v", got.Request, err)
	}

	// The fill granted the credential to the task under the reference name.
	version, items, err := creds.TaskCredentials(f.ctx, agent, task.Task.ID)
	if err != nil || version != 1 || len(items) != 1 || items[0].Status != service.TaskCredentialBound ||
		items[0].Credential.ID != cred.ID || items[0].Owner.Username != "alice" {
		t.Fatalf("task credentials = %d %+v, %v", version, items, err)
	}

	// Stored encrypted: the value is in no column, and the audit trail
	// names the credential but not the value.
	assertNoPlaintext(t, f)
}

// assertNoPlaintext scans every table that credentials touch.
func assertNoPlaintext(t *testing.T, f *fixture) {
	t.Helper()
	for _, table := range []string{"credentials", "credential_versions", "credential_requests", "credential_grants", "audit_events", "idempotency_records", "runs"} {
		rows, err := f.db.QueryContext(f.ctx, "SELECT * FROM "+table)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for i, v := range vals {
				var b []byte
				switch x := v.(type) {
				case []byte:
					b = x
				case string:
					b = []byte(x)
				}
				if bytes.Contains(b, []byte(testSecret)) {
					t.Errorf("%s.%s holds the plaintext", table, cols[i])
				}
			}
		}
		_ = rows.Close()
	}
}

func TestCredentialRequestValidation(t *testing.T) {
	f := newFixture(t)
	creds := f.credentials()
	alice := f.user("alice")
	task := f.apply(alice, "digest", basicManifest())
	field := []store.CredentialField{{Name: "TOKEN", Secret: true}}
	tests := []struct {
		name string
		in   service.CreateRequestInput
		want string
	}{
		{"neither task nor credential", service.CreateRequestInput{Purpose: "x", Fields: field}, "#/task_id"},
		{"task without name", service.CreateRequestInput{TaskID: task.Task.ID, Purpose: "x", Fields: field}, "#/task_id"},
		{"bad name", service.CreateRequestInput{TaskID: task.Task.ID, Name: "lower", Purpose: "x", Fields: field}, "#/name"},
		{"blank purpose", service.CreateRequestInput{TaskID: task.Task.ID, Name: "A", Purpose: " ", Fields: field}, "#/purpose"},
		{"reserved field", service.CreateRequestInput{TaskID: task.Task.ID, Name: "A", Purpose: "x", Fields: []store.CredentialField{{Name: "PATH"}}}, "#/fields/0/name"},
		{"platform field", service.CreateRequestInput{TaskID: task.Task.ID, Name: "A", Purpose: "x", Fields: []store.CredentialField{{Name: "LERRYN_RUN_ID"}}}, "#/fields/0/name"},
		{"duplicate field", service.CreateRequestInput{TaskID: task.Task.ID, Name: "A", Purpose: "x", Fields: []store.CredentialField{{Name: "T"}, {Name: "T"}}}, "#/fields/1/name"},
		{"all optional", service.CreateRequestInput{TaskID: task.Task.ID, Name: "A", Purpose: "x", Fields: []store.CredentialField{{Name: "T", Optional: true}}}, "#/fields"},
		{"short expiry", service.CreateRequestInput{TaskID: task.Task.ID, Name: "A", Purpose: "x", Fields: field, ExpiresIn: time.Minute}, "#/expires_in_minutes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := creds.CreateRequest(f.ctx, alice, tt.in)
			fields, _ := fieldErrors(t, err)
			if _, ok := fields[tt.want]; !ok {
				t.Fatalf("errors = %v, want %s", fields, tt.want)
			}
		})
	}
}

// Requests and credentials belong to one user: others, the platform
// admin included, cannot read, fill, cancel, grant or revoke them.
func TestCredentialsAreOwnerOnly(t *testing.T) {
	f := newFixture(t)
	creds := f.credentials()
	admin := f.user("admin") // the first account is the platform admin
	alice := f.user("alice")
	bob := f.user("bob")
	task := f.apply(alice, "digest", credManifest(service.CredentialSpec{Name: "LLM_API_KEY"}))
	f.grant(alice.PersonalProjectID, bob, service.RoleMaintainer)

	d, err := creds.CreateRequest(f.ctx, f.agentOf(alice), llmRequest(task.Task.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []*auth.Principal{admin, bob} {
		if _, err := creds.GetRequest(f.ctx, other, d.Request.ID); !errors.Is(err, service.ErrNotFound) {
			t.Errorf("%s reads the request: %v", other.Username, err)
		}
		if _, err := creds.Fill(f.ctx, other, d.Request.ID, map[string]string{"LLM_API_KEY": testSecret}); !errors.Is(err, service.ErrNotFound) {
			t.Errorf("%s fills the request: %v", other.Username, err)
		}
		if _, err := creds.CancelRequest(f.ctx, other, d.Request.ID); !errors.Is(err, service.ErrNotFound) {
			t.Errorf("%s cancels the request: %v", other.Username, err)
		}
	}
	cred, err := creds.Fill(f.ctx, alice, d.Request.ID, map[string]string{"LLM_API_KEY": testSecret})
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []*auth.Principal{admin, bob} {
		if _, err := creds.Get(f.ctx, other, cred.ID); !errors.Is(err, service.ErrNotFound) {
			t.Errorf("%s reads the credential: %v", other.Username, err)
		}
		if _, err := creds.Revoke(f.ctx, other, cred.ID); !errors.Is(err, service.ErrNotFound) {
			t.Errorf("%s revokes the credential: %v", other.Username, err)
		}
		if _, err := creds.Grant(f.ctx, other, cred.ID, task.Task.ID, "OTHER"); !errors.Is(err, service.ErrNotFound) {
			t.Errorf("%s grants the credential: %v", other.Username, err)
		}
		if err := creds.RevokeGrant(f.ctx, other, cred.ID, task.Task.ID); !errors.Is(err, service.ErrNotFound) {
			t.Errorf("%s withdraws the grant: %v", other.Username, err)
		}
		if list, _, err := creds.List(f.ctx, other, "", nil, ""); err != nil || len(list) != 0 {
			t.Errorf("%s lists %d credentials, %v", other.Username, len(list), err)
		}
	}
	// A request for a task the caller cannot see is not_found too.
	if _, err := creds.CreateRequest(f.ctx, admin, llmRequest(task.Task.ID)); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("request for an invisible task: %v", err)
	}
}

func TestGrantRules(t *testing.T) {
	f := newFixture(t)
	creds := f.credentials()
	alice := f.user("alice")
	bob := f.user("bob")
	carol := f.user("carol")
	task := f.apply(alice, "digest", credManifest(service.CredentialSpec{Name: "LLM_API_KEY"}))
	f.grant(alice.PersonalProjectID, bob, service.RoleViewer)

	aliceCred := f.fill(creds, alice, llmRequest(task.Task.ID), map[string]string{"LLM_API_KEY": testSecret})
	// Bob fills his own credential for his own task, then tries to point
	// alice's task at it.
	bobTask := f.apply(bob, "bob-task", basicManifest())
	bobCred := f.fill(creds, bob, llmRequest(bobTask.Task.ID), map[string]string{"LLM_API_KEY": "sk-bob-value"})

	// A viewer cannot take over a name bound to someone else's credential.
	if _, err := creds.Grant(f.ctx, bob, bobCred.ID, task.Task.ID, "LLM_API_KEY"); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("viewer replaces another user's grant: %v", err)
	}
	// But may lend it under a free name.
	if _, err := creds.Grant(f.ctx, bob, bobCred.ID, task.Task.ID, "SPARE_KEY"); err != nil {
		t.Fatalf("viewer lends a credential: %v", err)
	}
	// Someone who cannot view the task cannot grant to it.
	carolTask := f.apply(carol, "carol-task", basicManifest())
	carolCred := f.fill(creds, carol, llmRequest(carolTask.Task.ID), map[string]string{"LLM_API_KEY": "sk-carol-value"})
	if _, err := creds.Grant(f.ctx, carol, carolCred.ID, task.Task.ID, "LLM_API_KEY"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("grant to an invisible task: %v", err)
	}
	// After promotion to maintainer the takeover works, and replaces the
	// binding.
	if _, err := f.db.NewUpdate().Model((*store.RoleBinding)(nil)).Set("role = ?", service.RoleMaintainer).
		Where("user_id = ?", bob.UserID).Where("project_id = ?", alice.PersonalProjectID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Grant(f.ctx, bob, bobCred.ID, task.Task.ID, "LLM_API_KEY"); err != nil {
		t.Fatalf("maintainer replaces the grant: %v", err)
	}
	_, items, _ := creds.TaskCredentials(f.ctx, alice, task.Task.ID)
	if items[0].Credential.ID != bobCred.ID {
		t.Fatalf("binding = %v, want bob's credential", items[0].Credential.ID)
	}
	// A credential has one grant per task: granting it under a new name
	// moves the grant.
	d, _ := creds.Get(f.ctx, bob, bobCred.ID)
	names := map[string]bool{}
	for _, g := range d.Grants {
		if g.Task.ID == task.Task.ID {
			names[g.Grant.Name] = true
		}
	}
	if !names["LLM_API_KEY"] || names["SPARE_KEY"] {
		t.Fatalf("grants of bob's credential to the task = %v", names)
	}
	// Revoked credentials cannot be granted.
	if _, err := creds.Revoke(f.ctx, alice, aliceCred.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Grant(f.ctx, alice, aliceCred.ID, task.Task.ID, "LLM_API_KEY"); err == nil {
		t.Fatal("revoked credential granted")
	}
	// The owner can withdraw a grant.
	if err := creds.RevokeGrant(f.ctx, bob, bobCred.ID, task.Task.ID); err != nil {
		t.Fatal(err)
	}
	if _, items, _ := creds.TaskCredentials(f.ctx, alice, task.Task.ID); items[0].Status != service.TaskCredentialMissing {
		t.Fatalf("after withdrawal: %+v", items[0])
	}
}

func TestUpdateCredentialAddsVersion(t *testing.T) {
	f := newFixture(t)
	creds := f.credentials()
	alice := f.user("alice")
	task := f.apply(alice, "digest", credManifest(service.CredentialSpec{Name: "LLM_API_KEY"}))
	cred := f.fill(creds, alice, llmRequest(task.Task.ID), map[string]string{"LLM_API_KEY": "sk-version-one"})
	updated := f.fill(creds, alice, service.CreateRequestInput{
		CredentialID: cred.ID, Purpose: "轮换",
		Fields: []store.CredentialField{{Name: "LLM_API_KEY", Secret: true}},
	}, map[string]string{"LLM_API_KEY": testSecret})
	if updated.ID != cred.ID || updated.CurrentVersion != 2 {
		t.Fatalf("updated = %+v", updated)
	}
	run := f.start(alice, task.Task.ID)
	f.runner("r1")
	f.claim("r1", 1)
	got, err := f.coord.Secrets(f.ctx, "r1", run.ID)
	if err != nil || len(got) != 1 || got[0].Fields[0].Value != testSecret {
		t.Fatalf("secrets after update = %+v, %v", got, err)
	}
	// A second fill request for the same name makes a separate credential
	// rather than overwriting the first for other tasks.
	other := f.apply(alice, "other", credManifest(service.CredentialSpec{Name: "LLM_API_KEY"}))
	second := f.fill(creds, alice, llmRequest(other.Task.ID), map[string]string{"LLM_API_KEY": "sk-other-task"})
	if second.ID == cred.ID || second.Name != "LLM_API_KEY_2" {
		t.Fatalf("second credential = %+v", second)
	}
}

func TestRunSecrets(t *testing.T) {
	f := newFixture(t)
	creds := f.credentials()
	alice := f.user("alice")
	m := credManifest(
		service.CredentialSpec{Name: "LLM_API_KEY"},
		service.CredentialSpec{Name: "CRM", As: "file"},
		service.CredentialSpec{Name: "EXTRA", Optional: true},
	)
	task := f.apply(alice, "digest", m)

	// Starting without the required credentials is refused up front.
	_, err := f.runs.Start(f.ctx, alice, task.Task.ID, service.StartRunInput{})
	var ce *service.CredentialError
	if !errors.As(err, &ce) || ce.Code != "credential_missing" || ce.Name != "LLM_API_KEY" {
		t.Fatalf("start without credentials: %v", err)
	}

	f.fill(creds, alice, llmRequest(task.Task.ID), map[string]string{"LLM_API_KEY": testSecret})
	crm := f.fill(creds, alice, service.CreateRequestInput{
		TaskID: task.Task.ID, Name: "CRM", Purpose: "CRM",
		Fields: []store.CredentialField{{Name: "USER", Secret: false}, {Name: "PASSWORD", Secret: true}, {Name: "NOTE", Optional: true}},
	}, map[string]string{"USER": "bot", "PASSWORD": "hunter2-secret"})

	run := f.start(alice, task.Task.ID)
	f.runner("r1")
	f.runner("r2")
	// Not yet claimed: nobody gets anything.
	if _, err := f.coord.Secrets(f.ctx, "r1", run.ID); !errors.Is(err, runproto.ErrLeaseLost) {
		t.Fatalf("secrets of a queued run: %v", err)
	}
	f.claim("r1", 1)
	if _, err := f.coord.Secrets(f.ctx, "r2", run.ID); !errors.Is(err, runproto.ErrLeaseLost) {
		t.Fatalf("another runner fetches the secrets: %v", err)
	}
	got, err := f.coord.Secrets(f.ctx, "r1", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []runproto.Secret{
		{Name: "LLM_API_KEY", As: "env", Fields: []runproto.SecretField{{Name: "LLM_API_KEY", Value: testSecret, Sensitive: true}}},
		{Name: "CRM", As: "file", Fields: []runproto.SecretField{{Name: "USER", Value: "bot"}, {Name: "PASSWORD", Value: "hunter2-secret", Sensitive: true}}},
	}
	if g, w := mustJSON(t, got), mustJSON(t, want); g != w {
		t.Fatalf("secrets = %s\nwant %s", g, w)
	}
	d, _ := creds.Get(f.ctx, alice, crm.ID)
	if d.Credential.LastUsedAt.IsZero() {
		t.Error("last_used_at not recorded")
	}
	var uses int64
	uses, err = f.db.NewSelect().Model((*store.AuditEvent)(nil)).
		Where("action = ? AND actor_kind = ? AND result = ?", "credential.use", "runner", "success").Count(f.ctx)
	if err != nil || uses != 2 {
		t.Fatalf("credential.use audit records = %d, %v", uses, err)
	}

	// Once running, the values are no longer handed out.
	if err := f.coord.Started(f.ctx, "r1", run.ID, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coord.Secrets(f.ctx, "r1", run.ID); !errors.Is(err, runproto.ErrLeaseLost) {
		t.Fatalf("secrets of a running run: %v", err)
	}
	assertNoPlaintext(t, f)
}

// A runner whose lease on a preparing run has expired no longer gets the
// run's values, even before the reaper hands the run back.
func TestRunSecretsExpiredLease(t *testing.T) {
	f := newFixture(t)
	creds := f.credentials()
	alice := f.user("alice")
	task := f.apply(alice, "digest", credManifest(service.CredentialSpec{Name: "LLM_API_KEY"}))
	f.fill(creds, alice, llmRequest(task.Task.ID), map[string]string{"LLM_API_KEY": testSecret})
	run := f.start(alice, task.Task.ID)
	f.runner("r1")
	f.claim("r1", 1)
	if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).Set("lease_expires_at = ?", store.Now().Add(-time.Second)).
		Where("id = ?", run.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coord.Secrets(f.ctx, "r1", run.ID); !errors.Is(err, runproto.ErrLeaseLost) {
		t.Fatalf("secrets with an expired lease: %v, want ErrLeaseLost", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Revoking a credential, or disabling its owner, makes the next run fail
// in preparation with credential_unavailable (PRD A07).
func TestRevokedCredentialFailsPreparation(t *testing.T) {
	f := newFixture(t)
	creds := f.credentials()
	alice := f.user("alice")
	task := f.apply(alice, "digest", credManifest(service.CredentialSpec{Name: "LLM_API_KEY"}))
	cred := f.fill(creds, alice, llmRequest(task.Task.ID), map[string]string{"LLM_API_KEY": testSecret})
	f.runner("r1")

	queued := f.start(alice, task.Task.ID)
	if _, err := creds.Revoke(f.ctx, alice, cred.ID); err != nil {
		t.Fatal(err)
	}
	f.claim("r1", 1)
	_, err := f.coord.Secrets(f.ctx, "r1", queued.ID)
	var ce *runproto.CredentialError
	if !errors.As(err, &ce) || ce.Code != runproto.CodeCredentialUnavailable || strings.Contains(ce.Message, testSecret) {
		t.Fatalf("secrets after revoke: %v", err)
	}
	// New runs are refused at start.
	_, err = f.runs.Start(f.ctx, alice, task.Task.ID, service.StartRunInput{})
	var se *service.CredentialError
	if !errors.As(err, &se) || se.Code != "credential_unavailable" {
		t.Fatalf("start after revoke: %v", err)
	}
	if _, items, _ := creds.TaskCredentials(f.ctx, alice, task.Task.ID); items[0].Status != store.CredentialRevoked {
		t.Fatalf("task credential status = %q", items[0].Status)
	}
	failures, _ := f.db.NewSelect().Model((*store.AuditEvent)(nil)).
		Where("action = ? AND result = ?", "credential.use", "failure").Count(f.ctx)
	if failures != 1 {
		t.Fatalf("failed use audit records = %d", failures)
	}
}

func TestDisabledOwnerMakesCredentialUnavailable(t *testing.T) {
	f := newFixture(t)
	creds := f.credentials()
	alice := f.user("alice")
	task := f.apply(alice, "digest", credManifest(service.CredentialSpec{Name: "LLM_API_KEY"}))
	f.fill(creds, alice, llmRequest(task.Task.ID), map[string]string{"LLM_API_KEY": testSecret})
	run := f.start(alice, task.Task.ID)
	if _, err := f.db.NewUpdate().Model((*store.User)(nil)).Set("disabled_at = ?", store.Now()).Where("id = ?", alice.UserID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.runner("r1")
	f.claim("r1", 1)
	var ce *runproto.CredentialError
	if _, err := f.coord.Secrets(f.ctx, "r1", run.ID); !errors.As(err, &ce) || ce.Code != runproto.CodeCredentialUnavailable {
		t.Fatalf("secrets of a disabled owner: %v", err)
	}
}

// A database restored without its master key cannot decrypt: the run
// fails with credential_unavailable instead of running without values.
func TestWrongMasterKeyFailsPreparation(t *testing.T) {
	f := newFixture(t)
	creds := f.credentials()
	alice := f.user("alice")
	task := f.apply(alice, "digest", credManifest(service.CredentialSpec{Name: "LLM_API_KEY"}))
	f.fill(creds, alice, llmRequest(task.Task.ID), map[string]string{"LLM_API_KEY": testSecret})
	run := f.start(alice, task.Task.ID)

	f.credentials() // a fresh key replaces the coordinator's credentials
	f.runner("r1")
	f.claim("r1", 1)
	var ce *runproto.CredentialError
	if _, err := f.coord.Secrets(f.ctx, "r1", run.ID); !errors.As(err, &ce) || ce.Code != runproto.CodeCredentialUnavailable {
		t.Fatalf("secrets under another master key: %v", err)
	}
}

func TestRewrapAll(t *testing.T) {
	f := newFixture(t)
	oldKey, _ := secrets.GenerateKey()
	newKey, _ := secrets.GenerateKey()
	before, _ := secrets.NewLocalKeyring(oldKey)
	authz := service.NewAuthorizer(f.db)
	creds := service.NewCredentials(f.db, authz, before, nil)
	f.coord.UseCredentials(creds)
	alice := f.user("alice")
	task := f.apply(alice, "digest", credManifest(service.CredentialSpec{Name: "LLM_API_KEY"}))
	f.fill(creds, alice, llmRequest(task.Task.ID), map[string]string{"LLM_API_KEY": testSecret})

	rotated, _ := secrets.NewLocalKeyring(newKey, oldKey)
	n, err := service.NewCredentials(f.db, authz, rotated, nil).RewrapAll(f.ctx)
	if err != nil || n != 1 {
		t.Fatalf("rewrap = %d, %v", n, err)
	}
	if n, err := service.NewCredentials(f.db, authz, rotated, nil).RewrapAll(f.ctx); err != nil || n != 0 {
		t.Fatalf("second rewrap = %d, %v", n, err)
	}
	onlyNew, _ := secrets.NewLocalKeyring(newKey)
	f.coord.UseCredentials(service.NewCredentials(f.db, authz, onlyNew, nil))
	run := f.start(alice, task.Task.ID)
	f.runner("r1")
	f.claim("r1", 1)
	got, err := f.coord.Secrets(f.ctx, "r1", run.ID)
	if err != nil || got[0].Fields[0].Value != testSecret {
		t.Fatalf("secrets after rotation = %+v, %v", got, err)
	}
}

func TestExpiredAndCancelledRequests(t *testing.T) {
	f := newFixture(t)
	creds := f.credentials()
	alice := f.user("alice")
	task := f.apply(alice, "digest", basicManifest())
	expired, _ := creds.CreateRequest(f.ctx, alice, llmRequest(task.Task.ID))
	if _, err := f.db.NewUpdate().Model((*store.CredentialRequest)(nil)).
		Set("expires_at = ?", store.Now().Add(-time.Minute)).Where("id = ?", expired.Request.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Fill(f.ctx, alice, expired.Request.ID, map[string]string{"LLM_API_KEY": testSecret}); !errors.Is(err, service.ErrRequestClosed) {
		t.Fatalf("fill an expired request: %v", err)
	}
	if d, _ := creds.GetRequest(f.ctx, alice, expired.Request.ID); d.Request.Status != store.CredentialRequestExpired {
		t.Fatalf("status = %q", d.Request.Status)
	}
	cancelled, _ := creds.CreateRequest(f.ctx, alice, llmRequest(task.Task.ID))
	d, err := creds.CancelRequest(f.ctx, alice, cancelled.Request.ID)
	if err != nil || d.Request.Status != store.CredentialRequestCancelled {
		t.Fatalf("cancel = %q, %v", d.Request.Status, err)
	}
	if _, err := creds.Fill(f.ctx, alice, cancelled.Request.ID, map[string]string{"LLM_API_KEY": testSecret}); !errors.Is(err, service.ErrRequestClosed) {
		t.Fatalf("fill a cancelled request: %v", err)
	}
	for status, want := range map[string]int{"": 0, store.CredentialRequestExpired: 1, store.CredentialRequestCancelled: 1} {
		list, _, err := creds.ListRequests(f.ctx, alice, status, nil, "")
		if err != nil || len(list) != want {
			t.Errorf("list %q = %d, %v; want %d", status, len(list), err, want)
		}
	}
}

func TestManifestCredentialValidation(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	m := credManifest(service.CredentialSpec{Name: "A"}, service.CredentialSpec{Name: "A"}, service.CredentialSpec{Name: "b", As: "stdin"})
	_, err := f.tasks.Apply(f.ctx, alice, service.ApplyTaskInput{ProjectID: alice.PersonalProjectID, Name: "bad", Manifest: m, Files: mainPy()})
	fields, content := fieldErrors(t, err)
	for _, want := range []string{"#/manifest/credentials/1/name", "#/manifest/credentials/2/name", "#/manifest/credentials/2/as"} {
		if _, ok := fields[want]; !ok || !content {
			t.Errorf("missing %s in %v", want, fields)
		}
	}
}
