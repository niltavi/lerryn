package service_test

import (
	"strings"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestAdminOperationsNeedAnAdmin(t *testing.T) {
	e := newAccountsEnv(t)
	e.user("admin")
	bob, _ := e.user("bob")
	audit := service.NewAudit(e.db, nil)
	checks := map[string]error{}
	_, _, checks["list"] = e.admin.ListUsers(e.ctx, bob, service.UserFilter{})
	_, checks["create"] = e.admin.CreateUser(e.ctx, bob, service.AdminCreateUserInput{
		RegisterInput: service.RegisterInput{Username: "mallory", Password: "correct horse"}, IsAdmin: true,
	})
	_, checks["update"] = e.admin.UpdateUser(e.ctx, bob, bob.UserID, service.AdminUpdateUserInput{IsAdmin: new(true)})
	checks["reset"] = e.admin.ResetPassword(e.ctx, bob, bob.UserID, "new password 1")
	_, checks["settings"] = e.admin.Settings(e.ctx, bob)
	_, checks["update settings"] = e.admin.UpdateSettings(e.ctx, bob, service.SettingsInput{OpenRegistration: new(false)})
	_, _, checks["audit"] = audit.List(e.ctx, bob, service.AuditFilter{})
	for name, err := range checks {
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: err = %v, want ErrPermissionDenied", name, err)
		}
	}
}

func TestDisablingAUserCutsOffEveryCredential(t *testing.T) {
	e := newAccountsEnv(t)
	admin, _ := e.user("admin")
	bob, session := e.user("bob")
	token := e.token(bob, "read", "write")
	_, device, err := e.accounts.LoginDevice(e.ctx, "bob", "correct horse", &iphone)
	if err != nil {
		t.Fatal(err)
	}
	pending := pairingCode(t, e, bob)

	u, err := e.admin.UpdateUser(e.ctx, admin, bob.UserID, service.AdminUpdateUserInput{Disabled: new(true)})
	if err != nil {
		t.Fatal(err)
	}
	if u.User.DisabledAt.IsZero() {
		t.Fatal("disabled_at not set")
	}
	for _, tok := range []string{session, token, device.Token} {
		e.wantAuth(tok, service.ErrUnauthenticated)
	}
	_, _, err = e.accounts.Login(e.ctx, "bob", "correct horse")
	wantErr(t, err, service.ErrUnauthenticated)
	_, _, err = e.accounts.ExchangePairingCode(e.ctx, publicURL, pending.Code, iphone)
	wantErr(t, err, service.ErrUnauthenticated)

	// Restoring the account does not bring old credentials back.
	u, err = e.admin.UpdateUser(e.ctx, admin, bob.UserID, service.AdminUpdateUserInput{Disabled: new(false)})
	if err != nil || !u.User.DisabledAt.IsZero() {
		t.Fatalf("restore: %+v, %v", u.User, err)
	}
	e.wantAuth(token, service.ErrUnauthenticated)
	e.wantAuth(session, service.ErrUnauthenticated)
	if _, _, err := e.accounts.Login(e.ctx, "bob", "correct horse"); err != nil {
		t.Fatalf("login after restore: %v", err)
	}
}

