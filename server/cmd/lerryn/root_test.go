package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "lerryn dev") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRunnerRoleNeedsRemoteMode(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"runner"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "runner.mode") {
		t.Fatalf("err = %v, want an error naming runner.mode", err)
	}
}

func TestRunnerRoleNeedsServerURL(t *testing.T) {
	t.Setenv("LERRYN_RUNNER_MODE", "remote")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"runner"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "runner.server_url") {
		t.Fatalf("err = %v, want a runner.server_url validation error", err)
	}
}

func TestRunnerRoleNeedsCredentialOrToken(t *testing.T) {
	t.Setenv("LERRYN_RUNNER_MODE", "remote")
	t.Setenv("LERRYN_RUNNER_SERVER_URL", "http://127.0.0.1:1")
	t.Setenv("LERRYN_RUNNER_DATA_DIR", t.TempDir())
	cmd := newRootCmd()
	cmd.SetArgs([]string{"runner"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "registration_token_file") {
		t.Fatalf("err = %v, want an error naming runner.registration_token_file", err)
	}
}

func TestServerRejectsInvalidConfig(t *testing.T) {
	t.Setenv("LERRYN_DATABASE_DRIVER", "mysql")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"server"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "database.driver") {
		t.Fatalf("err = %v, want a database.driver validation error", err)
	}
}
