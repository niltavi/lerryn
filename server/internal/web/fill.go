// Package web serves the server-rendered pages that are not part of the
// JSON API. The first is the credential fill page /c/{requestId} (tech
// design 12.4), where the links agents hand out lead. It works without
// the console or any frontend build, also in mobile browsers. The only
// other way a value enters the platform is the console's fill dialog,
// through the API's session-only fillCredentialRequest.
package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// SessionCookieName is the console's session cookie, shared with the API.
const SessionCookieName = "lerryn_session"

// loginCookieName carries the random value the login form's CSRF token is
// bound to, before there is a session.
const loginCookieName = "lerryn_fill_login"

// maxFormBytes bounds a submitted form: values are limited to 96 KiB per
// credential, plus field names and the token.
const maxFormBytes = 256 << 10

// Credentials is what the fill page needs from the credential service.
// *service.Credentials implements it.
type Credentials interface {
	FillRequest(ctx context.Context, p *auth.Principal, id uuid.UUID) (service.CredentialRequestDetail, error)
	Fill(ctx context.Context, p *auth.Principal, id uuid.UUID, values map[string]string) (store.Credential, error)
}

// Accounts is what the fill page needs from the account service, to let
// a user who opens the link sign in or switch accounts. *service.Accounts
// implements it.
type Accounts interface {
	Authenticate(ctx context.Context, token string) (*auth.Principal, error)
	Login(ctx context.Context, username, password string) (service.Account, service.NewSession, error)
	Logout(ctx context.Context, p *auth.Principal) error
}

// FillOptions configures the fill page.
type FillOptions struct {
	// PublicURL is server.public_url. POSTs must come from its origin or
	// from one of ExtraOrigins (server.extra_origins).
	PublicURL    string
	ExtraOrigins []string
	Credentials  Credentials
	Accounts     Accounts
	// CSRFKey signs the forms' CSRF tokens. Every server of a deployment
	// must use the same key, so a form loaded from one server can be
	// submitted to another; derive it from the master key.
	CSRFKey []byte
	Logger  *slog.Logger
	// Location renders times when the browser does not run the page's
	// script; nil means UTC.
	Location *time.Location
	// TrustedProxies decides whether X-Forwarded-For names the client of a
	// request (server.trusted_proxies). The zero value trusts no proxy.
	TrustedProxies auth.TrustedProxies
}

// Fill serves /c/{requestId}.
type Fill struct {
	opts   FillOptions
	public *url.URL
	// origins are public's origin and the extra ones POSTs may come from.
	origins []*url.URL
	secure  bool
	log     *slog.Logger
	page    *template.Template
}

// NewFill returns the fill page handler. Mount it at "/c/".
func NewFill(opts FillOptions) (*Fill, error) {
	if opts.Credentials == nil || opts.Accounts == nil {
		return nil, errors.New("web: the fill page needs the credential and account services")
	}
	if len(opts.CSRFKey) < 32 {
		return nil, errors.New("web: the fill page needs a CSRF key of at least 32 bytes")
	}
	public, err := url.Parse(strings.TrimRight(opts.PublicURL, "/"))
	if err != nil || public.Host == "" {
		return nil, errors.New("web: the fill page needs server.public_url")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Location == nil {
		opts.Location = time.UTC
	}
	origins := []*url.URL{public}
	for _, o := range opts.ExtraOrigins {
		if u, err := url.Parse(strings.TrimRight(o, "/")); err == nil && u.Host != "" {
			origins = append(origins, u)
		}
	}
	return &Fill{
		opts:    opts,
		public:  public,
		origins: origins,
		secure:  strings.EqualFold(public.Scheme, "https"),
		log:     opts.Logger,
		page:    pageTemplate,
	}, nil
}

// ServeHTTP routes GET and POST /c/{id}, POST /c/{id}/login and POST
// /c/{id}/switch.
func (f *Fill) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/c/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	idText, action, _ := strings.Cut(rest, "/")
	ctx := auth.WithClient(r.Context(), f.opts.TrustedProxies.Client(r))
	r = r.WithContext(ctx)
	id, err := uuid.Parse(idText)
	if err != nil || strings.Contains(action, "/") {
		f.render(w, r, http.StatusNotFound, pageData{State: stateUnavailable})
		return
	}
	switch {
	case action == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		f.show(w, r, id)
	case action == "" && r.Method == http.MethodPost:
		f.submit(w, r, id)
	case action == "login" && r.Method == http.MethodPost:
		f.login(w, r, id)
	case action == "switch" && r.Method == http.MethodPost:
		f.switchAccount(w, r, id)
	case action == "" || action == "login" || action == "switch":
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	default:
		f.render(w, r, http.StatusNotFound, pageData{State: stateUnavailable})
	}
}

