package agentevals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Instance is a running `lerryn all-in-one` on a private data directory,
// with one registered user and an access token for the agent.
type Instance struct {
	// Base is the server URL, for example http://127.0.0.1:41234.
	Base string
	// Token is the agent's access token (scopes read, write, execute).
	Token string
	// ProjectID is the user's personal project.
	ProjectID string
	// Session is a cookie client logged in as the same user, for the
	// steps a person does in a browser (the credential fill page).
	Session *http.Client

	cmd  *exec.Cmd
	done chan error
	log  *os.File
}

var (
	buildOnce sync.Once
	binary    string
	buildErr  error
)

// Binary returns the lerryn binary to evaluate: LERRYN_AGENT_EVAL_BINARY
// when set (a release candidate), otherwise one built from this checkout
// once per process.
func Binary(ctx context.Context) (string, error) {
	buildOnce.Do(func() {
		if b := os.Getenv("LERRYN_AGENT_EVAL_BINARY"); b != "" {
			binary = b
			return
		}
		root, err := repoRoot(ctx)
		if err != nil {
			buildErr = err
			return
		}
		dir, err := os.MkdirTemp("", "lerryn-eval-bin-")
		if err != nil {
			buildErr = err
			return
		}
		binary = filepath.Join(dir, "lerryn")
		cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, "./server/cmd/lerryn") // #nosec G204 -- fixed arguments
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("build lerryn: %w\n%s", err, out)
		}
	})
	return binary, buildErr
}

func repoRoot(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("find the module root: %w", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == os.DevNull {
		return "", errors.New("not inside the lerryn module")
	}
	return filepath.Dir(mod), nil
}

// StartInstance starts an all-in-one whose data lives under dir and whose
// log goes to logPath, waits until it is ready, and signs up a user.
func StartInstance(ctx context.Context, dir, logPath string) (*Instance, error) {
	bin, err := Binary(ctx)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	base := "http://" + addr

	logf, err := os.Create(logPath) // #nosec G304 -- a path under the eval output directory
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "all-in-one") // #nosec G204 -- the binary built above
	cmd.Env = append(instanceEnv(os.Environ()),
		"LERRYN_SERVER_LISTEN="+addr,
		"LERRYN_SERVER_PUBLIC_URL="+base,
		"LERRYN_DATABASE_DRIVER=sqlite",
		"LERRYN_DATABASE_DSN="+filepath.Join(dir, "lerryn.db"),
		"LERRYN_STORAGE_DRIVER=local",
		"LERRYN_STORAGE_PATH="+filepath.Join(dir, "storage"),
		"LERRYN_SECRETS_MASTER_KEY_FILE="+filepath.Join(dir, "master.key"),
		"LERRYN_RUNNER_DATA_DIR="+filepath.Join(dir, "runner"),
		"LERRYN_RUNNER_SLOTS=2",
		// Scenarios receive notifications on a loopback server.
		"LERRYN_NOTIFY_ALLOWED_NETWORKS=127.0.0.0/8",
		"LERRYN_LOG_FORMAT=text",
		"LERRYN_LOG_LEVEL=info",
	)
	for _, img := range [][2]string{
		{"LERRYN_TEST_PYTHON_IMAGE", "LERRYN_RUNNER_PYTHON_IMAGE"},
		{"LERRYN_TEST_NODE_IMAGE", "LERRYN_RUNNER_NODE_IMAGE"},
		{"LERRYN_TEST_SHELL_IMAGE", "LERRYN_RUNNER_SHELL_IMAGE"},
	} {
		if v := os.Getenv(img[0]); v != "" {
			cmd.Env = append(cmd.Env, img[1]+"="+v)
		}
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		return nil, fmt.Errorf("start all-in-one: %w", err)
	}
	in := &Instance{Base: base, cmd: cmd, done: make(chan error, 1), log: logf}
	go func() { in.done <- cmd.Wait() }()

	if err := in.waitReady(ctx, time.Minute); err != nil {
		in.Stop()
		return nil, err
	}
	if err := in.signUp(ctx, "eval-user"); err != nil {
		in.Stop()
		return nil, err
	}
	return in, nil
}

