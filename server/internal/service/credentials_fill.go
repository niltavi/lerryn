package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/store"
)

// FillRequest returns a fill request for the fill page /c/{id}. Only the
// user the request is addressed to may open it; for anyone else, and for
// IDs that do not exist, it returns ErrNotFound, so the page cannot be
// used to probe for requests.
func (c *Credentials) FillRequest(ctx context.Context, p *auth.Principal, id uuid.UUID) (CredentialRequestDetail, error) {
	return c.GetRequest(ctx, p, id)
}

// FillFieldPointer is the FieldError.Pointer Fill uses for a field.
func FillFieldPointer(name string) string { return "#/" + name }

// Fill stores the values the user submitted, on the fill page or in the
// console's fill dialog, and fulfils the request: a new credential
// granted to the request's task, or a new version of the credential being
// updated. It is the only way values enter the platform, and it accepts
// browser sessions only, so an agent holding the user's access token
// cannot submit or replace values.
//
// values maps field names to what was typed; fields left out or empty
// count as not given. Validation errors name fields by FillFieldPointer
// and never quote a value.
func (c *Credentials) Fill(ctx context.Context, p *auth.Principal, id uuid.UUID, values map[string]string) (store.Credential, error) {
	if p == nil {
		return store.Credential{}, ErrUnauthenticated
	}
	if p.Method != auth.MethodSession || p.ReadOnly || !p.HasScope(auth.ScopeWrite) {
		// Read-only device sessions cannot fill in either.
		return store.Credential{}, ErrForbidden
	}
	req, err := c.ownRequest(ctx, c.db, p, id)
	if err != nil {
		return store.Credential{}, err
	}
	if requestStatus(req, c.now()) != store.CredentialRequestPending {
		return store.Credential{}, ErrRequestClosed
	}
	clean, err := checkFillValues(req.Fields, values)
	if err != nil {
		return store.Credential{}, err
	}
	defer clear(clean)
	var task store.Task
	if req.TaskID != uuid.Nil {
		// The user may have lost access to the task since the request was
		// made; the grant would then reach a task they cannot see.
		if task, err = c.visibleTask(ctx, p, req.TaskID); err != nil {
			return store.Credential{}, err
		}
	}

	var cred store.Credential
	err = c.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		now := c.now()
		// Claiming the request first makes a second submission, racing on
		// another server, find nothing to fulfil.
		res, err := tx.NewUpdate().Model((*store.CredentialRequest)(nil)).
			Set("status = ?", store.CredentialRequestFulfilled).
			Set("fulfilled_at = ?", now).
			Where("id = ? AND user_id = ? AND status = ? AND expires_at > ?", id, p.UserID, store.CredentialRequestPending, now).
			Exec(ctx)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrRequestClosed
		}

		action := "credential.update"
		if req.CredentialID == uuid.Nil {
			action = "credential.create"
			cred, err = c.createCredential(ctx, tx, p, req, clean)
		} else {
			cred, err = c.addVersion(ctx, tx, p, req, clean)
		}
		if err != nil {
			return err
		}
		if _, err := tx.NewUpdate().Model((*store.CredentialRequest)(nil)).
			Set("credential_id = ?", cred.ID).Where("id = ?", id).Exec(ctx); err != nil {
			return err
		}
		kind, actor := actorOf(p)
		if err := writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: action, resourceKind: "credential", resourceID: cred.ID.String(),
			result: resultSuccess,
			details: map[string]any{
				"name": cred.Name, "version": cred.CurrentVersion, "fields": fieldNames(cred.Fields),
				"request_id": id.String(),
			},
		}); err != nil {
			return err
		}
		if req.TaskID != uuid.Nil {
			if _, err := c.bind(ctx, tx, p, cred.ID, task, req.Name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return store.Credential{}, err
	}
	return cred, nil
}

