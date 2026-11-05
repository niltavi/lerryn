package service_test

import (
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// quietRunner makes a runner look as if it stopped heartbeating more than
// a lease ago.
func (f *fixture) quietRunner(id string) {
	f.t.Helper()
	if _, err := f.db.NewUpdate().Model((*store.Runner)(nil)).
		Set("last_seen_at = ?", store.Now().Add(-2*runproto.LeaseTTL)).
		Where("id = ?", id).Exec(f.ctx); err != nil {
		f.t.Fatal(err)
	}
}

func exchangeStatic(f *fixture, runners *service.Runners) string {
	f.t.Helper()
	cred, err := runners.Exchange(f.ctx, "static", runproto.Registration{ID: "r", Slots: 1}, service.ProtocolVersion)
	if err != nil {
		f.t.Fatal(err)
	}
	return cred
}

// The static registration token is shared and runner IDs are visible to
// users, so the token must not let anyone take over a runner in use.
func TestExchangeRefusesRunnerTakeover(t *testing.T) {
	cases := []struct {
		name string
		// setup prepares the runner with ID "r" and returns its
		// credential, if it has one.
		setup func(f *fixture, runners *service.Runners) string
		want  error
	}{
		{"new ID", func(*fixture, *service.Runners) string { return "" }, nil},
		{"embedded runner", func(f *fixture, _ *service.Runners) string {
			f.runner("r")
			return ""
		}, service.ErrForbidden},
		{"quiet embedded runner", func(f *fixture, _ *service.Runners) string {
			f.runner("r")
			f.quietRunner("r")
			return ""
		}, service.ErrForbidden},
		{"live remote runner", exchangeStatic, service.ErrRunnerIDInUse},
		{"quiet remote runner with a run assigned", func(f *fixture, runners *service.Runners) string {
			cred := exchangeStatic(f, runners)
			admin := f.user("admin")
			f.start(admin, f.apply(admin, "t", basicManifest()).Task.ID)
			if got := f.claim("r", 1); len(got) != 1 {
				f.t.Fatalf("claimed %d runs", len(got))
			}
			f.quietRunner("r")
			return cred
		}, service.ErrRunnerIDInUse},
		{"quiet remote runner without runs", func(f *fixture, runners *service.Runners) string {
			cred := exchangeStatic(f, runners)
			f.quietRunner("r")
			return cred
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			runners := f.runnerService().WithStaticRegistrationToken("static")
			old := tc.setup(f, runners)
			cred, err := runners.Exchange(f.ctx, "static", runproto.Registration{ID: "r", Slots: 1}, service.ProtocolVersion)
			if tc.want != nil {
				wantErr(t, err, tc.want)
				if old != "" {
					if id, err := runners.Authenticate(f.ctx, old); err != nil || id != "r" {
						t.Fatalf("the old credential stopped working: %q, %v", id, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if id, err := runners.Authenticate(f.ctx, cred); err != nil || id != "r" {
				t.Fatalf("authenticate new credential = %q, %v", id, err)
			}
			if old != "" {
				_, err := runners.Authenticate(f.ctx, old)
				wantErr(t, err, service.ErrUnauthenticated)
			}
		})
	}
}

// A runner restarting with its stored credential updates its details
// without a token exchange, while it is live.
func TestRunnerRestartWithCredential(t *testing.T) {
	f := newFixture(t)
	runners := f.runnerService().WithStaticRegistrationToken("static")
	cred := exchangeStatic(f, runners)
	id, err := runners.Authenticate(f.ctx, cred)
	if err != nil {
		t.Fatal(err)
	}
	if err := runners.Update(f.ctx, runproto.Registration{ID: id, Slots: 3, Labels: []string{"gpu"}}, service.ProtocolVersion); err != nil {
		t.Fatal(err)
	}
	if got, err := runners.Authenticate(f.ctx, cred); err != nil || got != "r" {
		t.Fatalf("authenticate after restart = %q, %v", got, err)
	}
}

// A refused exchange does not use up an admin's one-time token.
func TestExchangeRefusalKeepsAdminToken(t *testing.T) {
	f := newFixture(t)
	admin := f.user("admin")
	runners := f.runnerService()
	f.runner("embedded")
	token, _, err := runners.CreateRegistrationToken(f.ctx, admin, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runners.Exchange(f.ctx, token, runproto.Registration{ID: "embedded", Slots: 1}, service.ProtocolVersion)
	wantErr(t, err, service.ErrForbidden)
	if _, err := runners.Exchange(f.ctx, token, runproto.Registration{ID: "other", Slots: 1}, service.ProtocolVersion); err != nil {
		t.Fatalf("token after a refused exchange: %v", err)
	}
}
