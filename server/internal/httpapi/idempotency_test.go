package httpapi

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"

	"github.com/niltavi/lerryn/api"
)

// The operations that keep their responses in idempotency_records for 24
// hours are read from the contract, so adding the Idempotency-Key header
// to an operation in api/ is enough to enrol it. This pins the list: an
// operation whose response carries a plaintext secret (a new access token,
// a runner token, a credential value) must never be added, because the
// secret would then be stored in the database and replayed for a day.
func TestIdempotentOperationsArePinned(t *testing.T) {
	want := []string{"applyTask", "createCredentialRequest", "rerunRun", "startRun", "triggerWebhook"}
	got := slices.Sorted(maps.Keys(idempotentOperations))
	if !slices.Equal(got, want) {
		t.Fatalf("idempotent operations = %v, want %v", got, want)
	}
	fromSpec, err := operationsWithHeader(api.Spec, IdempotencyKeyHeader)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(fromSpec, idempotentOperations) {
		t.Fatalf("spec = %v, table = %v", fromSpec, idempotentOperations)
	}
}

// Access rules apply before the key is looked at: a token without the
// operation's scope neither replays a stored response nor takes a key.
func TestIdempotencyChecksAccessFirst(t *testing.T) {
	c := newM2Client(t)
	rec := c.call(http.MethodPost, "/api/v1/auth/register", map[string]any{"username": "alice", "password": "correct horse"})
	c.want(rec, http.StatusCreated, "")
	project := decode[Me](t, rec).PersonalProjectID
	cookie := sessionCookie(t, rec)
	token := func(scopes ...string) string {
		rec := c.call(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "agent", "scopes": scopes},
			"Cookie", cookie, "Origin", testOrigin)
		c.want(rec, http.StatusCreated, "")
		return "Bearer " + decode[CreatedToken](t, rec).Token
	}
	writer, reader := token("read", "write"), token("read")
	// Each case applies a new task, so no version precondition applies.
	path := func(i int) string { return "/api/v1/projects/" + project.String() + "/tasks/digest-" + strconv.Itoa(i) }
	apply := func(i int, tok, key string) *httptest.ResponseRecorder {
		return c.call(http.MethodPut, path(i), digestRequest(), "Authorization", tok, "Idempotency-Key", key)
	}

	for i, tc := range []struct {
		name string
		// first and second are the tokens of two requests with one key.
		first, second string
		wantFirst     int
		wantSecond    int
		replayed      bool
	}{
		{name: "stored response is not replayed without scope", first: writer, second: reader,
			wantFirst: http.StatusOK, wantSecond: http.StatusForbidden},
		{name: "refused request does not take the key", first: reader, second: writer,
			wantFirst: http.StatusForbidden, wantSecond: http.StatusOK},
		{name: "allowed retry is replayed", first: writer, second: writer,
			wantFirst: http.StatusOK, wantSecond: http.StatusOK, replayed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := "key-" + strconv.Itoa(i)
			if r := apply(i, tc.first, key); r.Code != tc.wantFirst {
				t.Fatalf("first = %d, want %d", r.Code, tc.wantFirst)
			}
			r := apply(i, tc.second, key)
			if r.Code != tc.wantSecond {
				t.Fatalf("second = %d, want %d", r.Code, tc.wantSecond)
			}
			if got := r.Header().Get(ReplayedHeader) == "true"; got != tc.replayed {
				t.Fatalf("replayed = %v, want %v", got, tc.replayed)
			}
		})
	}
}
