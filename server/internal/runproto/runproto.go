// Package runproto is the runner protocol (tech design 4.5): the calls a
// runner makes to the server to claim runs, report progress and finish
// them, and the data those calls carry.
//
// In all-in-one the embedded runner calls a Coordinator in the same
// process. A remote runner (M5) reaches the server over HTTPS through a
// client that implements the same interface, so the runner code does not
// change. Nothing here depends on the database or on HTTP.
package runproto

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
)

// Protocol timing. A runner heartbeats every HeartbeatInterval; a run whose
// lease is not renewed for LeaseTTL is considered lost and is marked
// interrupted by the server (tech design 4.4).
const (
	HeartbeatInterval = 2 * time.Second
	LeaseTTL          = 30 * time.Second
)

// Log limits per run (tech design 6.5). The runner enforces them before
// sending; the server rejects batches beyond them.
const (
	MaxLineBytes = 64 << 10
	MaxRunLines  = 200_000
	MaxRunBytes  = 100 << 20
)

// ErrLeaseLost is returned for calls about a run that is no longer assigned
// to the calling runner, for example after its lease expired and the run
// was marked interrupted. The runner must stop the run's container and
// forget the run without reporting further.
var ErrLeaseLost = errors.New("run is no longer assigned to this runner")

// ErrRejected is returned for calls the server refuses for good, such as a
// gap in log line numbers, a log beyond the per-run limits or an invalid
// registration. Sending the same call again fails the same way, so the
// runner must not retry it. Every other error is transient: the server or
// the network may recover, and the call can be sent again.
var ErrRejected = errors.New("runner call rejected by the server")

// Coordinator is the server side of the protocol.
type Coordinator interface {
	// Register records the runner and its capacity. A runner calls it on
	// start and may call it again; it is idempotent.
	Register(ctx context.Context, r Registration) error

	// Claim moves up to max queued runs to preparing, assigns them to the
	// runner with a fresh lease, and returns what is needed to run them. It
	// returns at once, possibly with no runs.
	Claim(ctx context.Context, runnerID string, max int) ([]Assignment, error)

	// Heartbeat renews the leases of the runner's active runs and returns
	// those that must be stopped: runs whose cancellation was requested,
	// and runs no longer assigned to this runner.
	Heartbeat(ctx context.Context, runnerID string, active []uuid.UUID) (HeartbeatReply, error)

	// Files returns the files of a task version, with contents.
	Files(ctx context.Context, versionID uuid.UUID) ([]File, error)

	// Secrets returns the credential values of a preparing run that is
	// assigned to the runner, as the task's grants resolve them now
	// (tech design 12.5). A credential the run cannot have fails the call
	// with a *CredentialError; the runner then fails the run with its code
	// before any task code runs. Other errors may be retried. The values
	// must stay in memory: they never go to disk, logs or other calls.
	// Remote transports must carry them only over an authenticated,
	// encrypted connection.
	Secrets(ctx context.Context, runnerID string, runID uuid.UUID) ([]Secret, error)

	// Input returns the run input of a claimed run that is assigned to the
	// runner: the request body of a webhook run (tech design 7.4, 8.3).
	// The runner calls it only for assignments with HasInput set. A run
	// that is not the runner's is ErrLeaseLost.
	Input(ctx context.Context, runnerID string, runID uuid.UUID) (Input, error)

	// GetState returns the task state pinned when this run was claimed.
	// Only assignments with HasState may use it; a missing referenced blob
	// is an error, while the first run receives an explicit empty snapshot.
	GetState(ctx context.Context, runnerID string, runID uuid.UUID) (StateSnapshot, error)

	// PutState prepares the stopped run's complete output snapshot. Success
	// means its candidate is ready; Finish publishes it atomically only for
	// a completed run. Repeating the same candidate is safe.
	PutState(ctx context.Context, runnerID string, runID uuid.UUID, snapshot StateSnapshot) error

	// Started records that the run's container is running, moving the run
	// from preparing to running. EnvID identifies the environment it runs
	// in; empty when the task has no dependencies.
	Started(ctx context.Context, runnerID string, runID uuid.UUID, envID string, at time.Time) error

	// AppendLogs stores log lines. Lines of one run carry consecutive Seq
	// numbers starting at 1; a batch may be resent after an error, and lines
	// already stored are ignored. Dropped is the running total of lines and
	// bytes the runner discarded because of the limits above.
	AppendLogs(ctx context.Context, runnerID string, runID uuid.UUID, lines []LogLine, dropped Dropped) error

	// AppendEvents stores stage and result events reported by the task.
	// Events carry consecutive Seq numbers per run; resending is harmless.
	AppendEvents(ctx context.Context, runnerID string, runID uuid.UUID, events []Event) error

	// AddArtifact stores a file the task left in /sbx/out (tech design
	// 7.4). The runner calls it after the container stopped and before
	// Finish. Sending the same path again replaces the earlier content.
	AddArtifact(ctx context.Context, runnerID string, runID uuid.UUID, a Artifact) error

	// Finish records the run's terminal state. It must be called only after
	// the container is confirmed stopped and all logs, events and
	// artifacts were delivered. A completed HasState assignment also needs
	// a ready state candidate; other terminal states keep the previous state.
	Finish(ctx context.Context, runnerID string, runID uuid.UUID, f Finish) error
}

