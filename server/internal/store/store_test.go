package store_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

// These are storage contract tests: each runs unchanged on the database
// selected by LERRYN_TEST_DATABASE. Dialect-specific expectations are
// spelled out with an explicit branch.

func TestOpenRejectsUnknownDriver(t *testing.T) {
	_, err := store.Open(context.Background(), config.Database{Driver: "mysql", DSN: "x"})
	if err == nil || !strings.Contains(err.Error(), "mysql") {
		t.Fatalf("err = %v, want an unknown driver error", err)
	}
}

func TestSQLiteSettings(t *testing.T) {
	if storetest.Driver(t) != config.DatabaseSQLite {
		t.Skip("SQLite only")
	}
	db := storetest.OpenEmpty(t)
	ctx := context.Background()

	var journalMode string
	var foreignKeys, busyTimeout int
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" || foreignKeys != 1 || busyTimeout <= 0 {
		t.Fatalf("journal_mode=%s foreign_keys=%d busy_timeout=%d", journalMode, foreignKeys, busyTimeout)
	}
}

func TestSQLiteRejectsUnsafeOverrides(t *testing.T) {
	if storetest.Driver(t) != config.DatabaseSQLite {
		t.Skip("SQLite only")
	}
	path := storetest.NewDatabase(t).DSN
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{"foreign keys off", path + "?_pragma=foreign_keys(0)", "foreign_keys"},
		{"deferred transactions", path + "?_txlock=deferred", "_txlock"},
		{"in-memory database", ":memory:", "journal_mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := store.Open(context.Background(), config.Database{Driver: config.DatabaseSQLite, DSN: tt.dsn})
			if err == nil {
				_ = db.Close()
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %s", err, tt.want)
			}
		})
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()
	db := storetest.OpenEmpty(t)
	ctx := context.Background()

	if err := store.CheckMigrations(ctx, db); err == nil {
		t.Fatal("CheckMigrations on an empty database should fail")
	}
	first, err := store.Migrate(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Applied) == 0 || first.Version != first.Applied[len(first.Applied)-1].Version {
		t.Fatalf("first run: %+v", first)
	}
	second, err := store.Migrate(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Applied) != 0 || second.Version != first.Version {
		t.Fatalf("second run: %+v, want nothing applied at version %d", second, first.Version)
	}
	if err := store.CheckMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateConcurrently(t *testing.T) {
	t.Parallel()
	cfg := storetest.NewDatabase(t)
	ctx := context.Background()

	// Several servers starting at once against an empty database.
	const servers = 4
	var wg sync.WaitGroup
	errs := make([]error, servers)
	for i := range servers {
		wg.Go(func() {
			db, err := store.Open(ctx, cfg)
			if err != nil {
				errs[i] = err
				return
			}
			defer func() { _ = db.Close() }()
			_, errs[i] = store.Migrate(ctx, db)
		})
	}
	wg.Wait()

	failed := 0
	for _, err := range errs {
		if err != nil {
			failed++
			t.Logf("migrator error: %v", err)
		}
	}
	// PostgreSQL serialises migrators under an advisory lock, so all
	// succeed. SQLite has a single server by design; racing migrators may
	// fail, but never leave a half-applied schema.
	if cfg.Driver == config.DatabasePostgres && failed > 0 {
		t.Fatalf("%d of %d concurrent migrations failed", failed, servers)
	}
	if failed == servers {
		t.Fatal("every migrator failed")
	}
	db, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := store.CheckMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationsRollBack(t *testing.T) {
	t.Parallel()
	db := storetest.Open(t)
	ctx := context.Background()
	if err := store.ResetMigrations(ctx, db); err != nil {
		t.Fatalf("down migrations: %v", err)
	}
	if _, err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("up again after down: %v", err)
	}
}

func TestMigrateRefusesNewerSchema(t *testing.T) {
	t.Parallel()
	db := storetest.Open(t)
	ctx := context.Background()
	// A version a future binary would have applied.
	if _, err := db.NewRaw("INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, ?)", int64(99991231235959), true).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Migrate(ctx, db); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("Migrate err = %v, want a newer-schema error", err)
	}
	if err := store.CheckMigrations(ctx, db); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("CheckMigrations err = %v, want a newer-schema error", err)
	}
}

