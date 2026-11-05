package store

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// Artifact is a file a run left in /sbx/out. The content lives in the
// object store under BlobKey until retention sets ExpiredAt.
type Artifact struct {
	bun.BaseModel `bun:"table:artifacts"`

	ID          uuid.UUID `bun:"id,pk,nullzero"`
	RunID       uuid.UUID `bun:"run_id,notnull"`
	Path        string    `bun:"path,notnull"`
	Title       string    `bun:"title,nullzero"`
	Size        int64     `bun:"size,notnull"`
	ContentType string    `bun:"content_type,notnull"`
	SHA256      string    `bun:"sha256,notnull"`
	BlobKey     string    `bun:"blob_key,notnull"`
	CreatedAt   time.Time `bun:"created_at,notnull"`
	ExpiredAt   time.Time `bun:"expired_at,nullzero"`
}

// BlobRelease is an object that retention stopped referencing (tech
// design 6.6). The collector deletes the object once no row references
// it and keeps the row, with DeletedAt set, as a tombstone for a while.
type BlobRelease struct {
	bun.BaseModel `bun:"table:blob_releases"`

	ID         uuid.UUID `bun:"id,pk,nullzero"`
	Hash       string    `bun:"hash,notnull"`
	ReleasedAt time.Time `bun:"released_at,notnull"`
	DeletedAt  time.Time `bun:"deleted_at,nullzero"`
}

// Notification channel kinds and delivery statuses (tech design 14).
const (
	ChannelWebhook = "webhook"
	ChannelFeishu  = "feishu"
	ChannelSlack   = "slack"

	DeliveryPending   = "pending"
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
)

// NotificationChannel is a user's notification outlet. The target URL and
// the webhook signing secret are encrypted together like a credential
// value; URLPreview is the masked form shown in the console.
type NotificationChannel struct {
	bun.BaseModel `bun:"table:notification_channels"`

	ID               uuid.UUID `bun:"id,pk,nullzero"`
	OwnerUserID      uuid.UUID `bun:"owner_user_id,notnull"`
	Name             string    `bun:"name,notnull"`
	Kind             string    `bun:"kind,notnull"`
	Enabled          bool      `bun:"enabled,notnull"`
	URLPreview       string    `bun:"url_preview,notnull"`
	SecretCiphertext []byte    `bun:"secret_ciphertext,notnull"`
	SecretWrappedDEK []byte    `bun:"secret_wrapped_dek,notnull"`
	SecretKEKID      string    `bun:"secret_kek_id,notnull"`
	CreatedAt        time.Time `bun:"created_at,notnull"`
	UpdatedAt        time.Time `bun:"updated_at,notnull"`
}

// NotificationSubscription sends events of a project, or of one task in
// it when TaskID is set, to a channel.
type NotificationSubscription struct {
	bun.BaseModel `bun:"table:notification_subscriptions"`

	ID        uuid.UUID `bun:"id,pk,nullzero"`
	ChannelID uuid.UUID `bun:"channel_id,notnull"`
	ProjectID uuid.UUID `bun:"project_id,notnull"`
	TaskID    uuid.UUID `bun:"task_id,nullzero"`
	Events    []string  `bun:"events,notnull"`
	CreatedAt time.Time `bun:"created_at,notnull"`
}

// NotificationDelivery is one notification to one channel and its
// delivery attempts. Consecutive failures of a task merge into the pending
// delivery with the same MergeKey.
type NotificationDelivery struct {
	bun.BaseModel `bun:"table:notification_deliveries"`

	ID          uuid.UUID `bun:"id,pk,nullzero"`
	ChannelID   uuid.UUID `bun:"channel_id,notnull"`
	EventType   string    `bun:"event_type,notnull"`
	MergeKey    string    `bun:"merge_key,nullzero"`
	TaskID      uuid.UUID `bun:"task_id,nullzero"`
	RunID       uuid.UUID `bun:"run_id,nullzero"`
	MergedCount int       `bun:"merged_count,nullzero,notnull,default:1"`
	// Payload is the JSON NotificationEvent; it never holds secrets.
	Payload       string    `bun:"payload,notnull"`
	Status        string    `bun:"status,notnull"`
	Attempts      int       `bun:"attempts,notnull"`
	HTTPStatus    int       `bun:"http_status,nullzero"`
	Error         string    `bun:"error,nullzero"`
	NextAttemptAt time.Time `bun:"next_attempt_at,nullzero"`
	LastAttemptAt time.Time `bun:"last_attempt_at,nullzero"`
	DeliveredAt   time.Time `bun:"delivered_at,nullzero"`
	CreatedAt     time.Time `bun:"created_at,notnull"`
	// ScheduledAt is when the delivery first became due. The next merged
	// failure of the same task waits until ScheduledAt plus the merge
	// window.
	ScheduledAt time.Time `bun:"scheduled_at,nullzero"`
}

// NotificationEventRecord marks a source event as dispatched: a run that
// failed, timed out or was interrupted, or a credential about to expire.
// Its unique keys make every server replica fan an event out at most
// once.
type NotificationEventRecord struct {
	bun.BaseModel `bun:"table:notification_events"`

	ID                  uuid.UUID `bun:"id,pk,nullzero"`
	EventType           string    `bun:"event_type,notnull"`
	RunID               uuid.UUID `bun:"run_id,nullzero"`
	CredentialID        uuid.UUID `bun:"credential_id,nullzero"`
	CredentialExpiresAt time.Time `bun:"credential_expires_at,nullzero"`
	Deliveries          int       `bun:"deliveries,notnull"`
	CreatedAt           time.Time `bun:"created_at,notnull"`
}
