package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
)

// RunnerAdmin is what the runner administration operations need;
// *service.Runners implements it.
type RunnerAdmin interface {
	List(ctx context.Context, p *auth.Principal, in service.ListRunnersInput) ([]service.RunnerInfo, string, error)
	SetDisabled(ctx context.Context, p *auth.Principal, id string, disabled bool) (service.RunnerInfo, error)
	CreateRegistrationToken(ctx context.Context, p *auth.Principal, name string, ttl time.Duration) (string, service.RegistrationToken, error)
}

var errNoRunnerAdmin = errors.New("httpapi: no runner service configured")

// ListRunners lists the execution nodes.
func (h *handler) ListRunners(ctx context.Context, req ListRunnersRequestObject) (ListRunnersResponseObject, error) {
	if h.runnerAdmin == nil {
		return nil, errNoRunnerAdmin
	}
	in := service.ListRunnersInput{Limit: req.Params.Limit}
	if req.Params.Status != nil {
		in.Status = string(*req.Params.Status)
	}
	if req.Params.Cursor != nil {
		in.Cursor = *req.Params.Cursor
	}
	rows, next, err := h.runnerAdmin.List(ctx, auth.PrincipalFrom(ctx), in)
	if err != nil {
		return errorResponse[ListRunnersdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	page := ListRunners200JSONResponse{Items: make([]Runner, len(rows))}
	for i, r := range rows {
		page.Items[i] = toRunner(r)
	}
	if next != "" {
		page.NextCursor = &next
	}
	return page, nil
}

// UpdateRunner disables or re-enables an execution node.
func (h *handler) UpdateRunner(ctx context.Context, req UpdateRunnerRequestObject) (UpdateRunnerResponseObject, error) {
	if h.runnerAdmin == nil {
		return nil, errNoRunnerAdmin
	}
	r, err := h.runnerAdmin.SetDisabled(ctx, auth.PrincipalFrom(ctx), req.RunnerID, req.Body.Disabled)
	if err != nil {
		return errorResponse[UpdateRunnerdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return UpdateRunner200JSONResponse(toRunner(r)), nil
}

// CreateRunnerToken issues a one-time runner registration token.
func (h *handler) CreateRunnerToken(ctx context.Context, req CreateRunnerTokenRequestObject) (CreateRunnerTokenResponseObject, error) {
	if h.runnerAdmin == nil {
		return nil, errNoRunnerAdmin
	}
	name, ttl := "", service.DefaultRunnerTokenTTL
	if req.Body != nil {
		if req.Body.Name != nil {
			name = *req.Body.Name
		}
		if req.Body.ExpiresInHours != nil {
			// Clamped only so the conversion cannot overflow; the service
			// rejects anything outside 1 to 720 hours.
			ttl = time.Duration(min(max(*req.Body.ExpiresInHours, 0), 100_000)) * time.Hour
		}
	}
	token, info, err := h.runnerAdmin.CreateRegistrationToken(ctx, auth.PrincipalFrom(ctx), name, ttl)
	if err != nil {
		return errorResponse[CreateRunnerTokendefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return CreateRunnerToken201JSONResponse{ID: info.ID, Token: token, ExpiresAt: info.ExpiresAt}, nil
}

func toRunner(r service.RunnerInfo) Runner {
	out := Runner{
		ID:         r.ID,
		Name:       r.Name,
		Labels:     r.Labels,
		Status:     RunnerStatus(r.Status),
		Slots:      r.Slots,
		Running:    r.ActiveRuns,
		LastSeenAt: r.LastSeenAt,
		CreatedAt:  r.CreatedAt,
	}
	if out.Labels == nil {
		out.Labels = []string{}
	}
	if r.Version != "" {
		out.Version = &r.Version
	}
	if !r.DisabledAt.IsZero() {
		out.DisabledAt = &r.DisabledAt
	}
	return out
}