// Page states.
const (
	stateLogin       = "login"
	stateForm        = "form"
	stateSaved       = "saved"
	stateSubmitted   = "submitted"
	stateExpired     = "expired"
	stateCancelled   = "cancelled"
	stateOtherUser   = "other_user"
	stateUnavailable = "unavailable"
)

// pageData is everything the template shows. It never holds a value the
// user typed: after a submission, successful or not, nothing is echoed.
type pageData struct {
	State     string
	Nonce     string
	Action    string
	CSRF      string
	Username  string
	Initial   string
	Host      string
	Title     string
	Purpose   string
	Client    string
	TaskBound bool
	Update    bool
	Expires   timeView
	Fields    []fieldView
	// FormError is a message about the whole form.
	FormError    string
	LoginError   string
	CredentialID string
	SwitchAction string
	SwitchCSRF   string
	SettingsURL  string
	// ConsoleURL is the console's home page, the way out of every final state.
	ConsoleURL string
}

type fieldView struct {
	Name        string
	ID          string
	Label       string
	Description string
	Secret      bool
	Multiline   bool
	Optional    bool
	Error       string
}

type timeView struct {
	ISO  string
	Text string
}

func (f *Fill) timeView(t time.Time) timeView {
	return timeView{ISO: t.UTC().Format(time.RFC3339), Text: t.In(f.opts.Location).Format("1月2日 15:04")}
}

// principal returns the session of the request, or nil. Only the session
// cookie counts: an access token, which an agent holds, cannot open the
// page, and a stale cookie is treated as no session.
func (f *Fill) principal(r *http.Request) *auth.Principal {
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	p, err := f.opts.Accounts.Authenticate(r.Context(), c.Value)
	if err != nil {
		if !errors.Is(err, service.ErrUnauthenticated) {
			f.log.ErrorContext(r.Context(), "fill page: authenticate", "error", err)
		}
		return nil
	}
	if p.Method != auth.MethodSession {
		return nil
	}
	return p
}

func (f *Fill) show(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p := f.principal(r)
	if p == nil {
		f.renderLogin(w, r, id, http.StatusOK, "")
		return
	}
	f.renderRequest(w, r, p, id, http.StatusOK, nil, "")
}

// renderRequest shows the request to p: the form while it is open, or its
// outcome. fieldErrors and formError come from a rejected submission.
func (f *Fill) renderRequest(w http.ResponseWriter, r *http.Request, p *auth.Principal, id uuid.UUID, status int, fieldErrors map[string]string, formError string) {
	d, err := f.opts.Credentials.FillRequest(r.Context(), p, id)
	data := pageData{Username: p.Username, Initial: initial(p)}
	switch {
	case errors.Is(err, service.ErrNotFound):
		data.State = stateOtherUser
		data.SwitchAction = "/c/" + id.String() + "/switch"
		data.SwitchCSRF = f.token("switch", id, p.CredentialID.String())
		f.render(w, r, http.StatusNotFound, data)
		return
	case err != nil:
		f.fail(w, r, err)
		return
	}
	req := d.Request
	switch req.Status {
	case store.CredentialRequestFulfilled:
		data.State = stateSubmitted
		if r.URL.Query().Get("saved") == "1" {
			data.State = stateSaved
		}
		data.CredentialID = req.CredentialID.String()
		data.SettingsURL = f.public.String() + "/settings/credentials"
		f.render(w, r, status, data)
		return
	case store.CredentialRequestExpired:
		data.State = stateExpired
		data.Expires = f.timeView(req.ExpiresAt)
		f.render(w, r, status, data)
		return
	case store.CredentialRequestCancelled:
		data.State = stateCancelled
		f.render(w, r, status, data)
		return
	}

	data.State = stateForm
	data.Action = "/c/" + id.String()
	data.CSRF = f.token("fill", id, p.CredentialID.String())
	data.Purpose = req.Purpose
	data.Client = req.Client
	data.Expires = f.timeView(req.ExpiresAt)
	data.FormError = formError
	switch {
	case d.Task != nil:
		data.Title = d.Task.Name + " 需要凭证"
		data.TaskBound = true
	default:
		data.Title = "更新凭证 " + req.Name
		data.Update = true
	}
	for i, fl := range req.Fields {
		data.Fields = append(data.Fields, fieldView{
			Name:        fl.Name,
			ID:          "f" + strconv.Itoa(i),
			Label:       fl.Label,
			Description: fl.Description,
			Secret:      fl.Secret,
			Multiline:   fl.Multiline,
			Optional:    fl.Optional,
			Error:       fieldErrors[fl.Name],
		})
	}
	f.render(w, r, status, data)
}

