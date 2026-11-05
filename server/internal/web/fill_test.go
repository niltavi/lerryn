package web_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/secrets"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
	"github.com/niltavi/lerryn/server/internal/web"
)

const (
	secretValue = "sk-fill-page-0123456789" // #nosec G101 -- a test value
	password    = "correct horse"
)

type env struct {
	t        *testing.T
	ctx      context.Context
	db       *store.DB
	srv      *httptest.Server
	accounts *service.Accounts
	creds    *service.Credentials
	tasks    *service.Tasks
}

// extraOrigin is another address the console is opened at, such as a
// tunnel's domain.
const extraOrigin = "https://lerryn.example.com"

func newEnv(t *testing.T) *env {
	t.Helper()
	db := storetest.Open(t)
	blobs, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, _ := secrets.GenerateKey()
	kr, _ := secrets.NewLocalKeyring(key)
	authz := service.NewAuthorizer(db)
	accounts := service.NewAccounts(db, nil)
	creds := service.NewCredentials(db, authz, kr, nil)
	e := &env{t: t, ctx: context.Background(), db: db, accounts: accounts, creds: creds, tasks: service.NewTasks(db, authz, blobs, nil)}
	mux := http.NewServeMux()
	e.srv = httptest.NewUnstartedServer(mux)
	e.srv.Start()
	t.Cleanup(e.srv.Close)
	fill, err := web.NewFill(web.FillOptions{
		PublicURL: e.srv.URL, ExtraOrigins: []string{extraOrigin},
		Credentials: creds, Accounts: accounts, CSRFKey: kr.DeriveKey("fill-csrf"),
	})
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/c/", fill)
	return e
}

