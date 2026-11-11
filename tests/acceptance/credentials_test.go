//go:build integration

package acceptance

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	agentevals "github.com/niltavi/lerryn/tests/agent-evals"
)

// credServer is a running `lerryn all-in-one` for the credential
// scenarios, with its output kept so it can be searched for values.
type credServer struct {
	t    *testing.T
	base string
	dir  string
	cmd  *exec.Cmd
	mu   sync.Mutex
	logs bytes.Buffer
}

func (s *credServer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logs.Write(p)
}

func (s *credServer) output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logs.String()
}

func startServer(t *testing.T, slots int) *credServer {
	t.Helper()
	dir, err := os.MkdirTemp(os.Getenv("LERRYN_TEST_MOUNT_ROOT"), "lerryn-acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) }) // #nosec G703 -- a temp dir this test created
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	s := &credServer{t: t, base: "http://" + addr, dir: dir}
	s.cmd = exec.Command(binary, "all-in-one") // #nosec G204 -- the binary built above
	s.cmd.Env = append(os.Environ(),
		"LERRYN_SERVER_LISTEN="+addr,
		"LERRYN_SERVER_PUBLIC_URL="+s.base,
		"LERRYN_DATABASE_DSN="+filepath.Join(dir, "lerryn.db"),
		"LERRYN_STORAGE_PATH="+filepath.Join(dir, "storage"),
		"LERRYN_SECRETS_MASTER_KEY_FILE="+filepath.Join(dir, "keys", "master.key"),
		"LERRYN_RUNNER_DATA_DIR="+filepath.Join(dir, "runner"),
		fmt.Sprintf("LERRYN_RUNNER_SLOTS=%d", slots),
		"LERRYN_LOG_LEVEL=debug",
	)
	if img := os.Getenv("LERRYN_TEST_PYTHON_IMAGE"); img != "" {
		s.cmd.Env = append(s.cmd.Env, "LERRYN_RUNNER_PYTHON_IMAGE="+img)
	}
	s.cmd.Stdout, s.cmd.Stderr = s, s
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = s.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Minute):
			_ = s.cmd.Process.Kill()
		}
		s.removeContainers()
		if t.Failed() {
			t.Logf("server output:\n%s", s.output())
		}
	})
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get(s.base + "/readyz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("server not ready:\n%s", s.output())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// removeContainers deletes containers the embedded runner left behind when
// the process stopped right after a run ended, before it removed them. They
// carry the runner's ID, which it keeps in its data directory.
func (s *credServer) removeContainers() {
	id, err := os.ReadFile(filepath.Join(s.dir, "runner", "runner-id")) // #nosec G304 -- the test's own file
	if err != nil {
		return
	}
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "label=lerryn.runner_id="+strings.TrimSpace(string(id))).Output() // #nosec G204 -- fixed command
	if err != nil {
		return
	}
	if ids := strings.Fields(string(out)); len(ids) > 0 {
		_ = exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).Run() // #nosec G204 -- container IDs from docker
	}
}

// user is a signed-up account with a browser session and an agent token.
type user struct {
	s       *credServer
	name    string
	project string
	token   string
	browser *http.Client
}

func (s *credServer) signUp(name string) *user {
	s.t.Helper()
	jar, _ := cookiejar.New(nil)
	u := &user{s: s, name: name, browser: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
	var me struct {
		PersonalProjectID string `json:"personal_project_id"`
	}
	u.browserJSON(http.MethodPost, "/api/v1/auth/register", map[string]string{"username": name, "password": "correct horse"}, http.StatusCreated, &me)
	u.project = me.PersonalProjectID
	var tok struct {
		Token string `json:"token"`
	}
	u.browserJSON(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "Claude Code", "scopes": []string{"read", "write", "execute"}}, http.StatusCreated, &tok)
	u.token = tok.Token
	return u
}

func (u *user) browserJSON(method, path string, body any, want int, v any) {
	u.s.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, u.s.base+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", u.s.base)
	resp, err := u.browser.Do(req)
	if err != nil {
		u.s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		u.s.t.Fatalf("%s %s = %d: %s", method, path, resp.StatusCode, data)
	}
	if v != nil {
		_ = json.Unmarshal(data, v)
	}
}

