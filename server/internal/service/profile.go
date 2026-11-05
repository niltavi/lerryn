package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/store"
)

const (
	// PairingTTL is how long a mobile pairing code stays valid.
	PairingTTL = 5 * time.Minute
	// pairingCodeLen is the length of a pairing code. Its 40 bits, a
	// five-minute life, single use and the per-address rate limit make
	// guessing impractical.
	pairingCodeLen = 8
	// pairingAlphabet is Crockford's base32: no I, L, O or U, so a code
	// typed from a screen is not misread.
	pairingAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

	maxTimezoneBytes = 64
	maxDeviceRunes   = 64
)

var locales = []string{"", "zh-CN", "en"}

// UpdateMeInput changes the caller's profile. Nil fields stay as they are;
// an empty string clears the field.
type UpdateMeInput struct {
	DisplayName *string
	Email       *string
	Locale      *string
	Timezone    *string
}

// UpdateMe changes p's display name, email, locale and time zone.
func (a *Accounts) UpdateMe(ctx context.Context, p *auth.Principal, in UpdateMeInput) (Account, error) {
	var v validation
	sets := map[string]any{}
	var changed []string
	if in.DisplayName != nil {
		name := strings.TrimSpace(*in.DisplayName)
		checkDisplayName(&v, name)
		sets["display_name"] = name
		changed = append(changed, "display_name")
	}
	if in.Email != nil {
		email := strings.TrimSpace(*in.Email)
		checkEmail(&v, email)
		sets["email"] = nullable(email)
		changed = append(changed, "email")
	}
	if in.Locale != nil {
		if !slices.Contains(locales, *in.Locale) {
			v.body("#/locale", "界面语言只能是 zh-CN 或 en，空字符串表示清除")
		}
		sets["locale"] = nullable(*in.Locale)
		changed = append(changed, "locale")
	}
	if in.Timezone != nil {
		tz := strings.TrimSpace(*in.Timezone)
		if tz != "" {
			// "Local" would mean the server's zone, which is not what a
			// user picking a zone means.
			if _, err := time.LoadLocation(tz); err != nil || tz == "Local" || len(tz) > maxTimezoneBytes {
				v.body("#/timezone", "时区应为 IANA 时区名，例如 Asia/Shanghai")
			}
		}
		sets["timezone"] = nullable(tz)
		changed = append(changed, "timezone")
	}
	if len(changed) == 0 {
		v.body("", "至少给出一个要修改的字段")
	}
	if err := v.err(); err != nil {
		return Account{}, err
	}
	err := a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		q := tx.NewUpdate().Model((*store.User)(nil)).Where("id = ?", p.UserID).Set("updated_at = ?", store.Now())
		for _, col := range changed {
			q = q.Set(col+" = ?", sets[col])
		}
		if _, err := q.Exec(ctx); err != nil {
			return err
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "user.update", resourceKind: "user", resourceID: p.UserID.String(),
			details: map[string]any{"fields": changed},
		})
	})
	if err != nil {
		return Account{}, err
	}
	return a.Me(ctx, p)
}

// ChangePassword changes p's password after checking the current one. It
// ends every other session and device session of the user, so whoever
// knew the old password is logged out; access tokens stay. Wrong current
// passwords count towards the same rate limit as failed logins.
func (a *Accounts) ChangePassword(ctx context.Context, p *auth.Principal, current, next string) error {
	if p.Method != auth.MethodSession || p.ReadOnly {
		return ErrForbidden
	}
	var u store.User
	if err := a.db.NewSelect().Model(&u).Where("id = ?", p.UserID).Scan(ctx); err != nil {
		return fmt.Errorf("load user %s: %w", p.UserID, err)
	}
	if u.PasswordHash == "" {
		var v validation
		v.content("#/current_password", "这个账号只用单点登录，没有可以修改的密码")
		return v.err()
	}
	var v validation
	checkPassword(&v, "#/new_password", next)
	if err := v.err(); err != nil {
		return err
	}
	// Counted before the check and taken back when the password is right,
	// for the reason LoginDevice gives.
	key := "user:" + strings.ToLower(p.Username)
	if !a.logins.Allow(key) {
		return ErrRateLimited
	}
	ok, err := auth.VerifyPassword(ctx, u.PasswordHash, current)
	if err != nil {
		return fmt.Errorf("verify password of user %s: %w", u.ID, err)
	}
	if !ok {
		kind, actor := actorOf(p)
		auditFailure(ctx, a.db, a.log, auditEntry{
			actorKind: kind, actorID: actor,
			action: "user.change_password", resourceKind: "user", resourceID: u.ID.String(),
			details: map[string]any{"reason": "wrong_password"},
		})
		return ErrUnauthenticated
	}
	a.logins.Forgive(key)
	hash, err := auth.HashPassword(ctx, next)
	if err != nil {
		return err
	}
	return a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewUpdate().Model((*store.User)(nil)).
			Set("password_hash = ?", hash).Set("updated_at = ?", store.Now()).
			Where("id = ?", u.ID).Exec(ctx); err != nil {
			return err
		}
		ended, err := endSessions(ctx, tx, u.ID, p.CredentialID)
		if err != nil {
			return err
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "user.change_password", resourceKind: "user", resourceID: u.ID.String(),
			details: map[string]any{"sessions_ended": ended},
		})
	})
}

