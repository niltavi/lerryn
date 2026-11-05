package runnerhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

// Client defaults.
const (
	// DefaultClaimWait is how long a claim may be held by the server.
	DefaultClaimWait = 10 * time.Second
	// requestTimeout bounds every call except the extra wait of a claim.
	requestTimeout = 60 * time.Second
)

// clientProtocol is the protocol version this client speaks.
const clientProtocol = runproto.StateProtocolVersion

// ClientOptions configures Client.
type ClientOptions struct {
	// ServerURL is the server's base URL, such as https://lerryn.example.com.
	ServerURL string
	// RegistrationToken is exchanged for a credential when the runner has
	// none yet or the server no longer accepts it. It may be empty once a
	// credential is stored.
	RegistrationToken string
	// CredentialFile is where the credential is kept between restarts,
	// readable by the runner's user only.
	CredentialFile string
	// HTTPClient sends the requests; nil means a client with the default
	// transport.
	HTTPClient *http.Client
	// ClaimWait is how long the server may hold a claim while no run is
	// queued. Zero means DefaultClaimWait.
	ClaimWait time.Duration
	Logger    *slog.Logger
}

// Client is the runner side of the protocol over HTTP. It implements
// runproto.Coordinator: ErrLeaseLost and ErrRejected come back as such, and
// every other error is transient.
type Client struct {
	base      *url.URL
	regToken  string
	credFile  string
	http      *http.Client
	claimWait time.Duration
	log       *slog.Logger

	mu         sync.Mutex
	credential string
	// reg is the last registration, repeated when the credential must be
	// renewed.
	reg *runproto.Registration
}

var _ runproto.Coordinator = (*Client)(nil)

// NewClient returns a client and loads the stored credential, if any.
func NewClient(opts ClientOptions) (*Client, error) {
	u, err := url.Parse(opts.ServerURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("runner server URL must be an absolute http or https URL, got %q", opts.ServerURL)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	if opts.CredentialFile == "" {
		return nil, errors.New("runner credential file is required")
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	c := &Client{
		base:      u,
		regToken:  strings.TrimSpace(opts.RegistrationToken),
		credFile:  opts.CredentialFile,
		http:      hc,
		claimWait: opts.ClaimWait,
		log:       log,
	}
	if c.claimWait == 0 {
		c.claimWait = DefaultClaimWait
	}
	data, err := os.ReadFile(opts.CredentialFile) // #nosec G304 -- the operator chooses the data directory
	switch {
	case err == nil:
		c.credential = strings.TrimSpace(string(data))
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, fmt.Errorf("read runner credential: %w", err)
	}
	if c.credential == "" && c.regToken == "" {
		return nil, errors.New("runner has no stored credential; configure runner.registration_token_file")
	}
	return c, nil
}

// errUnauthenticated marks a 401 answer, which a fresh credential may fix.
var errUnauthenticated = errors.New("runner is not authenticated")

// Register implements runproto.Coordinator. With a stored credential it
// updates the runner's details; without one, or when the server no
// longer accepts it, it exchanges the registration token for a new
// credential and stores it.
func (c *Client) Register(ctx context.Context, r runproto.Registration) error {
	c.mu.Lock()
	reg := r
	c.reg = &reg
	c.mu.Unlock()
	if cred := c.currentCredential(); cred != "" {
		err := c.postJSON(ctx, pathRegister, cred, registerRequest{ID: r.ID, Name: r.Name, Slots: r.Slots, Labels: r.Labels, Version: r.Version}, nil)
		if !errors.Is(err, errUnauthenticated) {
			return err
		}
		c.log.WarnContext(ctx, "the server no longer accepts this runner's credential; registering again")
	}
	return c.exchange(ctx, r)
}

func (c *Client) exchange(ctx context.Context, r runproto.Registration) error {
	if c.regToken == "" {
		return fmt.Errorf("%w: the server does not accept the stored credential and no registration token is configured", runproto.ErrRejected)
	}
	var resp registerResponse
	err := c.postJSON(ctx, pathRegister, c.regToken, registerRequest{ID: r.ID, Name: r.Name, Slots: r.Slots, Labels: r.Labels, Version: r.Version}, &resp)
	if errors.Is(err, errUnauthenticated) {
		return fmt.Errorf("%w: the server does not accept the registration token", runproto.ErrRejected)
	}
	if err != nil {
		return err
	}
	if !strings.HasPrefix(resp.Credential, "wl_") {
		return errors.New("register: the server returned no credential")
	}
	if err := writeCredential(c.credFile, resp.Credential); err != nil {
		return err
	}
	c.mu.Lock()
	c.credential = resp.Credential
	c.mu.Unlock()
	c.log.InfoContext(ctx, "runner registered with the server", "runner_id", resp.RunnerID)
	return nil
}

// writeCredential stores the credential atomically, readable by the owner
// only.
func writeCredential(path, credential string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("store runner credential: %w", err)
	}
	f, err := os.CreateTemp(dir, ".runner-credential-*")
	if err != nil {
		return fmt.Errorf("store runner credential: %w", err)
	}
	tmp := f.Name()
	_, werr := f.WriteString(credential + "\n")
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("store runner credential: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("store runner credential: %w", err)
	}
	return nil
}

func (c *Client) currentCredential() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.credential
}

