package store

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// Models mirror the tables of server/migrations; the migrations are the
// source of truth for types and constraints. Conventions:
//
//   - IDs come from NewID and are set by the caller before insert. The
//     nullzero tag turns a forgotten ID into a NOT NULL violation instead
//     of a row keyed by the nil UUID.
//   - Times are UTC and written by the application (use Now). Optional
//     times and strings use nullzero: the zero value is stored as NULL.
//   - Slice and map fields are stored as JSON text and never queried.

// User is a member account, built-in or created on first OIDC login.
type User struct {
	bun.BaseModel `bun:"table:users"`

	ID       uuid.UUID `bun:"id,pk,nullzero"`
	Username string    `bun:"username,notnull"`
	// DisplayName defaults to empty; clients fall back to Username.
	DisplayName string `bun:"display_name,notnull"`
	Email       string `bun:"email,nullzero"`
	// PasswordHash is an Argon2id hash, empty for OIDC-only users.
	PasswordHash string    `bun:"password_hash,nullzero"`
	IsAdmin      bool      `bun:"is_admin,notnull"`
	DisabledAt   time.Time `bun:"disabled_at,nullzero"`
	CreatedAt    time.Time `bun:"created_at,notnull"`
	UpdatedAt    time.Time `bun:"updated_at,notnull"`
	// Locale (zh-CN or en) and Timezone (IANA) are profile preferences;
	// empty lets the client decide.
	Locale   string `bun:"locale,nullzero"`
	Timezone string `bun:"timezone,nullzero"`
	// LastSeenAt is the last request with any session or token of the
	// user, written at most once a minute.
	LastSeenAt time.Time `bun:"last_seen_at,nullzero"`
}

// AccessToken is a personal access token used by agents and the CLI.
type AccessToken struct {
	bun.BaseModel `bun:"table:access_tokens"`

	ID     uuid.UUID `bun:"id,pk,nullzero"`
	UserID uuid.UUID `bun:"user_id,notnull"`
	Name   string    `bun:"name,notnull"`
	// TokenHash is the hex SHA-256 of the token.
	TokenHash string `bun:"token_hash,notnull"`
	// Prefix is the start of the token, shown so users can tell tokens apart.
	Prefix     string    `bun:"prefix,notnull"`
	Scopes     []string  `bun:"scopes,notnull"`
	ExpiresAt  time.Time `bun:"expires_at,nullzero"`
	LastUsedAt time.Time `bun:"last_used_at,nullzero"`
	RevokedAt  time.Time `bun:"revoked_at,nullzero"`
	CreatedAt  time.Time `bun:"created_at,notnull"`
}

// Session is a login session of the web console or the mobile app.
type Session struct {
	bun.BaseModel `bun:"table:sessions"`

	ID     uuid.UUID `bun:"id,pk,nullzero"`
	UserID uuid.UUID `bun:"user_id,notnull"`
	// TokenHash is the hex SHA-256 of the session token.
	TokenHash  string    `bun:"token_hash,notnull"`
	Kind       string    `bun:"kind,notnull"`
	IP         string    `bun:"ip,nullzero"`
	UserAgent  string    `bun:"user_agent,nullzero"`
	CreatedAt  time.Time `bun:"created_at,notnull"`
	ExpiresAt  time.Time `bun:"expires_at,notnull"`
	LastSeenAt time.Time `bun:"last_seen_at,notnull"`
	// Device fields describe the device of a mobile session (kind mobile),
	// which is read-only.
	DeviceName      string `bun:"device_name,nullzero"`
	DevicePlatform  string `bun:"device_platform,nullzero"`
	DeviceOSVersion string `bun:"device_os_version,nullzero"`
}

// Session kinds.
const (
	SessionWeb    = "web"
	SessionMobile = "mobile"
)

// Project is the unit of sharing. Each user owns one personal project.
type Project struct {
	bun.BaseModel `bun:"table:projects"`

	ID          uuid.UUID `bun:"id,pk,nullzero"`
	Name        string    `bun:"name,notnull"`
	OwnerUserID uuid.UUID `bun:"owner_user_id,notnull"`
	IsPersonal  bool      `bun:"is_personal,notnull"`
	CreatedAt   time.Time `bun:"created_at,notnull"`
	UpdatedAt   time.Time `bun:"updated_at,notnull"`
}

