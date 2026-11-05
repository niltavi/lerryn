// Package leader elects one server to do the background work that should
// not run on every replica: reconciling lost runs, archiving logs and,
// later, scheduling and notifications (tech design 4.4).
//
// On PostgreSQL the leader holds the "leader" row of the leases table,
// with expiry on the database clock, and renews it well before it
// expires. A server that cannot renew in time steps down on its own clock
// before the lease can pass to another server. On SQLite there is only
// one server, which leads from the start.
//
// Election avoids duplicate work; it is not what makes the work correct.
// Every leader task must stay safe when two servers briefly both believe
// they lead.
package leader

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/dialect"
)

// LeaseName is the name of the leader lease in the leases table.
const LeaseName = "leader"

// Election timing (tech design 4.4). A follower retries every
// RenewInterval, so after a leader dies another server takes over within
// TTL plus RenewInterval; after a clean shutdown, within RenewInterval.
const (
	TTL           = 15 * time.Second
	RenewInterval = 5 * time.Second
)

// Elector runs the leader work of one server process.
type Elector struct {
	db     *store.DB
	holder string
	log    *slog.Logger
	leader atomic.Bool

	ttl, renew time.Duration
}

// New returns an elector for the server process identified by holder,
// which must be unique per process, for example a fresh UUID.
func New(db *store.DB, holder string, log *slog.Logger) *Elector {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Elector{db: db, holder: holder, log: log.With("holder", holder), ttl: TTL, renew: RenewInterval}
}

// IsLeader reports whether this process currently leads.
func (e *Elector) IsLeader() bool { return e.leader.Load() }

// Run calls lead each time this process becomes the leader, with a context
// that ends when leadership is lost or ctx ends, and waits for it to
// return before competing again. Run returns when ctx ends; it then
// releases the lease so another server can take over at once.
func (e *Elector) Run(ctx context.Context, lead func(ctx context.Context)) {
	if e.db.Native().Name() == dialect.SQLiteName {
		e.leader.Store(true)
		e.log.InfoContext(ctx, "leading: single server on SQLite")
		lead(ctx)
		e.leader.Store(false)
		return
	}
	defer e.release()

	tick := time.NewTicker(e.renew)
	defer tick.Stop()
	for {
		if e.acquire(ctx) {
			e.lead(ctx, tick, lead)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// acquire tries to take or renew the lease once.
func (e *Elector) acquire(ctx context.Context) bool {
	_, ok, err := e.db.AcquireLease(ctx, LeaseName, e.holder, e.ttl)
	if err != nil {
		if ctx.Err() == nil {
			e.log.WarnContext(ctx, "leader election: lease query failed", "error", err)
		}
		return false
	}
	return ok
}

// lead runs lead while this process keeps renewing the lease. It steps
// down when another server holds the lease, or when renewal has failed
// for long enough that the lease may have expired: the deadline is kept
// on the local monotonic clock, one renewal interval short of the TTL.
func (e *Elector) lead(ctx context.Context, tick *time.Ticker, lead func(ctx context.Context)) {
	leadCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	e.leader.Store(true)
	e.log.InfoContext(ctx, "leading")
	go func() {
		defer close(done)
		lead(leadCtx)
	}()
	defer func() {
		stop()
		<-done
		e.leader.Store(false)
	}()

	renewed := time.Now()
	finished := done
	for {
		select {
		case <-ctx.Done():
			return
		case <-finished:
			// lead returned on its own; keep renewing so that no other
			// server repeats the work.
			finished = nil
			continue
		case <-tick.C:
		}
		_, ok, err := e.db.AcquireLease(ctx, LeaseName, e.holder, e.ttl)
		switch {
		case err == nil && ok:
			renewed = time.Now()
		case err == nil:
			e.log.WarnContext(ctx, "stepping down: another server holds the leader lease")
			return
		case ctx.Err() != nil:
			return
		default:
			e.log.WarnContext(ctx, "leader lease renewal failed", "error", err)
			if time.Since(renewed) >= e.ttl-e.renew {
				e.log.WarnContext(ctx, "stepping down: the leader lease could not be renewed in time")
				return
			}
		}
	}
}

// release gives up the lease after a clean shutdown.
func (e *Elector) release() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.db.ReleaseLease(ctx, LeaseName, e.holder); err != nil {
		e.log.WarnContext(ctx, "release leader lease", "error", err)
	}
}

// All returns a lead function that runs every task concurrently and
// returns when all of them have. Each task is a loop that works until its
// context ends; it is started on every election and must stop promptly
// when leadership is lost.
func All(tasks ...func(ctx context.Context)) func(ctx context.Context) {
	return func(ctx context.Context) {
		var wg sync.WaitGroup
		for _, task := range tasks {
			wg.Go(func() { task(ctx) })
		}
		wg.Wait()
	}
}
