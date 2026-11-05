//go:build integration

package acceptance

// A multi-node deployment for the scenarios that need one (A14 on remote
// runners, B01, B03): several `lerryn server` processes on one
// PostgreSQL, remote `lerryn runner` processes on this host's Docker
// daemon, and a small load balancer in front of the servers. The servers
// share a local object store directory, which on one host is the same as
// a shared file system; the S3 path is covered by the M5 test in
// server/cmd/lerryn.

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	// The scenarios read lease and run rows from PostgreSQL.
	_ "github.com/jackc/pgx/v5/stdlib"
)

const clusterRegToken = "acceptance-registration-token-shared-by-runners" // #nosec G101 -- a throwaway test deployment

// startPostgres starts a postgres:17 container and returns its DSN. The
// container is removed when the test ends.
func startPostgres(t *testing.T) string {
	t.Helper()
	image := os.Getenv("LERRYN_TEST_POSTGRES_IMAGE")
	if image == "" {
		image = "postgres:17"
	}
	name := fmt.Sprintf("lerryn-acceptance-pg-%d", time.Now().UnixNano())
	run := exec.Command("docker", "run", "-d", "--rm", "--name", name, // #nosec G204 G702 -- test-controlled arguments
		"-e", "POSTGRES_USER=lerryn", "-e", "POSTGRES_PASSWORD=lerryn", "-e", "POSTGRES_DB=lerryn",
		"-p", "127.0.0.1::5432", image, "-c", "max_connections=300")
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("start %s: %v\n%s", image, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() }) // #nosec G204 -- the container this test started
	out, err := exec.Command("docker", "port", name, "5432/tcp").Output()    // #nosec G204 -- the container this test started
	if err != nil {
		t.Fatalf("read published port: %v", err)
	}
	addr := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	dsn := fmt.Sprintf("postgres://lerryn:lerryn@%s/lerryn?sslmode=disable", addr)
	deadline := time.Now().Add(60 * time.Second)
	for {
		// pg_isready answers before the init scripts finish and the
		// server restarts; a real query does not.
		db, err := sql.Open("pgx", dsn)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err = db.PingContext(ctx)
			cancel()
			_ = db.Close()
		}
		if err == nil {
			return dsn
		}
		if time.Now().After(deadline) {
			t.Fatalf("postgres did not become ready: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// node is a lerryn process of a cluster.
type node struct {
	name string
	cmd  *exec.Cmd
	log  string
	done chan struct{}
	url  string
}

type cluster struct {
	t       *testing.T
	dir     string
	dsn     string
	env     []string
	lb      *httptest.Server
	bal     *balancer
	mu      sync.Mutex
	nodes   []*node
	runners map[string]string // name to data directory
}

// newCluster starts PostgreSQL and prepares the shared configuration;
// servers and runners are started with startServer and startRunner.
func newCluster(t *testing.T, extraEnv ...string) *cluster {
	t.Helper()
	dir, err := os.MkdirTemp(os.Getenv("LERRYN_TEST_MOUNT_ROOT"), "lerryn-acceptance-cluster-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) }) // #nosec G703 -- a temp dir this test created
	dsn := startPostgres(t)
	token := filepath.Join(dir, "registration-token")
	if err := os.WriteFile(token, []byte(clusterRegToken+"\n"), 0o600); err != nil { // #nosec G703 -- a temp dir this test created
		t.Fatal(err)
	}
	c := &cluster{t: t, dir: dir, dsn: dsn, runners: map[string]string{}, bal: &balancer{rt: http.DefaultTransport}}
	c.lb = httptest.NewServer(c.bal)
	t.Cleanup(c.lb.Close)
	c.env = append([]string{
		"LERRYN_CONFIG=",
		"LERRYN_LOG_LEVEL=info",
		"LERRYN_LOG_FORMAT=text",
		"LERRYN_DATABASE_DRIVER=postgres",
		"LERRYN_DATABASE_DSN=" + dsn,
		"LERRYN_STORAGE_DRIVER=local",
		"LERRYN_STORAGE_PATH=" + filepath.Join(dir, "storage"),
		"LERRYN_SERVER_PUBLIC_URL=" + c.lb.URL,
		"LERRYN_SERVER_RUNNER_REGISTRATION_TOKEN_FILE=" + token,
		"LERRYN_SERVER_REQUIRE_TEST_BEFORE_ACTIVATE=false",
		"LERRYN_RUNNER_REGISTRATION_TOKEN_FILE=" + token,
		// Every server of a deployment shares the master key; the first
		// to start generates it.
		"LERRYN_SECRETS_MASTER_KEY_FILE=" + filepath.Join(dir, "keys", "master.key"),
	}, extraEnv...)
	if img := os.Getenv("LERRYN_TEST_PYTHON_IMAGE"); img != "" {
		c.env = append(c.env, "LERRYN_RUNNER_PYTHON_IMAGE="+img)
	}
	t.Cleanup(c.shutdown)
	return c
}

func (c *cluster) start(name, role string, extra ...string) *node {
	c.t.Helper()
	logPath := filepath.Join(c.dir, name+".log")
	f, err := os.Create(logPath) // #nosec G304 -- a file in this test's directory
	if err != nil {
		c.t.Fatal(err)
	}
	cmd := exec.Command(binary, role) // #nosec G204 -- the binary this package built
	cmd.Env = append(append(os.Environ(), c.env...), extra...)
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		c.t.Fatal(err)
	}
	n := &node{name: name, cmd: cmd, log: logPath, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		_ = f.Close()
		close(n.done)
	}()
	c.mu.Lock()
	c.nodes = append(c.nodes, n)
	c.mu.Unlock()
	return n
}

// startServer starts a server replica and adds it to the load balancer.
func (c *cluster) startServer(name string) *node {
	c.t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		c.t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	n := c.start(name, "server", "LERRYN_SERVER_LISTEN="+addr)
	n.url = "http://" + addr
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get(n.url + "/readyz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				c.bal.add(n.url)
				return n
			}
		}
		select {
		case <-n.done:
			c.t.Fatalf("server %s exited during startup:\n%s", name, logTail(n.log, 40))
		default:
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("server %s did not become ready:\n%s", name, logTail(n.log, 40))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// startRunner starts a remote runner that reaches the servers through the
// load balancer.
func (c *cluster) startRunner(name string, slots int, extra ...string) *node {
	c.t.Helper()
	dataDir := filepath.Join(c.dir, "runner-"+name)
	c.runners[name] = dataDir
	return c.start(name, "runner", append([]string{
		"LERRYN_RUNNER_MODE=remote",
		"LERRYN_RUNNER_SERVER_URL=" + c.lb.URL,
		"LERRYN_RUNNER_DATA_DIR=" + dataDir,
		"LERRYN_RUNNER_SLOTS=" + strconv.Itoa(slots),
	}, extra...)...)
}

// kill ends n at once, like a crashed host.
func (n *node) kill() {
	_ = n.cmd.Process.Signal(syscall.SIGKILL)
	<-n.done
}

func (n *node) alive() bool {
	select {
	case <-n.done:
		return false
	default:
		return true
	}
}

func (c *cluster) shutdown() {
	c.mu.Lock()
	nodes := c.nodes
	c.mu.Unlock()
	for _, n := range nodes {
		if n.alive() {
			_ = n.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-n.done:
			case <-time.After(time.Minute):
				n.kill()
			}
		}
		if c.t.Failed() {
			c.t.Logf("--- %s log (tail) ---\n%s", n.name, logTail(n.log, 60))
		}
	}
	// Containers a killed runner left behind.
	for _, dir := range c.runners {
		removeRunnerContainers(dir)
	}
}

// removeRunnerContainers deletes the containers of the runner whose data
// directory is dir; it keeps its ID there.
func removeRunnerContainers(dir string) {
	id, err := os.ReadFile(filepath.Join(dir, "runner-id")) // #nosec G304 G703 -- fixed name in this test's runner data directory
	if err != nil {
		return
	}
	out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=lerryn.runner_id="+strings.TrimSpace(string(id))).Output() // #nosec G204 G702 -- fixed argv; runner label comes from this test's data directory
	if ids := strings.Fields(string(out)); len(ids) > 0 {
		_ = exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).Run() // #nosec G204 G702 -- IDs returned by docker ps for this test's runner label
	}
}

// db opens the cluster's database.
func (c *cluster) db() *sql.DB {
	c.t.Helper()
	db, err := sql.Open("pgx", c.dsn)
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(func() { _ = db.Close() })
	return db
}

// api is a client for the deployment through the load balancer; signUp
// gives it a token.
func (c *cluster) api() *server {
	return &server{t: c.t, base: c.lb.URL}
}

// leader returns the server that holds the leader lease, waiting for one.
func (c *cluster) leader(db *sql.DB) *node {
	c.t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		var holder string
		err := db.QueryRowContext(context.Background(),
			`SELECT holder FROM leases WHERE name = 'leader' AND expires_at > now()`).Scan(&holder)
		if err == nil {
			c.mu.Lock()
			nodes := c.nodes
			c.mu.Unlock()
			for _, n := range nodes {
				data, _ := os.ReadFile(n.log) // #nosec G304 -- a file in this test's directory
				if n.alive() && bytes.Contains(data, []byte("holder="+holder)) && bytes.Contains(data, []byte("msg=leading")) {
					return n
				}
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			c.t.Fatal(err)
		}
		if time.Now().After(deadline) {
			c.t.Fatal("no server holds the leader lease")
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func logTail(path string, n int) string {
	data, _ := os.ReadFile(path) // #nosec G304 -- a file in this test's directory
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// balancer forwards each request to the next live server, like the load
// balancer in front of the server replicas. A request that cannot reach a
// server is sent to the next one. Responses are streamed, so SSE works
// through it.
type balancer struct {
	mu       sync.Mutex
	backends []string
	next     int
	rt       http.RoundTripper
}

func (b *balancer) add(url string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.backends = append(b.backends, url)
}

func (b *balancer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	backends := append([]string(nil), b.backends...)
	start := b.next
	b.next++
	b.mu.Unlock()
	for i := range backends {
		target := backends[(start+i)%len(backends)]
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
		flusher, _ := w.(http.Flusher)
		buf := make([]byte, 32<<10)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
				if flusher != nil {
					flusher.Flush()
				}
			}
			if err != nil {
				break
			}
		}
		_ = resp.Body.Close()
		return
	}
	http.Error(w, "no server reachable", http.StatusBadGateway)
}
