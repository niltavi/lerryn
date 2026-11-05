package scheduler

import (
	"context"
	"log/slog"
	"time"
)

// Loop timing (tech design 8.2).
const (
	// DefaultInterval is how often the leader looks for due triggers and
	// retries.
	DefaultInterval = time.Second
	// DefaultSweepInterval is how often expired idempotency records are
	// deleted.
	DefaultSweepInterval = time.Hour
)

// Leadership tells the loop whether this process is the leader, the one
// server that runs background work (tech design 4.4). The loop asks on
// every tick, so leadership may come and go while it runs. Correctness
// does not depend on it: fires and retries are deduplicated by the
// database, so two leaders for a moment do not double any run.
type Leadership interface {
	IsLeader() bool
}

// AlwaysLeader is the Leadership of a deployment with a single server,
// such as all-in-one on SQLite: it always leads.
type AlwaysLeader struct{}

// IsLeader implements Leadership.
func (AlwaysLeader) IsLeader() bool { return true }

// Firer is the scheduling work the loop drives; *service.Triggers
// implements it. Each method is given the loop's clock reading, so that
// tests can drive it with a fake clock.
type Firer interface {
	// FireDue processes the cron triggers due by now and returns how many
	// runs, queued or skipped, it recorded.
	FireDue(ctx context.Context, now time.Time) (int, error)
	// RetryFailed queues the automatic retries of ended attempts and
	// returns how many it recorded.
	RetryFailed(ctx context.Context, now time.Time) (int, error)
	// Sweep deletes expired bookkeeping such as idempotency records.
	Sweep(ctx context.Context, now time.Time) error
}

// Clock is the loop's time source.
type Clock interface {
	Now() time.Time
	// After returns a channel that receives once d has passed.
	After(d time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now().UTC().Truncate(time.Microsecond) }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Options configures a Scheduler. Zero values take the defaults.
type Options struct {
	Interval      time.Duration
	SweepInterval time.Duration
	// Leader defaults to AlwaysLeader.
	Leader Leadership
	// Clock defaults to the system clock in UTC.
	Clock  Clock
	Logger *slog.Logger
}

// Scheduler is the scheduling loop. It does nothing on servers that are
// not the leader.
type Scheduler struct {
	firer     Firer
	interval  time.Duration
	sweep     time.Duration
	leader    Leadership
	clock     Clock
	log       *slog.Logger
	lastSweep time.Time
}

// New returns a scheduler that drives f.
func New(f Firer, opts Options) *Scheduler {
	s := &Scheduler{firer: f, interval: opts.Interval, sweep: opts.SweepInterval, leader: opts.Leader, clock: opts.Clock, log: opts.Logger}
	if s.interval <= 0 {
		s.interval = DefaultInterval
	}
	if s.sweep <= 0 {
		s.sweep = DefaultSweepInterval
	}
	if s.leader == nil {
		s.leader = AlwaysLeader{}
	}
	if s.clock == nil {
		s.clock = systemClock{}
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	return s
}

// Run ticks every interval until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.clock.After(s.interval):
		}
	}
}

// Tick does one round of work if this process is the leader: fire due
// triggers, queue retries, and sweep when a sweep interval has passed.
// Errors are logged, not returned: the next tick tries again.
func (s *Scheduler) Tick(ctx context.Context) {
	if !s.leader.IsLeader() {
		return
	}
	now := s.clock.Now()
	if n, err := s.firer.FireDue(ctx, now); err != nil {
		s.logError(ctx, "fire due triggers", err)
	} else if n > 0 {
		s.log.DebugContext(ctx, "fired triggers", "runs", n)
	}
	if n, err := s.firer.RetryFailed(ctx, now); err != nil {
		s.logError(ctx, "queue retries", err)
	} else if n > 0 {
		s.log.DebugContext(ctx, "queued retries", "runs", n)
	}
	if now.Sub(s.lastSweep) >= s.sweep {
		if err := s.firer.Sweep(ctx, now); err != nil {
			s.logError(ctx, "sweep", err)
		} else {
			s.lastSweep = now
		}
	}
}

func (s *Scheduler) logError(ctx context.Context, what string, err error) {
	if ctx.Err() != nil {
		return
	}
	s.log.ErrorContext(ctx, "scheduler step failed", "step", what, "error", err)
}