func TestUserRoundTrip(t *testing.T) {
	t.Parallel()
	db := storetest.Open(t)
	ctx := context.Background()

	now := store.Now()
	want := store.User{
		ID:           store.NewID(),
		Username:     "Alice",
		DisplayName:  "Alice Liu",
		PasswordHash: strings.Repeat("h", 32), // stands in for an Argon2id hash
		IsAdmin:      true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if _, err := db.NewInsert().Model(&want).Exec(ctx); err != nil {
		t.Fatal(err)
	}

	// Lookups are case-insensitive through the lower(username) index.
	var got store.User
	err := db.NewSelect().Model(&got).Where("lower(username) = lower(?)", "ALICE").Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Username != "Alice" || got.DisplayName != want.DisplayName ||
		got.PasswordHash != want.PasswordHash || !got.IsAdmin || got.Email != "" {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if !got.CreatedAt.Equal(now) || !got.DisabledAt.IsZero() {
		t.Fatalf("times: created_at=%v (want %v) disabled_at=%v", got.CreatedAt, now, got.DisabledAt)
	}

	// Optional columns are NULL in the database, not empty strings.
	var nulls int
	err = db.NewSelect().Model((*store.User)(nil)).ColumnExpr("count(*)").
		Where("email IS NULL AND disabled_at IS NULL").Scan(ctx, &nulls)
	if err != nil {
		t.Fatal(err)
	}
	if nulls != 1 {
		t.Fatalf("rows with NULL email and disabled_at = %d, want 1", nulls)
	}
}

func TestUsernameUniqueIgnoringCase(t *testing.T) {
	t.Parallel()
	db := storetest.Open(t)
	ctx := context.Background()

	insertUser(t, db, "bob")
	dup := newUser("BOB")
	_, err := db.NewInsert().Model(&dup).Exec(ctx)
	if !store.IsUniqueViolation(err) {
		t.Fatalf("err = %v, want a unique violation", err)
	}
	if store.IsForeignKeyViolation(err) {
		t.Fatal("a unique violation must not look like a foreign key violation")
	}
}

func TestInsertWithoutIDFails(t *testing.T) {
	t.Parallel()
	db := storetest.Open(t)
	u := newUser("noid")
	u.ID = uuid.Nil
	if _, err := db.NewInsert().Model(&u).Exec(context.Background()); err == nil {
		t.Fatal("inserting a row without an ID should fail")
	}
}

func TestAccessTokens(t *testing.T) {
	t.Parallel()
	db := storetest.Open(t)
	ctx := context.Background()
	user := insertUser(t, db, "carol")

	tok := store.AccessToken{
		ID:        store.NewID(),
		UserID:    user.ID,
		Name:      "claude-code",
		TokenHash: strings.Repeat("ab", 32),
		Scopes:    []string{"read", "execute"},
		ExpiresAt: store.Now().Add(90 * 24 * time.Hour),
		CreatedAt: store.Now(),
	}
	if _, err := db.NewInsert().Model(&tok).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	var got store.AccessToken
	if err := db.NewSelect().Model(&got).Where("token_hash = ?", tok.TokenHash).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Scopes, tok.Scopes) || !got.ExpiresAt.Equal(tok.ExpiresAt) || !got.RevokedAt.IsZero() {
		t.Fatalf("got %+v\nwant %+v", got, tok)
	}

	t.Run("token hash is unique", func(t *testing.T) {
		dup := tok
		dup.ID = store.NewID()
		if _, err := db.NewInsert().Model(&dup).Exec(ctx); !store.IsUniqueViolation(err) {
			t.Fatalf("err = %v, want a unique violation", err)
		}
	})
	t.Run("user must exist", func(t *testing.T) {
		orphan := tok
		orphan.ID = store.NewID()
		orphan.TokenHash = strings.Repeat("cd", 32)
		orphan.UserID = store.NewID()
		_, err := db.NewInsert().Model(&orphan).Exec(ctx)
		if !store.IsForeignKeyViolation(err) {
			t.Fatalf("err = %v, want a foreign key violation", err)
		}
	})
}

func TestProjectsAndRoleBindings(t *testing.T) {
	t.Parallel()
	db := storetest.Open(t)
	ctx := context.Background()
	owner := insertUser(t, db, "dave")
	member := insertUser(t, db, "erin")

	personal := newProject(owner.ID, "dave", true)
	shared := newProject(owner.ID, "ops", false)
	other := newProject(owner.ID, "infra", false)
	for _, p := range []*store.Project{&personal, &shared, &other} {
		if _, err := db.NewInsert().Model(p).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("one personal project per user", func(t *testing.T) {
		second := newProject(owner.ID, "dave-2", true)
		if _, err := db.NewInsert().Model(&second).Exec(ctx); !store.IsUniqueViolation(err) {
			t.Fatalf("err = %v, want a unique violation", err)
		}
	})

	binding := store.RoleBinding{
		ID: store.NewID(), ProjectID: shared.ID, UserID: member.ID, Role: "maintainer",
		CreatedAt: store.Now(), UpdatedAt: store.Now(),
	}
	if _, err := db.NewInsert().Model(&binding).Exec(ctx); err != nil {
		t.Fatal(err)
	}

	t.Run("one binding per user and project", func(t *testing.T) {
		dup := binding
		dup.ID = store.NewID()
		dup.Role = "viewer"
		if _, err := db.NewInsert().Model(&dup).Exec(ctx); !store.IsUniqueViolation(err) {
			t.Fatalf("err = %v, want a unique violation", err)
		}
	})

	t.Run("member's projects through a join", func(t *testing.T) {
		var names []string
		err := db.NewSelect().Model((*store.Project)(nil)).Column("project.name").
			Join("JOIN role_bindings AS rb ON rb.project_id = project.id").
			Where("rb.user_id = ?", member.ID).
			Scan(ctx, &names)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(names, []string{"ops"}) {
			t.Fatalf("names = %v", names)
		}
	})

	t.Run("deleting a project deletes its bindings", func(t *testing.T) {
		if _, err := db.NewDelete().Model(&shared).WherePK().Exec(ctx); err != nil {
			t.Fatal(err)
		}
		n, err := db.NewSelect().Model((*store.RoleBinding)(nil)).Where("project_id = ?", shared.ID).Count(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%d bindings left", n)
		}
	})
}

func TestAuditEventsOrderByTime(t *testing.T) {
	t.Parallel()
	db := storetest.Open(t)
	ctx := context.Background()

	// SQLite stores times as text; fractional seconds of different lengths
	// must still sort as instants.
	base := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	stamps := []time.Time{
		base.Add(500 * time.Millisecond),
		base,
		base.Add(450 * time.Millisecond),
		base.Add(time.Second),
		base.Add(time.Microsecond),
	}
	actor := store.NewID()
	for i, ts := range stamps {
		ev := store.AuditEvent{
			ID: store.NewID(), TS: ts, ActorKind: "user", Action: "token.create",
			ResourceKind: "access_token", Result: "success",
			Details: map[string]any{"i": i},
		}
		if i == 0 {
			ev.ActorID = actor
			ev.IP = "192.0.2.1"
		}
		if _, err := db.NewInsert().Model(&ev).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}

	var got []store.AuditEvent
	if err := db.NewSelect().Model(&got).Order("ts").Scan(ctx); err != nil {
		t.Fatal(err)
	}
	want := slices.Clone(stamps)
	slices.SortFunc(want, time.Time.Compare)
	for i := range want {
		if !got[i].TS.Equal(want[i]) {
			t.Fatalf("position %d: ts = %v, want %v", i, got[i].TS, want[i])
		}
	}

	var since []store.AuditEvent
	if err := db.NewSelect().Model(&since).Where("ts > ?", base.Add(450*time.Millisecond)).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if len(since) != 2 {
		t.Fatalf("events after 12:00:00.45 = %d, want 2", len(since))
	}

	first := got[slices.IndexFunc(got, func(e store.AuditEvent) bool { return e.ActorID == actor })]
	if first.IP != "192.0.2.1" || first.Details["i"] != float64(0) {
		t.Fatalf("event = %+v", first)
	}
}

func TestLeaderLease(t *testing.T) {
	t.Parallel()
	db := storetest.Open(t)
	ctx := context.Background()
	const ttl = 15 * time.Second

	exp1, ok, err := db.AcquireLease(ctx, "leader", "server-a", ttl)
	if err != nil || !ok {
		t.Fatalf("a acquires: ok=%v err=%v", ok, err)
	}
	// Both clocks are this machine's clock in tests; allow for slow CI.
	if d := time.Until(exp1); d < ttl-5*time.Second || d > ttl+5*time.Second {
		t.Fatalf("expires in %s, want about %s", d, ttl)
	}
	if _, ok, err := db.AcquireLease(ctx, "leader", "server-b", ttl); err != nil || ok {
		t.Fatalf("b must not take a live lease: ok=%v err=%v", ok, err)
	}
	time.Sleep(5 * time.Millisecond)
	exp2, ok, err := db.AcquireLease(ctx, "leader", "server-a", ttl)
	if err != nil || !ok || !exp2.After(exp1) {
		t.Fatalf("a renews: ok=%v err=%v expires %v -> %v", ok, err, exp1, exp2)
	}

	if err := db.ReleaseLease(ctx, "leader", "server-b"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.AcquireLease(ctx, "leader", "server-b", ttl); ok {
		t.Fatal("releasing someone else's lease must have no effect")
	}
	if err := db.ReleaseLease(ctx, "leader", "server-a"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := db.AcquireLease(ctx, "leader", "server-b", ttl); err != nil || !ok {
		t.Fatalf("b acquires after release: ok=%v err=%v", ok, err)
	}

	// An expired lease is taken over.
	_, err = db.NewUpdate().Model((*store.Lease)(nil)).
		Set("expires_at = ?", time.Now().Add(-time.Hour)).
		Where("name = ?", "leader").Exec(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := db.AcquireLease(ctx, "leader", "server-c", ttl); err != nil || !ok {
		t.Fatalf("c takes over an expired lease: ok=%v err=%v", ok, err)
	}
}

func TestLeaderLeaseSingleWinner(t *testing.T) {
	t.Parallel()
	db := storetest.Open(t)
	ctx := context.Background()

	const contenders = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	var winners []string
	var errs []error
	for i := range contenders {
		holder := "server-" + string(rune('a'+i))
		wg.Go(func() {
			_, ok, err := db.AcquireLease(ctx, "leader", holder, time.Minute)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
			if ok {
				winners = append(winners, holder)
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	if len(winners) != 1 {
		t.Fatalf("winners = %v, want exactly one", winners)
	}
	var lease store.Lease
	if err := db.NewSelect().Model(&lease).Where("name = ?", "leader").Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if lease.Holder != winners[0] {
		t.Fatalf("holder = %s, winner = %s", lease.Holder, winners[0])
	}
}

func TestLeaseValidation(t *testing.T) {
	t.Parallel()
	db := storetest.Open(t)
	for _, tc := range []struct {
		name, holder string
		ttl          time.Duration
	}{{"", "a", time.Second}, {"x", "", time.Second}, {"x", "a", 0}} {
		if _, _, err := db.AcquireLease(context.Background(), tc.name, tc.holder, tc.ttl); err == nil {
			t.Errorf("AcquireLease(%q, %q, %s): expected an error", tc.name, tc.holder, tc.ttl)
		}
	}
}

func TestReadOnlyTxDoesNotBlockWriters(t *testing.T) {
	t.Parallel()
	db := storetest.Open(t)
	ctx := context.Background()
	insertUser(t, db, "frank")

	ro, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Rollback() }()
	if n, err := ro.NewSelect().Model((*store.User)(nil)).Count(ctx); err != nil || n != 1 {
		t.Fatalf("count = %d, err = %v", n, err)
	}

	// A writer commits while the read transaction is open: WAL on SQLite,
	// MVCC on PostgreSQL.
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	u := newUser("grace")
	if _, err := db.NewInsert().Model(&u).Exec(writeCtx); err != nil {
		t.Fatalf("write during an open read transaction: %v", err)
	}
}

func newUser(name string) store.User {
	now := store.Now()
	return store.User{ID: store.NewID(), Username: name, CreatedAt: now, UpdatedAt: now}
}

func insertUser(t *testing.T, db *store.DB, name string) store.User {
	t.Helper()
	u := newUser(name)
	if _, err := db.NewInsert().Model(&u).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	return u
}

func newProject(owner uuid.UUID, name string, personal bool) store.Project {
	now := store.Now()
	return store.Project{ID: store.NewID(), Name: name, OwnerUserID: owner, IsPersonal: personal, CreatedAt: now, UpdatedAt: now}
}
