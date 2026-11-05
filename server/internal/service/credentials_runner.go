package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/secrets"
	"github.com/niltavi/lerryn/server/internal/store"
)

// UseCredentials lets the coordinator hand out credential values to
// runners. Without it, runs that reference credentials fail in
// preparation. It returns c for chaining.
func (c *Coordinator) UseCredentials(creds *Credentials) *Coordinator {
	c.creds = creds
	return c
}

// Secrets implements runproto.Coordinator.
func (c *Coordinator) Secrets(ctx context.Context, runnerID string, runID uuid.UUID) ([]runproto.Secret, error) {
	if c.creds == nil {
		return nil, &runproto.CredentialError{
			Code:    runproto.CodeCredentialUnavailable,
			Message: "服务端没有配置凭证存储，无法注入凭证",
		}
	}
	return c.creds.RunSecrets(ctx, runnerID, runID)
}

// RunSecrets returns the credential values of a run (tech design 12.5).
// The run must be assigned to runnerID and still preparing; anything else
// is runproto.ErrLeaseLost, so a runner can only ever fetch the values of
// its own run, once it starts it. A run whose lease has expired is lost
// as well: the reaper is about to hand it back, and a runner that stopped
// heartbeating must not still collect its values. Each credential the
// manifest requires
// must be granted to the task, active, and owned by an enabled user, or
// the call fails with a *runproto.CredentialError that the runner records
// on the run before any task code runs. Every fetch is audited.
func (c *Credentials) RunSecrets(ctx context.Context, runnerID string, runID uuid.UUID) ([]runproto.Secret, error) {
	var run store.Run
	err := c.db.NewSelect().Model(&run).Column("id", "task_id", "version_id", "status", "runner_id", "lease_expires_at").
		Where("id = ?", runID).Scan(ctx)
	if err == nil && run.RunnerID == runnerID && run.Status == store.RunPreparing && !run.LeaseExpiresAt.After(c.now()) {
		return nil, runproto.ErrLeaseLost
	}
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (run.RunnerID != runnerID || run.Status != store.RunPreparing)) {
		if err == nil && run.RunnerID == runnerID && run.Status == store.RunCancelling {
			// Not lost: the runner learns of the cancel from its next
			// heartbeat and ends the run as cancelled.
			return nil, fmt.Errorf("run %s is being cancelled; no credentials are handed out", runID)
		}
		return nil, runproto.ErrLeaseLost
	}
	if err != nil {
		return nil, fmt.Errorf("credentials of run %s: %w", runID, err)
	}
	var version store.TaskVersion
	if err := c.db.NewSelect().Model(&version).Column("manifest").Where("id = ?", run.VersionID).Scan(ctx); err != nil {
		return nil, fmt.Errorf("credentials of run %s: %w", runID, err)
	}
	m, err := decodeManifest(version.Manifest)
	if err != nil {
		return nil, fmt.Errorf("credentials of run %s: %w", runID, err)
	}
	if len(m.Credentials) == 0 {
		return nil, nil
	}

	bindings, err := loadBindings(ctx, c.db, run.TaskID)
	if err != nil {
		return nil, fmt.Errorf("credentials of run %s: %w", runID, err)
	}
	now := c.now()
	var (
		out  []runproto.Secret
		used []binding
	)
	for _, spec := range m.Credentials {
		b, ok := bindings[spec.Name]
		why := ""
		if ok {
			why = unusable(b, now)
		}
		switch {
		case (!ok || why != "") && spec.Optional:
			continue
		case !ok:
			ce := missingCredential(spec.Name)
			c.auditUse(ctx, runnerID, run, nil, spec.Name, resultFailure, ce.Code)
			return nil, &runproto.CredentialError{Code: ce.Code, Message: ce.Detail}
		case why != "":
			c.auditUse(ctx, runnerID, run, &b, spec.Name, resultFailure, runproto.CodeCredentialUnavailable)
			return nil, &runproto.CredentialError{Code: runproto.CodeCredentialUnavailable, Message: why}
		}
		secret, err := c.decryptFor(ctx, b, spec)
		if err != nil {
			c.log.ErrorContext(ctx, "decrypt credential for run", "run_id", runID, "credential_id", b.cred.ID,
				"version", b.cred.CurrentVersion, "error", err)
			c.auditUse(ctx, runnerID, run, &b, spec.Name, resultFailure, runproto.CodeCredentialUnavailable)
			if errors.Is(err, secrets.ErrDecrypt) || errors.Is(err, secrets.ErrUnknownKey) {
				return nil, &runproto.CredentialError{
					Code:    runproto.CodeCredentialUnavailable,
					Message: fmt.Sprintf("凭证 %s 无法解密，请管理员检查服务端的主密钥文件", spec.Name),
				}
			}
			return nil, fmt.Errorf("credentials of run %s: %w", runID, err)
		}
		out = append(out, secret)
		used = append(used, b)
	}

	for _, b := range used {
		if _, err := c.db.NewUpdate().Model((*store.Credential)(nil)).
			Set("last_used_at = ?", now).Where("id = ?", b.cred.ID).Exec(ctx); err != nil {
			c.log.WarnContext(ctx, "record credential use", "credential_id", b.cred.ID, "error", err)
		}
		c.auditUse(ctx, runnerID, run, &b, b.grant.Name, resultSuccess, "")
	}
	return out, nil
}