// PairingCode is a code a web session shows as a QR code.
type PairingCode struct {
	Code      string
	URI       string
	ExpiresAt time.Time
}

// CreatePairingCode issues a one-time pairing code for p, replacing any
// unused code p created before. Only a web session may pair a device.
func (a *Accounts) CreatePairingCode(ctx context.Context, p *auth.Principal) (PairingCode, error) {
	if p.Method != auth.MethodSession || p.ReadOnly {
		return PairingCode{}, ErrForbidden
	}
	code, err := newPairingCode()
	if err != nil {
		return PairingCode{}, err
	}
	now := store.Now()
	row := store.PairingCode{
		ID: store.NewID(), UserID: p.UserID, CodeHash: auth.HashToken(code),
		CreatedAt: now, ExpiresAt: now.Add(PairingTTL),
	}
	err = a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewDelete().Model((*store.PairingCode)(nil)).
			Where("user_id = ? AND used_at IS NULL", p.UserID).Exec(ctx); err != nil {
			return err
		}
		if _, err := tx.NewInsert().Model(&row).Exec(ctx); err != nil {
			return err
		}
		return writeAudit(ctx, tx, auditEntry{
			actorKind: actorUser, actorID: p.UserID,
			action: "pairing.create", resourceKind: "pairing_code", resourceID: row.ID.String(),
		})
	})
	if err != nil {
		return PairingCode{}, err
	}
	uri := "lerryn://pair?" + url.Values{"server": {a.publicURL}, "code": {code}}.Encode()
	return PairingCode{Code: code, URI: uri, ExpiresAt: row.ExpiresAt}, nil
}

func newPairingCode() (string, error) {
	b := make([]byte, pairingCodeLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate pairing code: %w", err)
	}
	for i := range b {
		// 256 is a multiple of 32, so the modulo is unbiased.
		b[i] = pairingAlphabet[int(b[i])%len(pairingAlphabet)]
	}
	return string(b), nil
}

// normalizePairingCode accepts a code as a user may type it: in lower
// case, with spaces or hyphens.
func normalizePairingCode(code string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == ' ' || r == '-':
			return -1
		case r >= 'a' && r <= 'z':
			return r - ('a' - 'A')
		}
		return r
	}, code)
}

