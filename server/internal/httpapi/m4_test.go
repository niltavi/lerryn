package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/auth/oidctest"
	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

// m4Client drives the API with every account, project, admin and audit
// service wired, as the server does, plus an optional fake OIDC provider.
type m4Client struct {
	*apiClient
	idp *oidctest.Provider
}

func newM4Client(t *testing.T, withOIDC bool) *m4Client {
	t.Helper()
	db := storetest.Open(t)
	blobs, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := &m4Client{}
	opts := []service.AccountOption{service.WithPublicURL(testOrigin)}
	if withOIDC {
		c.idp = oidctest.New(t)
		o, err := auth.NewOIDC(auth.OIDCConfig{
			Issuer: c.idp.Issuer(), ClientID: c.idp.ClientID, ClientSecret: c.idp.ClientSecret,
			RedirectURL: testOrigin + "/api/v1/auth/oidc/callback",
		})
		if err != nil {
			t.Fatal(err)
		}
		opts = append(opts, service.WithOIDC(o))
	}
	login := []LoginMethod{LoginMethodPassword}
	if withOIDC {
		login = append(login, LoginMethodOidc)
	}
	authz := service.NewAuthorizer(db)
	runs := service.NewRuns(db, authz, nil)
	accounts := service.NewAccounts(db, nil, opts...)
	h := New(Options{
		PublicURL:    testOrigin,
		Version:      "dev",
		Capabilities: Capabilities{RequireTestBeforeActivate: true, LoginMethods: login},
		Accounts:     accounts,
		Tasks:        service.NewTasks(db, authz, blobs, nil),
		Runs:         runs,
		Projects:     service.NewProjects(db, authz, nil),
		Admin:        service.NewAdmin(accounts, nil),
		Audit:        service.NewAudit(db, nil),
	})
	c.apiClient = &apiClient{t: t, h: h}
	return c
}

// register signs up and returns the session cookie and the user.
func (c *m4Client) register(username string) (string, Me) {
	c.t.Helper()
	rec := c.call(http.MethodPost, "/api/v1/auth/register", map[string]any{"username": username, "password": "correct horse"})
	c.want(rec, http.StatusCreated, "")
	return sessionCookie(c.t, rec), decode[Me](c.t, rec)
}

// web sends a console request with the session cookie and the console's
// Origin.
func (c *m4Client) web(cookie, method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	return c.call(method, path, body, "Cookie", cookie, "Origin", testOrigin)
}

func (c *m4Client) token(cookie string, scopes ...string) string {
	c.t.Helper()
	rec := c.web(cookie, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "agent", "scopes": scopes})
	c.want(rec, http.StatusCreated, "")
	return "Bearer " + decode[CreatedToken](c.t, rec).Token
}

// pairDevice pairs a phone for the user of cookie and returns the device
// session cookie.
func (c *m4Client) pairDevice(cookie string) string {
	c.t.Helper()
	rec := c.web(cookie, http.MethodPost, "/api/v1/auth/pairing", nil)
	c.want(rec, http.StatusCreated, "")
	code := decode[PairingCode](c.t, rec)
	// The app sends no Origin.
	rec = c.call(http.MethodPost, "/api/v1/auth/pairing/exchange", map[string]any{
		"server": testOrigin, "code": code.Code, "device": map[string]any{"name": "iPhone", "platform": "ios"},
	})
	c.want(rec, http.StatusOK, "")
	return sessionCookie(c.t, rec)
}