func (c *Credentials) decryptFor(ctx context.Context, b binding, spec CredentialSpec) (runproto.Secret, error) {
	var v store.CredentialVersion
	if err := c.db.NewSelect().Model(&v).
		Where("credential_id = ? AND version = ?", b.cred.ID, b.cred.CurrentVersion).Scan(ctx); err != nil {
		return runproto.Secret{}, fmt.Errorf("version %d of credential %s: %w", b.cred.CurrentVersion, b.cred.ID, err)
	}
	values, err := c.open(v)
	if err != nil {
		return runproto.Secret{}, err
	}
	s := runproto.Secret{Name: spec.Name, As: spec.as()}
	// Fields in the order the credential declares them, so injection is
	// deterministic.
	for _, f := range b.cred.Fields {
		if val, ok := values[f.Name]; ok {
			s.Fields = append(s.Fields, runproto.SecretField{Name: f.Name, Value: val, Sensitive: fieldSensitive(b.cred.Fields, f.Name)})
		}
	}
	clear(values)
	return s, nil
}

// auditUse records a runner fetching, or failing to fetch, a credential.
// The record names the run, task and credential, never a value. Failing
// to write it is logged: the run's own record carries the outcome too.
func (c *Credentials) auditUse(ctx context.Context, runnerID string, run store.Run, b *binding, name, result, code string) {
	details := map[string]any{"run_id": run.ID.String(), "task_id": run.TaskID.String(), "name": name, "runner_id": runnerID}
	resourceID := ""
	if b != nil {
		resourceID = b.cred.ID.String()
		details["version"] = b.cred.CurrentVersion
	}
	if code != "" {
		details["error_code"] = code
	}
	err := writeAudit(ctx, c.db, auditEntry{
		actorKind: actorRunner, actorName: runnerID,
		action: "credential.use", resourceKind: "credential", resourceID: resourceID,
		result: result, details: details,
	})
	if err != nil {
		c.log.ErrorContext(ctx, "write audit event", "action", "credential.use", "error", err)
	}
}

// RewrapAll moves every credential version that is not wrapped by the
// keyring's current master key onto it, after a key rotation. It returns
// how many versions it rewrapped. Ciphertexts are not touched; a version
// whose key is not in the keyring fails the call.
func (c *Credentials) RewrapAll(ctx context.Context) (int, error) {
	if c.keys == nil {
		return 0, errors.New("credentials: no master key configured")
	}
	current := c.keys.CurrentID()
	n := 0
	for {
		var batch []store.CredentialVersion
		if err := c.db.NewSelect().Model(&batch).Where("kek_id <> ?", current).Order("id").Limit(100).Scan(ctx); err != nil {
			return n, err
		}
		if len(batch) == 0 {
			return n, nil
		}
		err := c.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
			for _, v := range batch {
				moved, err := secrets.Rewrap(c.keys, secrets.Sealed{Ciphertext: v.Ciphertext, WrappedDEK: v.WrappedDEK, KEKID: v.KEKID},
					credentialAAD(v.CredentialID, v.Version))
				if err != nil {
					return fmt.Errorf("rewrap version %d of credential %s: %w", v.Version, v.CredentialID, err)
				}
				if _, err := tx.NewUpdate().Model((*store.CredentialVersion)(nil)).
					Set("wrapped_dek = ?", moved.WrappedDEK).Set("kek_id = ?", moved.KEKID).
					Where("id = ? AND kek_id = ?", v.ID, v.KEKID).Exec(ctx); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return n, err
		}
		n += len(batch)
	}
}
