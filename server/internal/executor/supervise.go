package executor

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Outcome says why a supervised execution ended.
type Outcome string

// Outcomes of Supervise.
const (
	// OutcomeExited means the main process exited on its own.
	OutcomeExited Outcome = "exited"
	// OutcomeTimedOut means the time limit elapsed and the container was
	// stopped.
	OutcomeTimedOut Outcome = "timed_out"
	// OutcomeCanceled means a cancel request arrived and the container was
	// stopped.
	OutcomeCanceled Outcome = "canceled"
)

// SuperviseOptions controls Supervise.
type SuperviseOptions struct {
	// Timeout is the run's time limit, measured from the call. Zero means
	// no limit.
	Timeout time.Duration
	// Grace is the SIGTERM grace period. Zero means DefaultStopGrace.
	Grace time.Duration
	// Cancel, when closed, requests the run be cancelled.
	Cancel <-chan struct{}
	// StopTimeout bounds how long Supervise waits for the runtime to
	// confirm the stop, beyond Grace. Zero means 30 seconds.
	StopTimeout time.Duration
}

// Result is the end of a supervised execution.
type Result struct {
	Outcome Outcome
	Exit    ExitStatus
	// Escalated is true when the stop needed SIGKILL.
	Escalated bool
}

// Supervise waits for run to exit, stopping it when the timeout elapses or
// a cancel request arrives. A TimedOut or Canceled result is returned only
// after the executor confirmed the container stopped and its output was
// drained, so the caller may report the terminal state straight away (tech
// design 5.4, acceptance scenario A09). The container is not removed.
//
// If ctx ends first, Supervise returns ctx's error and leaves the container
// alone: the runner is going away, and the run is settled by reconciliation
// rather than by guessing.
func Supervise(ctx context.Context, ex Executor, run Execution, opts SuperviseOptions) (Result, error) {
	grace := opts.Grace
	if grace <= 0 {
		grace = DefaultStopGrace
	}
	stopTimeout := opts.StopTimeout
	if stopTimeout <= 0 {
		stopTimeout = 30 * time.Second
	}

	var timeout <-chan time.Time
	if opts.Timeout > 0 {
		t := time.NewTimer(opts.Timeout)
		defer t.Stop()
		timeout = t.C
	}

	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	type waited struct {
		status ExitStatus
		err    error
	}
	done := make(chan waited, 1)
	go func() {
		st, err := run.Wait(waitCtx)
		done <- waited{st, err}
	}()

	var outcome Outcome
	select {
	case w := <-done:
		if w.err != nil {
			return Result{}, fmt.Errorf("wait for %s: %w", run.ID(), w.err)
		}
		return Result{Outcome: OutcomeExited, Exit: w.status}, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-timeout:
		outcome = OutcomeTimedOut
	case <-opts.Cancel:
		outcome = OutcomeCanceled
	}

	// The stop must finish even though the caller's request that led here
	// may be gone; it is bounded by its own deadline instead.
	stopCtx, cancelStop := context.WithTimeout(context.WithoutCancel(ctx), grace+stopTimeout)
	defer cancelStop()
	stopped, err := ex.Stop(stopCtx, run.ID(), grace)
	if err != nil {
		return Result{}, fmt.Errorf("stop %s: %w", run.ID(), err)
	}
	if stopped.AlreadyExited {
		// The process won the race against the timer or the cancel request.
		outcome = OutcomeExited
	}

	// Stop confirmed the container is not running; Wait now only has to
	// finish draining output, which is quick.
	select {
	case w := <-done:
		if w.err != nil && !errors.Is(w.err, context.Canceled) {
			return Result{}, fmt.Errorf("wait for %s after stop: %w", run.ID(), w.err)
		}
		exit := stopped.Exit
		if w.err == nil {
			exit = w.status
		}
		return Result{Outcome: outcome, Exit: exit, Escalated: stopped.Escalated}, nil
	case <-stopCtx.Done():
		return Result{}, fmt.Errorf("drain output of %s: %w", run.ID(), stopCtx.Err())
	}
}
