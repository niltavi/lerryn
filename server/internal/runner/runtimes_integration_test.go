//go:build integration

package runner

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

func itRuntimeManifest(language, version string, entrypoint ...string) runproto.Manifest {
	m := itManifest()
	m.Runtime = runproto.Runtime{Language: language, Version: version}
	m.Entrypoint = entrypoint
	return m
}

// stdoutStages maps each stdout line to its stage label.
func stdoutStages(c *fakeCoordinator, id uuid.UUID) ([]string, map[string]string) {
	var texts []string
	stages := map[string]string{}
	for _, l := range linesOf(c, id, runproto.StreamStdout) {
		texts = append(texts, l.Text)
		stages[l.Text] = l.Stage
	}
	return texts, stages
}

func eventStrings(c *fakeCoordinator, id uuid.UUID) []string {
	return describeEvents(c.runEvents(id))
}

func requireCompleted(t *testing.T, h *harness, id uuid.UUID, timeout time.Duration) {
	t.Helper()
	f := h.coord.waitFinish(t, id, timeout)
	if f.Status != runproto.StatusCompleted || f.ExitCode == nil || *f.ExitCode != 0 {
		t.Fatalf("Finish = %+v; logs:\n%s", f, strings.Join(h.coord.logTexts(id), "\n"))
	}
	h.coord.checkProtocol(t)
}

// A TypeScript task run directly by Node 22, which strips the types. It
// uses the module and, from a child process, the sbx command.
const nodeTask = `
import { execFileSync } from "node:child_process";
import { writeFileSync } from "node:fs";
import * as sbx from "@lerryn/sbx";

interface Params { source: string; n: number }

const p = sbx.params<Params>();
console.log("params", p.source, p.n);
console.log("uid", process.getuid(), "node", process.versions.node.split(".")[0]);
execFileSync("sbx", ["stage", "start", "fetch"], { stdio: "inherit" });
for (let i = 0; i < 3; i++) console.log("line " + i);
console.error("to stderr");
execFileSync("sbx", ["stage", "done", "fetch", "--message", "fetched 3"], { stdio: "inherit" });
const saved: number = await sbx.stage("save", async () => {
  writeFileSync("/sbx/work/out.txt", "ok");
  try {
    writeFileSync("/sbx/task/new.txt", "x");
    console.log("task dir is writable");
  } catch {
    console.log("task dir is read-only");
  }
  return 3;
});
sbx.result("ok", "saved " + saved + " lines", { lines: saved });
process.stdout.write("bye");
`

func TestIntegrationNodeTask(t *testing.T) {
	h := newDockerHarness(t, "", "", nil)
	a := newAssignment(itRuntimeManifest("node", "22", "node", "main.ts"))
	a.Params = []byte(`{"source": "crm", "n": 3}`)
	h.coord.enqueue(a, []runproto.File{{Path: "main.ts", Content: []byte(nodeTask)}})
	h.start()
	requireCompleted(t, h, a.RunID, 2*time.Minute)

	stdout, stages := stdoutStages(h.coord, a.RunID)
	want := []string{"params crm 3", "uid 65532 node 22", "line 0", "line 1", "line 2", "task dir is read-only", "bye"}
	if !slices.Equal(stdout, want) {
		t.Fatalf("stdout = %q\nwant %q", stdout, want)
	}
	if stages["params crm 3"] != "" || stages["line 0"] != "fetch" || stages["task dir is read-only"] != "save" || stages["bye"] != "" {
		t.Fatalf("stage labels = %v", stages)
	}
	if stderr := linesOf(h.coord, a.RunID, runproto.StreamStderr); len(stderr) != 1 || stderr[0].Text != "to stderr" {
		t.Fatalf("stderr = %+v", stderr)
	}
	wantEvents := []string{
		"stage.start fetch",
		"stage.done fetch fetched 3",
		"stage.start save",
		"stage.done save",
		`result ok saved 3 lines {"lines":3}`,
	}
	if got := eventStrings(h.coord, a.RunID); !slices.Equal(got, wantEvents) {
		t.Fatalf("events = %q\nwant %q", got, wantEvents)
	}
	assertContainersGone(t, a.RunID)
}