// checkFillValues returns the submitted values to store: the given ones,
// normalised. Single-line values lose surrounding whitespace, which a
// paste often adds; multi-line values keep theirs but get "\n" line
// breaks, since browsers submit "\r\n".
func checkFillValues(fields []store.CredentialField, values map[string]string) (map[string]string, error) {
	var v validation
	out := make(map[string]string, len(fields))
	total := 0
	for _, f := range fields {
		raw := values[f.Name]
		val := raw
		if f.Multiline {
			val = strings.ReplaceAll(val, "\r\n", "\n")
		} else {
			val = strings.TrimSpace(val)
		}
		at := FillFieldPointer(f.Name)
		switch {
		case val == "" && !f.Optional:
			v.content(at, "请填写这一项")
		case val == "":
		case len(val) > maxFieldValue:
			v.content(at, fmt.Sprintf("内容不能超过 %d KiB", maxFieldValue>>10))
		case !utf8.ValidString(val) || strings.ContainsRune(val, 0):
			v.content(at, "内容包含无法保存的字符")
		case !f.Multiline && strings.ContainsAny(val, "\r\n"):
			v.content(at, "这一项只能填写一行")
		default:
			out[f.Name] = val
			total += len(val)
		}
	}
	if total > maxCredentialValues {
		v.content("#", fmt.Sprintf("所有字段合计不能超过 %d KiB", maxCredentialValues>>10))
	}
	if err := v.err(); err != nil {
		clear(out)
		return nil, err
	}
	return out, nil
}

// createCredential stores a new credential from a fulfilled request. If
// the owner already has an active credential of that name, the new one
// gets a numbered name instead of silently replacing it for other tasks.
func (c *Credentials) createCredential(ctx context.Context, tx bun.Tx, p *auth.Principal, req store.CredentialRequest, values map[string]string) (store.Credential, error) {
	var taken []string
	if err := tx.NewSelect().Model((*store.Credential)(nil)).Column("name").
		Where("owner_user_id = ? AND revoked_at IS NULL", p.UserID).
		Scan(ctx, &taken); err != nil {
		return store.Credential{}, err
	}
	name := req.Name
	for i := 2; slices.Contains(taken, name); i++ {
		if i > credentialNameAttempts+1 {
			return store.Credential{}, fmt.Errorf("credentials: user %s has too many credentials named %s", p.UserID, req.Name)
		}
		name = fmt.Sprintf("%s_%d", req.Name, i)
	}
	now := c.now()
	cred := store.Credential{
		ID: store.NewID(), OwnerUserID: p.UserID, Name: name, Purpose: req.Purpose,
		Fields: req.Fields, Status: store.CredentialActive, CurrentVersion: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.NewInsert().Model(&cred).Exec(ctx); err != nil {
		return store.Credential{}, err
	}
	return cred, c.insertVersion(ctx, tx, p, cred, values)
}

// addVersion stores values as the next version of the credential the
// request updates.
func (c *Credentials) addVersion(ctx context.Context, tx bun.Tx, p *auth.Principal, req store.CredentialRequest, values map[string]string) (store.Credential, error) {
	cred, err := c.ownCredential(ctx, tx, p, req.CredentialID)
	if err != nil {
		return store.Credential{}, err
	}
	if !cred.RevokedAt.IsZero() {
		return store.Credential{}, ErrRequestClosed
	}
	now := c.now()
	res, err := tx.NewUpdate().Model((*store.Credential)(nil)).
		Set("current_version = ?", cred.CurrentVersion+1).
		Set("fields = ?", req.Fields).
		Set("purpose = ?", req.Purpose).
		Set("updated_at = ?", now).
		Where("id = ? AND current_version = ? AND revoked_at IS NULL", cred.ID, cred.CurrentVersion).
		Exec(ctx)
	if err != nil {
		return store.Credential{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return store.Credential{}, err
	} else if n == 0 {
		return store.Credential{}, errors.New("credentials: credential changed while being updated")
	}
	cred.CurrentVersion++
	cred.Fields, cred.Purpose, cred.UpdatedAt = req.Fields, req.Purpose, now
	return cred, c.insertVersion(ctx, tx, p, cred, values)
}

func (c *Credentials) insertVersion(ctx context.Context, tx bun.Tx, p *auth.Principal, cred store.Credential, values map[string]string) error {
	sealed, err := c.seal(cred.ID, cred.CurrentVersion, values)
	if err != nil {
		return err
	}
	fingerprints, err := c.fingerprints(cred.Fields, values)
	if err != nil {
		return err
	}
	_, err = tx.NewInsert().Model(&store.CredentialVersion{
		ID: store.NewID(), CredentialID: cred.ID, Version: cred.CurrentVersion,
		Ciphertext: sealed.Ciphertext, WrappedDEK: sealed.WrappedDEK, KEKID: sealed.KEKID,
		Fingerprints: fingerprints,
		CreatedBy:    p.UserID, CreatedAt: c.now(),
	}).Exec(ctx)
	return err
}