// Every operation that changes something is refused for a device session,
// whatever it is; reads and logging out are allowed (A17, tech design 12.1).
func TestDeviceSessionsAreReadOnly(t *testing.T) {
	c := newM4Client(t, false)
	web, me := c.register("alice")
	device := c.pairDevice(web)

	got := decode[Me](t, c.call(http.MethodGet, "/api/v1/me", nil, "Cookie", device))
	if got.ID != me.ID {
		t.Fatalf("device me = %+v", got)
	}
	devices := decode[DeviceList](t, c.call(http.MethodGet, "/api/v1/me/devices", nil, "Cookie", device))
	if len(devices.Items) != 1 || !devices.Items[0].Current || devices.Items[0].Platform != DevicePlatformIos {
		t.Fatalf("devices = %+v", devices)
	}
	disc := decode[Discovery](t, c.call(http.MethodGet, "/api/v1/discover", nil, "Cookie", device))
	if disc.Principal == nil || len(disc.Principal.Scopes) != 1 || disc.Principal.Scopes[0] != TokenScopeRead {
		t.Fatalf("device principal = %+v", disc.Principal)
	}

	doc, _ := loadSpec(t)
	checked := 0
	for path, item := range doc.Paths.Map() {
		if !strings.HasPrefix(path, BasePath) {
			continue
		}
		for method, op := range item.Operations() {
			if k := op.Extensions["x-operation-kind"]; k == "read" || op.OperationID == "logout" {
				continue
			}
			checked++
			t.Run(op.OperationID, func(t *testing.T) {
				req, headers := sampleRequest(path, item, op)
				headers = append(headers, "Cookie", device, "Origin", testOrigin)
				var body any
				if op.RequestBody != nil {
					body = map[string]any{}
				}
				c.want(c.call(method, req, body, headers...), http.StatusForbidden, ErrorCodeInsufficientScope)
			})
		}
	}
	if checked < 30 {
		t.Fatalf("only %d mutating operations checked", checked)
	}

	// Logging out works without an Origin, which the app does not send.
	c.want(c.call(http.MethodPost, "/api/v1/auth/logout", nil, "Cookie", device), http.StatusNoContent, "")
	c.want(c.call(http.MethodGet, "/api/v1/me", nil, "Cookie", device), http.StatusUnauthorized, ErrorCodeUnauthenticated)
	// The web session is untouched.
	c.want(c.call(http.MethodGet, "/api/v1/me", nil, "Cookie", web), http.StatusOK, "")
}