func TestIntegrationNodeDependencies(t *testing.T) {
	pkg, err := os.ReadFile("testdata/node-deps/package.json")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.ReadFile("testdata/node-deps/package-lock.json")
	if err != nil {
		t.Fatal(err)
	}
	files := []runproto.File{
		{Path: "main.mjs", Content: []byte(`import isNumber from "is-number";
import { createRequire } from "node:module";
const require = createRequire(import.meta.url);
console.log("is-number", isNumber(5), require("is-number/package.json").version);
console.log("resolved", require.resolve("is-number"));
`)},
		{Path: "package.json", Content: pkg},
		{Path: "package-lock.json", Content: lock},
	}
	m := itRuntimeManifest("node", "22", "node", "main.mjs")
	m.Runtime.Lockfile = "package-lock.json"

	h := newDockerHarness(t, "", "", nil)
	first, second := newAssignment(m), newAssignment(m)
	h.coord.enqueue(first, files)
	h.start()
	requireCompleted(t, h, first.RunID, 5*time.Minute)
	logs := h.coord.logTexts(first.RunID)
	for _, want := range []string{"is-number true 7.0.0", "resolved /sbx/task/node_modules/is-number/index.js"} {
		if !slices.Contains(logs, want) {
			t.Fatalf("log lacks %q:\n%s", want, strings.Join(logs, "\n"))
		}
	}
	if !strings.HasPrefix(logs[0], "installing dependencies from package-lock.json") {
		t.Fatalf("installation not logged:\n%s", strings.Join(logs, "\n"))
	}

	h.coord.enqueue(second, files)
	requireCompleted(t, h, second.RunID, 2*time.Minute)
	logs = h.coord.logTexts(second.RunID)
	if !strings.HasPrefix(logs[0], "using cached dependency environment") || !slices.Contains(logs, "is-number true 7.0.0") {
		t.Fatalf("second run logs:\n%s", strings.Join(logs, "\n"))
	}
	h.coord.mu.Lock()
	envIDs := []string{h.coord.started[first.RunID], h.coord.started[second.RunID]}
	h.coord.mu.Unlock()
	if envIDs[0] == "" || envIDs[0] != envIDs[1] {
		t.Fatalf("env IDs = %q", envIDs)
	}
	assertContainersGone(t, first.RunID)
	assertContainersGone(t, second.RunID)
}

func TestIntegrationNodeDependencyFailure(t *testing.T) {
	m := itRuntimeManifest("node", "22", "node", "main.mjs")
	m.Runtime.Lockfile = "package-lock.json"
	h := newDockerHarness(t, "", "", nil)
	a := newAssignment(m)
	// The lock file does not match package.json, which npm ci refuses.
	h.coord.enqueue(a, []runproto.File{
		{Path: "main.mjs", Content: []byte("console.log(1)\n")},
		{Path: "package.json", Content: []byte(`{"name":"x","version":"1.0.0","dependencies":{"is-number":"7.0.0"}}`)},
		{Path: "package-lock.json", Content: []byte(`{"name":"x","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{"":{"name":"x","version":"1.0.0"}}}`)},
	})
	h.start()
	f := h.coord.waitFinish(t, a.RunID, 5*time.Minute)
	if f.Status != runproto.StatusFailed || f.ErrorCode != errDependencyInstallFailed || !strings.Contains(f.ErrorMessage, "npm ci exited with code") {
		t.Fatalf("Finish = %+v; logs:\n%s", f, strings.Join(h.coord.logTexts(a.RunID), "\n"))
	}
	h.coord.checkProtocol(t)
	assertContainersGone(t, a.RunID)
}

const shellTask = `#!/usr/bin/env bash
set -euo pipefail
echo "bash ${BASH_VERSINFO[0]} uid $(id -u)"
echo "params $LERRYN_PARAMS"
sbx stage start fetch
for i in 0 1 2; do echo "line $i"; done
echo "to stderr" >&2
sbx stage done fetch -m "fetched 3"
sbx stage start save
printf '{"lines": 3, "note": "tab\\there"}\n' > /sbx/work/result.json
if touch /sbx/task/new.txt 2>/dev/null; then echo "task dir is writable"; else echo "task dir is read-only"; fi
sbx stage done save
sbx result --status ok --summary "saved 3 lines" --data-file /sbx/work/result.json
sbx stage start broken
echo "about to fail"
false
`