// Artifact limits per run. The runner skips files beyond them and says so
// in the log; the server rejects them.
const (
	MaxArtifacts        = 100
	MaxArtifactBytes    = 100 << 20
	MaxRunArtifactBytes = 500 << 20
)

// Artifact is a file of a run's output directory.
type Artifact struct {
	// Path is relative to /sbx/out, separated by '/'.
	Path string
	// Size is the length of Content in bytes.
	Size    int64
	Content io.Reader
}

// Registration describes a runner.
type Registration struct {
	// ID is stable across restarts of the same runner. It must match the
	// executor's ID rule: letters, digits, '_', '.', '-'.
	ID string
	// Name is for people, for example the host name.
	Name string
	// Slots is how many runs the runner executes at once.
	Slots int
	// Labels are matched against the runner_labels a task requires: the
	// runner claims a run only if it has every label the run requires
	// (tech design 4.4). Labels follow MaxLabelLen and LabelPattern.
	Labels []string
	// Version is the release of the runner, for administrators.
	Version string
}

// Label rules. A label is 1 to 63 lowercase letters, digits, '-', '_' or
// '.', starting with a letter or digit, so labels compare the same on both
// databases and read the same in configuration and manifests.
const (
	MaxLabels   = 32
	MaxLabelLen = 63
)

// ValidLabel reports whether s is a valid runner label.
func ValidLabel(s string) bool {
	if s == "" || len(s) > MaxLabelLen {
		return false
	}
	for i, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '-' || c == '_' || c == '.'):
		default:
			return false
		}
	}
	return true
}

// Assignment is a claimed run.
type Assignment struct {
	RunID     uuid.UUID
	TaskID    uuid.UUID
	VersionID uuid.UUID
	Manifest  Manifest
	// Params is the JSON object passed to the task in LERRYN_PARAMS.
	Params json.RawMessage
	// HasInput is true when the run has an input, such as the body of the
	// webhook request that started it; the runner fetches it with Input
	// and mounts it at /sbx/in/body.
	HasInput bool
	// HasState requires the runner to download the pinned task state and
	// deliver a complete candidate before reporting a completed run.
	HasState bool
}

// MaxInputBytes caps a run input. Webhook bodies are at most 10 MiB
// (the largest max_body_bytes a trigger accepts).
const MaxInputBytes = 10 << 20

// Input is a run's input.
type Input struct {
	// ContentType is the media type the caller declared, empty when it
	// sent none. The task reads it from LERRYN_INPUT_CONTENT_TYPE.
	ContentType string
	Content     []byte
}

// Manifest is the part of a task manifest the runner needs, already
// validated and with defaults applied by the server.
type Manifest struct {
	Runtime    Runtime
	Entrypoint []string
	Stages     []Stage
	// Timeout is the execution time limit.
	Timeout time.Duration
	// MilliCPUs is the CPU limit in thousandths of a CPU.
	MilliCPUs int64
	// MemoryBytes is the memory limit.
	MemoryBytes int64
	// Network is "none" or "full".
	Network string
	// Credentials lists the credentials the manifest references. The
	// runner calls Secrets only when there are any.
	Credentials []CredentialRef
	// Tools are the registered tools the manifest references, resolved
	// at claim time (tech design 7.8).
	Tools []Tool
}

// Tool is a registered tool of a run: an HTTP API or MCP server the run
// may call, or a script tool mounted into it. The runner hands an
// address to the task in LERRYN_TOOL_<NAME>_URL, a script tool's
// directory in LERRYN_TOOL_<NAME>_DIR, and all of them in LERRYN_TOOLS.
type Tool struct {
	Name string
	// Kind is ToolHTTP, ToolMCP or ToolScript.
	Kind string
	// URL is empty for script tools.
	URL string
	// TreeHash identifies a script tool's files, as a task version's
	// tree hash does; empty for other kinds.
	TreeHash string
	// Files are a script tool's files, mounted read-only at
	// /sbx/tools/<Name>.
	Files []File
}

// Tool kinds.
const (
	ToolHTTP   = "http"
	ToolMCP    = "mcp"
	ToolScript = "script"
)

// Credential injection modes (the manifest's credentials[].as).
const (
	InjectEnv  = "env"
	InjectFile = "file"
)

// CredentialRef is a credential the manifest references by name.
type CredentialRef struct {
	Name string
	// As is InjectEnv or InjectFile.
	As       string
	Optional bool
}

