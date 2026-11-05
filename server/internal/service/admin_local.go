package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Account changes a maintainer makes on the server itself (tech design
// 12.1): `lerryn admin create`, `lerryn admin reset-password` and the
// initial admin from the configuration. They bypass the API, so there is
// no principal; whoever can run them can already read the database. The
// audit log records them as the system actor, with how they were made.

// Ways a local account change was made, recorded in its audit details.
const (
	ViaCLI          = "cli"
	ViaInitialAdmin = "initial_admin"
)

// ErrAccountsExist is returned by BootstrapAdmin when the deployment
// already has an account.
var ErrAccountsExist = errors.New("the deployment already has accounts")

// CreateUserLocally creates a built-in account and its personal project,
// as CreateUser does for an admin, with open registration closed or not.
func (a *Admin) CreateUserLocally(ctx context.Context, in AdminCreateUserInput, via string) (AdminUser, error) {
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
		acct, err = createLocal(ctx, tx, reg, hash, in.IsAdmin, via)
		return err
	})
	if err != nil {
		return AdminUser{}, err
	}
	return AdminUser{User: acct.User}, nil
}

func createLocal(ctx context.Context, tx bun.IDB, reg RegisterInput, hash string, isAdmin bool, via string) (Account, error) {
	acct, err := createAccount(ctx, tx, newUser{
		username: reg.Username, displayName: reg.DisplayName, email: reg.Email,
		passwordHash: hash, isAdmin: isAdmin,
	})
	if err != nil {
		return Account{}, err
	}
	return acct, writeAudit(ctx, tx, auditEntry{
		actorKind: actorSystem,
		action:    "user.create", resourceKind: "user", resourceID: acct.User.ID.String(),
		details: map[string]any{"username": acct.User.Username, "is_admin": isAdmin, "via": via},
	})
}

// BootstrapAdmin creates an admin account when the deployment has none
// yet; with any account present it changes nothing and returns
// ErrAccountsExist. The check and the insert share a transaction that
// holds the accounts lock, so it serialises with registration and single
// sign-on and there is only ever one first account.
func (a *Admin) BootstrapAdmin(ctx context.Context, in RegisterInput) (AdminUser, error) {
	reg, err := checkNewAccount(in)
	if err != nil {
		return AdminUser{}, err
	}
	hash, err := auth.HashPassword(ctx, reg.Password)
	if err != nil {
		return AdminUser{}, err
	}
	var acct Account
	err = a.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		users, err := countUsersLocked(ctx, a.db, tx)
		if err != nil {
			return err
		}
		if users > 0 {
			return ErrAccountsExist
		}
		acct, err = createLocal(ctx, tx, reg, hash, true, ViaInitialAdmin)
		return err
	})
	if err != nil {
		return AdminUser{}, err
	}
	return AdminUser{User: acct.User}, nil
}

// ResetPasswordLocally sets a new password for the account with the given
// username and ends its sessions and device sessions, as ResetPassword
// does for an admin; access tokens stay. A disabled account stays
// disabled. An unknown username is ErrNotFound.
func (a *Admin) ResetPasswordLocally(ctx context.Context, username, password string) (store.User, error) {
	var v validation
	checkPassword(&v, "#/password", password)
	if err := v.err(); err != nil {
		return store.User{}, err
	}
	hash, err := auth.HashPassword(ctx, password)
	if err != nil {
		return store.User{}, err
	}
	var u store.User
	err = a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := tx.NewSelect().Model(&u).Where("lower(username) = lower(?)", username).Scan(ctx); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := tx.NewUpdate().Model((*store.User)(nil)).
			Set("password_hash = ?", hash).Set("updated_at = ?", store.Now()).Where("id = ?", u.ID).Exec(ctx); err != nil {
			return err
		}
		ended, err := endSessions(ctx, tx, u.ID, uuid.Nil)
		if err != nil {
			return err
		}
		return writeAudit(ctx, tx, auditEntry{
			actorKind: actorSystem,
			action:    "user.reset_password", resourceKind: "user", resourceID: u.ID.String(),
			details: map[string]any{"username": u.Username, "sessions_ended": ended, "via": ViaCLI},
		})
	})
	if err != nil {
		return store.User{}, fmt.Errorf("reset password of %s: %w", username, err)
	}
	return u, nil
}
