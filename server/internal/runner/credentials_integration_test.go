//go:build integration

package runner

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/client"

	"github.com/niltavi/lerryn/server/internal/executor"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

// itSecret is the credential value of these tests. Every output path is
// checked for it.
const itSecret = "sk-integration-5f4e3d2c1b0a"

func itSecrets() []runproto.Secret {
	return []runproto.Secret{
		{Name: "LLM", As: "env", Fields: []runproto.SecretField{{Name: "LLM_API_KEY", Value: itSecret, Sensitive: true}}},
		{Name: "CRM", As: "file", Fields: []runproto.SecretField{
			{Name: "USER", Value: "bot"},
			{Name: "PASSWORD", Value: itSecret + "-file", Sensitive: true},
		}},
	}
}

func itCredentialRefs() []runproto.CredentialRef {
	return []runproto.CredentialRef{{Name: "LLM", As: "env"}, {Name: "CRM", As: "file"}}
}

// The task reads its credentials from the environment and /run/secrets,
// prints them raw and encoded, reports them in a result, and checks that
// the credential directory is private and that stdin is at its end.
const pythonSecretTask = `
import base64, json, os, stat, sys, urllib.parse
import sbx

key = os.environ["LLM_API_KEY"]
password = open("/run/secrets/CRM/PASSWORD").read()
user = open("/run/secrets/CRM/USER").read()
st = os.stat("/run/secrets")
print("secrets dir", oct(stat.S_IMODE(st.st_mode)), st.st_uid == os.getuid())
print("file mode", oct(stat.S_IMODE(os.stat("/run/secrets/CRM/PASSWORD").st_mode)))
print("user", user)
print("stdin", repr(sys.stdin.read()))
print("raw", key)
print("file", password)
print("b64", base64.b64encode(key.encode()).decode())
print("basic", base64.b64encode(("bot:" + key).encode()).decode())
print("url", urllib.parse.quote(key + " /"))
print("json", json.dumps({"k": key}))
sys.stdout.write("half " + key[:10])
sys.stdout.flush()
sys.stdout.write(key[10:] + "\n")
print("err", key, file=sys.stderr)
sbx.stage_start("use", "using " + key)
sbx.result("ok", "used " + key, {"key": key})
`

func TestIntegrationPythonSecrets(t *testing.T) {
	h := newDockerHarness(t, "", "", nil)
	m := itManifest()
	m.Credentials = itCredentialRefs()
	m.Stages = nil
	a := newAssignment(m)
	h.coord.secrets[a.RunID] = itSecrets()
	h.coord.enqueue(a, pyFiles(pythonSecretTask))
	h.start()
	requireCompleted(t, h, a.RunID, 2*time.Minute)

	logs := h.coord.logTexts(a.RunID)
	for _, want := range []string{"secrets dir 0o700 True", "file mode 0o600", "user bot", "stdin ''"} {
		if !slices.Contains(logs, want) {
			t.Errorf("missing log line %q in\n%s", want, strings.Join(logs, "\n"))
		}
	}
	assertNoSecret(t, h, a.RunID)
	// The values reached the task: every line that printed one shows the
	// mark instead.
	redacted := 0
	for _, l := range logs {
		if strings.Contains(l, redactedMark) {
			redacted++
		}
	}
	if redacted < 8 {
		t.Fatalf("%d redacted lines, want at least 8:\n%s", redacted, strings.Join(logs, "\n"))
	}
}

// assertNoSecret checks logs, events and the finish record.
func assertNoSecret(t *testing.T, h *harness, runID uuid.UUID) {
	t.Helper()
	for _, l := range h.coord.logTexts(runID) {
		if strings.Contains(l, itSecret) || strings.Contains(l, itSecret[:12]) {
			t.Errorf("log line leaks the credential: %q", l)
		}
	}
	ev, _ := json.Marshal(h.coord.runEvents(runID))
	if strings.Contains(string(ev), itSecret) {
		t.Errorf("events leak the credential: %s", ev)
	}
	f, _ := h.coord.finish(runID)
	if strings.Contains(f.ErrorMessage, itSecret) {
		t.Errorf("finish leaks the credential: %+v", f)
	}
}

