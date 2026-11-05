package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// SessionCookieName is the cookie that carries a login session.
const SessionCookieName = "lerryn_session"

// Accounts is the account service the API calls. *service.Accounts
// implements it; tests may substitute their own.
type Accounts interface {
	Register(ctx context.Context, in service.RegisterInput) (service.Account, service.NewSession, error)
	Login(ctx context.Context, username, password string) (service.Account, service.NewSession, error)
	Logout(ctx context.Context, p *auth.Principal) error
	Authenticate(ctx context.Context, token string) (*auth.Principal, error)
	Me(ctx context.Context, p *auth.Principal) (service.Account, error)
	CreateToken(ctx context.Context, p *auth.Principal, in service.CreateTokenInput) (string, store.AccessToken, error)
	ListTokens(ctx context.Context, p *auth.Principal, limit *int, cursor string) ([]store.AccessToken, string, error)
	RevokeToken(ctx context.Context, p *auth.Principal, id uuid.UUID) error

	LoginDevice(ctx context.Context, username, password string, device *service.DeviceInfo) (service.Account, service.NewSession, error)
	SignupState(ctx context.Context) (open, first bool, err error)
	PasswordLoginForAll(ctx context.Context) (bool, error)
	UpdateMe(ctx context.Context, p *auth.Principal, in service.UpdateMeInput) (service.Account, error)
	ChangePassword(ctx context.Context, p *auth.Principal, current, next string) error
	CreatePairingCode(ctx context.Context, p *auth.Principal) (service.PairingCode, error)
	ExchangePairingCode(ctx context.Context, server, code string, device service.DeviceInfo) (service.Account, service.NewSession, error)
	ListDevices(ctx context.Context, p *auth.Principal) ([]service.Device, error)
	RevokeDevice(ctx context.Context, p *auth.Principal, id uuid.UUID) error
	BeginOIDC(ctx context.Context, returnTo string) (authURL, cookie string, err error)
	FinishOIDC(ctx context.Context, cookie, state, code string) (service.Account, service.NewSession, string, error)
}

// Projects is the project service the API calls; *service.Projects
// implements it.
type Projects interface {
	List(ctx context.Context, p *auth.Principal, limit *int, cursor string) ([]service.ProjectView, string, error)
	Create(ctx context.Context, p *auth.Principal, name string) (service.ProjectView, error)
	Get(ctx context.Context, p *auth.Principal, id uuid.UUID) (service.ProjectView, error)
	Rename(ctx context.Context, p *auth.Principal, id uuid.UUID, name string) (service.ProjectView, error)
	Members(ctx context.Context, p *auth.Principal, id uuid.UUID, limit *int, cursor string) ([]service.Member, string, error)
	SetMember(ctx context.Context, p *auth.Principal, projectID, userID uuid.UUID, role string) (service.Member, error)
	RemoveMember(ctx context.Context, p *auth.Principal, projectID, userID uuid.UUID) error
	SearchUsers(ctx context.Context, p *auth.Principal, q string, limit *int, cursor string) ([]store.User, string, error)
}

// Admin is the account administration service; *service.Admin
// implements it.
type Admin interface {
	ListUsers(ctx context.Context, p *auth.Principal, f service.UserFilter) ([]service.AdminUser, string, error)
	CreateUser(ctx context.Context, p *auth.Principal, in service.AdminCreateUserInput) (service.AdminUser, error)
	UpdateUser(ctx context.Context, p *auth.Principal, id uuid.UUID, in service.AdminUpdateUserInput) (service.AdminUser, error)
	ResetPassword(ctx context.Context, p *auth.Principal, id uuid.UUID, password string) error
	Settings(ctx context.Context, p *auth.Principal) (service.SettingsView, error)
	UpdateSettings(ctx context.Context, p *auth.Principal, in service.SettingsInput) (service.SettingsView, error)
}

// Audit records generic audit events for API calls and answers the audit
// query; *service.Audit implements it.
type Audit interface {
	RecordCall(ctx context.Context, out service.CallOutcome)
	List(ctx context.Context, p *auth.Principal, f service.AuditFilter) ([]store.AuditEvent, string, error)
}

var errNoAccounts = errors.New("httpapi: no account service configured")

