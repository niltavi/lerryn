package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Idempotency rules (tech design 9.3).
const (
	// IdempotencyTTL is how long a key's first response is kept.
	IdempotencyTTL = 24 * time.Hour
	// idempotencyStale is when a first request that never completed, its
	// server having died, stops blocking its key.
	idempotencyStale = 10 * time.Minute
)

var (
	// ErrIdempotencyInProgress means the first request with the key has
	// not finished yet.
	ErrIdempotencyInProgress = errors.New("the first request with this idempotency key is still in progress")
)

// Idempotency remembers the first response to each idempotency key for
// IdempotencyTTL, per user. It shares idempotency_records with webhook
// calls (principal trigger:<id>); the leader's Triggers.Sweep deletes
// expired records of both.
type Idempotency struct {
	db  *store.DB
	log *slog.Logger
}

// NewIdempotency returns the idempotency record store.
func NewIdempotency(db *store.DB, log *slog.Logger) *Idempotency {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Idempotency{db: db, log: log}
}

// IdempotencyClaim is the outcome of Begin: either a stored response to
// replay, or a new record the caller completes or releases.
type IdempotencyClaim struct {
	// Replay is set when the key already has a response.
	Replay *StoredResponse
	id     uuid.UUID
}

// StoredResponse is a remembered response.
type StoredResponse struct {
	Status int
	Body   string
}

// Begin claims key for a request identified by operation and hash, the
// SHA-256 of the request. A key used for another request is
// ErrIdempotencyConflict; one whose first request is still running is
// ErrIdempotencyInProgress.
func (i *Idempotency) Begin(ctx context.Context, p *auth.Principal, key, operation, hash string) (IdempotencyClaim, error) {
	if p == nil {
		return IdempotencyClaim{}, ErrUnauthenticated
	}
	principal := "user:" + p.UserID.String()
	var claim IdempotencyClaim
	err := i.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		claim = IdempotencyClaim{}
		now := store.Now()
		var rec store.IdempotencyRecord
		err := tx.NewSelect().Model(&rec).Where("principal = ? AND key = ?", principal, key).Scan(ctx)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		case rec.ExpiresAt.Before(now) || (rec.Status == 0 && rec.CreatedAt.Before(now.Add(-idempotencyStale))):
			if _, err := tx.NewDelete().Model(&rec).WherePK().Exec(ctx); err != nil {
				return err
			}
		case rec.Operation != operation || rec.RequestHash != hash:
			return ErrIdempotencyConflict
		case rec.Status == 0:
			return ErrIdempotencyInProgress
		default:
			claim.Replay = &StoredResponse{Status: rec.Status, Body: rec.Response}
			return nil
		}
		rec = store.IdempotencyRecord{
			ID: store.NewID(), Principal: principal, Key: key, Operation: operation, RequestHash: hash,
			CreatedAt: now, ExpiresAt: now.Add(IdempotencyTTL),
		}
		if _, err := tx.NewInsert().Model(&rec).Exec(ctx); err != nil {
			if store.IsUniqueViolation(err) {
				// Another server took the key between the read and the
				// insert; its request is the first one.
				return ErrIdempotencyInProgress
			}
			return err
		}
		claim.id = rec.ID
		return nil
	})
	return claim, err
}

// Complete stores the response to a claimed key.
func (i *Idempotency) Complete(ctx context.Context, c IdempotencyClaim, status int, body string) error {
	return i.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.NewUpdate().Model((*store.IdempotencyRecord)(nil)).
			Set("status = ?", status).Set("response = ?", body).
			Where("id = ?", c.id).Exec(ctx)
		if err != nil {
			return fmt.Errorf("store idempotent response: %w", err)
		}
		return nil
	})
}

// Release forgets a claimed key whose request failed in a way worth
// retrying, such as an internal error, so the same key can be used again.
func (i *Idempotency) Release(ctx context.Context, c IdempotencyClaim) error {
	return i.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.NewDelete().Model((*store.IdempotencyRecord)(nil)).Where("id = ?", c.id).Exec(ctx)
		return err
	})
}
