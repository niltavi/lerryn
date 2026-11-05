package service_test

import (
	"errors"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Open log and event streams end once the credential that opened them
// stops being valid, although the user's role bindings remain.
func TestStreamsEndWhenCredentialEnds(t *testing.T) {
	service.SetCredentialRecheck(t, 0)
	for _, tc := range []struct {
		name string
		// open returns the principal that opens the streams; end makes
		// its credential invalid.
		open func(f *fixture, session *auth.Principal) *auth.Principal
		end  func(f *fixture, session, caller *auth.Principal)
	}{
		{
			name: "token revoked",
			open: func(f *fixture, session *auth.Principal) *auth.Principal { return f.tokenPrincipal(session) },
			end: func(f *fixture, session, caller *auth.Principal) {
				if err := f.accounts.RevokeToken(f.ctx, session, caller.CredentialID); err != nil {
					f.t.Fatal(err)
				}
			},
		},
		{
			name: "token user disabled",
			open: func(f *fixture, session *auth.Principal) *auth.Principal { return f.tokenPrincipal(session) },
			end:  func(f *fixture, _, caller *auth.Principal) { f.disable(caller) },
		},
		{
			name: "session logged out",
			open: func(_ *fixture, session *auth.Principal) *auth.Principal { return session },
			end: func(f *fixture, session, _ *auth.Principal) {
				if err := f.accounts.Logout(f.ctx, session); err != nil {
					f.t.Fatal(err)
				}
			},
		},
		{
			name: "session user disabled",
			open: func(_ *fixture, session *auth.Principal) *auth.Principal { return session },
			end:  func(f *fixture, _, caller *auth.Principal) { f.disable(caller) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			session := f.user("alice")
			task := f.apply(session, "digest", basicManifest())
			run := f.start(session, task.Task.ID)
			caller := tc.open(f, session)

			logs, err := f.runs.FollowLogs(f.ctx, caller, run.ID, service.FollowInput{})
			if err != nil {
				t.Fatal(err)
			}
			updates, err := f.runs.StreamUpdates(f.ctx, caller, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer updates.Close()

			// Both streams work while the credential is live.
			if _, err := logs.Next(f.ctx, 10*time.Millisecond); err != nil {
				t.Fatalf("log stream before: %v", err)
			}
			if _, _, err := updates.Next(f.ctx, 10*time.Millisecond); err != nil {
				t.Fatalf("event stream before: %v", err)
			}

			tc.end(f, session, caller)

			if _, err := logs.Next(f.ctx, time.Second); !errors.Is(err, service.ErrUnauthenticated) {
				t.Errorf("log stream after: err = %v, want unauthenticated", err)
			}
			if _, _, err := updates.Next(f.ctx, time.Second); !errors.Is(err, service.ErrUnauthenticated) {
				t.Errorf("event stream after: err = %v, want unauthenticated", err)
			}
		})
	}
}

// tokenPrincipal creates an access token for session and authenticates
// with it.
func (f *fixture) tokenPrincipal(session *auth.Principal) *auth.Principal {
	f.t.Helper()
	token, _, err := f.accounts.CreateToken(f.ctx, session, service.CreateTokenInput{Name: "agent", Scopes: []string{"read"}})
	if err != nil {
		f.t.Fatal(err)
	}
	return authenticate(f.t, f.accounts, token)
}

// disable marks the user disabled the way the admin service does, without
// ending its credentials, so only the user check can end a stream.
func (f *fixture) disable(p *auth.Principal) {
	f.t.Helper()
	if _, err := f.db.NewUpdate().Model((*store.User)(nil)).Set("disabled_at = ?", store.Now()).
		Where("id = ?", p.UserID).Exec(f.ctx); err != nil {
		f.t.Fatal(err)
	}
}
