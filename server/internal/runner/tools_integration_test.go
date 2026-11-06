//go:build integration

package runner

import (
	"context"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/niltavi/lerryn/server/internal/executor"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

// envTestContainerHost overrides the address task containers reach the
// test process at.
const envTestContainerHost = "LERRYN_TEST_CONTAINER_HOST"

// containerHost returns the address at which a container on the test task
// network reaches a server of the test process: host.docker.internal on
// Docker Desktop and OrbStack, the network's gateway on Linux.
func containerHost(t *testing.T) string {
	t.Helper()
	if h := os.Getenv(envTestContainerHost); h != "" {
		return h
	}
	if runtime.GOOS == "darwin" {
		return "host.docker.internal"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	name := "lerryn-test-" + itID
	res, err := itAPI.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if err != nil {
		// Created the way the executor creates it, so it adopts this one.
		if _, err := itAPI.NetworkCreate(ctx, name, client.NetworkCreateOptions{
			Driver:  network.NetworkBridge,
			Labels:  map[string]string{testLabel: itID, executor.LabelManaged: "true"},
			Options: map[string]string{"com.docker.network.bridge.enable_icc": "false"},
		}); err != nil {
			t.Fatalf("create the task network: %v", err)
		}
		if res, err = itAPI.NetworkInspect(ctx, name, client.NetworkInspectOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range res.Network.IPAM.Config {
		if c.Gateway.Is4() {
			return c.Gateway.String()
		}
	}
	t.Fatalf("network %s has no IPv4 gateway", name)
	return ""
}

// The tools of a run: the calc MCP server, called with the token from
// a credential, a script tool, an HTTP tool and an unreachable one.
func toolRunManifest(t *testing.T, m runproto.Manifest, port int) runproto.Manifest {
	t.Helper()
	m.Network = "full"
	m.Stages = []runproto.Stage{{ID: "call", Name: "Call"}}
	m.Credentials = []runproto.CredentialRef{{Name: "CALC", As: "env"}}
	m.Tools = append(calcTools(containerHost(t), port), runproto.Tool{
		Name: "lib", Kind: runproto.ToolScript, TreeHash: strings.Repeat("ab", 32),
		Files: []runproto.File{{Path: "hello.txt", Content: []byte("hello from the script tool\n")}},
	})
	return m
}

func calcSecrets() []runproto.Secret {
	return []runproto.Secret{{Name: "CALC", As: "env", Fields: []runproto.SecretField{{Name: "CALC_TOKEN", Value: mcpTestToken, Sensitive: true}}}}
}

const pythonToolTask = `
import json, os, subprocess, sys
import sbx

lib = os.environ["LERRYN_TOOL_LIB_DIR"]
print(open(lib + "/hello.txt").read().strip())
try:
    open(lib + "/new.txt", "w")
    print("tool dir writable")
except OSError:
    print("tool dir read-only")
auth = "Authorization: Bearer " + os.environ["CALC_TOKEN"]
with sbx.stage("call"):
    out = subprocess.run(["sbx", "mcp", "call", "calc", "add", "--args", '{"a": 2, "b": 3}', "-H", auth],
                         check=True, capture_output=True, text=True).stdout
    print("sum", json.loads(out)["structuredContent"]["sum"])
    subprocess.run(["sbx", "mcp", "call", "calc", "add", "--args", json.dumps({"token": os.environ["CALC_TOKEN"], "a": 1, "b": 1}),
                    "-H", auth], check=True, capture_output=True)
    r = subprocess.run(["sbx", "mcp", "call", "calc", "fail", "-H", auth], capture_output=True)
    print("fail exit", r.returncode)
    res = sbx.mcp_call("calc", "add", {"a": 4, "b": 5}, headers={"Authorization": auth.split(": ", 1)[1]})
    print("api sum", res["structuredContent"]["sum"])
`

const nodeToolTask = `
import { execFileSync, spawnSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import * as sbx from "@lerryn/sbx";

const lib = process.env.LERRYN_TOOL_LIB_DIR;
console.log(readFileSync(lib + "/hello.txt", "utf8").trim());
try {
  writeFileSync(lib + "/new.txt", "x");
  console.log("tool dir writable");
} catch {
  console.log("tool dir read-only");
}
const token = process.env.CALC_TOKEN;
const auth = "Authorization: Bearer " + token;
await sbx.stage("call", async () => {
  const out = execFileSync("sbx", ["mcp", "call", "calc", "add", "--args", '{"a": 2, "b": 3}', "-H", auth], { encoding: "utf8" });
  console.log("sum", JSON.parse(out).structuredContent.sum);
  execFileSync("sbx", ["mcp", "call", "calc", "add", "--args", JSON.stringify({ token, a: 1, b: 1 }), "-H", auth]);
  const r = spawnSync("sbx", ["mcp", "call", "calc", "fail", "-H", auth]);
  console.log("fail exit", r.status);
  const res = await sbx.mcpCall("calc", "add", { a: 4, b: 5 }, { headers: { Authorization: "Bearer " + token } });
  console.log("api sum", res.structuredContent.sum);
});
`

// The shell runtime has busybox wget, no curl and no Node.
const shellToolTask = `
cat "$LERRYN_TOOL_LIB_DIR/hello.txt"
if (echo x >"$LERRYN_TOOL_LIB_DIR/new.txt") 2>/dev/null; then echo "tool dir writable"; else echo "tool dir read-only"; fi
auth="Authorization: Bearer $CALC_TOKEN"
sbx stage start call
out=$(sbx mcp call calc add --args '{"a": 2, "b": 3}' -H "$auth") || exit 1
case $out in *'"sum":5'*) echo "sum 5" ;; *) echo "unexpected: $out" ;; esac
sbx mcp call calc add --args "{\"token\":\"$CALC_TOKEN\",\"a\":1,\"b\":1}" -H "$auth" >/dev/null || exit 1
sbx mcp call calc fail -H "$auth" >/dev/null 2>&1
echo "fail exit $?"
out=$(sbx mcp call calc add --args '{"a":4,"b":5}' -H "$auth") || exit 1
case $out in *'"sum":9'*) echo "api sum 9" ;; *) echo "unexpected: $out" ;; esac
sbx stage done call
`

// A registered MCP server is called from each runtime with sbx mcp call
// (and the Python and Node APIs) over the task network; every call is
// recorded as a tool.call event in the stage it was made in, with the
// credential in the arguments redacted. The script tool is mounted
// read-only.
func TestIntegrationToolCalls(t *testing.T) {
	skipInsideContainer(t, "the task containers reach the host's gateway, not this container, so they cannot reach the test's MCP server")
	port := startCalcServer(t, ":0", false)
	cases := []struct {
		name     string
		manifest runproto.Manifest
		files    []runproto.File
	}{
		{"python", itManifest(), pyFiles(pythonToolTask)},
		{"node", itRuntimeManifest("node", "22", "node", "main.mjs"), []runproto.File{{Path: "main.mjs", Content: []byte(nodeToolTask)}}},
		{"shell", itRuntimeManifest("shell", "5", "sh", "main.sh"), []runproto.File{{Path: "main.sh", Content: []byte(shellToolTask)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newDockerHarness(t, "", "", nil)
			a := newAssignment(toolRunManifest(t, tc.manifest, port))
			h.coord.secrets[a.RunID] = calcSecrets()
			h.coord.enqueue(a, tc.files)
			h.start()
			requireCompleted(t, h, a.RunID, 3*time.Minute)

			logs := h.coord.logTexts(a.RunID)
			for _, want := range []string{"hello from the script tool", "tool dir read-only", "sum 5", "fail exit 1", "api sum 9"} {
				if !slices.Contains(logs, want) {
					t.Fatalf("missing %q in logs:\n%s", want, strings.Join(logs, "\n"))
				}
			}
			if !slices.ContainsFunc(logs, func(l string) bool { return strings.Contains(l, "tool lib: script tool sha256:abababababab") }) {
				t.Errorf("the log does not name the script tool's tree:\n%s", strings.Join(logs, "\n"))
			}
			var calls []runproto.Event
			for _, e := range h.coord.runEvents(a.RunID) {
				if e.Kind == runproto.EventToolCall {
					calls = append(calls, e)
				}
			}
			got := describeToolCalls(calls)
			want := []string{
				`calc add ok=true args={"a":2,"b":3}`,
				`calc add ok=true args={"token":"[REDACTED]","a":1,"b":1}`,
				`calc fail ok=false args={} error=quota "exceeded" for today`,
				`calc add ok=true args={"a":4,"b":5}`,
			}
			if !slices.Equal(got, want) {
				t.Fatalf("tool calls = %q\nwant %q", got, want)
			}
			for _, c := range calls {
				if c.Stage != "call" || c.ToolCall.DurationMillis < 0 {
					t.Errorf("call %+v: stage %q", c.ToolCall, c.Stage)
				}
			}
			for _, l := range logs {
				if strings.Contains(l, mcpTestToken) {
					t.Errorf("the token reached the log: %q", l)
				}
			}
			assertContainersGone(t, a.RunID)
		})
	}
}

// Without network the call cannot reach the server, and says so in the
// record; the script tool still works.
func TestIntegrationToolCallWithoutNetwork(t *testing.T) {
	port := startCalcServer(t, ":0", true)
	h := newDockerHarness(t, "", "", nil)
	m := toolRunManifest(t, itManifest(), port)
	m.Network = "none"
	a := newAssignment(m)
	h.coord.secrets[a.RunID] = calcSecrets()
	h.coord.enqueue(a, pyFiles(`
import os, subprocess
print(open(os.environ["LERRYN_TOOL_LIB_DIR"] + "/hello.txt").read().strip())
r = subprocess.run(["sbx", "mcp", "call", "calc", "add", "--timeout", "5", "-H", "Authorization: Bearer " + os.environ["CALC_TOKEN"]])
print("exit", r.returncode)
`))
	h.start()
	requireCompleted(t, h, a.RunID, 2*time.Minute)
	logs := h.coord.logTexts(a.RunID)
	if !slices.Contains(logs, "hello from the script tool") || !slices.Contains(logs, "exit 1") {
		t.Fatalf("logs:\n%s", strings.Join(logs, "\n"))
	}
	got := describeToolCalls(h.coord.runEvents(a.RunID))
	if len(got) != 1 || !strings.HasPrefix(got[0], "calc add ok=false args={} error=cannot reach the tool") {
		t.Fatalf("tool calls = %q", got)
	}
	assertContainersGone(t, a.RunID)
}

// skipInsideContainer skips a test that needs the task containers to reach
// a server the test itself listens on: when the test runs in a container
// (the runner-in-a-container check in docs/zh-CN/target-env-runbook.md), the
// task containers only reach the Docker host.
func skipInsideContainer(t *testing.T, why string) {
	t.Helper()
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			t.Skip("running inside a container: " + why)
		}
	}
}
