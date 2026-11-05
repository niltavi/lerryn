package service_test

import (
	"net/url"
	"testing"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/auth/oidctest"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

type testOIDC struct {
	provider *oidctest.Provider
	client   *auth.OIDC
}

func newTestOIDC(t *testing.T) testOIDC {
	t.Helper()
	p := oidctest.New(t)
	c, err := auth.NewOIDC(auth.OIDCConfig{
		Issuer: p.Issuer(), ClientID: p.ClientID, ClientSecret: p.ClientSecret,
		RedirectURL: publicURL + "/api/v1/auth/oidc/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	return testOIDC{provider: p, client: c}
}

// ssoLogin runs a complete single sign-on login as u.
func ssoLogin(t *testing.T, e *accountsEnv, o testOIDC, u oidctest.User) (service.Account, service.NewSession, error) {
	t.Helper()
	authURL, cookie, err := e.accounts.BeginOIDC(e.ctx, "/runs")
	if err != nil {
		t.Fatal(err)
	}
	cb, err := url.Parse(o.provider.Authorize(authURL, u))
	if err != nil {
		t.Fatal(err)
	}
	acct, sess, returnTo, err := e.accounts.FinishOIDC(e.ctx, cookie, cb.Query().Get("state"), cb.Query().Get("code"))
	if err == nil && returnTo != "/runs" {
		t.Errorf("return to %q", returnTo)
	}
	return acct, sess, err
}

func TestOIDCLoginCreatesAndLinksUsers(t *testing.T) {
	o := newTestOIDC(t)
	e := newAccountsEnv(t, service.WithOIDC(o.client))
	admin, _ := e.user("admin")
	e.user("zhanglin")
	// Registration is closed; single sign-on still creates accounts.
	if _, err := e.admin.UpdateSettings(e.ctx, admin, service.SettingsInput{OpenRegistration: new(false)}); err != nil {
		t.Fatal(err)
	}

	lin := oidctest.User{Subject: "s-1", PreferredUsername: "ZhangLin", Email: "lin@example.com", Name: "张林"}
	acct, sess, err := ssoLogin(t, e, o, lin)
	if err != nil {
		t.Fatal(err)
	}
	// The name is taken (case-insensitively), so a suffix is added.
	if u := acct.User; u.Username != "ZhangLin2" || u.DisplayName != "张林" || u.Email != "lin@example.com" ||
		u.PasswordHash != "" || u.IsAdmin {
		t.Fatalf("user = %+v", u)
	}
	if len(acct.Identities) != 1 || acct.Identities[0].Issuer != o.provider.Issuer() || acct.Identities[0].Subject != "s-1" {
		t.Fatalf("identities = %+v", acct.Identities)
	}
	if p := authenticate(t, e.accounts, sess.Token); p.ReadOnly || p.UserID != acct.User.ID {
		t.Fatalf("principal = %+v", p)
	}

	// The next login finds the same user by (issuer, subject), even when
	// the provider reports another username.
	lin.PreferredUsername = "lin.zhang"
	again, _, err := ssoLogin(t, e, o, lin)
	if err != nil || again.User.ID != acct.User.ID {
		t.Fatalf("second login: %+v, %v", again.User, err)
	}

	// A user without a usable username claim gets one from the email.
	other, _, err := ssoLogin(t, e, o, oidctest.User{Subject: "s-2", PreferredUsername: "王", Email: "wang.fang@example.com"})
	if err != nil || other.User.Username != "wang.fang" {
		t.Fatalf("fallback username: %+v, %v", other.User, err)
	}

	// Disabling the account stops single sign-on too.
	if _, err := e.admin.UpdateUser(e.ctx, admin, acct.User.ID, service.AdminUpdateUserInput{Disabled: new(true)}); err != nil {
		t.Fatal(err)
	}
	_, _, err = ssoLogin(t, e, o, lin)
	wantErr(t, err, service.ErrUnauthenticated)

	view, err := e.admin.Settings(e.ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if view.OIDC == nil || view.OIDC.UserCount != 2 || view.OIDC.UsernameClaim != "preferred_username" ||
		view.OIDC.CallbackURL != publicURL+"/api/v1/auth/oidc/callback" {
		t.Fatalf("oidc info = %+v", view.OIDC)
	}
}

func TestOIDCFirstUserBecomesAdmin(t *testing.T) {
	o := newTestOIDC(t)
	e := newAccountsEnv(t, service.WithOIDC(o.client))
	acct, _, err := ssoLogin(t, e, o, oidctest.User{Subject: "s-1", PreferredUsername: "root"})
	if err != nil || !acct.User.IsAdmin {
		t.Fatalf("first sso user: %+v, %v", acct.User, err)
	}
}

func TestOIDCRefusesTamperedLogins(t *testing.T) {
	o := newTestOIDC(t)
	e := newAccountsEnv(t, service.WithOIDC(o.client))
	o.provider.SetTamper(oidctest.Tamper{WrongNonce: true})
	_, _, err := ssoLogin(t, e, o, oidctest.User{Subject: "s-1", PreferredUsername: "eve"})
	wantErr(t, err, service.ErrUnauthenticated)
	_, _, _, err = e.accounts.FinishOIDC(e.ctx, "", "state", "code")
	wantErr(t, err, service.ErrUnauthenticated)
	if n, _ := e.db.NewSelect().Model((*store.User)(nil)).Count(e.ctx); n != 0 {
		t.Fatalf("%d users created by refused logins", n)
	}
	failures, err := e.db.NewSelect().Model((*store.AuditEvent)(nil)).Where("result = ?", "failure").Count(e.ctx)
	if err != nil || failures != 2 {
		t.Fatalf("failure audit events = %d, %v", failures, err)
	}
}

func TestOIDCNotConfigured(t *testing.T) {
	e := newAccountsEnv(t)
	_, _, err := e.accounts.BeginOIDC(e.ctx, "/")
	wantErr(t, err, service.ErrNotFound)
}
