package runnerhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/events"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
)

// claimPoll is how often a held claim request tries again without an
// event, covering runs queued on servers whose events were missed.
const claimPoll = 2 * time.Second

// Options configures Handler.
type Options struct {
	Coordinator *service.Coordinator
	Runners     *service.Runners
	// Events, when set, wakes held claim requests as soon as a run is
	// queued; without it they poll.
	Events events.Bus
	Logger *slog.Logger
}

type handler struct {
	coord   *service.Coordinator
	runners *service.Runners
	bus     events.Bus
	log     *slog.Logger
	poll    time.Duration
	// draining is closed when the server shuts down, so held claims
	// answer at once instead of delaying the shutdown.
	draining  chan struct{}
	drainOnce sync.Once
}

// Handler serves the runner protocol.
type Handler struct {
	http.Handler
	h *handler
}

// Drain makes held claim requests return now and new ones return without
// waiting. Call it when the server starts shutting down, for example from
// http.Server.RegisterOnShutdown.
func (h *Handler) Drain() {
	h.h.drainOnce.Do(func() { close(h.h.draining) })
}

// NewHandler returns the server side of the protocol. Mount it at
// BasePath.
func NewHandler(opts Options) *Handler {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	h := &handler{coord: opts.Coordinator, runners: opts.Runners, bus: opts.Events, log: log.With("component", "runner_api"),
		poll: claimPoll, draining: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+pathRegister, h.register)
	mux.Handle("POST "+pathClaim, h.authed(h.claim))
	mux.Handle("POST "+pathBeat, h.authed(h.heartbeat))
	mux.Handle("GET "+BasePath+"versions/{version}/files", h.authed(h.files))
	mux.Handle("POST "+BasePath+"runs/{run}/secrets", h.authed(h.secrets))
	mux.Handle("POST "+BasePath+"runs/{run}/input", h.authed(h.input))
	mux.Handle("GET "+BasePath+"runs/{run}/state", h.authed(h.state))
	mux.Handle("PUT "+BasePath+"runs/{run}/state", h.authed(h.state))
	mux.Handle("POST "+BasePath+"runs/{run}/started", h.authed(h.started))
	mux.Handle("POST "+BasePath+"runs/{run}/logs", h.authed(h.logs))
	mux.Handle("POST "+BasePath+"runs/{run}/events", h.authed(h.events))
	mux.Handle("POST "+BasePath+"runs/{run}/finish", h.authed(h.finish))
	mux.Handle("POST "+BasePath+"runs/{run}"+artifactSuffix, h.authed(h.artifact))
	return &Handler{Handler: h.protocol(mux), h: h}
}

