package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/store"
)

// credentialRecheck is how long an open stream trusts the credential that
// opened it. A request is authenticated once, so without this a log or
// event stream would outlive logout, token revocation and account disable
// for as long as the client keeps the connection. A variable so tests can
// shorten it.
var credentialRecheck = 5 * time.Second

// credentialGuard re-validates a streaming caller's credential at most once
// per credentialRecheck.
type credentialGuard struct {
	db      *store.DB
	p       *auth.Principal
	checked time.Time
}

func newCredentialGuard(db *store.DB, p *auth.Principal) *credentialGuard {
	// The request that opened the stream was just authenticated.
	return &credentialGuard{db: db, p: p, checked: time.Now()}
}

// check returns ErrUnauthenticated once the credential is no longer live.
func (g *credentialGuard) check(ctx context.Context) error {
	if time.Since(g.checked) < credentialRecheck {
		return nil
	}
	if err := liveCredential(ctx, g.db, g.p); err != nil {
		return err
	}
	g.checked = time.Now()
	return nil
}

// liveCredential reports whether the session or access token that
// authenticated p still would: it exists, is not revoked or expired, and
// belongs to an enabled user. It mirrors the rules of Accounts.Authenticate
// but looks the credential up by ID, since a stream holds no token.
func liveCredential(ctx context.Context, db *store.DB, p *auth.Principal) error {
	now := store.Now()
	switch p.Method {
	case auth.MethodAccessToken:
		var t store.AccessToken
		err := db.NewSelect().Model(&t).Where("id = ?", p.CredentialID).Where("user_id = ?", p.UserID).Scan(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnauthenticated
		}
		if err != nil {
			return fmt.Errorf("load access token: %w", err)
		}
		if !t.RevokedAt.IsZero() || (!t.ExpiresAt.IsZero() && !now.Before(t.ExpiresAt)) {
			return ErrUnauthenticated
		}
	case auth.MethodSession:
		var s store.Session
		err := db.NewSelect().Model(&s).Where("id = ?", p.CredentialID).Where("user_id = ?", p.UserID).Scan(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnauthenticated
		}
		if err != nil {
			return fmt.Errorf("load session: %w", err)
		}
		if !now.Before(s.ExpiresAt) {
			return ErrUnauthenticated
		}
	default:
		return ErrUnauthenticated
	}
	var u store.User
	err := db.NewSelect().Model(&u).Column("disabled_at").Where("id = ?", p.UserID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUnauthenticated
	}
	if err != nil {
		return fmt.Errorf("load user: %w", err)
	}
	if !u.DisabledAt.IsZero() {
		return ErrUnauthenticated
	}
	return nil
}