// Secret is one credential of a run, with its values.
type Secret struct {
	// Name is the manifest's reference name.
	Name string
	// As is InjectEnv: every field becomes an environment variable of the
	// same name; or InjectFile: every field is written to
	// /run/secrets/<Name>/<field>.
	As     string
	Fields []SecretField
}

// SecretField is one field of a credential.
type SecretField struct {
	Name  string
	Value string
	// Sensitive is false for fields the owner marked as not secret, such
	// as a region. Only sensitive values are redacted from output.
	Sensitive bool
}

// CredentialError is why a run cannot have its credentials. Code is
// credential_missing or credential_unavailable from the API contract's
// ErrorCode; Message explains it to people and agents and never contains
// a credential value.
type CredentialError struct {
	Code    string
	Message string
}

func (e *CredentialError) Error() string {
	return e.Code + ": " + e.Message
}

// Credential error codes.
//
// #nosec G101 -- error codes, not credentials
const (
	CodeCredentialMissing     = "credential_missing"
	CodeCredentialUnavailable = "credential_unavailable"
)

// Runtime selects the runtime image and dependency handling.
type Runtime struct {
	// Language is "python" in M1.
	Language string
	// Version is "3.12" in M1.
	Version string
	// Lockfile is "uv.lock" when dependencies are installed from a lock
	// file, empty otherwise.
	Lockfile string
}

// Stage is a stage declared in the manifest.
type Stage struct {
	ID    string
	Name  string
	After []string
}

// File is a task file.
type File struct {
	Path       string
	Content    []byte
	Executable bool
}

// HeartbeatReply lists runs the runner must stop.
type HeartbeatReply struct {
	// Cancel lists runs whose cancellation was requested. The runner stops
	// the container and finishes the run as cancelled.
	Cancel []uuid.UUID
	// Lost lists runs no longer assigned to this runner. The runner stops
	// the container and does not report anything more about them.
	Lost []uuid.UUID
}

// Log streams.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
	// StreamSystem carries the runner's own messages, such as dependency
	// installation progress or a truncation notice.
	StreamSystem = "system"
)

// LogLine is one line of output, without the trailing newline.
type LogLine struct {
	Seq    int64
	TS     time.Time
	Stream string
	// Stage is the stage the task was in when it wrote the line, empty
	// outside any stage.
	Stage string
	Text  string
}

// Dropped counts output discarded because of the per-run limits.
type Dropped struct {
	Lines int64
	Bytes int64
}

// Event kinds.
const (
	EventStageStart = "stage.start"
	EventStageDone  = "stage.done"
	EventStageFail  = "stage.fail"
	EventResult     = "result"
	// EventToolCall is a call of a registered tool made with sbx mcp call
	// (tech design 7.8).
	EventToolCall = "tool.call"
)

// Event is a stage or result event the task reported through sbx.
type Event struct {
	Seq  int64
	TS   time.Time
	Kind string
	// Stage is set for stage events.
	Stage string
	// Message is the optional text of a stage event.
	Message string
	// Result is set for result events.
	Result *Result
	// ToolCall is set for tool.call events. Stage is then the stage the
	// task was in.
	ToolCall *ToolCall
}

// ToolCall is a tool call the task recorded through sbx. Only a redacted
// summary of the arguments is kept, never the response.
type ToolCall struct {
	// Tool is the registered tool's name, Method the MCP tool called.
	Tool   string
	Method string
	OK     bool
	// DurationMillis is how long the call took, as the task measured.
	DurationMillis int64
	// ArgsSummary is the start of the arguments as compact JSON.
	ArgsSummary string
	// Error says why the call failed; empty when OK.
	Error string
}

// Limits of a tool call record, enforced by the runner and the server.
const (
	MaxToolMethodBytes  = 128
	MaxToolArgsRunes    = 200
	MaxToolErrorBytes   = 1 << 10
	MaxToolCallDuration = 24 * 60 * 60 * 1000
)

// Result is the business result the task reported.
type Result struct {
	// Status is ok, partial or failed.
	Status  string
	Summary string
	// Data is optional JSON.
	Data json.RawMessage
}

// Terminal statuses a runner reports. The server sets queued, preparing,
// running, cancelling and skipped itself.
const (
	StatusCompleted   = "completed"
	StatusFailed      = "failed"
	StatusTimedOut    = "timed_out"
	StatusCancelled   = "cancelled"
	StatusInterrupted = "interrupted"
)

// Finish is the end of a run.
type Finish struct {
	// Status is one of the terminal statuses above.
	Status string
	// ExitCode is the main process's exit code; nil when no process ran.
	ExitCode *int
	// ErrorCode is an ErrorCode value from the API contract, such as
	// script_error, timeout, out_of_memory or dependency_install_failed.
	// Empty for completed and cancelled runs.
	ErrorCode string
	// ErrorStage is the stage the run failed in, empty if none.
	ErrorStage string
	// ErrorMessage explains the failure to people and agents.
	ErrorMessage string
	FinishedAt   time.Time
}