// sampleRequest fills an operation's path and required query parameters
// and headers with values of the right type.
func sampleRequest(template string, item *openapi3.PathItem, op *openapi3.Operation) (string, []string) {
	path := template
	query := url.Values{}
	var headers []string
	params := append(append(openapi3.Parameters{}, item.Parameters...), op.Parameters...)
	for _, ref := range params {
		p := ref.Value
		v := "x"
		if s := p.Schema.Value; s != nil {
			switch {
			case s.Format == "uuid":
				v = uuid.NewString()
			case s.Type.Is("integer"):
				v = "1"
			case len(s.Enum) > 0:
				v = s.Enum[0].(string)
			case s.Pattern != "":
				v = "digest"
			}
		}
		switch p.In {
		case "path":
			path = strings.ReplaceAll(path, "{"+p.Name+"}", v)
		case "query":
			if p.Required {
				query.Set(p.Name, v)
			}
		case "header":
			if p.Required {
				headers = append(headers, p.Name, `"1"`)
			}
		}
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return path, headers
}

func TestOIDCLoginOverHTTP(t *testing.T) {
	c := newM4Client(t, true)

	rec := c.call(http.MethodGet, "/api/v1/auth/oidc/start?return_to=/runs", nil)
	c.want(rec, http.StatusFound, "")
	var state *http.Cookie
	for _, k := range rec.Result().Cookies() {
		if k.Name == oidcStateCookie {
			state = k
		}
	}
	if state == nil || !state.HttpOnly || !state.Secure || state.Path != "/api/v1/auth/oidc/" || state.MaxAge != 600 {
		t.Fatalf("state cookie = %+v", state)
	}
	cb, err := url.Parse(c.idp.Authorize(rec.Header().Get("Location"), oidctest.User{Subject: "s-1", PreferredUsername: "lin"}))
	if err != nil {
		t.Fatal(err)
	}
	callback := cb.Path + "?" + cb.RawQuery

	// Without the state cookie the callback is refused: a forged link
	// cannot log someone in.
	rec = c.call(http.MethodGet, callback, nil)
	c.want(rec, http.StatusFound, "")
	if loc := rec.Header().Get("Location"); loc != testOrigin+"/login?error=unauthenticated" {
		t.Fatalf("location = %s", loc)
	}

	rec = c.call(http.MethodGet, callback, nil, "Cookie", state.Name+"="+state.Value)
	c.want(rec, http.StatusFound, "")
	if loc := rec.Header().Get("Location"); loc != testOrigin+"/runs" {
		t.Fatalf("location = %s", loc)
	}
	session, cleared := "", false
	for _, k := range rec.Result().Cookies() {
		switch {
		case k.Name == SessionCookieName:
			session = k.Name + "=" + k.Value
		case k.Name == oidcStateCookie && k.MaxAge < 0:
			cleared = true
		}
	}
	if session == "" || !cleared {
		t.Fatalf("cookies = %v", rec.Header().Values("Set-Cookie"))
	}
	me := decode[Me](t, c.call(http.MethodGet, "/api/v1/me", nil, "Cookie", session))
	if me.Username != "lin" || me.HasPassword || len(me.Identities) != 1 || me.Identities[0].Issuer != c.idp.Issuer() {
		t.Fatalf("me = %+v", me)
	}

	// The provider's error and open redirects end on the login page or
	// are refused by the contract.
	rec = c.call(http.MethodGet, "/api/v1/auth/oidc/callback?error=access_denied", nil)
	c.want(rec, http.StatusFound, "")
	c.want(c.call(http.MethodGet, "/api/v1/auth/oidc/start?return_to=//evil.example.com", nil),
		http.StatusBadRequest, ErrorCodeValidationFailed)
}

func TestOIDCNotConfiguredOverHTTP(t *testing.T) {
	c := newM4Client(t, false)
	c.want(c.call(http.MethodGet, "/api/v1/auth/oidc/start", nil), http.StatusNotFound, ErrorCodeNotFound)
}

func TestDiscoverSignupState(t *testing.T) {
	c := newM4Client(t, false)
	caps := decode[Discovery](t, c.call(http.MethodGet, "/api/v1/discover", nil)).Capabilities
	if !caps.FirstAccount || !caps.RegistrationOpen {
		t.Fatalf("empty deployment: %+v", caps)
	}
	admin, _ := c.register("admin")
	c.want(c.web(admin, http.MethodPatch, "/api/v1/admin/settings", map[string]any{"open_registration": false}), http.StatusOK, "")
	caps = decode[Discovery](t, c.call(http.MethodGet, "/api/v1/discover", nil)).Capabilities
	if caps.FirstAccount || caps.RegistrationOpen {
		t.Fatalf("closed deployment: %+v", caps)
	}
	c.want(c.call(http.MethodPost, "/api/v1/auth/register", map[string]any{"username": "bob", "password": "correct horse"}),
		http.StatusForbidden, ErrorCodeRegistrationDisabled)
}

// Turning local password login off takes password out of discover's
// login methods at once, so clients lead with single sign-on, and says
// that admins may still use a password.
func TestDiscoverPasswordLoginSetting(t *testing.T) {
	c := newM4Client(t, true)
	caps := decode[Discovery](t, c.call(http.MethodGet, "/api/v1/discover", nil)).Capabilities
	if !slices.Equal(caps.LoginMethods, []LoginMethod{LoginMethodPassword, LoginMethodOidc}) ||
		caps.AdminPasswordLogin == nil || *caps.AdminPasswordLogin {
		t.Fatalf("default: %+v", caps)
	}
	admin, _ := c.register("admin")
	c.want(c.web(admin, http.MethodPatch, "/api/v1/admin/settings", map[string]any{"local_password_login": false}), http.StatusOK, "")
	caps = decode[Discovery](t, c.call(http.MethodGet, "/api/v1/discover", nil)).Capabilities
	if !slices.Equal(caps.LoginMethods, []LoginMethod{LoginMethodOidc}) || caps.AdminPasswordLogin == nil || !*caps.AdminPasswordLogin {
		t.Fatalf("password login off: %+v", caps)
	}
	c.want(c.web(admin, http.MethodPatch, "/api/v1/admin/settings", map[string]any{"local_password_login": true}), http.StatusOK, "")
	caps = decode[Discovery](t, c.call(http.MethodGet, "/api/v1/discover", nil)).Capabilities
	if !slices.Contains(caps.LoginMethods, LoginMethodPassword) || *caps.AdminPasswordLogin {
		t.Fatalf("password login back on: %+v", caps)
	}
}

func TestAdminEndpoints(t *testing.T) {
	c := newM4Client(t, false)
	admin, _ := c.register("admin")
	bobCookie, bob := c.register("bob")
	bobToken := c.token(bobCookie, "read", "write")

	// Admin operations are for admins only, whatever the credential.
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/users"},
		{http.MethodGet, "/api/v1/admin/settings"},
		{http.MethodGet, "/api/v1/admin/audit-events"},
	} {
		c.want(c.call(r.method, r.path, nil, "Authorization", bobToken), http.StatusForbidden, ErrorCodePermissionDenied)
	}
	c.want(c.call(http.MethodPatch, "/api/v1/admin/users/"+bob.ID.String(), map[string]any{"is_admin": true}, "Authorization", bobToken),
		http.StatusForbidden, ErrorCodeInsufficientScope)

	// Even an admin's token cannot create accounts, change them or reset
	// passwords: a leaked token could otherwise make a new admin, or reset
	// the admin's own password, log in and mint more tokens.
	adminToken := c.token(admin, "read", "write", "execute")
	for _, r := range []struct {
		method, path string
		body         map[string]any
	}{
		{http.MethodPost, "/api/v1/admin/users", map[string]any{"username": "mallory", "password": "correct horse"}},
		{http.MethodPatch, "/api/v1/admin/users/" + bob.ID.String(), map[string]any{"is_admin": true}},
		{http.MethodPost, "/api/v1/admin/users/" + bob.ID.String() + "/password", map[string]any{"password": "new password 1"}},
	} {
		c.want(c.call(r.method, r.path, r.body, "Authorization", adminToken), http.StatusForbidden, ErrorCodeInsufficientScope)
	}

	rec := c.web(admin, http.MethodPost, "/api/v1/admin/users", map[string]any{"username": "carol", "password": "correct horse"})
	c.want(rec, http.StatusCreated, "")
	if u := decode[AdminUser](t, rec); u.Username != "carol" || !u.HasPassword || u.IsAdmin {
		t.Fatalf("created = %+v", u)
	}
	page := decode[AdminUserPage](t, c.web(admin, http.MethodGet, "/api/v1/admin/users?status=active&limit=2", nil))
	if len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("users = %+v", page)
	}
	settings := decode[Settings](t, c.web(admin, http.MethodGet, "/api/v1/admin/settings", nil))
	if !settings.OpenRegistration || !settings.LocalPasswordLogin || settings.Oidc != nil || settings.UpdatedBy != nil {
		t.Fatalf("settings = %+v", settings)
	}
	c.want(c.web(admin, http.MethodPatch, "/api/v1/admin/settings", map[string]any{"local_password_login": false}),
		http.StatusUnprocessableEntity, ErrorCodeValidationFailed)

	// Disabling bob ends his session and token at once (A18).
	c.want(c.call(http.MethodGet, "/api/v1/me", nil, "Authorization", bobToken), http.StatusOK, "")
	rec = c.web(admin, http.MethodPatch, "/api/v1/admin/users/"+bob.ID.String(), map[string]any{"disabled": true})
	c.want(rec, http.StatusOK, "")
	if u := decode[AdminUser](t, rec); u.DisabledAt == nil {
		t.Fatalf("disabled = %+v", u)
	}
	c.want(c.call(http.MethodGet, "/api/v1/me", nil, "Authorization", bobToken), http.StatusUnauthorized, ErrorCodeUnauthenticated)
	c.want(c.call(http.MethodGet, "/api/v1/me", nil, "Cookie", bobCookie), http.StatusUnauthorized, ErrorCodeUnauthenticated)

	c.want(c.web(admin, http.MethodPost, "/api/v1/admin/users/"+bob.ID.String()+"/password", map[string]any{"password": "new password 1"}),
		http.StatusNoContent, "")

	// The audit log has the changes, with the entry point and request ID.
	rec = c.web(admin, http.MethodGet, "/api/v1/admin/audit-events?action=adminUpdateUser&result=success", nil)
	events := decode[AuditEventPage](t, rec)
	if len(events.Items) != 1 {
		t.Fatalf("events = %+v", events)
	}
	ev := events.Items[0]
	if ev.Via == nil || *ev.Via != AuditEventViaConsole || ev.Kind == nil || *ev.Kind != AuditEventKindMutate ||
		ev.RequestID == nil || ev.ResourceID == nil || *ev.ResourceID != bob.ID.String() || ev.Actor.Name == nil || *ev.Actor.Name != "admin" {
		t.Fatalf("event = %+v", ev)
	}
}