// protocol rejects runners that speak a protocol version this server does
// not, and bounds request bodies.
func (h *handler) protocol(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, err := strconv.Atoi(r.Header.Get(ProtocolHeader))
		if err != nil {
			h.fail(w, r, fmt.Errorf("%w: missing or invalid %s header", service.ErrProtocol, ProtocolHeader))
			return
		}
		if err := service.CheckProtocol(v); err != nil {
			h.fail(w, r, err)
			return
		}
		if !isArtifactUpload(r) {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// authed resolves the bearer credential to a runner ID.
func (h *handler) authed(next func(w http.ResponseWriter, r *http.Request, runnerID string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := h.runners.Authenticate(r.Context(), bearer(r))
		if err != nil {
			h.fail(w, r, err)
			return
		}
		next(w, r, id)
	})
}

func bearer(r *http.Request) string {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(token)
}

func protocolVersion(r *http.Request) int {
	v, _ := strconv.Atoi(r.Header.Get(ProtocolHeader))
	return v
}

func (h *handler) register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !h.decode(w, r, &req) {
		return
	}
	reg := runproto.Registration{ID: req.ID, Name: req.Name, Slots: req.Slots, Labels: req.Labels, Version: req.Version}
	token := bearer(r)
	if strings.HasPrefix(token, service.RunnerCredentialPrefix) {
		id, err := h.runners.Authenticate(r.Context(), token)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if id != reg.ID {
			h.fail(w, r, fmt.Errorf("%w: credential of runner %s used to register %q", service.ErrForbidden, id, reg.ID))
			return
		}
		if err := h.runners.Update(r.Context(), reg, protocolVersion(r)); err != nil {
			h.fail(w, r, err)
			return
		}
		h.reply(w, registerResponse{RunnerID: id})
		return
	}
	credential, err := h.runners.Exchange(r.Context(), token, reg, protocolVersion(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.reply(w, registerResponse{RunnerID: reg.ID, Credential: credential})
}

// claim hands out queued runs. While none is queued it holds the request
// for up to the requested wait (long polling), so idle runners do not
// have to ask every second.
func (h *handler) claim(w http.ResponseWriter, r *http.Request, runnerID string) {
	if protocolVersion(r) < runproto.StateProtocolVersion {
		h.fail(w, r, fmt.Errorf("%w: claiming runs requires runner protocol %d", service.ErrProtocol, runproto.StateProtocolVersion))
		return
	}
	var req claimRequest
	if !h.decode(w, r, &req) {
		return
	}
	wait := min(time.Duration(max(req.WaitMillis, 0))*time.Millisecond, MaxClaimWait)
	var queued <-chan events.Event
	if h.bus != nil && wait > 0 {
		// Subscribe before the first attempt, so a run queued in between
		// is not missed.
		ch, stop := h.bus.Subscribe()
		defer stop()
		queued = ch
	}
	ctx := r.Context()
	deadline := time.Now().Add(wait)
	for {
		as, err := h.coord.Claim(ctx, runnerID, min(req.Max, maxClaimBatch))
		if err != nil {
			h.fail(w, r, err)
			return
		}
		remaining := time.Until(deadline)
		if len(as) > 0 || remaining <= 0 || h.isDraining() {
			resp := claimResponse{Assignments: make([]assignment, len(as))}
			for i, a := range as {
				resp.Assignments[i] = toWireAssignment(a)
			}
			if len(as) > 0 && ctx.Err() != nil {
				// The runner went away after the runs were assigned. They
				// are not lost for good: their leases expire and the runs
				// are marked interrupted.
				h.log.WarnContext(ctx, "runner disconnected while claiming; its runs will be interrupted when their leases expire",
					"runner_id", runnerID, "runs", len(as))
			}
			h.reply(w, resp)
			return
		}
		if !waitForQueued(ctx, queued, h.draining, min(remaining, h.poll)) {
			return
		}
	}
}

func (h *handler) isDraining() bool {
	select {
	case <-h.draining:
		return true
	default:
		return false
	}
}

// waitForQueued waits for a run.queued event, for draining or for d, and
// reports false when the request ended.
func waitForQueued(ctx context.Context, queued <-chan events.Event, draining <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-draining:
			return true
		case <-t.C:
			return true
		case e := <-queued:
			if e.Kind == events.RunQueued {
				return true
			}
		}
	}
}

func (h *handler) heartbeat(w http.ResponseWriter, r *http.Request, runnerID string) {
	var req heartbeatRequest
	if !h.decode(w, r, &req) {
		return
	}
	reply, err := h.coord.Heartbeat(r.Context(), runnerID, req.Active)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.reply(w, heartbeatResponse{Cancel: nonNil(reply.Cancel), Lost: nonNil(reply.Lost)})
}

func nonNil(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}

func (h *handler) files(w http.ResponseWriter, r *http.Request, runnerID string) {
	version, err := uuid.Parse(r.PathValue("version"))
	if err != nil {
		h.fail(w, r, fmt.Errorf("%w: invalid version ID", service.ErrProtocol))
		return
	}
	files, err := h.coord.FilesFor(r.Context(), runnerID, version)
	if errors.Is(err, service.ErrNotFound) {
		// The runner has no active run of this version (any more).
		err = runproto.ErrLeaseLost
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	resp := filesResponse{Files: make([]file, len(files))}
	for i, f := range files {
		resp.Files[i] = file(f)
	}
	h.reply(w, resp)
}

// runCall decodes a call about one run.
func (h *handler) runCall(w http.ResponseWriter, r *http.Request, v any) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("run"))
	if err != nil {
		h.fail(w, r, fmt.Errorf("%w: invalid run ID", service.ErrProtocol))
		return uuid.Nil, false
	}
	return id, h.decode(w, r, v)
}