// user registers name and returns its session principal.
func (e *env) user(name string) *auth.Principal {
	e.t.Helper()
	_, sess, err := e.accounts.Register(e.ctx, service.RegisterInput{Username: name, Password: password})
	if err != nil {
		e.t.Fatal(err)
	}
	p, err := e.accounts.Authenticate(e.ctx, sess.Token)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

// request creates a fill request for p's new task.
func (e *env) request(p *auth.Principal, fields ...store.CredentialField) uuid.UUID {
	e.t.Helper()
	task, err := e.tasks.Apply(e.ctx, p, service.ApplyTaskInput{
		ProjectID: p.PersonalProjectID, Name: "price-watch-" + strings.ToLower(uuid.NewString()[:6]),
		Manifest: service.Manifest{Runtime: service.RuntimeSpec{Language: "python", Version: "3.12"}, Entrypoint: []string{"python", "main.py"}},
		Files:    []service.FileInput{{Path: "main.py", Content: []byte("print(1)\n")}},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	if len(fields) == 0 {
		fields = []store.CredentialField{{Name: "PRICE_API_TOKEN", Secret: true, Description: "读取竞品价格接口"}}
	}
	d, err := e.creds.CreateRequest(e.ctx, p, service.CreateRequestInput{TaskID: task.Task.Task.ID, Name: "PRICE_API_TOKEN", Purpose: "读取竞品价格", Fields: fields})
	if err != nil {
		e.t.Fatal(err)
	}
	return d.Request.ID
}

// browser is a cookie-keeping client that does not follow redirects.
type browser struct {
	t      *testing.T
	e      *env
	client *http.Client
}

func (e *env) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: e.t, e: e, client: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

type page struct {
	status int
	header http.Header
	body   string
}

func (b *browser) do(method, path string, form url.Values, origin string) page {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, b.e.srv.URL+path, body)
	if err != nil {
		b.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return page{status: resp.StatusCode, header: resp.Header, body: string(data)}
}

func (b *browser) get(path string) page { return b.do(http.MethodGet, path, nil, "") }

func (b *browser) post(path string, form url.Values) page {
	return b.do(http.MethodPost, path, form, b.e.srv.URL)
}

var csrfPattern = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func csrf(t *testing.T, p page) string {
	t.Helper()
	m := csrfPattern.FindStringSubmatch(p.body)
	if m == nil {
		t.Fatalf("no CSRF token in page: %s", p.body)
	}
	return m[1]
}

func (b *browser) login(id uuid.UUID, username string) {
	b.t.Helper()
	p := b.get("/c/" + id.String())
	if p.status != http.StatusOK || !strings.Contains(p.body, `name="password"`) {
		b.t.Fatalf("login page = %d: %s", p.status, p.body)
	}
	r := b.post("/c/"+id.String()+"/login", url.Values{"csrf": {csrf(b.t, p)}, "username": {username}, "password": {password}})
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/c/"+id.String() {
		b.t.Fatalf("login = %d %v: %s", r.status, r.header, r.body)
	}
}

func TestFillPageFlow(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	id := e.request(alice)
	b := e.browser()

	b.login(id, "alice")
	form := b.get("/c/" + id.String())
	if form.status != http.StatusOK || !strings.Contains(form.body, "PRICE_API_TOKEN") || !strings.Contains(form.body, "需要凭证") ||
		!strings.Contains(form.body, "读取竞品价格接口") || !strings.Contains(form.body, `type="password"`) {
		t.Fatalf("form = %d: %s", form.status, form.body)
	}
	checkHeaders(t, form)

	saved := b.post("/c/"+id.String(), url.Values{"csrf": {csrf(t, form)}, "PRICE_API_TOKEN": {secretValue}})
	if saved.status != http.StatusSeeOther || saved.header.Get("Location") != "/c/"+id.String()+"?saved=1" || strings.Contains(saved.body, secretValue) {
		t.Fatalf("submit = %d %v: %s", saved.status, saved.header, saved.body)
	}
	done := b.get("/c/" + id.String() + "?saved=1")
	d, _ := e.creds.GetRequest(e.ctx, alice, id)
	if !strings.Contains(done.body, "已保存") || !strings.Contains(done.body, d.Request.CredentialID.String()) || strings.Contains(done.body, secretValue) {
		t.Fatalf("saved page: %s", done.body)
	}
	// The saved page offers a way out: the user's credentials and the console.
	if !strings.Contains(done.body, `href="`+e.srv.URL+`/settings/credentials"`) || !strings.Contains(done.body, `href="`+e.srv.URL+`/"`) {
		t.Fatalf("saved page has no way back: %s", done.body)
	}
	// Revisiting, or submitting again, shows that it was submitted.
	if again := b.get("/c/" + id.String()); !strings.Contains(again.body, "已提交过") {
		t.Fatalf("revisit: %s", again.body)
	}
	resubmit := b.post("/c/"+id.String(), url.Values{"csrf": {csrf(t, form)}, "PRICE_API_TOKEN": {"other"}})
	if resubmit.status != http.StatusConflict || !strings.Contains(resubmit.body, "已提交过") {
		t.Fatalf("resubmit = %d: %s", resubmit.status, resubmit.body)
	}
}

func checkHeaders(t *testing.T, p page) {
	t.Helper()
	theme, err := os.ReadFile("theme.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.body, string(theme)) {
		t.Error("page does not render the embedded shared theme")
	}
	for k, want := range map[string]string{
		"Cache-Control":          "no-store",
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "same-origin",
	} {
		if got := p.header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	csp := p.header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "frame-ancestors 'none'", "form-action 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP allows inline code: %q", csp)
	}
}

// Submissions need the form's token, bound to this session and request,
// and must come from the deployment's origin.
func TestFillPageCSRF(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	id := e.request(alice)
	otherID := e.request(alice)
	b := e.browser()
	b.login(id, "alice")
	token := csrf(t, b.get("/c/"+id.String()))
	otherToken := csrf(t, b.get("/c/"+otherID.String()))

	// Another session of the same user gets its own tokens.
	b2 := e.browser()
	b2.login(id, "alice")
	foreignToken := csrf(t, b2.get("/c/"+id.String()))

	values := func(tok string) url.Values {
		return url.Values{"csrf": {tok}, "PRICE_API_TOKEN": {secretValue}}
	}
	tests := []struct {
		name   string
		form   url.Values
		origin string
	}{
		{"no token", url.Values{"PRICE_API_TOKEN": {secretValue}}, e.srv.URL},
		{"wrong token", values("forged"), e.srv.URL},
		{"token of another request", values(otherToken), e.srv.URL},
		{"token of another session", values(foreignToken), e.srv.URL},
		{"cross-site origin", values(token), "https://attacker.example"},
		{"no origin", values(token), ""},
		{"null origin", values(token), "null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := b.do(http.MethodPost, "/c/"+id.String(), tt.form, tt.origin)
			if p.status != http.StatusForbidden || strings.Contains(p.body, secretValue) {
				t.Fatalf("status %d: %s", p.status, p.body)
			}
		})
	}
	if d, _ := e.creds.GetRequest(e.ctx, alice, id); d.Request.Status != store.CredentialRequestPending {
		t.Fatalf("request status = %q after forged submissions", d.Request.Status)
	}

	// Login is protected the same way: no forged sign-in into another
	// account.
	anon := e.browser()
	login := anon.get("/c/" + id.String())
	for name, form := range map[string]url.Values{
		"no token":    {"username": {"alice"}, "password": {password}},
		"wrong token": {"csrf": {"x"}, "username": {"alice"}, "password": {password}},
	} {
		if p := anon.post("/c/"+id.String()+"/login", form); p.status != http.StatusForbidden {
			t.Errorf("login with %s = %d", name, p.status)
		}
	}
	if p := anon.do(http.MethodPost, "/c/"+id.String()+"/login",
		url.Values{"csrf": {csrf(t, login)}, "username": {"alice"}, "password": {password}}, "https://attacker.example"); p.status != http.StatusForbidden {
		t.Errorf("cross-site login = %d", p.status)
	}
	if p := anon.do(http.MethodPost, "/c/"+id.String()+"/login",
		url.Values{"csrf": {csrf(t, login)}, "username": {"alice"}, "password": {password}}, "http://lerryn.example.com"); p.status != http.StatusForbidden {
		t.Errorf("login from the extra origin's host over another scheme = %d", p.status)
	}
	// The extra origin is the deployment's own, so signing in from it works.
	if p := anon.do(http.MethodPost, "/c/"+id.String()+"/login",
		url.Values{"csrf": {csrf(t, login)}, "username": {"alice"}, "password": {password}}, extraOrigin); p.status != http.StatusSeeOther {
		t.Errorf("login from the extra origin = %d: %s", p.status, p.body)
	}
	// A login token is bound to the browser's login cookie.
	fresh := e.browser()
	if p := fresh.post("/c/"+id.String()+"/login", url.Values{"csrf": {csrf(t, login)}, "username": {"alice"}, "password": {password}}); p.status != http.StatusForbidden {
		t.Errorf("login token from another browser = %d", p.status)
	}
}

