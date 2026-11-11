package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/cli"
)

// TestCLITaskExportImport exports a task from one in-process server with
// the CLI and imports it into another.
func TestCLITaskExportImport(t *testing.T) {
	source, _ := startTestServer(t)
	target, _ := startTestServer(t)
	t.Setenv(cli.EnvServer, "")
	t.Setenv(cli.EnvToken, "")
	work := t.TempDir()

	// On the source: a task with two versions and a webhook trigger.
	t.Setenv(cli.EnvConfig, filepath.Join(work, "source", "cli.json"))
	register(t, source.URL, "alice", "correct horse")
	runCLI(t, "correct horse\n", "login", source.URL, "--username", "alice", "--password-stdin").ok(t)
	dir := filepath.Join(work, "digest")
	writeFile(t, filepath.Join(dir, "lerryn.yaml"), `name: digest
runtime: {language: python, version: "3.12"}
entrypoint: [python, main.py]
workflow:
  - title: Print version
    description: Write the script version to the run log.
credentials: [{name: LLM_API_KEY}]
`, 0o600)
	writeFile(t, filepath.Join(dir, "main.py"), "print('v1')\n", 0o600)
	taskID := runCLI(t, "", "tasks", "apply", dir).ok(t).json(t)["task"].(map[string]any)["id"].(string)
	writeFile(t, filepath.Join(dir, "main.py"), "print('v2')\n", 0o600)
	runCLI(t, "", "tasks", "apply", dir, "--expected-version", "1").ok(t)
	runCLI(t, "", "api", "setTriggers", "--task-id", taskID, "--arg", `triggers=[{"name":"push","type":"webhook"}]`).ok(t)

	file := filepath.Join(work, "digest.bundle.json")
	summary := runCLI(t, "", "tasks", "export", "digest", "--history", "--file", file).ok(t).json(t)
	if summary["name"] != "digest" || summary["triggers"] != float64(1) || len(summary["versions"].([]any)) != 2 {
		t.Fatalf("export summary = %v", summary)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("bundle file: %v, %v", info, err)
	}
	stdout := runCLI(t, "", "tasks", "export", taskID, "--no-triggers", "-o", "text").ok(t).stdout
	var latest map[string]any
	if err := json.Unmarshal([]byte(stdout), &latest); err != nil || latest["triggers"] != nil || len(latest["versions"].([]any)) != 1 {
		t.Fatalf("export to stdout: %v\n%s", err, stdout)
	}
	if r := runCLI(t, "", "tasks", "export", "no-such-task"); r.code != cli.ExitAPI {
		t.Fatalf("export of an unknown task: exit %d", r.code)
	}

	// On the target: another account imports the file.
	t.Setenv(cli.EnvConfig, filepath.Join(work, "target", "cli.json"))
	register(t, target.URL, "bob", "correct horse")
	runCLI(t, "correct horse\n", "login", target.URL, "--username", "bob", "--password-stdin").ok(t)
	preview := runCLI(t, "", "tasks", "import", file, "--dry-run").ok(t).json(t)
	if preview["dry_run"] != true || preview["task"] != nil {
		t.Fatalf("dry run = %v", preview)
	}
	text := runCLI(t, "", "tasks", "import", file, "-o", "text").ok(t).stdout
	for _, want := range []string{"digest", "Needs binding: credential LLM_API_KEY", "token (shown once): wl_", "untested"} {
		if !strings.Contains(text, want) {
			t.Errorf("import output lacks %q:\n%s", want, text)
		}
	}
	items := runCLI(t, "", "api", "listTasks", "--name", "digest").ok(t).json(t)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("tasks named digest = %v", items)
	}
	got := runCLI(t, "", "tasks", "get", items[0].(map[string]any)["id"].(string)).ok(t).json(t)
	if got["latest_version"].(map[string]any)["seq"] != float64(2) || got["state"] != "inactive" || got["id"] == taskID {
		t.Fatalf("imported task = %v", got)
	}

	// The name is taken now; --name and standard input work.
	if r := runCLI(t, "", "tasks", "import", file); r.code != cli.ExitAPI || r.json(t)["code"] != "version_conflict" {
		t.Fatalf("second import: exit %d\n%s", r.code, r.stdout)
	}
	data, err := os.ReadFile(file) // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatal(err)
	}
	copied := runCLI(t, string(data), "tasks", "import", "-", "--name", "digest-copy").ok(t).json(t)
	if copied["task"].(map[string]any)["name"] != "digest-copy" {
		t.Fatalf("import from stdin = %v", copied)
	}
	writeFile(t, filepath.Join(work, "not-a-bundle.json"), "[1, 2]", 0o600)
	if r := runCLI(t, "", "tasks", "import", filepath.Join(work, "not-a-bundle.json")); r.code != cli.ExitUsage {
		t.Fatalf("import of a JSON array: exit %d", r.code)
	}
}
