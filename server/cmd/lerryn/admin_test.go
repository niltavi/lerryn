package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// adminTestConfig is a SQLite deployment in a temporary directory with
// its schema migrated.
func adminTestConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Database.DSN = filepath.Join(dir, "lerryn.db")
	cfg.Storage.Path = filepath.Join(dir, "storage")
	cfg.Secrets.MasterKeyFile = filepath.Join(dir, "master.key")
	if err := runMigrate(context.Background(), cfg, slog.New(slog.DiscardHandler), io.Discard); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func openAccounts(t *testing.T, cfg config.Config) (*service.Accounts, *store.DB) {
	t.Helper()
	db, err := store.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return service.NewAccounts(db, nil), db
}

func TestAdminCreate(t *testing.T) {
	ctx := context.Background()
	cfg := adminTestConfig(t)
	log := slog.New(slog.DiscardHandler)
	var out bytes.Buffer
	in := service.AdminCreateUserInput{RegisterInput: service.RegisterInput{Username: "ops", Password: "correct horse"}, IsAdmin: true}
	if err := runAdminCreate(ctx, cfg, log, &out, in); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "created admin ops (") {
		t.Fatalf("output = %q", out.String())
	}
	accounts, db := openAccounts(t, cfg)
	acct, _, err := accounts.Login(ctx, "OPS", "correct horse")
	if err != nil || !acct.User.IsAdmin || acct.PersonalProjectID.String() == "" {
		t.Fatalf("login = %+v, %v", acct, err)
	}
	var audit store.AuditEvent
	if err := db.NewSelect().Model(&audit).Where("action = ?", "user.create").Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if details := fmt.Sprint(audit.Details); audit.ActorKind != "system" || audit.Details["via"] != "cli" || strings.Contains(details, "correct horse") {
		t.Fatalf("audit = %+v", audit)
	}

	// A regular user, then the errors a maintainer can make.
	in = service.AdminCreateUserInput{RegisterInput: service.RegisterInput{Username: "bob", Password: "correct horse"}}
	if err := runAdminCreate(ctx, cfg, log, &out, in); err != nil {
		t.Fatal(err)
	}
	if acct, _, _ := accounts.Login(ctx, "bob", "correct horse"); acct.User.IsAdmin {
		t.Fatal("bob became an admin")
	}
	for name, tc := range map[string]struct {
		in   service.RegisterInput
		want string
	}{
		"taken":          {service.RegisterInput{Username: "Ops", Password: "correct horse"}, "taken"},
		"short password": {service.RegisterInput{Username: "carol", Password: "short"}, "密码"},
		"bad username":   {service.RegisterInput{Username: "-x", Password: "correct horse"}, "用户名"},
	} {
		err := runAdminCreate(ctx, cfg, log, io.Discard, service.AdminCreateUserInput{RegisterInput: tc.in, IsAdmin: true})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestAdminResetPassword(t *testing.T) {
	ctx := context.Background()
	cfg := adminTestConfig(t)
	log := slog.New(slog.DiscardHandler)
	accounts, _ := openAccounts(t, cfg)
	_, sess, err := accounts.Register(ctx, service.RegisterInput{Username: "alice", Password: "forgotten pw"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runAdminResetPassword(ctx, cfg, log, &out, "ALICE", "  new secret  "); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "reset the password of alice") {
		t.Fatalf("output = %q", out.String())
	}
	if _, err := accounts.Authenticate(ctx, sess.Token); !errors.Is(err, service.ErrUnauthenticated) {
		t.Fatalf("old session after reset: %v", err)
	}
	if _, _, err := accounts.Login(ctx, "alice", "forgotten pw"); !errors.Is(err, service.ErrUnauthenticated) {
		t.Fatalf("old password: %v", err)
	}
	if _, _, err := accounts.Login(ctx, "alice", "  new secret  "); err != nil {
		t.Fatalf("new password: %v", err)
	}
	if err := runAdminResetPassword(ctx, cfg, log, io.Discard, "nobody", "new secret"); err == nil || !strings.Contains(err.Error(), "no account") {
		t.Fatalf("unknown user: %v", err)
	}
	if err := runAdminResetPassword(ctx, cfg, log, io.Discard, "alice", "short"); err == nil {
		t.Fatal("short password accepted")
	}
}

// The commands read the password from standard input only when asked to,
// keeping it as typed apart from the line ending.
func TestAdminCommandPassword(t *testing.T) {
	cfg := adminTestConfig(t)
	t.Setenv("LERRYN_DATABASE_DSN", cfg.Database.DSN)
	t.Setenv("LERRYN_STORAGE_PATH", cfg.Storage.Path)
	t.Setenv("LERRYN_LOG_LEVEL", "error")
	run := func(stdin string, args ...string) (string, error) {
		cmd := newRootCmd()
		var out bytes.Buffer
		cmd.SetIn(strings.NewReader(stdin))
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append([]string{"--config", ""}, args...))
		err := cmd.Execute()
		return out.String(), err
	}
	if _, err := run("correct horse\n", "admin", "create", "ops"); err == nil || !strings.Contains(err.Error(), "--password-stdin") {
		t.Fatalf("without --password-stdin: %v", err)
	}
	if out, err := run(" spaced pass \r\nignored\n", "admin", "create", "ops", "--password-stdin", "--display-name", "运维"); err != nil || !strings.Contains(out, "created admin ops") {
		t.Fatalf("create = %q, %v", out, err)
	}
	accounts, _ := openAccounts(t, cfg)
	acct, _, err := accounts.Login(context.Background(), "ops", " spaced pass ")
	if err != nil || acct.User.DisplayName != "运维" {
		t.Fatalf("login = %+v, %v", acct.User, err)
	}
	if _, err := run("\n", "admin", "reset-password", "ops", "--password-stdin"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty password: %v", err)
	}
	if out, err := run("another pass\n", "admin", "reset-password", "ops", "--password-stdin"); err != nil || !strings.Contains(out, "reset the password of ops") {
		t.Fatalf("reset = %q, %v", out, err)
	}
}

// auth.initial_admin creates the first admin on an empty deployment only;
// later starts with the setting still in place change nothing.
func TestInitialAdmin(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Database.DSN = filepath.Join(dir, "lerryn.db")
	cfg.Storage.Path = filepath.Join(dir, "storage")
	cfg.Secrets.MasterKeyFile = filepath.Join(dir, "master.key")
	pwFile := filepath.Join(dir, "admin-password")
	if err := os.WriteFile(pwFile, []byte("initial pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Auth.InitialAdmin = config.InitialAdmin{Username: "root", PasswordFile: pwFile}
	log := slog.New(slog.DiscardHandler)

	st, err := buildServer(ctx, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.db.Close()
	accounts, db := openAccounts(t, cfg)
	acct, _, err := accounts.Login(ctx, "root", "initial pass")
	if err != nil || !acct.User.IsAdmin {
		t.Fatalf("initial admin = %+v, %v", acct.User, err)
	}
	// The next account registered is not an admin: the deployment is no
	// longer empty.
	if second, _, err := accounts.Register(ctx, service.RegisterInput{Username: "alice", Password: "correct horse"}); err != nil || second.User.IsAdmin {
		t.Fatalf("second account = %+v, %v", second.User, err)
	}

	cfg.Auth.InitialAdmin.Username = "other"
	st, err = buildServer(ctx, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.db.Close()
	if n, err := db.NewSelect().Model((*store.User)(nil)).Count(ctx); err != nil || n != 2 {
		t.Fatalf("users = %d, %v", n, err)
	}

	// A missing password file stops the server instead of starting it
	// without the admin the operator asked for.
	empty := cfg
	empty.Database.DSN = filepath.Join(t.TempDir(), "lerryn.db")
	empty.Auth.InitialAdmin.PasswordFile = filepath.Join(dir, "missing")
	if _, err := buildServer(ctx, empty, log); err == nil || !strings.Contains(err.Error(), "password_file") {
		t.Fatalf("missing password file: %v", err)
	}
}
