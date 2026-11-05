package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Admin implements account administration and deployment settings. Every
// operation is for platform admins only; being an admin grants nothing
// inside other people's projects (tech design 12.2).
type Admin struct {
	db       *store.DB
	accounts *Accounts
	log      *slog.Logger
}

// NewAdmin returns the admin service. It shares the account service's
// settings cache, so a change applies at once on this replica.
func NewAdmin(accounts *Accounts, log *slog.Logger) *Admin {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Admin{db: accounts.db, accounts: accounts, log: log}
}

// AdminUser is an account as an admin sees it.
type AdminUser struct {
	User store.User
	// SSO reports whether the account has a linked OIDC identity.
	SSO bool
}

// UserFilter selects accounts. Zero values do not filter.
type UserFilter struct {
	// Query matches part of the username, display name or email.
	Query string
	// Status is "active", "disabled" or empty.
	Status string
	Admin  *bool
	Limit  *int
	Cursor string
}

// ListUsers returns accounts by username.
func (a *Admin) ListUsers(ctx context.Context, p *auth.Principal, f UserFilter) ([]AdminUser, string, error) {
	if err := requireAdmin(p); err != nil {
		return nil, "", err
	}
	var v validation
	limit := pageLimit(&v, f.Limit)
	offset, err := decodeOffset(f.Cursor)
	if err != nil {
		v.param("cursor", "游标无效，请使用上一页响应中的 next_cursor")
	}
	if err := v.err(); err != nil {
		return nil, "", err
	}
	q := a.db.NewSelect().Model((*store.User)(nil))
	if s := strings.TrimSpace(f.Query); s != "" {
		pattern := "%" + likeEscape(strings.ToLower(s)) + "%"
		q = q.WhereGroup(" AND ", func(sq *bun.SelectQuery) *bun.SelectQuery {
			return sq.Where(`lower(username) LIKE ? ESCAPE '\'`, pattern).
				WhereOr(`lower(display_name) LIKE ? ESCAPE '\'`, pattern).
				WhereOr(`lower(email) LIKE ? ESCAPE '\'`, pattern)
		})
	}
	switch f.Status {
	case "active":
		q = q.Where("disabled_at IS NULL")
	case "disabled":
		q = q.Where("disabled_at IS NOT NULL")
	}
	if f.Admin != nil {
		q = q.Where("is_admin = ?", *f.Admin)
	}
	var users []store.User
	if err := q.OrderExpr(a.db.Native().Bytewise("lower(username)")+" ASC, id ASC").Offset(int64(offset)).Limit(int64(limit)+1).Scan(ctx, &users); err != nil {
		return nil, "", err
	}
	next := ""
	if len(users) > limit {
		users = users[:limit]
		next = encodeOffset(offset + limit)
	}
	out, err := a.withSSO(ctx, a.db, users)
	return out, next, err
}

func (a *Admin) withSSO(ctx context.Context, idb bun.IDB, users []store.User) ([]AdminUser, error) {
	out := make([]AdminUser, len(users))
	if len(users) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(users))
	for i, u := range users {
		ids[i] = u.ID
	}
	var linked []uuid.UUID
	if err := idb.NewSelect().Model((*store.Identity)(nil)).Column("user_id").
		Where("user_id IN (?)", bun.List(ids)).Scan(ctx, &linked); err != nil {
		return nil, err
	}
	sso := map[uuid.UUID]bool{}
	for _, id := range linked {
		sso[id] = true
	}
	for i, u := range users {
		out[i] = AdminUser{User: u, SSO: sso[u.ID]}
	}
	return out, nil
}

// AdminCreateUserInput is a built-in account an admin creates.
type AdminCreateUserInput struct {
	RegisterInput
	IsAdmin bool
}

// CreateUser creates a built-in account and its personal project. It works
// with open registration closed.
func (a *Admin) CreateUser(ctx context.Context, p *auth.Principal, in AdminCreateUserInput) (AdminUser, error) {
	if err := requireAdmin(p); err != nil {
		return AdminUser{}, err
	}
	reg, err := checkNewAccount(in.RegisterInput)
	if err != nil {
		return AdminUser{}, err
	}
	hash, err := auth.HashPassword(ctx, reg.Password)
	if err != nil {
		return AdminUser{}, err
	}
	var acct Account
	err = a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if acct, err = createAccount(ctx, tx, newUser{
			username: reg.Username, displayName: reg.DisplayName, email: reg.Email,
			passwordHash: hash, isAdmin: in.IsAdmin,
		}); err != nil {
			return err
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "user.create", resourceKind: "user", resourceID: acct.User.ID.String(),
			details: map[string]any{"username": acct.User.Username, "is_admin": in.IsAdmin},
		})
	})
	if err != nil {
		return AdminUser{}, err
	}
	return AdminUser{User: acct.User}, nil
}

