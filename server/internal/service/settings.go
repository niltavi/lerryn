package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/store"
)

// settingsTTL is how long a replica trusts its copy of the settings. A
// change made on one replica applies there at once and on the others
// within this time (tech design 12.1: "立即生效", consistent across
// replicas within seconds).
const settingsTTL = 3 * time.Second

// DeploymentSettings are the settings an admin changes at runtime.
type DeploymentSettings struct {
	OpenRegistration   bool
	LocalPasswordLogin bool
	// UpdatedAt and UpdatedBy describe the latest change; zero when the
	// defaults were never changed.
	UpdatedAt time.Time
	UpdatedBy uuid.UUID
}

// defaultSettings are the values of a new deployment.
func defaultSettings() DeploymentSettings {
	return DeploymentSettings{OpenRegistration: true, LocalPasswordLogin: true}
}

// settingsCache reads the settings table with a short cache, so checks on
// the login path do not query the database on every request.
type settingsCache struct {
	db  *store.DB
	now func() time.Time

	mu      sync.Mutex
	value   DeploymentSettings
	fetched time.Time
}

func newSettingsCache(db *store.DB) *settingsCache {
	return &settingsCache{db: db, now: time.Now}
}

// get returns the current settings.
func (c *settingsCache) get(ctx context.Context) (DeploymentSettings, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.fetched.IsZero() && c.now().Sub(c.fetched) < settingsTTL {
		return c.value, nil
	}
	v, err := loadSettings(ctx, c.db)
	if err != nil {
		return DeploymentSettings{}, err
	}
	c.value, c.fetched = v, c.now()
	return v, nil
}

// invalidate drops the cached copy after a change on this replica.
func (c *settingsCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetched = time.Time{}
}

func loadSettings(ctx context.Context, idb bun.IDB) (DeploymentSettings, error) {
	var rows []store.Setting
	if err := idb.NewSelect().Model(&rows).Scan(ctx); err != nil {
		return DeploymentSettings{}, fmt.Errorf("load settings: %w", err)
	}
	s := defaultSettings()
	for _, r := range rows {
		var target *bool
		switch r.Key {
		case store.SettingOpenRegistration:
			target = &s.OpenRegistration
		case store.SettingLocalPasswordLogin:
			target = &s.LocalPasswordLogin
		default:
			continue
		}
		if err := json.Unmarshal([]byte(r.Value), target); err != nil {
			return DeploymentSettings{}, fmt.Errorf("setting %s: %w", r.Key, err)
		}
		if r.UpdatedAt.After(s.UpdatedAt) {
			s.UpdatedAt, s.UpdatedBy = r.UpdatedAt, r.UpdatedBy
		}
	}
	return s, nil
}

// putSetting writes one setting in tx.
func putSetting(ctx context.Context, tx bun.IDB, key string, value any, by uuid.UUID, now time.Time) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var existing store.Setting
	err = tx.NewSelect().Model(&existing).Where("key = ?", key).Scan(ctx)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.NewInsert().Model(&store.Setting{
			ID: store.NewID(), Key: key, Value: string(b), UpdatedBy: by, UpdatedAt: now,
		}).Exec(ctx)
		return err
	case err != nil:
		return err
	}
	_, err = tx.NewUpdate().Model((*store.Setting)(nil)).
		Set("value = ?", string(b)).Set("updated_by = ?", by).Set("updated_at = ?", now).
		Where("id = ?", existing.ID).Exec(ctx)
	return err
}
