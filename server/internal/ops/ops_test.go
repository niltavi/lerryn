package ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

// tricky holds text that breaks naive SQL or JSON handling: quotes,
// placeholders, backslashes, newlines and non-ASCII.
const tricky = "it's a \"test\" ? $1 ?0 \\n\\x00-free\nline two 中文 🚀"

// seed fills db with one of everything and returns the file content it
// stored in bs.
func seed(t *testing.T, db *store.DB, bs blob.Store) []byte {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 11, 3, 1, 2, 3, 456789000, time.UTC)

	content := []byte("print('hello from " + tricky + "')\n")
	hash, err := bs.Put(ctx, strings.NewReader(string(content)))
	if err != nil {
		t.Fatal(err)
	}

	admin := store.User{ID: store.NewID(), Username: "admin", DisplayName: tricky, Email: "a@example.com",
		PasswordHash: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA", IsAdmin: true, CreatedAt: at, UpdatedAt: at} // #nosec G101 -- a made-up hash
	other := store.User{ID: store.NewID(), Username: "other", CreatedAt: at, UpdatedAt: at, DisabledAt: at.Add(time.Hour)}
	project := store.Project{ID: store.NewID(), Name: "personal", OwnerUserID: admin.ID, IsPersonal: true, CreatedAt: at, UpdatedAt: at}
	binding := store.RoleBinding{ID: store.NewID(), ProjectID: project.ID, UserID: other.ID, Role: "viewer", CreatedAt: at, UpdatedAt: at}
	token := store.AccessToken{ID: store.NewID(), UserID: admin.ID, Name: "agent", TokenHash: strings.Repeat("ab", 32),
		Prefix: "wlg_ab", Scopes: []string{"read", "write"}, CreatedAt: at, ExpiresAt: at.Add(24 * time.Hour)}
	session := store.Session{ID: store.NewID(), UserID: admin.ID, TokenHash: strings.Repeat("cd", 32), Kind: "web",
		CreatedAt: at, ExpiresAt: at.Add(time.Hour), LastSeenAt: at}
	audit := store.AuditEvent{ID: store.NewID(), TS: at, ActorKind: "user", ActorID: admin.ID, Action: "task.apply",
		ResourceKind: "task", Result: "ok", Details: map[string]any{"note": tricky}}
	task := store.Task{ID: store.NewID(), ProjectID: project.ID, Name: "daily-report", Description: tricky,
		LatestSeq: 1, CreatedBy: admin.ID, CreatedAt: at, UpdatedAt: at}
	version := store.TaskVersion{ID: store.NewID(), TaskID: task.ID, Seq: 1, Manifest: `{"name":"daily-report","note":"` + strings.ReplaceAll(tricky, "\n", `\n`) + `"}`,
		TreeHash: hash, CreatedBy: admin.ID, CreatedAt: at}
	file := store.TaskFile{ID: store.NewID(), VersionID: version.ID, Path: "main.py", BlobHash: hash, Size: int64(len(content)), Executable: true}
	runner := store.Runner{ID: "runner-1", Name: "host-a", Slots: 4, LastSeenAt: at, CreatedAt: at}
	exit := 0
	done := store.Run{ID: store.NewID(), ProjectID: project.ID, TaskID: task.ID, VersionID: version.ID, VersionSeq: 1,
		Status: store.RunCompleted, IsTest: true, TriggerKind: "manual", CreatedBy: admin.ID, Params: `{"q":"?"}`,
		RunnerID: runner.ID, QueuedAt: at, StartedAt: at.Add(time.Second), FinishedAt: at.Add(2 * time.Second),
		ExitCode: &exit, Result: `{"ok":true}`, LogLines: 2, EventSeq: 1}
	queued := store.Run{ID: store.NewID(), ProjectID: project.ID, TaskID: task.ID, VersionID: version.ID, VersionSeq: 1,
		Status: store.RunQueued, TriggerKind: "manual", Params: "{}", QueuedAt: at.Add(time.Minute)}
	event := store.RunEvent{ID: store.NewID(), RunID: done.ID, Seq: 1, TS: at, Kind: store.EventResult, Payload: `{"ok":true}`}
	chunk := store.LogChunk{ID: store.NewID(), RunID: done.ID, SeqFrom: 1, SeqTo: 2,
		Data: `{"seq":1,"text":"a"}` + "\n" + `{"seq":2,"text":"` + strings.ReplaceAll(tricky, "\n", `\n`) + `"}` + "\n", Bytes: 10, CreatedAt: at}

	for _, m := range []any{&admin, &other, &project, &binding, &token, &session, &audit, &task, &version, &file, &runner, &done, &queued, &event, &chunk} {
		if _, err := db.NewInsert().Model(m).Exec(ctx); err != nil {
			t.Fatalf("seed %T: %v", m, err)
		}
	}
	if _, _, err := db.AcquireLease(ctx, "leader", "server-a", time.Minute); err != nil {
		t.Fatal(err)
	}
	return content
}

