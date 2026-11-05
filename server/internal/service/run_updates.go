package service

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
)

// visibilityTTL is how long an update stream trusts the project list it
// read; a member removed from a project stops receiving its updates
// within this time. The list is reread only when an update arrives, so a
// short TTL costs at most one query per TTL per open stream.
const visibilityTTL = 10 * time.Second

// UpdateStream delivers the run updates one caller may see.
type UpdateStream struct {
	r       *Runs
	p       *auth.Principal
	sub     *Subscription
	project *uuid.UUID
	task    *uuid.UUID
	allowed []uuid.UUID
	read    time.Time
	cred    *credentialGuard
}

// StreamUpdates subscribes p to updates of the runs it can view, in one
// project or task when given. Close the stream when done.
func (r *Runs) StreamUpdates(ctx context.Context, p *auth.Principal, project, task *uuid.UUID) (*UpdateStream, error) {
	if task != nil {
		var projectID uuid.UUID
		err := r.db.NewSelect().Table("tasks").Column("project_id").Where("id = ?", *task).Scan(ctx, &projectID)
		if err != nil {
			return nil, ErrNotFound
		}
		if err := r.authz.Require(ctx, p, projectID, ActionViewTask); err != nil {
			return nil, err
		}
	}
	s := &UpdateStream{r: r, p: p, project: project, task: task, cred: newCredentialGuard(r.db, p)}
	if err := s.refresh(ctx); err != nil {
		return nil, err
	}
	s.sub = r.feed.subscribe()
	return s, nil
}

func (s *UpdateStream) refresh(ctx context.Context) error {
	allowed, err := s.r.authz.Visible(ctx, s.p, ActionViewTask, s.project)
	if err != nil {
		return err
	}
	s.allowed, s.read = allowed, time.Now()
	return nil
}

// Next returns the next update the caller may see, waiting up to wait.
// It returns resync when updates were dropped and the client must read
// its lists again, and neither when wait passed without an update. It
// returns ErrUnauthenticated once the caller's session or token stops
// being valid, also while no update arrives.
func (s *UpdateStream) Next(ctx context.Context, wait time.Duration) (u *RunUpdate, resync bool, err error) {
	if err := s.cred.check(ctx); err != nil {
		return nil, false, err
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	// Role bindings survive logout, revocation and disable, so the
	// credential is rechecked on its own clock, not only on updates.
	recheck := time.NewTicker(max(credentialRecheck, time.Millisecond))
	defer recheck.Stop()
	for {
		if s.sub.Overflowed() {
			return nil, true, nil
		}
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-timer.C:
			return nil, false, nil
		case <-recheck.C:
			if err := s.cred.check(ctx); err != nil {
				return nil, false, err
			}
		case next := <-s.sub.C:
			if err := s.cred.check(ctx); err != nil {
				return nil, false, err
			}
			if time.Since(s.read) > visibilityTTL {
				if err := s.refresh(ctx); err != nil {
					return nil, false, err
				}
			}
			if s.task != nil && next.TaskID != *s.task {
				continue
			}
			if !slices.Contains(s.allowed, next.ProjectID) {
				continue
			}
			return &next, false, nil
		}
	}
}

// Close ends the stream.
func (s *UpdateStream) Close() { s.sub.Close() }