// Claim implements runproto.Coordinator. The server may hold the call for
// the client's claim wait while no run is queued.
func (c *Client) Claim(ctx context.Context, _ string, maxRuns int) ([]runproto.Assignment, error) {
	var resp claimResponse
	req := claimRequest{Max: min(maxRuns, maxClaimBatch), WaitMillis: c.claimWait.Milliseconds()}
	if err := c.call(ctx, http.MethodPost, pathClaim, req, &resp, c.claimWait); err != nil {
		return nil, err
	}
	out := make([]runproto.Assignment, len(resp.Assignments))
	for i, a := range resp.Assignments {
		out[i] = fromWireAssignment(a)
	}
	return out, nil
}

// Heartbeat implements runproto.Coordinator.
func (c *Client) Heartbeat(ctx context.Context, _ string, active []uuid.UUID) (runproto.HeartbeatReply, error) {
	var resp heartbeatResponse
	if active == nil {
		active = []uuid.UUID{}
	}
	if err := c.call(ctx, http.MethodPost, pathBeat, heartbeatRequest{Active: active}, &resp, 0); err != nil {
		return runproto.HeartbeatReply{}, err
	}
	return runproto.HeartbeatReply{Cancel: resp.Cancel, Lost: resp.Lost}, nil
}

// Files implements runproto.Coordinator. The server hands out the files
// only while the runner has an active run of the version.
func (c *Client) Files(ctx context.Context, versionID uuid.UUID) ([]runproto.File, error) {
	var resp filesResponse
	if err := c.call(ctx, http.MethodGet, filesPath(versionID), nil, &resp, 0); err != nil {
		return nil, err
	}
	out := make([]runproto.File, len(resp.Files))
	for i, f := range resp.Files {
		out[i] = runproto.File(f)
	}
	return out, nil
}

// Secrets implements runproto.Coordinator. The values arrive over the
// server connection, which must be HTTPS outside a single host, and stay
// in memory.
func (c *Client) Secrets(ctx context.Context, _ string, runID uuid.UUID) ([]runproto.Secret, error) {
	var resp secretsResponse
	if err := c.call(ctx, http.MethodPost, runPath(runID, "secrets"), struct{}{}, &resp, 0); err != nil {
		return nil, err
	}
	return fromWireSecrets(resp.Secrets), nil
}

// Input implements runproto.Coordinator.
func (c *Client) Input(ctx context.Context, _ string, runID uuid.UUID) (runproto.Input, error) {
	var resp inputResponse
	if err := c.call(ctx, http.MethodPost, runPath(runID, "input"), struct{}{}, &resp, 0); err != nil {
		return runproto.Input{}, err
	}
	return runproto.Input{ContentType: resp.ContentType, Content: resp.Content}, nil
}

// Started implements runproto.Coordinator.
func (c *Client) Started(ctx context.Context, _ string, runID uuid.UUID, envID string, at time.Time) error {
	return c.call(ctx, http.MethodPost, runPath(runID, "started"), startedRequest{EnvID: envID, At: at}, nil, 0)
}

// AppendLogs implements runproto.Coordinator.
func (c *Client) AppendLogs(ctx context.Context, _ string, runID uuid.UUID, lines []runproto.LogLine, dropped runproto.Dropped) error {
	req := logsRequest{Lines: toWireLines(lines), DroppedLines: dropped.Lines, DroppedBytes: dropped.Bytes}
	return c.call(ctx, http.MethodPost, runPath(runID, "logs"), req, nil, 0)
}

// AppendEvents implements runproto.Coordinator.
func (c *Client) AppendEvents(ctx context.Context, _ string, runID uuid.UUID, events []runproto.Event) error {
	const prefix = `{"events":[`
	const suffix = `]}`
	path := runPath(runID, "events")
	batch := []byte(prefix)
	count := 0
	for _, e := range toWireEvents(events) {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := encodeRequest(path, e)
		if err != nil {
			return err
		}
		if len(prefix)+len(data)+len(suffix) > maxRequestBytes {
			return fmt.Errorf("%w: encode %s: event %d is larger than the request limit of %d bytes", runproto.ErrRejected, path, e.Seq, maxRequestBytes)
		}
		// A final flush can contain hundreds of individually valid 64-KiB
		// results. Split by encoded bytes, including metadata and escaping,
		// so their count does not make the HTTP request permanently fail.
		if count > 0 && len(batch)+1+len(data)+len(suffix) > maxRequestBytes {
			if err := c.callJSON(ctx, http.MethodPost, path, append(batch, suffix...), nil, 0); err != nil {
				return err
			}
			batch = []byte(prefix)
			count = 0
		}
		if count > 0 {
			batch = append(batch, ',')
		}
		batch = append(batch, data...)
		count++
	}
	if count == 0 {
		return ctx.Err()
	}
	return c.callJSON(ctx, http.MethodPost, path, append(batch, suffix...), nil, 0)
}

