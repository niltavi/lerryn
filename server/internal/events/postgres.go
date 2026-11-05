package events

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/niltavi/lerryn/server/internal/store"
)

// Reconnect back-off of the listening connection.
const (
	listenRetryMin = 250 * time.Millisecond
	listenRetryMax = 10 * time.Second
)

// Postgres is a Bus across every server on one PostgreSQL database, built
// on LISTEN/NOTIFY (tech design 4.4). Publish sends a NOTIFY through the
// connection pool and also delivers locally at once, so this process does
// not depend on the round trip; a receiver may therefore see its own
// events twice, which the event kinds tolerate. Run holds one dedicated
// connection that listens and forwards what other servers publish.
//
// Notifications sent while the listening connection is down are lost.
// Receivers poll as well, so the loss costs latency only.
type Postgres struct {
	local   *Local
	db      *store.DB
	dsn     string
	channel string
	log     *slog.Logger
	// ready is closed once the first LISTEN succeeded.
	ready chan struct{}
}

var _ Bus = (*Postgres)(nil)

// NewPostgres returns a bus on db, which must be a PostgreSQL database
// opened from dsn. The channel name is derived from the current schema, so
// deployments sharing a database in different schemas do not hear each
// other. Call Run to receive events from other servers.
func NewPostgres(ctx context.Context, db *store.DB, dsn string, log *slog.Logger) (*Postgres, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	var schema string
	if err := db.NewRaw("SELECT current_schema()").Scan(ctx, &schema); err != nil {
		return nil, fmt.Errorf("event bus: read current schema: %w", err)
	}
	return &Postgres{
		local:   NewLocal(),
		db:      db,
		dsn:     dsn,
		channel: channelName(schema),
		log:     log,
		ready:   make(chan struct{}),
	}, nil
}

// channelName returns a channel name that is a plain lowercase identifier,
// so it needs no quoting rules beyond double quotes.
func channelName(schema string) string {
	sum := sha256.Sum256([]byte("lerryn-events:" + schema))
	return "lerryn_events_" + hex.EncodeToString(sum[:8])
}

// Publish implements Bus. A failed NOTIFY is returned after the local
// delivery; callers log it and rely on other servers' polling.
func (p *Postgres) Publish(ctx context.Context, e Event) error {
	p.local.deliver(e)
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("event bus: encode event: %w", err)
	}
	if _, err := p.db.NewRaw("SELECT pg_notify(?, ?)", p.channel, string(payload)).Exec(ctx); err != nil {
		return fmt.Errorf("event bus: notify: %w", err)
	}
	return nil
}

// Subscribe implements Bus.
func (p *Postgres) Subscribe() (<-chan Event, func()) { return p.local.Subscribe() }

// Ready is closed once the bus first listens for other servers' events.
func (p *Postgres) Ready() <-chan struct{} { return p.ready }

// Run listens for events from other servers until ctx ends, reconnecting
// with back-off when the connection drops.
func (p *Postgres) Run(ctx context.Context) {
	delay := listenRetryMin
	for ctx.Err() == nil {
		err := p.listen(ctx, func() { delay = listenRetryMin })
		if ctx.Err() != nil {
			return
		}
		p.log.WarnContext(ctx, "event bus connection lost; reconnecting", "error", err, "retry_in", delay)
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		delay = min(delay*2, listenRetryMax)
	}
}

// listen holds one connection on the channel and forwards notifications
// until the connection fails or ctx ends. connected is called after
// LISTEN succeeds.
func (p *Postgres) listen(ctx context.Context, connected func()) error {
	cfg, err := pgx.ParseConfig(p.dsn)
	if err != nil {
		return fmt.Errorf("parse postgres dsn: %w", err)
	}
	cfg.RuntimeParams["application_name"] = "lerryn-events"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	// The channel name is built from hex digits only.
	if _, err := conn.Exec(ctx, `LISTEN "`+p.channel+`"`); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	connected()
	select {
	case <-p.ready:
	default:
		close(p.ready)
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var e Event
		if err := json.Unmarshal([]byte(n.Payload), &e); err != nil || e.Kind == "" {
			p.log.WarnContext(ctx, "event bus: ignoring a malformed notification", "bytes", len(n.Payload))
			continue
		}
		p.local.deliver(e)
	}
}