// snapshot reads every seeded table through the Bun models, which makes
// rows from either database comparable.
type snapshot struct {
	Users    []store.User
	Tokens   []store.AccessToken
	Sessions []store.Session
	Projects []store.Project
	Bindings []store.RoleBinding
	Audit    []store.AuditEvent
	Tasks    []store.Task
	Versions []store.TaskVersion
	Files    []store.TaskFile
	Runners  []store.Runner
	Runs     []store.Run
	Events   []store.RunEvent
	Chunks   []store.LogChunk
}

func readSnapshot(t *testing.T, db bun.IDB) snapshot {
	t.Helper()
	var s snapshot
	ctx := context.Background()
	for _, dst := range []any{&s.Users, &s.Tokens, &s.Sessions, &s.Projects, &s.Bindings, &s.Audit, &s.Tasks, &s.Versions, &s.Files, &s.Runners, &s.Runs, &s.Events, &s.Chunks} {
		if err := db.NewSelect().Model(dst).Order("id").Scan(ctx); err != nil {
			t.Fatalf("read %T: %v", dst, err)
		}
	}
	utcTimes(reflect.ValueOf(&s).Elem())
	return s
}

// utcTimes converts every time.Time below v to UTC. The pgx driver hands
// out times in the local zone, SQLite in UTC; the instants are what must
// survive a move.
func utcTimes(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		if tm, ok := v.Addr().Interface().(*time.Time); ok {
			*tm = tm.UTC()
			return
		}
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				utcTimes(v.Field(i))
			}
		}
	case reflect.Slice:
		for i := range v.Len() {
			utcTimes(v.Index(i))
		}
	}
}