// RoleBinding grants a user a role in a project: viewer, maintainer or
// owner. Valid roles are checked in the service layer, not by the schema,
// so adding one needs no migration.
type RoleBinding struct {
	bun.BaseModel `bun:"table:role_bindings"`

	ID        uuid.UUID `bun:"id,pk,nullzero"`
	ProjectID uuid.UUID `bun:"project_id,notnull"`
	UserID    uuid.UUID `bun:"user_id,notnull"`
	Role      string    `bun:"role,notnull"`
	CreatedAt time.Time `bun:"created_at,notnull"`
	UpdatedAt time.Time `bun:"updated_at,notnull"`
}

// AuditEvent records a security-relevant action (tech design 12.7). It
// never holds secrets.
type AuditEvent struct {
	bun.BaseModel `bun:"table:audit_events"`

	ID uuid.UUID `bun:"id,pk,nullzero"`
	TS time.Time `bun:"ts,notnull"`
	// ActorKind is user, token or runner; ActorID is empty when the actor
	// is unknown, such as a failed login.
	ActorKind    string         `bun:"actor_kind,notnull"`
	ActorID      uuid.UUID      `bun:"actor_id,nullzero"`
	Action       string         `bun:"action,notnull"`
	ResourceKind string         `bun:"resource_kind,notnull"`
	ResourceID   string         `bun:"resource_id,nullzero"`
	Result       string         `bun:"result,notnull"`
	IP           string         `bun:"ip,nullzero"`
	Details      map[string]any `bun:"details,nullzero"`
	// ActorName is the username, token name or runner name at the time.
	ActorName string `bun:"actor_name,nullzero"`
	// Kind is the x-operation-kind of the operation: read, mutate or
	// execute. Via is the entry point (api, mcp, console, runner, hook)
	// and Tool the MCP tool name when Via is mcp.
	Kind      string `bun:"kind,nullzero"`
	Via       string `bun:"via,nullzero"`
	Tool      string `bun:"tool,nullzero"`
	RequestID string `bun:"request_id,nullzero"`
}

// Lease is a named, expiring lock such as the leader lease. Use
// DB.AcquireLease rather than writing rows directly: expiry must follow
// the database clock on PostgreSQL.
type Lease struct {
	bun.BaseModel `bun:"table:leases"`

	ID        uuid.UUID `bun:"id,pk,nullzero"`
	Name      string    `bun:"name,notnull"`
	Holder    string    `bun:"holder,notnull"`
	ExpiresAt time.Time `bun:"expires_at,notnull"`
}

// Task states (tech design 5.2).
const (
	TaskActive   = "active"
	TaskInactive = "inactive"
)

// Task is an automation task. Its versions are immutable; LatestSeq is the
// highest version number and Description comes from that version's
// manifest.
type Task struct {
	bun.BaseModel `bun:"table:tasks"`

	ID          uuid.UUID `bun:"id,pk,nullzero"`
	ProjectID   uuid.UUID `bun:"project_id,notnull"`
	Name        string    `bun:"name,notnull"`
	Description string    `bun:"description,notnull"`
	LatestSeq   int       `bun:"latest_seq,notnull"`
	CreatedBy   uuid.UUID `bun:"created_by,notnull"`
	CreatedAt   time.Time `bun:"created_at,notnull"`
	UpdatedAt   time.Time `bun:"updated_at,notnull"`
	// State is TaskActive or TaskInactive. Left empty on insert it
	// becomes the column default, inactive.
	State string `bun:"state,nullzero,notnull,default:'inactive'"`
	// ActiveVersionID and ActiveSeq name the version triggers run; zero
	// until the first activation. They stay set while the task is paused.
	ActiveVersionID uuid.UUID `bun:"active_version_id,nullzero"`
	ActiveSeq       int       `bun:"active_seq,nullzero"`
	ActivatedAt     time.Time `bun:"activated_at,nullzero"`
	// ArchivedAt is set when the task is archived; the name is then free
	// for a new task in the project.
	ArchivedAt time.Time `bun:"archived_at,nullzero"`
	// TriggersRevision counts setTriggers calls; it is the If-Match value
	// of the trigger set.
	TriggersRevision int `bun:"triggers_revision,notnull"`
	// ConsoleHideManualRun hides the console's manual run button and
	// refuses manual runs started from a browser session. Agents set it;
	// the zero value shows the button.
	ConsoleHideManualRun bool `bun:"console_hide_manual_run,notnull"`
}

