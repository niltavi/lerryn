package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

func newAccounts(t *testing.T) (*service.Accounts, *store.DB) {
	t.Helper()
	db := storetest.Open(t)
	return service.NewAccounts(db, nil), db
}

func register(t *testing.T, a *service.Accounts, username string) (service.Account, service.NewSession) {
	t.Helper()
	acct, sess, err := a.Register(context.Background(), service.RegisterInput{Username: username, Password: "correct horse"})
	if err != nil {
		t.Fatalf("register %s: %v", username, err)
	}
	return acct, sess
}

func authenticate(t *testing.T, a *service.Accounts, token string) *auth.Principal {
	t.Helper()
	p, err := a.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return p
}

func TestRegister(t *testing.T) {
	a, db := newAccounts(t)
	ctx := context.Background()

	alice, sess := register(t, a, "Alice")
	if !alice.User.IsAdmin {
		t.Error("the first account must become an admin")
	}
	if !strings.HasPrefix(sess.Token, auth.SessionTokenPrefix) || !sess.ExpiresAt.After(time.Now()) {
		t.Errorf("session = %+v", sess)
	}
	var binding store.RoleBinding
	if err := db.NewSelect().Model(&binding).Where("project_id = ?", alice.PersonalProjectID).Scan(ctx); err != nil {
		t.Fatalf("personal project binding: %v", err)
	}
	if binding.UserID != alice.User.ID || binding.Role != service.RoleOwner {
		t.Errorf("binding = %+v", binding)
	}

	bob, _ := register(t, a, "bob")
	if bob.User.IsAdmin {
		t.Error("only the first account becomes an admin")
	}

	_, _, err := a.Register(ctx, service.RegisterInput{Username: "ALICE", Password: "another pass"})
	if !errors.Is(err, service.ErrUsernameTaken) {
		t.Fatalf("err = %v, want ErrUsernameTaken (usernames are case-insensitive)", err)
	}
}

func TestRegisterValidation(t *testing.T) {
	a, _ := newAccounts(t)
	_, _, err := a.Register(context.Background(), service.RegisterInput{
		Username: "-bad", Password: "short", Email: "not-an-email", DisplayName: strings.Repeat("名", 65),
	})
	var verr *service.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}
	got := map[string]bool{}
	for _, f := range verr.Fields {
		got[f.Pointer] = true
	}
	for _, want := range []string{"#/username", "#/password", "#/email", "#/display_name"} {
		if !got[want] {
			t.Errorf("no error for %s in %v", want, verr.Fields)
		}
	}
	// Registration without email or display name is the default path.
	if _, _, err := a.Register(context.Background(), service.RegisterInput{Username: "u_1.x-y", Password: "12345678"}); err != nil {
		t.Fatalf("minimal registration: %v", err)
	}
}

func TestLoginLogout(t *testing.T) {
	a, _ := newAccounts(t)
	ctx := context.Background()
	alice, _ := register(t, a, "alice")

	for _, tt := range []struct{ username, password string }{
		{"alice", "wrong password"},
		{"nobody", "correct horse"},
	} {
		if _, _, err := a.Login(ctx, tt.username, tt.password); !errors.Is(err, service.ErrUnauthenticated) {
			t.Errorf("login %s/%s: err = %v, want ErrUnauthenticated", tt.username, tt.password, err)
		}
	}

	acct, sess, err := a.Login(ctx, "ALICE", "correct horse")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if acct.User.ID != alice.User.ID || acct.PersonalProjectID != alice.PersonalProjectID {
		t.Errorf("account = %+v", acct)
	}
	p := authenticate(t, a, sess.Token)
	if p.Method != auth.MethodSession || len(p.Scopes) != len(auth.AllScopes) || p.Username != "alice" {
		t.Errorf("principal = %+v", p)
	}

	if err := a.Logout(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, sess.Token); !errors.Is(err, service.ErrUnauthenticated) {
		t.Fatalf("session still valid after logout: %v", err)
	}
}

func TestLoginRateLimit(t *testing.T) {
	a, _ := newAccounts(t)
	ctx := auth.WithClient(context.Background(), auth.Client{IP: "192.0.2.7"})
	register(t, a, "alice")
	for range 10 {
		if _, _, err := a.Login(ctx, "alice", "wrong password"); !errors.Is(err, service.ErrUnauthenticated) {
			t.Fatalf("err = %v", err)
		}
	}
	if _, _, err := a.Login(ctx, "alice", "correct horse"); !errors.Is(err, service.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited after 10 failures", err)
	}
	// The address is blocked too, so cycling usernames does not help.
	if _, _, err := a.Login(ctx, "someone-else", "x"); !errors.Is(err, service.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited for the same address", err)
	}
}