// A mutation refused before its service runs is still audited, with the
// request ID of the response.
func TestRefusedMutationsAreAudited(t *testing.T) {
	c := newM4Client(t, false)
	admin, me := c.register("admin")
	reader := c.token(admin, "read")
	rec := c.call(http.MethodPost, "/api/v1/tasks/"+uuid.NewString()+"/runs", nil, "Authorization", reader)
	c.want(rec, http.StatusForbidden, ErrorCodeInsufficientScope)
	events := decode[AuditEventPage](t, c.web(admin, http.MethodGet, "/api/v1/admin/audit-events?action=startRun", nil))
	if len(events.Items) != 1 {
		t.Fatalf("events = %+v", events)
	}
	ev := events.Items[0]
	if ev.Result != AuditEventResultDenied || ev.Actor.Kind != AuditActorKindToken || ev.Actor.ID == nil || *ev.Actor.ID != me.ID.String() ||
		ev.Via == nil || *ev.Via != AuditEventViaAPI || *ev.RequestID != rec.Header().Get(RequestIDHeader) || ev.ResourceKind != "task" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestProjectEndpoints(t *testing.T) {
	c := newM4Client(t, false)
	alice, _ := c.register("alice")
	bobCookie, bob := c.register("bob")
	carolCookie, _ := c.register("carol")

	rec := c.web(alice, http.MethodPost, "/api/v1/projects", map[string]any{"name": "数据组"})
	c.want(rec, http.StatusCreated, "")
	p := decode[Project](t, rec)
	if p.Role != ProjectRoleOwner || p.MemberCount != 1 || p.Links.Console != testOrigin+"/projects/"+p.ID.String() {
		t.Fatalf("project = %+v", p)
	}
	base := "/api/v1/projects/" + p.ID.String()
	c.want(c.web(bobCookie, http.MethodGet, base, nil), http.StatusNotFound, ErrorCodeNotFound)

	users := decode[UserRefPage](t, c.web(alice, http.MethodGet, "/api/v1/users?q=BO", nil))
	if len(users.Items) != 1 || users.Items[0].ID != bob.ID {
		t.Fatalf("search = %+v", users)
	}
	c.want(c.web(alice, http.MethodPut, base+"/members/"+bob.ID.String(), map[string]any{"role": "viewer"}), http.StatusOK, "")
	c.want(c.web(bobCookie, http.MethodPut, base+"/members/"+bob.ID.String(), map[string]any{"role": "owner"}),
		http.StatusForbidden, ErrorCodePermissionDenied)
	c.want(c.web(alice, http.MethodDelete, base+"/members/"+decode[Me](t, c.web(alice, http.MethodGet, "/api/v1/me", nil)).ID.String(), nil),
		http.StatusConflict, ErrorCodeLastOwner)
	members := decode[MemberPage](t, c.web(bobCookie, http.MethodGet, base+"/members", nil))
	if len(members.Items) != 2 {
		t.Fatalf("members = %+v", members)
	}
	list := decode[ProjectPage](t, c.web(bobCookie, http.MethodGet, "/api/v1/projects", nil))
	if len(list.Items) != 2 || !list.Items[0].IsPersonal || list.Items[1].Role != ProjectRoleViewer {
		t.Fatalf("bob's projects = %+v", list)
	}
	c.want(c.web(alice, http.MethodPatch, base, map[string]any{"name": "数据二组"}), http.StatusOK, "")
	c.want(c.web(bobCookie, http.MethodDelete, base+"/members/"+bob.ID.String(), nil), http.StatusNoContent, "")
	c.want(c.web(carolCookie, http.MethodGet, base+"/members", nil), http.StatusNotFound, ErrorCodeNotFound)
}

func TestProfileEndpoints(t *testing.T) {
	c := newM4Client(t, false)
	cookie, _ := c.register("alice")
	token := c.token(cookie, "read", "write")

	rec := c.web(cookie, http.MethodPatch, "/api/v1/me", map[string]any{"display_name": "爱丽丝", "locale": "zh-CN", "timezone": "Asia/Singapore"})
	c.want(rec, http.StatusOK, "")
	if me := decode[Me](t, rec); me.DisplayName == nil || *me.DisplayName != "爱丽丝" || me.Locale == nil || *me.Locale != MeLocaleZhCN {
		t.Fatalf("me = %+v", me)
	}
	c.want(c.web(cookie, http.MethodPatch, "/api/v1/me", map[string]any{"timezone": "Nowhere/City"}),
		http.StatusBadRequest, ErrorCodeValidationFailed)

	// Changing the password needs the session, not a token.
	body := map[string]any{"current_password": "correct horse", "new_password": "new password 1"}
	c.want(c.call(http.MethodPost, "/api/v1/me/password", body, "Authorization", token), http.StatusForbidden, ErrorCodeInsufficientScope)
	c.want(c.web(cookie, http.MethodPost, "/api/v1/me/password", map[string]any{"current_password": "nope", "new_password": "new password 1"}),
		http.StatusUnauthorized, ErrorCodeUnauthenticated)
	other := sessionCookie(t, c.call(http.MethodPost, "/api/v1/auth/login", map[string]any{"username": "alice", "password": "correct horse"}))
	c.want(c.web(cookie, http.MethodPost, "/api/v1/me/password", body), http.StatusNoContent, "")
	c.want(c.call(http.MethodGet, "/api/v1/me", nil, "Cookie", other), http.StatusUnauthorized, ErrorCodeUnauthenticated)
	c.want(c.call(http.MethodGet, "/api/v1/me", nil, "Cookie", cookie), http.StatusOK, "")
	c.want(c.call(http.MethodGet, "/api/v1/me", nil, "Authorization", token), http.StatusOK, "")

	// A device logs in with a password too and gets a read-only session.
	rec = c.call(http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "alice", "password": "new password 1", "device": map[string]any{"name": "Pixel", "platform": "android"},
	})
	c.want(rec, http.StatusOK, "")
	device := sessionCookie(t, rec)
	c.want(c.web(device, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "x", "scopes": []string{"read"}}),
		http.StatusForbidden, ErrorCodeInsufficientScope)
	devices := decode[DeviceList](t, c.web(cookie, http.MethodGet, "/api/v1/me/devices", nil))
	if len(devices.Items) != 1 || devices.Items[0].Current {
		t.Fatalf("devices = %+v", devices)
	}
	c.want(c.web(cookie, http.MethodDelete, "/api/v1/me/devices/"+devices.Items[0].ID.String(), nil), http.StatusNoContent, "")
	c.want(c.call(http.MethodGet, "/api/v1/me", nil, "Cookie", device), http.StatusUnauthorized, ErrorCodeUnauthenticated)
}
