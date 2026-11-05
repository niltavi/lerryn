package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
)

// oidcStateCookie carries the state, nonce and PKCE verifier of a single
// sign-on login from oidcStart to oidcCallback. It is scoped to the OIDC
// paths and signed by the auth package.
const (
	oidcStateCookie = "lerryn_oidc"
	oidcCookiePath  = BasePath + "auth/oidc/"
)

// consoleLoginPath is where a failed single sign-on lands, with ?error=.
const consoleLoginPath = "/login"

type oidcStateKey struct{}

// withOIDCState hands the state cookie to the OIDC callback. Strict
// handlers see only their parameters, and the contract does not list the
// cookie as one: it is an implementation detail of the flow.
func withOIDCState(f StrictHandlerFunc, operationID string) StrictHandlerFunc {
	if operationKey(operationID) != operationKey("oidcCallback") {
		return f
	}
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		if c, err := r.Cookie(oidcStateCookie); err == nil {
			ctx = context.WithValue(ctx, oidcStateKey{}, c.Value)
		}
		return f(ctx, w, r, request)
	}
}

// OidcStart redirects to the OIDC provider.
func (h *handler) OidcStart(ctx context.Context, req OidcStartRequestObject) (OidcStartResponseObject, error) {
	if h.accounts == nil {
		return nil, errNoAccounts
	}
	returnTo := "/"
	if req.Params.ReturnTo != nil && safeReturnPath(*req.Params.ReturnTo) {
		returnTo = *req.Params.ReturnTo
	}
	authURL, state, err := h.accounts.BeginOIDC(ctx, returnTo)
	if errors.Is(err, service.ErrNotFound) {
		return problemResponse[OidcStartdefaultApplicationProblemPlusJSONResponse](ctx, http.StatusNotFound,
			ErrorCodeNotFound, "这个部署没有配置单点登录"), nil
	}
	if err != nil {
		return errorResponse[OidcStartdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	cookie := h.oidcCookie(state, int(auth.OIDCStateTTL.Seconds()))
	return OidcStart302Response{Headers: OidcStart302ResponseHeaders{Location: &authURL, SetCookie: &cookie}}, nil
}

// OidcCallback completes an OIDC login and redirects to the console.
// Failures redirect to the login page with only an error code.
func (h *handler) OidcCallback(ctx context.Context, req OidcCallbackRequestObject) (OidcCallbackResponseObject, error) {
	if h.accounts == nil {
		return nil, errNoAccounts
	}
	cleared := h.oidcCookie("", -1)
	failed := func(code ErrorCode) (OidcCallbackResponseObject, error) {
		loc := h.consoleLink(consoleLoginPath + "?" + url.Values{"error": {string(code)}}.Encode())
		return oidcRedirect{location: loc, cookies: []string{cleared}}, nil
	}
	if req.Params.Error != nil {
		// The user declined or the provider failed; nothing to verify.
		return failed(ErrorCodeUnauthenticated)
	}
	state, _ := ctx.Value(oidcStateKey{}).(string)
	_, sess, returnTo, err := h.accounts.FinishOIDC(ctx, state, deref(req.Params.State), deref(req.Params.Code))
	switch {
	case errors.Is(err, service.ErrNotFound):
		return problemResponse[OidcCallbackdefaultApplicationProblemPlusJSONResponse](ctx, http.StatusNotFound,
			ErrorCodeNotFound, "这个部署没有配置单点登录"), nil
	case errors.Is(err, service.ErrUnauthenticated):
		return failed(ErrorCodeUnauthenticated)
	case err != nil:
		h.log.ErrorContext(ctx, "oidc callback", "error", err)
		return failed(ErrorCodeInternalError)
	}
	if !safeReturnPath(returnTo) {
		returnTo = "/"
	}
	return oidcRedirect{location: h.consoleLink(returnTo), cookies: []string{h.sessionCookie(ctx, sess), cleared}}, nil
}

// oidcRedirect is the 302 of oidcCallback. The generated response type can
// set one Set-Cookie header; the callback sets the session and clears the
// state cookie.
type oidcRedirect struct {
	location string
	cookies  []string
}

func (o oidcRedirect) VisitOidcCallbackResponse(w http.ResponseWriter) error {
	w.Header().Set("Location", o.location)
	for _, c := range o.cookies {
		w.Header().Add("Set-Cookie", c)
	}
	w.WriteHeader(http.StatusFound)
	return nil
}

func (h *handler) oidcCookie(value string, maxAge int) string {
	c := &http.Cookie{ // #nosec G124 -- Secure is set whenever public_url is https
		Name:     oidcStateCookie,
		Value:    value,
		Path:     oidcCookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   h.secureCookies,
		// Lax: the provider sends the browser back with a top-level GET,
		// which carries Lax cookies.
		SameSite: http.SameSiteLaxMode,
	}
	if maxAge > 0 {
		c.Expires = time.Now().Add(time.Duration(maxAge) * time.Second).UTC()
	}
	return c.String()
}

// safeReturnPath accepts only a path on the console itself, so the login
// cannot be used to redirect elsewhere.
func safeReturnPath(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "/\\") &&
		!strings.ContainsAny(p, "\r\n") && len(p) <= 512
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// CreatePairingCode issues a one-time code for pairing a mobile device.
func (h *handler) CreatePairingCode(ctx context.Context, _ CreatePairingCodeRequestObject) (CreatePairingCodeResponseObject, error) {
	code, err := h.accounts.CreatePairingCode(ctx, auth.PrincipalFrom(ctx))
	if err != nil {
		return errorResponse[CreatePairingCodedefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return CreatePairingCode201JSONResponse{Code: code.Code, URI: code.URI, ExpiresAt: code.ExpiresAt}, nil
}

// ExchangePairingCode trades a pairing code for a read-only device session.
func (h *handler) ExchangePairingCode(ctx context.Context, req ExchangePairingCodeRequestObject) (ExchangePairingCodeResponseObject, error) {
	if h.accounts == nil {
		return nil, errNoAccounts
	}
	acct, sess, err := h.accounts.ExchangePairingCode(ctx, req.Body.Server, req.Body.Code, toDevice(req.Body.Device))
	if errors.Is(err, service.ErrUnauthenticated) {
		return problemResponse[ExchangePairingCodedefaultApplicationProblemPlusJSONResponse](ctx, http.StatusUnauthorized,
			ErrorCodeUnauthenticated, "配对码无效、已过期或已使用，请在控制台重新生成"), nil
	}
	if err != nil {
		return errorResponse[ExchangePairingCodedefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	cookie := h.sessionCookie(ctx, sess)
	return ExchangePairingCode200JSONResponse{Body: toMe(acct), Headers: ExchangePairingCode200ResponseHeaders{SetCookie: &cookie}}, nil
}

func toDevice(d DeviceInfo) service.DeviceInfo {
	return service.DeviceInfo{Name: d.Name, Platform: string(d.Platform), OSVersion: deref(d.OsVersion)}
}

// UpdateMe changes the caller's profile.
func (h *handler) UpdateMe(ctx context.Context, req UpdateMeRequestObject) (UpdateMeResponseObject, error) {
	in := service.UpdateMeInput{DisplayName: req.Body.DisplayName, Email: req.Body.Email, Timezone: req.Body.Timezone}
	if req.Body.Locale != nil {
		l := string(*req.Body.Locale)
		in.Locale = &l
	}
	acct, err := h.accounts.UpdateMe(ctx, auth.PrincipalFrom(ctx), in)
	if err != nil {
		return errorResponse[UpdateMedefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return UpdateMe200JSONResponse(toMe(acct)), nil
}

// ChangePassword changes the caller's password.
func (h *handler) ChangePassword(ctx context.Context, req ChangePasswordRequestObject) (ChangePasswordResponseObject, error) {
	err := h.accounts.ChangePassword(ctx, auth.PrincipalFrom(ctx), req.Body.CurrentPassword, req.Body.NewPassword)
	if errors.Is(err, service.ErrUnauthenticated) {
		return problemResponse[ChangePassworddefaultApplicationProblemPlusJSONResponse](ctx, http.StatusUnauthorized,
			ErrorCodeUnauthenticated, "当前密码不正确"), nil
	}
	if err != nil {
		return errorResponse[ChangePassworddefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return ChangePassword204Response{}, nil
}

// ListDevices lists the caller's mobile device sessions.
func (h *handler) ListDevices(ctx context.Context, _ ListDevicesRequestObject) (ListDevicesResponseObject, error) {
	devices, err := h.accounts.ListDevices(ctx, auth.PrincipalFrom(ctx))
	if err != nil {
		return errorResponse[ListDevicesdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	out := ListDevices200JSONResponse{Items: make([]Device, len(devices))}
	for i, d := range devices {
		s := d.Session
		platform := DevicePlatform(s.DevicePlatform)
		if !platform.Valid() {
			platform = DevicePlatformOther
		}
		out.Items[i] = Device{
			ID: s.ID, Name: s.DeviceName, Platform: platform, OsVersion: optional(s.DeviceOSVersion),
			CreatedAt: s.CreatedAt, LastSeenAt: s.LastSeenAt, ExpiresAt: s.ExpiresAt, Current: d.Current,
		}
	}
	return out, nil
}

// RevokeDevice ends one of the caller's device sessions.
func (h *handler) RevokeDevice(ctx context.Context, req RevokeDeviceRequestObject) (RevokeDeviceResponseObject, error) {
	if err := h.accounts.RevokeDevice(ctx, auth.PrincipalFrom(ctx), req.DeviceID); err != nil {
		return errorResponse[RevokeDevicedefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return RevokeDevice204Response{}, nil
}