// instanceEnv drops the caller's LERRYN_* settings, so the instance only
// sees the configuration set here and never the developer's config file.
func instanceEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "LERRYN_") {
			out = append(out, kv)
		}
	}
	return out
}

func (in *Instance) waitReady(ctx context.Context, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		select {
		case err := <-in.done:
			in.done <- err
			return fmt.Errorf("all-in-one exited during startup (see %s): %w", in.log.Name(), err)
		default:
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, in.Base+"/readyz", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("all-in-one not ready after %s (see %s)", within, in.log.Name())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Stop shuts the instance down like SIGTERM and waits for it.
func (in *Instance) Stop() {
	if in.cmd.Process != nil {
		_ = in.cmd.Process.Signal(os.Interrupt)
		// The exit status after an interrupt is not interesting.
		select {
		case err := <-in.done:
			_ = err
		case <-time.After(time.Minute):
			_ = in.cmd.Process.Kill()
			err := <-in.done
			_ = err
		}
	}
	_ = in.log.Close()
}

const evalPassword = "eval correct horse battery"

func (in *Instance) signUp(ctx context.Context, username string) error {
	jar, _ := cookiejar.New(nil)
	in.Session = &http.Client{Jar: jar, Timeout: time.Minute}
	var me struct {
		PersonalProjectID string `json:"personal_project_id"`
	}
	if err := in.sessionJSON(ctx, http.MethodPost, "/api/v1/auth/register",
		map[string]string{"username": username, "password": evalPassword}, http.StatusCreated, &me); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	var tok struct {
		Token string `json:"token"`
	}
	if err := in.sessionJSON(ctx, http.MethodPost, "/api/v1/me/tokens",
		map[string]any{"name": "agent-eval", "scopes": []string{"read", "write", "execute"}}, http.StatusCreated, &tok); err != nil {
		return fmt.Errorf("create token: %w", err)
	}
	in.ProjectID, in.Token = me.PersonalProjectID, tok.Token
	return nil
}

func (in *Instance) sessionJSON(ctx context.Context, method, path string, body any, want int, v any) error {
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, method, in.Base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", in.Base)
	resp, err := in.Session.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		return fmt.Errorf("%s %s = %d: %s", method, path, resp.StatusCode, out)
	}
	if v != nil {
		return json.Unmarshal(out, v)
	}
	return nil
}

// Response is an API answer.
type Response struct {
	Status int
	Body   []byte
}

// Code returns the problem code of an error answer, or "".
func (r Response) Code() string {
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.Body, &p)
	return p.Code
}

// Decode unmarshals the body into v.
func (r Response) Decode(v any) error {
	if err := json.Unmarshal(r.Body, v); err != nil {
		return fmt.Errorf("decode %d answer: %w: %.300s", r.Status, err, r.Body)
	}
	return nil
}

// Pending reports whether the answer is the stub's: operations whose
// handlers are not written yet answer 500 internal_error. Real handlers
// never fail that way for the well-formed probes the scenarios send.
func (r Response) Pending() bool {
	return r.Status == http.StatusInternalServerError && r.Code() == "internal_error"
}

// API sends a request with the agent's token. headers are name, value
// pairs.
func (in *Instance) API(ctx context.Context, method, path string, body any, headers ...string) (Response, error) {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return Response{}, err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, in.Base+path, rd)
	if err != nil {
		return Response{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+in.Token)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	return Response{Status: resp.StatusCode, Body: data}, err
}

// MustJSON sends a request, requires status want and decodes into v.
func (in *Instance) MustJSON(ctx context.Context, method, path string, body any, want int, v any, headers ...string) error {
	r, err := in.API(ctx, method, path, body, headers...)
	if err != nil {
		return err
	}
	if r.Status != want {
		return fmt.Errorf("%s %s = %d, want %d: %.500s", method, path, r.Status, want, r.Body)
	}
	if v == nil {
		return nil
	}
	return r.Decode(v)
}

// Task and Run are the parts of the API objects the scenarios read.
type Task struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	State         string `json:"state"`
	ActiveVersion *int   `json:"active_version"`
	LatestVersion struct {
		Seq      int             `json:"seq"`
		TestedAt *string         `json:"tested_at"`
		Manifest json.RawMessage `json:"manifest"`
		Files    []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	} `json:"latest_version"`
}

