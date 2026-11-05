package runnerhttp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
)

func (h *handler) state(w http.ResponseWriter, r *http.Request, runnerID string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if protocolVersion(r) < runproto.StateProtocolVersion {
		h.fail(w, r, fmt.Errorf("%w: task state requires runner protocol %d", service.ErrProtocol, runproto.StateProtocolVersion))
		return
	}
	id, err := uuid.Parse(r.PathValue("run"))
	if err != nil {
		h.fail(w, r, fmt.Errorf("%w: invalid run ID", service.ErrProtocol))
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		snapshot, err := h.coord.GetState(r.Context(), runnerID, id)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		data, _, err := runproto.EncodeState(snapshot)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data) // #nosec G705 -- canonical JSON is served with application/json and nosniff, never as HTML
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, int64(runproto.MaxStateWireBytes)+1))
	err = errors.Join(err, r.Body.Close())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	snapshot, _, err := runproto.DecodeState(data)
	if err != nil {
		h.fail(w, r, fmt.Errorf("%w: invalid task state: %w", service.ErrProtocol, err))
		return
	}
	h.done(w, r, h.coord.PutState(r.Context(), runnerID, id, snapshot))
}

func (c *Client) GetState(ctx context.Context, _ string, runID uuid.UUID) (runproto.StateSnapshot, error) {
	data, err := c.callState(ctx, http.MethodGet, runPath(runID, "state"), nil)
	if err != nil {
		return runproto.StateSnapshot{}, err
	}
	snapshot, _, err := runproto.DecodeState(data)
	if err != nil {
		return runproto.StateSnapshot{}, fmt.Errorf("%w: invalid task state response: %w", runproto.ErrRejected, err)
	}
	return snapshot, nil
}

func (c *Client) PutState(ctx context.Context, _ string, runID uuid.UUID, snapshot runproto.StateSnapshot) error {
	data, _, err := runproto.EncodeState(snapshot)
	if err != nil {
		return fmt.Errorf("%w: invalid task state: %w", runproto.ErrRejected, err)
	}
	_, err = c.callState(ctx, http.MethodPut, runPath(runID, "state"), data)
	return err
}

func (c *Client) callState(ctx context.Context, method, path string, data []byte) ([]byte, error) {
	response, err := c.requestState(ctx, method, path, c.currentCredential(), data)
	if !errors.Is(err, errUnauthenticated) || c.regToken == "" {
		return response, err
	}
	c.mu.Lock()
	reg := c.reg
	c.mu.Unlock()
	if reg == nil {
		return nil, err
	}
	if registerErr := c.exchange(ctx, *reg); registerErr != nil {
		return nil, errors.Join(err, registerErr)
	}
	// Canonical bytes are immutable, so a credential refresh can replay
	// the complete body even after the first request consumed it.
	return c.requestState(ctx, method, path, c.currentCredential(), data)
}

func (c *Client) requestState(ctx context.Context, method, path, token string, data []byte) (response []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u := *c.base
	u.Path += path
	request, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: create task state request: %w", runproto.ErrRejected, err)
	}
	request.Header.Set(ProtocolHeader, strconv.Itoa(clientProtocol))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if data != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	reply, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s task state: %w", method, err)
	}
	defer func() { err = errors.Join(err, reply.Body.Close()) }()
	response, err = readResponseBody(reply, runproto.MaxStateWireBytes)
	if err != nil {
		return nil, fmt.Errorf("read task state response: %w", err)
	}
	if reply.StatusCode >= 300 {
		return nil, responseError(method, path, reply.StatusCode, response)
	}
	wantStatus := http.StatusOK
	if method == http.MethodPut {
		wantStatus = http.StatusNoContent
	}
	if reply.StatusCode != wantStatus {
		return nil, fmt.Errorf("%w: unexpected task state response status %d", runproto.ErrRejected, reply.StatusCode)
	}
	return response, nil
}
