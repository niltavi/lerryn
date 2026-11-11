//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	// The scenarios read run records the public API does not list yet.
	_ "modernc.org/sqlite"
)

// binary is the lerryn binary built once for all scenarios.
var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "lerryn-acceptance-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "lerryn")
	build := exec.Command("go", "build", "-o", binary, "./server/cmd/lerryn") // #nosec G204 -- fixed repository build; output is in this test's temporary directory
	build.Dir = filepath.Join("..", "..")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build lerryn:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// server is a `lerryn all-in-one` process on its own directories. Stopping
// and starting it again keeps the database, objects and runner ID.
type server struct {
	t    *testing.T
	dir  string
	base string
	env  []string
	cmd  *exec.Cmd
	done chan error
	// token is the agent's access token.
	token string
}

func newServer(t *testing.T) *server {
	t.Helper()
	s := prepareServer(t)
	s.start()
	return s
}

// prepareServer sets up the directories and environment of a server
// without starting it, so a scenario can add settings or restore data
// first.
func prepareServer(t *testing.T) *server {
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
	s := &server{t: t, dir: dir, base: "http://" + addr}
	s.env = append(os.Environ(),
		"LERRYN_SERVER_LISTEN="+addr,
		"LERRYN_SERVER_PUBLIC_URL="+s.base,
		"LERRYN_DATABASE_DRIVER=sqlite",
		"LERRYN_DATABASE_DSN="+filepath.Join(dir, "lerryn.db"),
		"LERRYN_STORAGE_DRIVER=local",
		"LERRYN_STORAGE_PATH="+filepath.Join(dir, "storage"),
		"LERRYN_SECRETS_MASTER_KEY_FILE="+filepath.Join(dir, "master.key"),
		"LERRYN_RUNNER_DATA_DIR="+filepath.Join(dir, "runner"),
		"LERRYN_RUNNER_SLOTS=2",
		"LERRYN_SERVER_REQUIRE_TEST_BEFORE_ACTIVATE=false",
		"LERRYN_LOG_LEVEL=warn",
		"LERRYN_LOG_FORMAT=text",
	)
	if img := os.Getenv("LERRYN_TEST_PYTHON_IMAGE"); img != "" {
		s.env = append(s.env, "LERRYN_RUNNER_PYTHON_IMAGE="+img)
	}
	// A scheduled run that starts as the server stops can outlive it;
	// remove what the embedded runner left once the server is down.
	t.Cleanup(func() { removeRunnerContainers(filepath.Join(dir, "runner")) })
	t.Cleanup(s.stop)
	return s
}

