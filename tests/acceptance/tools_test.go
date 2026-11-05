//go:build integration

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolToken is the value Alice fills in for the task's credential; the
// test MCP server accepts it as a bearer token.
const toolToken = "calc-acceptance-token-31"

// startCalcMCP serves an MCP server with one tool, "add", requiring
// Authorization: Bearer toolToken, on all interfaces so task containers
// reach it. It returns the port.
func startCalcMCP(t *testing.T) int {
	t.Helper()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "calc", Version: "1"}, nil)
	type in struct {
		A     int    `json:"a"`
		B     int    `json:"b"`
		Token string `json:"token,omitempty"`
	}
	type out struct {
		Sum int `json:"sum"`
	}
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "add", Description: "a + b"}, func(_ context.Context, _ *mcpsdk.CallToolRequest, v in) (*mcpsdk.CallToolResult, out, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: fmt.Sprint(v.A + v.B)}}}, out{Sum: v.A + v.B}, nil
	})
	h := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, &mcpsdk.StreamableHTTPOptions{DisableLocalhostProtection: true})
	l, err := net.Listen("tcp", ":0") // #nosec G102 -- task containers reach this ephemeral test MCP endpoint through the host gateway
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+toolToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})}
	go func() { _ = hs.Serve(l) }()
	t.Cleanup(func() { _ = hs.Close() })
	return l.Addr().(*net.TCPAddr).Port
}

// taskHost is where task containers on the default task network reach
// this process: LERRYN_TEST_CONTAINER_HOST, host.docker.internal on
// Docker Desktop and OrbStack, or the network's gateway on Linux.
func taskHost(t *testing.T) string {
	t.Helper()
	if h := os.Getenv("LERRYN_TEST_CONTAINER_HOST"); h != "" {
		return h
	}
	if runtime.GOOS == "darwin" {
		return "host.docker.internal"
	}
	inspect := func() (string, error) {
		out, err := exec.Command("docker", "network", "inspect", "lerryn-tasks", "-f", "{{range .IPAM.Config}}{{.Gateway}} {{end}}").Output()
		return strings.TrimSpace(string(out)), err
	}
	gw, err := inspect()
	if err != nil {
		// Created as the executor creates it, which then uses it.
		if out, err := exec.Command("docker", "network", "create", "--driver", "bridge",
			"-o", "com.docker.network.bridge.enable_icc=false", "--label", "lerryn.managed=true", "lerryn-tasks").CombinedOutput(); err != nil {
			t.Fatalf("create the task network: %v: %s", err, out)
		}
		if gw, err = inspect(); err != nil {
			t.Fatal(err)
		}
	}
	return strings.Fields(gw)[0]
}