// api calls the API as the user's agent and returns the raw body.
func (u *user) api(method, path string, body any, want int, v any) string {
	u.s.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, u.s.base+path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+u.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		u.s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		u.s.t.Fatalf("%s %s = %d, want %d: %s", method, path, resp.StatusCode, want, data)
	}
	if v != nil {
		if err := json.Unmarshal(data, v); err != nil {
			u.s.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
	return string(data)
}

// page fetches or posts the fill page in the user's browser.
func (u *user) page(method, path string, form url.Values) (int, string) {
	u.s.t.Helper()
	var r io.Reader
	if form != nil {
		r = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, u.s.base+path, r)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", u.s.base)
	}
	resp, err := u.browser.Do(req)
	if err != nil {
		u.s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

var csrfPattern = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// fill opens the link the agent got and submits values on the page.
func (u *user) fill(link string, values map[string]string) {
	u.s.t.Helper()
	path := strings.TrimPrefix(link, u.s.base)
	status, body := u.page(http.MethodGet, path, nil)
	m := csrfPattern.FindStringSubmatch(body)
	if status != http.StatusOK || m == nil || !strings.Contains(body, "需要凭证") {
		u.s.t.Fatalf("fill page = %d: %s", status, body)
	}
	form := url.Values{"csrf": {m[1]}}
	for k, v := range values {
		form.Set(""+k, v)
	}
	status, body = u.page(http.MethodPost, path, form)
	if status != http.StatusSeeOther {
		u.s.t.Fatalf("submit = %d: %s", status, body)
	}
	if status, body = u.page(http.MethodGet, path+"?saved=1", nil); !strings.Contains(body, "已保存") {
		u.s.t.Fatalf("after submit = %d: %s", status, body)
	}
}

type task struct {
	ID string `json:"id"`
}

func (u *user) applyTask(name, main string, manifest map[string]any) task {
	u.s.t.Helper()
	m := map[string]any{
		"runtime":     map[string]any{"language": "python", "version": "3.12"},
		"entrypoint":  []string{"python", "main.py"},
		"workflow":    []map[string]any{{"title": "使用凭证", "description": "从注入的凭证读取授权信息并执行操作。"}},
		"network":     "none",
		"concurrency": "allow",
		"credentials": []any{map[string]any{"name": "API_TOKEN"}},
	}
	for k, v := range manifest {
		m[k] = v
	}
	var res struct {
		Task task `json:"task"`
	}
	u.api(http.MethodPut, "/api/v1/projects/"+u.project+"/tasks/"+name, map[string]any{
		"manifest": m, "files": []any{map[string]any{"path": "main.py", "content": main}},
	}, http.StatusOK, &res)
	return res.Task
}

type credentialRequest struct {
	ID           string  `json:"id"`
	URL          string  `json:"url"`
	Status       string  `json:"status"`
	CredentialID *string `json:"credential_id"`
}

func (u *user) requestCredential(taskID string) credentialRequest {
	u.s.t.Helper()
	var r credentialRequest
	u.api(http.MethodPost, "/api/v1/credential-requests", map[string]any{
		"task_id": taskID, "name": "API_TOKEN", "purpose": "调用价格接口",
		"fields": []any{map[string]any{"name": "API_TOKEN"}},
	}, http.StatusCreated, &r)
	return r
}

type run struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (u *user) startRun(taskID string) run {
	u.s.t.Helper()
	var r run
	u.api(http.MethodPost, "/api/v1/tasks/"+taskID+"/runs", map[string]any{"test": true}, http.StatusAccepted, &r)
	return r
}

func (u *user) waitRun(id string, terminal bool) (run, string) {
	u.s.t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		var r run
		body := u.api(http.MethodGet, "/api/v1/runs/"+id+"?wait=10", nil, http.StatusOK, &r)
		switch r.Status {
		case "completed", "failed", "timed_out", "cancelled", "interrupted":
			return r, body
		case "running":
			if !terminal {
				return r, body
			}
		}
		if time.Now().After(deadline) {
			u.s.t.Fatalf("run %s still %s", id, r.Status)
		}
	}
}

func (u *user) runLogs(id string) (string, []string) {
	u.s.t.Helper()
	var page struct {
		Lines []struct {
			Text string `json:"text"`
		} `json:"lines"`
	}
	body := u.api(http.MethodGet, "/api/v1/runs/"+id+"/logs?limit=1000", nil, http.StatusOK, &page)
	var texts []string
	for _, l := range page.Lines {
		texts = append(texts, l.Text)
	}
	return body, texts
}

// A06: a credential value appears in no log line, run record, event, API
// response, server log or database file, also when the task prints it in
// pieces or encoded.
const leakyTask = `
import base64, json, os, sys, time, urllib.parse
import sbx
key = os.environ["API_TOKEN"]
print("raw", key, flush=True)
sys.stdout.write("split " + key[:7]); sys.stdout.flush()
time.sleep(0.3)
sys.stdout.write(key[7:] + " end\n"); sys.stdout.flush()
print("b64", base64.b64encode(key.encode()).decode())
print("basic", base64.b64encode(("user:" + key).encode()).decode())
print("url", urllib.parse.quote(key))
print("json", json.dumps({"token": key}))
print("err", key, file=sys.stderr)
sbx.stage_start("call", "calling with " + key)
sbx.stage_done("call")
sbx.result("ok", "token " + key, {"token": key})
print("length", len(key))
`

const a06Secret = "pk_live_A06+/sensitive=value" // #nosec G101 -- synthetic credential for raw, split and encoded redaction checks

func TestA06SensitiveValuesNeverShown(t *testing.T) {
	s := startServer(t, 2)
	alice := s.signUp("alice")
	tk := alice.applyTask("leaky", leakyTask, map[string]any{"stages": []any{map[string]any{"id": "call", "name": "调用"}}})

	// Without the credential the run is refused with the next step.
	body := alice.api(http.MethodPost, "/api/v1/tasks/"+tk.ID+"/runs", map[string]any{"test": true}, http.StatusUnprocessableEntity, nil)
	if !strings.Contains(body, "credential_missing") || !strings.Contains(body, "credential_request") {
		t.Fatalf("start without credential: %s", body)
	}
	req := alice.requestCredential(tk.ID)
	if !strings.HasPrefix(req.URL, s.base+"/c/") {
		t.Fatalf("fill link = %q", req.URL)
	}
	alice.fill(req.URL, map[string]string{"API_TOKEN": a06Secret})

	var got credentialRequest
	reqBody := alice.api(http.MethodGet, "/api/v1/credential-requests/"+req.ID, nil, http.StatusOK, &got)
	if got.Status != "fulfilled" || got.CredentialID == nil {
		t.Fatalf("request after fill: %s", reqBody)
	}

	// A value that reached the agent, say pasted into the conversation,
	// never reaches a parameter snapshot or a task version: run params,
	// manifests and files containing it or its encodings are refused
	// (tech design 11.7), naming the credential and the place only.
	rejectRun := alice.api(http.MethodPost, "/api/v1/tasks/"+tk.ID+"/runs",
		map[string]any{"test": true, "params": map[string]any{"token": a06Secret}}, http.StatusUnprocessableEntity, nil)
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("bot:"+a06Secret))
	rejectApply := alice.api(http.MethodPut, "/api/v1/projects/"+alice.project+"/tasks/leaky-copy", map[string]any{
		"manifest": map[string]any{
			"runtime": map[string]any{"language": "python", "version": "3.12"}, "entrypoint": []string{"python", "main.py"},
			"workflow": []map[string]any{{"title": "输出结果", "description": "运行脚本并记录结果。"}},
			"params":   map[string]any{"auth": basic},
		},
		"files": []any{map[string]any{"path": "main.py", "content": "TOKEN = '" + url.QueryEscape(a06Secret) + "'\n"}},
	}, http.StatusUnprocessableEntity, nil)
	for name, body := range map[string]string{"run": rejectRun, "apply": rejectApply} {
		for _, want := range []string{`"validation_failed"`, "API_TOKEN", `"credential_request"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s rejection lacks %s: %s", name, want, body)
			}
		}
	}
	if !strings.Contains(rejectRun, `"#/params/token"`) ||
		!strings.Contains(rejectApply, `"#/manifest/params/auth"`) || !strings.Contains(rejectApply, `"#/files/0/content"`) {
		t.Errorf("rejections do not point at the values:\n%s\n%s", rejectRun, rejectApply)
	}

	r := alice.startRun(tk.ID)
	final, runBody := alice.waitRun(r.ID, true)
	logsBody, lines := alice.runLogs(r.ID)
	if final.Status != "completed" {
		t.Fatalf("run = %s\nlogs:\n%s", runBody, strings.Join(lines, "\n"))
	}
	if !contains(lines, "length 28") || !contains(lines, "raw [REDACTED]") || !contains(lines, "split [REDACTED] end") {
		t.Fatalf("logs:\n%s", strings.Join(lines, "\n"))
	}
	// Stage messages and the result are part of the run record.
	credsBody := alice.api(http.MethodGet, "/api/v1/credentials", nil, http.StatusOK, nil)
	taskCredsBody := alice.api(http.MethodGet, "/api/v1/tasks/"+tk.ID+"/credentials", nil, http.StatusOK, nil)
	eventsBody := alice.api(http.MethodGet, "/api/v1/runs/"+r.ID+"/events", nil, http.StatusOK, nil)
	downloadBody := alice.api(http.MethodGet, "/api/v1/runs/"+r.ID+"/logs/download", nil, http.StatusOK, nil)
	// The same records as an agent reads them over MCP.
	m := (&client{s: &instance{t: t, base: s.base}, token: alice.token}).mcp(t)
	var mcpOut strings.Builder
	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"run_get", map[string]any{"run_id": r.ID}},
		{"run_logs", map[string]any{"run_id": r.ID}},
		{"task_get", map[string]any{"task_id": tk.ID}},
		{"api_call", map[string]any{"operation_id": "listRunEvents", "arguments": map[string]any{"run_id": r.ID}}},
	} {
		out, _ := m.call(t, call.tool, call.args)
		b, _ := json.Marshal(out)
		mcpOut.Write(b)
	}
	if !strings.Contains(mcpOut.String(), "[REDACTED]") {
		t.Fatalf("MCP output lacks the redacted lines: %s", mcpOut.String())
	}
	out, isError := m.call(t, "run_start", map[string]any{"task_id": tk.ID, "params": map[string]any{"q": url.QueryEscape(a06Secret)}})
	rejectMCP, _ := json.Marshal(out)
	if !isError || !strings.Contains(string(rejectMCP), "validation_failed") || !strings.Contains(string(rejectMCP), "API_TOKEN") {
		t.Errorf("run_start with the value: error %v, %s", isError, rejectMCP)
	}
	var runs struct {
		Items []json.RawMessage `json:"items"`
	}
	alice.api(http.MethodGet, "/api/v1/runs?task_id="+tk.ID, nil, http.StatusOK, &runs)
	if len(runs.Items) != 1 {
		t.Errorf("rejected starts queued runs: %d runs", len(runs.Items))
	}

	forms := []string{a06Secret, "pk_live_A06", url.QueryEscape(a06Secret), base64.StdEncoding.EncodeToString([]byte("bot:" + a06Secret))}
	for name, text := range map[string]string{
		"run": runBody, "logs": logsBody, "credentials": credsBody,
		"task credentials": taskCredsBody, "request": reqBody, "server log": s.output(),
		"events": eventsBody, "log download": downloadBody, "MCP": mcpOut.String(),
		"run rejection": rejectRun, "apply rejection": rejectApply, "MCP rejection": string(rejectMCP),
	} {
		for _, f := range forms {
			if i := strings.Index(text, f); i >= 0 {
				t.Errorf("%s contains %q: ...%s...", name, f, text[max(0, i-80):min(len(text), i+80)])
			}
		}
	}
	// The database file holds only ciphertext.
	db, err := os.ReadFile(filepath.Join(s.dir, "lerryn.db")) // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatal(err)
	}
	wal, _ := os.ReadFile(filepath.Join(s.dir, "lerryn.db-wal")) // #nosec G304 -- the test's own file
	if bytes.Contains(db, []byte("pk_live_A06")) || bytes.Contains(wal, []byte("pk_live_A06")) {
		t.Error("the database file contains the plaintext")
	}
}

// A05: two users fill in their own credentials; a task uses only what was
// granted to it, and one user cannot reach the other's credential through
// the link, the ID or the API.
func TestA05PersonalCredentials(t *testing.T) {
	s := startServer(t, 2)
	alice := s.signUp("alice")
	bob := s.signUp("bob")
	printTask := "import os\nprint('token', len(os.environ['API_TOKEN']), os.environ['API_TOKEN'][-3:] == 'ice')\n"
	aliceTask := alice.applyTask("alice-task", printTask, nil)
	bobTask := bob.applyTask("bob-task", printTask, nil)

	aliceReq := alice.requestCredential(aliceTask.ID)
	bobReq := bob.requestCredential(bobTask.ID)

	// Bob opens alice's link: the page belongs to another account.
	status, page := bob.page(http.MethodGet, strings.TrimPrefix(aliceReq.URL, s.base), nil)
	if status != http.StatusNotFound || !strings.Contains(page, "不属于当前账号") || strings.Contains(page, "API_TOKEN") {
		t.Fatalf("bob opens alice's link = %d: %s", status, page)
	}
	alice.fill(aliceReq.URL, map[string]string{"API_TOKEN": "secret-of-alice"})
	// Bob's browser is the generic form driver of the agent evaluations,
	// which only reads standard form markup.
	if err := agentevals.FillCredential(t.Context(), &agentevals.Instance{Base: s.base, Session: bob.browser}, bobReq.URL,
		map[string]string{"API_TOKEN": "secret-of-bob"}); err != nil {
		t.Fatal(err)
	}

	var aliceView credentialRequest
	alice.api(http.MethodGet, "/api/v1/credential-requests/"+aliceReq.ID, nil, http.StatusOK, &aliceView)
	credID := *aliceView.CredentialID
	bob.api(http.MethodGet, "/api/v1/credential-requests/"+aliceReq.ID, nil, http.StatusNotFound, nil)
	bob.api(http.MethodGet, "/api/v1/credentials/"+credID, nil, http.StatusNotFound, nil)
	bob.api(http.MethodDelete, "/api/v1/credentials/"+credID, nil, http.StatusNotFound, nil)
	bob.api(http.MethodPut, "/api/v1/credentials/"+credID+"/grants/"+bobTask.ID, map[string]any{"name": "API_TOKEN"}, http.StatusNotFound, nil)
	// Alice cannot grant hers to a task she cannot see.
	alice.api(http.MethodPut, "/api/v1/credentials/"+credID+"/grants/"+bobTask.ID, map[string]any{"name": "API_TOKEN"}, http.StatusNotFound, nil)
	if body := bob.api(http.MethodGet, "/api/v1/credentials", nil, http.StatusOK, nil); strings.Contains(body, credID) {
		t.Fatal("bob lists alice's credential")
	}

	// Each task runs with its owner's value only.
	ar := alice.startRun(aliceTask.ID)
	br := bob.startRun(bobTask.ID)
	for _, c := range []struct {
		u    *user
		id   string
		want string
	}{{alice, ar.ID, "token 15 True"}, {bob, br.ID, "token 13 False"}} {
		final, body := c.u.waitRun(c.id, true)
		_, lines := c.u.runLogs(c.id)
		if final.Status != "completed" || !contains(lines, c.want) {
			t.Fatalf("%s's run = %s\nlogs: %q", c.u.name, body, lines)
		}
	}
}

// A07: after revocation a run fails before any task code runs and says
// why; runs already holding the value are not affected, which the docs
// state.
func TestA07RevokedCredential(t *testing.T) {
	s := startServer(t, 1)
	alice := s.signUp("alice")
	tk := alice.applyTask("revoke-me",
		"import os, sys, time\nprint('started', flush=True)\nif len(sys.argv) > 0 and os.environ.get('LERRYN_PARAMS', '').find('sleep') >= 0:\n    time.sleep(600)\n",
		nil)
	req := alice.requestCredential(tk.ID)
	alice.fill(req.URL, map[string]string{"API_TOKEN": "revocable-value"}) // #nosec G101 -- synthetic credential for revocation in this test instance
	var got credentialRequest
	alice.api(http.MethodGet, "/api/v1/credential-requests/"+req.ID, nil, http.StatusOK, &got)

	// One slot: a sleeping run holds it, so the second run waits queued
	// while the credential is revoked.
	var first run
	alice.api(http.MethodPost, "/api/v1/tasks/"+tk.ID+"/runs", map[string]any{"test": true, "params": map[string]any{"mode": "sleep"}}, http.StatusAccepted, &first)
	alice.waitRun(first.ID, false)
	second := alice.startRun(tk.ID)

	alice.api(http.MethodDelete, "/api/v1/credentials/"+*got.CredentialID, nil, http.StatusOK, nil)
	// New runs are refused up front.
	body := alice.api(http.MethodPost, "/api/v1/tasks/"+tk.ID+"/runs", map[string]any{"test": true}, http.StatusUnprocessableEntity, nil)
	if !strings.Contains(body, "credential_unavailable") {
		t.Fatalf("start after revoke: %s", body)
	}
	// The queued one fails in preparation once it gets the slot.
	alice.api(http.MethodPost, "/api/v1/runs/"+first.ID+"/cancel", nil, http.StatusAccepted, nil)
	final, runBody := alice.waitRun(second.ID, true)
	_, lines := alice.runLogs(second.ID)
	if final.Status != "failed" || final.Error == nil || final.Error.Code != "credential_unavailable" || contains(lines, "started") {
		t.Fatalf("queued run after revoke = %s\nlogs: %q", runBody, lines)
	}
	if !strings.Contains(final.Error.Message, "撤销") {
		t.Fatalf("error message = %q", final.Error.Message)
	}
}

func contains(lines []string, s string) bool {
	for _, l := range lines {
		if l == s {
			return true
		}
	}
	return false
}