// TaskVersion is one immutable version of a task: a manifest and a file
// tree.
type TaskVersion struct {
	bun.BaseModel `bun:"table:task_versions"`

	ID     uuid.UUID `bun:"id,pk,nullzero"`
	TaskID uuid.UUID `bun:"task_id,notnull"`
	Seq    int       `bun:"seq,notnull"`
	// Manifest is the manifest as submitted, as JSON text. Text rather
	// than json.RawMessage: Bun writes byte slices as blobs, which the
	// STRICT TEXT column of SQLite rejects.
	Manifest string `bun:"manifest,notnull"`
	// TreeHash is the hex SHA-256 over the sorted file list.
	TreeHash  string    `bun:"tree_hash,notnull"`
	CreatedBy uuid.UUID `bun:"created_by,notnull"`
	CreatedAt time.Time `bun:"created_at,notnull"`
	// Client is the name of the access token the version was submitted
	// with, empty for sessions.
	Client string `bun:"client,nullzero"`
	// TestedAt is the end of the first successful test run of the version.
	TestedAt time.Time `bun:"tested_at,nullzero"`
}

// TaskFile is a file of a task version. The content lives in the blob
// store under BlobHash.
type TaskFile struct {
	bun.BaseModel `bun:"table:task_files"`

	ID         uuid.UUID `bun:"id,pk,nullzero"`
	VersionID  uuid.UUID `bun:"version_id,notnull"`
	Path       string    `bun:"path,notnull"`
	BlobHash   string    `bun:"blob_hash,notnull"`
	Size       int64     `bun:"size,notnull"`
	Executable bool      `bun:"executable,notnull"`
}

// Runner is an execution node. Its ID is chosen by the runner itself and
// stays the same across restarts.
type Runner struct {
	bun.BaseModel `bun:"table:runners"`

	ID         string    `bun:"id,pk,nullzero"`
	Name       string    `bun:"name,notnull"`
	Slots      int       `bun:"slots,notnull"`
	LastSeenAt time.Time `bun:"last_seen_at,notnull"`
	CreatedAt  time.Time `bun:"created_at,notnull"`
	// TokenHash is the hash of the remote runner's credential; empty for
	// the embedded runner of all-in-one.
	TokenHash       string    `bun:"token_hash,nullzero"`
	ProtocolVersion int       `bun:"protocol_version,notnull"`
	DisabledAt      time.Time `bun:"disabled_at,nullzero"`
	// Version is the release the runner reported; empty if unknown.
	Version string `bun:"version,nullzero"`
}

// RunnerLabel is a label a runner offers (tech design 4.4).
type RunnerLabel struct {
	bun.BaseModel `bun:"table:runner_labels"`

	ID       uuid.UUID `bun:"id,pk,nullzero"`
	RunnerID string    `bun:"runner_id,notnull"`
	Label    string    `bun:"label,notnull"`
}

// RunRequiredLabel is a label a run requires from the runner that claims
// it. A run is claimed only by a runner that has all of them.
type RunRequiredLabel struct {
	bun.BaseModel `bun:"table:run_required_labels"`

	ID    uuid.UUID `bun:"id,pk,nullzero"`
	RunID uuid.UUID `bun:"run_id,notnull"`
	Label string    `bun:"label,notnull"`
}

// RunnerToken is a registration token. A runner presents it to obtain its
// own credential; it stays usable until it expires or is revoked.
type RunnerToken struct {
	bun.BaseModel `bun:"table:runner_tokens"`

	ID         uuid.UUID `bun:"id,pk,nullzero"`
	Name       string    `bun:"name,notnull"`
	TokenHash  string    `bun:"token_hash,notnull"`
	Prefix     string    `bun:"prefix,notnull"`
	CreatedBy  uuid.UUID `bun:"created_by,nullzero"`
	ExpiresAt  time.Time `bun:"expires_at,nullzero"`
	RevokedAt  time.Time `bun:"revoked_at,nullzero"`
	LastUsedAt time.Time `bun:"last_used_at,nullzero"`
	CreatedAt  time.Time `bun:"created_at,notnull"`
}

// Run statuses (tech design 5.4). The active ones are also spelled out in
// the runs_concurrency_key index and in the run queue queries of the
// dialect package.
const (
	RunQueued      = "queued"
	RunPreparing   = "preparing"
	RunRunning     = "running"
	RunCancelling  = "cancelling"
	RunCompleted   = "completed"
	RunFailed      = "failed"
	RunTimedOut    = "timed_out"
	RunCancelled   = "cancelled"
	RunInterrupted = "interrupted"
	RunSkipped     = "skipped"
)