// authenticate resolves the request's credentials and puts the principal in
// the context. Rules:
//
//   - An Authorization header must carry a valid access token; anything
//     else is answered with 401 at once.
//   - A session cookie that is no longer valid is ignored rather than
//     rejected, so a browser with a stale cookie can still log in.
//   - A write made with a session cookie must come from the console's own
//     origin, which stops cross-site request forgery.
//
// Whether an operation needs a principal at all is decided per operation by
// requireAccess.
func (h *handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = h.withConsoleBase(r)
		ctx := auth.WithClient(r.Context(), h.proxies.Client(r))
		r = r.WithContext(ctx)

		token, fromCookie, ok := credential(r)
		if !ok {
			unauthorized(w, r, "Authorization 头只接受 Bearer 访问令牌")
			return
		}
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		p, err := h.resolve(ctx, token)
		switch {
		case errors.Is(err, service.ErrUnauthenticated) && fromCookie:
			next.ServeHTTP(w, r)
			return
		case errors.Is(err, service.ErrUnauthenticated):
			unauthorized(w, r, "访问令牌无效、已过期或已撤销")
			return
		case err != nil:
			h.responseError(w, r, err)
			return
		}
		// A read-only device session can make no write but logging itself
		// out, so a forged request gains nothing; the mobile app sends no
		// Origin, so requiring one would keep it from logging out.
		if fromCookie && !p.ReadOnly && !safeMethod(r.Method) && !h.sameOrigin(r) {
			writeProblem(w, newProblem(r, http.StatusForbidden, ErrorCodeOriginNotAllowed,
				"用会话发起的写操作必须来自控制台页面；Agent 和脚本请改用访问令牌"))
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(ctx, p)))
	})
}

func (h *handler) resolve(ctx context.Context, token string) (*auth.Principal, error) {
	if h.accounts == nil {
		return nil, service.ErrUnauthenticated
	}
	return h.accounts.Authenticate(ctx, token)
}

// requireAccess is a strict middleware that applies an operation's access
// rules from the contract: credentials unless the operation allows
// anonymous calls, the token scope for its x-operation-kind, and a session
// for session-only operations.
func (h *handler) requireAccess(f StrictHandlerFunc, operationID string) StrictHandlerFunc {
	key := operationKey(operationID)
	rules, known := operations[key]
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		if !known {
			return nil, errors.New("httpapi: no access rules for operation " + operationID)
		}
		p := auth.PrincipalFrom(ctx)
		if p != nil && p.ReadOnly && key == operationKey("logout") {
			// The one write a read-only device session may make: ending
			// itself.
			return f(ctx, w, r, request)
		}
		switch status, detail := denial(p, rules); status {
		case 0:
			return f(ctx, w, r, request)
		case http.StatusUnauthorized:
			unauthorized(w, r, detail)
		default:
			writeProblem(w, newProblem(r, status, ErrorCodeInsufficientScope, detail))
		}
		return nil, nil
	}
}

// denial returns the status and detail of the answer to a caller p that may
// not call an operation with rules, or a zero status when it may.
func denial(p *auth.Principal, rules operation) (status int, detail string) {
	switch {
	case rules.hook:
		// Reachable only through NewHooks; the trigger token is checked
		// by the trigger service, and no principal is involved.
		return 0, ""
	case p == nil && rules.anonymous:
		return 0, ""
	case p == nil:
		return http.StatusUnauthorized, "需要访问令牌或登录会话"
	case p.ReadOnly && !p.HasScope(rules.scope):
		return http.StatusForbidden, "移动端的设备会话只能查看，不能执行写操作"
	case rules.sessionOnly && p.Method != auth.MethodSession:
		return http.StatusForbidden, "这个操作只能在登录会话中调用"
	case !p.HasScope(rules.scope):
		return http.StatusForbidden, "访问令牌缺少 " + string(rules.scope) + " 范围"
	}
	return 0, ""
}

