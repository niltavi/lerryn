package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type stalledWaitBody struct{ ctx context.Context }

func (b stalledWaitBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (stalledWaitBody) Close() error { return nil }

func TestWaitRunDeadlineBoundsRequests(t *testing.T) {
	reg, err := (&app{opts: Options{Spec: []byte(testSpec)}}).registry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			parentDeadline, _ := ctx.Deadline()
			client, err := newClient("https://lerryn.example.com", testToken, &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					deadline, ok := req.Context().Deadline()
					if !ok || !deadline.Before(parentDeadline) {
						t.Error("run wait did not put its own deadline on the HTTP request")
					}
					if phase == "headers" {
						<-req.Context().Done()
						return nil, req.Context().Err()
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: stalledWaitBody{req.Context()}}, nil
				}),
			}, reg)
			if err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			s := &session{Client: client, json: true, out: &out}
			if err := s.waitRun(ctx, "r1", 50*time.Millisecond); ExitCode(err) != ExitWaitTimeout {
				t.Fatalf("stalled %s = %v; want wait timeout", phase, err)
			}
			if out.Len() != 0 {
				t.Fatalf("invented a run response after stalled %s: %s", phase, out.String())
			}
		})
	}
}

func TestWaitRunTimeoutKeepsLastObservedRun(t *testing.T) {
	reg, err := (&app{opts: Options{Spec: []byte(testSpec)}}).registry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	calls := 0
	client, err := newClient("https://lerryn.example.com", testToken, &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if calls > 1 {
				<-req.Context().Done()
				return nil, req.Context().Err()
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"r1","status":"running"}`))}, nil
		}),
	}, reg)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	s := &session{Client: client, json: true, out: &out}
	if err := s.waitRun(ctx, "r1", 200*time.Millisecond); ExitCode(err) != ExitWaitTimeout {
		t.Fatalf("wait after an observed run = %v; want wait timeout", err)
	}
	if !strings.Contains(out.String(), `"status": "running"`) {
		t.Fatalf("last observed run was lost: %s", out.String())
	}
}

func TestWaitRunPreservesParentCancellation(t *testing.T) {
	reg, err := (&app{opts: Options{Spec: []byte(testSpec)}}).registry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%t", deadline), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 30*time.Millisecond)
				want = context.DeadlineExceeded
			}
			defer cancel()
			client, err := newClient("https://lerryn.example.com", testToken, &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if !deadline {
						cancel()
					}
					<-req.Context().Done()
					return nil, req.Context().Err()
				}),
			}, reg)
			if err != nil {
				t.Fatal(err)
			}
			s := &session{Client: client, json: true, out: io.Discard}
			if err := s.waitRun(ctx, "r1", time.Minute); !errors.Is(err, want) || ExitCode(err) != ExitFailure {
				t.Fatalf("parent cancellation = %v, want %v without wait-timeout exit", err, want)
			}
		})
	}
}

type bufferedWaitBody struct {
	*strings.Reader
	once       sync.Once
	beforeRead func()
}

func (b *bufferedWaitBody) Read(p []byte) (int, error) {
	b.once.Do(b.beforeRead)
	return b.Reader.Read(p)
}

func (*bufferedWaitBody) Close() error { return nil }

func TestWaitRunRejectsTerminalResponseAfterCancellation(t *testing.T) {
	reg, err := (&app{opts: Options{Spec: []byte(testSpec)}}).registry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, parentCanceled := range []bool{true, false} {
		t.Run(fmt.Sprintf("parentCanceled=%t", parentCanceled), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			client, err := newClient("https://lerryn.example.com", testToken, &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					body := &bufferedWaitBody{
						Reader: strings.NewReader(`{"status":"completed"}`),
						beforeRead: func() {
							if parentCanceled {
								cancel()
							} else {
								<-req.Context().Done()
							}
						},
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
				}),
			}, reg)
			if err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			s := &session{Client: client, json: true, out: &out}
			err = s.waitRun(ctx, "r1", 20*time.Millisecond)
			if parentCanceled {
				if !errors.Is(err, context.Canceled) || ExitCode(err) != ExitFailure {
					t.Fatalf("terminal response after parent cancellation = %v", err)
				}
			} else if ExitCode(err) != ExitWaitTimeout {
				t.Fatalf("terminal response after wait timeout = %v", err)
			}
			if out.Len() != 0 {
				t.Fatalf("printed a response received outside the wait: %s", out.String())
			}
		})
	}
}