// RunActive reports whether status is one in which a run holds its
// concurrency key; every other status is terminal.
func RunActive(status string) bool {
	switch status {
	case RunQueued, RunPreparing, RunRunning, RunCancelling:
		return true
	}
	return false
}

// Run is one execution of a task version. Claiming, lease renewal and
// lease expiry go through DB.ClaimRuns, DB.RenewRunLeases and
// DB.ExpireRunLeases, which follow the database clock on PostgreSQL.
type Run struct {
	bun.BaseModel `bun:"table:runs"`

	ID          uuid.UUID `bun:"id,pk,nullzero"`
	ProjectID   uuid.UUID `bun:"project_id,notnull"`
	TaskID      uuid.UUID `bun:"task_id,notnull"`
	VersionID   uuid.UUID `bun:"version_id,notnull"`
	VersionSeq  int       `bun:"version_seq,notnull"`
	Status      string    `bun:"status,notnull"`
	IsTest      bool      `bun:"is_test,notnull"`
	TriggerKind string    `bun:"trigger_kind,notnull"`
	CreatedBy   uuid.UUID `bun:"created_by,nullzero"`
	// Params is the JSON object passed to the task.
	Params string `bun:"params,notnull"`
	// ConcurrencyKey is the task ID while the task forbids overlapping
	// runs, empty (NULL) otherwise.
	ConcurrencyKey    string    `bun:"concurrency_key,nullzero"`
	RunnerID          string    `bun:"runner_id,nullzero"`
	LeaseExpiresAt    time.Time `bun:"lease_expires_at,nullzero"`
	EnvID             string    `bun:"env_id,nullzero"`
	CancelRequestedAt time.Time `bun:"cancel_requested_at,nullzero"`
	QueuedAt          time.Time `bun:"queued_at,notnull"`
	StartedAt         time.Time `bun:"started_at,nullzero"`
	FinishedAt        time.Time `bun:"finished_at,nullzero"`
	// ExitCode is a pointer because 0 is a meaningful exit code.
	ExitCode     *int   `bun:"exit_code"`
	ErrorCode    string `bun:"error_code,nullzero"`
	ErrorStage   string `bun:"error_stage,nullzero"`
	ErrorMessage string `bun:"error_message,nullzero"`
	// Result is the JSON of the last reported result, empty if none.
	Result       string `bun:"result,nullzero"`
	LogLines     int64  `bun:"log_lines,notnull"`
	DroppedLines int64  `bun:"dropped_lines,notnull"`
	DroppedBytes int64  `bun:"dropped_bytes,notnull"`
	// EventSeq is the number of the run's last stored event, from the
	// runner or the server. RunnerEventSeq is the last number the runner
	// sent; runner batches are deduplicated against it.
	EventSeq       int64 `bun:"event_seq,notnull"`
	RunnerEventSeq int64 `bun:"runner_event_seq,notnull"`

	// TriggerID, TriggerFireID and ScheduledAt name the trigger behind a
	// cron or webhook run. They are not foreign keys: triggers can be
	// deleted while their runs remain.
	TriggerID     uuid.UUID `bun:"trigger_id,nullzero"`
	TriggerFireID uuid.UUID `bun:"trigger_fire_id,nullzero"`
	ScheduledAt   time.Time `bun:"scheduled_at,nullzero"`
	// Client is the name of the access token a manual run was started with.
	Client string `bun:"client,nullzero"`
	// Attempt counts from 1 and MaxAttempts is 1 plus the manifest's
	// retry.max_attempts. Left zero on insert they become 1.
	Attempt     int `bun:"attempt,nullzero,notnull,default:1"`
	MaxAttempts int `bun:"max_attempts,nullzero,notnull,default:1"`
	// RetryOf is the previous attempt of an automatic retry; NotBefore
	// holds a queued retry back until its delay has passed.
	RetryOf   uuid.UUID `bun:"retry_of,nullzero"`
	NotBefore time.Time `bun:"not_before,nullzero"`
	// RerunOf is the run a rerun was modelled on.
	RerunOf uuid.UUID `bun:"rerun_of,nullzero"`
	// SkipReason is set on skipped runs: overlap, queue_full, missed,
	// inactive or manual.
	SkipReason string `bun:"skip_reason,nullzero"`
	// ClaimedAt is when a runner claimed the run; RunnerName is copied at
	// claim time so the run still names its runner after it is gone.
	ClaimedAt  time.Time `bun:"claimed_at,nullzero"`
	RunnerName string    `bun:"runner_name,nullzero"`
	// CancelRequestedBy and CancelClient say who asked to cancel.
	CancelRequestedBy uuid.UUID `bun:"cancel_requested_by,nullzero"`
	CancelClient      string    `bun:"cancel_client,nullzero"`
	// InputKey is the object store key of a webhook request body, and
	// InputContentType its media type.
	InputKey         string `bun:"input_key,nullzero"`
	InputContentType string `bun:"input_content_type,nullzero"`
	// LogArchive is the JSON description of the run's archived log in the
	// object store; empty while the log is still in log chunks.
	LogArchive string `bun:"log_archive,nullzero"`
	// LogBlob and LogBytes are the hash and size of the archive object,
	// copied out of LogArchive so they can be queried. LogBytes is nil for
	// a log archived before they existed until the leader fills it in.
	LogBlob  string `bun:"log_blob,nullzero"`
	LogBytes *int64 `bun:"log_bytes"`
	// LogsExpiredAt is when retention removed the run's log.
	LogsExpiredAt time.Time `bun:"logs_expired_at,nullzero"`
	// ArchivedAt pins this run and its database logs against retention.
	ArchivedAt time.Time `bun:"archived_at,nullzero"`
	// RetryDecidedAt is when the scheduler decided whether this ended
	// attempt is retried; zero while undecided (tech design 8.5).
	RetryDecidedAt time.Time `bun:"retry_decided_at,nullzero"`
}

