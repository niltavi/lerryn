package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Credentials is the credential service the API calls.
// *service.Credentials implements it. Credential values are accepted from
// the console and the server-rendered fill page. No operation returns one.
type Credentials interface {
	AIConfiguration(ctx context.Context, p *auth.Principal) (service.AIConfiguration, error)
	AIModels(ctx context.Context, p *auth.Principal) ([]string, error)
	SetAIConfiguration(ctx context.Context, p *auth.Principal, config service.AIConfiguration, key string) (service.AIConfiguration, error)
	RemoveAIConfiguration(ctx context.Context, p *auth.Principal) error
	AIChat(ctx context.Context, p *auth.Principal, model string, messages []service.AIMessage) (string, error)
	CreateRequest(ctx context.Context, p *auth.Principal, in service.CreateRequestInput) (service.CredentialRequestDetail, error)
	GetRequest(ctx context.Context, p *auth.Principal, id uuid.UUID) (service.CredentialRequestDetail, error)
	ListRequests(ctx context.Context, p *auth.Principal, status string, limit *int, cursor string) ([]service.CredentialRequestDetail, string, error)
	CancelRequest(ctx context.Context, p *auth.Principal, id uuid.UUID) (service.CredentialRequestDetail, error)
	Fill(ctx context.Context, p *auth.Principal, id uuid.UUID, values map[string]string) (store.Credential, error)
	List(ctx context.Context, p *auth.Principal, status string, limit *int, cursor string) ([]service.CredentialDetail, string, error)
	Get(ctx context.Context, p *auth.Principal, id uuid.UUID) (service.CredentialDetail, error)
	Revoke(ctx context.Context, p *auth.Principal, id uuid.UUID) (service.CredentialDetail, error)
	Delete(ctx context.Context, p *auth.Principal, id uuid.UUID) error
	Grant(ctx context.Context, p *auth.Principal, credentialID, taskID uuid.UUID, name string) (service.GrantDetail, error)
	RevokeGrant(ctx context.Context, p *auth.Principal, credentialID, taskID uuid.UUID) error
	TaskCredentials(ctx context.Context, p *auth.Principal, taskID uuid.UUID) (int, []service.TaskCredential, error)
}

var errNoCredentials = errors.New("httpapi: no credential service configured")

// FillPath is the path of the fill page of a request, relative to the
// deployment's public URL.
func FillPath(id uuid.UUID) string { return "/c/" + id.String() }

