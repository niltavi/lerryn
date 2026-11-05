package auth_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/auth/oidctest"
)

const redirect = "https://lerryn.example.com/api/v1/auth/oidc/callback"

func newOIDC(t *testing.T, p *oidctest.Provider) *auth.OIDC {
	t.Helper()
	o, err := auth.NewOIDC(auth.OIDCConfig{
		Issuer: p.Issuer(), ClientID: p.ClientID, ClientSecret: p.ClientSecret, RedirectURL: redirect,
	})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// callback splits the callback URL the provider sends the browser to.
func callback(t *testing.T, cb string) (state, code string) {
	t.Helper()
	u, err := url.Parse(cb)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != redirect {
		t.Fatalf("callback to %s, want %s", got, redirect)
	}
	return u.Query().Get("state"), u.Query().Get("code")
}

var alice = oidctest.User{Subject: "sub-alice", PreferredUsername: "alice", Email: "alice@example.com", Name: "Alice"}

func TestOIDCLogin(t *testing.T) {
	ctx := context.Background()
	p := oidctest.New(t)
	o := newOIDC(t, p)

	authURL, cookie, err := o.Begin(ctx, "/tasks")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cookie, "/tasks") {
		t.Errorf("state cookie is not encoded: %s", cookie)
	}
	state, code := callback(t, p.Authorize(authURL, alice))
	claims, returnTo, err := o.Finish(ctx, cookie, state, code)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Issuer != p.Issuer() || claims.Subject != "sub-alice" || claims.Username != "alice" ||
		claims.Email != "alice@example.com" || claims.Name != "Alice" || returnTo != "/tasks" {
		t.Fatalf("claims = %+v, return to %q", claims, returnTo)
	}

	// The code is single use, so replaying the callback fails at the provider.
	if _, _, err := o.Finish(ctx, cookie, state, code); !errors.Is(err, auth.ErrOIDCToken) {
		t.Fatalf("replay: err = %v, want ErrOIDCToken", err)
	}
}

func TestOIDCRefusesForgedCallbacks(t *testing.T) {
	ctx := context.Background()
	p := oidctest.New(t)
	o := newOIDC(t, p)
	other := newOIDC(t, oidctest.New(t)) // signs state cookies with another key

	tests := []struct {
		name   string
		tamper oidctest.Tamper
		// edit changes what the callback presents.
		edit func(cookie, state, code string) (string, string, string)
		want error
	}{
		{name: "no cookie", edit: func(_, s, c string) (string, string, string) { return "", s, c }, want: auth.ErrOIDCState},
		{name: "state mismatch", edit: func(k, _, c string) (string, string, string) { return k, "forged", c }, want: auth.ErrOIDCState},
		{name: "empty state", edit: func(k, _, c string) (string, string, string) { return k, "", c }, want: auth.ErrOIDCState},
		{name: "tampered cookie", edit: func(k, s, c string) (string, string, string) {
			payload, sig, _ := strings.Cut(k, ".")
			return payload[:len(payload)-2] + "AA." + sig, s, c
		}, want: auth.ErrOIDCState},
		{name: "cookie from another key", edit: func(_, s, c string) (string, string, string) {
			_, k, err := other.Begin(ctx, "/")
			if err != nil {
				t.Fatal(err)
			}
			return k, s, c
		}, want: auth.ErrOIDCState},
		{name: "wrong verifier", edit: func(_, s, c string) (string, string, string) {
			// A valid cookie of another login carries another verifier;
			// keep its state so only PKCE can tell the difference.
			authURL, k, err := o.Begin(ctx, "/")
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(authURL)
			if err != nil {
				t.Fatal(err)
			}
			return k, u.Query().Get("state"), c
		}, want: auth.ErrOIDCToken},
		{name: "no code", edit: func(k, s, _ string) (string, string, string) { return k, s, "" }, want: auth.ErrOIDCToken},
		{name: "wrong nonce", tamper: oidctest.Tamper{WrongNonce: true}, want: auth.ErrOIDCToken},
		{name: "wrong audience", tamper: oidctest.Tamper{WrongAudience: true}, want: auth.ErrOIDCToken},
		{name: "wrong issuer", tamper: oidctest.Tamper{WrongIssuer: true}, want: auth.ErrOIDCToken},
		{name: "expired", tamper: oidctest.Tamper{Expired: true}, want: auth.ErrOIDCToken},
		{name: "foreign signing key", tamper: oidctest.Tamper{ForeignKey: true}, want: auth.ErrOIDCToken},
		{name: "no id token", tamper: oidctest.Tamper{NoIDToken: true}, want: auth.ErrOIDCToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p.SetTamper(tt.tamper)
			authURL, cookie, err := o.Begin(ctx, "/")
			if err != nil {
				t.Fatal(err)
			}
			state, code := callback(t, p.Authorize(authURL, alice))
			if tt.edit != nil {
				cookie, state, code = tt.edit(cookie, state, code)
			}
			if _, _, err := o.Finish(ctx, cookie, state, code); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}
