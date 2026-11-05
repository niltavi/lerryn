package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/store/dialect"
)

func blobBackupDB(t *testing.T) (*DB, string) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "lerryn.db")
	db, err := Open(t.Context(), config.Database{Driver: config.DatabaseSQLite, DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	return db, dsn
}

func checkBlobBackups(t *testing.T, db *DB, want bool) {
	t.Helper()
	if active, err := db.BlobBackupsActive(t.Context(), db); err != nil || active != want {
		t.Fatalf("BlobBackupsActive = %v, %v; want %v", active, err, want)
	}
}

func TestBlobBackupPinsAreSharedAndIndependent(t *testing.T) {
	db, dsn := blobBackupDB(t)
	other, err := Open(t.Context(), config.Database{Driver: config.DatabaseSQLite, DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	checkBlobBackups(t, other, false)
	first, err := db.PinBlobs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := other.PinBlobs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if first.name == second.name {
		t.Fatal("concurrent backups share a lease")
	}
	checkBlobBackups(t, db, true)
	checkBlobBackups(t, other, true)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	checkBlobBackups(t, db, true)
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	checkBlobBackups(t, other, false)
}

func TestBlobBackupRenewsBeforeExpiry(t *testing.T) {
	db, _ := blobBackupDB(t)
	const ttl = 500 * time.Millisecond
	pin, err := db.pinBlobs(t.Context(), ttl, ttl/10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pin.Close() })
	pin.mu.Lock()
	initial := pin.expiresAt
	pin.mu.Unlock()
	timer := time.NewTimer(time.Until(initial) + 100*time.Millisecond)
	defer timer.Stop()
	select {
	case <-pin.Context().Done():
		t.Fatalf("backup pin ended during renewal: %v", pin.Check())
	case <-timer.C:
	}
	if err := pin.Check(); err != nil {
		t.Fatal(err)
	}
	var lease Lease
	if err := db.NewSelect().Model(&lease).Where("name = ?", pin.name).Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !lease.ExpiresAt.After(initial) {
		t.Fatalf("lease was not renewed: initial %s, current %s", initial, lease.ExpiresAt)
	}
	checkBlobBackups(t, db, true)
}

func TestBlobBackupCancellationStillReleasesLease(t *testing.T) {
	db, _ := blobBackupDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	pin, err := db.PinBlobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pin.Close() })
	cancel()
	<-pin.Context().Done()
	if err := pin.Check(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Check = %v, want context cancellation", err)
	}
	if err := pin.Close(); err != nil {
		t.Fatalf("Close after cancellation: %v", err)
	}
	checkBlobBackups(t, db, false)
}

func TestBlobBackupCheckRejectsExpiredLease(t *testing.T) {
	db, _ := blobBackupDB(t)
	pin, err := db.pinBlobs(t.Context(), time.Minute, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pin.Close() })
	// Simulate a process resuming after its lease expired, before the
	// renewal goroutine gets to run. A synchronous check must reject it.
	pin.mu.Lock()
	pin.expiresAt = time.Now().Add(-time.Second)
	pin.mu.Unlock()
	if err := pin.Check(); !errors.Is(err, ErrBlobBackupExpired) {
		t.Fatalf("Check = %v, want expired protection", err)
	}
	if err := pin.renew(); !errors.Is(err, ErrBlobBackupExpired) {
		t.Fatalf("expired renewal = %v, want expired protection", err)
	}
	select {
	case <-pin.Context().Done():
	default:
		t.Fatal("expired pin did not cancel backup work")
	}
}

type lateBlobBackupRenewal struct{ delayed bool }

func (h *lateBlobBackupRenewal) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (h *lateBlobBackupRenewal) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
	if event.Err == nil && strings.Contains(event.Query, "INSERT INTO leases") {
		h.delayed = true
		// The database renewed the lease, but the caller resumes only
		// after its old lease's deadline. That gap must invalidate it.
		<-ctx.Done()
	}
}

