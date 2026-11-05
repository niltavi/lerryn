package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUndefinedRoutesAnswerNotFound(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "unknown path", method: http.MethodGet, path: "/api/v1/nope"},
		{name: "undeclared method on a known path", method: http.MethodPost, path: "/api/v1/discover"},
		{name: "prefix itself", method: http.MethodGet, path: "/api/v1/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			New(Options{}).ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			p := decodeProblem(t, rec, http.StatusNotFound)
			if p.Code != ErrorCodeNotFound || p.Type != "urn:lerryn:error:not_found" {
				t.Errorf("problem = %+v", p)
			}
			if p.Instance == nil || *p.Instance != tt.path {
				t.Errorf("instance = %v, want %q", p.Instance, tt.path)
			}
		})
	}
}

func TestRequestIDs(t *testing.T) {
	h := New(Options{})
	seen := map[string]bool{}
	for range 3 {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil)
		req.Header.Set(RequestIDHeader, "req_chosen_by_client")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		id := rec.Header().Get(RequestIDHeader)
		if !strings.HasPrefix(id, "req_") || len(id) != len("req_")+26 {
			t.Fatalf("%s = %q", RequestIDHeader, id)
		}
		if id == "req_chosen_by_client" {
			t.Fatal("client-supplied request ID was reused")
		}
		if seen[id] {
			t.Fatalf("request ID %q repeated", id)
		}
		seen[id] = true
		if p := decodeProblem(t, rec, http.StatusNotFound); p.RequestID != id {
			t.Errorf("request_id = %q, header = %q", p.RequestID, id)
		}
	}
}

func TestResponseErrorHidesDetails(t *testing.T) {
	var logs bytes.Buffer
	h := &handler{log: slog.New(slog.NewJSONHandler(&logs, nil))}
	var rec *httptest.ResponseRecorder
	withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec = httptest.NewRecorder()
		h.responseError(rec, r, errors.New("dial tcp 10.0.0.5:5432: connection refused"))
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/discover", nil))

	p := decodeProblem(t, rec, http.StatusInternalServerError)
	if p.Code != ErrorCodeInternalError {
		t.Errorf("code = %q", p.Code)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Errorf("internal error leaked to the client: %s", rec.Body)
	}
	if !strings.Contains(logs.String(), "10.0.0.5") || !strings.Contains(logs.String(), p.RequestID) {
		t.Errorf("log should carry the error and request ID: %s", logs.String())
	}
}

func TestResponseErrorLevel(t *testing.T) {
	canceled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	tests := []struct {
		name  string
		ctx   context.Context
		err   error
		level string
	}{
		{"server fault", context.Background(), errors.New("dial tcp: connection refused"), "ERROR"},
		{"client went away", canceled(), fmt.Errorf("list runs: %w", context.Canceled), "DEBUG"},
		// The server cancelled something itself while the client waits:
		// a real failure.
		{"internal cancellation", context.Background(), fmt.Errorf("query: %w", context.Canceled), "ERROR"},
		// The client left, but the failure is something else.
		{"other error after disconnect", canceled(), errors.New("disk full"), "ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			h := &handler{log: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
			r := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil).WithContext(tt.ctx)
			h.responseError(httptest.NewRecorder(), r, tt.err)
			var rec struct{ Level, Error string }
			if err := json.Unmarshal(logs.Bytes(), &rec); err != nil {
				t.Fatalf("log line: %v: %s", err, logs.String())
			}
			if rec.Level != tt.level || rec.Error == "" {
				t.Errorf("level = %s, error = %q; want %s", rec.Level, rec.Error, tt.level)
			}
		})
	}
}

func TestProblemResponsesMatchSpec(t *testing.T) {
	// discover declares only `default` for errors, so any problem written on
	// its path must validate against the Problem schema.
	h := &handler{log: slog.New(slog.DiscardHandler)}
	tests := []struct {
		name  string
		write func(http.ResponseWriter, *http.Request)
	}{
		{name: "parameter error", write: func(w http.ResponseWriter, r *http.Request) {
			h.parameterError(w, r, &RequiredHeaderError{ParamName: "If-Match"})
		}},
		{name: "body error", write: func(w http.ResponseWriter, r *http.Request) {
			h.requestError(w, r, errors.New("unexpected EOF"))
		}},
		{name: "internal error", write: func(w http.ResponseWriter, r *http.Request) {
			h.responseError(w, r, errors.New("boom"))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/discover", nil)
			rec := httptest.NewRecorder()
			withRequestID(http.HandlerFunc(tt.write)).ServeHTTP(rec, req)
			assertMatchesSpec(t, req, rec)
		})
	}
}

func TestProblemResponseForStrictHandlers(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discover", nil)
	rec := httptest.NewRecorder()
	withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := problemResponse[DiscoverdefaultApplicationProblemPlusJSONResponse](
			r.Context(), http.StatusServiceUnavailable, ErrorCodeRunnerUnavailable, "没有带 gpu 标签的 runner")
		if err := resp.VisitDiscoverResponse(w); err != nil {
			t.Fatal(err)
		}
	})).ServeHTTP(rec, req)

	p := decodeProblem(t, rec, http.StatusServiceUnavailable)
	if p.Code != ErrorCodeRunnerUnavailable || p.RequestID != rec.Header().Get(RequestIDHeader) {
		t.Errorf("problem = %+v", p)
	}
	assertMatchesSpec(t, req, rec)
}

func TestParameterFieldError(t *testing.T) {
	inner := errors.New("strconv.ParseInt: invalid syntax")
	tests := []struct {
		name          string
		err           error
		wantHeader    string
		wantParameter string
	}{
		{name: "missing header", err: &RequiredHeaderError{ParamName: "If-Match"}, wantHeader: "If-Match"},
		{name: "missing query", err: &RequiredParamError{ParamName: "limit"}, wantParameter: "limit"},
		{name: "bad format", err: &InvalidParamFormatError{ParamName: "limit", Err: inner}, wantParameter: "limit"},
		{name: "repeated", err: &TooManyValuesForParamError{ParamName: "cursor", Count: 2}, wantParameter: "cursor"},
		{name: "bad json", err: &UnmarshalingParamError{ParamName: "filter", Err: inner}, wantParameter: "filter"},
		{name: "bad cookie", err: &UnescapedCookieParamError{ParamName: "lerryn_session", Err: inner}, wantParameter: "lerryn_session"},
		{name: "unknown", err: errors.New("something new")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fe := parameterFieldError(tt.err)
			if fe.Detail == "" {
				t.Error("detail is empty")
			}
			if deref(fe.Header) != tt.wantHeader || deref(fe.Parameter) != tt.wantParameter || fe.Pointer != nil {
				t.Errorf("got header=%q parameter=%q pointer=%v", deref(fe.Header), deref(fe.Parameter), fe.Pointer)
			}
		})
	}
}

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) Problem {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d: %s", rec.Code, wantStatus, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != wantStatus || p.Title == "" || p.RequestID == "" {
		t.Errorf("problem = %+v", p)
	}
	return p
}
