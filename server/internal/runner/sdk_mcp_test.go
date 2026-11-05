//go:build unix

package runner

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The three sbx implementations call a registered MCP tool the same way
// and record the same tool.call events, against a server answering in
// JSON and one answering as SSE. These run on the host; the integration
// tests repeat the calls inside the runtime containers.
func TestSDKMCPCall(t *testing.T) {
	type impl struct {
		name, language string
		// cmd returns the sbx command line for the materialized SDK.
		cmd  func(dir string) []string
		need string
	}
	impls := []impl{
		{"python", langPython, func(dir string) []string { return []string{"python3", filepath.Join(dir, "bin", "sbx")} }, "python3"},
		{"sh with curl", langShell, func(dir string) []string { return []string{"sh", filepath.Join(dir, "bin", "sbx")} }, "curl"},
		{"sh delegating to node", langNode, func(dir string) []string { return []string{"sh", filepath.Join(dir, "bin", "sbx")} }, "node"},
	}
	for _, im := range impls {
		for _, jsonResponse := range []bool{false, true} {
			mode := "sse"
			if jsonResponse {
				mode = "json"
			}
			t.Run(im.name+"/"+mode, func(t *testing.T) {
				if _, err := exec.LookPath(im.need); err != nil {
					t.Skip(im.need + " not installed")
				}
				dir, err := materializeSDK(t.TempDir(), sdkFiles(t, im.language))
				if err != nil {
					t.Fatal(err)
				}
				port := startCalcServer(t, "127.0.0.1:0", jsonResponse)
				e := newSDKEnv(t, toolEnv(calcTools("127.0.0.1", port))...)
				runMCPCalls(t, e, im.cmd(dir))
			})
		}
	}
}

// runMCPCalls runs the call sequence wantToolCalls expects.
func runMCPCalls(t *testing.T, e *sdkEnv, sbx []string) {
	t.Helper()
	call := func(code int, args ...string) (string, string) {
		t.Helper()
		return e.run(code, append(append(append([]string{}, sbx...), "mcp", "call"), args...)...)
	}
	auth := "Authorization: Bearer " + mcpTestToken

	out, _ := call(0, "calc", "add", "--args", `{"a": 2, "b": 3}`, "-H", auth)
	var res struct {
		Content []struct {
			Type, Text string
		}
		StructuredContent struct{ Sum int }
		IsError           bool
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.StructuredContent.Sum != 5 || res.IsError ||
		len(res.Content) != 1 || res.Content[0].Text != "5" {
		t.Fatalf("add printed %q (%v)", out, err)
	}
	out, stderr := call(1, "calc", "fail", "--header", auth)
	if !strings.Contains(out, `"isError"`) || !strings.Contains(stderr, "quota") {
		t.Fatalf("fail printed %q, %q", out, stderr)
	}
	if _, stderr = call(1, "nope", "x"); !strings.Contains(stderr, "not one of this run's tools") {
		t.Fatalf("unknown tool: %q", stderr)
	}
	call(1, "prices", "x")
	call(1, "gone", "x", "--timeout", "5")
	if _, stderr = call(1, "calc", "add", "--args", `{"a":1}`); !strings.Contains(stderr, "401") {
		t.Fatalf("without the token: %q", stderr)
	}
	// Bad usage is not a call and records nothing.
	call(1, "calc", "add", "--args", `[1]`, "-H", auth)
	call(1, "calc", "add", "--args", `{"a":`, "-H", auth)
	call(2, "calc")
	call(2, "calc", "add", "--args", "{}", "--args-file", "x.json")
	call(2, "calc", "add", "--header", "no colon")

	wantToolCalls(t, describeToolCalls(e.events()))
}
