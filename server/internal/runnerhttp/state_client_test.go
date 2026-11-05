package runnerhttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

type stateRoundTrip func(*http.Request) (*http.Response, error)

func (f stateRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type stateResponseBody struct {
	io.Reader
	closeErr error
	closed   bool
}

func (b *stateResponseBody) Close() error { b.closed = true; return b.closeErr }

type statePaddingReader struct{}

func (statePaddingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}

type stateErrorReader struct{}

func (stateErrorReader) Read([]byte) (int, error) { return 0, errors.New("synthetic read failure") }

func stateClient(t *testing.T, transport stateRoundTrip) *Client {
	t.Helper()
	client, err := NewClient(ClientOptions{ServerURL: "http://state.invalid", RegistrationToken: "synthetic-registration", CredentialFile: filepath.Join(t.TempDir(), "credential"), HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestStateClientResponseLimitsAndValidation(t *testing.T) {
	valid := `{"version":1,"entries":[]}`
	for _, mode := range []string{"valid", "missing", "null", "unknown", "trailing", "content length", "chunked limit", "read failure", "close failure"} {
		t.Run(mode, func(t *testing.T) {
			var body *stateResponseBody
			client := stateClient(t, func(request *http.Request) (*http.Response, error) {
				if request.Header.Get(ProtocolHeader) != strconv.Itoa(runproto.StateProtocolVersion) || request.Method != http.MethodGet {
					t.Error("state GET did not use the frozen v2 protocol")
				}
				data, length := valid, int64(-1)
				switch mode {
				case "missing":
					data = `{"version":1}`
				case "null":
					data = `{"version":1,"entries":null}`
				case "unknown":
					data = `{"version":1,"entries":[],"extra":1}`
				case "trailing":
					data += `{}`
				case "content length":
					length = int64(runproto.MaxStateWireBytes) + 1
				}
				body = &stateResponseBody{Reader: strings.NewReader(data)}
				if mode == "chunked limit" {
					body.Reader = io.LimitReader(statePaddingReader{}, int64(runproto.MaxStateWireBytes)+1)
				}
				if mode == "read failure" {
					body.Reader = stateErrorReader{}
				}
				if mode == "close failure" {
					body.closeErr = errors.New("synthetic close failure")
				}
				return &http.Response{StatusCode: http.StatusOK, Body: body, ContentLength: length, Header: http.Header{}}, nil
			})
			snapshot, err := client.GetState(t.Context(), "ignored", uuid.New())
			if mode == "valid" {
				if err != nil || snapshot.Version != runproto.StateFormatVersion || snapshot.Entries == nil {
					t.Fatalf("valid empty snapshot: %+v, %v", snapshot, err)
				}
			} else if err == nil {
				t.Fatal("incomplete or oversized state response was accepted")
			}
			if body == nil || !body.closed {
				t.Fatal("state response body was not closed")
			}
		})
	}
}

func TestStateClientCredentialRefreshReplaysCanonicalBody(t *testing.T) {
	snapshot := runproto.StateSnapshot{Version: runproto.StateFormatVersion, Entries: []runproto.StateEntry{{Path: "cursor", Content: []byte{0, 1, 255}}}}
	want, _, err := runproto.EncodeState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	registrations, uploads := 0, 0
	client := stateClient(t, func(request *http.Request) (*http.Response, error) {
		status, data := http.StatusOK, ""
		if request.URL.Path == pathRegister {
			registrations++
			data = `{"runner_id":"state-client","credential":"wl_synthetic_transport"}`
		} else {
			uploads++
			body, err := io.ReadAll(request.Body)
			if err != nil || !bytes.Equal(body, want) || request.Method != http.MethodPut {
				t.Error("credential refresh did not replay the complete canonical snapshot")
			}
			status = http.StatusNoContent
			if uploads == 1 {
				status, data = http.StatusUnauthorized, `{"code":"unauthenticated"}`
			}
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(data)), Header: http.Header{}}, nil
	})
	if err := client.Register(t.Context(), runproto.Registration{ID: "state-client", Slots: 1}); err != nil {
		t.Fatal(err)
	}
	if err := client.PutState(t.Context(), "ignored", uuid.New(), snapshot); err != nil {
		t.Fatal(err)
	}
	if uploads != 2 || registrations != 2 {
		t.Fatalf("uploads=%d registrations=%d", uploads, registrations)
	}
}

func TestStateClientRejectsInvalidBeforeSending(t *testing.T) {
	calls := 0
	client := stateClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected request")
	})
	invalid := runproto.StateSnapshot{Version: runproto.StateFormatVersion, Entries: []runproto.StateEntry{{Path: "../outside"}}}
	if err := client.PutState(t.Context(), "ignored", uuid.New(), invalid); !errors.Is(err, runproto.ErrRejected) || calls != 0 {
		t.Fatal("invalid state was sent to the server or lost its permanent classification")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.GetState(ctx, "ignored", uuid.New()); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("cancelled state request reached the transport")
	}
}

func TestStatePutPropagatesResponseCloseFailure(t *testing.T) {
	body := &stateResponseBody{Reader: strings.NewReader(""), closeErr: errors.New("synthetic close failure")}
	client := stateClient(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Body: body, Header: http.Header{}}, nil
	})
	if err := client.PutState(t.Context(), "ignored", uuid.New(), runproto.StateSnapshot{Version: runproto.StateFormatVersion}); err == nil || !body.closed {
		t.Fatal("state Put accepted an incomplete acknowledgement")
	}
}
