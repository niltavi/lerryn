package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/secrets"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestRotateMasterKey(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Database.DSN = filepath.Join(dir, "lerryn.db")
	cfg.Storage.Path = filepath.Join(dir, "storage")
	cfg.Secrets.MasterKeyFile = filepath.Join(dir, "keys", "master.key")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The first start generates the key.
	st, err := buildServer(ctx, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := secrets.LoadKeyFile(cfg.Secrets.MasterKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	seedCredential(t, st.db, before)
	_ = st.db.Close()

	var out bytes.Buffer
	if err := runRotateKey(ctx, cfg, log, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "re-wrapped 1 credential version(s)") {
		t.Fatalf("output = %q", out.String())
	}
	after, _, err := secrets.LoadKeyFile(cfg.Secrets.MasterKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if ids := after.IDs(); len(ids) != 2 || ids[1] != before.CurrentID() || ids[0] == before.CurrentID() {
		t.Fatalf("key ids after rotation = %v", ids)
	}
	db, err := store.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var v store.CredentialVersion
	if err := db.NewSelect().Model(&v).Limit(1).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if v.KEKID != after.CurrentID() {
		t.Fatalf("version wrapped by %s, want %s", v.KEKID, after.CurrentID())
	}
	// rewrap is a no-op now.
	out.Reset()
	if err := rewrap(ctx, cfg, log, after, &out); err != nil || !strings.Contains(out.String(), "re-wrapped 0 credential") {
		t.Fatalf("rewrap = %q, %v", out.String(), err)
	}
}

// seedCredential stores one filled credential.
func seedCredential(t *testing.T, db *store.DB, kr *secrets.LocalKeyring) {
	t.Helper()
	ctx := context.Background()
	accounts := service.NewAccounts(db, nil)
	_, sess, err := accounts.Register(ctx, service.RegisterInput{Username: "alice", Password: "correct horse"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := accounts.Authenticate(ctx, sess.Token)
	blobs, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authz := service.NewAuthorizer(db)
	task, err := service.NewTasks(db, authz, blobs, nil).Apply(ctx, p, service.ApplyTaskInput{
		ProjectID: p.PersonalProjectID, Name: "digest",
		Manifest: service.Manifest{Runtime: service.RuntimeSpec{Language: "python", Version: "3.12"}, Entrypoint: []string{"python", "main.py"}},
		Files:    []service.FileInput{{Path: "main.py", Content: []byte("print(1)\n")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	creds := service.NewCredentials(db, authz, kr, nil)
	d, err := creds.CreateRequest(ctx, p, service.CreateRequestInput{
		TaskID: task.Task.Task.ID, Name: "TOKEN", Purpose: "x",
		Fields: []store.CredentialField{{Name: "TOKEN", Secret: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Fill(ctx, p, d.Request.ID, map[string]string{"TOKEN": "sk-rotate"}); err != nil {
		t.Fatal(err)
	}
}
