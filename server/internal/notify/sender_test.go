package notify

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type confirmationTransport func(*http.Request) (*http.Response, error)

func (f confirmationTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type confirmationBody struct {
	io.Reader
	read   int
	closed bool
}

func (r *confirmationBody) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

func (r *confirmationBody) Close() error { r.closed = true; return nil }

type confirmationReadError struct{ err error }

func (r confirmationReadError) Read([]byte) (int, error) { return 0, r.err }

func TestSenderRequiresCompleteFeishuConfirmation(t *testing.T) {
	for _, tt := range []struct {
		name, kind, body string
		status           int
		readError        error
		wantFailure      bool
	}{
		{name: "modern confirmation", kind: KindFeishu, body: `{"code":0,"msg":"success"}`},
		{name: "confirmation at the size limit", kind: KindFeishu, body: `{"code":0}` + strings.Repeat(" ", maxResponseBytes-len(`{"code":0}`))},
		{name: "legacy confirmation", kind: KindFeishu, body: `{"StatusCode":0,"StatusMessage":"success"}`},
		{name: "modern code takes precedence", kind: KindFeishu, body: `{"code":0,"StatusCode":1}`},
		{name: "modern error", kind: KindFeishu, body: `{"code":19024,"msg":"Key Words Not Found"}`, wantFailure: true},
		{name: "legacy error", kind: KindFeishu, body: `{"StatusCode":19024,"StatusMessage":"Key Words Not Found"}`, wantFailure: true},
		{name: "missing confirmation", kind: KindFeishu, body: `{}`, wantFailure: true},
		{name: "empty response", kind: KindFeishu, wantFailure: true},
		{name: "null response", kind: KindFeishu, body: `null`, wantFailure: true},
		{name: "null codes", kind: KindFeishu, body: `{"code":null,"StatusCode":null}`, wantFailure: true},
		{name: "malformed confirmation", kind: KindFeishu, body: `{"code":19024`, wantFailure: true},
		{name: "non-integer code", kind: KindFeishu, body: `{"code":"0"}`, wantFailure: true},
		{name: "proxy HTML", kind: KindFeishu, body: `<html>upstream unavailable</html>`, wantFailure: true},
		{name: "response exceeds limit", kind: KindFeishu, body: strings.Repeat(" ", maxResponseBytes+1) + `{"code":19024}`, wantFailure: true},
		{name: "incomplete error response", kind: KindFeishu, body: `{"code":19024,`, readError: io.ErrUnexpectedEOF, wantFailure: true},
		{name: "success prefix before read failure", kind: KindFeishu, body: `{"code":0}`, readError: io.ErrUnexpectedEOF, wantFailure: true},
		{name: "body timeout", kind: KindFeishu, body: `{"code":`, readError: context.DeadlineExceeded, wantFailure: true},
		{name: "webhook acknowledges by status", kind: KindWebhook, status: http.StatusNoContent},
		{name: "webhook body failure does not reject status", kind: KindWebhook, readError: io.ErrUnexpectedEOF},
		{name: "slack acknowledges by status", kind: KindSlack, body: "ok"},
		{name: "slack does not parse a feishu code", kind: KindSlack, body: `{"code":19024}`},
		{name: "slack body failure does not reject status", kind: KindSlack, readError: io.ErrUnexpectedEOF},
		{name: "non-2xx is still a status error", kind: KindFeishu, status: http.StatusBadGateway, readError: io.ErrUnexpectedEOF, wantFailure: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status := tt.status
			if status == 0 {
				status = http.StatusOK
			}
			var reader io.Reader = strings.NewReader(tt.body)
			if tt.readError != nil {
				reader = io.MultiReader(reader, confirmationReadError{err: tt.readError})
			}
			body := &confirmationBody{Reader: reader}
			sender := &Sender{client: &http.Client{Transport: confirmationTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Body: body}, nil
			})}}
			got, err := sender.Send(t.Context(), tt.kind, "https://notify.example.com/hook/private-token", Request{Header: http.Header{}})
			if got != status || (err != nil) != tt.wantFailure {
				t.Fatalf("Send = %d, %v; want status %d, failure %v", got, err, status, tt.wantFailure)
			}
			if tt.kind == KindFeishu && tt.readError != nil && status == http.StatusOK && !errors.Is(err, tt.readError) {
				t.Errorf("read failure = %v, want %v", err, tt.readError)
			}
			if tt.wantFailure && Describe(err) == "" {
				t.Error("failed confirmation has no delivery explanation")
			}
			if err != nil && strings.Contains(err.Error(), "private-token") {
				t.Errorf("error includes target credential: %v", err)
			}
			if body.read > maxResponseBytes+1 || !body.closed {
				t.Errorf("response read %d bytes, closed %v", body.read, body.closed)
			}
		})
	}
}
