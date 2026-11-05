package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

// deployment is a config file with its database and local object store.
type deployment struct {
	configPath string
	db         config.Database
	storage    string
}

func newDeployment(t *testing.T, db config.Database) deployment {
	t.Helper()
	dir := t.TempDir()
	d := deployment{configPath: filepath.Join(dir, "lerryn.yaml"), db: db, storage: filepath.Join(dir, "storage")}
	yaml := "database:\n  driver: " + db.Driver + "\n  dsn: '" + db.DSN + "'\n" +
		"storage:\n  driver: local\n  path: '" + d.storage + "'\n" +
		"secrets:\n  master_key_file: '" + filepath.Join(dir, "master.key") + "'\n" +
		"log:\n  level: error\n"
	if err := os.WriteFile(d.configPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return d
}

func sqliteDeployment(t *testing.T) deployment {
	return newDeployment(t, config.Database{Driver: config.DatabaseSQLite, DSN: filepath.Join(t.TempDir(), "lerryn.db")})
}

// populate migrates the deployment's database and adds a task with one
// file, the smallest instance whose move exercises rows and objects.
func populate(t *testing.T, d deployment) (taskID string, fileHash string) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, d.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	bs, err := blob.NewLocal(d.storage)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bs.Put(ctx, strings.NewReader("print('hi')\n"))
	if err != nil {
		t.Fatal(err)
	}
	now := store.Now()
	user := store.User{ID: store.NewID(), Username: "owner", IsAdmin: true, CreatedAt: now, UpdatedAt: now}
	project := store.Project{ID: store.NewID(), Name: "p", OwnerUserID: user.ID, IsPersonal: true, CreatedAt: now, UpdatedAt: now}
	task := store.Task{ID: store.NewID(), ProjectID: project.ID, Name: "hello", LatestSeq: 1, CreatedBy: user.ID, CreatedAt: now, UpdatedAt: now}
	version := store.TaskVersion{ID: store.NewID(), TaskID: task.ID, Seq: 1, Manifest: "{}", TreeHash: hash, CreatedBy: user.ID, CreatedAt: now}
	file := store.TaskFile{ID: store.NewID(), VersionID: version.ID, Path: "main.py", BlobHash: hash, Size: 12}
	for _, m := range []any{&user, &project, &task, &version, &file} {
		if _, err := db.NewInsert().Model(m).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return task.ID.String(), hash
}

// assertTask checks that the deployment holds the task and its file.
func assertTask(t *testing.T, d deployment, taskID, hash string) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, d.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := store.CheckMigrations(ctx, db); err != nil {
		t.Fatalf("schema: %v", err)
	}
	var task store.Task
	if err := db.NewSelect().Model(&task).Where("id = ?", taskID).Scan(ctx); err != nil || task.Name != "hello" {
		t.Fatalf("task = %+v, %v", task, err)
	}
	bs, err := blob.NewLocal(d.storage)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := bs.Has(ctx, hash); !ok || err != nil {
		t.Fatalf("object %s missing: %v", hash, err)
	}
}

func runCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestBackupAndRestoreCommands(t *testing.T) {
	src := sqliteDeployment(t)
	taskID, hash := populate(t, src)

	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	out, err := runCmd(t, "--config", src.configPath, "backup", "--output", archive)
	if err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	if !strings.Contains(out, "master key") {
		t.Errorf("backup output lacks the master key reminder: %q", out)
	}

	dst := sqliteDeployment(t)
	if out, err := runCmd(t, "--config", dst.configPath, "restore", "--input", archive); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	assertTask(t, dst, taskID, hash)

	if _, err := runCmd(t, "--config", dst.configPath, "restore", "--input", archive); err == nil {
		t.Fatal("restore over existing data succeeded without --force")
	}
	out, err = runCmd(t, "--config", dst.configPath, "restore", "--input", archive, "--force")
	if err != nil || !strings.Contains(out, "previous data kept at") {
		t.Fatalf("forced restore: %v\n%s", err, out)
	}
	assertTask(t, dst, taskID, hash)
}

func TestBackupRefusesPostgres(t *testing.T) {
	t.Setenv("LERRYN_LOG_LEVEL", "error")
	t.Setenv("LERRYN_DATABASE_DRIVER", "postgres")
	t.Setenv("LERRYN_DATABASE_DSN", "postgres://unused")
	_, err := runCmd(t, "backup", "--output", filepath.Join(t.TempDir(), "b.tar.gz"))
	if err == nil || !strings.Contains(err.Error(), "pg_dump") {
		t.Fatalf("backup on postgres: %v", err)
	}
}

func TestExportAndImportCommands(t *testing.T) {
	src := sqliteDeployment(t)
	taskID, hash := populate(t, src)
	archive := filepath.Join(t.TempDir(), "export.tar.gz")
	if out, err := runCmd(t, "--config", src.configPath, "export", "--output", archive); err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	dst := newDeployment(t, storetest.NewDatabase(t))
	out, err := runCmd(t, "--config", dst.configPath, "import", "--input", archive)
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	assertTask(t, dst, taskID, hash)
}

// TestMigrateDB moves a SQLite deployment to the database selected for
// the test run; under `make test-pg` that is PostgreSQL.
func TestMigrateDB(t *testing.T) {
	src := sqliteDeployment(t)
	taskID, hash := populate(t, src)
	dst := newDeployment(t, storetest.NewDatabase(t))

	out, err := runCmd(t, "--config", src.configPath, "migrate-db", "--to-config", dst.configPath, "--work-dir", t.TempDir())
	if err != nil {
		t.Fatalf("migrate-db: %v\n%s", err, out)
	}
	for _, want := range []string{"tasks", "task_files", "row counts match"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "differs") {
		t.Errorf("counts differ:\n%s", out)
	}
	assertTask(t, dst, taskID, hash)

	// The target now holds data; a second move must refuse.
	if _, err := runCmd(t, "--config", src.configPath, "migrate-db", "--to-config", dst.configPath); err == nil {
		t.Fatal("migrate-db into a non-empty target succeeded")
	}
	// Moving a deployment onto itself is refused before anything runs.
	if _, err := runCmd(t, "--config", src.configPath, "migrate-db", "--to-config", src.configPath); err == nil ||
		!strings.Contains(err.Error(), "same database") {
		t.Fatalf("migrate-db onto itself: %v", err)
	}
}

func TestReadMetricsToken(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if tok, err := readMetricsToken(write("ok", "  s3cret\n")); err != nil || tok != "s3cret" {
		t.Fatalf("token = %q, %v", tok, err)
	}
	if _, err := readMetricsToken(write("empty", "\n")); err == nil {
		t.Error("empty token file accepted")
	}
	if _, err := readMetricsToken(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing token file accepted")
	}
	if tok, err := readMetricsToken(""); err != nil || tok != "" {
		t.Errorf("no token file: %q, %v", tok, err)
	}
}
