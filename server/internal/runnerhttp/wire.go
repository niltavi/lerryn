// Package runnerhttp carries the runner protocol (tech design 4.5) over
// HTTP: Handler serves it on the server under /runner/v1/, and Client
// implements runproto.Coordinator for a remote runner, so the runner code
// is the same in all-in-one and on a separate host.
//
// The wire format is defined here rather than in api/: both ends ship in
// the same binary, the protocol is not public, and the server accepts the
// current and the previous protocol version, announced in the
// Lerryn-Runner-Protocol header. The JSON field names below are the
// contract between versions; change them only together with the version.
//
// Authentication: a runner first presents a registration token to
// /runner/v1/register and receives a credential of its own, which it sends
// as a bearer token on every later call. The server takes the runner's
// identity from the credential, never from the request body.
package runnerhttp

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Paths, relative to the server URL.
const (
	BasePath     = "/runner/v1/"
	pathRegister = BasePath + "register"
	pathClaim    = BasePath + "claim"
	pathBeat     = BasePath + "heartbeat"
)

func filesPath(version uuid.UUID) string { return BasePath + "versions/" + version.String() + "/files" }
func runPath(run uuid.UUID, what string) string {
	return BasePath + "runs/" + run.String() + "/" + what
}

// ProtocolHeader carries the protocol version the runner speaks.
const ProtocolHeader = "Lerryn-Runner-Protocol"

// Error codes of protocol error responses.
const (
	// codeRejected: the call breaks the protocol and will fail the same
	// way if repeated (runproto.ErrRejected).
	codeRejected = "rejected"
	// codeLeaseLost: the run is no longer assigned to the runner
	// (runproto.ErrLeaseLost).
	codeLeaseLost = "lease_lost"
	// codeUnauthenticated: the credential or registration token is
	// missing, wrong, expired or revoked.
	codeUnauthenticated = "unauthenticated"
	// codeForbidden: the credential belongs to another runner.
	codeForbidden = "forbidden"
	// codeUnavailable: the server failed; the call may be retried.
	codeUnavailable = "unavailable"
	// codeCredential: the run cannot have its credentials
	// (runproto.CredentialError); errorBody.CredentialCode says why.
	codeCredential = "credential"
	// codeRunnerIDInUse: a registration token was presented for the ID of
	// a live runner (service.ErrRunnerIDInUse). The runner retries; older
	// clients treat the unknown code as a transient error too.
	codeRunnerIDInUse = "runner_id_in_use"
)

// Limits of the transport.
const (
	// maxRequestBytes bounds request bodies. A log batch holds at most a
	// few hundred KiB of text; JSON escaping can multiply that several
	// times for binary output.
	maxRequestBytes = 16 << 20
	// maxResponseBytes bounds responses the client reads; claims with
	// embedded script tools are the largest.
	maxResponseBytes = 256 << 20
	// maxClaimBatch applies at both HTTP peers, before leases are committed.
	// Each run may embed twenty MiB of script tools (base64 on the wire),
	// plus file paths and manifest strings that JSON can expand sixfold.
	// Eight maximum assignments, including that metadata, fit the response
	// limit; TestClaimBudgetForMaximumAssignments checks these bounds.
	// The in-process coordinator keeps its larger batch size.
	maxClaimBatch = 8
	// MaxClaimWait caps how long a claim request is held open when no run
	// is queued.
	MaxClaimWait = 30 * time.Second
)

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// CredentialCode is credential_missing or credential_unavailable when
	// Code is codeCredential.
	CredentialCode string `json:"credential_code,omitempty"`
}

type registerRequest struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Slots  int      `json:"slots"`
	Labels []string `json:"labels"`
	// Version is the runner's release; added within protocol version 1
	// as an optional field.
	Version string `json:"version,omitempty"`
}

type registerResponse struct {
	RunnerID string `json:"runner_id"`
	// Credential is set when the request presented a registration token:
	// the runner keeps it and sends it from then on.
	Credential string `json:"credential,omitempty"`
}

type claimRequest struct {
	Max int `json:"max"`
	// WaitMillis is how long the server may hold the request while no run
	// is queued; zero answers at once.
	WaitMillis int64 `json:"wait_ms"`
}

type claimResponse struct {
	Assignments []assignment `json:"assignments"`
}