func TestAdminUserManagement(t *testing.T) {
	e := newAccountsEnv(t)
	admin, _ := e.user("admin")
	created, err := e.admin.CreateUser(e.ctx, admin, service.AdminCreateUserInput{
		RegisterInput: service.RegisterInput{Username: "Carol", Password: "correct horse", Email: "carol@example.com"},
	})
	if err != nil || created.User.IsAdmin || created.SSO {
		t.Fatalf("create: %+v, %v", created, err)
	}
	_, err = e.admin.CreateUser(e.ctx, admin, service.AdminCreateUserInput{
		RegisterInput: service.RegisterInput{Username: "carol", Password: "correct horse"},
	})
	wantErr(t, err, service.ErrUsernameTaken)
	e.user("dave")

	// Admins cannot lock themselves out.
	_, err = e.admin.UpdateUser(e.ctx, admin, admin.UserID, service.AdminUpdateUserInput{Disabled: new(true)})
	wantValidation(t, err, "#/disabled")
	_, err = e.admin.UpdateUser(e.ctx, admin, admin.UserID, service.AdminUpdateUserInput{IsAdmin: new(false)})
	wantValidation(t, err, "#/is_admin")

	u, err := e.admin.UpdateUser(e.ctx, admin, created.User.ID, service.AdminUpdateUserInput{IsAdmin: new(true), DisplayName: new("卡罗尔")})
	if err != nil || !u.User.IsAdmin || u.User.DisplayName != "卡罗尔" {
		t.Fatalf("update: %+v, %v", u.User, err)
	}
	_, err = e.admin.UpdateUser(e.ctx, admin, store.NewID(), service.AdminUpdateUserInput{IsAdmin: new(true)})
	wantErr(t, err, service.ErrNotFound)

	users, next, err := e.admin.ListUsers(e.ctx, admin, service.UserFilter{Limit: new(2)})
	if err != nil || len(users) != 2 || next == "" || users[0].User.Username != "admin" || users[1].User.Username != "Carol" {
		t.Fatalf("page 1 = %v, %q, %v", users, next, err)
	}
	users, next, err = e.admin.ListUsers(e.ctx, admin, service.UserFilter{Limit: new(2), Cursor: next})
	if err != nil || len(users) != 1 || next != "" || users[0].User.Username != "dave" {
		t.Fatalf("page 2 = %v, %q, %v", users, next, err)
	}
	users, _, _ = e.admin.ListUsers(e.ctx, admin, service.UserFilter{Query: "EXAMPLE.com"})
	if len(users) != 1 || users[0].User.Username != "Carol" {
		t.Fatalf("query by email = %v", users)
	}
	users, _, _ = e.admin.ListUsers(e.ctx, admin, service.UserFilter{Admin: new(true)})
	if len(users) != 2 {
		t.Fatalf("admins = %v", users)
	}
	// LIKE wildcards in the query are taken literally.
	if users, _, _ = e.admin.ListUsers(e.ctx, admin, service.UserFilter{Query: "%"}); len(users) != 0 {
		t.Fatalf("query %% matched %v", users)
	}
}

func TestAdminResetPassword(t *testing.T) {
	e := newAccountsEnv(t)
	admin, _ := e.user("admin")
	bob, session := e.user("bob")
	token := e.token(bob, "read")
	if err := e.admin.ResetPassword(e.ctx, admin, bob.UserID, "brand new pass"); err != nil {
		t.Fatal(err)
	}
	e.wantAuth(session, service.ErrUnauthenticated)
	e.wantAuth(token, nil)
	if _, _, err := e.accounts.Login(e.ctx, "bob", "brand new pass"); err != nil {
		t.Fatal(err)
	}
	wantErr(t, e.admin.ResetPassword(e.ctx, admin, store.NewID(), "brand new pass"), service.ErrNotFound)
	wantValidation(t, e.admin.ResetPassword(e.ctx, admin, bob.UserID, "short"), "#/password")
}