func (f *Fill) submit(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if !f.sameOrigin(r) {
		f.forbidden(w, r)
		return
	}
	p := f.principal(r)
	if p == nil {
		f.renderLogin(w, r, id, http.StatusUnauthorized, "登录已失效，请重新登录后再填写")
		return
	}
	if err := f.parseForm(w, r); err != nil {
		f.renderRequest(w, r, p, id, http.StatusRequestEntityTooLarge, nil, "提交的内容过大")
		return
	}
	if !f.validToken(r.PostForm.Get("csrf"), "fill", id, p.CredentialID.String()) {
		f.forbidden(w, r)
		return
	}
	values := map[string]string{}
	// Inputs are named after the credential's fields, which are upper-case
	// identifiers and cannot clash with the lower-case csrf field.
	for name, vs := range r.PostForm {
		if name != "csrf" && len(vs) == 1 {
			values[name] = vs[0]
		}
	}
	_, err := f.opts.Credentials.Fill(r.Context(), p, id, values)
	clear(values)
	var verr *service.ValidationError
	switch {
	case err == nil:
		http.Redirect(w, r, "/c/"+id.String()+"?saved=1", http.StatusSeeOther) // #nosec G710 -- id is a parsed UUID; the target is this page
	case errors.As(err, &verr):
		fieldErrors := map[string]string{}
		formError := ""
		for _, fe := range verr.Fields {
			if name, ok := strings.CutPrefix(fe.Pointer, "#/"); ok && name != "" {
				fieldErrors[name] = fe.Detail
			} else {
				formError = fe.Detail
			}
		}
		f.renderRequest(w, r, p, id, http.StatusUnprocessableEntity, fieldErrors, formError)
	case errors.Is(err, service.ErrRequestClosed), errors.Is(err, service.ErrNotFound):
		// Expired, cancelled or fulfilled meanwhile; the page shows which.
		f.renderRequest(w, r, p, id, http.StatusConflict, nil, "")
	case errors.Is(err, service.ErrForbidden):
		f.render(w, r, http.StatusForbidden, pageData{State: stateUnavailable, Username: p.Username, Initial: initial(p)})
	default:
		f.fail(w, r, err)
	}
}

