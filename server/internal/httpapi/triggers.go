package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Triggers is the trigger service the API calls. *service.Triggers
// implements it.
type Triggers interface {
	SkipNext(ctx context.Context, p *auth.Principal, taskID uuid.UUID, expected time.Time) (service.TriggerSet, error)
	List(ctx context.Context, p *auth.Principal, taskID uuid.UUID) (service.TriggerSet, error)
	Set(ctx context.Context, p *auth.Principal, taskID uuid.UUID, in service.SetTriggersInput) (service.TriggerSet, error)
	RotateToken(ctx context.Context, p *auth.Principal, triggerID uuid.UUID) (service.TriggerView, error)
	Webhook(ctx context.Context, call service.WebhookCall) (service.WebhookResult, error)
}

// SkipNextTaskRun skips the pending cron time the caller confirmed.
func (h *handler) SkipNextTaskRun(ctx context.Context, req SkipNextTaskRunRequestObject) (SkipNextTaskRunResponseObject, error) {
	if h.triggers == nil {
		return nil, errNoTriggers
	}
	var expected time.Time
	if req.Body != nil {
		expected = req.Body.ScheduledAt
	}
	set, err := h.triggers.SkipNext(ctx, auth.PrincipalFrom(ctx), req.TaskID, expected)
	if err != nil {
		return errorResponse[SkipNextTaskRundefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return SkipNextTaskRun200JSONResponse(h.toTriggerSet(set)), nil
}

var errNoTriggers = errors.New("httpapi: no trigger service configured")

// hooksPath is where webhook calls arrive; it is outside BasePath.
const hooksPath = "/hooks/"

type hookTokenKey struct{}

// withHookToken carries the bearer token of a webhook call to the
// handler, which the generated request object does not include. Only
// NewHooks sets it, so a call that reaches the webhook operation any
// other way has no token.
func withHookToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, hookTokenKey{}, token)
}

func hookTokenFrom(ctx context.Context) string {
	t, _ := ctx.Value(hookTokenKey{}).(string)
	return t
}

// hookToken returns the bearer token of r, or "" when there is none.
func hookToken(r *http.Request) string {
	scheme, value, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(value)
}