// AdminUpdateUserInput changes an account. Nil fields stay as they are.
type AdminUpdateUserInput struct {
	IsAdmin     *bool
	Disabled    *bool
	DisplayName *string
}

// UpdateUser grants or removes admin, disables or restores an account, or
// renames it. Disabling takes effect at once: the account's sessions and
// device sessions are ended and its access tokens revoked, so restoring it
// later does not bring old credentials back. Admins cannot disable
// themselves or drop their own admin role, so the deployment keeps an
// admin.
func (a *Admin) UpdateUser(ctx context.Context, p *auth.Principal, id uuid.UUID, in AdminUpdateUserInput) (AdminUser, error) {
	if err := requireAdmin(p); err != nil {
		return AdminUser{}, err
	}
	var v validation
	if id == p.UserID && in.Disabled != nil && *in.Disabled {
		v.content("#/disabled", "不能停用自己的账号")
	}
	if id == p.UserID && in.IsAdmin != nil && !*in.IsAdmin {
		v.content("#/is_admin", "不能取消自己的管理员身份")
	}
	var name string
	if in.DisplayName != nil {
		name = strings.TrimSpace(*in.DisplayName)
		checkDisplayName(&v, name)
	}
	if in.IsAdmin == nil && in.Disabled == nil && in.DisplayName == nil {
		v.body("", "至少给出一个要修改的字段")
	}
	if err := v.err(); err != nil {
		return AdminUser{}, err
	}
	var out AdminUser
	err := a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var u store.User
		if err := tx.NewSelect().Model(&u).Where("id = ?", id).Scan(ctx); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		now := store.Now()
		details := map[string]any{"username": u.Username}
		q := tx.NewUpdate().Model((*store.User)(nil)).Where("id = ?", id).Set("updated_at = ?", now)
		if in.IsAdmin != nil && *in.IsAdmin != u.IsAdmin {
			q = q.Set("is_admin = ?", *in.IsAdmin)
			details["is_admin_before"], details["is_admin"] = u.IsAdmin, *in.IsAdmin
			u.IsAdmin = *in.IsAdmin
		}
		if in.DisplayName != nil && name != u.DisplayName {
			q = q.Set("display_name = ?", name)
			details["display_name_changed"] = true
			u.DisplayName = name
		}
		if in.Disabled != nil && *in.Disabled != !u.DisabledAt.IsZero() {
			if *in.Disabled {
				q = q.Set("disabled_at = ?", now)
				u.DisabledAt = now
				ended, revoked, err := cutOff(ctx, tx, id, now)
				if err != nil {
					return err
				}
				details["disabled"], details["sessions_ended"], details["tokens_revoked"] = true, ended, revoked
			} else {
				q = q.Set("disabled_at = NULL")
				u.DisabledAt = time.Time{}
				details["disabled"] = false
			}
		}
		if _, err := q.Exec(ctx); err != nil {
			return err
		}
		kind, actor := actorOf(p)
		if err := writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "user.admin_update", resourceKind: "user", resourceID: id.String(), details: details,
		}); err != nil {
			return err
		}
		users, err := a.withSSO(ctx, tx, []store.User{u})
		if err != nil {
			return err
		}
		out = users[0]
		return nil
	})
	return out, err
}

// cutOff ends every session and device session of a user, revokes their
// access tokens and, through endSessions, drops unused pairing codes.
func cutOff(ctx context.Context, tx bun.IDB, userID uuid.UUID, now time.Time) (sessions, tokens int64, err error) {
	if sessions, err = endSessions(ctx, tx, userID, uuid.Nil); err != nil {
		return 0, 0, err
	}
	res, err := tx.NewUpdate().Model((*store.AccessToken)(nil)).Set("revoked_at = ?", now).
		Where("user_id = ? AND revoked_at IS NULL", userID).Exec(ctx)
	if err != nil {
		return 0, 0, err
	}
	tokens, _ = res.RowsAffected()
	return sessions, tokens, nil
}

