package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/store"
)

// oidcCreateAttempts bounds retries when a concurrent first login takes the
// same username or links the same identity.
const oidcCreateAttempts = 3

// OIDCEnabled reports whether single sign-on is configured.
func (a *Accounts) OIDCEnabled() bool { return a.oidc != nil }

// BeginOIDC starts a single sign-on login. It returns the provider URL to
// redirect to and the state cookie value. Without a provider it returns
// ErrNotFound, as the contract asks.
func (a *Accounts) BeginOIDC(ctx context.Context, returnTo string) (authURL, cookie string, err error) {
	if a.oidc == nil {
		return "", "", ErrNotFound
	}
	return a.oidc.Begin(ctx, returnTo)
}

// FinishOIDC completes a single sign-on login and opens a web session. The
// user is found by (issuer, subject); a first login creates the user, even
// when open registration is closed (tech design 12.1), with a username
// taken from the configured claim and a numeric suffix on conflict. A
// disabled account, a forged or replayed callback and an invalid ID token
// all return ErrUnauthenticated. It also returns the console path to go
// back to.
func (a *Accounts) FinishOIDC(ctx context.Context, cookie, state, code string) (Account, NewSession, string, error) {
	if a.oidc == nil {
		return Account{}, NewSession{}, "", ErrNotFound
	}
	fail := func(reason string, e auditEntry) (Account, NewSession, string, error) {
		if e.actorKind == "" {
			e.actorKind = actorAnonymous
		}
		e.action, e.resourceKind = "session.login", "user"
		e.details = map[string]any{"method": "oidc", "reason": reason}
		auditFailure(ctx, a.db, a.log, e)
		return Account{}, NewSession{}, "", ErrUnauthenticated
	}
	claims, returnTo, err := a.oidc.Finish(ctx, cookie, state, code)
	switch {
	case errors.Is(err, auth.ErrOIDCState):
		return fail("state_mismatch", auditEntry{})
	case errors.Is(err, auth.ErrOIDCToken):
		a.log.WarnContext(ctx, "oidc login refused", "error", err)
		return fail("invalid_id_token", auditEntry{})
	case err != nil:
		return Account{}, NewSession{}, "", err
	}
	if claims.Subject == "" {
		return fail("no_subject", auditEntry{})
	}

	var (
		acct Account
		sess NewSession
	)
	for attempt := 1; ; attempt++ {
		acct, sess, err = a.oidcLogin(ctx, claims)
		if !errors.Is(err, errOIDCRace) || attempt == oidcCreateAttempts {
			break
		}
	}
	var disabled *disabledUserError
	if errors.As(err, &disabled) {
		return fail("disabled", auditEntry{actorKind: actorUser, actorID: disabled.user.ID, resourceID: disabled.user.ID.String()})
	}
	if err != nil {
		return Account{}, NewSession{}, "", err
	}
	return acct, sess, returnTo, nil
}

// errOIDCRace means a concurrent login created the same user or identity;
// the login is retried and then finds it.
var errOIDCRace = errors.New("concurrent oidc login")

type disabledUserError struct{ user store.User }

func (e *disabledUserError) Error() string { return "user " + e.user.ID.String() + " is disabled" }

func (a *Accounts) oidcLogin(ctx context.Context, claims auth.OIDCClaims) (acct Account, sess NewSession, err error) {
	err = a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		now := store.Now()
		created := false
		var id store.Identity
		err := tx.NewSelect().Model(&id).Where("issuer = ? AND subject = ?", claims.Issuer, claims.Subject).Scan(ctx)
		var u store.User
		switch {
		case err == nil:
			if err := tx.NewSelect().Model(&u).Where("id = ?", id.UserID).Scan(ctx); err != nil {
				return fmt.Errorf("user of identity %s: %w", id.ID, err)
			}
			if !u.DisabledAt.IsZero() {
				return &disabledUserError{user: u}
			}
			if _, err := tx.NewUpdate().Model((*store.Identity)(nil)).
				Set("username = ?", nullable(claims.Username)).Set("email = ?", nullable(claims.Email)).
				Set("last_login_at = ?", now).Where("id = ?", id.ID).Exec(ctx); err != nil {
				return err
			}
		case errors.Is(err, sql.ErrNoRows):
			users, err := countUsersLocked(ctx, a.db, tx)
			if err != nil {
				return err
			}
			username, err := freeUsername(ctx, tx, usernameBase(claims))
			if err != nil {
				return err
			}
			email := claims.Email
			var v validation
			if checkEmail(&v, email); v.err() != nil {
				email = ""
			}
			created = true
			// The first account of a deployment becomes an admin, however
			// it is created.
			acct, err = createAccount(ctx, tx, newUser{
				username: username, displayName: truncateRunes(claims.Name, maxDisplayRunes),
				email: email, isAdmin: users == 0,
			})
			if errors.Is(err, ErrUsernameTaken) {
				return errOIDCRace
			}
			if err != nil {
				return err
			}
			u = acct.User
			id = store.Identity{
				ID: store.NewID(), UserID: u.ID, Issuer: claims.Issuer, Subject: claims.Subject,
				Username: claims.Username, Email: claims.Email, CreatedAt: now, LastLoginAt: now,
			}
			if _, err := tx.NewInsert().Model(&id).Exec(ctx); err != nil {
				if store.IsUniqueViolation(err) {
					return errOIDCRace
				}
				return err
			}
		default:
			return err
		}
		if acct, err = loadAccount(ctx, tx, u); err != nil {
			return err
		}
		if sess, err = createSession(ctx, tx, u.ID); err != nil {
			return err
		}
		return writeAudit(ctx, tx, auditEntry{
			actorKind: actorUser, actorID: u.ID, actorName: u.Username,
			action: "session.login", resourceKind: "user", resourceID: u.ID.String(),
			details: map[string]any{"method": "oidc", "issuer": claims.Issuer, "created": created, "is_admin": u.IsAdmin},
		})
	})
	return acct, sess, err
}

// usernameBase derives a valid username from the ID token: the configured
// claim, else the local part of the email, else "user". Characters the
// Username schema does not allow are dropped.
func usernameBase(c auth.OIDCClaims) string {
	for _, candidate := range []string{c.Username, strings.SplitN(c.Email, "@", 2)[0]} {
		var b strings.Builder
		for _, r := range candidate {
			if r < utf8.RuneSelf && (r == '.' || r == '_' || r == '-' ||
				(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
				b.WriteRune(r)
			}
		}
		s := strings.TrimLeft(b.String(), "._-")
		if len(s) > 32 {
			s = s[:32]
		}
		if usernamePattern.MatchString(s) {
			return s
		}
	}
	return "user"
}

// freeUsername returns base, or base with the smallest numeric suffix that
// no account uses, compared case-insensitively. The result stays within
// 32 characters.
func freeUsername(ctx context.Context, tx bun.IDB, base string) (string, error) {
	for n := 1; n < 10_000; n++ {
		candidate := base
		if n > 1 {
			suffix := strconv.Itoa(n)
			b := base
			if len(b)+len(suffix) > 32 {
				b = b[:32-len(suffix)]
			}
			candidate = b + suffix
		}
		taken, err := tx.NewSelect().Model((*store.User)(nil)).Where("lower(username) = lower(?)", candidate).Exists(ctx)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no free username for %q", base)
}

func truncateRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