// A14, the tools part: a script tool an agent registers is mounted into
// the run, and a registered MCP server is called from the task with
// sbx mcp call, the token coming from a credential. The call appears in
// the run's events as a tool.call record with the token redacted, over
// the API and over MCP.
func TestA14ScriptToolAndToolCalls(t *testing.T) {
	port := startCalcMCP(t)
	host := taskHost(t)
	s := startServer(t, 1)
	alice := s.signUp("alice")
	agent := (&client{s: &instance{t: t, base: s.base}, token: alice.token}).mcp(t)

	var script struct {
		Kind     string `json:"kind"`
		TreeHash string `json:"tree_hash"`
		Files    []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	body, _ := json.Marshal(agent.ok(t, "api_call", map[string]any{"operation_id": "registerTool", "arguments": map[string]any{
		"project_id": alice.project, "name": "fmt-lib", "kind": "script", "description": "格式化金额：python3 /sbx/tools/fmt-lib/fmt.py <数字>",
		"files": []any{map[string]any{"path": "fmt.py", "content": "import sys\nprint(f'{int(sys.argv[1]):,} 元')\n"}},
	}}))
	if err := json.Unmarshal(jsonBody(t, body), &script); err != nil || script.Kind != "script" || len(script.TreeHash) != 64 ||
		len(script.Files) != 1 || script.Files[0].Path != "fmt.py" {
		t.Fatalf("registerTool over MCP = %s (%v)", body, err)
	}
	alice.api(http.MethodPost, "/api/v1/projects/"+alice.project+"/tools", map[string]any{
		"name": "calc", "kind": "mcp", "description": "加法：add(a, b)，需要 Authorization: Bearer <API_TOKEN>",
		"url": fmt.Sprintf("http://%s:%d/mcp", host, port),
	}, http.StatusOK, nil)

	tk := alice.applyTask("uses-tools", `
import os, subprocess, sbx
print(subprocess.run(["python3", os.environ["LERRYN_TOOL_FMT_LIB_DIR"] + "/fmt.py", "1234567"], capture_output=True, text=True).stdout.strip())
with sbx.stage("call"):
    res = sbx.mcp_call("calc", "add", {"a": 40, "b": 2, "token": os.environ["API_TOKEN"]},
                       headers={"Authorization": "Bearer " + os.environ["API_TOKEN"]})
print("sum", res["structuredContent"]["sum"])
`, map[string]any{"network": "full", "tools": []string{"calc", "fmt-lib"}, "stages": []any{map[string]any{"id": "call", "name": "调用"}}})
	req := alice.requestCredential(tk.ID)
	alice.fill(req.URL, map[string]string{"API_TOKEN": toolToken})

	r := alice.startRun(tk.ID)
	final, runBody := alice.waitRun(r.ID, true)
	_, lines := alice.runLogs(r.ID)
	if final.Status != "completed" || !contains(lines, "1,234,567 元") || !contains(lines, "sum 42") {
		t.Fatalf("run = %s\nlogs:\n%s", runBody, strings.Join(lines, "\n"))
	}

	var events struct {
		Events []struct {
			Kind  string          `json:"kind"`
			Stage *string         `json:"stage"`
			Data  json.RawMessage `json:"data"`
		} `json:"events"`
	}
	eventsBody := alice.api(http.MethodGet, "/api/v1/runs/"+r.ID+"/events?kind=tool.call", nil, http.StatusOK, &events)
	if len(events.Events) != 1 || events.Events[0].Stage == nil || *events.Events[0].Stage != "call" {
		t.Fatalf("tool.call events = %s", eventsBody)
	}
	var rec struct {
		Tool        string  `json:"tool"`
		Method      string  `json:"method"`
		OK          bool    `json:"ok"`
		ArgsSummary *string `json:"args_summary"`
		Error       *string `json:"error"`
	}
	if err := json.Unmarshal(events.Events[0].Data, &rec); err != nil || rec.Tool != "calc" || rec.Method != "add" || !rec.OK ||
		rec.ArgsSummary == nil || !strings.Contains(*rec.ArgsSummary, `"token":"[REDACTED]"`) || rec.Error != nil {
		t.Fatalf("tool call record = %s (%v)", events.Events[0].Data, err)
	}
	mcpBody, _ := json.Marshal(agent.ok(t, "api_call", map[string]any{"operation_id": "listRunEvents", "arguments": map[string]any{"run_id": r.ID}}))
	if !strings.Contains(string(mcpBody), "tool.call") {
		t.Fatalf("listRunEvents over MCP = %s", mcpBody)
	}
	for name, text := range map[string]string{"events": eventsBody, "MCP": string(mcpBody), "logs": strings.Join(lines, "\n"), "run": runBody} {
		if strings.Contains(text, toolToken) {
			t.Errorf("%s contains the token", name)
		}
	}
}

// jsonBody returns the body of an api_call result: the structured
// content's "body" when there is one, else the marshalled result.
func jsonBody(t *testing.T, raw []byte) []byte {
	t.Helper()
	var r struct {
		StructuredContent struct {
			Body json.RawMessage `json:"body"`
		} `json:"structuredContent"`
		Body json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(raw, &r); err == nil {
		if len(r.StructuredContent.Body) > 0 {
			return r.StructuredContent.Body
		}
		if len(r.Body) > 0 {
			return r.Body
		}
	}
	return raw
}