type assignment struct {
	RunID     uuid.UUID       `json:"run_id"`
	TaskID    uuid.UUID       `json:"task_id"`
	VersionID uuid.UUID       `json:"version_id"`
	Manifest  manifest        `json:"manifest"`
	Params    json.RawMessage `json:"params"`
	// HasInput says the run has an input to fetch from runs/{id}/input;
	// added within protocol version 1 as an optional field.
	HasInput bool `json:"has_input,omitempty"`
	HasState bool `json:"has_state,omitempty"`
}

type manifest struct {
	Language      string   `json:"language"`
	Version       string   `json:"version"`
	Lockfile      string   `json:"lockfile,omitempty"`
	Entrypoint    []string `json:"entrypoint"`
	Stages        []stage  `json:"stages,omitempty"`
	TimeoutMillis int64    `json:"timeout_ms"`
	MilliCPUs     int64    `json:"milli_cpus"`
	MemoryBytes   int64    `json:"memory_bytes"`
	Network       string   `json:"network"`
	// Credentials are the manifest's credential references; added within
	// protocol version 1 as an optional field.
	Credentials []credentialRef `json:"credentials,omitempty"`
	// Tools are the referenced registered tools; added within protocol
	// version 1 as an optional field.
	Tools []tool `json:"tools,omitempty"`
}

type tool struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	URL  string `json:"url"`
	// TreeHash and Files are for script tools; added within protocol
	// version 1 as optional fields. An older runner ignores them and runs
	// without the tool's files.
	TreeHash string `json:"tree_hash,omitempty"`
	Files    []file `json:"files,omitempty"`
}

type credentialRef struct {
	Name     string `json:"name"`
	As       string `json:"as"`
	Optional bool   `json:"optional,omitempty"`
}

// secretsResponse carries a run's credential values. It is the only
// protocol message with plaintext credentials; the server sends it with
// Cache-Control: no-store, and the runner keeps it in memory only.
type secretsResponse struct {
	Secrets []secret `json:"secrets"`
}

type secret struct {
	Name   string        `json:"name"`
	As     string        `json:"as"`
	Fields []secretField `json:"fields"`
}

type secretField struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive"`
}

type stage struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	After []string `json:"after,omitempty"`
}

type heartbeatRequest struct {
	Active []uuid.UUID `json:"active"`
}

type heartbeatResponse struct {
	Cancel []uuid.UUID `json:"cancel"`
	Lost   []uuid.UUID `json:"lost"`
}

// inputResponse carries a run's input, such as a webhook body.
type inputResponse struct {
	ContentType string `json:"content_type,omitempty"`
	// Content is base64 in JSON.
	Content []byte `json:"content"`
}

type filesResponse struct {
	Files []file `json:"files"`
}

type file struct {
	Path string `json:"path"`
	// Content is base64 in JSON.
	Content    []byte `json:"content"`
	Executable bool   `json:"executable,omitempty"`
}

type startedRequest struct {
	EnvID string    `json:"env_id,omitempty"`
	At    time.Time `json:"at"`
}

type logsRequest struct {
	Lines        []logLine `json:"lines"`
	DroppedLines int64     `json:"dropped_lines"`
	DroppedBytes int64     `json:"dropped_bytes"`
}

type logLine struct {
	Seq    int64     `json:"seq"`
	TS     time.Time `json:"ts"`
	Stream string    `json:"stream"`
	Stage  string    `json:"stage,omitempty"`
	Text   string    `json:"text"`
}

type eventsRequest struct {
	Events []event `json:"events"`
}

type event struct {
	Seq     int64     `json:"seq"`
	TS      time.Time `json:"ts"`
	Kind    string    `json:"kind"`
	Stage   string    `json:"stage,omitempty"`
	Message string    `json:"message,omitempty"`
	Result  *result   `json:"result,omitempty"`
	// ToolCall is set for tool.call events; added within protocol
	// version 1. A server that predates it rejects the event, so servers
	// are upgraded before runners.
	ToolCall *toolCall `json:"tool_call,omitempty"`
}

type toolCall struct {
	Tool           string `json:"tool"`
	Method         string `json:"method"`
	OK             bool   `json:"ok"`
	DurationMillis int64  `json:"duration_ms"`
	ArgsSummary    string `json:"args_summary,omitempty"`
	Error          string `json:"error,omitempty"`
}

type result struct {
	Status  string          `json:"status"`
	Summary string          `json:"summary"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type finishRequest struct {
	Status       string    `json:"status"`
	ExitCode     *int      `json:"exit_code"`
	ErrorCode    string    `json:"error_code,omitempty"`
	ErrorStage   string    `json:"error_stage,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	FinishedAt   time.Time `json:"finished_at"`
}