type Run struct {
	ID      string  `json:"id"`
	TaskID  string  `json:"task_id"`
	Version int     `json:"version"`
	Status  string  `json:"status"`
	Test    bool    `json:"test"`
	RerunOf *string `json:"rerun_of"`
	Error   *struct {
		Code    string  `json:"code"`
		Stage   *string `json:"stage"`
		Message string  `json:"message"`
	} `json:"error"`
	Result *struct {
		Status  string          `json:"status"`
		Summary string          `json:"summary"`
		Data    json.RawMessage `json:"data"`
	} `json:"result"`
	QueuedAt   string  `json:"queued_at"`
	FinishedAt *string `json:"finished_at"`
}

// Terminal reports whether the run has ended.
func (r Run) Terminal() bool {
	switch r.Status {
	case "queued", "preparing", "running", "cancelling", "":
		return false
	}
	return true
}

// ApplyTask submits a task version; ifMatch 0 sends no If-Match.
func (in *Instance) ApplyTask(ctx context.Context, name string, req map[string]any, ifMatch int) (Task, error) {
	var res struct {
		Task Task `json:"task"`
	}
	var hdr []string
	if ifMatch > 0 {
		hdr = []string{"If-Match", fmt.Sprint(ifMatch)}
	}
	err := in.MustJSON(ctx, http.MethodPut, "/api/v1/projects/"+in.ProjectID+"/tasks/"+name, req, http.StatusOK, &res, hdr...)
	return res.Task, err
}

// GetTask reads a task.
func (in *Instance) GetTask(ctx context.Context, id string) (Task, error) {
	var t Task
	err := in.MustJSON(ctx, http.MethodGet, "/api/v1/tasks/"+id, nil, http.StatusOK, &t)
	return t, err
}

// StartRun starts a run and returns it.
func (in *Instance) StartRun(ctx context.Context, taskID string, body map[string]any) (Run, error) {
	if body == nil {
		body = map[string]any{} // a nil map would encode as null
	}
	var r Run
	err := in.MustJSON(ctx, http.MethodPost, "/api/v1/tasks/"+taskID+"/runs", body, http.StatusAccepted, &r)
	return r, err
}

// GetRun reads a run.
func (in *Instance) GetRun(ctx context.Context, id string) (Run, error) {
	var r Run
	err := in.MustJSON(ctx, http.MethodGet, "/api/v1/runs/"+url.PathEscape(id), nil, http.StatusOK, &r)
	return r, err
}

// WaitRun waits until the run ends.
func (in *Instance) WaitRun(ctx context.Context, id string, within time.Duration) (Run, error) {
	deadline := time.Now().Add(within)
	for {
		var r Run
		if err := in.MustJSON(ctx, http.MethodGet, "/api/v1/runs/"+url.PathEscape(id)+"?wait=30", nil, http.StatusOK, &r); err != nil {
			return r, err
		}
		if r.Terminal() {
			return r, nil
		}
		if time.Now().After(deadline) {
			return r, fmt.Errorf("run %s still %s after %s", id, r.Status, within)
		}
	}
}

// TaskRuns returns the task's runs: from listRuns when it is implemented,
// otherwise from the given run IDs (for example those the agent started,
// as the MCP proxy recorded them). ok is false in the second case.
func (in *Instance) TaskRuns(ctx context.Context, taskID string, known []string) (runs []Run, viaList bool, err error) {
	r, err := in.API(ctx, http.MethodGet, "/api/v1/runs?limit=100&task_id="+url.QueryEscape(taskID), nil)
	if err != nil {
		return nil, false, err
	}
	if r.Status == http.StatusOK {
		var page struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		if err := r.Decode(&page); err != nil {
			return nil, false, err
		}
		for _, it := range page.Items {
			known = append(known, it.ID)
		}
		viaList = true
	}
	seen := map[string]bool{}
	for _, id := range known {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		run, err := in.GetRun(ctx, id)
		if err != nil {
			continue // an ID the agent made up, or another task's run
		}
		if run.TaskID == taskID {
			runs = append(runs, run)
		}
	}
	return runs, viaList, nil
}
