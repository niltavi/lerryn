package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestValidation(t *testing.T) {
	c := newTaskClient(t)
	token, project := c.agent("alice")
	path := "/api/v1/projects/" + project.String() + "/tasks/digest"

	raw := func(auth, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		c.h.ServeHTTP(rec, req)
		return rec
	}

	// A body that is not JSON points at the whole body.
	rec := raw(token, `{"manifest":`)
	c.want(rec, http.StatusBadRequest, ErrorCodeValidationFailed)
	wantPointers(t, rec, "#")

	// Bodies are capped before they are read.
	rec = raw(token, `{"files":"`+strings.Repeat("x", maxRequestBytes)+`"}`)
	c.want(rec, http.StatusRequestEntityTooLarge, ErrorCodeValidationFailed)

	// A caller without access learns nothing from validation: the access
	// check answers first.
	c.want(raw("", `{"manifest":{}}`), http.StatusUnauthorized, ErrorCodeUnauthenticated)
	readOnly, _ := c.agent("bob", "read")
	c.want(raw(readOnly, `{"manifest":{}}`), http.StatusForbidden, ErrorCodeInsufficientScope)

	// Query parameters are checked against their schema too.
	rec = c.as(token, http.MethodGet, "/api/v1/runs/"+project.String()+"/logs?limit=0", nil)
	c.want(rec, http.StatusBadRequest, ErrorCodeValidationFailed)
	if errs := *decode[Problem](t, rec).Errors; len(errs) != 1 || deref(errs[0].Parameter) != "limit" {
		t.Fatalf("errors = %+v", errs)
	}
}

func TestJSONPointer(t *testing.T) {
	if got := jsonPointer([]string{"files", "0", "a/b~c"}); got != "/files/0/a~1b~0c" {
		t.Fatalf("jsonPointer = %q", got)
	}
	if got := jsonPointer(nil); got != "" {
		t.Fatalf("jsonPointer(nil) = %q", got)
	}
}
