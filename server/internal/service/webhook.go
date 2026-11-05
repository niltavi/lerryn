package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/store"
)

// webhookOperation is the operationId idempotency records of webhook calls
// are kept under.
const webhookOperation = "triggerWebhook"

// maxIdempotencyKey is the longest Idempotency-Key accepted.
const maxIdempotencyKey = 128

// errKeyTaken means another call recorded the Idempotency-Key first.
var errKeyTaken = errors.New("idempotency key taken")

// WebhookCall is one POST /hooks/{triggerId} request.
type WebhookCall struct {
	TriggerID uuid.UUID
	// Token is the bearer token of the request, empty when there was none.
	Token          string
	IdempotencyKey string
	ContentType    string
	Body           io.Reader
}

// WebhookResult is the run a webhook call produced.
type WebhookResult struct {
	RunID  uuid.UUID
	Status string
	// Duplicate is true when the Idempotency-Key was seen before; RunID is
	// then the run of the first call.
	Duplicate bool
}

// webhookResponse is the stored first response to an Idempotency-Key.
type webhookResponse struct {
	RunID uuid.UUID `json:"run_id"`
}

// Webhook runs a webhook trigger's task (tech design 8.3). The token must
// be the trigger's: a wrong or missing token, or an unknown trigger, is
// ErrUnauthenticated, so the answer does not reveal which triggers exist.
// A disabled trigger or an inactive task is ErrNotFound. The body is kept
// in the object store and becomes the run's input. With an
// Idempotency-Key, repeating the call returns the first run.
//
// The run is queued, or recorded as skipped for overlap or a full queue,
// like a scheduled run.
func (t *Triggers) Webhook(ctx context.Context, call WebhookCall) (WebhookResult, error) {
	trig, err := t.authenticateHook(ctx, call)
	if err != nil {
		return WebhookResult{}, err
	}
	key := trig.ID.String()
	if t.hooks.Blocked(key) {
		return WebhookResult{}, ErrRateLimited
	}
	t.hooks.Record(key)
	if call.IdempotencyKey != "" && !validIdempotencyKey(call.IdempotencyKey) {
		v := validation{}
		v.fields = append(v.fields, FieldError{Parameter: "Idempotency-Key", Detail: "幂等键为 1 到 128 个可见 ASCII 字符"})
		return WebhookResult{}, v.err()
	}
	var task store.Task
	if err := t.db.NewSelect().Model(&task).Where("id = ?", trig.TaskID).Scan(ctx); err != nil {
		return WebhookResult{}, fmt.Errorf("task of trigger %s: %w", trig.ID, err)
	}
	if !trig.Enabled || !taskCanFire(task) {
		return WebhookResult{}, ErrNotFound
	}

	limit := trig.MaxBodyBytes
	if limit <= 0 {
		limit = defaultMaxBodyBytes
	}
	body := []byte{}
	if call.Body != nil {
		body, err = io.ReadAll(io.LimitReader(call.Body, limit+1))
		if err != nil {
			return WebhookResult{}, fmt.Errorf("read webhook body: %w", err)
		}
	}
	if int64(len(body)) > limit {
		return WebhookResult{}, &PayloadTooLargeError{Limit: limit}
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "POST /hooks/%s\n%s\n", trig.ID, call.ContentType)
	_, _ = h.Write(body)
	requestHash := hex.EncodeToString(h.Sum(nil))
	principal := "trigger:" + trig.ID.String()

	if call.IdempotencyKey != "" {
		if res, ok, err := t.replay(ctx, principal, call.IdempotencyKey, requestHash); ok || err != nil {
			return res, err
		}
	}
	// Stored before the transaction: a blob whose transaction then fails
	// is unreferenced, which is harmless.
	inputKey, err := t.blobs.Put(ctx, bytes.NewReader(body))
	if err != nil {
		return WebhookResult{}, fmt.Errorf("store webhook body: %w", err)
	}

	now := t.now()
	var result WebhookResult
	var restore []string
	err = t.writeRetrying(ctx, func(ctx context.Context, tx bun.Tx) error {
		restore = nil
		if call.IdempotencyKey != "" {
			// A concurrent call with the same key may have committed
			// since the replay check above.
			taken, err := tx.NewSelect().Model((*store.IdempotencyRecord)(nil)).
				Where("principal = ? AND key = ?", principal, call.IdempotencyKey).Exists(ctx)
			if err != nil {
				return err
			}
			if taken {
				return errKeyTaken
			}
		}
		version, err := loadVersionByID(ctx, tx, task.ActiveVersionID)
		if err != nil {
			return err
		}
		params, err := triggerParams(trig, version.Manifest)
		if err != nil {
			return err
		}
		run, err := t.admit(ctx, tx, runSource{
			task: task, version: version, kind: store.TriggerWebhook, triggerID: trig.ID,
			params: params, inputKey: inputKey, inputType: call.ContentType, now: now,
		})
		if err != nil {
			return err
		}
		result = WebhookResult{RunID: run.ID, Status: run.Status}
		if _, err := tx.NewUpdate().Model((*store.Trigger)(nil)).Set("last_fired_at = ?", now).
			Where("id = ?", trig.ID).Exec(ctx); err != nil {
			return err
		}
		if restore, err = claimBlobs(ctx, tx, []string{inputKey}); err != nil {
			return err
		}
		if call.IdempotencyKey == "" {
			return nil
		}
		resp, err := json.Marshal(webhookResponse{RunID: run.ID})
		if err != nil {
			return err
		}
		_, err = tx.NewInsert().Model(&store.IdempotencyRecord{
			ID: store.NewID(), Principal: principal, Key: call.IdempotencyKey, Operation: webhookOperation,
			RequestHash: requestHash, Status: 202, Response: string(resp),
			CreatedAt: now, ExpiresAt: now.Add(idempotencyTTL),
		}).Exec(ctx)
		return err
	})
	if errors.Is(err, errKeyTaken) || (store.IsUniqueViolation(err) && call.IdempotencyKey != "") {
		// A concurrent call with the same key committed first.
		if res, ok, rerr := t.replay(ctx, principal, call.IdempotencyKey, requestHash); ok || rerr != nil {
			return res, rerr
		}
	}
	if err != nil {
		return WebhookResult{}, err
	}
	if _, err := restoreBlobs(ctx, t.blobs, restore, func(string) io.Reader { return bytes.NewReader(body) }); err != nil {
		return WebhookResult{}, err
	}
	if result.Status == store.RunQueued {
		t.queued(ctx, []uuid.UUID{result.RunID})
	}
	return result, nil
}

