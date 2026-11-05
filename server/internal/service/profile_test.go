package service_test

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

const publicURL = "https://lerryn.example.com"

// accountsEnv is the account and admin services on one test database.
type accountsEnv struct {
	t        *testing.T
	ctx      context.Context
	db       *store.DB
	accounts *service.Accounts
	admin    *service.Admin
}

func newAccountsEnv(t *testing.T, opts ...service.AccountOption) *accountsEnv {
	t.Helper()
	db := storetest.Open(t)
	a := service.NewAccounts(db, nil, append([]service.AccountOption{service.WithPublicURL(publicURL)}, opts...)...)
	return &accountsEnv{t: t, ctx: context.Background(), db: db, accounts: a, admin: service.NewAdmin(a, nil)}
}

// user registers an account and returns its web session principal and
// session token.
func (e *accountsEnv) user(name string) (*auth.Principal, string) {
	e.t.Helper()
	_, sess := register(e.t, e.accounts, name)
	return authenticate(e.t, e.accounts, sess.Token), sess.Token
}

func (e *accountsEnv) token(p *auth.Principal, scopes ...string) string {
	e.t.Helper()
	tok, _, err := e.accounts.CreateToken(e.ctx, p, service.CreateTokenInput{Name: "agent", Scopes: scopes})
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

func (e *accountsEnv) wantAuth(token string, want error) {
	e.t.Helper()
	_, err := e.accounts.Authenticate(e.ctx, token)
	if !errors.Is(err, want) && (want != nil || err != nil) {
		e.t.Fatalf("authenticate: err = %v, want %v", err, want)
	}
}

func wantValidation(t *testing.T, err error, pointer string) {
	t.Helper()
	var verr *service.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a validation error at %s", err, pointer)
	}
	for _, f := range verr.Fields {
		if f.Pointer == pointer || f.Parameter == pointer {
			return
		}
	}
	t.Fatalf("validation errors %v do not include %s", verr.Fields, pointer)
}

