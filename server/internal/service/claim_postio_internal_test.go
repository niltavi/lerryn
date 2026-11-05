package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

func TestClaimToolReadMissingAndContextFailure(t *testing.T) {
	db := storetest.Open(t)
	coord := NewCoordinator(db, nil, nil, nil)
	changed, err := coord.failClaimedToolRead(t.Context(), "runner", store.NewID())
	if changed || err != nil {
		t.Fatalf("missing run: changed=%t err=%v", changed, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	changed, err = coord.failClaimedToolRead(ctx, "runner", store.NewID())
	if changed || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled transaction: changed=%t err=%v", changed, err)
	}
}

func TestClaimToolReadDatabaseFailure(t *testing.T) {
	// An unmigrated database has no runs table: this must remain a real
	// storage error rather than looking like a missing or ended run.
	db := storetest.OpenEmpty(t)
	coord := NewCoordinator(db, nil, nil, nil)
	changed, err := coord.failClaimedToolRead(t.Context(), "runner", store.NewID())
	if changed || err == nil || errors.Is(err, runproto.ErrLeaseLost) || errors.Is(err, context.Canceled) {
		t.Fatalf("database failure: changed=%t err=%v", changed, err)
	}
}

// Connection failures exercise WriteTx's real error path without changing
// production transaction hooks or requiring a second database connection.
type claimErrorConnector struct {
	err    error
	cancel context.CancelFunc
}

func (c claimErrorConnector) Connect(context.Context) (driver.Conn, error) {
	if c.cancel != nil {
		c.cancel()
	}
	return nil, c.err
}

func (c claimErrorConnector) Driver() driver.Driver { return c }
func (c claimErrorConnector) Open(string) (driver.Conn, error) {
	return c.Connect(context.Background())
}

func TestClaimToolReadLifecycleErrorClassification(t *testing.T) {
	dbErr := errors.New("synthetic database connection failure")
	for _, test := range []struct {
		name   string
		err    error
		cancel bool
		want   error
	}{
		{name: "bare lease loss", err: runproto.ErrLeaseLost},
		{name: "wrapped lease loss", err: fmt.Errorf("database: %w", runproto.ErrLeaseLost), want: runproto.ErrLeaseLost},
		{name: "joined database failure", err: errors.Join(runproto.ErrLeaseLost, dbErr), want: dbErr},
		{name: "joined cancellation", err: errors.Join(runproto.ErrLeaseLost, context.Canceled), want: context.Canceled},
		{name: "lease loss with canceled caller", err: runproto.ErrLeaseLost, cancel: true, want: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			connector := claimErrorConnector{err: test.err}
			if test.cancel {
				connector.cancel = cancel
			}
			db := bun.NewDB(sql.OpenDB(connector), sqlitedialect.New())
			t.Cleanup(func() { _ = db.Close() })
			coord := NewCoordinator(&store.DB{DB: db}, nil, nil, nil)
			changed, err := coord.failClaimedToolRead(ctx, "runner", store.NewID())
			if changed || test.want == nil && err != nil || test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("connection error classification: changed=%t err=%v want=%v", changed, err, test.want)
			}
			if test.want != nil && !errors.Is(err, runproto.ErrLeaseLost) {
				t.Fatal("the original lease error was lost while preserving its independent failure")
			}
		})
	}
}