func openSQLite(t *testing.T, path string) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), config.Database{Driver: config.DatabaseSQLite, DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newLocal(t *testing.T, root string) *blob.Local {
	t.Helper()
	bs, err := blob.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	return bs
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	db := openSQLite(t, filepath.Join(src, "lerryn.db"))
	if _, err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	storage := filepath.Join(src, "storage")
	content := seed(t, db, newLocal(t, storage))
	// A write in progress must not end up in the backup.
	if err := os.WriteFile(filepath.Join(storage, "blobs", "tmp", "put-123"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := readSnapshot(t, db)

	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	res, err := Backup(ctx, BackupOptions{DB: db, StoragePath: storage, Output: archive})
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if res.StorageFiles != 1 || !res.Manifest.StorageIncluded || res.Manifest.SchemaVersion == 0 {
		t.Fatalf("backup result = %+v", res)
	}
	if _, err := Backup(ctx, BackupOptions{DB: db, StoragePath: storage, Output: archive}); err == nil {
		t.Fatal("Backup overwrote an existing archive")
	}
	info, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o007 != 0 {
		t.Errorf("archive mode %v is readable by others", perm)
	}

	dst := t.TempDir()
	target := config.Database{Driver: config.DatabaseSQLite, DSN: filepath.Join(dst, "data", "lerryn.db")}
	restoredStorage := filepath.Join(dst, "data", "storage")
	got, err := Restore(ctx, RestoreOptions{Input: archive, Database: target, StoragePath: restoredStorage})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got.StorageFiles != 1 || len(got.MovedAside) != 0 {
		t.Fatalf("restore result = %+v", got)
	}

	restored := openSQLite(t, target.DSN)
	if err := store.CheckMigrations(ctx, restored); err != nil {
		t.Fatalf("restored schema: %v", err)
	}
	if diff := readSnapshot(t, restored); !reflect.DeepEqual(diff, want) {
		t.Fatalf("restored rows differ:\n got %+v\nwant %+v", diff, want)
	}
	data, err := blob.ReadAll(ctx, newLocal(t, restoredStorage), want.Files[0].BlobHash)
	if err != nil || string(data) != string(content) {
		t.Fatalf("restored blob = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(restoredStorage, "blobs", "tmp", "put-123")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary upload was restored: %v", err)
	}
	_ = restored.Close()

	// A second restore over live data needs --force and keeps the old data.
	_, err = Restore(ctx, RestoreOptions{Input: archive, Database: target, StoragePath: restoredStorage})
	if !errors.Is(err, ErrTargetNotEmpty) {
		t.Fatalf("restore over existing data: %v, want ErrTargetNotEmpty", err)
	}
	got, err = Restore(ctx, RestoreOptions{Input: archive, Database: target, StoragePath: restoredStorage, Force: true})
	if err != nil {
		t.Fatalf("forced Restore: %v", err)
	}
	if len(got.MovedAside) < 2 {
		t.Fatalf("moved aside = %v, want the database and the storage", got.MovedAside)
	}
	for _, p := range got.MovedAside {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("moved-aside copy %s: %v", p, err)
		}
	}
	again := openSQLite(t, target.DSN)
	if diff := readSnapshot(t, again); !reflect.DeepEqual(diff, want) {
		t.Fatal("rows differ after forced restore")
	}
}

func TestRestoreRejectsForeignArchives(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := openSQLite(t, filepath.Join(dir, "src.db"))
	if _, err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	export := filepath.Join(dir, "export.tar.gz")
	if _, err := Export(ctx, ExportOptions{DB: db, Blobs: newLocal(t, filepath.Join(dir, "s")), Output: export}); err != nil {
		t.Fatal(err)
	}
	target := config.Database{Driver: config.DatabaseSQLite, DSN: filepath.Join(dir, "new.db")}
	if _, err := Restore(ctx, RestoreOptions{Input: export, Database: target}); err == nil || !strings.Contains(err.Error(), "not a lerryn backup") {
		t.Fatalf("restoring an export: %v", err)
	}
	if _, err := os.Stat(target.DSN); !errors.Is(err, os.ErrNotExist) {
		t.Error("a failed restore created the database")
	}

	unsafe := filepath.Join(dir, "unsafe.tar.gz")
	a, err := createArchive(unsafe)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.addBytes("../escape", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := a.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(ctx, RestoreOptions{Input: unsafe, Database: target}); err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("restoring an archive with ../: %v", err)
	}
}

func TestBackupNeedsSQLite(t *testing.T) {
	if storetest.Driver(t) != config.DatabasePostgres {
		t.Skip("needs LERRYN_TEST_DATABASE=postgres")
	}
	db := storetest.Open(t)
	_, err := Backup(context.Background(), BackupOptions{DB: db, Output: filepath.Join(t.TempDir(), "b.tar.gz")})
	if !errors.Is(err, ErrNotSQLite) {
		t.Fatalf("Backup on PostgreSQL: %v, want ErrNotSQLite", err)
	}
}

func TestCleanEntryName(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"tables/users.ndjson", true},
		{"storage/blobs/sha256/ab/abcd", true},
		{"./database/lerryn.db", true},
		{"", false},
		{"/etc/passwd", false},
		{"../x", false},
		{"storage/../../x", false},
		{`storage\..\x`, false},
		{"..", false},
	}
	for _, tt := range tests {
		_, err := cleanEntryName(tt.name)
		if (err == nil) != tt.ok {
			t.Errorf("cleanEntryName(%q) error = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}

func TestSQLitePath(t *testing.T) {
	tests := []struct {
		dsn, want string
		ok        bool
	}{
		{"/var/lib/lerryn/lerryn.db", "/var/lib/lerryn/lerryn.db", true},
		{"/var/lib/lerryn/data%20#name.db?cache=shared", "/var/lib/lerryn/data%20#name.db", true},
		{"/var/lib/lerryn/data.db?mode=memory", "/var/lib/lerryn/data.db", true},
		{"/var/lib/lerryn/data.db?vfs=memdb", "", false},
		{"file:/var/lib/lerryn/lerryn.db?_pragma=busy_timeout(1000)", "/var/lib/lerryn/lerryn.db", true},
		{"file:///var/lib/lerryn/data%20name.db", "/var/lib/lerryn/data name.db", true},
		{"file://localhost/var/lib/lerryn/data%25%3f%23.db", "/var/lib/lerryn/data%?#.db", true},
		{"file:relative%20name.db?cache=shared", "relative name.db", true},
		{"file:relative%2520name.db", "relative%20name.db", true},
		{"file:relative+name.db", "relative+name.db", true},
		{"file:/var/lib/lerryn/data.db#ignored", "/var/lib/lerryn/data.db", true},
		{"file:/var/lib/lerryn/data.db#ignored?mode=memory", "/var/lib/lerryn/data.db", true},
		{"file:/var/lib/lerryn/data.db?mode=rw&cache=shared", "/var/lib/lerryn/data.db", true},
		{"file:/var/lib/lerryn/data.db?mode=rwc&vfs=unix", "/var/lib/lerryn/data.db", true},
		{"file:/var/lib/lerryn/data.db?mode=rwc&vfs=memdb", "", false},
		{"file:/var/lib/lerryn/data.db#ignored?vfs=memdb", "", false},
		{"file:/var/lib/lerryn/data.db?vfs=memdb%00ignored", "", false},
		{"file:/var/lib/lerryn/data.db?mode=memory", "", false},
		{"file:/var/lib/lerryn/data.db?mode=memory%00ignored", "", false},
		// Lerryn normalizes raw query parameters before SQLite opens the
		// URI: a # after ? becomes part of the mode, not a fragment.
		{"file:/var/lib/lerryn/data.db?mode=ro#ignored", "", false},
		{"file:/var/lib/lerryn/data.db?mode=", "", false},
		{"file:/var/lib/lerryn/data.db?mode=rw&mode=memory", "", false},
		{"file:/var/lib/lerryn/data.db?%6dode=memory", "", false},
		{"file://remote/var/lib/lerryn/data.db", "", false},
		{"file://localhost:80/var/lib/lerryn/data.db", "", false},
		{"file://user@localhost/var/lib/lerryn/data.db", "", false},
		{"file:/var/lib/lerryn/data%00other.db", "", false},
		{"file:relative%zz.db", "", false},
		{"file:/var/lib/lerryn/data%zz.db", "", false},
		{"file:/var/lib/lerryn/data.db?cache=%zz", "", false},
		{"file:", "", false},
		{"file://localhost", "", false},
		{"file:?mode=memory", "", false},
		{":memory:", "", false},
		{"file::memory:?cache=shared", "", false},
		{"file:%3amemory%3a?cache=shared", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, err := SQLitePath(tt.dsn)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("SQLitePath(%q) = %q, %v; want %q, ok=%v", tt.dsn, got, err, tt.want, tt.ok)
		}
	}
}
