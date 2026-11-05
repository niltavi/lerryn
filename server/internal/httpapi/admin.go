package httpapi

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

var errNoAdmin = errors.New("httpapi: no admin service configured")

// AdminListUsers lists every account.
func (h *handler) AdminListUsers(ctx context.Context, req AdminListUsersRequestObject) (AdminListUsersResponseObject, error) {
	if h.admin == nil {
		return nil, errNoAdmin
	}
	f := service.UserFilter{Query: deref(req.Params.Q), Admin: req.Params.Admin, Limit: req.Params.Limit, Cursor: deref(req.Params.Cursor)}
	if req.Params.Status != nil {
		f.Status = string(*req.Params.Status)
	}
	users, next, err := h.admin.ListUsers(ctx, auth.PrincipalFrom(ctx), f)
	if err != nil {
		return errorResponse[AdminListUsersdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	out := AdminListUsers200JSONResponse{Items: make([]AdminUser, len(users)), NextCursor: optional(next)}
	for i, u := range users {
		out.Items[i] = toAdminUser(u)
	}
	return out, nil
}

// AdminCreateUser creates a built-in account.
func (h *handler) AdminCreateUser(ctx context.Context, req AdminCreateUserRequestObject) (AdminCreateUserResponseObject, error) {
	if h.admin == nil {
		return nil, errNoAdmin
	}
	in := service.AdminCreateUserInput{RegisterInput: service.RegisterInput{
		Username: req.Body.Username, Password: req.Body.Password, DisplayName: deref(req.Body.DisplayName),
	}}
	if req.Body.Email != nil {
		in.Email = string(*req.Body.Email)
	}
	if req.Body.IsAdmin != nil {
		in.IsAdmin = *req.Body.IsAdmin
	}
	u, err := h.admin.CreateUser(ctx, auth.PrincipalFrom(ctx), in)
	if err != nil {
		return errorResponse[AdminCreateUserdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return AdminCreateUser201JSONResponse(toAdminUser(u)), nil
}

// AdminUpdateUser changes an account's admin flag, disabled state or display name.
func (h *handler) AdminUpdateUser(ctx context.Context, req AdminUpdateUserRequestObject) (AdminUpdateUserResponseObject, error) {
	if h.admin == nil {
		return nil, errNoAdmin
	}
	u, err := h.admin.UpdateUser(ctx, auth.PrincipalFrom(ctx), req.UserID, service.AdminUpdateUserInput{
		IsAdmin: req.Body.IsAdmin, Disabled: req.Body.Disabled, DisplayName: req.Body.DisplayName,
	})
	if err != nil {
		return errorResponse[AdminUpdateUserdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return AdminUpdateUser200JSONResponse(toAdminUser(u)), nil
}

// AdminResetPassword sets a new password for an account.
func (h *handler) AdminResetPassword(ctx context.Context, req AdminResetPasswordRequestObject) (AdminResetPasswordResponseObject, error) {
	if h.admin == nil {
		return nil, errNoAdmin
	}
	if err := h.admin.ResetPassword(ctx, auth.PrincipalFrom(ctx), req.UserID, req.Body.Password); err != nil {
		return errorResponse[AdminResetPassworddefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return AdminResetPassword204Response{}, nil
}

// GetSettings returns the deployment settings.
func (h *handler) GetSettings(ctx context.Context, _ GetSettingsRequestObject) (GetSettingsResponseObject, error) {
	if h.admin == nil {
		return nil, errNoAdmin
	}
	v, err := h.admin.Settings(ctx, auth.PrincipalFrom(ctx))
	if err != nil {
		return errorResponse[GetSettingsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return GetSettings200JSONResponse(h.toSettings(v)), nil
}

// UpdateSettings changes the deployment settings.
func (h *handler) UpdateSettings(ctx context.Context, req UpdateSettingsRequestObject) (UpdateSettingsResponseObject, error) {
	if h.admin == nil {
		return nil, errNoAdmin
	}
	v, err := h.admin.UpdateSettings(ctx, auth.PrincipalFrom(ctx), service.SettingsInput{
		OpenRegistration: req.Body.OpenRegistration, LocalPasswordLogin: req.Body.LocalPasswordLogin,
	})
	if err != nil {
		return errorResponse[UpdateSettingsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return UpdateSettings200JSONResponse(h.toSettings(v)), nil
}

// ListAuditEvents queries the audit log.
func (h *handler) ListAuditEvents(ctx context.Context, req ListAuditEventsRequestObject) (ListAuditEventsResponseObject, error) {
	if h.audit == nil {
		return nil, errNoAdmin
	}
	f := service.AuditFilter{
		ActorID: deref(req.Params.ActorID), Action: deref(req.Params.Action),
		ResourceKind: deref(req.Params.ResourceKind), ResourceID: deref(req.Params.ResourceID),
		Limit: req.Params.Limit, Cursor: deref(req.Params.Cursor),
	}
	if req.Params.Since != nil {
		f.Since = *req.Params.Since
	}
	if req.Params.Until != nil {
		f.Until = *req.Params.Until
	}
	if req.Params.Result != nil {
		f.Result = string(*req.Params.Result)
	}
	events, next, err := h.audit.List(ctx, auth.PrincipalFrom(ctx), f)
	if err != nil {
		return errorResponse[ListAuditEventsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	out := ListAuditEvents200JSONResponse{Items: make([]AuditEvent, len(events)), NextCursor: optional(next)}
	for i, e := range events {
		out.Items[i] = toAuditEvent(e)
	}
	return out, nil
}

func toAdminUser(u service.AdminUser) AdminUser {
	return AdminUser{
		ID:          u.User.ID,
		Username:    u.User.Username,
		DisplayName: optional(u.User.DisplayName),
		Email:       optional(u.User.Email),
		IsAdmin:     u.User.IsAdmin,
		HasPassword: u.User.PasswordHash != "",
		Sso:         u.SSO,
		DisabledAt:  optionalTime(u.User.DisabledAt),
		CreatedAt:   u.User.CreatedAt,
		LastSeenAt:  optionalTime(u.User.LastSeenAt),
	}
}

func (h *handler) toSettings(v service.SettingsView) Settings {
	out := Settings{
		OpenRegistration:          v.Settings.OpenRegistration,
		LocalPasswordLogin:        v.Settings.LocalPasswordLogin,
		RequireTestBeforeActivate: h.capabilities.RequireTestBeforeActivate,
		UpdatedAt:                 optionalTime(v.Settings.UpdatedAt),
	}
	if v.UpdatedBy != nil {
		ref := toUserRef(*v.UpdatedBy)
		out.UpdatedBy = &ref
	}
	if o := v.OIDC; o != nil {
		out.Oidc = &OidcInfo{
			Issuer: o.Issuer, ClientID: o.ClientID, UsernameClaim: o.UsernameClaim,
			CallbackURL: o.CallbackURL, UserCount: o.UserCount,
		}
	}
	return out
}

func toAuditEvent(e store.AuditEvent) AuditEvent {
	out := AuditEvent{
		ID:           e.ID,
		TS:           e.TS,
		Action:       e.Action,
		Actor:        AuditActor{Kind: AuditActorKind(e.ActorKind), Name: optional(e.ActorName)},
		Kind:         optionalEnum[AuditEventKind](e.Kind),
		ResourceKind: e.ResourceKind,
		ResourceID:   optional(e.ResourceID),
		Result:       AuditEventResult(e.Result),
		Via:          optionalEnum[AuditEventVia](e.Via),
		Tool:         optional(e.Tool),
		IP:           optional(e.IP),
		RequestID:    optional(e.RequestID),
	}
	if !out.Actor.Kind.Valid() {
		out.Actor.Kind = AuditActorKindSystem
	}
	if !out.Result.Valid() {
		out.Result = AuditEventResultFailure
	}
	if e.ActorID != uuid.Nil {
		out.Actor.ID = new(e.ActorID.String())
	}
	if len(e.Details) > 0 {
		d := e.Details
		out.Details = &d
	}
	return out
}