// RequireAccessToken guards an endpoint that only agents use, such as /mcp:
// the request must carry a valid access token, and the response to a
// missing or bad one is a 401 that MCP clients understand.
func RequireAccessToken(accounts Accounts, proxies auth.TrustedProxies, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, fromCookie, ok := credential(r)
		if !ok || token == "" || fromCookie {
			unauthorized(w, r, "需要在 Authorization 头中提供访问令牌")
			return
		}
		ctx := auth.WithClient(r.Context(), proxies.Client(r))
		if _, err := accounts.Authenticate(ctx, token); err != nil {
			if !errors.Is(err, service.ErrUnauthenticated) {
				writeProblem(w, newProblem(r, http.StatusInternalServerError, ErrorCodeInternalError, ""))
				return
			}
			unauthorized(w, r, "访问令牌无效、已过期或已撤销")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// credential returns the token of the request and whether it came from the
// session cookie. ok is false for an Authorization header that is not a
// Bearer access token.
func credential(r *http.Request) (token string, fromCookie, ok bool) {
	if h := r.Header.Get("Authorization"); h != "" {
		scheme, value, found := strings.Cut(h, " ")
		value = strings.TrimSpace(value)
		if !found || !strings.EqualFold(scheme, "Bearer") || !strings.HasPrefix(value, auth.AccessTokenPrefix) {
			return "", false, false
		}
		return value, false, true
	}
	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		return c.Value, true, true
	}
	return "", false, true
}

// sameOrigin reports whether the request's Origin is one of the console's
// origins: public_url's or an extra one. Browsers send Origin on every
// cross-site and same-site write, so a missing Origin on a cookie write is
// refused too.
func (h *handler) sameOrigin(r *http.Request) bool {
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || origin.Host == "" {
		return false
	}
	if len(h.origins) == 0 {
		return strings.EqualFold(origin.Host, r.Host)
	}
	for _, o := range h.origins {
		if strings.EqualFold(origin.Scheme, o.Scheme) && strings.EqualFold(origin.Host, o.Host) {
			return true
		}
	}
	return false
}

type consoleBaseKey struct{}

// withConsoleBase records the console base URL for a request that came in
// at one of the extra origins, found by its Host: that origin with
// public_url's path. The scheme comes from the configuration, since a
// proxy in front of the server terminates TLS. Only discover's console
// link uses it: the console compares that link with its own address to
// warn when it was opened at an address where writes are refused. Links
// handed to agents keep public_url.
func (h *handler) withConsoleBase(r *http.Request) *http.Request {
	public, err := url.Parse(h.publicURL)
	if err != nil || strings.EqualFold(r.Host, public.Host) {
		return r
	}
	for _, o := range h.origins {
		if strings.EqualFold(r.Host, o.Host) {
			base := o.String() + public.Path
			return r.WithContext(context.WithValue(r.Context(), consoleBaseKey{}, base))
		}
	}
	return r
}

// consoleBase is the base URL withConsoleBase recorded, or public_url.
func (h *handler) consoleBase(ctx context.Context) string {
	if b, ok := ctx.Value(consoleBaseKey{}).(string); ok {
		return b
	}
	return h.publicURL
}

// sessionCookie returns the Set-Cookie value for a new session. Secure
// follows the scheme of the address the console was opened at: a plain-http
// address cannot use Secure cookies, every https one gets them.
func (h *handler) sessionCookie(ctx context.Context, s service.NewSession) string {
	return (&http.Cookie{ // #nosec G124 -- Secure is set whenever the console's address is https
		Name:     SessionCookieName,
		Value:    s.Token,
		Path:     "/",
		Expires:  s.ExpiresAt.UTC(),
		MaxAge:   int(time.Until(s.ExpiresAt).Seconds()),
		HttpOnly: true,
		Secure:   h.secureSession(ctx),
		SameSite: http.SameSiteLaxMode,
	}).String()
}

// clearedSessionCookie returns the Set-Cookie value that removes the
// session cookie.
func (h *handler) clearedSessionCookie(ctx context.Context) string {
	return (&http.Cookie{ // #nosec G124 -- same attributes as sessionCookie
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.secureSession(ctx),
		SameSite: http.SameSiteLaxMode,
	}).String()
}

// secureSession reports whether the session cookie of a request is Secure:
// whether the console address it came in at is https.
func (h *handler) secureSession(ctx context.Context) bool {
	return strings.HasPrefix(strings.ToLower(h.consoleBase(ctx)), "https://")
}

func unauthorized(w http.ResponseWriter, r *http.Request, detail string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="lerryn"`)
	writeProblem(w, newProblem(r, http.StatusUnauthorized, ErrorCodeUnauthenticated, detail))
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}
