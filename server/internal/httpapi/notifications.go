package httpapi

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
)

// Notifications is the notification service the API calls.
// *service.Notifications implements it.
type Notifications interface {
	ListChannels(ctx context.Context, p *auth.Principal) ([]service.ChannelDetail, error)
	CreateChannel(ctx context.Context, p *auth.Principal, in service.CreateChannelInput) (service.ChannelDetail, string, error)
	UpdateChannel(ctx context.Context, p *auth.Principal, id uuid.UUID, in service.UpdateChannelInput) (service.ChannelDetail, error)
	DeleteChannel(ctx context.Context, p *auth.Principal, id uuid.UUID) error
	TestChannel(ctx context.Context, p *auth.Principal, id uuid.UUID) (service.DeliveryDetail, error)
	ListDeliveries(ctx context.Context, p *auth.Principal, channelID uuid.UUID, status string, limit *int, cursor string) ([]service.DeliveryDetail, string, error)
}

var errNoNotifications = errors.New("httpapi: no notification service configured")

// ListNotificationChannels lists the caller's notification channels.
func (h *handler) ListNotificationChannels(ctx context.Context, _ ListNotificationChannelsRequestObject) (ListNotificationChannelsResponseObject, error) {
	if h.notifications == nil {
		return nil, errNoNotifications
	}
	rows, err := h.notifications.ListChannels(ctx, auth.PrincipalFrom(ctx))
	if err != nil {
		return errorResponse[ListNotificationChannelsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	out := ListNotificationChannels200JSONResponse{Items: make([]NotificationChannel, len(rows))}
	for i, d := range rows {
		out.Items[i] = toNotificationChannel(d)
	}
	return out, nil
}

// CreateNotificationChannel adds a notification channel with its subscriptions.
func (h *handler) CreateNotificationChannel(ctx context.Context, req CreateNotificationChannelRequestObject) (CreateNotificationChannelResponseObject, error) {
	if h.notifications == nil {
		return nil, errNoNotifications
	}
	b := req.Body
	in := service.CreateChannelInput{Name: b.Name, Kind: string(b.Kind), URL: b.URL, Enabled: b.Enabled}
	if b.Subscriptions != nil {
		in.Subscriptions = subscriptionInputs(*b.Subscriptions)
	}
	d, secret, err := h.notifications.CreateChannel(ctx, auth.PrincipalFrom(ctx), in)
	if err != nil {
		return errorResponse[CreateNotificationChanneldefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return CreateNotificationChannel201JSONResponse{Channel: toNotificationChannel(d), SigningSecret: optional(secret)}, nil
}

// UpdateNotificationChannel changes a notification channel.
func (h *handler) UpdateNotificationChannel(ctx context.Context, req UpdateNotificationChannelRequestObject) (UpdateNotificationChannelResponseObject, error) {
	if h.notifications == nil {
		return nil, errNoNotifications
	}
	b := req.Body
	in := service.UpdateChannelInput{Name: b.Name, URL: b.URL, Enabled: b.Enabled}
	if b.Subscriptions != nil {
		subs := subscriptionInputs(*b.Subscriptions)
		in.Subscriptions = &subs
	}
	d, err := h.notifications.UpdateChannel(ctx, auth.PrincipalFrom(ctx), req.ChannelID, in)
	if err != nil {
		return errorResponse[UpdateNotificationChanneldefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return UpdateNotificationChannel200JSONResponse(toNotificationChannel(d)), nil
}

// DeleteNotificationChannel deletes a notification channel.
func (h *handler) DeleteNotificationChannel(ctx context.Context, req DeleteNotificationChannelRequestObject) (DeleteNotificationChannelResponseObject, error) {
	if h.notifications == nil {
		return nil, errNoNotifications
	}
	if err := h.notifications.DeleteChannel(ctx, auth.PrincipalFrom(ctx), req.ChannelID); err != nil {
		return errorResponse[DeleteNotificationChanneldefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return DeleteNotificationChannel204Response{}, nil
}

// TestNotificationChannel sends a test notification and reports the outcome.
func (h *handler) TestNotificationChannel(ctx context.Context, req TestNotificationChannelRequestObject) (TestNotificationChannelResponseObject, error) {
	if h.notifications == nil {
		return nil, errNoNotifications
	}
	d, err := h.notifications.TestChannel(ctx, auth.PrincipalFrom(ctx), req.ChannelID)
	if err != nil {
		return errorResponse[TestNotificationChanneldefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return TestNotificationChannel200JSONResponse(toNotificationDelivery(d)), nil
}

// ListNotificationDeliveries lists the deliveries of a notification channel.
func (h *handler) ListNotificationDeliveries(ctx context.Context, req ListNotificationDeliveriesRequestObject) (ListNotificationDeliveriesResponseObject, error) {
	if h.notifications == nil {
		return nil, errNoNotifications
	}
	var status, cursor string
	if req.Params.Status != nil {
		status = string(*req.Params.Status)
	}
	if req.Params.Cursor != nil {
		cursor = *req.Params.Cursor
	}
	rows, next, err := h.notifications.ListDeliveries(ctx, auth.PrincipalFrom(ctx), req.ChannelID, status, req.Params.Limit, cursor)
	if err != nil {
		return errorResponse[ListNotificationDeliveriesdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	page := ListNotificationDeliveries200JSONResponse{Items: make([]NotificationDelivery, len(rows))}
	for i, d := range rows {
		page.Items[i] = toNotificationDelivery(d)
	}
	page.NextCursor = optional(next)
	return page, nil
}

func subscriptionInputs(in []NotificationSubscriptionInput) []service.SubscriptionInput {
	out := make([]service.SubscriptionInput, len(in))
	for i, s := range in {
		out[i] = service.SubscriptionInput{ProjectID: s.ProjectID, Events: make([]string, len(s.Events))}
		if s.TaskID != nil {
			out[i].TaskID = *s.TaskID
		}
		for j, e := range s.Events {
			out[i].Events[j] = string(e)
		}
	}
	return out
}

func toNotificationChannel(d service.ChannelDetail) NotificationChannel {
	c := d.Channel
	out := NotificationChannel{
		ID:            c.ID,
		Name:          c.Name,
		Kind:          NotificationChannelKind(c.Kind),
		Enabled:       c.Enabled,
		URLPreview:    c.URLPreview,
		Subscriptions: make([]NotificationSubscription, len(d.Subscriptions)),
		CreatedAt:     c.CreatedAt,
		UpdatedAt:     c.UpdatedAt,
	}
	for i, s := range d.Subscriptions {
		sub := NotificationSubscription{
			ID:      s.Subscription.ID,
			Project: ProjectRef{ID: s.Subscription.ProjectID, Name: s.ProjectName},
			Events:  make([]NotificationEventType, len(s.Subscription.Events)),
		}
		for j, e := range s.Subscription.Events {
			sub.Events[j] = NotificationEventType(e)
		}
		if s.Task != nil {
			sub.Task = toTaskRef(*s.Task)
		}
		out.Subscriptions[i] = sub
	}
	if d.LastDelivery != nil {
		last := toNotificationDelivery(*d.LastDelivery)
		out.LastDelivery = &last
	}
	return out
}

func toNotificationDelivery(d service.DeliveryDetail) NotificationDelivery {
	r := d.Delivery
	out := NotificationDelivery{
		ID:            r.ID,
		EventType:     r.EventType,
		Status:        NotificationDeliveryStatus(r.Status),
		Attempts:      r.Attempts,
		Error:         optional(r.Error),
		RunID:         optionalUUID(r.RunID),
		MergedCount:   max(r.MergedCount, 1),
		CreatedAt:     r.CreatedAt,
		LastAttemptAt: optionalTime(r.LastAttemptAt),
		DeliveredAt:   optionalTime(r.DeliveredAt),
	}
	if r.HTTPStatus != 0 {
		out.HTTPStatus = &r.HTTPStatus
	}
	if d.Task != nil {
		out.Task = toTaskRef(*d.Task)
	}
	return out
}

func toTaskRef(t service.TaskRef) *TaskRef {
	return &TaskRef{ID: t.ID, Name: t.Name, ProjectID: t.ProjectID}
}