// Finish implements runproto.Coordinator.
func (c *Client) Finish(ctx context.Context, _ string, runID uuid.UUID, f runproto.Finish) error {
	return c.call(ctx, http.MethodPost, runPath(runID, "finish"), finishRequest(f), nil, 0)
}

// call sends an authenticated request. When the server no longer accepts
// the credential and a registration token is configured, it registers
// again once and repeats the request.
func (c *Client) call(ctx context.Context, method, path string, body, out any, extra time.Duration) error {
	data, err := encodeRequest(path, body)
	if err != nil {
		return err
	}
	return c.callJSON(ctx, method, path, data, out, extra)
}

// callJSON keeps the encoded body unchanged if registering again is needed.
func (c *Client) callJSON(ctx context.Context, method, path string, data []byte, out any, extra time.Duration) error {
	err := c.doJSON(ctx, method, path, c.currentCredential(), data, out, extra)
	if !errors.Is(err, errUnauthenticated) || c.regToken == "" {
		return err
	}
	c.mu.Lock()
	reg := c.reg
	c.mu.Unlock()
	if reg == nil {
		return err
	}
	if rerr := c.exchange(ctx, *reg); rerr != nil {
		return errors.Join(err, rerr)
	}
	return c.doJSON(ctx, method, path, c.currentCredential(), data, out, extra)
}

func (c *Client) postJSON(ctx context.Context, path, token string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, token, body, out, 0)
}

// do sends one request and decodes the answer into out when it is not
// nil.
func (c *Client) do(ctx context.Context, method, path, token string, body, out any, extra time.Duration) error {
	data, err := encodeRequest(path, body)
	if err != nil {
		return err
	}
	return c.doJSON(ctx, method, path, token, data, out, extra)
}

func encodeRequest(path string, body any) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	// Preserve v1 wire semantics. Both HTML escaping and JS separator
	// escaping expand valid raw result data past the server's byte limit.
	data, err := jsonv2.Marshal(body, json.DefaultOptionsV1(), jsontext.EscapeForHTML(false), jsontext.EscapeForJS(false))
	if err != nil {
		return nil, fmt.Errorf("%w: encode %s: %w", runproto.ErrRejected, path, err)
	}
	return data, nil
}

func (c *Client) doJSON(ctx context.Context, method, path, token string, data []byte, out any, extra time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout+extra)
	defer cancel()
	var rd io.Reader
	if data != nil {
		rd = bytes.NewReader(data)
	}
	u := *c.base
	u.Path += path
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return fmt.Errorf("%w: %w", runproto.ErrRejected, err)
	}
	if data != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(ProtocolHeader, strconv.Itoa(clientProtocol))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err = readResponseBody(resp, maxResponseBytes)
	if err != nil {
		return fmt.Errorf("%s %s: read response: %w", method, path, err)
	}
	if resp.StatusCode >= 300 {
		return responseError(method, path, resp.StatusCode, data)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, path, err)
	}
	return nil
}

// readResponseBody detects truncation even when the response is chunked or
// a proxy supplied an incorrect length. Never decode a silently cut body.
func readResponseBody(resp *http.Response, limit int64) ([]byte, error) {
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("response is larger than %d bytes", limit)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response is larger than %d bytes", limit)
	}
	return data, nil
}

// responseError maps a protocol error response to the errors the runner
// tells apart. Answers that are not protocol errors, such as a proxy's
// 502, are transient.
func responseError(method, path string, status int, data []byte) error {
	var body errorBody
	_ = json.Unmarshal(data, &body)
	msg := body.Message
	if msg == "" {
		msg = http.StatusText(status)
	}
	switch body.Code {
	case codeCredential:
		return &runproto.CredentialError{Code: body.CredentialCode, Message: body.Message}
	case codeLeaseLost:
		return fmt.Errorf("%s %s: %w", method, path, runproto.ErrLeaseLost)
	case codeRejected, codeForbidden:
		return fmt.Errorf("%s %s: %w: %s", method, path, runproto.ErrRejected, msg)
	case codeUnauthenticated:
		return fmt.Errorf("%s %s: %w", method, path, errUnauthenticated)
	}
	switch status {
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		// The server does not have this endpoint: another version, or not
		// a lerryn server at all. Retrying will not help.
		return fmt.Errorf("%s %s: %w: %s", method, path, runproto.ErrRejected, msg)
	}
	return fmt.Errorf("%s %s: server answered %d: %s", method, path, status, msg)
}
