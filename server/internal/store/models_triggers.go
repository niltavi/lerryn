package store

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// Trigger types and catch-up policies (tech design 8).
const (
	TriggerTypeCron    = "cron"
	TriggerTypeWebhook = "webhook"

	CatchUpSkip   = "skip"
	CatchUpLatest = "latest"
)

// Trigger starts runs of a task's active version, on a cron schedule or
// from webhook calls. Triggers live outside task versions: changing a
// schedule does not create a version.
type Trigger struct {
	bun.BaseModel `bun:"table:triggers"`

	ID     uuid.UUID `bun:"id,pk,nullzero"`
	TaskID uuid.UUID `bun:"task_id,notnull"`
	// Name is unique within the task; setTriggers matches by it.
	Name    string `bun:"name,notnull"`
	Type    string `bun:"type,notnull"`
	Enabled bool   `bun:"enabled,notnull"`
	// Schedule and Timezone are set for cron triggers only.
	Schedule string `bun:"schedule,nullzero"`
	Timezone string `bun:"timezone,nullzero"`
	CatchUp  string `bun:"catch_up,nullzero,notnull,default:'skip'"`
	// Params is the JSON object replacing the manifest params, empty to
	// use them.
	Params string `bun:"params,nullzero"`
	// MaxBodyBytes, TokenHash and TokenPrefix are set for webhook
	// triggers only. TokenHash is the hex SHA-256 of the token.
	MaxBodyBytes int64     `bun:"max_body_bytes,nullzero"`
	TokenHash    string    `bun:"token_hash,nullzero"`
	TokenPrefix  string    `bun:"token_prefix,nullzero"`
	NextFireAt   time.Time `bun:"next_fire_at,nullzero"`
	LastFiredAt  time.Time `bun:"last_fired_at,nullzero"`
	CreatedBy    uuid.UUID `bun:"created_by,notnull"`
	CreatedAt    time.Time `bun:"created_at,notnull"`
	UpdatedAt    time.Time `bun:"updated_at,notnull"`
}

// Trigger fire outcomes.
const (
	FireQueued  = "queued"
	FireSkipped = "skipped"
)

// TriggerFire is one scheduled time of a trigger. (TriggerID, ScheduledAt)
// is unique, which keeps several servers from creating the same run twice.
type TriggerFire struct {
	bun.BaseModel `bun:"table:trigger_fires"`

	ID          uuid.UUID `bun:"id,pk,nullzero"`
	TriggerID   uuid.UUID `bun:"trigger_id,notnull"`
	ScheduledAt time.Time `bun:"scheduled_at,notnull"`
	Outcome     string    `bun:"outcome,notnull"`
	RunID       uuid.UUID `bun:"run_id,nullzero"`
	// MissedCount is how many fires a collapsed missed record stands for.
	MissedCount int       `bun:"missed_count,notnull"`
	CreatedAt   time.Time `bun:"created_at,notnull"`
}

// IdempotencyRecord remembers the first response to an Idempotency-Key for
// 24 hours (tech design 9.3).
type IdempotencyRecord struct {
	bun.BaseModel `bun:"table:idempotency_records"`

	ID uuid.UUID `bun:"id,pk,nullzero"`
	// Principal is user:<id> or trigger:<id>.
	Principal string `bun:"principal,notnull"`
	Key       string `bun:"key,notnull"`
	Operation string `bun:"operation,notnull"`
	// RequestHash is the hex SHA-256 over method, path and body.
	RequestHash string `bun:"request_hash,notnull"`
	// Status and Response are zero while the first request is in progress.
	Status    int       `bun:"status,nullzero"`
	Response  string    `bun:"response,nullzero"`
	CreatedAt time.Time `bun:"created_at,notnull"`
	ExpiresAt time.Time `bun:"expires_at,notnull"`
}