func (f *Fill) login(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if !f.sameOrigin(r) {
		f.forbidden(w, r)
		return
	}
	if err := f.parseForm(w, r); err != nil {
		f.renderLogin(w, r, id, http.StatusRequestEntityTooLarge, "")
		return
	}
	c, err := r.Cookie(loginCookieName)
	if err != nil || !f.validToken(r.PostForm.Get("csrf"), "login", id, c.Value) {
		f.forbidden(w, r)
		return
	}
	_, sess, err := f.opts.Accounts.Login(r.Context(), r.PostForm.Get("username"), r.PostForm.Get("password"))
	switch {
	case errors.Is(err, service.ErrUnauthenticated):
		f.renderLogin(w, r, id, http.StatusUnauthorized, "用户名或密码不正确")
		return
	case errors.Is(err, service.ErrRateLimited):
		f.renderLogin(w, r, id, http.StatusTooManyRequests, "尝试次数过多，请一分钟后再试")
		return
	case errors.Is(err, service.ErrPasswordLoginDisabled):
		f.renderLogin(w, r, id, http.StatusForbidden, "管理员已关闭普通用户的密码登录，请在控制台使用单点登录后再打开这个链接")
		return
	case err != nil:
		f.fail(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- HttpOnly and SameSite set; Secure whenever public_url is https
		Name: SessionCookieName, Value: sess.Token, Path: "/",
		Expires: sess.ExpiresAt.UTC(), MaxAge: int(time.Until(sess.ExpiresAt).Seconds()),
		HttpOnly: true, Secure: f.secure, SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, f.loginCookie("", -1))
	http.Redirect(w, r, "/c/"+id.String(), http.StatusSeeOther) // #nosec G710 -- id is a parsed UUID; the target is this page
}

// switchAccount ends the current session, so the user can sign in with
// the account the request is addressed to.
func (f *Fill) switchAccount(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if !f.sameOrigin(r) {
		f.forbidden(w, r)
		return
	}
	if err := f.parseForm(w, r); err != nil {
		f.forbidden(w, r)
		return
	}
	p := f.principal(r)
	if p != nil {
		if !f.validToken(r.PostForm.Get("csrf"), "switch", id, p.CredentialID.String()) {
			f.forbidden(w, r)
			return
		}
		if err := f.opts.Accounts.Logout(r.Context(), p); err != nil {
			f.fail(w, r, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- HttpOnly and SameSite set; Secure whenever public_url is https
		Name: SessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: f.secure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/c/"+id.String(), http.StatusSeeOther) // #nosec G710 -- id is a parsed UUID; the target is this page
}

func (f *Fill) renderLogin(w http.ResponseWriter, r *http.Request, id uuid.UUID, status int, msg string) {
	var raw [24]byte
	_, _ = rand.Read(raw[:])
	nonce := base64.RawURLEncoding.EncodeToString(raw[:])
	http.SetCookie(w, f.loginCookie(nonce, 1800))
	f.render(w, r, status, pageData{
		State:      stateLogin,
		Action:     "/c/" + id.String() + "/login",
		CSRF:       f.token("login", id, nonce),
		Host:       f.public.Host,
		LoginError: msg,
	})
}

func (f *Fill) loginCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{ // #nosec G124 -- HttpOnly and SameSite set; Secure whenever public_url is https
		Name: loginCookieName, Value: value, Path: "/c/", MaxAge: maxAge,
		HttpOnly: true, Secure: f.secure, SameSite: http.SameSiteStrictMode,
	}
}

func (f *Fill) parseForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	return r.ParseForm()
}

// token is the CSRF token of a form: an HMAC over its purpose, the
// request and what the form is bound to (the session, or before login the
// login cookie). A token taken from one session or request is useless for
// any other.
func (f *Fill) token(purpose string, id uuid.UUID, binding string) string {
	m := hmac.New(sha256.New, f.opts.CSRFKey)
	m.Write([]byte("lerryn/fill-csrf/v1\x00" + purpose + "\x00" + id.String() + "\x00" + binding))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (f *Fill) validToken(got, purpose string, id uuid.UUID, binding string) bool {
	if got == "" || binding == "" {
		return false
	}
	return hmac.Equal([]byte(got), []byte(f.token(purpose, id, binding)))
}

// sameOrigin requires the POST to come from the deployment's own pages.
// Browsers send Origin on form submissions; the page's referrer policy
// keeps it for same-origin requests.
func (f *Fill) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	for _, o := range f.origins {
		if strings.EqualFold(u.Scheme, o.Scheme) && strings.EqualFold(u.Host, o.Host) {
			return true
		}
	}
	return false
}

func (f *Fill) forbidden(w http.ResponseWriter, r *http.Request) {
	f.render(w, r, http.StatusForbidden, pageData{State: stateUnavailable})
}

func (f *Fill) fail(w http.ResponseWriter, r *http.Request, err error) {
	f.log.ErrorContext(r.Context(), "fill page failed", "path", r.URL.Path, "error", err)
	f.render(w, r, http.StatusInternalServerError, pageData{State: stateUnavailable})
}

// render writes the page with headers that keep it out of caches, frames
// and other origins' reach. Scripts and styles run only with the
// per-response nonce.
func (f *Fill) render(w http.ResponseWriter, r *http.Request, status int, data pageData) {
	var raw [18]byte
	_, _ = rand.Read(raw[:])
	data.Nonce = base64.RawStdEncoding.EncodeToString(raw[:])
	data.ConsoleURL = f.public.String() + "/"
	if data.SettingsURL == "" {
		data.SettingsURL = f.public.String() + "/settings/credentials"
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Pragma", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'nonce-"+data.Nonce+"'; script-src 'nonce-"+data.Nonce+
		"'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	if err := f.page.Execute(w, data); err != nil {
		f.log.ErrorContext(r.Context(), "render fill page", "error", err)
	}
}

func initial(p *auth.Principal) string {
	name := p.DisplayName
	if name == "" {
		name = p.Username
	}
	for _, r := range name {
		return strings.ToUpper(string(r))
	}
	return ""
}
