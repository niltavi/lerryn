package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type failedForwarder struct{ err error }

func (failedForwarder) ServeHTTP(http.ResponseWriter, *http.Request) {
	panic("the error-returning HTTP handler must be used")
}

func (f failedForwarder) ServeHTTPError(w http.ResponseWriter, _ *http.Request) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"partial":"must not escape"}`)
	return f.err
}

func TestForwardingFailureDiscardsResponse(t *testing.T) {
	reg := loadTestRegistry(t)
	get, _ := reg.Tool("run_get")
	for _, cause := range []error{io.ErrUnexpectedEOF, context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			res, err := get.call(t.Context(), failedForwarder{err: cause}, json.RawMessage(`{"run_id":"run_1"}`), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError || res.StructuredContent != nil || res.Meta != nil || len(res.Content) != 1 {
				t.Fatalf("failed forwarding result = %+v", res)
			}
			if text := res.Content[0].(*mcpsdk.TextContent).Text; text != cause.Error() {
				t.Fatalf("failed forwarding text = %q, want only the error", text)
			}
		})
	}
}
