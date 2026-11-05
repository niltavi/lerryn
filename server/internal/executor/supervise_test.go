package executor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeRun is an Execution whose exit the test controls.
type fakeRun struct {
	exited chan struct{}
	status ExitStatus
}

func newFakeRun() *fakeRun { return &fakeRun{exited: make(chan struct{})} }

func (r *fakeRun) ID() string { return "c1" }

func (r *fakeRun) Wait(ctx context.Context) (ExitStatus, error) {
	select {
	case <-r.exited:
		return r.status, nil
	case <-ctx.Done():
		return ExitStatus{}, ctx.Err()
	}
}

func (r *fakeRun) exit(code int) {
	r.status = ExitStatus{Code: code}
	close(r.exited)
}

// fakeStopper is an Executor whose Stop the test controls. Only Stop is
// used by Supervise.
type fakeStopper struct {
	Executor
	mu     sync.Mutex
	calls  int
	grace  time.Duration
	result Stopped
	err    error
	// onStop runs inside Stop, before it returns; tests use it to make the
	// container exit or to block.
	onStop func(ctx context.Context)
}

func (f *fakeStopper) Stop(ctx context.Context, _ string, grace time.Duration) (Stopped, error) {
	f.mu.Lock()
	f.calls++
	f.grace = grace
	f.mu.Unlock()
	if f.onStop != nil {
		f.onStop(ctx)
	}
	return f.result, f.err
}

func (f *fakeStopper) stopCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestSuperviseExited(t *testing.T) {
	run := newFakeRun()
	ex := &fakeStopper{}
	run.exit(0)
	res, err := Supervise(t.Context(), ex, run, SuperviseOptions{Timeout: time.Hour})
	if err != nil || res.Outcome != OutcomeExited || res.Exit.Code != 0 {
		t.Fatalf("Supervise() = %+v, %v", res, err)
	}
	if ex.stopCalls() != 0 {
		t.Fatal("Stop called for a run that exited")
	}
}

func TestSuperviseTimeoutAndCancel(t *testing.T) {
	tests := []struct {
		name string
		opts func(cancel chan struct{}) SuperviseOptions
		want Outcome
	}{
		{"timeout", func(chan struct{}) SuperviseOptions {
			return SuperviseOptions{Timeout: 20 * time.Millisecond, Grace: 3 * time.Second}
		}, OutcomeTimedOut},
		{"cancel", func(c chan struct{}) SuperviseOptions {
			close(c)
			return SuperviseOptions{Timeout: time.Hour, Grace: 3 * time.Second, Cancel: c}
		}, OutcomeCanceled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run := newFakeRun()
			stopReturned := false
			ex := &fakeStopper{result: Stopped{Exit: ExitStatus{Code: 137}, Escalated: true}}
			ex.onStop = func(context.Context) {
				// The container exits while Stop is still confirming.
				run.exit(137)
				time.Sleep(10 * time.Millisecond)
				stopReturned = true
			}
			res, err := Supervise(t.Context(), ex, run, tc.opts(make(chan struct{})))
			if err != nil {
				t.Fatalf("Supervise() error = %v", err)
			}
			if !stopReturned {
				t.Fatal("Supervise returned before Stop confirmed")
			}
			if res.Outcome != tc.want || res.Exit.Code != 137 || !res.Escalated {
				t.Fatalf("Supervise() = %+v", res)
			}
			if ex.grace != 3*time.Second {
				t.Fatalf("grace = %v", ex.grace)
			}
		})
	}
}

func TestSuperviseStopFailureIsNotAnOutcome(t *testing.T) {
	run := newFakeRun()
	ex := &fakeStopper{err: ErrNotStopped}
	cancel := make(chan struct{})
	close(cancel)
	res, err := Supervise(t.Context(), ex, run, SuperviseOptions{Cancel: cancel})
	if !errors.Is(err, ErrNotStopped) {
		t.Fatalf("Supervise() = %+v, %v; want ErrNotStopped", res, err)
	}
	if res.Outcome != "" {
		t.Fatalf("outcome %q reported without a confirmed stop", res.Outcome)
	}
}

func TestSuperviseExitRacesCancel(t *testing.T) {
	run := newFakeRun()
	ex := &fakeStopper{result: Stopped{Exit: ExitStatus{Code: 0}, AlreadyExited: true}}
	ex.onStop = func(context.Context) { run.exit(0) }
	cancel := make(chan struct{})
	close(cancel)
	res, err := Supervise(t.Context(), ex, run, SuperviseOptions{Cancel: cancel})
	if err != nil || res.Outcome != OutcomeExited {
		t.Fatalf("Supervise() = %+v, %v; want exited", res, err)
	}
}

func TestSuperviseLeavesContainerWhenContextEnds(t *testing.T) {
	run := newFakeRun()
	ex := &fakeStopper{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Supervise(ctx, ex, run, SuperviseOptions{Timeout: time.Hour})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Supervise() error = %v, want context.Canceled", err)
	}
	if ex.stopCalls() != 0 {
		t.Fatal("Stop called although the runner is going away")
	}
}

func TestSuperviseStopOutlivesCallerContext(t *testing.T) {
	run := newFakeRun()
	ctx, cancelCtx := context.WithCancel(t.Context())
	var stopCtxErr error
	ex := &fakeStopper{result: Stopped{Exit: ExitStatus{Code: 143}}}
	ex.onStop = func(stopCtx context.Context) {
		// The caller's context ends mid-stop; the stop must carry on.
		cancelCtx()
		stopCtxErr = stopCtx.Err()
		run.exit(143)
	}
	cancel := make(chan struct{})
	close(cancel)
	res, err := Supervise(ctx, ex, run, SuperviseOptions{Cancel: cancel})
	if err != nil || res.Outcome != OutcomeCanceled || res.Exit.Code != 143 {
		t.Fatalf("Supervise() = %+v, %v", res, err)
	}
	if stopCtxErr != nil {
		t.Fatalf("stop context ended with the caller's: %v", stopCtxErr)
	}
}