// Run trigger kinds (the API's TriggerKind).
const (
	TriggerManual  = "manual"
	TriggerCron    = "cron"
	TriggerWebhook = "webhook"
	TriggerRetry   = "retry"
)

// Reasons a run was skipped (the API's SkipReason).
const (
	SkipOverlap   = "overlap"
	SkipQueueFull = "queue_full"
	SkipMissed    = "missed"
	SkipInactive  = "inactive"
	SkipManual    = "manual"
)

// Run event kinds (the API's RunEventKind). Stage and result events come
// from the task through the runner; the others are written by the server.
const (
	EventStageStart   = "stage.start"
	EventStageDone    = "stage.done"
	EventStageFail    = "stage.fail"
	EventResult       = "result"
	EventToolCall     = "tool.call"
	EventStatus       = "status"
	EventArtifact     = "artifact"
	EventWarning      = "warning"
	EventLogTruncated = "log.truncated"
)

// RunEvent is an event of a run: a stage or result reported by the task,
// or a status change, artifact or warning recorded by the server.
type RunEvent struct {
	bun.BaseModel `bun:"table:run_events"`

	ID      uuid.UUID `bun:"id,pk,nullzero"`
	RunID   uuid.UUID `bun:"run_id,notnull"`
	Seq     int64     `bun:"seq,notnull"`
	TS      time.Time `bun:"ts,notnull"`
	Kind    string    `bun:"kind,notnull"`
	Stage   string    `bun:"stage,nullzero"`
	Message string    `bun:"message,nullzero"`
	// Payload is the JSON data of the event: the result of a result
	// event, {"from", "to"} of a status event, and so on.
	Payload string `bun:"payload,nullzero"`
}

// LogChunk is a batch of consecutive log lines of a run, stored as NDJSON
// with one LogRecord per line (tech design 6.5).
type LogChunk struct {
	bun.BaseModel `bun:"table:log_chunks"`

	ID      uuid.UUID `bun:"id,pk,nullzero"`
	RunID   uuid.UUID `bun:"run_id,notnull"`
	SeqFrom int64     `bun:"seq_from,notnull"`
	SeqTo   int64     `bun:"seq_to,notnull"`
	Data    string    `bun:"data,notnull"`
	// Bytes is the total length of the lines' text, the measure the
	// per-run byte limit applies to.
	Bytes     int64     `bun:"bytes,notnull"`
	CreatedAt time.Time `bun:"created_at,notnull"`
}

// LogRecord is one line of LogChunk.Data.
type LogRecord struct {
	Seq    int64     `json:"seq"`
	TS     time.Time `json:"ts"`
	Stream string    `json:"stream"`
	Stage  string    `json:"stage,omitempty"`
	Text   string    `json:"text"`
}