func TestUpdateMe(t *testing.T) {
	e := newAccountsEnv(t)
	p, _ := e.user("alice")
	name, email, locale, tz := "爱丽丝", "alice@example.com", "zh-CN", "Asia/Singapore"
	acct, err := e.accounts.UpdateMe(e.ctx, p, service.UpdateMeInput{DisplayName: &name, Email: &email, Locale: &locale, Timezone: &tz})
	if err != nil {
		t.Fatal(err)
	}
	if u := acct.User; u.DisplayName != name || u.Email != email || u.Locale != locale || u.Timezone != tz {
		t.Fatalf("user = %+v", u)
	}
	empty := ""
	acct, err = e.accounts.UpdateMe(e.ctx, p, service.UpdateMeInput{Email: &empty, Timezone: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if u := acct.User; u.Email != "" || u.Timezone != "" || u.DisplayName != name {
		t.Fatalf("after clearing: %+v", u)
	}
	for _, tt := range []struct {
		in      service.UpdateMeInput
		pointer string
	}{
		{service.UpdateMeInput{Timezone: new("Mars/Olympus")}, "#/timezone"},
		{service.UpdateMeInput{Timezone: new("Local")}, "#/timezone"},
		{service.UpdateMeInput{Email: new("not an email")}, "#/email"},
		{service.UpdateMeInput{Locale: new("fr")}, "#/locale"},
	} {
		_, err := e.accounts.UpdateMe(e.ctx, p, tt.in)
		wantValidation(t, err, tt.pointer)
	}
}

func TestChangePassword(t *testing.T) {
	e := newAccountsEnv(t)
	p, current := e.user("alice")
	_, other, err := e.accounts.Login(e.ctx, "alice", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	_, device, err := e.accounts.LoginDevice(e.ctx, "alice", "correct horse", &service.DeviceInfo{Name: "iPhone", Platform: "ios"})
	if err != nil {
		t.Fatal(err)
	}
	token := e.token(p, "read")

	wantErr(t, e.accounts.ChangePassword(e.ctx, p, "wrong password", "new password 1"), service.ErrUnauthenticated)
	wantValidation(t, e.accounts.ChangePassword(e.ctx, p, "correct horse", "short"), "#/new_password")
	if err := e.accounts.ChangePassword(e.ctx, p, "correct horse", "new password 1"); err != nil {
		t.Fatal(err)
	}
	// The session that changed it stays; other sessions and devices end;
	// tokens are not sessions and stay.
	e.wantAuth(current, nil)
	e.wantAuth(other.Token, service.ErrUnauthenticated)
	e.wantAuth(device.Token, service.ErrUnauthenticated)
	e.wantAuth(token, nil)
	if _, _, err := e.accounts.Login(e.ctx, "alice", "new password 1"); err != nil {
		t.Fatalf("login with the new password: %v", err)
	}

	// Tokens and device sessions cannot change passwords.
	tp := authenticate(t, e.accounts, token)
	wantErr(t, e.accounts.ChangePassword(e.ctx, tp, "new password 1", "another one 1"), service.ErrForbidden)

	// Wrong current passwords are rate limited like logins.
	for range 10 {
		_ = e.accounts.ChangePassword(e.ctx, p, "wrong password", "another one 1")
	}
	wantErr(t, e.accounts.ChangePassword(e.ctx, p, "new password 1", "another one 1"), service.ErrRateLimited)
}

func pairingCode(t *testing.T, e *accountsEnv, p *auth.Principal) service.PairingCode {
	t.Helper()
	code, err := e.accounts.CreatePairingCode(e.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

var iphone = service.DeviceInfo{Name: "张林的 iPhone", Platform: "ios", OSVersion: "iOS 26.1"}

func TestPairing(t *testing.T) {
	e := newAccountsEnv(t)
	p, _ := e.user("alice")
	code := pairingCode(t, e, p)
	if len(code.Code) != 8 || time.Until(code.ExpiresAt) > service.PairingTTL || time.Until(code.ExpiresAt) < service.PairingTTL-time.Minute {
		t.Fatalf("code = %+v", code)
	}
	u, err := url.Parse(code.URI)
	if err != nil || u.Scheme != "lerryn" || u.Host != "pair" || u.Query().Get("server") != publicURL || u.Query().Get("code") != code.Code {
		t.Fatalf("uri = %s", code.URI)
	}

	// The QR code of another deployment is refused before the code is used.
	_, _, err = e.accounts.ExchangePairingCode(e.ctx, "https://other.example.com", code.Code, iphone)
	wantValidation(t, err, "#/server")

	acct, sess, err := e.accounts.ExchangePairingCode(e.ctx, publicURL+"/", " "+lowerHyphen(code.Code), iphone)
	if err != nil {
		t.Fatal(err)
	}
	if acct.User.ID != p.UserID {
		t.Fatalf("paired account %s, want %s", acct.User.ID, p.UserID)
	}
	dp := authenticate(t, e.accounts, sess.Token)
	if !dp.ReadOnly || len(dp.Scopes) != 1 || dp.Scopes[0] != auth.ScopeRead || dp.Method != auth.MethodSession {
		t.Fatalf("device principal = %+v", dp)
	}

	// A code works once.
	_, _, err = e.accounts.ExchangePairingCode(e.ctx, publicURL, code.Code, iphone)
	wantErr(t, err, service.ErrUnauthenticated)

	// A device session cannot create tokens, pairing codes or passwords.
	_, _, err = e.accounts.CreateToken(e.ctx, dp, service.CreateTokenInput{Name: "x", Scopes: []string{"read"}})
	wantErr(t, err, service.ErrForbidden)
	_, err = e.accounts.CreatePairingCode(e.ctx, dp)
	wantErr(t, err, service.ErrForbidden)

	// A new code replaces the unused one.
	first := pairingCode(t, e, p)
	second := pairingCode(t, e, p)
	_, _, err = e.accounts.ExchangePairingCode(e.ctx, publicURL, first.Code, iphone)
	wantErr(t, err, service.ErrUnauthenticated)
	if _, _, err := e.accounts.ExchangePairingCode(e.ctx, publicURL, second.Code, iphone); err != nil {
		t.Fatal(err)
	}

	// Expired codes are refused.
	expired := pairingCode(t, e, p)
	if _, err := e.db.NewUpdate().Model((*store.PairingCode)(nil)).Set("expires_at = ?", store.Now().Add(-time.Second)).
		Where("used_at IS NULL").Exec(e.ctx); err != nil {
		t.Fatal(err)
	}
	_, _, err = e.accounts.ExchangePairingCode(e.ctx, publicURL, expired.Code, iphone)
	wantErr(t, err, service.ErrUnauthenticated)
}

func lowerHyphen(code string) string {
	b := []byte(code)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b[:4]) + "-" + string(b[4:])
}

func TestPairingIsRateLimited(t *testing.T) {
	e := newAccountsEnv(t)
	p, _ := e.user("alice")
	ctx := auth.WithClient(e.ctx, auth.Client{IP: "198.51.100.7"})
	for range 10 {
		_, _, err := e.accounts.ExchangePairingCode(ctx, publicURL, "AAAAAAAA", iphone)
		wantErr(t, err, service.ErrUnauthenticated)
	}
	code := pairingCode(t, e, p)
	_, _, err := e.accounts.ExchangePairingCode(ctx, publicURL, code.Code, iphone)
	wantErr(t, err, service.ErrRateLimited)
	// Another address is not affected.
	other := auth.WithClient(e.ctx, auth.Client{IP: "198.51.100.8"})
	if _, _, err := e.accounts.ExchangePairingCode(other, publicURL, code.Code, iphone); err != nil {
		t.Fatal(err)
	}
}

func TestDevices(t *testing.T) {
	e := newAccountsEnv(t)
	p, _ := e.user("alice")
	bob, _ := e.user("bob")
	_, sess, err := e.accounts.ExchangePairingCode(e.ctx, publicURL, pairingCode(t, e, p).Code, iphone)
	if err != nil {
		t.Fatal(err)
	}
	_, pad, err := e.accounts.LoginDevice(e.ctx, "alice", "correct horse", &service.DeviceInfo{Name: "iPad", Platform: "ios"})
	if err != nil {
		t.Fatal(err)
	}
	dp := authenticate(t, e.accounts, sess.Token)

	devices, err := e.accounts.ListDevices(e.ctx, dp)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("devices = %+v", devices)
	}
	for _, d := range devices {
		if d.Current != (d.Session.ID == dp.CredentialID) {
			t.Errorf("device %s current = %v", d.Session.DeviceName, d.Current)
		}
	}
	// Web sessions are not devices, and other users see none of these.
	if got, _ := e.accounts.ListDevices(e.ctx, bob); len(got) != 0 {
		t.Fatalf("bob sees %d devices", len(got))
	}
	wantErr(t, e.accounts.RevokeDevice(e.ctx, bob, pad.ID), service.ErrNotFound)
	wantErr(t, e.accounts.RevokeDevice(e.ctx, p, p.CredentialID), service.ErrNotFound)

	if err := e.accounts.RevokeDevice(e.ctx, p, pad.ID); err != nil {
		t.Fatal(err)
	}
	e.wantAuth(pad.Token, service.ErrUnauthenticated)
	e.wantAuth(sess.Token, nil)
}

func TestRegistrationAndPasswordLoginSettings(t *testing.T) {
	e := newAccountsEnv(t)
	admin, _ := e.user("admin")
	bob, _ := e.user("bob")
	_ = bob

	// Admin-only.
	_, err := e.admin.UpdateSettings(e.ctx, bob, service.SettingsInput{OpenRegistration: new(false)})
	wantErr(t, err, service.ErrPermissionDenied)

	view, err := e.admin.UpdateSettings(e.ctx, admin, service.SettingsInput{OpenRegistration: new(false)})
	if err != nil {
		t.Fatal(err)
	}
	if view.Settings.OpenRegistration || view.UpdatedBy == nil || view.UpdatedBy.ID != admin.UserID {
		t.Fatalf("settings = %+v", view)
	}
	_, _, err = e.accounts.Register(e.ctx, service.RegisterInput{Username: "carol", Password: "correct horse"})
	wantErr(t, err, service.ErrRegistrationDisabled)
	open, first, err := e.accounts.SignupState(e.ctx)
	if err != nil || open || first {
		t.Fatalf("signup state = %v, %v, %v", open, first, err)
	}
	// Admins still create accounts.
	if _, err := e.admin.CreateUser(e.ctx, admin, service.AdminCreateUserInput{
		RegisterInput: service.RegisterInput{Username: "carol", Password: "correct horse"},
	}); err != nil {
		t.Fatal(err)
	}

	// Password login can only be turned off with single sign-on.
	_, err = e.admin.UpdateSettings(e.ctx, admin, service.SettingsInput{LocalPasswordLogin: new(false)})
	wantValidation(t, err, "#/local_password_login")
}

func TestPasswordLoginDisabledKeepsAdmins(t *testing.T) {
	e := newAccountsEnv(t, service.WithOIDC(newTestOIDC(t).client))
	admin, _ := e.user("admin")
	e.user("bob")
	if _, err := e.admin.UpdateSettings(e.ctx, admin, service.SettingsInput{LocalPasswordLogin: new(false)}); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.accounts.Login(e.ctx, "bob", "correct horse")
	wantErr(t, err, service.ErrPasswordLoginDisabled)
	// A wrong password still looks like any failed login.
	_, _, err = e.accounts.Login(e.ctx, "bob", "wrong password")
	wantErr(t, err, service.ErrUnauthenticated)
	if _, _, err := e.accounts.Login(e.ctx, "admin", "correct horse"); err != nil {
		t.Fatalf("admins keep password login: %v", err)
	}
}

func TestFirstAccountRegistersWithRegistrationClosed(t *testing.T) {
	e := newAccountsEnv(t)
	// Closing registration before anyone exists must not lock the
	// deployment out.
	if _, err := e.db.NewInsert().Model(&store.Setting{
		ID: store.NewID(), Key: store.SettingOpenRegistration, Value: "false", UpdatedAt: store.Now(),
	}).Exec(e.ctx); err != nil {
		t.Fatal(err)
	}
	open, first, err := e.accounts.SignupState(e.ctx)
	if err != nil || open || !first {
		t.Fatalf("signup state = %v, %v, %v", open, first, err)
	}
	acct, _, err := e.accounts.Register(e.ctx, service.RegisterInput{Username: "admin", Password: "correct horse"})
	if err != nil || !acct.User.IsAdmin {
		t.Fatalf("first registration: %+v, %v", acct.User, err)
	}
}
