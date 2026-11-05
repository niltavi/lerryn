package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/store/dialect"
)

const (
	blobBackupPrefix        = "blob-backup:"
	blobBackupTTL           = 30 * time.Second
	blobBackupRenewInterval = 10 * time.Second
	blobBackupReleaseWait   = 5 * time.Second
)

// ErrBlobBackupExpired means that a backup can no longer rely on object
// collection remaining suspended. Such a backup must not be published.
var ErrBlobBackupExpired = errors.New("blob backup protection expired")

// BlobBackupPin keeps SQLite object collection suspended while an online
// backup copies a database snapshot and the objects it references.
type BlobBackupPin struct {
	db           *DB
	name, holder string
	ttl          time.Duration
	ctx          context.Context
	cancel       context.CancelCauseFunc
	done         chan struct{}
	mu           sync.Mutex
	expiresAt    time.Time
	closeOnce    sync.Once
	closeErr     error
}

// PinBlobs registers a distinct, renewable SQLite lease before a backup
// takes its database snapshot. Lease writes serialize with the collector's
// BEGIN IMMEDIATE transaction, even through another DB or process; the
// collector must call BlobBackupsActive inside that same transaction.
func (db *DB) PinBlobs(ctx context.Context) (*BlobBackupPin, error) {
	return db.pinBlobs(ctx, blobBackupTTL, blobBackupRenewInterval)
}

func (db *DB) pinBlobs(ctx context.Context, ttl, interval time.Duration) (*BlobBackupPin, error) {
	if db.Native().Name() != dialect.SQLiteName {
		return nil, errors.New("blob backup protection requires SQLite")
	}
	if ttl <= 0 || interval <= 0 || interval >= ttl {
		return nil, errors.New("blob backup protection requires a positive renewal interval shorter than its lifetime")
	}
	holder := NewID().String()
	name := blobBackupPrefix + NewID().String()
	acquireCtx, cancelAcquire := context.WithTimeout(ctx, ttl)
	expiresAt, ok, err := db.AcquireLease(acquireCtx, name, holder, ttl)
	cancelAcquire()
	if err != nil {
		return nil, fmt.Errorf("pin backup objects: %w", err)
	}
	if !ok {
		return nil, errors.New("pin backup objects: lease is already held")
	}
	pinCtx, cancel := context.WithCancelCause(ctx)
	p := &BlobBackupPin{
		db: db, name: name, holder: holder, ttl: ttl, ctx: pinCtx, cancel: cancel,
		done: make(chan struct{}), expiresAt: expiresAt,
	}
	go p.renewLoop(interval)
	if err := p.Check(); err != nil {
		return nil, errors.Join(err, p.Close())
	}
	return p, nil
}

// Context ends when the caller cancels, renewal fails, protection expires
// or Close is called. Backup work must use this context.
func (p *BlobBackupPin) Context() context.Context { return p.ctx }

// Check also checks expiry synchronously. After a long process pause the
// backup may resume before the renewal goroutine; it must still fail.
func (p *BlobBackupPin) Check() error {
	p.mu.Lock()
	if !time.Now().Before(p.expiresAt) {
		p.cancel(ErrBlobBackupExpired)
	}
	p.mu.Unlock()
	return context.Cause(p.ctx)
}

func (p *BlobBackupPin) renewLoop(interval time.Duration) {
	defer close(p.done)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-tick.C:
			if err := p.renew(); err != nil {
				p.cancel(err)
				return
			}
		}
	}
}

func (p *BlobBackupPin) renew() error {
	if err := p.Check(); err != nil {
		return err
	}
	p.mu.Lock()
	previous := p.expiresAt
	p.mu.Unlock()
	ctx, cancel := context.WithDeadline(p.ctx, previous)
	defer cancel()
	expiresAt, ok, err := p.db.AcquireLease(ctx, p.name, p.holder, p.ttl)
	if err != nil {
		return fmt.Errorf("renew backup object protection: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !ok || !time.Now().Before(previous) || !time.Now().Before(expiresAt) {
		return ErrBlobBackupExpired
	}
	if err := context.Cause(p.ctx); err != nil {
		return err
	}
	p.expiresAt = expiresAt
	return nil
}

// Close stops renewal, waits for it to finish, then releases protection
// with its own bounded context even when the backup was cancelled.
func (p *BlobBackupPin) Close() error {
	p.closeOnce.Do(func() {
		p.cancel(context.Canceled)
		<-p.done
		ctx, cancel := context.WithTimeout(context.WithoutCancel(p.ctx), blobBackupReleaseWait)
		defer cancel()
		p.closeErr = p.db.ReleaseLease(ctx, p.name, p.holder)
	})
	return p.closeErr
}

// BlobBackupsActive reports live SQLite backup pins. A collector calls it
// inside its write transaction before deleting any object; checking on a
// separate connection would leave a gap before the deletion.
func (db *DB) BlobBackupsActive(ctx context.Context, idb bun.IDB) (bool, error) {
	if db.Native().Name() != dialect.SQLiteName {
		return false, nil
	}
	if idb == nil {
		idb = db
	}
	active, err := idb.NewSelect().Model((*Lease)(nil)).
		Where("name LIKE ? AND expires_at > ?", blobBackupPrefix+"%", Now()).Exists(ctx)
	if err != nil {
		return false, fmt.Errorf("check backup object protection: %w", err)
	}
	return active, nil
}
