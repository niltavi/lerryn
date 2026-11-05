package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/niltavi/lerryn/server/internal/mcp"
)

func TestStdioProxyInterruptedResponse(t *testing.T) {
	for _, size := range []int{16, mcp.DefaultMaxResultBytes + 100} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			partial := `{"value":"` + strings.Repeat("x", size)
			srv := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Length", strconv.Itoa(len(partial)+1))
				_, _ = io.WriteString(w, partial)
			})
			// The ordinary CLI already detects this real HTTP read failure.
			got := run(t, srv.URL, "runs", "get", "r1")
			if got.code != ExitFailure || !strings.Contains(got.stderr, "unexpected EOF") {
				t.Fatalf("ordinary CLI: exit %d, stderr %q", got.code, got.stderr)
			}
			cs := proxySession(t, srv.URL)
			for _, params := range []*mcpsdk.CallToolParams{
				{Name: "run_get", Arguments: map[string]any{"run_id": "r1"}},
				{Name: "api_call", Arguments: map[string]any{"operation_id": "getRun", "arguments": map[string]any{"run_id": "r1"}}},
			} {
				t.Run(params.Name, func(t *testing.T) {
					res, err := cs.CallTool(t.Context(), params)
					if err != nil {
						t.Fatal(err)
					}
					text := res.Content[0].(*mcpsdk.TextContent).Text
					if !res.IsError || res.StructuredContent != nil || !strings.Contains(text, "unexpected EOF") {
						t.Fatalf("interrupted response: %+v, text %q", res, text)
					}
					if strings.Contains(text, partial) || res.Meta["lerryn/http_status"] != nil || res.Meta["lerryn/truncated"] != nil {
						t.Fatalf("partial response survived the read failure: %+v, text %q", res, text)
					}
				})
			}
		})
	}
}

func TestStdioProxyCompleteResponses(t *testing.T) {
	for _, tt := range []struct {
		name, contentType, body, wantText string
		structured, truncated             bool
	}{
		{name: "JSON", contentType: "application/json", body: `{"id":"r1"}`, wantText: `"id":"r1"`, structured: true},
		{name: "binary", contentType: "application/octet-stream", body: "\xff\xfe\x00\x01", wantText: "Binary content"},
		{name: "event stream bytes", contentType: "text/event-stream", body: "event: end\ndata: {}\n\n", wantText: "event: end"},
		{name: "large JSON", contentType: "application/json", body: `{"value":"` + strings.Repeat("x", mcp.DefaultMaxResultBytes+100) + `"}`, wantText: "[Truncated:", truncated: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				_, _ = io.WriteString(w, tt.body)
			})
			cs := proxySession(t, srv.URL)
			res, err := cs.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "run_get", Arguments: map[string]any{"run_id": "r1"}})
			if err != nil {
				t.Fatal(err)
			}
			text := res.Content[0].(*mcpsdk.TextContent).Text
			if res.IsError || (res.StructuredContent != nil) != tt.structured || !strings.Contains(text, tt.wantText) {
				t.Fatalf("complete response: %+v, text %q", res, text)
			}
			if (res.Meta["lerryn/truncated"] == true) != tt.truncated {
				t.Fatalf("truncation changed: %+v", res.Meta)
			}
			if tt.truncated && res.Meta["lerryn/total_bytes"] != float64(len(tt.body)) {
				t.Fatalf("response byte count = %v, want %d", res.Meta["lerryn/total_bytes"], len(tt.body))
			}
		})
	}
}

type endingReader struct {
	io.Reader
	end func() error
}

func (r endingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		err = r.end()
	}
	return n, err
}

func TestRemoteAPIResponseFailure(t *testing.T) {
	for _, cause := range []string{"read failure", "cancel after buffered body"} {
		t.Run(cause, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			client, err := newClient("https://lerryn.example.com/private", testToken, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body := endingReader{Reader: strings.NewReader(`{"id":"r1"}`), end: func() error {
					if cause == "read failure" {
						return &url.Error{Op: "read", URL: req.URL.String() + "?token=" + testToken, Err: io.ErrUnexpectedEOF}
					}
					cancel()
					return io.EOF
				}}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(body)}, nil
			})}, nil)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/runs/r1", nil)
			err = (remoteAPI{c: client}).ServeHTTPError(httptest.NewRecorder(), req)
			want := io.ErrUnexpectedEOF
			if cause != "read failure" {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("forwarding error = %v, want %v", err, want)
			}
			if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "https://") || strings.Contains(err.Error(), "/private") {
				t.Fatalf("forwarding error includes the target URL or credential: %v", err)
			}
			defer func() {
				p := recover()
				if err, ok := p.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
					t.Errorf("HTTP handler panic = %v, want ErrAbortHandler", p)
				}
			}()
			(remoteAPI{c: client}).ServeHTTP(httptest.NewRecorder(), req)
		})
	}
}

func TestRemoteAPIRequestFailureOmitsTargetURL(t *testing.T) {
	client, err := newClient("https://lerryn.example.com/private", testToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	err = (remoteAPI{c: client}).ServeHTTPError(rec, httptest.NewRequest(http.MethodGet, "/api/v1/runs/r1", nil))
	if err != nil || rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "unexpected EOF") {
		t.Fatalf("request failure = status %d, %v, body %q", rec.Code, err, rec.Body.String())
	}
	for _, hidden := range []string{testToken, "https://", "/private"} {
		if strings.Contains(rec.Body.String(), hidden) {
			t.Fatalf("request failure exposes the target URL or credential: %q", rec.Body.String())
		}
	}
}
