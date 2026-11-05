//go:build unix

package runner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/niltavi/lerryn/runtimes/sdk"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

// sbxExecShells are the shells sbx-exec runs under in the runtime images
// (dash in Debian, busybox ash in Alpine); each one present on the host
// is tested.
func sbxExecShells(t *testing.T) []string {
	var shells []string
	for _, sh := range []string{"/bin/sh", "dash", "bash", "busybox"} {
		if p, err := exec.LookPath(sh); err == nil {
			shells = append(shells, p)
		}
	}
	if len(shells) == 0 {
		t.Skip("no POSIX shell")
	}
	return shells
}

func sbxExecScript(t *testing.T) string {
	t.Helper()
	files, err := sdk.Files(langShell)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := materializeSDK(t.TempDir(), files)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "bin", "sbx-exec")
}

type sbxExecResult struct {
	code           int
	stdout, stderr string
	pid            int
}

func runSBXExec(t *testing.T, shell, script string, stdin []byte, env []string, args ...string) sbxExecResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	argv := []string{script, "--"}
	if filepath.Base(shell) == "busybox" {
		argv = append([]string{"sh"}, argv...)
	}
	cmd := exec.CommandContext(ctx, shell, append(argv, args...)...) // #nosec G204 -- test commands
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := sbxExecResult{stdout: stdout.String(), stderr: stderr.String(), pid: cmd.Process.Pid}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		res.code = ee.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	return res
}

func TestSBXExecInstallsSecrets(t *testing.T) {
	script := sbxExecScript(t)
	value := "sk-test value with spaces\n"
	rs, err := buildSecrets([]runproto.Secret{
		{Name: "LLM", As: "env", Fields: []runproto.SecretField{{Name: "LLM_API_KEY", Value: value, Sensitive: true}}},
		{Name: "CRM", As: "file", Fields: []runproto.SecretField{{Name: "USER", Value: "bot"}, {Name: "CERT", Value: "line1\nline2\n\n", Sensitive: true}}},
		{Name: "EMPTY", As: "env", Fields: []runproto.SecretField{{Name: "EMPTY_VALUE", Value: ""}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, shell := range sbxExecShells(t) {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "secrets")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			// The entrypoint prints its PID, the env value with a marker
			// around it, and exits 7: exec keeps the process, the value and
			// the exit code.
			res := runSBXExec(t, shell, script, rs.payload, []string{"LERRYN_SECRETS_DIR=" + dir},
				"sh", "-c", `printf '%s|[%s]|%s' "$$" "$LLM_API_KEY" "${EMPTY_VALUE-unset}"; exit 7`)
			if res.code != 7 {
				t.Fatalf("exit %d: %s", res.code, res.stderr)
			}
			if want := strconv.Itoa(res.pid) + "|[" + value + "]|"; res.stdout != want {
				t.Fatalf("stdout = %q, want %q", res.stdout, want)
			}
			if strings.Contains(res.stderr, "sk-test") {
				t.Fatalf("stderr leaks: %q", res.stderr)
			}
			for name, want := range map[string]string{"CRM/USER": "bot", "CRM/CERT": "line1\nline2\n\n"} {
				path := filepath.Join(dir, name)
				got, err := os.ReadFile(path) // #nosec G304 -- test file
				if err != nil || string(got) != want {
					t.Fatalf("%s = %q, %v", name, got, err)
				}
				if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
					t.Fatalf("%s mode %v", name, fi.Mode())
				}
			}
			if fi, _ := os.Stat(filepath.Join(dir, "CRM")); fi.Mode().Perm() != 0o700 {
				t.Fatalf("credential directory mode %v", fi.Mode())
			}
		})
	}
}

