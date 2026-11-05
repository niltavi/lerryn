package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Account rules (tech design 12.1).
const (
	// SessionTTL is how long a login session lasts.
	SessionTTL = 30 * 24 * time.Hour

	// loginAttempts failed logins per account or per client address within
	// loginWindow block further attempts until the window ends.
	loginAttempts = 10
	loginWindow   = time.Minute

	// registrationAttempts registrations per client address within
	// loginWindow are accepted; further ones are refused until the window
	// ends. The window matches logins so the rate_limited message, which
	// says to wait a minute, holds for both.
	registrationAttempts = 5

	// touchInterval limits how often last_used_at and last_seen_at are
	// written, so that authenticating is not a write on every request.
	touchInterval = time.Minute

	minPasswordRunes = 8
	maxPasswordRunes = 256
	maxDisplayRunes  = 64
	maxEmailBytes    = 254
	maxTokenName     = 100
	maxTokenDays     = 3650

	defaultPageSize = 50
	maxPageSize     = 200
)

// usernamePattern matches the Username schema. ASCII only: lower() folds
// Unicode in PostgreSQL but only ASCII in SQLite, and the unique index on
// lower(username) must mean the same in both.
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{1,31}$`)

// Accounts implements built-in accounts, single sign-on, login sessions,
// mobile device sessions and access tokens.
type Accounts struct {
	db       *store.DB
	log      *slog.Logger
	logins   *auth.Limiter
	pairings *auth.Limiter
	// registrations limits sign-ups per client address.
	registrations *auth.Limiter
	settings      *settingsCache
	// oidc is the single sign-on provider; nil when none is configured.
	oidc *auth.OIDC
	// publicURL is server.public_url, which pairing codes point at.
	publicURL string
}

// AccountOption configures the account service.
type AccountOption func(*Accounts)

// WithOIDC enables single sign-on with o.
func WithOIDC(o *auth.OIDC) AccountOption {
	return func(a *Accounts) { a.oidc = o }
}

// WithPublicURL sets server.public_url, which pairing QR codes carry and
// pairing exchanges are checked against.
func WithPublicURL(u string) AccountOption {
	return func(a *Accounts) { a.publicURL = strings.TrimRight(u, "/") }
}

// NewAccounts returns the account service.
func NewAccounts(db *store.DB, log *slog.Logger, opts ...AccountOption) *Accounts {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	a := &Accounts{
		db:            db,
		log:           log,
		logins:        auth.NewLimiter(loginAttempts, loginWindow),
		pairings:      auth.NewLimiter(loginAttempts, loginWindow),
		registrations: auth.NewLimiter(registrationAttempts, loginWindow),
		settings:      newSettingsCache(db),
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Account is a user with the facts the API reports about them.
type Account struct {
	User              store.User
	PersonalProjectID uuid.UUID
	// Identities are the user's linked OIDC identities.
	Identities []store.Identity
}

// NewSession is a session that was just created. Token is returned to the
// client once, as a cookie, and never stored.
type NewSession struct {
	Token     string
	ExpiresAt time.Time
	// ID is the session's row ID.
	ID uuid.UUID
}

// SignupState reports whether open registration is on and whether the
// deployment has no account yet, for discover.
func (a *Accounts) SignupState(ctx context.Context) (open, first bool, err error) {
	s, err := a.settings.get(ctx)
	if err != nil {
		return false, false, err
	}
	exists, err := a.db.NewSelect().Model((*store.User)(nil)).Exists(ctx)
	if err != nil {
		return false, false, err
	}
	return s.OpenRegistration, !exists, nil
}

// PasswordLoginForAll reports whether every user may sign in with a
// password. When an admin turned local password login off, only admins
// still may (tech design 12.1).
func (a *Accounts) PasswordLoginForAll(ctx context.Context) (bool, error) {
	s, err := a.settings.get(ctx)
	if err != nil {
		return false, err
	}
	return s.LocalPasswordLogin, nil
}

// RegisterInput is a registration request.
type RegisterInput struct {
	Username    string
	Password    string
	DisplayName string
	Email       string
}

// Register creates a built-in account and logs it in. The first account of
// a deployment becomes an admin. Every account gets a personal project
// that it owns. When an admin has closed open registration, only the first
// account of an empty deployment can still register.
func (a *Accounts) Register(ctx context.Context, in RegisterInput) (Account, NewSession, error) {
	in, err := checkNewAccount(in)
	if err != nil {
		return Account{}, NewSession{}, err
	}
	// Every attempt counts, not only failures: a registration that
	// succeeds is exactly what a mass sign-up does, and each one costs a
	// password hash.
	if ip := auth.ClientFrom(ctx).IP; ip != "" && !a.registrations.Allow("ip:"+ip) {
		auditFailure(ctx, a.db, a.log, auditEntry{
			actorKind: actorAnonymous, action: "user.register", resourceKind: "user",
			details: map[string]any{"username": in.Username, "reason": "rate_limited"},
		})
		return Account{}, NewSession{}, ErrRateLimited
	}
	settings, err := a.settings.get(ctx)
	if err != nil {
		return Account{}, NewSession{}, err
	}
	// Hash before opening the transaction: it is the slow part.
	hash, err := auth.HashPassword(ctx, in.Password)
	if err != nil {
		return Account{}, NewSession{}, err
	}

	var (
		acct Account
		sess NewSession
	)
	err = a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		users, err := countUsersLocked(ctx, a.db, tx)
		if err != nil {
			return err
		}
		if users > 0 && !settings.OpenRegistration {
			return ErrRegistrationDisabled
		}
		if acct, err = createAccount(ctx, tx, newUser{
			username: in.Username, displayName: in.DisplayName, email: in.Email,
			passwordHash: hash, isAdmin: users == 0,
		}); err != nil {
			return err
		}
		if sess, err = createSession(ctx, tx, acct.User.ID); err != nil {
			return err
		}
		return writeAudit(ctx, tx, auditEntry{
			actorKind: actorUser, actorID: acct.User.ID, actorName: acct.User.Username,
			action: "user.register", resourceKind: "user", resourceID: acct.User.ID.String(),
			result: resultSuccess, details: map[string]any{"is_admin": acct.User.IsAdmin},
		})
	})
	if errors.Is(err, ErrRegistrationDisabled) {
		auditFailure(ctx, a.db, a.log, auditEntry{
			actorKind: actorAnonymous, action: "user.register", resourceKind: "user",
			details: map[string]any{"username": in.Username, "reason": "registration_disabled"},
		})
	}
	if err != nil {
		return Account{}, NewSession{}, err
	}
	return acct, sess, nil
}

// countUsersLocked counts the accounts in tx after taking the accounts
// lock, so that the "first account becomes an admin" rule holds when
// registrations, single sign-on and the initial admin race on an empty
// deployment: the second of them waits for the first to commit and then
// counts its account.
func countUsersLocked(ctx context.Context, db *store.DB, tx bun.IDB) (int64, error) {
	if err := db.Native().LockAccounts(ctx, tx); err != nil {
		return 0, err
	}
	n, err := tx.NewSelect().Model((*store.User)(nil)).Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// checkNewAccount validates and normalises the input of a new built-in
// account.
func checkNewAccount(in RegisterInput) (RegisterInput, error) {
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.Email = strings.TrimSpace(in.Email)
	var v validation
	if !usernamePattern.MatchString(in.Username) {
		v.body("#/username", "用户名为 2 到 32 个字符，只能包含 ASCII 字母、数字、点、下划线和连字符，并以字母或数字开头")
	}
	checkPassword(&v, "#/password", in.Password)
	checkDisplayName(&v, in.DisplayName)
	checkEmail(&v, in.Email)
	return in, v.err()
}

func checkDisplayName(v *validation, name string) {
	if utf8.RuneCountInString(name) > maxDisplayRunes {
		v.body("#/display_name", fmt.Sprintf("显示名最多 %d 个字符", maxDisplayRunes))
	}
}

func checkEmail(v *validation, email string) {
	if email == "" {
		return
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || len(email) > maxEmailBytes {
		v.body("#/email", "邮箱格式不正确")
	}
}

// newUser is an account to create.
type newUser struct {
	username, displayName, email, passwordHash string
	isAdmin                                    bool
}

// createAccount inserts a user, its personal project and the owner role
// binding in tx. A taken username is ErrUsernameTaken.
func createAccount(ctx context.Context, tx bun.IDB, nu newUser) (Account, error) {
	now := store.Now()
	u := store.User{
		ID:           store.NewID(),
		Username:     nu.username,
		DisplayName:  nu.displayName,
		Email:        nu.email,
		PasswordHash: nu.passwordHash,
		IsAdmin:      nu.isAdmin,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if _, err := tx.NewInsert().Model(&u).Exec(ctx); err != nil {
		if store.IsUniqueViolation(err) {
			return Account{}, ErrUsernameTaken
		}
		return Account{}, err
	}
	project := store.Project{
		ID:          store.NewID(),
		Name:        nu.username,
		OwnerUserID: u.ID,
		IsPersonal:  true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if _, err := tx.NewInsert().Model(&project).Exec(ctx); err != nil {
		return Account{}, err
	}
	if _, err := tx.NewInsert().Model(&store.RoleBinding{
		ID:        store.NewID(),
		ProjectID: project.ID,
		UserID:    u.ID,
		Role:      RoleOwner,
		CreatedAt: now,
		UpdatedAt: now,
	}).Exec(ctx); err != nil {
		return Account{}, err
	}
	return Account{User: u, PersonalProjectID: project.ID, Identities: []store.Identity{}}, nil
}

// DeviceInfo describes the mobile device of a read-only device session.
type DeviceInfo struct {
	Name      string
	Platform  string
	OSVersion string
}

// Login checks a username and password and opens a web session. Unknown
// users, wrong passwords, disabled accounts and accounts without a password
// all return ErrUnauthenticated and take about the same time.
func (a *Accounts) Login(ctx context.Context, username, password string) (Account, NewSession, error) {
	return a.LoginDevice(ctx, username, password, nil)
}

// LoginDevice is Login for the mobile app: with a device it opens a
// read-only device session instead of a web session. When an admin has
// turned off local password login, only admins may log in with a password;
// everyone else gets ErrPasswordLoginDisabled, and only after the password
// was found correct, so the answer tells a guesser nothing.
func (a *Accounts) LoginDevice(ctx context.Context, username, password string, device *DeviceInfo) (Account, NewSession, error) {
	keys := []string{"user:" + strings.ToLower(username)}
	if ip := auth.ClientFrom(ctx).IP; ip != "" {
		keys = append(keys, "ip:"+ip)
	}
	// The attempt is counted before the password is checked and taken
	// back once it proves right. Counting only after a failure would let
	// a burst of parallel guesses all pass the check before any of them
	// was recorded, each holding an Argon2id computation.
	if !a.logins.Allow(keys...) {
		auditFailure(ctx, a.db, a.log, auditEntry{
			actorKind: actorAnonymous, action: "session.login", resourceKind: "user",
			details: map[string]any{"username": username, "reason": "rate_limited"},
		})
		return Account{}, NewSession{}, ErrRateLimited
	}
	failure := func(reason string, userID uuid.UUID) {
		e := auditEntry{
			actorKind: actorAnonymous, action: "session.login", resourceKind: "user",
			details: map[string]any{"username": username, "reason": reason},
		}
		if userID != uuid.Nil {
			e.actorKind, e.actorID, e.resourceID = actorUser, userID, userID.String()
		}
		auditFailure(ctx, a.db, a.log, e)
	}
	// fail leaves the attempt counted.
	fail := func(reason string, userID uuid.UUID) (Account, NewSession, error) {
		failure(reason, userID)
		return Account{}, NewSession{}, ErrUnauthenticated
	}

	var u store.User
	err := a.db.NewSelect().Model(&u).Where("lower(username) = lower(?)", username).Scan(ctx)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		auth.VerifyDummy(ctx, password)
		return fail("unknown_user", uuid.Nil)
	case err != nil:
		return Account{}, NewSession{}, err
	}
	if u.PasswordHash == "" {
		auth.VerifyDummy(ctx, password)
		return fail("no_password", u.ID)
	}
	ok, err := auth.VerifyPassword(ctx, u.PasswordHash, password)
	if err != nil {
		return Account{}, NewSession{}, fmt.Errorf("verify password of user %s: %w", u.ID, err)
	}
	if !ok {
		return fail("wrong_password", u.ID)
	}
	if !u.DisabledAt.IsZero() {
		return fail("disabled", u.ID)
	}
	// The password is right: this was not a guess, so it does not count
	// towards the limit, as before the attempt was counted up front.
	a.logins.Forgive(keys...)
	settings, err := a.settings.get(ctx)
	if err != nil {
		return Account{}, NewSession{}, err
	}
	if !settings.LocalPasswordLogin && !u.IsAdmin {
		failure("password_login_disabled", u.ID)
		return Account{}, NewSession{}, ErrPasswordLoginDisabled
	}

	var (
		acct Account
		sess NewSession
	)
	err = a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if acct, err = loadAccount(ctx, tx, u); err != nil {
			return err
		}
		details := map[string]any{"method": "password"}
		if device != nil {
			sess, err = createDeviceSession(ctx, tx, u.ID, *device)
			details["device"] = device.Name
		} else {
			sess, err = createSession(ctx, tx, u.ID)
		}
		if err != nil {
			return err
		}
		return writeAudit(ctx, tx, auditEntry{
			actorKind: actorUser, actorID: u.ID, actorName: u.Username,
			action: "session.login", resourceKind: "user", resourceID: u.ID.String(),
			result: resultSuccess, details: details,
		})
	})
	if err != nil {
		return Account{}, NewSession{}, err
	}
	return acct, sess, nil
}

// Logout ends the session that authenticated p. A read-only device session
// may end itself too.
func (a *Accounts) Logout(ctx context.Context, p *auth.Principal) error {
	if p.Method != auth.MethodSession {
		return ErrForbidden
	}
	return a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewDelete().Model((*store.Session)(nil)).Where("id = ?", p.CredentialID).Exec(ctx); err != nil {
			return err
		}
		return writeAudit(ctx, tx, auditEntry{
			actorKind: actorUser, actorID: p.UserID,
			action: "session.logout", resourceKind: "session", resourceID: p.CredentialID.String(),
			result: resultSuccess,
		})
	})
}

// Authenticate resolves an access token or a session token to a principal.
// It returns ErrUnauthenticated for anything that is not a live credential
// of an enabled user.
func (a *Accounts) Authenticate(ctx context.Context, token string) (*auth.Principal, error) {
	switch {
	case strings.HasPrefix(token, auth.AccessTokenPrefix):
		return a.authenticateToken(ctx, token)
	case strings.HasPrefix(token, auth.SessionTokenPrefix):
		return a.authenticateSession(ctx, token)
	default:
		return nil, ErrUnauthenticated
	}
}

func (a *Accounts) authenticateToken(ctx context.Context, token string) (*auth.Principal, error) {
	var t store.AccessToken
	err := a.db.NewSelect().Model(&t).Where("token_hash = ?", auth.HashToken(token)).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	now := store.Now()
	if !t.RevokedAt.IsZero() || (!t.ExpiresAt.IsZero() && !now.Before(t.ExpiresAt)) {
		return nil, ErrUnauthenticated
	}
	u, err := a.enabledUser(ctx, t.UserID)
	if err != nil {
		return nil, err
	}
	if now.Sub(t.LastUsedAt) >= touchInterval {
		a.touch(ctx, (*store.AccessToken)(nil), "last_used_at", t.ID, now)
		a.touch(ctx, (*store.User)(nil), "last_seen_at", u.ID, now)
	}
	scopes := make([]auth.Scope, 0, len(t.Scopes))
	for _, s := range auth.AllScopes {
		for _, have := range t.Scopes {
			if string(s) == have {
				scopes = append(scopes, s)
			}
		}
	}
	p, err := a.principal(ctx, u, auth.MethodAccessToken, scopes, t.ID)
	if err != nil {
		return nil, err
	}
	p.CredentialName = t.Name
	return p, nil
}

func (a *Accounts) authenticateSession(ctx context.Context, token string) (*auth.Principal, error) {
	var s store.Session
	err := a.db.NewSelect().Model(&s).Where("token_hash = ?", auth.HashToken(token)).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	now := store.Now()
	if !now.Before(s.ExpiresAt) {
		return nil, ErrUnauthenticated
	}
	u, err := a.enabledUser(ctx, s.UserID)
	if err != nil {
		return nil, err
	}
	if now.Sub(s.LastSeenAt) >= touchInterval {
		a.touch(ctx, (*store.Session)(nil), "last_seen_at", s.ID, now)
		a.touch(ctx, (*store.User)(nil), "last_seen_at", u.ID, now)
	}
	switch s.Kind {
	case store.SessionWeb:
		return a.principal(ctx, u, auth.MethodSession, append([]auth.Scope(nil), auth.AllScopes...), s.ID)
	case store.SessionMobile:
		// Device sessions are read-only (tech design 12.1): with only the
		// read scope, every mutating operation is refused by the same
		// check that limits access tokens.
		p, err := a.principal(ctx, u, auth.MethodSession, []auth.Scope{auth.ScopeRead}, s.ID)
		if err != nil {
			return nil, err
		}
		p.ReadOnly = true
		return p, nil
	default:
		// An unknown kind grants nothing, like an unknown role.
		return nil, ErrUnauthenticated
	}
}

// enabledUser loads a user and returns ErrUnauthenticated when the account
// is gone or disabled, so a disabled user's credentials stop working at once.
func (a *Accounts) enabledUser(ctx context.Context, id uuid.UUID) (store.User, error) {
	var u store.User
	err := a.db.NewSelect().Model(&u).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return store.User{}, ErrUnauthenticated
	}
	if err != nil {
		return store.User{}, err
	}
	if !u.DisabledAt.IsZero() {
		return store.User{}, ErrUnauthenticated
	}
	return u, nil
}

// touch records that a credential was used. Failing to record it must not
// fail the request, so the error is only logged.
func (a *Accounts) touch(ctx context.Context, model any, column string, id uuid.UUID, now time.Time) {
	if _, err := a.db.NewUpdate().Model(model).Set(column+" = ?", now).Where("id = ?", id).Exec(ctx); err != nil {
		a.log.WarnContext(ctx, "record credential use", "column", column, "error", err)
	}
}

// Me returns the account of p.
func (a *Accounts) Me(ctx context.Context, p *auth.Principal) (Account, error) {
	var u store.User
	err := a.db.NewSelect().Model(&u).Where("id = ?", p.UserID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	return loadAccount(ctx, a.db, u)
}

// loadAccount completes u with its personal project and identities.
func loadAccount(ctx context.Context, idb bun.IDB, u store.User) (Account, error) {
	projectID, err := personalProject(ctx, idb, u.ID)
	if err != nil {
		return Account{}, err
	}
	ids := []store.Identity{}
	if err := idb.NewSelect().Model(&ids).Where("user_id = ?", u.ID).Order("created_at ASC").Scan(ctx); err != nil {
		return Account{}, fmt.Errorf("identities of user %s: %w", u.ID, err)
	}
	return Account{User: u, PersonalProjectID: projectID, Identities: ids}, nil
}

// CreateTokenInput is a request for a new access token. A nil
// ExpiresInDays means the token does not expire.
type CreateTokenInput struct {
	Name          string
	Scopes        []string
	ExpiresInDays *int
}

// CreateToken creates an access token for p and returns it once. Only a
// web login session may create tokens, so a leaked token cannot mint new
// ones and a read-only device cannot gain write access.
func (a *Accounts) CreateToken(ctx context.Context, p *auth.Principal, in CreateTokenInput) (string, store.AccessToken, error) {
	if p.Method != auth.MethodSession || p.ReadOnly {
		return "", store.AccessToken{}, ErrForbidden
	}
	in.Name = strings.TrimSpace(in.Name)
	var v validation
	if n := utf8.RuneCountInString(in.Name); n == 0 || n > maxTokenName {
		v.body("#/name", fmt.Sprintf("名字为 1 到 %d 个字符", maxTokenName))
	}
	if len(in.Scopes) == 0 {
		v.body("#/scopes", "至少选择一个范围")
	}
	seen := map[string]bool{}
	for i, s := range in.Scopes {
		switch {
		case !auth.ValidScope(s):
			v.body(fmt.Sprintf("#/scopes/%d", i), "范围只能是 read、write 或 execute")
		case seen[s]:
			v.body(fmt.Sprintf("#/scopes/%d", i), "范围重复")
		}
		seen[s] = true
	}
	if d := in.ExpiresInDays; d != nil && (*d < 1 || *d > maxTokenDays) {
		v.body("#/expires_in_days", fmt.Sprintf("有效天数为 1 到 %d", maxTokenDays))
	}
	if err := v.err(); err != nil {
		return "", store.AccessToken{}, err
	}

	token, hash, err := auth.NewToken(auth.AccessTokenPrefix)
	if err != nil {
		return "", store.AccessToken{}, err
	}
	now := store.Now()
	row := store.AccessToken{
		ID:        store.NewID(),
		UserID:    p.UserID,
		Name:      in.Name,
		TokenHash: hash,
		Prefix:    auth.DisplayPrefix(token),
		Scopes:    in.Scopes,
		CreatedAt: now,
	}
	if in.ExpiresInDays != nil {
		row.ExpiresAt = now.Add(time.Duration(*in.ExpiresInDays) * 24 * time.Hour)
	}
	err = a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewInsert().Model(&row).Exec(ctx); err != nil {
			return err
		}
		return writeAudit(ctx, tx, auditEntry{
			actorKind: actorUser, actorID: p.UserID,
			action: "token.create", resourceKind: "access_token", resourceID: row.ID.String(),
			result: resultSuccess, details: map[string]any{"name": row.Name, "scopes": row.Scopes},
		})
	})
	if err != nil {
		return "", store.AccessToken{}, err
	}
	return token, row, nil
}

// ListTokens returns p's access tokens, newest first. A nil limit means the
// default page size. cursor is the opaque value of a previous page; the
// returned cursor is empty on the last page.
func (a *Accounts) ListTokens(ctx context.Context, p *auth.Principal, limitParam *int, cursor string) ([]store.AccessToken, string, error) {
	var v validation
	limit := pageLimit(&v, limitParam)
	after, err := decodeCursor(cursor)
	if err != nil {
		v.param("cursor", "游标无效，请使用上一页响应中的 next_cursor")
	}
	if err := v.err(); err != nil {
		return nil, "", err
	}

	// UUIDv7 IDs sort by creation time, so the ID is the keyset.
	q := a.db.NewSelect().Model((*store.AccessToken)(nil)).
		Where("user_id = ?", p.UserID).
		Order("id DESC").
		Limit(int64(limit) + 1)
	if after != uuid.Nil {
		q = q.Where("id < ?", after)
	}
	var rows []store.AccessToken
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, "", err
	}
	rows, next := page(rows, limit, func(t store.AccessToken) uuid.UUID { return t.ID })
	return rows, next, nil
}

// RevokeToken revokes one of p's access tokens. Revoking an already revoked
// token succeeds; another user's token is ErrNotFound.
func (a *Accounts) RevokeToken(ctx context.Context, p *auth.Principal, id uuid.UUID) error {
	return a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var t store.AccessToken
		err := tx.NewSelect().Model(&t).Where("id = ? AND user_id = ?", id, p.UserID).Scan(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !t.RevokedAt.IsZero() {
			return nil
		}
		if _, err := tx.NewUpdate().Model((*store.AccessToken)(nil)).
			Set("revoked_at = ?", store.Now()).Where("id = ?", id).Exec(ctx); err != nil {
			return err
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "token.revoke", resourceKind: "access_token", resourceID: id.String(),
			result: resultSuccess, details: map[string]any{"name": t.Name},
		})
	})
}

func checkPassword(v *validation, pointer, password string) {
	if n := utf8.RuneCountInString(password); n < minPasswordRunes || n > maxPasswordRunes {
		v.body(pointer, fmt.Sprintf("密码为 %d 到 %d 个字符", minPasswordRunes, maxPasswordRunes))
	}
}

// createSession opens a web console session.
func createSession(ctx context.Context, idb bun.IDB, userID uuid.UUID) (NewSession, error) {
	return insertSession(ctx, idb, store.Session{UserID: userID, Kind: store.SessionWeb})
}

// createDeviceSession opens a read-only session for a mobile device.
func createDeviceSession(ctx context.Context, idb bun.IDB, userID uuid.UUID, d DeviceInfo) (NewSession, error) {
	return insertSession(ctx, idb, store.Session{
		UserID: userID, Kind: store.SessionMobile,
		DeviceName: d.Name, DevicePlatform: d.Platform, DeviceOSVersion: d.OSVersion,
	})
}

func insertSession(ctx context.Context, idb bun.IDB, s store.Session) (NewSession, error) {
	token, hash, err := auth.NewToken(auth.SessionTokenPrefix)
	if err != nil {
		return NewSession{}, err
	}
	now := store.Now()
	client := auth.ClientFrom(ctx)
	s.ID = store.NewID()
	s.TokenHash = hash
	s.IP = client.IP
	s.UserAgent = client.UserAgent
	s.CreatedAt = now
	s.ExpiresAt = now.Add(SessionTTL)
	s.LastSeenAt = now
	if _, err := idb.NewInsert().Model(&s).Exec(ctx); err != nil {
		return NewSession{}, err
	}
	return NewSession{Token: token, ExpiresAt: s.ExpiresAt, ID: s.ID}, nil
}

// endSessions deletes the sessions of a user, web and device alike, except
// keep, and the user's unused pairing codes: a code is a device session
// waiting to be claimed, so one issued before a password reset or change
// would otherwise let whoever holds it in afterwards. Access tokens are not
// sessions and stay.
func endSessions(ctx context.Context, idb bun.IDB, userID, keep uuid.UUID) (int64, error) {
	q := idb.NewDelete().Model((*store.Session)(nil)).Where("user_id = ?", userID)
	if keep != uuid.Nil {
		q = q.Where("id <> ?", keep)
	}
	res, err := q.Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("end sessions of user %s: %w", userID, err)
	}
	n, _ := res.RowsAffected()
	if _, err := idb.NewDelete().Model((*store.PairingCode)(nil)).
		Where("user_id = ? AND used_at IS NULL", userID).Exec(ctx); err != nil {
		return 0, fmt.Errorf("drop pairing codes of user %s: %w", userID, err)
	}
	return n, nil
}

func personalProject(ctx context.Context, idb bun.IDB, userID uuid.UUID) (uuid.UUID, error) {
	var p store.Project
	err := idb.NewSelect().Model(&p).Column("id").
		Where("owner_user_id = ? AND is_personal = ?", userID, true).Scan(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("personal project of user %s: %w", userID, err)
	}
	return p.ID, nil
}

func (a *Accounts) principal(ctx context.Context, u store.User, method auth.Method, scopes []auth.Scope, credentialID uuid.UUID) (*auth.Principal, error) {
	projectID, err := personalProject(ctx, a.db, u.ID)
	if err != nil {
		return nil, err
	}
	return &auth.Principal{
		UserID:            u.ID,
		Username:          u.Username,
		DisplayName:       u.DisplayName,
		IsAdmin:           u.IsAdmin,
		PersonalProjectID: projectID,
		Method:            method,
		Scopes:            scopes,
		CredentialID:      credentialID,
	}, nil
}

func encodeCursor(id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id.String()))
}

func decodeCursor(s string) (uuid.UUID, error) {
	if s == "" {
		return uuid.Nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return uuid.Nil, err
	}
	return uuid.Parse(string(b))
}
