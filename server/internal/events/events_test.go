package events_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/events"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

func receive(t *testing.T, ch <-chan events.Event, want events.Event) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e := <-ch:
			if e == want {
				return
			}
		case <-timeout:
			t.Fatalf("did not receive %+v", want)
		}
	}
}

func TestLocal(t *testing.T) {
	bus := events.NewLocal()
	a, stopA := bus.Subscribe()
	b, stopB := bus.Subscribe()
	e := events.Event{Kind: events.RunEnded, RunID: uuid.New()}
	if err := bus.Publish(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	receive(t, a, e)
	receive(t, b, e)
	stopB()
	stopB() // idempotent

	// A subscriber that does not read misses events instead of blocking
	// the publisher.
	for range 1000 {
		_ = bus.Publish(context.Background(), e)
	}
	stopA()
}

func TestPostgresAcrossServers(t *testing.T) {
	if storetest.Driver(t) != config.DatabasePostgres {
		t.Skip("LISTEN/NOTIFY needs PostgreSQL; run with make test-pg")
	}
	cfg := storetest.NewDatabase(t)
	open := func() *store.DB {
		db, err := store.Open(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	newBus := func() *events.Postgres {
		bus, err := events.NewPostgres(ctx, open(), cfg.DSN, nil)
		if err != nil {
			t.Fatal(err)
		}
		go bus.Run(ctx)
		select {
		case <-bus.Ready():
		case <-time.After(10 * time.Second):
			t.Fatal("bus did not start listening")
		}
		return bus
	}
	a, b := newBus(), newBus()
	fromA, stop := b.Subscribe()
	defer stop()
	own, stopOwn := a.Subscribe()
	defer stopOwn()

	e := events.Event{Kind: events.RunQueued, RunID: uuid.New()}
	if err := a.Publish(ctx, e); err != nil {
		t.Fatal(err)
	}
	receive(t, fromA, e)
	receive(t, own, e)

	// Another schema is another deployment and hears nothing.
	otherCfg := storetest.NewDatabase(t)
	otherDB, err := store.Open(ctx, otherCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = otherDB.Close() }()
	other, err := events.NewPostgres(ctx, otherDB, otherCfg.DSN, nil)
	if err != nil {
		t.Fatal(err)
	}
	go other.Run(ctx)
	<-other.Ready()
	quiet, stopQuiet := other.Subscribe()
	defer stopQuiet()
	if err := a.Publish(ctx, events.Event{Kind: events.RunEnded, RunID: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-quiet:
		t.Fatalf("another schema received %+v", e)
	case <-time.After(300 * time.Millisecond):
	}
}