func TestBlobBackupRejectsRenewalReturningAfterPreviousExpiry(t *testing.T) {
	db, _ := blobBackupDB(t)
	pin, err := db.pinBlobs(t.Context(), time.Minute, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pin.Close() })
	hook := &lateBlobBackupRenewal{}
	db.DB = db.WithQueryHook(hook)
	pin.mu.Lock()
	pin.expiresAt = time.Now().Add(100 * time.Millisecond)
	pin.mu.Unlock()
	if err := pin.renew(); !errors.Is(err, ErrBlobBackupExpired) {
		t.Fatalf("late renewal = %v, want expired protection", err)
	}
	if !hook.delayed {
		t.Fatal("test did not delay a successful lease renewal")
	}
	if err := pin.Check(); !errors.Is(err, ErrBlobBackupExpired) {
		t.Fatalf("Check after late renewal = %v, want expired protection", err)
	}
}

func TestBlobBackupRenewalFailureCancelsWork(t *testing.T) {
	db, _ := blobBackupDB(t)
	pin, err := db.pinBlobs(t.Context(), time.Second, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pin.Close() })
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pin.Context().Done():
		if err := pin.Check(); err == nil {
			t.Fatal("renewal failure did not invalidate the backup")
		}
	case <-time.After(time.Second):
		t.Fatal("renewal failure did not cancel backup work")
	}
	if err := pin.Close(); err == nil {
		t.Fatal("Close did not report the failed lease release")
	}
}

func TestBlobBackupPinSerializesWithCollectorTransaction(t *testing.T) {
	db, dsn := blobBackupDB(t)
	other, err := Open(t.Context(), config.Database{Driver: config.DatabaseSQLite, DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	tx, err := other.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if active, err := other.BlobBackupsActive(t.Context(), tx); err != nil || active {
		t.Fatalf("before pin: active = %v, %v", active, err)
	}
	type result struct {
		pin *BlobBackupPin
		err error
	}
	acquired := make(chan result, 1)
	go func() {
		pin, err := db.PinBlobs(t.Context())
		acquired <- result{pin, err}
	}()
	select {
	case got := <-acquired:
		if got.pin != nil {
			_ = got.pin.Close()
		}
		t.Fatalf("pin acquisition passed an open SQLite write transaction: %v", got.err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got := <-acquired
	if got.err != nil {
		t.Fatal(got.err)
	}
	t.Cleanup(func() { _ = got.pin.Close() })
	if err := other.WriteTx(t.Context(), func(ctx context.Context, tx bun.Tx) error {
		active, err := other.BlobBackupsActive(ctx, tx)
		if err == nil && !active {
			return errors.New("collector transaction cannot see the acquired backup pin")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBlobBackupIgnoresExpiredRows(t *testing.T) {
	db, _ := blobBackupDB(t)
	lease := Lease{ID: NewID(), Name: blobBackupPrefix + "old", Holder: "stopped", ExpiresAt: Now().Add(-time.Second)}
	if _, err := db.NewInsert().Model(&lease).Exec(t.Context()); err != nil {
		t.Fatal(err)
	}
	checkBlobBackups(t, db, false)
}

func TestBlobBackupRejectsUnsupportedDatabaseAndTiming(t *testing.T) {
	pg := &DB{dialect: dialect.Postgres{}}
	if _, err := pg.PinBlobs(t.Context()); err == nil {
		t.Fatal("PinBlobs accepted PostgreSQL")
	}
	checkBlobBackups(t, pg, false)
	db, _ := blobBackupDB(t)
	for _, timing := range [][2]time.Duration{{0, time.Second}, {time.Second, 0}, {time.Second, time.Second}} {
		if _, err := db.pinBlobs(t.Context(), timing[0], timing[1]); err == nil {
			t.Fatalf("pinBlobs accepted ttl/interval %v", timing)
		}
	}
}
