package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/events"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

// fixtureOn is a fixture on an open database, with runs from the caller.
func fixtureOn(t *testing.T, db *store.DB, runs *service.Runs) *fixture {
	t.Helper()
	blobs, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authz := service.NewAuthorizer(db)
	return &fixture{
		t: t, ctx: context.Background(), db: db, blobs: blobs,
		accounts: service.NewAccounts(db, nil),
		tasks:    service.NewTasks(db, authz, blobs, nil),
		runs:     runs,
		coord:    service.NewCoordinator(db, blobs, runs, nil),
	}
}

// nextUpdate waits for the next update of run on s, skipping others.
func nextUpdate(t *testing.T, s *service.UpdateStream, run uuid.UUID, status string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		u, _, err := s.Next(context.Background(), time.Until(deadline))
		if err != nil {
			t.Fatal(err)
		}
		if u != nil && u.RunID == run && u.Status == status {
			return
		}
	}
	t.Fatalf("no %s update of run %s", status, run)
}

// On one server, the stream gets each change once even though the
// process also hears its own events, and runs queued by triggers (which
// announce them only with RunQueued) reach it through the bus.
func TestUpdateStreamLocalBus(t *testing.T) {
	db := storetest.Open(t)
	bus := events.NewLocal()
	runs := service.NewRuns(db, service.NewAuthorizer(db), nil).WithEvents(bus, false)
	f := fixtureOn(t, db, runs)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	go runs.Listen(ctx)

	alice := f.user("alice")
	allow := basicManifest()
	allow.Concurrency = service.ConcurrencyAllow
	task := f.apply(alice, "mine", allow).Task.ID
	s, err := runs.StreamUpdates(f.ctx, alice, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	run := f.start(alice, task)
	nextUpdate(t, s, run.ID, store.RunQueued)
	if u, _, _ := s.Next(f.ctx, 200*time.Millisecond); u != nil {
		t.Fatalf("duplicate update %+v", u)
	}

	if err := bus.Publish(f.ctx, events.Event{Kind: events.RunQueued, RunID: run.ID}); err != nil {
		t.Fatal(err)
	}
	nextUpdate(t, s, run.ID, store.RunQueued)
}

// With the PostgreSQL bus, a stream on one server reports changes made on
// another (tech design 4.4).
func TestUpdateStreamAcrossServers(t *testing.T) {
	if storetest.Driver(t) != config.DatabasePostgres {
		t.Skip("LISTEN/NOTIFY needs PostgreSQL; run with make test-pg")
	}
	cfg := storetest.NewDatabase(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := func(migrate bool) (*store.DB, *service.Runs) {
		db, err := store.Open(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if migrate {
			if _, err := store.Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
		}
		bus, err := events.NewPostgres(ctx, db, cfg.DSN, nil)
		if err != nil {
			t.Fatal(err)
		}
		go bus.Run(ctx)
		select {
		case <-bus.Ready():
		case <-time.After(10 * time.Second):
			t.Fatal("bus did not start listening")
		}
		runs := service.NewRuns(db, service.NewAuthorizer(db), nil).WithEvents(bus, true)
		go runs.Listen(ctx)
		return db, runs
	}
	dbA, runsA := server(true)
	_, runsB := server(false)
	a := fixtureOn(t, dbA, runsA)

	alice, bob := a.user("alice"), a.user("bob")
	allow := basicManifest()
	allow.Concurrency = service.ConcurrencyAllow
	mine := a.apply(alice, "mine", allow).Task.ID
	theirs := a.apply(bob, "theirs", allow).Task.ID

	s, err := runsB.StreamUpdates(ctx, alice, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a.start(bob, theirs)
	run := a.start(alice, mine)
	nextUpdate(t, s, run.ID, store.RunQueued)
	a.claim(a.runner("r1"), 64)
	nextUpdate(t, s, run.ID, store.RunPreparing)
	// Bob's run never shows up on alice's stream.
	for {
		u, _, err := s.Next(ctx, 300*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if u == nil {
			break
		}
		if u.ProjectID != alice.PersonalProjectID {
			t.Fatalf("alice saw bob's run: %+v", u)
		}
	}
}
