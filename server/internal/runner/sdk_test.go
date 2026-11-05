//go:build unix

package runner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/niltavi/lerryn/runtimes/sdk"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

func sdkFiles(t *testing.T, language string) []sdk.File {
	t.Helper()
	files, err := sdk.Files(language)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestMaterializeSDK(t *testing.T) {
	parent := t.TempDir()
	wantFiles := map[string][]string{
		langPython: {"bin/sbx", "sbx/__init__.py", "sbx/__main__.py", "sbx/cli.py", "bin/sbx-exec"},
		langNode:   {"bin/sbx", "node_modules/@lerryn/sbx/package.json", "node_modules/@lerryn/sbx/index.js", "node_modules/@lerryn/sbx/index.d.ts", "node_modules/@lerryn/sbx/cli.js", "bin/sbx-exec"},
		langShell:  {"bin/sbx", "bin/sbx-exec"},
	}
	dirs := map[string]bool{}
	for lang, names := range wantFiles {
		dir, err := materializeSDK(parent, sdkFiles(t, lang))
		if err != nil {
			t.Fatal(err)
		}
		again, err := materializeSDK(parent, sdkFiles(t, lang))
		if err != nil || again != dir {
			t.Fatalf("%s: second call = %q, %v; want %q", lang, again, err, dir)
		}
		dirs[dir] = true
		for _, name := range names {
			want := os.FileMode(0o644)
			if strings.HasPrefix(name, "bin/") {
				want = 0o755
			}
			fi, err := os.Stat(filepath.Join(dir, name))
			if err != nil || fi.Mode().Perm() != want {
				t.Fatalf("%s: %s: %v, %v; want mode %v", lang, name, fi, err, want)
			}
		}
	}
	entries, _ := os.ReadDir(parent)
	if len(dirs) != 3 || len(entries) != 3 {
		t.Fatalf("sdk parent holds %d entries for %d trees", len(entries), len(dirs))
	}
	if _, err := sdk.Files("ruby"); err == nil {
		t.Fatal("sdk for an unknown runtime")
	}
}

// sdkEnv runs SDK commands on the host the way a task container would.
type sdkEnv struct {
	t         *testing.T
	eventsDir string
	env       []string
}

func newSDKEnv(t *testing.T, extra ...string) *sdkEnv {
	eventsDir := t.TempDir()
	// A proxy from the developer's environment would answer for the
	// unreachable tool and turn "cannot reach" into an HTTP error.
	env := append(withoutProxy(os.Environ()),
		"LERRYN_EVENTS_FILE="+filepath.Join(eventsDir, eventsFileName),
		`LERRYN_PARAMS={"source": "crm", "n": 3}`,
		"PYTHONDONTWRITEBYTECODE=1",
	)
	return &sdkEnv{t: t, eventsDir: eventsDir, env: append(env, extra...)}
}

// run runs args and checks the exit code; it returns stdout and stderr.
func (e *sdkEnv) run(wantCode int, args ...string) (string, string) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) // #nosec G204 -- test commands
	cmd.Env = e.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			e.t.Fatalf("%v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	if code != wantCode {
		e.t.Fatalf("%v exited %d, want %d: %s%s", args, code, wantCode, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

// events reads the events file with the runner's parser and fails on any
// warning, so the SDKs and the runner cannot drift apart.
func (e *sdkEnv) events() []runproto.Event {
	e.t.Helper()
	stages := &stageTracker{}
	logs := newLogPipeline(defaultLogLimits(), stages, nil, time.Now)
	r := newEventReader(e.eventsDir, stages, logs, time.Now)
	defer r.close()
	r.poll(true)
	if warnings := texts(mustDrain(e.t, logs)); len(warnings) != 0 {
		e.t.Fatalf("runner warnings for SDK output: %q", warnings)
	}
	for _, ev := range r.pending {
		if time.Since(ev.TS) > time.Minute {
			e.t.Errorf("event %d has timestamp %v", ev.Seq, ev.TS)
		}
	}
	return r.pending
}

func describeEvents(evs []runproto.Event) []string {
	var out []string
	for _, e := range evs {
		s := e.Kind + " " + e.Stage + " " + e.Message
		if e.Result != nil {
			s = e.Kind + " " + e.Result.Status + " " + e.Result.Summary + " " + string(e.Result.Data)
		}
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

func checkEvents(t *testing.T, got []runproto.Event, want []string) {
	t.Helper()
	if d := describeEvents(got); !slices.Equal(d, want) {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(d, "\n"), strings.Join(want, "\n"))
	}
}

func checkCut(t *testing.T, what, s string, limit int) {
	t.Helper()
	if len(s) > limit || !strings.HasSuffix(s, "…") || !utf8.ValidString(s) || len(s) < limit-8 {
		t.Fatalf("%s not cut at %d bytes on a character boundary: %d bytes, valid %t", what, limit, len(s), utf8.ValidString(s))
	}
}

// TestPythonSDK runs the embedded SDK with the host's python3. The
// container runs Python 3.12; the SDK sticks to what 3.8 already had, so
// any host python3 works.
func TestPythonSDK(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	sdkDir, err := materializeSDK(t.TempDir(), sdkFiles(t, langPython))
	if err != nil {
		t.Fatal(err)
	}
	e := newSDKEnv(t, "PYTHONPATH="+sdkDir)
	sbx := filepath.Join(sdkDir, "bin", "sbx")

	e.run(0, python, sbx, "stage", "start", "fetch")
	e.run(0, python, sbx, "stage", "done", "fetch", "--message", "got 3 records")
	dataFile := filepath.Join(t.TempDir(), "data.json")
	if err := os.WriteFile(dataFile, []byte(`{"files": ["a.md"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e.run(0, python, sbx, "result", "--status", "partial", "--summary", "1 of 2", "--data-file", dataFile)
	e.run(0, python, sbx, "result", "--status", "ok", "--summary", "json", "--data-json", `[1, 2]`)
	script := `
import sbx
p = sbx.params()
assert p == {"source": "crm", "n": 3}, p
with sbx.stage("save"):
    pass
sbx.result("ok", "é" * 3000)
with sbx.stage("boom"):
    raise ValueError("bad input")
`
	if _, stderr := e.run(1, python, "-c", script); !strings.Contains(stderr, "ValueError: bad input") {
		t.Fatalf("exception not re-raised: %s", stderr)
	}
	if _, stderr := e.run(1, python, sbx, "stage", "start", "Bad Id"); !strings.Contains(stderr, "stage id") {
		t.Fatalf("bad stage id accepted: %s", stderr)
	}
	if _, stderr := e.run(1, python, sbx, "result", "--status", "ok", "--summary", "s", "--data-json", "{bad"); !strings.Contains(stderr, "sbx:") {
		t.Fatalf("bad JSON accepted: %s", stderr)
	}
	e.run(2, python, sbx, "result", "--status", "great", "--summary", "s")
	e.run(0, python, "-m", "sbx", "stage", "fail", "last", "-m", "via -m")

	evs := e.events()
	checkCut(t, "summary", evs[6].Result.Summary, maxEventSummaryBytes)
	evs[6].Result.Summary = "long"
	checkEvents(t, evs, []string{
		"stage.start fetch",
		"stage.done fetch got 3 records",
		`result partial 1 of 2 {"files":["a.md"]}`,
		"result ok json [1,2]",
		"stage.start save",
		"stage.done save",
		"result ok long",
		"stage.start boom",
		"stage.fail boom ValueError: bad input",
		"stage.fail last via -m",
	})
}

// TestShellSDK runs the POSIX sh command with the host's sh and awk. On
// Linux that is usually dash with mawk or gawk, on macOS bash and BSD awk;
// the integration tests cover the runtime images' busybox and mawk.
func TestShellSDK(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not installed")
	}
	sdkDir, err := materializeSDK(t.TempDir(), sdkFiles(t, langShell))
	if err != nil {
		t.Fatal(err)
	}
	e := newSDKEnv(t)
	sbx := filepath.Join(sdkDir, "bin", "sbx")
	run := func(code int, args ...string) string {
		t.Helper()
		_, stderr := e.run(code, append([]string{sh, sbx}, args...)...)
		return stderr
	}

	run(0, "stage", "start", "fetch")
	run(0, "stage", "done", "fetch", "--message", "got \"3\"\trecords\n\x01ok \\ é")
	run(0, "stage", "fail", "save", "-m", strings.Repeat("é", 600))
	run(0, "stage", "start", "x-1_y", "--message=")
	dataFile := filepath.Join(t.TempDir(), "data.json")
	if err := os.WriteFile(dataFile, []byte("{\n  \"files\" : [ \"a.md\", \"\\u00e9\\n\" ],\n  \"n\": -1.5e+3, \"ok\": true, \"none\": null, \"o\": {}, \"a\": []\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(0, "result", "--status", "partial", "--summary", "1 of 2", "--data-file", dataFile)
	run(0, "result", "--status=ok", "--summary", strings.Repeat("é", 1500), "--data-json", `"é"`)
	run(0, "result", "--status", "failed", "--summary", "")

	for _, bad := range []string{"{bad", "[1,]", "01", "1.", `"\x"`, "\"a\x01\"", "[1] 2", "tru", `{"a" 1}`, ""} {
		if stderr := run(1, "result", "--status", "ok", "--summary", "s", "--data-json", bad); !strings.Contains(stderr, "not valid JSON") {
			t.Fatalf("data %q: %s", bad, stderr)
		}
	}
	big := "[" + strings.Repeat(`"abcdefghij",`, 6000) + "1]"
	if stderr := run(1, "result", "--status", "ok", "--summary", "s", "--data-json", big); !strings.Contains(stderr, "the limit is 65536") {
		t.Fatalf("large data accepted: %s", stderr)
	}
	if stderr := run(1, "stage", "start", "Bad"); !strings.Contains(stderr, "stage id") {
		t.Fatalf("bad stage id accepted: %s", stderr)
	}
	run(1, "result", "--status", "ok", "--summary", "s", "--data-file", filepath.Join(t.TempDir(), "missing.json"))
	run(2, "result", "--status", "great", "--summary", "s")
	run(2, "result", "--status", "ok")
	run(2, "result", "--status", "ok", "--summary", "s", "--data-json", "1", "--data-file", dataFile)
	run(2, "stage", "begin", "x")
	run(2, "stage", "start")
	run(2, "stage", "start", "a", "b")
	run(2, "nothing")
	run(2)
	if out, _ := e.run(0, sh, sbx, "--help"); !strings.Contains(out, "usage: sbx stage") {
		t.Fatalf("help = %q", out)
	}

	evs := e.events()
	checkCut(t, "message", evs[2].Message, maxEventMessageBytes)
	checkCut(t, "summary", evs[5].Result.Summary, maxEventSummaryBytes)
	evs[2].Message, evs[5].Result.Summary = "long", "long"
	checkEvents(t, evs, []string{
		"stage.start fetch",
		"stage.done fetch got \"3\"\trecords\n\x01ok \\ é",
		"stage.fail save long",
		"stage.start x-1_y",
		`result partial 1 of 2 {"files":["a.md","\u00e9\n"],"n":-1.5e+3,"ok":true,"none":null,"o":{},"a":[]}`,
		`result ok long "é"`,
		"result failed",
	})
}

// TestNodeSDK runs the Node module with the host's node.
func TestNodeSDK(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	sdkDir, err := materializeSDK(t.TempDir(), sdkFiles(t, langNode))
	if err != nil {
		t.Fatal(err)
	}
	e := newSDKEnv(t)
	// The task directory sits below the SDK's node_modules, as /sbx/task
	// sits below /sbx/node_modules in the container.
	taskDir := filepath.Join(sdkDir, "task")
	if err := os.Mkdir(taskDir, 0o750); err != nil {
		t.Fatal(err)
	}
	script := `
import * as sbx from "@lerryn/sbx";
import assert from "node:assert";

assert.deepStrictEqual(sbx.params(), { source: "crm", n: 3 });
sbx.stageStart("fetch");
sbx.stageDone("fetch", "got 3 records");
assert.strictEqual(sbx.stage("sync", () => 42), 42);
assert.strictEqual(await sbx.stage("async", async () => "v"), "v");
await assert.rejects(sbx.stage("rejects", async () => { throw new TypeError("nope"); }));
assert.throws(() => sbx.stage("throws", () => { throw new Error("bad input"); }));
assert.throws(() => sbx.stageStart("Bad Id"), /stage id/);
assert.throws(() => sbx.result("great", "s"), /status/);
assert.throws(() => sbx.result("ok", "s", "x".repeat(70000)), /limit/);
sbx.result("partial", "é".repeat(1500), { files: ["a.md"], n: 1 });
sbx.result("ok", "no data");
`
	main := filepath.Join(taskDir, "main.mjs")
	if err := os.WriteFile(main, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	e.run(0, node, main)

	evs := e.events()
	checkCut(t, "summary", evs[10].Result.Summary, maxEventSummaryBytes)
	evs[10].Result.Summary = "long"
	checkEvents(t, evs, []string{
		"stage.start fetch",
		"stage.done fetch got 3 records",
		"stage.start sync",
		"stage.done sync",
		"stage.start async",
		"stage.done async",
		"stage.start rejects",
		"stage.fail rejects TypeError: nope",
		"stage.start throws",
		"stage.fail throws Error: bad input",
		`result partial long {"files":["a.md"],"n":1}`,
		"result ok no data",
	})
}

// TestSDKMarkers checks that every SDK writes a stage marker on stdout that
// the runner recognises, carrying the same mark as the event in the file,
// and that none writes one without a token.
func TestSDKMarkers(t *testing.T) {
	type writer struct {
		name     string
		language string
		tool     string
		args     func(sdkDir string) []string
	}
	writers := []writer{
		{"python command", langPython, "python3", func(d string) []string { return []string{filepath.Join(d, "bin", "sbx"), "stage", "start", "fetch"} }},
		{"python module", langPython, "python3", func(string) []string {
			return []string{"-c", `import sbx; print("before", end=""); sbx.stage_start("fetch")`}
		}},
		{"sh command", langShell, "sh", func(d string) []string { return []string{filepath.Join(d, "bin", "sbx"), "stage", "start", "fetch"} }},
		{"node module", langNode, "node", func(string) []string {
			return []string{"--input-type=module", "-e", `const m = await import(process.env.SBX_MODULE); process.stdout.write("before"); m.stageStart("fetch");`}
		}},
	}
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			tool, err := exec.LookPath(w.tool)
			if err != nil {
				t.Skip(w.tool + " not installed")
			}
			sdkDir, err := materializeSDK(t.TempDir(), sdkFiles(t, w.language))
			if err != nil {
				t.Fatal(err)
			}
			module := "PYTHONPATH=" + sdkDir
			if w.language == langNode {
				module = "SBX_MODULE=" + filepath.Join(sdkDir, "node_modules", "@lerryn", "sbx", "index.js")
			}
			args := append([]string{tool}, w.args(sdkDir)...)

			e := newSDKEnv(t, module, "LERRYN_LOG_MARKER=T0KEN")
			stdout, _ := e.run(0, args...)
			evs := e.events()
			raw, err := os.ReadFile(filepath.Join(e.eventsDir, eventsFileName))
			if err != nil {
				t.Fatal(err)
			}
			_, mark, _, err := parseEvent(bytes.TrimSpace(raw), time.Now())
			if err != nil || mark == "" || len(evs) != 1 {
				t.Fatalf("event %q: mark %q, %v", raw, mark, err)
			}

			// The runner strips the marker, keeps text before it on the
			// same line as a line of its own, and labels what follows.
			p := newLogPipeline(defaultLogLimits(), &stageTracker{}, nil, time.Now)
			p.useMarkers("T0KEN")
			out, flush := p.taskOutput()
			_, _ = out.Stdout.Write([]byte(stdout + "after\n"))
			p.fileEvent(evs[0], mark)
			flush()
			var got []string
			for _, l := range mustDrain(t, p) {
				got = append(got, l.Text+"@"+l.Stage)
			}
			want := []string{"after@fetch"}
			if strings.HasPrefix(stdout, "before") {
				want = []string{"before@", "after@fetch"}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("stdout %q gave lines %q, want %q", stdout, got, want)
			}
			if len(p.marks.seen) != 0 || len(p.marks.awaiting) != 0 {
				t.Fatalf("marker and event not paired: %+v", p.marks)
			}

			e = newSDKEnv(t, module)
			if stdout, _ := e.run(0, args...); strings.Contains(stdout, "\x1e") {
				t.Fatalf("marker without a token: %q", stdout)
			}
			raw, _ = os.ReadFile(filepath.Join(e.eventsDir, eventsFileName))
			if bytes.Contains(raw, []byte(`"mark"`)) {
				t.Fatalf("mark without a token: %s", raw)
			}
		})
	}
}

// withoutProxy drops the proxy variables curl, Python and Node read.
func withoutProxy(env []string) []string {
	var out []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		switch strings.ToLower(name) {
		case "http_proxy", "https_proxy", "all_proxy", "no_proxy":
			continue
		}
		out = append(out, kv)
	}
	return out
}
