//go:build integration

package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/store"
)

// TestMigrateDBToPostgres17 moves a populated SQLite deployment into a
// fresh postgres:17 container with `lerryn migrate-db`, the path an
// operator takes from the single-node to the standard topology (tech
// design 6.1, 15.5). The container is removed when the test ends.
//
//	go test -tags integration -run TestMigrateDBToPostgres17 -v ./server/cmd/lerryn/
func TestMigrateDBToPostgres17(t *testing.T) {
	dsn := startPostgres(t, "postgres:17")

	src := sqliteDeployment(t)
	taskID, hash := populate(t, src)
	dst := newDeployment(t, config.Database{Driver: config.DatabasePostgres, DSN: dsn})

	out, err := runCmd(t, "--config", src.configPath, "migrate-db", "--to-config", dst.configPath, "--work-dir", t.TempDir())
	if err != nil {
		t.Fatalf("migrate-db: %v\n%s", err, out)
	}
	t.Logf("migrate-db output:\n%s", out)
	if !strings.Contains(out, "row counts match") || strings.Contains(out, "differs") {
		t.Fatalf("row counts do not match:\n%s", out)
	}
	assertTask(t, dst, taskID, hash)

	// The moved database serves the run queue: claim a run on PostgreSQL.
	ctx := context.Background()
	db, err := store.Open(ctx, dst.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var task store.Task
	if err := db.NewSelect().Model(&task).Where("id = ?", taskID).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	var version store.TaskVersion
	if err := db.NewSelect().Model(&version).Where("task_id = ?", task.ID).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	now := store.Now()
	runner := store.Runner{ID: "runner-after-move", Name: "r", Slots: 1, LastSeenAt: now, CreatedAt: now}
	run := store.Run{ID: store.NewID(), ProjectID: task.ProjectID, TaskID: task.ID, VersionID: version.ID, VersionSeq: 1,
		Status: store.RunQueued, TriggerKind: "manual", Params: "{}", QueuedAt: now}
	for _, m := range []any{&runner, &run} {
		if _, err := db.NewInsert().Model(m).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var claimed int
	err = db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		ids, err := db.ClaimRuns(ctx, tx, runner.ID, 1, 30*time.Second)
		claimed = len(ids)
		return err
	})
	if err != nil || claimed != 1 {
		t.Fatalf("claim after move: %d claimed, %v", claimed, err)
	}
}

// startPostgres runs image with a throwaway database, waits until it
// accepts connections and returns its DSN.
func startPostgres(t *testing.T, image string) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not found")
	}
	// The whole ID: the first characters of a UUIDv7 are its timestamp and
	// repeat across runs started within the same minute.
	name := "lerryn-migratedb-test-" + store.NewID().String()
	// #nosec G204 -- fixed arguments; name and image come from this test
	run := exec.Command("docker", "run", "-d", "--rm", "--name", name,
		"-e", "POSTGRES_USER=lerryn", "-e", "POSTGRES_PASSWORD=lerryn", "-e", "POSTGRES_DB=lerryn",
		"-p", "127.0.0.1::5432", image)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("start %s: %v\n%s", image, err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("docker", "rm", "-f", name).CombinedOutput(); err != nil { // #nosec G204 -- the container this test started
			t.Errorf("remove container %s: %v\n%s", name, err, out)
		}
	})
	out, err := exec.Command("docker", "port", name, "5432/tcp").Output() // #nosec G204 -- the container this test started
	if err != nil {
		t.Fatalf("read published port: %v", err)
	}
	addr := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	dsn := fmt.Sprintf("postgres://lerryn:lerryn@%s/lerryn?sslmode=disable", addr)

	deadline := time.Now().Add(60 * time.Second)
	for {
		// pg_isready inside the container answers before the init scripts
		// finish and the server restarts; a real connection does not.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		db, err := store.Open(ctx, config.Database{Driver: config.DatabasePostgres, DSN: dsn})
		cancel()
		if err == nil {
			_ = db.Close()
			return dsn
		}
		if time.Now().After(deadline) {
			t.Fatalf("postgres did not become ready: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
