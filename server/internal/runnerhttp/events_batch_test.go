package runnerhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

func batchClient(t *testing.T, transport budgetRoundTrip) *Client {
	t.Helper()
	c, err := NewClient(ClientOptions{ServerURL: "https://lerryn.example.com", RegistrationToken: "registration", CredentialFile: filepath.Join(t.TempDir(), "credential"), HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func resultBurst(count int) []runproto.Event {
	raw := json.RawMessage(`"` + strings.Repeat("a", (64<<10)-2) + `"`)
	events := make([]runproto.Event, count)
	for i := range events {
		events[i] = runproto.Event{Seq: int64(i + 1), TS: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC), Kind: runproto.EventResult, Result: &runproto.Result{Status: "ok", Summary: "checkpoint", Data: raw}}
	}
	return events
}

func readEventBatch(t *testing.T, req *http.Request) ([]byte, []event) {
	t.Helper()
	body, err := io.ReadAll(io.LimitReader(req.Body, maxRequestBytes+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > maxRequestBytes || int64(len(body)) != req.ContentLength {
		t.Fatalf("request size=%d Content-Length=%d, limit=%d", len(body), req.ContentLength, maxRequestBytes)
	}
	var decoded eventsRequest
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Events) == 0 {
		t.Fatal("sent an empty event batch")
	}
	return body, decoded.Events
}

func TestClientEventRequestBoundaries(t *testing.T) {
	for _, extra := range []int{-1, 0, 1} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			events := resultBurst(256)
			// The ASCII fixture has the same bytes under the legacy encoder. Use it
			// independently to place the whole request on either side of the limit.
			encoded, err := json.Marshal(eventsRequest{Events: toWireEvents(events)})
			if err != nil {
				t.Fatal(err)
			}
			trim := len(encoded) - maxRequestBytes - extra
			if trim <= 0 || trim >= len(events[255].Result.Data)-2 {
				t.Fatal("boundary fixture no longer reaches the request limit")
			}
			events[255].Result.Data = json.RawMessage(`"` + strings.Repeat("a", len(events[255].Result.Data)-trim-2) + `"`)
			var got []event
			var sizes []int
			c := batchClient(t, func(req *http.Request) (*http.Response, error) {
				data, batch := readEventBatch(t, req)
				sizes = append(sizes, len(data))
				got = append(got, batch...)
				return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
			})
			if err := c.AppendEvents(t.Context(), "runner", uuid.New(), events); err != nil {
				t.Fatal(err)
			}
			wantCalls := 1
			if extra > 0 {
				wantCalls = 2
			}
			if len(sizes) != wantCalls || extra <= 0 && sizes[0] != maxRequestBytes+extra {
				t.Fatalf("request sizes=%v; expected %d requests around %d bytes", sizes, wantCalls, maxRequestBytes+extra)
			}
			if !reflect.DeepEqual(got, toWireEvents(events)) {
				t.Fatal("event order, metadata or raw result data changed while batching")
			}
		})
	}
}

func TestClientEventBatchPreservesEscapes(t *testing.T) {
	events := resultBurst(300)
	for i := range events {
		events[i].Result.Summary = "\x00\n\"\\<&>\u2028\u2029"
		events[i].Result.Data = json.RawMessage(`{"html":"<>&","literal":"\\u003c","escaped":"\u003c\u2028","number":9007199254740993,"padding":"` + strings.Repeat("a", 65000) + `"}`)
	}
	var got []event
	calls := 0
	c := batchClient(t, func(req *http.Request) (*http.Response, error) {
		_, batch := readEventBatch(t, req)
		calls++
		got = append(got, batch...)
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	})
	if err := c.AppendEvents(t.Context(), "runner", uuid.New(), events); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !reflect.DeepEqual(got, toWireEvents(events)) {
		t.Fatalf("%d requests did not preserve escaped result JSON and metadata", calls)
	}
}

