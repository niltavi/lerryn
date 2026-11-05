package store

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// Identity links a user to an OIDC identity, keyed by (Issuer, Subject).
type Identity struct {
	bun.BaseModel `bun:"table:identities"`

	ID      uuid.UUID `bun:"id,pk,nullzero"`
	UserID  uuid.UUID `bun:"user_id,notnull"`
	Issuer  string    `bun:"issuer,notnull"`
	Subject string    `bun:"subject,notnull"`
	// Username and Email are preferred_username and email as last seen.
	Username    string    `bun:"username,nullzero"`
	Email       string    `bun:"email,nullzero"`
	CreatedAt   time.Time `bun:"created_at,notnull"`
	LastLoginAt time.Time `bun:"last_login_at,notnull"`
}

// PairingCode is a one-time code a web session shows as a QR code; a
// mobile app trades it for a read-only device session (tech design 12.1).
type PairingCode struct {
	bun.BaseModel `bun:"table:pairing_codes"`

	ID     uuid.UUID `bun:"id,pk,nullzero"`
	UserID uuid.UUID `bun:"user_id,notnull"`
	// CodeHash is the hex SHA-256 of the code.
	CodeHash  string    `bun:"code_hash,notnull"`
	CreatedAt time.Time `bun:"created_at,notnull"`
	ExpiresAt time.Time `bun:"expires_at,notnull"`
	UsedAt    time.Time `bun:"used_at,nullzero"`
	SessionID uuid.UUID `bun:"session_id,nullzero"`
}

// Setting keys changed in the admin console.
const (
	SettingOpenRegistration   = "open_registration"
	SettingLocalPasswordLogin = "local_password_login"
)

// Setting is a deployment setting changed at runtime. Value is JSON.
type Setting struct {
	bun.BaseModel `bun:"table:settings"`

	ID        uuid.UUID `bun:"id,pk,nullzero"`
	Key       string    `bun:"key,notnull"`
	Value     string    `bun:"value,notnull"`
	UpdatedBy uuid.UUID `bun:"updated_by,nullzero"`
	UpdatedAt time.Time `bun:"updated_at,notnull"`
}
