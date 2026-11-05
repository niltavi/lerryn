// Package notify turns notification events into requests for the
// supported outlets (tech design 14) and sends them: the generic webhook
// with its JSON payload and HMAC signature, and the Feishu and Slack bot
// formats. It also guards outgoing requests against reaching internal
// addresses.
//
// The package knows nothing about the database. The service layer decides
// which events go to which channels, stores the deliveries and retries
// them.
package notify

import (
	"time"

	"github.com/google/uuid"
)

// Event types, the values of NotificationEventKind in the contract.
const (
	EventRunFailed          = "run_failed"
	EventRunTimedOut        = "run_timed_out"
	EventRunInterrupted     = "run_interrupted"
	EventCredentialExpiring = "credential_expiring" // #nosec G101 -- an event name, not a credential
	EventTest               = "test"
)

// SubscribableEvents are the event types a subscription may name.
var SubscribableEvents = []string{EventRunFailed, EventRunTimedOut, EventRunInterrupted, EventCredentialExpiring}

// Channel kinds, the values of NotificationChannelKind in the contract.
const (
	KindWebhook = "webhook"
	KindFeishu  = "feishu"
	KindSlack   = "slack"
)

// Kinds lists the channel kinds.
var Kinds = []string{KindWebhook, KindFeishu, KindSlack}

// MaxReasonRunes caps the reason of a run event (tech design 14).
const MaxReasonRunes = 200

// Event is the NotificationEvent of the contract's webhooks section, the
// body of a generic webhook request. Feishu and Slack get the same content
// in their own formats. It never holds credentials, logs or parameters.
type Event struct {
	// ID is the delivery ID, also sent as X-Lerryn-Delivery. It stays the
	// same across retries so receivers can drop duplicates.
	ID          uuid.UUID   `json:"id"`
	Type        string      `json:"type"`
	OccurredAt  time.Time   `json:"occurred_at"`
	Project     ProjectRef  `json:"project"`
	Task        *TaskRef    `json:"task"`
	Run         *Run        `json:"run"`
	Credential  *Credential `json:"credential"`
	MergedCount int         `json:"merged_count"`
	Link        string      `json:"link"`
}

// ProjectRef names a project.
type ProjectRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// TaskRef names a task.
type TaskRef struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	ProjectID uuid.UUID `json:"project_id"`
}

// Run is the run behind a run event, the latest one when failures merged.
type Run struct {
	ID        uuid.UUID `json:"id"`
	Status    string    `json:"status"`
	ErrorCode *string   `json:"error_code"`
	// Reason is the redacted, shortened error message.
	Reason  *string `json:"reason"`
	Version int     `json:"version"`
	Attempt int     `json:"attempt"`
	Link    string  `json:"link"`
}

// Credential is the credential of a credential_expiring event.
type Credential struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expires_at"`
}