func TestClientEventBatchRejectsInvalidSingleEvent(t *testing.T) {
	for _, tt := range []struct {
		name   string
		events []runproto.Event
	}{
		{name: "oversized", events: []runproto.Event{{Seq: 1, Kind: runproto.EventStageStart, Stage: "fetch", Message: strings.Repeat("a", maxRequestBytes)}}},
		{name: "invalid result JSON", events: []runproto.Event{{Seq: 1, Kind: runproto.EventResult, Result: &runproto.Result{Status: "ok", Data: json.RawMessage(`{"bad":`)}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c := batchClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
			})
			if err := c.AppendEvents(t.Context(), "runner", uuid.New(), tt.events); !errors.Is(err, runproto.ErrRejected) {
				t.Fatalf("invalid single event error=%v", err)
			}
			if calls != 0 {
				t.Fatalf("sent %d requests for an invalid single event", calls)
			}
		})
	}
}

func TestClientEventBatchEmptyAndCanceled(t *testing.T) {
	calls := 0
	c := batchClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	})
	for _, events := range [][]runproto.Event{nil, {}} {
		if err := c.AppendEvents(t.Context(), "runner", uuid.New(), events); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.AppendEvents(ctx, "runner", uuid.New(), resultBurst(1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled batch=%v", err)
	}
	if calls != 0 {
		t.Fatalf("sent %d empty or canceled requests", calls)
	}
}

func TestClientEventBatchStopsOnFailure(t *testing.T) {
	for _, kind := range []string{"network", "server", "rejected", "lease lost", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			c := batchClient(t, func(req *http.Request) (*http.Response, error) {
				_, batch := readEventBatch(t, req)
				calls++
				if calls == 1 {
					if batch[0].Seq != 1 {
						t.Fatal("first batch does not begin at sequence one")
					}
					if kind == "canceled" {
						cancel()
					}
					return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
				}
				if kind == "network" {
					return nil, errors.New("connection reset")
				}
				body := "server unavailable"
				status := http.StatusServiceUnavailable
				if kind == "rejected" {
					status = http.StatusBadRequest
					body = `{"code":"rejected","message":"invalid event"}`
				}
				if kind == "lease lost" {
					status = http.StatusConflict
					body = `{"code":"lease_lost"}`
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			err := c.AppendEvents(ctx, "runner", uuid.New(), resultBurst(600))
			wantCalls := 2
			if kind == "canceled" {
				wantCalls = 1
			}
			if err == nil || calls != wantCalls {
				t.Fatalf("failure=%v requests=%d; want error after %d requests", err, calls, wantCalls)
			}
			if errors.Is(err, runproto.ErrRejected) != (kind == "rejected") || errors.Is(err, runproto.ErrLeaseLost) != (kind == "lease lost") || errors.Is(err, context.Canceled) != (kind == "canceled") {
				t.Fatalf("wrong error class for %s: %v", kind, err)
			}
		})
	}
}

func TestClientEventBatchCredentialRetryUsesSameBody(t *testing.T) {
	registrations, eventCalls := 0, 0
	var retryBody []byte
	c := batchClient(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == pathRegister {
			registrations++
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"runner_id":"runner","credential":"wl_credential%d"}`, registrations)))}, nil
		}
		body, _ := readEventBatch(t, req)
		eventCalls++
		if eventCalls == 2 {
			retryBody = bytes.Clone(body)
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{"code":"unauthenticated"}`))}, nil
		}
		if eventCalls == 3 && (!bytes.Equal(body, retryBody) || req.Header.Get("Authorization") != "Bearer wl_credential2") {
			t.Fatal("credential retry changed the batch or used the old credential")
		}
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	})
	if err := c.Register(t.Context(), runproto.Registration{ID: "runner", Slots: 1}); err != nil {
		t.Fatal(err)
	}
	if err := c.AppendEvents(t.Context(), "runner", uuid.New(), resultBurst(300)); err != nil {
		t.Fatal(err)
	}
	if registrations != 2 || eventCalls != 3 {
		t.Fatalf("registrations=%d events=%d", registrations, eventCalls)
	}
}
