package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Register creates a built-in account and logs it in.
func (h *handler) Register(ctx context.Context, req RegisterRequestObject) (RegisterResponseObject, error) {
	if h.accounts == nil {
		return nil, errNoAccounts
	}
	in := service.RegisterInput{Username: req.Body.Username, Password: req.Body.Password}
	if req.Body.DisplayName != nil {
		in.DisplayName = *req.Body.DisplayName
	}
	if req.Body.Email != nil {
		in.Email = string(*req.Body.Email)
	}
	acct, sess, err := h.accounts.Register(ctx, in)
	if err != nil {
		return errorResponse[RegisterdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	cookie := h.sessionCookie(ctx, sess)
	return Register201JSONResponse{Body: toMe(acct), Headers: Register201ResponseHeaders{SetCookie: &cookie}}, nil
}

// Login checks a username and password and opens a session.
func (h *handler) Login(ctx context.Context, req LoginRequestObject) (LoginResponseObject, error) {
	if h.accounts == nil {
		return nil, errNoAccounts
	}
	var device *service.DeviceInfo
	if req.Body.Device != nil {
		d := toDevice(*req.Body.Device)
		device = &d
	}
	acct, sess, err := h.accounts.LoginDevice(ctx, req.Body.Username, req.Body.Password, device)
	if errors.Is(err, service.ErrUnauthenticated) {
		return problemResponse[LogindefaultApplicationProblemPlusJSONResponse](ctx, http.StatusUnauthorized,
			ErrorCodeUnauthenticated, "用户名或密码不正确"), nil
	}
	if err != nil {
		return errorResponse[LogindefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	cookie := h.sessionCookie(ctx, sess)
	return Login200JSONResponse{Body: toMe(acct), Headers: Login200ResponseHeaders{SetCookie: &cookie}}, nil
}

// Logout ends the current session and clears its cookie.
func (h *handler) Logout(ctx context.Context, _ LogoutRequestObject) (LogoutResponseObject, error) {
	if err := h.accounts.Logout(ctx, auth.PrincipalFrom(ctx)); err != nil {
		return errorResponse[LogoutdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	cleared := h.clearedSessionCookie(ctx)
	return Logout204Response{Headers: Logout204ResponseHeaders{SetCookie: &cleared}}, nil
}

// GetMe returns the caller's account.
func (h *handler) GetMe(ctx context.Context, _ GetMeRequestObject) (GetMeResponseObject, error) {
	acct, err := h.accounts.Me(ctx, auth.PrincipalFrom(ctx))
	if err != nil {
		return errorResponse[GetMedefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return GetMe200JSONResponse(toMe(acct)), nil
}

// ListTokens lists the caller's access tokens.
func (h *handler) ListTokens(ctx context.Context, req ListTokensRequestObject) (ListTokensResponseObject, error) {
	var cursor string
	if req.Params.Cursor != nil {
		cursor = *req.Params.Cursor
	}
	rows, next, err := h.accounts.ListTokens(ctx, auth.PrincipalFrom(ctx), req.Params.Limit, cursor)
	if err != nil {
		return errorResponse[ListTokensdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	page := ListTokens200JSONResponse{Items: make([]AccessToken, len(rows))}
	for i, row := range rows {
		page.Items[i] = toAccessToken(row)
	}
	if next != "" {
		page.NextCursor = &next
	}
	return page, nil
}

// CreateToken creates an access token and returns it once.
func (h *handler) CreateToken(ctx context.Context, req CreateTokenRequestObject) (CreateTokenResponseObject, error) {
	in := service.CreateTokenInput{Name: req.Body.Name, ExpiresInDays: req.Body.ExpiresInDays}
	for _, s := range req.Body.Scopes {
		in.Scopes = append(in.Scopes, string(s))
	}
	token, row, err := h.accounts.CreateToken(ctx, auth.PrincipalFrom(ctx), in)
	if err != nil {
		return errorResponse[CreateTokendefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return CreateToken201JSONResponse{Token: token, AccessToken: toAccessToken(row)}, nil
}

// RevokeToken revokes one of the caller's access tokens.
func (h *handler) RevokeToken(ctx context.Context, req RevokeTokenRequestObject) (RevokeTokenResponseObject, error) {
	if err := h.accounts.RevokeToken(ctx, auth.PrincipalFrom(ctx), req.TokenID); err != nil {
		return errorResponse[RevokeTokendefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return RevokeToken204Response{}, nil
}

// errorResponse maps a service error to an operation's default response.
// Errors without a mapping are returned as they are and become an
// internal_error through the response error handler.
func errorResponse[T defaultResponse](ctx context.Context, err error) (T, error) {
	if p, ok := lifecycleProblem(ctx, err); ok {
		return T{Body: p, StatusCode: p.Status}, nil
	}
	var (
		status int
		code   ErrorCode
		detail string
		verr   *service.ValidationError
	)
	var next []NextAction
	switch {
	case errors.As(err, &verr):
		status, code, detail = http.StatusBadRequest, ErrorCodeValidationFailed, "请求内容校验未通过"
		if verr.Content {
			// Well-formed, but the content breaks a rule (api/README.md).
			status = http.StatusUnprocessableEntity
		}
		if _, ok := errors.AsType[*service.PlaintextCredentialError](err); ok {
			detail = "提交的内容中含有凭证的值"
			next = []NextAction{
				{Action: "credential_request", Hint: "去掉这些值，改为在清单 credentials 中引用凭证；任务还没有绑定这个凭证时，发起凭证填写请求，由用户在填写页填写。不要向用户索要明文"},
				{Action: "credential_request", Hint: "值是用户在对话中提供的，就已经出现在对话记录里：建议用户以 credential_id 发起更新请求，换一个新值"},
			}
		}
	case errors.Is(err, service.ErrRunInProgress):
		status, code, detail = http.StatusConflict, ErrorCodeRunInProgress, "任务的上一个运行还没有结束"
		if rip, ok := errors.AsType[*service.RunInProgressError](err); ok && rip.RunID != uuid.Nil {
			detail = "任务的运行 " + rip.RunID.String() + " 还没有结束"
		}
		next = []NextAction{
			{Action: "run_get", Hint: "带 wait 参数读取上一个运行，等它结束后再发起"},
			{Action: "run_cancel", Hint: "不再需要上一个运行时先取消它，再发起新的运行"},
		}
	case errors.As(err, new(*service.CredentialError)):
		ce, _ := errors.AsType[*service.CredentialError](err)
		p := credentialProblem(ctx, ce)
		return T{Body: p, StatusCode: p.Status}, nil
	case errors.Is(err, service.ErrIdempotencyConflict):
		status, code, detail = http.StatusConflict, ErrorCodeIdempotencyConflict, "这个幂等键已用于另一个请求；换一个新的键，或按原请求重试"
	case errors.Is(err, service.ErrNotFound):
		status, code = http.StatusNotFound, ErrorCodeNotFound
	case errors.Is(err, service.ErrUnauthenticated):
		status, code = http.StatusUnauthorized, ErrorCodeUnauthenticated
	case errors.Is(err, service.ErrPermissionDenied):
		status, code, detail = http.StatusForbidden, ErrorCodePermissionDenied, "当前用户不能执行这个操作"
	case errors.Is(err, service.ErrForbidden):
		status, code, detail = http.StatusForbidden, ErrorCodeInsufficientScope, "当前凭证不能执行这个操作"
	case errors.Is(err, service.ErrUsernameTaken):
		status, code, detail = http.StatusConflict, ErrorCodeUsernameTaken, "这个用户名已被占用"
	case errors.Is(err, service.ErrRegistrationDisabled):
		status, code, detail = http.StatusForbidden, ErrorCodeRegistrationDisabled, "管理员已关闭开放注册"
		next = []NextAction{{Action: "oidcStart", Hint: "联系管理员建立账号，或使用单点登录"}}
	case errors.Is(err, service.ErrPasswordLoginDisabled):
		status, code, detail = http.StatusForbidden, ErrorCodePasswordLoginDisabled, "管理员已关闭普通用户的密码登录"
		next = []NextAction{{Action: "oidcStart", Hint: "使用单点登录"}}
	case errors.Is(err, service.ErrLastOwner):
		status, code, detail = http.StatusConflict, ErrorCodeLastOwner, "项目至少要有一个所有者"
		next = []NextAction{{Action: "setMember", Hint: "先把其他成员设为所有者"}}
	case errors.Is(err, service.ErrRateLimited):
		status, code, detail = http.StatusTooManyRequests, ErrorCodeRateLimited, "尝试次数过多，请一分钟后再试"
	case errors.Is(err, service.ErrQueueFull):
		status, code, detail = http.StatusTooManyRequests, ErrorCodeQueueFull, "排队的运行已达上限，请稍后再试"
	case errors.Is(err, service.ErrPreconditionRequired):
		status, code, detail = http.StatusPreconditionRequired, ErrorCodePreconditionRequired, "修改已有资源时必须在 If-Match 中带上读到的版本号"
	case errors.Is(err, service.ErrVersionConflict):
		status, code, detail = http.StatusPreconditionFailed, ErrorCodeVersionConflict, "资源已被其他会话修改，请重新读取后再提交"
		if conflict, ok := errors.AsType[*service.VersionConflictError](err); ok && conflict.Detail != "" {
			detail = conflict.Detail
		}
	case errors.Is(err, service.ErrIdempotencyConflict):
		status, code, detail = http.StatusConflict, ErrorCodeIdempotencyConflict, "这个幂等键已用于另一个请求，请换一个新的键"
	case errors.As(err, new(*service.PayloadTooLargeError)):
		tooLarge, _ := errors.AsType[*service.PayloadTooLargeError](err)
		status, code, detail = http.StatusRequestEntityTooLarge, ErrorCodeValidationFailed, fmt.Sprintf("请求体超过上限 %d 字节", tooLarge.Limit)
	default:
		var zero T
		return zero, err
	}
	p := buildProblem(ctx, status, code, detail)
	if conflict, ok := errors.AsType[*service.VersionConflictError](err); ok && conflict.Current != 0 {
		p.CurrentVersion = &conflict.Current
	}
	if next != nil {
		p.NextActions = &next
	}
	if verr != nil {
		fields := make([]FieldError, len(verr.Fields))
		for i, f := range verr.Fields {
			fields[i] = FieldError{Detail: f.Detail}
			if f.Pointer != "" {
				fields[i].Pointer = new(f.Pointer)
			}
			if f.Parameter != "" {
				fields[i].Parameter = new(f.Parameter)
			}
		}
		p.Errors = &fields
	}
	return T{Body: p, StatusCode: status}, nil
}

func toMe(a service.Account) Me {
	return Me{
		ID:                a.User.ID,
		Username:          a.User.Username,
		DisplayName:       optional(a.User.DisplayName),
		Email:             optional(a.User.Email),
		IsAdmin:           a.User.IsAdmin,
		PersonalProjectID: a.PersonalProjectID,
		CreatedAt:         a.User.CreatedAt,
		Locale:            optionalEnum[MeLocale](a.User.Locale),
		Timezone:          optional(a.User.Timezone),
		HasPassword:       a.User.PasswordHash != "",
		Identities:        toIdentities(a.Identities),
	}
}

func toIdentities(ids []store.Identity) []IdentityInfo {
	out := make([]IdentityInfo, len(ids))
	for i, id := range ids {
		out[i] = IdentityInfo{Issuer: id.Issuer, Username: optional(id.Username), LinkedAt: id.CreatedAt}
	}
	return out
}

// optionalEnum is optional for enum types; a stored value the contract
// does not know is reported as unset rather than breaking clients.
func optionalEnum[T interface {
	~string
	Valid() bool
}](s string) *T {
	v := T(s)
	if s == "" || !v.Valid() {
		return nil
	}
	return &v
}

func toAccessToken(t store.AccessToken) AccessToken {
	scopes := make([]TokenScope, len(t.Scopes))
	for i, s := range t.Scopes {
		scopes[i] = TokenScope(s)
	}
	return AccessToken{
		ID:         t.ID,
		Name:       t.Name,
		Prefix:     t.Prefix,
		Scopes:     scopes,
		CreatedAt:  t.CreatedAt,
		ExpiresAt:  optionalTime(t.ExpiresAt),
		LastUsedAt: optionalTime(t.LastUsedAt),
		RevokedAt:  optionalTime(t.RevokedAt),
	}
}

func toPrincipal(p *auth.Principal) *Principal {
	if p == nil {
		return nil
	}
	scopes := make([]TokenScope, len(p.Scopes))
	for i, s := range p.Scopes {
		scopes[i] = TokenScope(s)
	}
	return &Principal{
		UserID:            p.UserID,
		Username:          p.Username,
		DisplayName:       optional(p.DisplayName),
		IsAdmin:           p.IsAdmin,
		PersonalProjectID: p.PersonalProjectID,
		AuthMethod:        AuthMethod(p.Method),
		Scopes:            scopes,
	}
}

// optional maps the empty string to null.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// optionalTime maps the zero time to null.
func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
