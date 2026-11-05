package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword(t.Context(), "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash = %q", hash)
	}
	for _, tt := range []struct {
		password string
		want     bool
	}{
		{"correct horse", true},
		{"correct horsE", false},
		{"", false},
	} {
		ok, err := VerifyPassword(t.Context(), hash, tt.password)
		if err != nil || ok != tt.want {
			t.Errorf("VerifyPassword(%q) = %v, %v; want %v", tt.password, ok, err, tt.want)
		}
	}
	other, err := HashPassword(t.Context(), "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if other == hash {
		t.Error("two hashes of one password must differ by salt")
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	for _, h := range []string{
		"",
		"plain",
		"$argon2i$v=19$m=19456,t=2,p=1$c2FsdA$a2V5",
		"$argon2id$v=18$m=19456,t=2,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=99999999,t=2,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=19456,t=0,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=19456,t=2,p=1$!!$a2V5",
		"$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$",
	} {
		if _, err := VerifyPassword(t.Context(), h, "x"); err == nil {
			t.Errorf("VerifyPassword(%q) accepted a malformed hash", h)
		}
	}
}

func TestNewToken(t *testing.T) {
	token, hash, err := NewToken(AccessTokenPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, AccessTokenPrefix) || len(token) != len(AccessTokenPrefix)+52 {
		t.Fatalf("token = %q", token)
	}
	if token != strings.ToLower(token) {
		t.Error("tokens are lower case so they survive case-insensitive copying")
	}
	if hash != HashToken(token) || len(hash) != 64 {
		t.Fatalf("hash = %q", hash)
	}
	if p := DisplayPrefix(token); p != token[:len(AccessTokenPrefix)+4] {
		t.Errorf("DisplayPrefix = %q", p)
	}
	if DisplayPrefix("ghp_abcdef") != "" {
		t.Error("foreign tokens have no display prefix")
	}
	again, _, err := NewToken(AccessTokenPrefix)
	if err != nil || again == token {
		t.Fatal("tokens must be random")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(3, time.Minute)
	l.now = func() time.Time { return now }

	for range 3 {
		if l.Blocked("alice") {
			t.Fatal("blocked before the limit")
		}
		l.Record("alice")
	}
	if !l.Blocked("alice") {
		t.Fatal("not blocked at the limit")
	}
	if l.Blocked("bob") {
		t.Fatal("keys must be independent")
	}
	now = now.Add(time.Minute)
	if l.Blocked("alice") {
		t.Fatal("still blocked after the window")
	}
}

func TestLimiterSweepsExpiredKeys(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(1, time.Minute)
	l.now = func() time.Time { return now }
	for i := range maxKeys {
		l.Record(uuid.NewString() + string(rune(i)))
	}
	now = now.Add(2 * time.Minute)
	l.Record("fresh")
	if n := len(l.windows); n != 1 {
		t.Fatalf("%d windows after sweep, want 1", n)
	}
}

func TestPrincipalContext(t *testing.T) {
	ctx := context.Background()
	if PrincipalFrom(ctx) != nil {
		t.Fatal("anonymous context must have no principal")
	}
	p := &Principal{Username: "alice", Scopes: []Scope{ScopeRead}}
	got := PrincipalFrom(WithPrincipal(ctx, p))
	if got != p || !got.HasScope(ScopeRead) || got.HasScope(ScopeWrite) {
		t.Fatalf("got %+v", got)
	}
	if ClientFrom(WithClient(ctx, Client{IP: "192.0.2.1"})).IP != "192.0.2.1" {
		t.Fatal("client not carried")
	}
	if !ValidScope("execute") || ValidScope("admin") {
		t.Fatal("ValidScope")
	}
}