// A failed submission shows what is wrong without echoing anything typed,
// not even the fields that were valid.
func TestFillPageDoesNotEcho(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	id := e.request(alice,
		store.CredentialField{Name: "API_KEY", Secret: true},
		store.CredentialField{Name: "REGION", Secret: false},
		store.CredentialField{Name: "CERT", Secret: true, Multiline: true, Optional: true},
	)
	b := e.browser()
	b.login(id, "alice")
	form := b.get("/c/" + id.String())
	if !strings.Contains(form.body, "<textarea") || !strings.Contains(form.body, `type="text"`) || !strings.Contains(form.body, "可选") {
		t.Fatalf("form: %s", form.body)
	}
	p := b.post("/c/"+id.String(), url.Values{"csrf": {csrf(t, form)}, "API_KEY": {secretValue}, "REGION": {""}, "CERT": {"cert-body-value"}})
	if p.status != http.StatusUnprocessableEntity || !strings.Contains(p.body, "请填写这一项") {
		t.Fatalf("submit = %d: %s", p.status, p.body)
	}
	if strings.Contains(p.body, secretValue) || strings.Contains(p.body, "cert-body-value") {
		t.Fatal("the page echoes submitted values")
	}
	ok := b.post("/c/"+id.String(), url.Values{"csrf": {csrf(t, p)}, "API_KEY": {secretValue}, "REGION": {"cn-north"}, "CERT": {"a\r\nb\r\n"}})
	if ok.status != http.StatusSeeOther {
		t.Fatalf("second submit = %d: %s", ok.status, ok.body)
	}
}