func TestAuditRecordsCallContext(t *testing.T) {
	e := newAccountsEnv(t)
	admin, _ := e.user("admin")
	tokenP := authenticate(t, e.accounts, e.token(admin, "read", "write"))

	// A service record made inside an API call takes the operation, its
	// kind, the entry point and the actor's token from the context.
	call := &auth.Call{OperationID: "adminCreateUser", Kind: "mutate", Via: auth.ViaMCP, Tool: "api_call", RequestID: "req_test"}
	ctx := auth.WithCall(auth.WithPrincipal(e.ctx, tokenP), call)
	ctx = auth.WithClient(ctx, auth.Client{IP: "203.0.113.4"})
	created, err := e.admin.CreateUser(ctx, tokenP, service.AdminCreateUserInput{
		RegisterInput: service.RegisterInput{Username: "carol", Password: "correct horse"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !call.Audited() {
		t.Fatal("the call is not marked as audited")
	}
	audit := service.NewAudit(e.db, nil)
	events, _, err := audit.List(e.ctx, admin, service.AuditFilter{Action: "adminCreateUser"})
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %v, %v", events, err)
	}
	ev := events[0]
	if ev.ActorKind != "token" || ev.ActorID != admin.UserID || ev.ActorName != "agent" || ev.Kind != "mutate" ||
		ev.Via != "mcp" || ev.Tool != "api_call" || ev.RequestID != "req_test" || ev.IP != "203.0.113.4" ||
		ev.ResourceKind != "user" || ev.ResourceID != created.User.ID.String() || ev.Details["token_id"] != tokenP.CredentialID.String() {
		t.Fatalf("event = %+v", ev)
	}

	// A mutation whose service wrote nothing gets a generic record; reads,
	// audited calls, malformed requests and anonymous 401s get none.
	generic := &auth.Call{OperationID: "setTriggers", Kind: "mutate", Via: auth.ViaAPI}
	audit.RecordCall(auth.WithCall(auth.WithPrincipal(e.ctx, admin), generic), service.CallOutcome{Status: 403, ResourceKind: "task", ResourceID: "t-1"})
	for _, c := range []struct {
		call   *auth.Call
		p      *auth.Principal
		status int
	}{
		{&auth.Call{OperationID: "listRuns", Kind: "read"}, admin, 200},
		{call, tokenP, 200},
		{&auth.Call{OperationID: "startRun", Kind: "execute"}, admin, 400},
		{&auth.Call{OperationID: "startRun", Kind: "execute"}, nil, 401},
	} {
		ctx := auth.WithCall(e.ctx, c.call)
		if c.p != nil {
			ctx = auth.WithPrincipal(ctx, c.p)
		}
		audit.RecordCall(ctx, service.CallOutcome{Status: c.status})
	}
	events, _, err = audit.List(e.ctx, admin, service.AuditFilter{ResourceKind: "task"})
	if err != nil || len(events) != 1 || events[0].Action != "setTriggers" || events[0].Result != "denied" || events[0].ResourceID != "t-1" {
		t.Fatalf("generic events = %+v, %v", events, err)
	}
	for _, action := range []string{"listRuns", "startRun"} {
		if got, _, _ := audit.List(e.ctx, admin, service.AuditFilter{Action: action}); len(got) != 0 {
			t.Errorf("%s recorded: %+v", action, got)
		}
	}
}

func TestAuditListFiltersAndPages(t *testing.T) {
	e := newAccountsEnv(t)
	admin, _ := e.user("admin")
	bob, _ := e.user("bob")
	_, _, _ = e.accounts.Login(e.ctx, "bob", "wrong password")
	audit := service.NewAudit(e.db, nil)

	all, _, err := audit.List(e.ctx, admin, service.AuditFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("all = %d, %v", len(all), err)
	}
	if !all[0].TS.After(all[2].TS) && all[0].TS != all[2].TS {
		t.Errorf("not newest first: %v", all)
	}
	got, _, _ := audit.List(e.ctx, admin, service.AuditFilter{ActorID: bob.UserID.String()})
	if len(got) != 2 {
		t.Errorf("by actor = %d", len(got))
	}
	got, _, _ = audit.List(e.ctx, admin, service.AuditFilter{Result: "failure"})
	if len(got) != 1 || got[0].Details["reason"] != "wrong_password" {
		t.Errorf("failures = %+v", got)
	}
	if got, _, _ = audit.List(e.ctx, admin, service.AuditFilter{ActorID: "not-a-uuid"}); len(got) != 0 {
		t.Errorf("bad actor id matched %d", len(got))
	}
	got, _, _ = audit.List(e.ctx, admin, service.AuditFilter{Since: time.Now().Add(time.Hour)})
	if len(got) != 0 {
		t.Errorf("future since matched %d", len(got))
	}
	page1, next, err := audit.List(e.ctx, admin, service.AuditFilter{Limit: new(2)})
	if err != nil || len(page1) != 2 || next == "" {
		t.Fatalf("page 1 = %d, %q, %v", len(page1), next, err)
	}
	page2, next, err := audit.List(e.ctx, admin, service.AuditFilter{Limit: new(2), Cursor: next})
	if err != nil || len(page2) != 1 || next != "" || page2[0].ID == page1[1].ID {
		t.Fatalf("page 2 = %v, %q, %v", page2, next, err)
	}
	_, _, err = audit.List(e.ctx, admin, service.AuditFilter{Cursor: "%%%"})
	wantValidation(t, err, "cursor")
}