// start runs the binary and waits until it is ready.
func (s *server) start() {
	s.t.Helper()
	cmd := exec.Command(binary, "all-in-one") // #nosec G204 -- the binary this test built
	cmd.Env = s.env
	cmd.Stdout, cmd.Stderr = s.t.Output(), s.t.Output()
	if err := cmd.Start(); err != nil {
		s.t.Fatal(err)
	}
	s.cmd, s.done = cmd, make(chan error, 1)
	go func() { s.done <- cmd.Wait() }()
	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case err := <-s.done:
			s.done <- err
			s.t.Fatalf("lerryn exited during startup: %v", err)
		default:
		}
		if resp, err := http.Get(s.base + "/readyz"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			s.t.Fatal("lerryn did not become ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// stop sends SIGTERM-like interrupt and waits for a clean exit.
func (s *server) stop() {
	if s.cmd == nil {
		return
	}
	_ = s.cmd.Process.Signal(os.Interrupt)
	select {
	case err := <-s.done:
		if err != nil {
			s.t.Errorf("lerryn exited with %v", err)
		}
	case <-time.After(time.Minute):
		_ = s.cmd.Process.Kill()
		s.t.Error("lerryn did not stop")
	}
	s.cmd = nil
}

// request sends body as JSON (or raw bytes for a []byte body) and returns
// the status and response body.
func (s *server) request(method, path string, body any, header ...string) (int, []byte) {
	s.t.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		r = bytes.NewReader(b)
	default:
		data, err := json.Marshal(b)
		if err != nil {
			s.t.Fatal(err)
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, s.base+path, r)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// call is request that requires status want and decodes into v.
func (s *server) call(method, path string, body any, want int, v any, header ...string) {
	s.t.Helper()
	status, data := s.request(method, path, body, header...)
	if status != want {
		s.t.Fatalf("%s %s = %d, want %d: %s", method, path, status, want, data)
	}
	if v != nil {
		if err := json.Unmarshal(data, v); err != nil {
			s.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
}

// signUp registers a user, gives the agent an access token and returns
// the user's personal project.
func (s *server) signUp(username string) string {
	s.t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": "correct horse"})
	resp, err := http.Post(s.base+"/api/v1/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	var me struct {
		PersonalProjectID string `json:"personal_project_id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&me)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusCreated {
		s.t.Fatalf("register = %d: %v", resp.StatusCode, err)
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "lerryn_session" {
			session = c
		}
	}
	body, _ = json.Marshal(map[string]any{"name": "acceptance-agent", "scopes": []string{"read", "write", "execute"}})
	req, _ := http.NewRequest(http.MethodPost, s.base+"/api/v1/me/tokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", s.base)
	req.AddCookie(session)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	var created struct {
		Token string `json:"token"`
	}
	err = json.NewDecoder(resp.Body).Decode(&created)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusCreated {
		s.t.Fatalf("create token = %d: %v", resp.StatusCode, err)
	}
	s.token = created.Token
	return me.PersonalProjectID
}

// applyTask submits a Python task and returns its ID.
func (s *server) applyTask(project, name, source string, manifest map[string]any) string {
	s.t.Helper()
	m := map[string]any{
		"runtime":    map[string]any{"language": "python", "version": "3.12"},
		"entrypoint": []string{"python", "main.py"},
		"workflow":   []map[string]any{{"title": "执行检查", "description": "运行验收脚本并记录结果。"}},
		"limits":     map[string]any{"timeout": "5m", "memory": "256Mi"},
	}
	for k, v := range manifest {
		m[k] = v
	}
	var applied struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	s.call(http.MethodPut, "/api/v1/projects/"+project+"/tasks/"+name,
		map[string]any{"manifest": m, "files": []map[string]any{{"path": "main.py", "content": source}}}, http.StatusOK, &applied)
	return applied.Task.ID
}

// activate activates the task's latest version through the API. The
// scheduling scenarios run with the test-before-activate rule off
// (newServer), since a test run would overlap the runs they count.
func (s *server) activate(taskID string) {
	s.t.Helper()
	s.call(http.MethodPost, "/api/v1/tasks/"+taskID+"/activation", map[string]any{"active": true}, http.StatusOK, nil)
}

// db opens the server's SQLite database next to the running process.
func (s *server) db() *sql.DB {
	s.t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(s.dir, "lerryn.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		s.t.Fatal(err)
	}
	return db
}

type apiRun struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	SkipReason string `json:"skip_reason"`
	Trigger    struct {
		Kind        string     `json:"kind"`
		TriggerID   string     `json:"trigger_id"`
		ScheduledAt *time.Time `json:"scheduled_at"`
	} `json:"trigger"`
}

// triggerRuns lists the runs of a trigger, oldest scheduled time first,
// through the API. Run listing arrives with M2, so the IDs come from the
// database and each run is read with getRun.
func (s *server) triggerRuns(triggerID string) []apiRun {
	s.t.Helper()
	db := s.db()
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), `SELECT id FROM runs WHERE trigger_id = ? ORDER BY queued_at, id`, triggerID)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			s.t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		s.t.Fatal(err)
	}
	runs := make([]apiRun, len(ids))
	for i, id := range ids {
		s.call(http.MethodGet, "/api/v1/runs/"+id, nil, http.StatusOK, &runs[i])
	}
	return runs
}