func TestFillPageOtherUserAndSwitch(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	e.user("bob")
	id := e.request(alice)
	b := e.browser()
	b.login(id, "bob")
	p := b.get("/c/" + id.String())
	if p.status != http.StatusNotFound || !strings.Contains(p.body, "不属于当前账号") || strings.Contains(p.body, "PRICE_API_TOKEN") {
		t.Fatalf("other user's page = %d: %s", p.status, p.body)
	}
	// Bob cannot submit either, even with a token for his session.
	if r := b.post("/c/"+id.String(), url.Values{"csrf": {csrf(t, p)}, "PRICE_API_TOKEN": {secretValue}}); r.status != http.StatusForbidden {
		t.Fatalf("bob submits = %d", r.status)
	}
	sw := b.post("/c/"+id.String()+"/switch", url.Values{"csrf": {csrf(t, p)}})
	if sw.status != http.StatusSeeOther {
		t.Fatalf("switch = %d: %s", sw.status, sw.body)
	}
	if after := b.get("/c/" + id.String()); !strings.Contains(after.body, `name="password"`) {
		t.Fatalf("after switch: %s", after.body)
	}
	b.login(id, "alice")
	if form := b.get("/c/" + id.String()); !strings.Contains(form.body, "PRICE_API_TOKEN") {
		t.Fatalf("alice's form: %s", form.body)
	}
}

func TestFillPageStates(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	expired := e.request(alice)
	cancelled := e.request(alice)
	if _, err := e.db.NewUpdate().Model((*store.CredentialRequest)(nil)).Set("expires_at = ?", store.Now().Add(-time.Hour)).
		Where("id = ?", expired).Exec(e.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.creds.CancelRequest(e.ctx, alice, cancelled); err != nil {
		t.Fatal(err)
	}
	b := e.browser()
	b.login(expired, "alice")
	if p := b.get("/c/" + expired.String()); !strings.Contains(p.body, "链接已过期") || strings.Contains(p.body, `name="PRICE_API_TOKEN"`) {
		t.Fatalf("expired: %s", p.body)
	}
	if p := b.get("/c/" + cancelled.String()); !strings.Contains(p.body, "请求已取消") {
		t.Fatalf("cancelled: %s", p.body)
	}
	if p := b.get("/c/not-a-uuid"); p.status != http.StatusNotFound || !strings.Contains(p.body, "链接不可用") {
		t.Fatalf("bad id = %d", p.status)
	}
	if p := b.get("/c/" + uuid.NewString()); p.status != http.StatusNotFound {
		t.Fatalf("unknown id = %d", p.status)
	}
	if p := b.do(http.MethodPut, "/c/"+expired.String(), url.Values{}, e.srv.URL); p.status != http.StatusMethodNotAllowed {
		t.Fatalf("PUT = %d", p.status)
	}
}

// An agent's access token does not open the page: only a browser session
// does.
func TestFillPageIgnoresAccessTokens(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	id := e.request(alice)
	token, _, err := e.accounts.CreateToken(e.ctx, alice, service.CreateTokenInput{Name: "agent", Scopes: []string{"read", "write"}})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/c/"+id.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(data), `name="password"`) || strings.Contains(string(data), "PRICE_API_TOKEN") {
		t.Fatalf("token request got: %s", data)
	}
	// A token in the session cookie is not a session either.
	b := e.browser()
	u, _ := url.Parse(e.srv.URL)
	b.client.Jar.SetCookies(u, []*http.Cookie{{Name: web.SessionCookieName, Value: token}}) // #nosec G124 -- a client-side test cookie
	if p := b.get("/c/" + id.String()); !strings.Contains(p.body, `name="password"`) {
		t.Fatalf("token cookie got: %s", p.body)
	}
}

func TestFillPageLoginErrors(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	id := e.request(alice)
	b := e.browser()
	p := b.get("/c/" + id.String())
	r := b.post("/c/"+id.String()+"/login", url.Values{"csrf": {csrf(t, p)}, "username": {"alice"}, "password": {"wrong password"}})
	if r.status != http.StatusUnauthorized || !strings.Contains(r.body, "用户名或密码不正确") || strings.Contains(r.body, "wrong password") {
		t.Fatalf("bad password = %d: %s", r.status, r.body)
	}
}