func TestIntegrationShellTask(t *testing.T) {
	h := newDockerHarness(t, "", "", nil)
	a := newAssignment(itRuntimeManifest("shell", "5", "bash", "main.sh"))
	h.coord.enqueue(a, []runproto.File{{Path: "main.sh", Content: []byte(shellTask)}})
	h.start()

	f := h.coord.waitFinish(t, a.RunID, 2*time.Minute)
	if f.Status != runproto.StatusFailed || f.ErrorCode != errScriptError || f.ErrorStage != "broken" || f.ExitCode == nil || *f.ExitCode != 1 {
		t.Fatalf("Finish = %+v; logs:\n%s", f, strings.Join(h.coord.logTexts(a.RunID), "\n"))
	}
	h.coord.checkProtocol(t)

	stdout, stages := stdoutStages(h.coord, a.RunID)
	want := []string{"bash 5 uid 65532", `params {"source":"crm"}`, "line 0", "line 1", "line 2", "task dir is read-only", "about to fail"}
	if !slices.Equal(stdout, want) {
		t.Fatalf("stdout = %q\nwant %q", stdout, want)
	}
	if stages["params {\"source\":\"crm\"}"] != "" || stages["line 2"] != "fetch" || stages["task dir is read-only"] != "save" || stages["about to fail"] != "broken" {
		t.Fatalf("stage labels = %v", stages)
	}
	wantEvents := []string{
		"stage.start fetch",
		"stage.done fetch fetched 3",
		"stage.start save",
		"stage.done save",
		`result ok saved 3 lines {"lines":3,"note":"tab\there"}`,
		"stage.start broken",
	}
	if got := eventStrings(h.coord, a.RunID); !slices.Equal(got, wantEvents) {
		t.Fatalf("events = %q\nwant %q", got, wantEvents)
	}
	assertContainersGone(t, a.RunID)
}

// TestIntegrationStageLabelsLargeOutput prints 100,000 lines across two
// stages as fast as each runtime can, and checks every line carries the
// stage it was printed in. Before stage markers, the lines near the change
// could carry the neighbouring stage once the runner fell behind.
func TestIntegrationStageLabelsLargeOutput(t *testing.T) {
	const half = 50000
	tests := []struct {
		name     string
		manifest runproto.Manifest
		file     string
		code     string
	}{
		{
			name:     "python",
			manifest: itRuntimeManifest("python", "3.12", "python", "main.py"),
			file:     "main.py",
			code: fmt.Sprintf(`import sys, sbx
out = sys.stdout
with sbx.stage("one"):
    for i in range(%[1]d):
        out.write(f"line {i}\n")
sbx.stage_start("two")
for i in range(%[1]d, %[2]d):
    out.write(f"line {i}\n")
sbx.stage_done("two")
`, half, 2*half),
		},
		{
			name:     "node",
			manifest: itRuntimeManifest("node", "22", "node", "main.mjs"),
			file:     "main.mjs",
			code: fmt.Sprintf(`import * as sbx from "@lerryn/sbx";
const emit = (from, to) => {
  const chunk = [];
  for (let i = from; i < to; i++) {
    chunk.push("line " + i + "\n");
    if (chunk.length === 1000) { process.stdout.write(chunk.join("")); chunk.length = 0; }
  }
  process.stdout.write(chunk.join(""));
};
sbx.stage("one", () => emit(0, %[1]d));
sbx.stageStart("two");
emit(%[1]d, %[2]d);
sbx.stageDone("two");
`, half, 2*half),
		},
		{
			name:     "shell",
			manifest: itRuntimeManifest("shell", "5", "bash", "main.sh"),
			file:     "main.sh",
			code: fmt.Sprintf(`set -eu
sbx stage start one
seq 0 %[1]d | sed 's/^/line /'
sbx stage done one
sbx stage start two
seq %[2]d %[3]d | sed 's/^/line /'
sbx stage done two
`, half-1, half, 2*half-1),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newDockerHarness(t, "", "", nil)
			m := tc.manifest
			m.Stages = []runproto.Stage{{ID: "one", Name: "One"}, {ID: "two", Name: "Two", After: []string{"one"}}}
			a := newAssignment(m)
			h.coord.enqueue(a, []runproto.File{{Path: tc.file, Content: []byte(tc.code)}})
			h.start()
			requireCompleted(t, h, a.RunID, 3*time.Minute)

			lines := linesOf(h.coord, a.RunID, runproto.StreamStdout)
			if len(lines) != 2*half {
				t.Fatalf("%d stdout lines, want %d", len(lines), 2*half)
			}
			for i, l := range lines {
				want := "one"
				if i >= half {
					want = "two"
				}
				if l.Text != fmt.Sprintf("line %d", i) || l.Stage != want {
					t.Fatalf("stdout line %d = %q in stage %q, want stage %q", i, l.Text, l.Stage, want)
				}
			}
			for _, l := range h.coord.logLines(a.RunID) {
				if strings.Contains(l.Text, "\x1e") {
					t.Fatalf("marker left in the log: %q", l.Text)
				}
			}
			wantEvents := []string{"stage.start one", "stage.done one", "stage.start two", "stage.done two"}
			if got := eventStrings(h.coord, a.RunID); !slices.Equal(got, wantEvents) {
				t.Fatalf("events = %q", got)
			}
			assertContainersGone(t, a.RunID)
		})
	}
}