func TestAccessTokens(t *testing.T) {
	a, db := newAccounts(t)
	ctx := context.Background()
	_, aliceSess := register(t, a, "alice")
	_, bobSess := register(t, a, "bob")
	alice := authenticate(t, a, aliceSess.Token)
	bob := authenticate(t, a, bobSess.Token)

	token, row, err := a.CreateToken(ctx, alice, service.CreateTokenInput{Name: "laptop", Scopes: []string{"read"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, auth.AccessTokenPrefix) || !strings.HasPrefix(token, row.Prefix) || row.Prefix == "" {
		t.Errorf("token %q, prefix %q", token, row.Prefix)
	}
	p := authenticate(t, a, token)
	if p.Method != auth.MethodAccessToken || p.CredentialID != row.ID || !p.HasScope(auth.ScopeRead) || p.HasScope(auth.ScopeWrite) {
		t.Errorf("principal = %+v", p)
	}

	// A token cannot mint more tokens.
	if _, _, err := a.CreateToken(ctx, p, service.CreateTokenInput{Name: "x", Scopes: []string{"read"}}); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("err = %v, want ErrForbidden", err)
	}
	_, _, err = a.CreateToken(ctx, alice, service.CreateTokenInput{Name: " ", Scopes: []string{"read", "admin", "read"}, ExpiresInDays: new(0)})
	var verr *service.ValidationError
	if !errors.As(err, &verr) || len(verr.Fields) != 4 {
		t.Fatalf("err = %v, want 4 field errors", err)
	}

	// Pagination: three tokens in pages of two, newest first.
	for _, name := range []string{"second", "third"} {
		if _, _, err := a.CreateToken(ctx, alice, service.CreateTokenInput{Name: name, Scopes: []string{"read", "write"}, ExpiresInDays: new(30)}); err != nil {
			t.Fatal(err)
		}
	}
	page1, next, err := a.ListTokens(ctx, alice, new(2), "")
	if err != nil || len(page1) != 2 || next == "" || page1[0].Name != "third" {
		t.Fatalf("page 1 = %v, %q, %v", names(page1), next, err)
	}
	page2, next, err := a.ListTokens(ctx, alice, new(2), next)
	if err != nil || len(page2) != 1 || next != "" || page2[0].Name != "laptop" {
		t.Fatalf("page 2 = %v, %q, %v", names(page2), next, err)
	}
	if _, _, err := a.ListTokens(ctx, alice, new(2), "garbage"); !errors.As(err, &verr) {
		t.Errorf("bad cursor: err = %v", err)
	}
	if _, _, err := a.ListTokens(ctx, alice, new(0), ""); !errors.As(err, &verr) {
		t.Errorf("limit 0: err = %v, want a validation error", err)
	}
	if own, _, _ := a.ListTokens(ctx, bob, nil, ""); len(own) != 0 {
		t.Errorf("bob sees %d of alice's tokens", len(own))
	}

	// Revocation: another user's token looks like it does not exist.
	if err := a.RevokeToken(ctx, bob, row.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := a.RevokeToken(ctx, alice, row.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.RevokeToken(ctx, alice, row.ID); err != nil {
		t.Fatalf("revoking twice must succeed: %v", err)
	}
	if _, err := a.Authenticate(ctx, token); !errors.Is(err, service.ErrUnauthenticated) {
		t.Fatalf("revoked token still valid: %v", err)
	}

	// Expiry.
	expiring, expRow, err := a.CreateToken(ctx, alice, service.CreateTokenInput{Name: "short", Scopes: []string{"read"}, ExpiresInDays: new(1)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.NewUpdate().Model((*store.AccessToken)(nil)).Set("expires_at = ?", store.Now().Add(-time.Second)).Where("id = ?", expRow.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, expiring); !errors.Is(err, service.ErrUnauthenticated) {
		t.Fatalf("expired token still valid: %v", err)
	}
}

func TestDisabledUserLosesAccess(t *testing.T) {
	a, db := newAccounts(t)
	ctx := context.Background()
	alice, sess := register(t, a, "alice")
	token, _, err := a.CreateToken(ctx, authenticate(t, a, sess.Token), service.CreateTokenInput{Name: "t", Scopes: []string{"read"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.NewUpdate().Model((*store.User)(nil)).Set("disabled_at = ?", store.Now()).Where("id = ?", alice.User.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	for _, cred := range []string{sess.Token, token} {
		if _, err := a.Authenticate(ctx, cred); !errors.Is(err, service.ErrUnauthenticated) {
			t.Errorf("credential of a disabled user still valid: %v", err)
		}
	}
	if _, _, err := a.Login(ctx, "alice", "correct horse"); !errors.Is(err, service.ErrUnauthenticated) {
		t.Errorf("disabled user can log in: %v", err)
	}
}

func TestAuthenticateRejectsGarbage(t *testing.T) {
	a, _ := newAccounts(t)
	for _, tok := range []string{"", "wl_pat_nope", "wl_ses_nope", "Bearer xyz", uuid.NewString()} {
		if _, err := a.Authenticate(context.Background(), tok); !errors.Is(err, service.ErrUnauthenticated) {
			t.Errorf("Authenticate(%q) = %v", tok, err)
		}
	}
}

func TestAuditTrail(t *testing.T) {
	a, db := newAccounts(t)
	ctx := auth.WithClient(context.Background(), auth.Client{IP: "192.0.2.9"})
	_, sess, err := a.Register(ctx, service.RegisterInput{Username: "alice", Password: "correct horse"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = a.Login(ctx, "alice", "wrong password")
	token, row, err := a.CreateToken(ctx, authenticate(t, a, sess.Token), service.CreateTokenInput{Name: "t", Scopes: []string{"read"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.RevokeToken(ctx, authenticate(t, a, sess.Token), row.ID); err != nil {
		t.Fatal(err)
	}

	var events []store.AuditEvent
	if err := db.NewSelect().Model(&events).Order("ts ASC", "id ASC").Scan(ctx); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range events {
		got = append(got, e.Action+"/"+e.Result)
		if e.IP != "192.0.2.9" {
			t.Errorf("%s: ip = %q", e.Action, e.IP)
		}
		for _, v := range e.Details {
			if s, ok := v.(string); ok && (strings.Contains(s, token) || strings.Contains(s, "correct horse")) {
				t.Errorf("%s: audit details contain a secret", e.Action)
			}
		}
	}
	want := []string{"user.register/success", "session.login/failure", "token.create/success", "token.revoke/success"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audit trail = %v, want %v", got, want)
	}
}

func names(tokens []store.AccessToken) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = t.Name
	}
	return out
}
