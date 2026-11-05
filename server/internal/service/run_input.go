package service

import (
	"context"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

// Input implements runproto.Coordinator. Only the runner the run is
// assigned to, while the run is claimed, can read it: anything else is
// runproto.ErrLeaseLost, so a runner credential cannot read the webhook
// bodies of other runs. The content is checked against its hash, as task
// files are.
func (c *Coordinator) Input(ctx context.Context, runnerID string, runID uuid.UUID) (runproto.Input, error) {
	run, err := ownedRun(ctx, c.db, runnerID, runID)
	if err != nil {
		return runproto.Input{}, err
	}
	if run.InputKey == "" {
		return runproto.Input{}, fmt.Errorf("%w: run %s has no input", ErrProtocol, runID)
	}
	rc, err := c.blobs.Get(ctx, run.InputKey)
	if err != nil {
		return runproto.Input{}, fmt.Errorf("input of run %s: %w", runID, err)
	}
	defer func() { _ = rc.Close() }()
	content, err := io.ReadAll(io.LimitReader(rc, runproto.MaxInputBytes+1))
	if err != nil {
		return runproto.Input{}, fmt.Errorf("read input of run %s: %w", runID, err)
	}
	if len(content) > runproto.MaxInputBytes {
		return runproto.Input{}, fmt.Errorf("input of run %s is larger than %d bytes", runID, runproto.MaxInputBytes)
	}
	if blob.Sum(content) != run.InputKey {
		return runproto.Input{}, fmt.Errorf("input of run %s does not match its hash", runID)
	}
	return runproto.Input{ContentType: run.InputContentType, Content: content}, nil
}
