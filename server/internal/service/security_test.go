package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Parallel guesses used to pass the rate limit together, because each one
// was recorded only after its password check had failed.
func TestLoginParallelAttemptsCannotExceedLimit(t *testing.T) {
	a, _ := newAccounts(t)
	register(t, a, "alice")
	ctx := auth.WithClient(context.Background(), auth.Client{IP: "192.0.2.50"})

	var checked, limited atomic.Int32
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			_, _, err := a.Login(ctx, "alice", "wrong password")
			switch {
			case errors.Is(err, service.ErrUnauthenticated):
				checked.Add(1)
			case errors.Is(err, service.ErrRateLimited):
				limited.Add(1)
			default:
				t.Errorf("login: %v", err)
			}
		})
	}
	wg.Wait()
	if n := checked.Load(); n != 10 {
		t.Fatalf("%d of 40 parallel guesses had their password checked, want 10", n)
	}
	if _, _, err := a.Login(ctx, "alice", "correct horse"); !errors.Is(err, service.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

// Counting attempts up front must not turn successful logins into
// failures: they are taken back once the password proves right.
func TestSuccessfulLoginsDoNotCount(t *testing.T) {
	a, _ := newAccounts(t)
	register(t, a, "alice")
	ctx := auth.WithClient(context.Background(), auth.Client{IP: "192.0.2.51"})
	for i := range 15 {
		if _, _, err := a.Login(ctx, "alice", "correct horse"); err != nil {
			t.Fatalf("login %d: %v", i, err)
		}
	}
	for range 9 {
		if _, _, err := a.Login(ctx, "alice", "wrong password"); !errors.Is(err, service.ErrUnauthenticated) {
			t.Fatalf("err = %v", err)
		}
	}
	if _, _, err := a.Login(ctx, "alice", "correct horse"); err != nil {
		t.Fatalf("login after 9 failures: %v", err)
	}
}

func TestRegisterRateLimit(t *testing.T) {
	a, _ := newAccounts(t)
	ctx := auth.WithClient(context.Background(), auth.Client{IP: "192.0.2.60"})
	in := func(name string) service.RegisterInput {
		return service.RegisterInput{Username: name, Password: "correct horse"}
	}
	for i := range 5 {
		if _, _, err := a.Register(ctx, in(fmt.Sprintf("user%d", i))); err != nil {
			t.Fatalf("registration %d: %v", i, err)
		}
	}
	if _, _, err := a.Register(ctx, in("user5")); !errors.Is(err, service.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited after 5 registrations", err)
	}
	other := auth.WithClient(context.Background(), auth.Client{IP: "192.0.2.61"})
	if _, _, err := a.Register(other, in("user5")); err != nil {
		t.Fatalf("another address: %v", err)
	}
}

func TestConcurrentFirstAccountsMakeOneAdmin(t *testing.T) {
	e := newAccountsEnv(t)
	const n = 8
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			var err error
			if i == 0 {
				_, err = e.admin.BootstrapAdmin(e.ctx, service.RegisterInput{Username: "initial", Password: "correct horse"})
				if errors.Is(err, service.ErrAccountsExist) {
					err = nil
				}
			} else {
				_, _, err = e.accounts.Register(e.ctx, service.RegisterInput{Username: fmt.Sprintf("racer%d", i), Password: "correct horse"})
			}
			if err != nil {
				t.Errorf("account %d: %v", i, err)
			}
		})
	}
	wg.Wait()
	admins, err := e.db.NewSelect().Model((*store.User)(nil)).Where("is_admin = ?", true).Count(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if admins != 1 {
		t.Fatalf("%d admins after concurrent first registrations, want 1", admins)
	}
}

// A pairing code issued before the password changed would let whoever
// holds it in afterwards.
func TestPasswordResetDropsPairingCodes(t *testing.T) {
	tests := []struct {
		name  string
		reset func(e *accountsEnv, admin, alice *auth.Principal) error
	}{
		{"admin reset", func(e *accountsEnv, admin, alice *auth.Principal) error {
			return e.admin.ResetPassword(e.ctx, admin, alice.UserID, "brand new pass")
		}},
		{"local reset", func(e *accountsEnv, _, _ *auth.Principal) error {
			_, err := e.admin.ResetPasswordLocally(e.ctx, "alice", "brand new pass")
			return err
		}},
		{"change", func(e *accountsEnv, _, alice *auth.Principal) error {
			return e.accounts.ChangePassword(e.ctx, alice, "correct horse", "brand new pass")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAccountsEnv(t)
			admin, _ := e.user("root")
			alice, _ := e.user("alice")
			code := pairingCode(t, e, alice)
			if err := tt.reset(e, admin, alice); err != nil {
				t.Fatal(err)
			}
			_, _, err := e.accounts.ExchangePairingCode(e.ctx, publicURL, code.Code, iphone)
			wantErr(t, err, service.ErrUnauthenticated)
		})
	}
}
