package runner

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/executor"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

func TestToolEnv(t *testing.T) {
	if env := toolEnv(nil); env != nil {
		t.Fatalf("no tools = %q", env)
	}
	got := toolEnv([]runproto.Tool{
		{Name: "crm-mcp", Kind: "mcp", URL: "https://crm.example.com/mcp"},
		{Name: "weather", Kind: "http", URL: "https://w.example.com/\nX=1"},
	})
	want := []string{
		"LERRYN_TOOL_CRM_MCP_URL=https://crm.example.com/mcp",
		"LERRYN_TOOL_WEATHER_URL=https://w.example.com/X=1",
		`LERRYN_TOOLS=[{"name":"crm-mcp","kind":"mcp","url":"https://crm.example.com/mcp"},{"name":"weather","kind":"http","url":"https://w.example.com/\nX=1"}]`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("env = %q\nwant %q", got, want)
	}

	got = toolEnv([]runproto.Tool{{Name: "price-lib", Kind: "script", TreeHash: "ab12", Files: []runproto.File{{Path: "a.py"}}}})
	want = []string{
		"LERRYN_TOOL_PRICE_LIB_DIR=/sbx/tools/price-lib",
		`LERRYN_TOOLS=[{"name":"price-lib","kind":"script","dir":"/sbx/tools/price-lib","tree_hash":"ab12"}]`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("script tool env = %q\nwant %q", got, want)
	}
}

// A script tool's files are written into the run directory and mounted
// read-only at /sbx/tools/<name>; the log names the tree.
func TestRunWithScriptTool(t *testing.T) {
	h := newHarness(t, nil)
	var spec executor.Spec
	var content []byte
	var mode fs.FileMode
	h.ex.behave = func(c *fakeContainer) {
		spec = c.spec
		for _, m := range c.spec.Mounts {
			if m.Target == "/sbx/tools/price-lib" {
				name := filepath.Join(m.Source, "bin", "fmt")
				content, _ = os.ReadFile(name) // #nosec G304 -- the test's own run directory
				if fi, err := os.Stat(name); err == nil {
					mode = fi.Mode().Perm()
				}
			}
		}
		c.exit(0, false)
	}
	m := pyManifest()
	m.Tools = []runproto.Tool{{Name: "price-lib", Kind: "script", TreeHash: strings.Repeat("c", 64),
		Files: []runproto.File{{Path: "bin/fmt", Content: []byte("#!/bin/sh\n"), Executable: true}}}}
	a := newAssignment(m)
	h.coord.enqueue(a, mainPy)
	h.start()
	h.coord.waitFinish(t, a.RunID, testTimeout)
	var mount *executor.Mount
	for i := range spec.Mounts {
		if spec.Mounts[i].Target == "/sbx/tools/price-lib" {
			mount = &spec.Mounts[i]
		}
	}
	if mount == nil || !mount.ReadOnly || string(content) != "#!/bin/sh\n" || mode != 0o755 {
		t.Fatalf("mount = %+v, content %q, mode %v", mount, content, mode)
	}
	if !slices.Contains(spec.Env, "LERRYN_TOOL_PRICE_LIB_DIR=/sbx/tools/price-lib") {
		t.Fatalf("env = %q", spec.Env)
	}
	if !slices.ContainsFunc(h.coord.logTexts(a.RunID), func(l string) bool {
		return strings.Contains(l, "tool price-lib: script tool sha256:cccccccccccc, 1 files")
	}) {
		t.Fatalf("log = %q", h.coord.logTexts(a.RunID))
	}
}

// A tool name that is not a single path element never reaches the file
// system; the server never sends one.
func TestScriptToolBadName(t *testing.T) {
	h := newHarness(t, nil)
	m := pyManifest()
	m.Tools = []runproto.Tool{{Name: "../x", Kind: "script", Files: []runproto.File{{Path: "a", Content: []byte("x")}}}}
	a := newAssignment(m)
	h.coord.enqueue(a, mainPy)
	h.start()
	if f := h.coord.waitFinish(t, a.RunID, testTimeout); f.Status != runproto.StatusFailed || f.ErrorCode != "internal_error" {
		t.Fatalf("finish = %+v", f)
	}
}

// The tools of an assignment reach the container's environment.
func TestRunWithTools(t *testing.T) {
	h := newHarness(t, nil)
	var env []string
	h.ex.behave = func(c *fakeContainer) { env = c.spec.Env; c.exit(0, false) }
	m := pyManifest()
	m.Tools = []runproto.Tool{{Name: "crm-mcp", Kind: "mcp", URL: "https://crm.example.com/mcp"}}
	a := newAssignment(m)
	h.coord.enqueue(a, mainPy)
	h.start()
	h.coord.waitFinish(t, a.RunID, testTimeout)
	if !slices.Contains(env, "LERRYN_TOOL_CRM_MCP_URL=https://crm.example.com/mcp") {
		t.Fatalf("env = %q", env)
	}
}