// ListCredentialRequests lists the fill requests addressed to the caller.
func (h *handler) ListCredentialRequests(ctx context.Context, req ListCredentialRequestsRequestObject) (ListCredentialRequestsResponseObject, error) {
	if h.credentials == nil {
		return nil, errNoCredentials
	}
	status := ""
	if req.Params.Status != nil {
		status = string(*req.Params.Status)
	}
	rows, next, err := h.credentials.ListRequests(ctx, auth.PrincipalFrom(ctx), status, req.Params.Limit, credValue(req.Params.Cursor))
	if err != nil {
		return errorResponse[ListCredentialRequestsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	page := ListCredentialRequests200JSONResponse{Items: make([]CredentialRequest, len(rows))}
	for i, r := range rows {
		page.Items[i] = h.toCredentialRequest(r)
	}
	if next != "" {
		page.NextCursor = &next
	}
	return page, nil
}

// CreateCredentialRequest asks the caller to fill in a credential and returns the fill link.
func (h *handler) CreateCredentialRequest(ctx context.Context, req CreateCredentialRequestRequestObject) (CreateCredentialRequestResponseObject, error) {
	if h.credentials == nil {
		return nil, errNoCredentials
	}
	b := req.Body
	in := service.CreateRequestInput{Purpose: b.Purpose, Fields: fromFieldSpecs(b.Fields)}
	if b.TaskID != nil {
		in.TaskID = *b.TaskID
	}
	if b.CredentialID != nil {
		in.CredentialID = *b.CredentialID
	}
	if b.Name != nil {
		in.Name = *b.Name
	}
	if b.ExpiresInMinutes != nil {
		in.ExpiresIn = time.Duration(*b.ExpiresInMinutes) * time.Minute
	}
	d, err := h.credentials.CreateRequest(ctx, auth.PrincipalFrom(ctx), in)
	if err != nil {
		return errorResponse[CreateCredentialRequestdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return CreateCredentialRequest201JSONResponse(h.toCredentialRequest(d)), nil
}

// GetCredentialRequest returns the state of a fill request.
func (h *handler) GetCredentialRequest(ctx context.Context, req GetCredentialRequestRequestObject) (GetCredentialRequestResponseObject, error) {
	if h.credentials == nil {
		return nil, errNoCredentials
	}
	d, err := h.credentials.GetRequest(ctx, auth.PrincipalFrom(ctx), req.RequestID)
	if err != nil {
		return errorResponse[GetCredentialRequestdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return GetCredentialRequest200JSONResponse(h.toCredentialRequest(d)), nil
}

// CancelCredentialRequest cancels a pending fill request.
func (h *handler) CancelCredentialRequest(ctx context.Context, req CancelCredentialRequestRequestObject) (CancelCredentialRequestResponseObject, error) {
	if h.credentials == nil {
		return nil, errNoCredentials
	}
	d, err := h.credentials.CancelRequest(ctx, auth.PrincipalFrom(ctx), req.RequestID)
	if err != nil {
		return errorResponse[CancelCredentialRequestdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return CancelCredentialRequest200JSONResponse(h.toCredentialRequest(d)), nil
}

// FillCredentialRequest stores the values typed in the console's fill
// dialog. The contract admits session cookies only, and the service
// refuses anything else as well, so an agent holding the user's access
// token cannot submit or replace values.
func (h *handler) FillCredentialRequest(ctx context.Context, req FillCredentialRequestRequestObject) (FillCredentialRequestResponseObject, error) {
	if h.credentials == nil {
		return nil, errNoCredentials
	}
	values := req.Body.Values
	defer clear(values)
	p := auth.PrincipalFrom(ctx)
	_, err := h.credentials.Fill(ctx, p, req.RequestID, values)
	if verr, ok := errors.AsType[*service.ValidationError](err); ok {
		// The service points at fields as on the fill page (#/NAME); in the
		// API body they sit under values.
		for i, f := range verr.Fields {
			if rest, found := strings.CutPrefix(f.Pointer, "#"); found {
				verr.Fields[i].Pointer = "#/values" + rest
			}
		}
	}
	switch {
	case errors.Is(err, service.ErrRequestClosed):
		return problemResponse[FillCredentialRequestdefaultApplicationProblemPlusJSONResponse](ctx, http.StatusConflict, ErrorCodeRequestClosed,
			"这个填写请求已经填写过、过期或被取消"), nil
	case err != nil:
		return errorResponse[FillCredentialRequestdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	d, err := h.credentials.GetRequest(ctx, p, req.RequestID)
	if err != nil {
		return errorResponse[FillCredentialRequestdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return FillCredentialRequest200JSONResponse(h.toCredentialRequest(d)), nil
}

// ListCredentials lists the caller's credentials.
func (h *handler) ListCredentials(ctx context.Context, req ListCredentialsRequestObject) (ListCredentialsResponseObject, error) {
	if h.credentials == nil {
		return nil, errNoCredentials
	}
	status := ""
	if req.Params.Status != nil {
		status = string(*req.Params.Status)
	}
	rows, next, err := h.credentials.List(ctx, auth.PrincipalFrom(ctx), status, req.Params.Limit, credValue(req.Params.Cursor))
	if err != nil {
		return errorResponse[ListCredentialsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	page := ListCredentials200JSONResponse{Items: make([]Credential, len(rows))}
	for i, r := range rows {
		page.Items[i] = toCredential(r)
	}
	if next != "" {
		page.NextCursor = &next
	}
	return page, nil
}

// GetCredential returns one of the caller's credentials.
func (h *handler) GetCredential(ctx context.Context, req GetCredentialRequestObject) (GetCredentialResponseObject, error) {
	if h.credentials == nil {
		return nil, errNoCredentials
	}
	d, err := h.credentials.Get(ctx, auth.PrincipalFrom(ctx), req.CredentialID)
	if err != nil {
		return errorResponse[GetCredentialdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return GetCredential200JSONResponse(toCredential(d)), nil
}

// RevokeCredential revokes one of the caller's credentials.
func (h *handler) RevokeCredential(ctx context.Context, req RevokeCredentialRequestObject) (RevokeCredentialResponseObject, error) {
	if h.credentials == nil {
		return nil, errNoCredentials
	}
	d, err := h.credentials.Revoke(ctx, auth.PrincipalFrom(ctx), req.CredentialID)
	if err != nil {
		return errorResponse[RevokeCredentialdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return RevokeCredential200JSONResponse(toCredential(d)), nil
}

// DeleteCredential permanently removes one of the caller's revoked credentials.
func (h *handler) DeleteCredential(ctx context.Context, req DeleteCredentialRequestObject) (DeleteCredentialResponseObject, error) {
	if h.credentials == nil {
		return nil, errNoCredentials
	}
	if err := h.credentials.Delete(ctx, auth.PrincipalFrom(ctx), req.CredentialID); err != nil {
		return errorResponse[DeleteCredentialdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return DeleteCredential204Response{}, nil
}

// GrantCredential grants a credential to a task under a reference name.
func (h *handler) GrantCredential(ctx context.Context, req GrantCredentialRequestObject) (GrantCredentialResponseObject, error) {
	if h.credentials == nil {
		return nil, errNoCredentials
	}
	g, err := h.credentials.Grant(ctx, auth.PrincipalFrom(ctx), req.CredentialID, req.TaskID, req.Body.Name)
	if err != nil {
		return errorResponse[GrantCredentialdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return GrantCredential200JSONResponse(toCredentialGrant(g)), nil
}

// RevokeGrant withdraws a credential grant from a task.
func (h *handler) RevokeGrant(ctx context.Context, req RevokeGrantRequestObject) (RevokeGrantResponseObject, error) {
	if h.credentials == nil {
		return nil, errNoCredentials
	}
	if err := h.credentials.RevokeGrant(ctx, auth.PrincipalFrom(ctx), req.CredentialID, req.TaskID); err != nil {
		return errorResponse[RevokeGrantdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return RevokeGrant204Response{}, nil
}

// ListTaskCredentials lists the credentials a task references and their binding state.
func (h *handler) ListTaskCredentials(ctx context.Context, req ListTaskCredentialsRequestObject) (ListTaskCredentialsResponseObject, error) {
	if h.credentials == nil {
		return nil, errNoCredentials
	}
	version, items, err := h.credentials.TaskCredentials(ctx, auth.PrincipalFrom(ctx), req.TaskID)
	if err != nil {
		return errorResponse[ListTaskCredentialsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	out := ListTaskCredentials200JSONResponse{Version: version, Items: make([]TaskCredential, len(items))}
	for i, it := range items {
		tc := TaskCredential{
			Name:     it.Spec.Name,
			As:       TaskCredentialAs(it.Spec.As),
			Optional: it.Spec.Optional,
			Status:   TaskCredentialStatus(it.Status),
		}
		if c := it.Credential; c != nil {
			ref := CredentialRef{ID: c.ID, Name: c.Name, Status: CredentialStatus(c.Status), Owner: UserRef{}}
			if o := it.Owner; o != nil {
				ref.Owner = UserRef{ID: o.ID, Username: o.Username, DisplayName: credString(o.DisplayName)}
			}
			tc.Credential = &ref
			tc.ExpiresAt = credTime(c.ExpiresAt)
		}
		if it.PendingRequestID != uuid.Nil {
			id := it.PendingRequestID
			tc.PendingRequestID = &id
		}
		out.Items[i] = tc
	}
	return out, nil
}

func (h *handler) toCredentialRequest(d service.CredentialRequestDetail) CredentialRequest {
	r := d.Request
	out := CredentialRequest{
		ID:          r.ID,
		Status:      CredentialRequestStatus(r.Status),
		URL:         h.publicURL + FillPath(r.ID),
		Name:        r.Name,
		Purpose:     r.Purpose,
		Fields:      toFieldSpecs(r.Fields),
		Client:      credString(r.Client),
		CreatedAt:   r.CreatedAt,
		ExpiresAt:   r.ExpiresAt,
		FulfilledAt: credTime(r.FulfilledAt),
	}
	if r.CredentialID != uuid.Nil {
		id := r.CredentialID
		out.CredentialID = &id
	}
	if t := d.Task; t != nil {
		out.Task = &TaskRef{ID: t.ID, Name: t.Name, ProjectID: t.ProjectID}
	}
	return out
}

func toCredential(d service.CredentialDetail) Credential {
	c := d.Credential
	out := Credential{
		ID:         c.ID,
		Name:       c.Name,
		Purpose:    c.Purpose,
		Fields:     make([]CredentialFieldName, len(c.Fields)),
		Status:     CredentialStatus(c.Status),
		Version:    c.CurrentVersion,
		ExpiresAt:  credTime(c.ExpiresAt),
		RevokedAt:  credTime(c.RevokedAt),
		LastUsedAt: credTime(c.LastUsedAt),
		CreatedAt:  c.CreatedAt,
		UpdatedAt:  c.UpdatedAt,
		Grants:     make([]CredentialGrant, len(d.Grants)),
	}
	for i, f := range c.Fields {
		out.Fields[i] = f.Name
	}
	for i, g := range d.Grants {
		out.Grants[i] = toCredentialGrant(g)
	}
	return out
}

func toCredentialGrant(g service.GrantDetail) CredentialGrant {
	return CredentialGrant{
		Task:      TaskRef{ID: g.Task.ID, Name: g.Task.Name, ProjectID: g.Task.ProjectID},
		Name:      g.Grant.Name,
		GrantedAt: g.Grant.CreatedAt,
	}
}

// fromFieldSpecs applies the schema defaults: fields are secret unless
// marked otherwise.
func fromFieldSpecs(in []CredentialFieldSpec) []store.CredentialField {
	out := make([]store.CredentialField, len(in))
	for i, f := range in {
		out[i] = store.CredentialField{
			Name:        f.Name,
			Label:       credValue(f.Label),
			Description: credValue(f.Description),
			Secret:      f.Secret == nil || *f.Secret,
			Multiline:   f.Multiline != nil && *f.Multiline,
			Optional:    f.Optional != nil && *f.Optional,
		}
	}
	return out
}

func toFieldSpecs(in []store.CredentialField) []CredentialFieldSpec {
	out := make([]CredentialFieldSpec, len(in))
	for i, f := range in {
		out[i] = CredentialFieldSpec{
			Name:      f.Name,
			Secret:    new(f.Secret),
			Multiline: new(f.Multiline),
			Optional:  new(f.Optional),
		}
		if f.Label != "" {
			out[i].Label = new(f.Label)
		}
		if f.Description != "" {
			out[i].Description = new(f.Description)
		}
	}
	return out
}

func credValue[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func credString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func credTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

// credentialProblem maps a *service.CredentialError to its problem, with
// the next step an agent should take.
func credentialProblem(ctx context.Context, ce *service.CredentialError) Problem {
	code := ErrorCodeCredentialUnavailable
	next := []NextAction{{Action: "credential_request", Hint: "用 credential_id 为凭证所有者发起更新请求，或请所有者重新授权"}}
	if ce.Code == string(ErrorCodeCredentialMissing) {
		code = ErrorCodeCredentialMissing
		next = []NextAction{{Action: "credential_request", Hint: "为该任务创建凭证填写请求（name 为 " + ce.Name + "），并把链接交给用户"}}
	}
	p := buildProblem(ctx, http.StatusUnprocessableEntity, code, ce.Detail)
	p.NextActions = &next
	return p
}
