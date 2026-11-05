package web_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/web"
)

// passwordLoginOff answers every login as when an admin has turned off
// password login for ordinary users. Turning it off for real needs single
// sign-on configured, which this test does not need.
type passwordLoginOff struct{ web.Accounts }

func (passwordLoginOff) Login(context.Context, string, string) (service.Account, service.NewSession, error) {
	return service.Account{}, service.NewSession{}, service.ErrPasswordLoginDisabled
}

func TestFillPageLoginPasswordLoginDisabled(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	id := e.request(alice)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	fill, err := web.NewFill(web.FillOptions{
		PublicURL: srv.URL, Credentials: e.creds, Accounts: passwordLoginOff{e.accounts},
		CSRFKey: bytes.Repeat([]byte{7}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/c/", fill)
	e.srv = srv

	b := e.browser()
	p := b.get("/c/" + id.String())
	r := b.post("/c/"+id.String()+"/login", url.Values{"csrf": {csrf(t, p)}, "username": {"alice"}, "password": {password}})
	if r.status != http.StatusForbidden || !strings.Contains(r.body, "管理员已关闭普通用户的密码登录") {
		t.Fatalf("login with password login off = %d: %s", r.status, r.body)
	}
	if strings.Contains(r.body, password) {
		t.Fatal("the page echoes the password")
	}
}