const shellSecretTask = `
set -eu
echo "user $(cat /run/secrets/CRM/USER)"
echo "raw $LLM_API_KEY"
echo "file $(cat /run/secrets/CRM/PASSWORD)"
printf '%s' "$LLM_API_KEY" | base64
sbx result --status ok --summary "used $LLM_API_KEY"
`

const nodeSecretTask = `
import { readFileSync } from "node:fs";
import * as sbx from "@lerryn/sbx";
const key = process.env.LLM_API_KEY ?? "";
console.log("user", readFileSync("/run/secrets/CRM/USER", "utf8"));
console.log("raw", key);
console.log("file", readFileSync("/run/secrets/CRM/PASSWORD", "utf8"));
console.log("json", JSON.stringify({ k: key }));
console.log("b64", Buffer.from(key).toString("base64"));
sbx.result("ok", "used " + key);
`

// sbx-exec runs under dash (Node's Debian image) and busybox ash (the
// Shell image) as well.
func TestIntegrationSecretsOtherRuntimes(t *testing.T) {
	tests := []struct {
		name     string
		manifest runproto.Manifest
		file     runproto.File
	}{
		{"shell", itRuntimeManifest("shell", "5", "bash", "main.sh"), runproto.File{Path: "main.sh", Content: []byte(shellSecretTask)}},
		{"node", itRuntimeManifest("node", "22", "node", "main.ts"), runproto.File{Path: "main.ts", Content: []byte(nodeSecretTask)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newDockerHarness(t, "", "", nil)
			m := tt.manifest
			m.Stages = nil
			m.Credentials = itCredentialRefs()
			a := newAssignment(m)
			h.coord.secrets[a.RunID] = itSecrets()
			h.coord.enqueue(a, []runproto.File{tt.file})
			h.start()
			requireCompleted(t, h, a.RunID, 3*time.Minute)
			logs := h.coord.logTexts(a.RunID)
			if !slices.Contains(logs, "user bot") || !slices.Contains(logs, "raw "+redactedMark) {
				t.Fatalf("logs:\n%s", strings.Join(logs, "\n"))
			}
			assertNoSecret(t, h, a.RunID)
		})
	}
}

// The values never appear in the container's configuration.
func TestIntegrationSecretsNotInContainerConfig(t *testing.T) {
	h := newDockerHarness(t, "", "", nil)
	m := itManifest()
	m.Stages = nil
	m.Credentials = itCredentialRefs()
	a := newAssignment(m)
	h.coord.secrets[a.RunID] = itSecrets()
	h.coord.enqueue(a, pyFiles("import time\nprint('ready', flush=True)\ntime.sleep(30)\n"))
	h.start()
	waitForLog(t, h.coord, a.RunID, "ready", 2*time.Minute)

	cs, err := itExec.List(t.Context(), map[string]string{executor.LabelRunID: a.RunID.String()})
	if err != nil || len(cs) != 1 {
		t.Fatalf("containers = %v, %v", cs, err)
	}
	res, err := itAPI.ContainerInspect(context.Background(), cs[0].ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.Container)
	if strings.Contains(string(b), itSecret) {
		t.Fatal("docker inspect shows the credential")
	}
	h.coord.requestCancel(a.RunID)
	h.coord.waitFinish(t, a.RunID, time.Minute)
}

// A credential error from the server fails the run before any container
// starts.
func TestIntegrationUnavailableSecrets(t *testing.T) {
	h := newDockerHarness(t, "", "", nil)
	m := itManifest()
	m.Credentials = itCredentialRefs()
	a := newAssignment(m)
	h.coord.secretsErr[a.RunID] = &runproto.CredentialError{Code: runproto.CodeCredentialUnavailable, Message: "凭证 LLM 已被所有者撤销"}
	h.coord.enqueue(a, pyFiles("print('must not run')\n"))
	h.start()
	f := h.coord.waitFinish(t, a.RunID, 2*time.Minute)
	if f.Status != runproto.StatusFailed || f.ErrorCode != runproto.CodeCredentialUnavailable || f.ExitCode != nil {
		t.Fatalf("finish = %+v", f)
	}
	if slices.Contains(h.coord.logTexts(a.RunID), "must not run") {
		t.Fatal("task code ran")
	}
	assertContainersGone(t, a.RunID)
}