// ListTriggers returns the triggers of a task.
func (h *handler) ListTriggers(ctx context.Context, req ListTriggersRequestObject) (ListTriggersResponseObject, error) {
	if h.triggers == nil {
		return nil, errNoTriggers
	}
	set, err := h.triggers.List(ctx, auth.PrincipalFrom(ctx), req.TaskID)
	if err != nil {
		return errorResponse[ListTriggersdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return ListTriggers200JSONResponse(h.toTriggerSet(set)), nil
}

// SetTriggers replaces the triggers of a task.
func (h *handler) SetTriggers(ctx context.Context, req SetTriggersRequestObject) (SetTriggersResponseObject, error) {
	if h.triggers == nil {
		return nil, errNoTriggers
	}
	in := service.SetTriggersInput{DryRun: req.Params.DryRun != nil && *req.Params.DryRun}
	if m := req.Params.IfMatch; m != nil {
		// The contract allows 3 and "3", one to ten digits.
		n, err := strconv.Atoi(strings.Trim(*m, `"`))
		if err != nil {
			return problemResponse[SetTriggersdefaultApplicationProblemPlusJSONResponse](ctx, http.StatusBadRequest, ErrorCodeValidationFailed, "If-Match 应为读到的 revision"), nil
		}
		in.IfMatch = &n
	}
	if req.Body != nil {
		in.Triggers = toServiceTriggers(req.Body.Triggers)
	}
	set, err := h.triggers.Set(ctx, auth.PrincipalFrom(ctx), req.TaskID, in)
	if err != nil {
		return errorResponse[SetTriggersdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return SetTriggers200JSONResponse(h.toTriggerSet(set)), nil
}

// RotateTriggerToken issues a new token for a webhook trigger.
func (h *handler) RotateTriggerToken(ctx context.Context, req RotateTriggerTokenRequestObject) (RotateTriggerTokenResponseObject, error) {
	if h.triggers == nil {
		return nil, errNoTriggers
	}
	v, err := h.triggers.RotateToken(ctx, auth.PrincipalFrom(ctx), req.TriggerID)
	if err != nil {
		return errorResponse[RotateTriggerTokendefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return RotateTriggerToken200JSONResponse(h.toTrigger(v)), nil
}

// TriggerWebhook starts a run from a webhook call authenticated by the
// trigger token.
func (h *handler) TriggerWebhook(ctx context.Context, req TriggerWebhookRequestObject) (TriggerWebhookResponseObject, error) {
	if h.triggers == nil {
		return nil, errNoTriggers
	}
	call := service.WebhookCall{
		TriggerID:   req.TriggerID,
		Token:       hookTokenFrom(ctx),
		ContentType: req.ContentType,
		Body:        req.Body,
	}
	if req.Params.IdempotencyKey != nil {
		call.IdempotencyKey = *req.Params.IdempotencyKey
	}
	res, err := h.triggers.Webhook(ctx, call)
	if errors.Is(err, service.ErrUnauthenticated) {
		return problemResponse[TriggerWebhookdefaultApplicationProblemPlusJSONResponse](ctx, http.StatusUnauthorized, ErrorCodeUnauthenticated,
			"需要在 Authorization 头中提供这个触发器的令牌，写成 Bearer <令牌>"), nil
	}
	if errors.Is(err, service.ErrRateLimited) {
		return problemResponse[TriggerWebhookdefaultApplicationProblemPlusJSONResponse](ctx, http.StatusTooManyRequests, ErrorCodeRateLimited,
			"这个触发器收到的请求过多，请稍后再试"), nil
	}
	if err != nil {
		return errorResponse[TriggerWebhookdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return TriggerWebhook202JSONResponse{RunID: res.RunID, Status: RunStatus(res.Status), Duplicate: res.Duplicate}, nil
}

func (h *handler) toTriggerSet(s service.TriggerSet) TriggerSet {
	out := TriggerSet{TaskID: s.TaskID, Revision: s.Revision, DryRun: s.DryRun, TaskActive: s.TaskActive, Triggers: make([]Trigger, len(s.Triggers))}
	for i, v := range s.Triggers {
		out.Triggers[i] = h.toTrigger(v)
	}
	return out
}

func (h *handler) toTrigger(v service.TriggerView) Trigger {
	t := v.Trigger
	out := Trigger{
		ID:          t.ID,
		Name:        t.Name,
		Type:        TriggerType(t.Type),
		Enabled:     t.Enabled,
		CatchUp:     CatchUp(t.CatchUp),
		NextFireAt:  optionalTime(v.NextFireAt),
		LastFiredAt: optionalTime(t.LastFiredAt),
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
	}
	if out.CatchUp == "" {
		out.CatchUp = CatchUp(store.CatchUpSkip)
	}
	if t.Params != "" {
		var params map[string]any
		if err := json.Unmarshal([]byte(t.Params), &params); err == nil {
			out.Params = &params
		} else {
			h.log.Error("decode stored trigger params", "trigger_id", t.ID, "error", err)
		}
	}
	switch t.Type {
	case store.TriggerTypeCron:
		fires := v.NextFires
		if fires == nil {
			fires = []time.Time{}
		}
		out.Cron = &CronDetail{Schedule: t.Schedule, Timezone: t.Timezone, ScheduleText: v.ScheduleText, NextFires: fires}
	case store.TriggerTypeWebhook:
		out.Webhook = &WebhookDetail{
			URL:          h.publicURL + hooksPath + t.ID.String(),
			TokenPrefix:  t.TokenPrefix,
			Token:        optional(v.Token),
			MaxBodyBytes: int(t.MaxBodyBytes),
		}
	}
	return out
}

// toServiceTriggers converts trigger definitions of a request.
func toServiceTriggers(inputs []TriggerInput) []service.TriggerInput {
	out := make([]service.TriggerInput, len(inputs))
	for i, t := range inputs {
		ti := service.TriggerInput{Name: t.Name, Type: string(t.Type), Enabled: t.Enabled}
		if t.Schedule != nil {
			ti.Schedule = *t.Schedule
		}
		if t.Timezone != nil {
			ti.Timezone = *t.Timezone
		}
		if t.CatchUp != nil {
			ti.CatchUp = string(*t.CatchUp)
		}
		if t.MaxBodyBytes != nil {
			b := int64(*t.MaxBodyBytes)
			ti.MaxBodyBytes = &b
		}
		if t.Params != nil {
			ti.Params = *t.Params
			if ti.Params == nil {
				ti.Params = map[string]any{}
			}
		}
		out[i] = ti
	}
	return out
}
