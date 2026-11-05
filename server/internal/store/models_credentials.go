package store

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// Credential and credential request statuses (tech design 12.4).
const (
	CredentialActive  = "active"
	CredentialRevoked = "revoked"
	CredentialExpired = "expired"

	CredentialRequestPending   = "pending"
	CredentialRequestFulfilled = "fulfilled"
	CredentialRequestExpired   = "expired"
	CredentialRequestCancelled = "cancelled"
)

// CredentialField describes one field of a credential or fill request.
// It never holds a value.
type CredentialField struct {
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	Secret      bool   `json:"secret"`
	Multiline   bool   `json:"multiline,omitempty"`
	Optional    bool   `json:"optional,omitempty"`
}

// Credential is a user's secret, such as an API key, with one or more
// fields. The values live encrypted in CredentialVersion.
type Credential struct {
	bun.BaseModel `bun:"table:credentials"`

	ID          uuid.UUID         `bun:"id,pk,nullzero"`
	OwnerUserID uuid.UUID         `bun:"owner_user_id,notnull"`
	Name        string            `bun:"name,notnull"`
	Purpose     string            `bun:"purpose,notnull"`
	Fields      []CredentialField `bun:"fields,notnull"`
	// Status is CredentialActive, CredentialRevoked or CredentialExpired.
	Status           string    `bun:"status,nullzero,notnull,default:'active'"`
	CurrentVersion   int       `bun:"current_version,notnull"`
	ExpiresAt        time.Time `bun:"expires_at,nullzero"`
	RevokedAt        time.Time `bun:"revoked_at,nullzero"`
	LastUsedAt       time.Time `bun:"last_used_at,nullzero"`
	ExpiryNotifiedAt time.Time `bun:"expiry_notified_at,nullzero"`
	CreatedAt        time.Time `bun:"created_at,notnull"`
	UpdatedAt        time.Time `bun:"updated_at,notnull"`
}

// CredentialVersion is one encrypted version of a credential's values
// (tech design 12.3): AES-256-GCM with a per-version data key, the data
// key wrapped by the master key KEKID.
type CredentialVersion struct {
	bun.BaseModel `bun:"table:credential_versions"`

	ID           uuid.UUID `bun:"id,pk,nullzero"`
	CredentialID uuid.UUID `bun:"credential_id,notnull"`
	Version      int       `bun:"version,notnull"`
	Ciphertext   []byte    `bun:"ciphertext,notnull"`
	WrappedDEK   []byte    `bun:"wrapped_dek,notnull"`
	KEKID        string    `bun:"kek_id,notnull"`
	// Fingerprints are keyed digests of the sensitive values and their
	// encodings, as JSON, for recognising them in submissions (tech
	// design 11.7). Empty for versions stored before they existed.
	Fingerprints string    `bun:"fingerprints,nullzero"`
	CreatedBy    uuid.UUID `bun:"created_by,notnull"`
	CreatedAt    time.Time `bun:"created_at,notnull"`
}

// CredentialGrant lets a task use a credential under the manifest
// reference Name.
type CredentialGrant struct {
	bun.BaseModel `bun:"table:credential_grants"`

	ID           uuid.UUID `bun:"id,pk,nullzero"`
	CredentialID uuid.UUID `bun:"credential_id,notnull"`
	TaskID       uuid.UUID `bun:"task_id,notnull"`
	Name         string    `bun:"name,notnull"`
	GrantedBy    uuid.UUID `bun:"granted_by,notnull"`
	CreatedAt    time.Time `bun:"created_at,notnull"`
}

// CredentialRequest asks UserID to fill in a credential on the
// server-rendered page /c/{id}.
type CredentialRequest struct {
	bun.BaseModel `bun:"table:credential_requests"`

	ID     uuid.UUID `bun:"id,pk,nullzero"`
	UserID uuid.UUID `bun:"user_id,notnull"`
	TaskID uuid.UUID `bun:"task_id,nullzero"`
	// CredentialID is the credential being updated, or the one created on
	// fulfilment.
	CredentialID uuid.UUID         `bun:"credential_id,nullzero"`
	Name         string            `bun:"name,notnull"`
	Purpose      string            `bun:"purpose,notnull"`
	Fields       []CredentialField `bun:"fields,notnull"`
	Status       string            `bun:"status,notnull"`
	// Client is the name of the access token the request was created with.
	Client      string    `bun:"client,nullzero"`
	CreatedAt   time.Time `bun:"created_at,notnull"`
	ExpiresAt   time.Time `bun:"expires_at,notnull"`
	FulfilledAt time.Time `bun:"fulfilled_at,nullzero"`
	CancelledAt time.Time `bun:"cancelled_at,nullzero"`
}
