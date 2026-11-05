//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/executor"
	"github.com/niltavi/lerryn/server/internal/leader"
	"github.com/niltavi/lerryn/server/internal/store"
)

// The M5 multi-node scenarios of the implementation plan, with real
// processes: two `lerryn server` replicas on one PostgreSQL and one
// S3-compatible store, remote `lerryn runner` processes on this host's
// Docker daemon, and a small load balancer in front of the servers.
//
//	go test -tags integration -run TestM5 -v ./server/cmd/lerryn/
//
// The processes are this test binary re-executed with
// LERRYN_TEST_CHILD_ROLE set, which runs the lerryn command instead of the
// tests. It needs Docker and pulls postgres:17 and a MinIO image
// (LERRYN_TEST_S3_IMAGE overrides it); the containers are removed
// afterwards. LERRYN_TEST_PYTHON_IMAGE and LERRYN_TEST_MOUNT_ROOT work as
// for the M1 tests.

const childRoleEnv = "LERRYN_TEST_CHILD_ROLE"

func TestMain(m *testing.M) {
	if role := os.Getenv(childRoleEnv); role != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		cmd := newRootCmd()
		cmd.SetArgs([]string{role})
		err := cmd.ExecuteContext(ctx)
		stop()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// m5Task sleeps briefly, sleeps for good with a child process, or prints
// a line every 200 ms, depending on the run's params.
const m5Task = `import subprocess, sys, time
import sbx

mode = sbx.params().get("mode")
if mode == "quick":
    time.sleep(2)
    print("quick done", flush=True)
    sbx.result("ok", "quick")
elif mode == "sleep":
    child = subprocess.Popen(["sleep", "600"])
    print(f"child started {child.pid}", flush=True)
    time.sleep(600)
elif mode == "ticks":
    for i in range(40):
        print(f"tick {i}", flush=True)
        time.sleep(0.2)
    sbx.result("ok", "ticked")
else:
    sys.exit(3)
`

const (
	m5MinIOUser   = "lerryn-m5"
	m5MinIOSecret = "lerryn-m5-secret" // #nosec G101 -- a throwaway test container
	m5RegToken    = "m5-registration-token-shared-by-the-runners"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// dockerRun starts a detached container and removes it when the test ends.
func dockerRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", append([]string{"run", "-d", "--rm"}, args...)...).CombinedOutput() // #nosec G204 G702 -- test-controlled arguments
	if err != nil {
		t.Fatalf("docker run %v: %v: %s", args, err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() }) // #nosec G204 G702 -- the container this test started
	return id
}

func startS3(t *testing.T) string {
	t.Helper()
	image := os.Getenv("LERRYN_TEST_S3_IMAGE")
	if image == "" {
		image = "pgsty/minio:latest"
	}
	port := freePort(t)
	dockerRun(t, "-e", "MINIO_ROOT_USER="+m5MinIOUser, "-e", "MINIO_ROOT_PASSWORD="+m5MinIOSecret,
		"-p", "127.0.0.1:"+strconv.Itoa(port)+":9000", image, "server", "/data")
	endpoint := "http://127.0.0.1:" + strconv.Itoa(port)
	s3 := m5Bucket(t, endpoint)
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := s3.CreateBucket(context.Background())
		if err == nil {
			return endpoint
		}
		if time.Now().After(deadline) {
			t.Fatalf("MinIO did not start: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func m5Bucket(t *testing.T, endpoint string) *blob.S3 {
	t.Helper()
	s3, err := blob.NewS3(blob.S3Options{Endpoint: endpoint, Bucket: "lerryn", PathStyle: true,
		AccessKeyID: m5MinIOUser, SecretAccessKey: m5MinIOSecret})
	if err != nil {
		t.Fatal(err)
	}
	return s3
}

// proc is a lerryn process started by the test.
type proc struct {
	name string
	cmd  *exec.Cmd
	log  string
	done chan struct{}
	url  string
}

type cluster struct {
	t       *testing.T
	dir     string
	env     []string
	mu      sync.Mutex
	procs   []*proc
	runners map[string]string // name to data directory
}

func newCluster(t *testing.T) *cluster {
	t.Helper()
	dir, err := os.MkdirTemp(os.Getenv("LERRYN_TEST_MOUNT_ROOT"), "lerryn-m5-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) }) // #nosec G703 -- a temp dir this test created
	dsn := startPostgres(t, "postgres:17")
	endpoint := startS3(t)
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content+"\n"), 0o600); err != nil { // #nosec G703 -- a temp dir this test created
			t.Fatal(err)
		}
		return p
	}
	env := []string{
		"LERRYN_CONFIG=",
		"LERRYN_LOG_LEVEL=info",
		"LERRYN_LOG_FORMAT=text",
		"LERRYN_DATABASE_DRIVER=postgres",
		"LERRYN_DATABASE_DSN=" + dsn,
		"LERRYN_STORAGE_DRIVER=s3",
		"LERRYN_STORAGE_S3_ENDPOINT=" + endpoint,
		"LERRYN_STORAGE_S3_BUCKET=lerryn",
		"LERRYN_STORAGE_S3_PATH_STYLE=true",
		"LERRYN_STORAGE_S3_ACCESS_KEY_ID_FILE=" + write("s3-key-id", m5MinIOUser),
		"LERRYN_STORAGE_S3_SECRET_ACCESS_KEY_FILE=" + write("s3-secret", m5MinIOSecret),
		"LERRYN_SERVER_RUNNER_REGISTRATION_TOKEN_FILE=" + write("registration-token", m5RegToken),
		"LERRYN_RUNNER_REGISTRATION_TOKEN_FILE=" + filepath.Join(dir, "registration-token"),
		// Every server of a deployment shares the master key; the first to
		// start generates it.
		"LERRYN_SECRETS_MASTER_KEY_FILE=" + filepath.Join(dir, "keys", "master.key"),
	}
	if img := os.Getenv("LERRYN_TEST_PYTHON_IMAGE"); img != "" {
		env = append(env, "LERRYN_RUNNER_PYTHON_IMAGE="+img)
	}
	c := &cluster{t: t, dir: dir, env: env, runners: map[string]string{}}
	t.Cleanup(c.shutdown)
	return c
}

func (c *cluster) start(name, role string, extra ...string) *proc {
	c.t.Helper()
	logPath := filepath.Join(c.dir, name+".log")
	f, err := os.Create(logPath) // #nosec G304 -- a file in this test's directory
	if err != nil {
		c.t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$") // #nosec G204 G702 -- this test binary
	cmd.Env = append(append(append(os.Environ(), childRoleEnv+"="+role), c.env...), extra...)
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		c.t.Fatal(err)
	}
	p := &proc{name: name, cmd: cmd, log: logPath, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		_ = f.Close()
		close(p.done)
	}()
	c.mu.Lock()
	c.procs = append(c.procs, p)
	c.mu.Unlock()
	return p
}

func (c *cluster) startServer(name string) *proc {
	c.t.Helper()
	addr := "127.0.0.1:" + strconv.Itoa(freePort(c.t))
	p := c.start(name, "server", "LERRYN_SERVER_LISTEN="+addr, "LERRYN_SERVER_PUBLIC_URL=http://"+addr)
	p.url = "http://" + addr
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get(p.url + "/readyz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return p
			}
		}
		select {
		case <-p.done:
			c.t.Fatalf("server %s exited during startup:\n%s", name, tail(p.log, 40))
		default:
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("server %s did not become ready:\n%s", name, tail(p.log, 40))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// startRunner starts a remote runner on serverURL. Starting a runner with
// a name used before reuses its data directory, so it is the same runner
// restarted.
func (c *cluster) startRunner(name, serverURL string) *proc {
	c.t.Helper()
	dataDir, ok := c.runners[name]
	if !ok {
		dataDir = filepath.Join(c.dir, "runner-"+name)
		c.runners[name] = dataDir
	}
	return c.start(name, "runner",
		"LERRYN_RUNNER_MODE=remote",
		"LERRYN_RUNNER_SERVER_URL="+serverURL,
		"LERRYN_RUNNER_DATA_DIR="+dataDir,
		"LERRYN_RUNNER_SLOTS=2",
	)
}

// kill ends p at once, like a crashed host: no cleanup, no lease release.
func (p *proc) kill(t *testing.T) {
	t.Helper()
	_ = p.cmd.Process.Signal(syscall.SIGKILL)
	<-p.done
}

// stop shuts p down cleanly.
func (p *proc) stop(t *testing.T) {
	t.Helper()
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(time.Minute):
		t.Errorf("%s did not stop; killing it", p.name)
		p.kill(t)
	}
}

func (c *cluster) shutdown() {
	c.mu.Lock()
	procs := c.procs
	c.mu.Unlock()
	for _, p := range procs {
		select {
		case <-p.done:
		default:
			p.stop(c.t)
		}
		if c.t.Failed() {
			c.t.Logf("--- %s log (tail) ---\n%s", p.name, tail(p.log, 60))
		}
	}
	// Containers a killed runner left behind, if a restart did not
	// already remove them.
	for _, dir := range c.runners {
		id, err := os.ReadFile(filepath.Join(dir, "runner-id")) // #nosec G304 -- a file in this test's directory
		if err != nil {
			continue
		}
		out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label="+executor.LabelRunnerID+"="+strings.TrimSpace(string(id))).Output() // #nosec G204 -- test-controlled arguments
		for _, cid := range strings.Fields(string(out)) {
			_ = exec.Command("docker", "rm", "-f", cid).Run() // #nosec G204 -- a container of this test
		}
	}
}

func tail(path string, n int) string {
	data, _ := os.ReadFile(path) // #nosec G304 -- a file in this test's directory
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// balancer forwards each request to the next live server, like the load
// balancer in front of the server replicas. A request that cannot reach a
// server is sent to the next one.
type balancer struct {
	mu       sync.Mutex
	backends []string
	next     int
	rt       http.RoundTripper
}

func (b *balancer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	start := b.next
	b.next++
	b.mu.Unlock()
	for i := range b.backends {
		target := b.backends[(start+i)%len(b.backends)]
		req := r.Clone(r.Context())
		req.RequestURI = ""
		req.URL.Scheme, req.URL.Host = "http", strings.TrimPrefix(target, "http://")
		req.Body = io.NopCloser(bytes.NewReader(body))
		resp, err := b.rt.RoundTrip(req)
		if err != nil {
			if opErr, ok := errors.AsType[*net.OpError](err); ok && opErr.Op == "dial" {
				continue
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for k, vs := range resp.Header {
			w.Header()[k] = vs
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		_ = resp.Body.Close()
		return
	}
	http.Error(w, "no server reachable", http.StatusBadGateway)
}

type m5Run struct {
	apiRun
	FinishedAt time.Time `json:"finished_at"`
}

func (a *allInOne) getRun(id, query string) m5Run {
	a.t.Helper()
	var run m5Run
	a.call(http.MethodGet, "/api/v1/runs/"+id+query, nil, http.StatusOK, &run)
	return run
}

// waitStatus polls until the run has the given status.
func (a *allInOne) waitStatus(id, status string, within time.Duration) m5Run {
	a.t.Helper()
	deadline := time.Now().Add(within)
	for {
		run := a.getRun(id, "")
		if run.Status == status {
			return run
		}
		if !store.RunActive(run.Status) || time.Now().After(deadline) {
			a.t.Fatalf("run %s is %s, want %s within %s (error %+v)", id, run.Status, status, within, run.Error)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// allLines reads a run's whole log, page by page.
func (a *allInOne) allLines(id string) []string {
	a.t.Helper()
	var out []string
	cursor := ""
	for range 100 {
		path := "/api/v1/runs/" + id + "/logs?limit=7"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var page logPage
		a.call(http.MethodGet, path, nil, http.StatusOK, &page)
		for _, l := range page.Lines {
			if l.Stream == "stdout" {
				out = append(out, l.Text)
			}
		}
		if page.Complete {
			return out
		}
		cursor = page.NextCursor
	}
	a.t.Fatalf("log of run %s did not complete", id)
	return nil
}

func TestM5MultiNode(t *testing.T) {
	c := newCluster(t)
	srvA := c.startServer("server-a")
	srvB := c.startServer("server-b")
	lb := httptest.NewServer(&balancer{backends: []string{srvA.url, srvB.url}, rt: http.DefaultTransport})
	defer lb.Close()

	viaA := &allInOne{t: t, owner: t, base: srvA.url}
	project := viaA.signUp("alice")
	viaB := &allInOne{t: t, owner: t, base: srvB.url, token: viaA.token}
	viaLB := &allInOne{t: t, owner: t, base: lb.URL, token: viaA.token}
	var applied struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	viaA.call(http.MethodPut, "/api/v1/projects/"+project+"/tasks/m5", map[string]any{
		"manifest": map[string]any{
			"runtime":     map[string]any{"language": "python", "version": "3.12"},
			"entrypoint":  []string{"python", "main.py"},
			"limits":      map[string]any{"timeout": "15m", "memory": "256Mi"},
			"concurrency": "allow",
		},
		"files": []map[string]any{{"path": "main.py", "content": m5Task}},
	}, http.StatusOK, &applied)
	task := applied.Task

	db, err := store.Open(context.Background(), config.Database{Driver: config.DatabasePostgres, DSN: envValue(c.env, "LERRYN_DATABASE_DSN=")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	// The first runner talks to server A directly.
	runner1 := c.startRunner("runner-1", srvA.url)

	t.Run("getRun wait across replicas", func(t *testing.T) {
		viaB.t = t
		run := viaB.startRun(task.ID, "quick")
		// Server B waits; runner 1 finishes the run through server A, so
		// only the event bus can wake B before its poll.
		var got m5Run
		for store.RunActive(got.Status) || got.Status == "" {
			got = viaB.getRun(run.ID, "?wait=60")
		}
		returned := time.Now()
		if got.Status != "completed" {
			t.Fatalf("run = %+v", got)
		}
		if lag := returned.Sub(got.FinishedAt); lag > 2*time.Second {
			t.Fatalf("server B answered %s after the run finished; the event from server A did not wake it", lag)
		}
	})

	t.Run("B02 runner killed mid-run", func(t *testing.T) {
		viaA.t = t
		long := viaA.startRun(task.ID, "sleep")
		viaA.waitForLog(long.ID, "child started", 2*time.Minute)
		killedAt := time.Now()
		runner1.kill(t)

		// Another runner keeps the queue moving while the lost run waits
		// for its lease to expire.
		c.startRunner("runner-2", lb.URL)
		quick := viaA.startRun(task.ID, "quick")
		if got := viaA.waitRun(quick.ID, 2*time.Minute); got.Status != "completed" {
			t.Fatalf("run on the second runner = %+v", got)
		}
		if got := viaA.getRun(long.ID, ""); got.Status != "running" {
			t.Fatalf("the lost run is %s before its lease expired", got.Status)
		}

		lost := viaA.waitStatus(long.ID, "interrupted", 90*time.Second)
		if lost.Error == nil || lost.Error.Code != "run_interrupted" {
			t.Fatalf("lost run = %+v", lost)
		}
		// Lease 30 s plus at most one reconcile interval, give or take.
		if took := time.Since(killedAt); took < 25*time.Second || took > 75*time.Second {
			t.Fatalf("the run was interrupted %s after its runner died", took)
		}

		// Restarted, runner 1 removes the container it left behind.
		runner1 = c.startRunner("runner-1", srvA.url)
		deadline := time.Now().Add(time.Minute)
		for len(runContainers(t, long.ID)) > 0 {
			if time.Now().After(deadline) {
				t.Fatalf("containers of the lost run were not removed: %v", runContainers(t, long.ID))
			}
			time.Sleep(250 * time.Millisecond)
		}
		// Runner 1 talks to server A only; stop it before A goes away.
		runner1.stop(t)
	})

	t.Run("B03 server replica down", func(t *testing.T) {
		viaLB.t = t
		viaB.t = t
		ticks := viaLB.startRun(task.ID, "ticks")
		viaLB.waitForLog(ticks.ID, "tick 3", 2*time.Minute)
		srvA.kill(t)

		// Agents and the runner go on through server B.
		got := viaLB.waitRun(ticks.ID, 2*time.Minute)
		if got.Status != "completed" {
			t.Fatalf("run across the server failure = %+v", got)
		}
		lines := viaLB.allLines(ticks.ID)
		if len(lines) != 40 {
			t.Fatalf("read %d lines after the failure, want 40", len(lines))
		}
		for i, l := range lines {
			if l != fmt.Sprintf("tick %d", i) {
				t.Fatalf("line %d = %q", i, l)
			}
		}
		next := viaLB.startRun(task.ID, "quick")
		if got := viaLB.waitRun(next.ID, 2*time.Minute); got.Status != "completed" {
			t.Fatalf("run queued after the failure = %+v", got)
		}

		// Server B takes over the leader lease once A's expires, and
		// archives the logs to the object store.
		deadline := time.Now().Add(leader.TTL + 2*leader.RenewInterval + 2*archiveInterval)
		var row store.Run
		for {
			if err := db.NewSelect().Model(&row).Column("log_archive").Where("id = ?", ticks.ID).Scan(context.Background()); err != nil {
				t.Fatal(err)
			}
			if row.LogArchive != "" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("server B did not archive the log after taking over")
			}
			time.Sleep(500 * time.Millisecond)
		}
		chunks, err := db.NewSelect().Model((*store.LogChunk)(nil)).Where("run_id = ?", ticks.ID).Count(context.Background())
		if err != nil || chunks != 0 {
			t.Fatalf("%d chunks left after archiving, %v", chunks, err)
		}
		var archive struct {
			Blob string `json:"blob"`
		}
		if err := json.Unmarshal([]byte(row.LogArchive), &archive); err != nil {
			t.Fatal(err)
		}
		if ok, err := m5Bucket(t, envValue(c.env, "LERRYN_STORAGE_S3_ENDPOINT=")).Has(context.Background(), archive.Blob); err != nil || !ok {
			t.Fatalf("archive object %s in the bucket: %v, %v", archive.Blob, ok, err)
		}
		if again := viaB.allLines(ticks.ID); strings.Join(again, "\n") != strings.Join(lines, "\n") {
			t.Fatalf("the archived log differs from the original: %d lines", len(again))
		}
	})
}

func envValue(env []string, prefix string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, prefix); ok {
			return v
		}
	}
	return ""
}