// ExchangePairingCode trades a pairing code for a read-only device session
// of the user who created it. The code works once. server must be the
// deployment's public URL, which the QR code carries, so an app pointed at
// the wrong deployment fails clearly. Attempts are rate limited per client
// address like logins.
func (a *Accounts) ExchangePairingCode(ctx context.Context, server, code string, device DeviceInfo) (Account, NewSession, error) {
	var v validation
	if !sameServer(server, a.publicURL) {
		v.content("#/server", "配对码不属于这个部署，请确认扫描的是这个部署的二维码")
	}
	checkDevice(&v, device)
	if err := v.err(); err != nil {
		return Account{}, NewSession{}, err
	}
	key := "pair:" + auth.ClientFrom(ctx).IP
	if a.pairings.Blocked(key) {
		return Account{}, NewSession{}, ErrRateLimited
	}
	fail := func(reason string, userID uuid.UUID) (Account, NewSession, error) {
		a.pairings.Record(key)
		e := auditEntry{actorKind: actorAnonymous, action: "pairing.exchange", resourceKind: "pairing_code",
			details: map[string]any{"reason": reason}}
		if userID != uuid.Nil {
			e.actorKind, e.actorID = actorUser, userID
		}
		auditFailure(ctx, a.db, a.log, e)
		return Account{}, NewSession{}, ErrUnauthenticated
	}

	var row store.PairingCode
	err := a.db.NewSelect().Model(&row).Where("code_hash = ?", auth.HashToken(normalizePairingCode(code))).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return fail("unknown_code", uuid.Nil)
	}
	if err != nil {
		return Account{}, NewSession{}, err
	}
	now := store.Now()
	if !row.UsedAt.IsZero() {
		return fail("used", row.UserID)
	}
	if !now.Before(row.ExpiresAt) {
		return fail("expired", row.UserID)
	}
	u, err := a.enabledUser(ctx, row.UserID)
	if errors.Is(err, ErrUnauthenticated) {
		return fail("disabled", row.UserID)
	}
	if err != nil {
		return Account{}, NewSession{}, err
	}

	var (
		acct Account
		sess NewSession
	)
	errUsed := errors.New("pairing code used concurrently")
	err = a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// Claiming the code with a conditional update makes the single use
		// hold when two devices race with the same code.
		res, err := tx.NewUpdate().Model((*store.PairingCode)(nil)).Set("used_at = ?", now).
			Where("id = ? AND used_at IS NULL", row.ID).Exec(ctx)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errUsed
		}
		if sess, err = createDeviceSession(ctx, tx, u.ID, device); err != nil {
			return err
		}
		if _, err := tx.NewUpdate().Model((*store.PairingCode)(nil)).Set("session_id = ?", sess.ID).
			Where("id = ?", row.ID).Exec(ctx); err != nil {
			return err
		}
		if acct, err = loadAccount(ctx, tx, u); err != nil {
			return err
		}
		return writeAudit(ctx, tx, auditEntry{
			actorKind: actorUser, actorID: u.ID, actorName: u.Username,
			action: "pairing.exchange", resourceKind: "session", resourceID: sess.ID.String(),
			details: map[string]any{"device": device.Name, "platform": device.Platform},
		})
	})
	if errors.Is(err, errUsed) {
		return fail("used", row.UserID)
	}
	if err != nil {
		return Account{}, NewSession{}, err
	}
	return acct, sess, nil
}

func checkDevice(v *validation, d DeviceInfo) {
	if n := len([]rune(strings.TrimSpace(d.Name))); n == 0 || n > maxDeviceRunes {
		v.body("#/device/name", fmt.Sprintf("设备名为 1 到 %d 个字符", maxDeviceRunes))
	}
	if !slices.Contains([]string{"ios", "android", "other"}, d.Platform) {
		v.body("#/device/platform", "平台只能是 ios、android 或 other")
	}
}

// sameServer compares a deployment address from a QR code with
// server.public_url, ignoring a trailing slash and the case of the scheme
// and host.
func sameServer(got, want string) bool {
	g, err1 := url.Parse(strings.TrimRight(got, "/"))
	w, err2 := url.Parse(strings.TrimRight(want, "/"))
	if err1 != nil || err2 != nil || w.Host == "" {
		return false
	}
	return strings.EqualFold(g.Scheme, w.Scheme) && strings.EqualFold(g.Host, w.Host) && g.Path == w.Path
}

// Device is a signed-in mobile device.
type Device struct {
	Session store.Session
	Current bool
}

// ListDevices returns p's live device sessions, most recently used first.
func (a *Accounts) ListDevices(ctx context.Context, p *auth.Principal) ([]Device, error) {
	var rows []store.Session
	if err := a.db.NewSelect().Model(&rows).
		Where("user_id = ? AND kind = ? AND expires_at > ?", p.UserID, store.SessionMobile, store.Now()).
		Order("last_seen_at DESC", "id DESC").Scan(ctx); err != nil {
		return nil, err
	}
	out := make([]Device, len(rows))
	for i, s := range rows {
		out[i] = Device{Session: s, Current: p.Method == auth.MethodSession && s.ID == p.CredentialID}
	}
	return out, nil
}

// RevokeDevice ends one of p's device sessions. A web session or another
// user's device is ErrNotFound.
func (a *Accounts) RevokeDevice(ctx context.Context, p *auth.Principal, id uuid.UUID) error {
	return a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.NewDelete().Model((*store.Session)(nil)).
			Where("id = ? AND user_id = ? AND kind = ?", id, p.UserID, store.SessionMobile).Exec(ctx)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "device.revoke", resourceKind: "session", resourceID: id.String(),
		})
	})
}