// secrets hands out a run's credential values. The coordinator answers
// only for a run that is assigned to the calling runner and still
// preparing (runproto.ErrLeaseLost otherwise), so the bearer credential
// alone cannot fetch another runner's or another run's values.
func (h *handler) secrets(w http.ResponseWriter, r *http.Request, runnerID string) {
	var req struct{}
	id, ok := h.runCall(w, r, &req)
	if !ok {
		return
	}
	secrets, err := h.coord.Secrets(r.Context(), runnerID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.reply(w, secretsResponse{Secrets: toWireSecrets(secrets)})
}

// input hands out a run's input. Like secrets, the coordinator answers
// only for a run assigned to the calling runner.
func (h *handler) input(w http.ResponseWriter, r *http.Request, runnerID string) {
	var req struct{}
	id, ok := h.runCall(w, r, &req)
	if !ok {
		return
	}
	in, err := h.coord.Input(r.Context(), runnerID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.reply(w, inputResponse{ContentType: in.ContentType, Content: in.Content})
}

func (h *handler) started(w http.ResponseWriter, r *http.Request, runnerID string) {
	var req startedRequest
	id, ok := h.runCall(w, r, &req)
	if !ok {
		return
	}
	h.done(w, r, h.coord.Started(r.Context(), runnerID, id, req.EnvID, req.At))
}

func (h *handler) logs(w http.ResponseWriter, r *http.Request, runnerID string) {
	var req logsRequest
	id, ok := h.runCall(w, r, &req)
	if !ok {
		return
	}
	dropped := runproto.Dropped{Lines: req.DroppedLines, Bytes: req.DroppedBytes}
	h.done(w, r, h.coord.AppendLogs(r.Context(), runnerID, id, fromWireLines(req.Lines), dropped))
}

func (h *handler) events(w http.ResponseWriter, r *http.Request, runnerID string) {
	var req eventsRequest
	id, ok := h.runCall(w, r, &req)
	if !ok {
		return
	}
	h.done(w, r, h.coord.AppendEvents(r.Context(), runnerID, id, fromWireEvents(req.Events)))
}

func (h *handler) finish(w http.ResponseWriter, r *http.Request, runnerID string) {
	var req finishRequest
	id, ok := h.runCall(w, r, &req)
	if !ok {
		return
	}
	h.done(w, r, h.coord.Finish(r.Context(), runnerID, id, runproto.Finish(req)))
}

func (h *handler) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		h.fail(w, r, fmt.Errorf("%w: request body: %w", service.ErrProtocol, err))
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		h.fail(w, r, fmt.Errorf("%w: request body has data after the JSON value", service.ErrProtocol))
		return false
	}
	return true
}

func (h *handler) done(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

// fail answers with the protocol error for err. Messages of rejected
// calls go back to the runner, which logs them; they name runs and
// counts, never credentials or log content. Internal errors are logged
// here and answered generically.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, body := http.StatusServiceUnavailable, errorBody{Code: codeUnavailable, Message: "server error; retry later"}
	var tooLarge *http.MaxBytesError
	var invalid *service.ValidationError
	var cred *runproto.CredentialError
	switch {
	case errors.As(err, &cred):
		status, body = http.StatusUnprocessableEntity, errorBody{Code: codeCredential, CredentialCode: cred.Code, Message: cred.Message}
	case errors.Is(err, runproto.ErrLeaseLost):
		status, body = http.StatusConflict, errorBody{Code: codeLeaseLost, Message: err.Error()}
	case errors.Is(err, service.ErrRunnerIDInUse):
		status, body = http.StatusConflict, errorBody{Code: codeRunnerIDInUse, Message: err.Error()}
	case errors.As(err, &tooLarge):
		status, body = http.StatusRequestEntityTooLarge, errorBody{Code: codeRejected, Message: "request body too large"}
	case errors.Is(err, service.ErrProtocol), errors.As(err, &invalid):
		status, body = http.StatusUnprocessableEntity, errorBody{Code: codeRejected, Message: err.Error()}
	case errors.Is(err, service.ErrUnauthenticated):
		status, body = http.StatusUnauthorized, errorBody{Code: codeUnauthenticated, Message: "runner credential or registration token is missing, wrong, expired or revoked"}
	case errors.Is(err, service.ErrForbidden):
		status, body = http.StatusForbidden, errorBody{Code: codeForbidden, Message: err.Error()}
	case r.Context().Err() != nil:
		// The runner went away; nobody reads the answer.
		return
	default:
		h.log.ErrorContext(r.Context(), "runner call failed", "path", r.URL.Path, "error", err)
	}
	if status != http.StatusServiceUnavailable && status != http.StatusConflict && body.Code != codeCredential {
		h.log.WarnContext(r.Context(), "runner call refused", "path", r.URL.Path, "status", status, "code", body.Code)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
