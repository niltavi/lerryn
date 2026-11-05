package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

// Requests from httptest come from 192.0.2.1, which plays the reverse
// proxy here.
func TestLoginRateLimitBehindTrustedProxy(t *testing.T) {
	tests := []struct {
		name    string
		trusted []string
		// wantOther is the status of a correct login from a second client
		// after the first one used up its failed attempts.
		wantOther int
	}{
		{"trusted proxy keeps clients apart", []string{"192.0.2.0/24"}, http.StatusOK},
		{"untrusted peer cannot pick its address", nil, http.StatusTooManyRequests},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxies, err := auth.ParseTrustedProxies(tt.trusted)
			if err != nil {
				t.Fatal(err)
			}
			accounts := service.NewAccounts(storetest.Open(t), nil)
			c := &apiClient{t: t, h: New(Options{PublicURL: testOrigin, Version: "dev", Accounts: accounts, TrustedProxies: proxies})}
			c.want(c.call(http.MethodPost, "/api/v1/auth/register", map[string]any{"username": "alice", "password": "correct horse"},
				"X-Forwarded-For", "198.51.100.9"), http.StatusCreated, "")
			// The guesser cycles usernames, so only its address limit
			// fills up, and varies a forged left-most entry.
			for i := range 10 {
				c.call(http.MethodPost, "/api/v1/auth/login", map[string]any{"username": fmt.Sprintf("nobody%d", i), "password": "nope nope"},
					"X-Forwarded-For", fmt.Sprintf("10.0.0.%d, 198.51.100.1", i))
			}
			c.want(c.call(http.MethodPost, "/api/v1/auth/login", map[string]any{"username": "alice", "password": "correct horse"},
				"X-Forwarded-For", "198.51.100.1"), http.StatusTooManyRequests, ErrorCodeRateLimited)
			rec := c.call(http.MethodPost, "/api/v1/auth/login", map[string]any{"username": "alice", "password": "correct horse"},
				"X-Forwarded-For", "198.51.100.2")
			if rec.Code != tt.wantOther {
				t.Fatalf("login from another client = %d, want %d: %s", rec.Code, tt.wantOther, rec.Body)
			}
		})
	}
}
