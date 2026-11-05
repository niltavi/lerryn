package runnerhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
)

// Artifacts travel as the raw request body rather than JSON, so a large
// file streams from the runner's disk to the object store without being
// held in memory or base64-encoded. The path is a query parameter; the
// size is the Content-Length.
const artifactSuffix = "/artifacts"

// artifactRate is the slowest upload speed the client allows for before
// it gives up on a request.
const artifactRate = 1 << 20 // bytes per second

func (h *handler) artifact(w http.ResponseWriter, r *http.Request, runnerID string) {
	id, err := uuid.Parse(r.PathValue("run"))
	if err != nil {
		h.fail(w, r, fmt.Errorf("%w: invalid run ID", service.ErrProtocol))
		return
	}
	size := r.ContentLength
	if size < 0 {
		h.fail(w, r, fmt.Errorf("%w: an artifact upload needs Content-Length", service.ErrProtocol))
		return
	}
	// The service checks the limits and that the body has exactly size
	// bytes; this only stops a body from running past them.
	body := http.MaxBytesReader(w, r.Body, runproto.MaxArtifactBytes+1)
	h.done(w, r, h.coord.AddArtifact(r.Context(), runnerID, id, runproto.Artifact{
		Path: r.URL.Query().Get("path"), Size: size, Content: body,
	}))
}

// isArtifactUpload reports whether r is an artifact upload, whose body
// limit is the artifact limit rather than maxRequestBytes.
func isArtifactUpload(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, BasePath+"runs/") && strings.HasSuffix(r.URL.Path, artifactSuffix)
}

// AddArtifact implements runproto.Coordinator.
func (c *Client) AddArtifact(ctx context.Context, _ string, runID uuid.UUID, a runproto.Artifact) error {
	path := runPath(runID, "artifacts") + "?path=" + url.QueryEscape(a.Path)
	err := c.upload(ctx, path, c.currentCredential(), a)
	if errors.Is(err, errUnauthenticated) && c.regToken != "" {
		// The body is spent; register again so the runner's next attempt,
		// which reopens the file, goes through.
		c.mu.Lock()
		reg := c.reg
		c.mu.Unlock()
		if reg != nil {
			if rerr := c.exchange(ctx, *reg); rerr != nil {
				return fmt.Errorf("%w: %w", err, rerr)
			}
		}
	}
	return err
}

func (c *Client) upload(ctx context.Context, path, token string, a runproto.Artifact) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout+time.Duration(a.Size/artifactRate)*time.Second)
	defer cancel()
	// path carries a query, so it is appended to the URL rather than to
	// its Path, which would escape the '?'.
	full, err := url.Parse(c.base.String() + path)
	if err != nil {
		return fmt.Errorf("%w: %w", runproto.ErrRejected, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, full.String(), io.LimitReader(a.Content, a.Size))
	if err != nil {
		return fmt.Errorf("%w: %w", runproto.ErrRejected, err)
	}
	req.ContentLength = a.Size
	if a.Size == 0 {
		req.Body = http.NoBody
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(ProtocolHeader, strconv.Itoa(clientProtocol))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := readResponseBody(resp, maxResponseBytes)
	if err != nil {
		return fmt.Errorf("POST %s: read response: %w", path, err)
	}
	if resp.StatusCode >= 300 {
		return responseError(http.MethodPost, path, resp.StatusCode, data)
	}
	return nil
}