// ResetPassword sets a new password for an account and ends its sessions
// and device sessions; access tokens stay. An account that only used
// single sign-on gets a password this way.
func (a *Admin) ResetPassword(ctx context.Context, p *auth.Principal, id uuid.UUID, password string) error {
	if err := requireAdmin(p); err != nil {
		return err
	}
	var v validation
	checkPassword(&v, "#/password", password)
	if err := v.err(); err != nil {
		return err
	}
	hash, err := auth.HashPassword(ctx, password)
	if err != nil {
		return err
	}
	return a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.NewUpdate().Model((*store.User)(nil)).
			Set("password_hash = ?", hash).Set("updated_at = ?", store.Now()).Where("id = ?", id).Exec(ctx)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		ended, err := endSessions(ctx, tx, id, uuid.Nil)
		if err != nil {
			return err
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "user.reset_password", resourceKind: "user", resourceID: id.String(),
			details: map[string]any{"sessions_ended": ended},
		})
	})
}

// OIDCInfo describes the configured single sign-on provider.
type OIDCInfo struct {
	Issuer, ClientID, UsernameClaim, CallbackURL string
	UserCount                                    int
}

// SettingsView is the settings page: the stored settings, who changed them
// last, and the provider.
type SettingsView struct {
	Settings  DeploymentSettings
	UpdatedBy *store.User
	OIDC      *OIDCInfo
}

// Settings returns the deployment settings.
func (a *Admin) Settings(ctx context.Context, p *auth.Principal) (SettingsView, error) {
	if err := requireAdmin(p); err != nil {
		return SettingsView{}, err
	}
	s, err := loadSettings(ctx, a.db)
	if err != nil {
		return SettingsView{}, err
	}
	return a.view(ctx, s)
}

func (a *Admin) view(ctx context.Context, s DeploymentSettings) (SettingsView, error) {
	out := SettingsView{Settings: s}
	if s.UpdatedBy != uuid.Nil {
		var u store.User
		if err := a.db.NewSelect().Model(&u).Where("id = ?", s.UpdatedBy).Scan(ctx); err == nil {
			out.UpdatedBy = &u
		} else if !errors.Is(err, sql.ErrNoRows) {
			return SettingsView{}, err
		}
	}
	if o := a.accounts.oidc; o != nil {
		var n int
		if err := a.db.NewSelect().Model((*store.Identity)(nil)).ColumnExpr("count(DISTINCT user_id)").
			Where("issuer = ?", o.Issuer()).Scan(ctx, &n); err != nil {
			return SettingsView{}, err
		}
		out.OIDC = &OIDCInfo{
			Issuer: o.Issuer(), ClientID: o.ClientID(), UsernameClaim: o.UsernameClaim(),
			CallbackURL: o.RedirectURL(), UserCount: n,
		}
	}
	return out, nil
}

// SettingsInput changes settings. Nil fields stay as they are.
type SettingsInput struct {
	OpenRegistration   *bool
	LocalPasswordLogin *bool
}

// UpdateSettings changes the deployment settings. Local password login can
// only be turned off when single sign-on is configured, so nobody is
// locked out; admins keep password login in any case.
func (a *Admin) UpdateSettings(ctx context.Context, p *auth.Principal, in SettingsInput) (SettingsView, error) {
	if err := requireAdmin(p); err != nil {
		return SettingsView{}, err
	}
	var v validation
	if in.LocalPasswordLogin != nil && !*in.LocalPasswordLogin && a.accounts.oidc == nil {
		v.content("#/local_password_login", "没有配置单点登录时不能关闭本地密码登录")
	}
	if in.OpenRegistration == nil && in.LocalPasswordLogin == nil {
		v.body("", "至少给出一个要修改的设置")
	}
	if err := v.err(); err != nil {
		return SettingsView{}, err
	}
	var after DeploymentSettings
	err := a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		before, err := loadSettings(ctx, tx)
		if err != nil {
			return err
		}
		now := store.Now()
		details := map[string]any{}
		if in.OpenRegistration != nil {
			if err := putSetting(ctx, tx, store.SettingOpenRegistration, *in.OpenRegistration, p.UserID, now); err != nil {
				return err
			}
			details["open_registration_before"], details["open_registration"] = before.OpenRegistration, *in.OpenRegistration
		}
		if in.LocalPasswordLogin != nil {
			if err := putSetting(ctx, tx, store.SettingLocalPasswordLogin, *in.LocalPasswordLogin, p.UserID, now); err != nil {
				return err
			}
			details["local_password_login_before"], details["local_password_login"] = before.LocalPasswordLogin, *in.LocalPasswordLogin
		}
		kind, actor := actorOf(p)
		if err := writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "settings.update", resourceKind: "setting", details: details,
		}); err != nil {
			return err
		}
		after, err = loadSettings(ctx, tx)
		return err
	})
	if err != nil {
		return SettingsView{}, err
	}
	a.accounts.settings.invalidate()
	return a.view(ctx, after)
}
