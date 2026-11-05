package httpapi

import (
	"context"
	"crypto/rand"
	"net/http"
)

// RequestIDHeader carries the request ID on every API response. Problem
// bodies repeat it in request_id.
const RequestIDHeader = "X-Request-Id"

type requestIDKey struct{}

// RequestIDFromContext returns the ID withRequestID assigned to the request,
// or "" outside an API request.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// withRequestID assigns every request a fresh ID. An incoming X-Request-Id
// is ignored: clients could otherwise choose the IDs that end up in logs
// and audit records.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// newRequestID returns "req_" followed by 26 base32 characters carrying at
// least 128 random bits.
func newRequestID() string {
	return "req_" + rand.Text()
}
