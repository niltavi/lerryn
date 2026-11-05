package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

func TestRunnerAdministration(t *testing.T) {
	db := storetest.Open(t)
	blobs, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authz := service.NewAuthorizer(db)
	runs := service.NewRuns(db, authz, nil)
	coord := service.NewCoordinator(db, blobs, runs, nil)
	runners := service.NewRunners(db, coord, nil)
	c := &apiClient{t: t, h: New(Options{
		PublicURL:   testOrigin,
		Version:     "dev",
		Accounts:    service.NewAccounts(db, nil),
		Runs:        runs,
		RunnerAdmin: runners,
	})}

	// The first account is the admin.
	rec := c.call(http.MethodPost, "/api/v1/auth/register", map[string]any{"username": "admin", "password": "correct horse"})
	c.want(rec, http.StatusCreated, "")
	adminCookie := sessionCookie(t, rec)
	rec = c.call(http.MethodPost, "/api/v1/auth/register", map[string]any{"username": "bob", "password": "correct horse"})
	c.want(rec, http.StatusCreated, "")
	bobCookie := sessionCookie(t, rec)

	// A registration token, once, for the admin only.
	rec = c.call(http.MethodPost, "/api/v1/admin/runner-tokens", map[string]any{"name": "rack 1"}, "Cookie", bobCookie, "Origin", testOrigin)
	c.want(rec, http.StatusForbidden, ErrorCodePermissionDenied)
	rec = c.call(http.MethodPost, "/api/v1/admin/runner-tokens", map[string]any{"expires_in_hours": 2}, "Cookie", adminCookie, "Origin", testOrigin)
	c.want(rec, http.StatusCreated, "")
	tok := decode[RunnerToken](t, rec)
	if !strings.HasPrefix(tok.Token, service.RunnerRegistrationTokenPrefix) || tok.ExpiresAt.IsZero() {
		t.Fatalf("token = %+v", tok)
	}
	rec = c.call(http.MethodPost, "/api/v1/admin/runner-tokens", nil, "Cookie", adminCookie, "Origin", testOrigin)
	c.want(rec, http.StatusCreated, "")

	ctx := context.Background()
	if _, err := runners.Exchange(ctx, tok.Token, runproto.Registration{ID: "remote-1", Name: "host-1", Slots: 2, Labels: []string{"intranet"}, Version: "0.5.0"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := coord.Register(ctx, runproto.Registration{ID: "embedded", Slots: 4}); err != nil {
		t.Fatal(err)
	}

	rec = c.call(http.MethodGet, "/api/v1/admin/runners", nil, "Cookie", bobCookie)
	c.want(rec, http.StatusForbidden, ErrorCodePermissionDenied)
	rec = c.call(http.MethodGet, "/api/v1/admin/runners?limit=1", nil, "Cookie", adminCookie)
	c.want(rec, http.StatusOK, "")
	page := decode[RunnerPage](t, rec)
	if len(page.Items) != 1 || page.Items[0].ID != "embedded" || page.NextCursor == nil || page.Items[0].Version != nil ||
		page.Items[0].Labels == nil || page.Items[0].Status != RunnerStatusOnline {
		t.Fatalf("first page = %+v", page)
	}
	rec = c.call(http.MethodGet, "/api/v1/admin/runners?limit=1&cursor="+*page.NextCursor, nil, "Cookie", adminCookie)
	c.want(rec, http.StatusOK, "")
	page = decode[RunnerPage](t, rec)
	if len(page.Items) != 1 || page.NextCursor != nil {
		t.Fatalf("second page = %+v", page)
	}
	r := page.Items[0]
	if r.ID != "remote-1" || r.Name != "host-1" || r.Slots != 2 || len(r.Labels) != 1 || r.Labels[0] != "intranet" ||
		r.Version == nil || *r.Version != "0.5.0" || r.DisabledAt != nil || r.Running != 0 {
		t.Fatalf("remote runner = %+v", r)
	}

	rec = c.call(http.MethodPatch, "/api/v1/admin/runners/remote-1", map[string]any{"disabled": true}, "Cookie", adminCookie, "Origin", testOrigin)
	c.want(rec, http.StatusOK, "")
	if got := decode[Runner](t, rec); got.Status != RunnerStatusDisabled || got.DisabledAt == nil {
		t.Fatalf("disabled runner = %+v", got)
	}
	rec = c.call(http.MethodGet, "/api/v1/admin/runners?status=disabled", nil, "Cookie", adminCookie)
	c.want(rec, http.StatusOK, "")
	if page := decode[RunnerPage](t, rec); len(page.Items) != 1 || page.Items[0].ID != "remote-1" {
		t.Fatalf("disabled runners = %+v", page)
	}
	rec = c.call(http.MethodPatch, "/api/v1/admin/runners/missing", map[string]any{"disabled": true}, "Cookie", adminCookie, "Origin", testOrigin)
	c.want(rec, http.StatusNotFound, ErrorCodeNotFound)
}