// authenticateHook returns the webhook trigger call names if the call's
// token is its token.
func (t *Triggers) authenticateHook(ctx context.Context, call WebhookCall) (store.Trigger, error) {
	var trig store.Trigger
	err := t.db.NewSelect().Model(&trig).Where("id = ?", call.TriggerID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Trigger{}, ErrUnauthenticated
	}
	if err != nil {
		return store.Trigger{}, err
	}
	got := auth.HashToken(call.Token)
	if call.Token == "" || trig.Type != store.TriggerTypeWebhook || trig.TokenHash == "" ||
		subtle.ConstantTimeCompare([]byte(got), []byte(trig.TokenHash)) != 1 {
		return store.Trigger{}, ErrUnauthenticated
	}
	return trig, nil
}

// replay returns the stored result of an earlier call with the same key.
// ok is false when there is none, or only an expired one.
func (t *Triggers) replay(ctx context.Context, principal, key, requestHash string) (WebhookResult, bool, error) {
	var rec store.IdempotencyRecord
	err := t.db.NewSelect().Model(&rec).Where("principal = ? AND key = ?", principal, key).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return WebhookResult{}, false, nil
	}
	if err != nil {
		return WebhookResult{}, false, err
	}
	if !rec.ExpiresAt.After(t.now()) {
		if _, err := t.db.NewDelete().Model((*store.IdempotencyRecord)(nil)).Where("id = ?", rec.ID).Exec(ctx); err != nil {
			return WebhookResult{}, false, err
		}
		return WebhookResult{}, false, nil
	}
	if rec.Operation != webhookOperation || rec.RequestHash != requestHash {
		return WebhookResult{}, false, ErrIdempotencyConflict
	}
	var resp webhookResponse
	if err := json.Unmarshal([]byte(rec.Response), &resp); err != nil {
		return WebhookResult{}, false, fmt.Errorf("decode idempotency record %s: %w", rec.ID, err)
	}
	var run store.Run
	if err := t.db.NewSelect().Model(&run).Column("id", "status").Where("id = ?", resp.RunID).Scan(ctx); err != nil {
		return WebhookResult{}, false, fmt.Errorf("run of idempotency record %s: %w", rec.ID, err)
	}
	return WebhookResult{RunID: run.ID, Status: run.Status, Duplicate: true}, true, nil
}

// validIdempotencyKey checks the IdempotencyKey schema: 1 to 128 visible
// ASCII characters.
func validIdempotencyKey(k string) bool {
	if len(k) == 0 || len(k) > maxIdempotencyKey {
		return false
	}
	for i := range len(k) {
		if k[i] < 0x21 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

// Sweep deletes idempotency records past their 24 hours. Call it now and
// then from the leader.
func (t *Triggers) Sweep(ctx context.Context, now time.Time) error {
	if _, err := t.db.NewDelete().Model((*store.IdempotencyRecord)(nil)).Where("expires_at < ?", now).Exec(ctx); err != nil {
		return fmt.Errorf("delete expired idempotency records: %w", err)
	}
	return nil
}