// The strict umask is for the credential files only: the task runs with
// the umask it would have had without credentials, so the events file and
// its outputs stay readable to a runner that is not the task user.
func TestSBXExecRestoresUmask(t *testing.T) {
	script := sbxExecScript(t)
	rs, err := buildSecrets([]runproto.Secret{
		{Name: "CRM", As: "file", Fields: []runproto.SecretField{{Name: "USER", Value: "bot"}}},
		{Name: "LLM", As: "env", Fields: []runproto.SecretField{{Name: "LLM_API_KEY", Value: "sk-test-value"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, shell := range sbxExecShells(t) {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "secrets")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			without := runSBXExec(t, shell, script, nil, nil, "sh", "-c", "umask")
			with := runSBXExec(t, shell, script, rs.payload, []string{"LERRYN_SECRETS_DIR=" + dir}, "sh", "-c", "umask")
			if without.code != 0 || with.code != 0 {
				t.Fatalf("exit %d, %d: %s%s", without.code, with.code, without.stderr, with.stderr)
			}
			if strings.TrimSpace(with.stdout) != strings.TrimSpace(without.stdout) {
				t.Fatalf("umask with credentials %q, without %q", with.stdout, without.stdout)
			}
			if fi, err := os.Stat(filepath.Join(dir, "CRM", "USER")); err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatalf("credential file: %v %v", fi, err)
			}
		})
	}
}

func TestSBXExecWithoutSecrets(t *testing.T) {
	script := sbxExecScript(t)
	for _, shell := range sbxExecShells(t) {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			res := runSBXExec(t, shell, script, nil, nil, "sh", "-c", `read x; echo "stdin done $?"`)
			if res.code != 0 || strings.TrimSpace(res.stdout) != "stdin done 1" {
				t.Fatalf("exit %d: %q %q", res.code, res.stdout, res.stderr)
			}
			if res := runSBXExec(t, shell, script, nil, nil, "no-such-command-lerryn"); res.code != 127 {
				t.Fatalf("missing entrypoint exit %d", res.code)
			}
		})
	}
}

// Malformed or incomplete input stops the run before the entrypoint, with
// exit code 125, and never prints a value.
func TestSBXExecRejectsBadInput(t *testing.T) {
	script := sbxExecScript(t)
	good, _ := buildSecrets([]runproto.Secret{{Name: "LLM", As: "env", Fields: []runproto.SecretField{{Name: "LLM_API_KEY", Value: "sk-abcdef"}}}})
	tests := map[string]struct {
		stdin string
		env   []string
	}{
		"garbage":           {stdin: "hello\n"},
		"truncated value":   {stdin: "LERRYN-SECRETS 1\nenv LLM LLM_API_KEY 20\nsk-abc"},
		"missing end":       {stdin: "LERRYN-SECRETS 1\nenv LLM LLM_API_KEY 9\nsk-abcdef\n"},
		"bad kind":          {stdin: "LERRYN-SECRETS 1\nexec LLM X 1\nx\nEND\n"},
		"bad name":          {stdin: "LERRYN-SECRETS 1\nenv llm X 1\nx\nEND\n"},
		"path in field":     {stdin: "LERRYN-SECRETS 1\nfile LLM ../X 1\nx\nEND\n"},
		"bad length":        {stdin: "LERRYN-SECRETS 1\nenv LLM X -1\nx\nEND\n"},
		"no separator":      {stdin: "LERRYN-SECRETS 1\nenv LLM X 1\nxy\nEND\n"},
		"overrides env":     {stdin: string(good.payload), env: []string{"LLM_API_KEY=already"}},
		"overrides PATH":    {stdin: "LERRYN-SECRETS 1\nenv LLM PATH 4\n/tmp\nEND\n"},
		"file without dir":  {stdin: "LERRYN-SECRETS 1\nfile LLM X 1\nx\nEND\n", env: []string{"LERRYN_SECRETS_DIR=/nonexistent-lerryn"}},
		"file listed twice": {stdin: "LERRYN-SECRETS 1\nfile LLM X 1\nx\nfile LLM X 1\ny\nEND\n"},
	}
	for _, shell := range sbxExecShells(t) {
		for name, tt := range tests {
			t.Run(filepath.Base(shell)+"/"+name, func(t *testing.T) {
				env := tt.env
				if len(env) == 0 || !strings.HasPrefix(env[0], "LERRYN_SECRETS_DIR=") {
					dir := t.TempDir()
					env = append(env, "LERRYN_SECRETS_DIR="+dir)
				}
				res := runSBXExec(t, shell, script, []byte(tt.stdin), env, "sh", "-c", "echo ran")
				if res.code != 125 || strings.Contains(res.stdout, "ran") {
					t.Fatalf("exit %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
				}
				if strings.Contains(res.stderr, "sk-abc") || !strings.HasPrefix(res.stderr, "sbx-exec: ") {
					t.Fatalf("stderr = %q", res.stderr)
				}
			})
		}
	}
}
