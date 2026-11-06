//go:build capacity

// Package capacity measures the control plane of the two reference
// topologies (implementation plan S5 and M6; targets in
// docs/zh-CN/capacity.md): how fast runs are claimed, how API calls answer
// while logs pour in, how many log lines per second the database takes,
// how soon a new line is visible to a reader and how fast it reaches many
// SSE subscribers.
//
// Runs are executed by synthetic runners that speak the HTTP runner
// protocol and report runs without starting containers, so the numbers
// are those of the servers and the database, not of Docker. The
// container side (start-up, hardening) is covered by the executor and
// acceptance tests.
//
//	go test -tags capacity -run TestCapacity -v -timeout 30m ./tests/capacity/
//
// Settings:
//
//	LERRYN_CAPACITY_PROFILES  comma-separated: single-node-sqlite, multi-node-postgres (default both)
//	LERRYN_CAPACITY_SCALE     multiplies the sizes of the profiles, e.g. 0.2 for a quick check (default 1)
//	LERRYN_CAPACITY_SECONDS   length of the log phase in seconds (default 20)
//	LERRYN_CAPACITY_OUT       directory for results.json and results.md (default a new temp dir)
//	LERRYN_TEST_POSTGRES_IMAGE PostgreSQL image (default postgres:17)
//
// Docker is needed for the PostgreSQL profile only.
package capacity

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "lerryn-capacity-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "lerryn")
	build := exec.Command("go", "build", "-o", binary, "./server/cmd/lerryn") // #nosec G204 -- a path in a temp dir this test created
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

// profile is one topology and its load.
type profile struct {
	Name     string `json:"name"`
	Database string `json:"database"`
	Servers  int    `json:"servers"`
	// Tasks task definitions are created first.
	Tasks int `json:"tasks"`
	// ClaimRuns runs are queued at once and claimed by Runners runners
	// with Slots slots each.
	ClaimRuns int `json:"claim_runs"`
	Runners   int `json:"runners"`
	Slots     int `json:"slots"`
	// LogRuns runs then print LinesPerSecond lines each for the log phase,
	// followed by SSESubscribers streams and APIClients API clients.
	LogRuns        int `json:"log_runs"`
	LinesPerSecond int `json:"lines_per_second"`
	SSESubscribers int `json:"sse_subscribers"`
	APIClients     int `json:"api_clients"`
	// ServerCPUs caps each server at that many CPUs with GOMAXPROCS, to
	// approximate the PRD's trial host; zero leaves it unlimited.
	ServerCPUs int `json:"server_cpus"`
}

// The sizes come from the PRD (容量验证目标) and S5.
var profiles = []profile{
	{Name: "single-node-sqlite", Database: "sqlite", Servers: 1, Tasks: 100, ClaimRuns: 300, Runners: 1, Slots: 5,
		LogRuns: 5, LinesPerSecond: 500, SSESubscribers: 20, APIClients: 4, ServerCPUs: 2},
	{Name: "multi-node-postgres", Database: "postgres", Servers: 2, Tasks: 1000, ClaimRuns: 1000, Runners: 3, Slots: 20,
		LogRuns: 50, LinesPerSecond: 500, SSESubscribers: 100, APIClients: 8},
}

// Targets (docs/zh-CN/capacity.md). Latencies are p95.
type targets struct {
	ClaimRunsPerSecond float64 `json:"claim_runs_per_second"`
	ClaimLatency       float64 `json:"claim_latency_ms"`
	APILatency         float64 `json:"api_latency_ms"`
	LogVisible         float64 `json:"log_visible_ms"`
	SSEDelivery        float64 `json:"sse_delivery_ms"`
}

var defaultTargets = targets{ClaimRunsPerSecond: 20, ClaimLatency: 1000, APILatency: 500, LogVisible: 2000, SSEDelivery: 2000}

func envInt(t *testing.T, name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("%s=%q: %v", name, v, err)
	}
	return n
}

func TestCapacity(t *testing.T) {
	scale := 1.0
	if v := os.Getenv("LERRYN_CAPACITY_SCALE"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 {
			t.Fatalf("LERRYN_CAPACITY_SCALE=%q", v)
		}
		scale = f
	}
	seconds := envInt(t, "LERRYN_CAPACITY_SECONDS", 20)
	out := os.Getenv("LERRYN_CAPACITY_OUT")
	if out == "" {
		var err error
		if out, err = os.MkdirTemp("", "lerryn-capacity-"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(out, 0o750); err != nil { // #nosec G703 -- the developer chose the output directory
		t.Fatal(err)
	}
	var only []string
	if v := os.Getenv("LERRYN_CAPACITY_PROFILES"); v != "" {
		only = strings.Split(v, ",")
	}
	var results []result
	defer func() { writeResults(t, out, results) }()
	for _, p := range profiles {
		if len(only) > 0 && !slices.Contains(only, p.Name) {
			continue
		}
		p = p.scaled(scale)
		t.Run(p.Name, func(t *testing.T) {
			r := measure(t, p, time.Duration(seconds)*time.Second)
			results = append(results, r)
			for _, v := range r.Verdicts {
				if !v.Met {
					t.Errorf("%s: %s, target %s", v.Metric, v.Got, v.Target)
				}
			}
		})
	}
}

func (p profile) scaled(f float64) profile {
	s := func(n int) int { return max(1, int(math.Round(float64(n)*f))) }
	p.Tasks, p.ClaimRuns, p.LogRuns, p.SSESubscribers = s(p.Tasks), s(p.ClaimRuns), s(p.LogRuns), s(p.SSESubscribers)
	return p
}

// result is what one profile measured.
type result struct {
	Profile  profile   `json:"profile"`
	Machine  string    `json:"machine"`
	Version  string    `json:"version"`
	Started  time.Time `json:"started"`
	Duration string    `json:"log_phase"`

	ApplyPerSecond float64 `json:"apply_per_second"`
	ApplyLatency   dist    `json:"apply_latency_ms"`

	ClaimRunsPerSecond float64 `json:"claim_runs_per_second"`
	// ClaimLatency is from startRun to the claim with idle runners;
	// BurstQueueWait the same for the burst, which includes the wait in
	// the queue.
	ClaimLatency   dist `json:"claim_latency_ms"`
	BurstQueueWait dist `json:"burst_queue_wait_ms"`
	// RetriedCalls counts runner calls answered 5xx and retried, as the
	// real runner does; ServerErrors are the first error lines of the
	// server logs.
	RetriedCalls int64    `json:"retried_runner_calls"`
	ServerErrors []string `json:"server_errors,omitempty"`

	LinesSent      int64   `json:"lines_sent"`
	LinesPerSecond float64 `json:"lines_per_second"`
	LogBatch       dist    `json:"log_batch_ms"`
	LogVisible     dist    `json:"log_visible_ms"`

	SSELinesWanted   int64 `json:"sse_lines_wanted"`
	SSELinesReceived int64 `json:"sse_lines_received"`
	SSEDelivery      dist  `json:"sse_delivery_ms"`

	API map[string]dist `json:"api_latency_ms"`

	ServerPeakRSSMiB float64 `json:"server_peak_rss_mib"`
	ServerAvgCPU     float64 `json:"server_avg_cpu_percent"`
	DatabasePeak     string  `json:"database_peak,omitempty"`
	DatabaseBytes    int64   `json:"database_bytes"`

	Verdicts []verdict `json:"verdicts"`
}

type verdict struct {
	Metric string `json:"metric"`
	Got    string `json:"got"`
	Target string `json:"target"`
	Met    bool   `json:"met"`
}

// dist summarises latencies in milliseconds.
type dist struct {
	N   int     `json:"n"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

type sampler struct {
	mu sync.Mutex
	v  []float64
}

func (s *sampler) add(d time.Duration) {
	s.mu.Lock()
	s.v = append(s.v, float64(d.Microseconds())/1000)
	s.mu.Unlock()
}

func (s *sampler) dist() dist {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.v) == 0 {
		return dist{}
	}
	v := append([]float64(nil), s.v...)
	sort.Float64s(v)
	q := func(p float64) float64 { return round(v[min(len(v)-1, int(math.Ceil(p*float64(len(v))))-1)]) }
	return dist{N: len(v), P50: q(0.50), P95: q(0.95), P99: q(0.99), Max: round(v[len(v)-1])}
}

func round(f float64) float64 { return math.Round(f*10) / 10 }

// deployment is the servers of a profile.
type deployment struct {
	t       *testing.T
	dir     string
	servers []*proc
	token   string
	project string
	pgName  string
	dbPath  string
	http    *http.Client
	logs    []string
	retried atomic.Int64
}

// serverErrors returns the first error and warning lines of the server
// logs.
func (d *deployment) serverErrors() []string {
	var out []string
	for _, path := range d.logs {
		data, _ := os.ReadFile(path) // #nosec G304 -- a log of this test
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "level=ERROR") || strings.Contains(line, "level=WARN") {
				out = append(out, line)
				if len(out) == 20 {
					return out
				}
			}
		}
	}
	return out
}

type proc struct {
	cmd  *exec.Cmd
	url  string
	done chan struct{}
}

const regToken = "capacity-registration-token-for-synthetic-runners" // #nosec G101 -- a throwaway test deployment

func deploy(t *testing.T, p profile) *deployment {
	t.Helper()
	dir := t.TempDir()
	d := &deployment{t: t, dir: dir, http: &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 512}}}
	tokenFile := filepath.Join(dir, "registration-token")
	if err := os.WriteFile(tokenFile, []byte(regToken+"\n"), 0o600); err != nil { // #nosec G703 -- a temp dir of this test
		t.Fatal(err)
	}
	env := []string{
		"LERRYN_CONFIG=",
		"LERRYN_LOG_LEVEL=warn",
		"LERRYN_LOG_FORMAT=text",
		"LERRYN_STORAGE_DRIVER=local",
		"LERRYN_STORAGE_PATH=" + filepath.Join(dir, "storage"),
		"LERRYN_SECRETS_MASTER_KEY_FILE=" + filepath.Join(dir, "master.key"),
		"LERRYN_SERVER_RUNNER_REGISTRATION_TOKEN_FILE=" + tokenFile,
		"LERRYN_SERVER_REQUIRE_TEST_BEFORE_ACTIVATE=false",
		"LERRYN_SCHEDULER_MAX_QUEUED_RUNS=0",
		"LERRYN_SCHEDULER_MAX_QUEUED_RUNS_PER_PROJECT=0",
		"LERRYN_RUNNER_MODE=remote",
	}
	switch p.Database {
	case "sqlite":
		d.dbPath = filepath.Join(dir, "lerryn.db")
		env = append(env, "LERRYN_DATABASE_DRIVER=sqlite", "LERRYN_DATABASE_DSN="+d.dbPath)
	case "postgres":
		dsn, name := startPostgres(t)
		d.pgName = name
		env = append(env, "LERRYN_DATABASE_DRIVER=postgres", "LERRYN_DATABASE_DSN="+dsn)
	}
	for i := range p.Servers {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		cmd := exec.Command(binary, "server") // #nosec G204 -- the binary this package built
		cmd.Env = append(append(os.Environ(), env...), "LERRYN_SERVER_LISTEN="+addr, "LERRYN_SERVER_PUBLIC_URL=http://"+addr)
		if p.ServerCPUs > 0 {
			cmd.Env = append(cmd.Env, fmt.Sprintf("GOMAXPROCS=%d", p.ServerCPUs))
		}
		logPath := filepath.Join(dir, fmt.Sprintf("server-%d.log", i))
		logf, err := os.Create(logPath) // #nosec G304 -- a temp dir of this test
		if err != nil {
			t.Fatal(err)
		}
		d.logs = append(d.logs, logPath)
		cmd.Stdout, cmd.Stderr = logf, logf
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		pr := &proc{cmd: cmd, url: "http://" + addr, done: make(chan struct{})}
		go func() { _ = cmd.Wait(); _ = logf.Close(); close(pr.done) }()
		t.Cleanup(func() {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-pr.done:
			case <-time.After(30 * time.Second):
				_ = cmd.Process.Kill()
			}
		})
		d.servers = append(d.servers, pr)
		deadline := time.Now().Add(60 * time.Second)
		for {
			resp, err := http.Get(pr.url + "/readyz")
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					break
				}
			}
			if time.Now().After(deadline) {
				data, _ := os.ReadFile(filepath.Join(dir, fmt.Sprintf("server-%d.log", i))) // #nosec G304 -- a temp dir of this test
				t.Fatalf("server %d not ready:\n%s", i, data)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	d.signUp()
	return d
}

func startPostgres(t *testing.T) (dsn, name string) {
	t.Helper()
	image := os.Getenv("LERRYN_TEST_POSTGRES_IMAGE")
	if image == "" {
		image = "postgres:17"
	}
	name = fmt.Sprintf("lerryn-capacity-pg-%d", time.Now().UnixNano())
	run := exec.Command("docker", "run", "-d", "--rm", "--name", name, // #nosec G204 G702 -- test-controlled arguments
		"-e", "POSTGRES_USER=lerryn", "-e", "POSTGRES_PASSWORD=lerryn", "-e", "POSTGRES_DB=lerryn",
		"-p", "127.0.0.1::5432", image, "-c", "max_connections=400")
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("start %s: %v\n%s", image, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() }) // #nosec G204 -- the container this test started
	out, err := exec.Command("docker", "port", name, "5432/tcp").Output()    // #nosec G204 -- the container this test started
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	deadline := time.Now().Add(60 * time.Second)
	for {
		// The init scripts restart the server once; wait for a real query.
		if out, err := exec.Command("docker", "exec", name, "psql", "-U", "lerryn", "-h", "127.0.0.1", "-tAc", "select 1").Output(); err == nil && strings.TrimSpace(string(out)) == "1" { // #nosec G204 -- the container this test started
			return fmt.Sprintf("postgres://lerryn:lerryn@%s/lerryn?sslmode=disable", addr), name
		}
		if time.Now().After(deadline) {
			t.Fatal("postgres did not become ready")
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (d *deployment) server(i int) string { return d.servers[i%len(d.servers)].url }

// api calls the API on server i and decodes the answer into v.
func (d *deployment) api(ctx context.Context, i int, method, path string, body, v any) (int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.server(i)+path, r)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}
	resp, err := d.http.Do(req) // #nosec G704 -- a server of this test
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s %s = %d: %s", method, path, resp.StatusCode, data)
	}
	if v != nil {
		return resp.StatusCode, json.Unmarshal(data, v)
	}
	return resp.StatusCode, nil
}

func (d *deployment) signUp() {
	t := d.t
	body, _ := json.Marshal(map[string]string{"username": "capacity", "password": "correct horse"})
	resp, err := http.Post(d.server(0)+"/api/v1/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var me struct {
		PersonalProjectID string `json:"personal_project_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&me)
	_ = resp.Body.Close()
	d.project = me.PersonalProjectID
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "lerryn_session" {
			session = c
		}
	}
	body, _ = json.Marshal(map[string]any{"name": "capacity", "scopes": []string{"read", "write", "execute"}})
	req, _ := http.NewRequest(http.MethodPost, d.server(0)+"/api/v1/me/tokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", d.server(0))
	req.AddCookie(session)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	_ = resp.Body.Close()
	if tok.Token == "" {
		t.Fatalf("create token = %d", resp.StatusCode)
	}
	d.token = tok.Token
}

// runner is a synthetic runner: it claims runs over the runner protocol
// and reports them without starting containers.
type runner struct {
	d          *deployment
	base       string
	id         string
	credential string
	slots      int
	mu         sync.Mutex
	active     map[string]bool
}

func (d *deployment) newRunner(i, slots int) *runner {
	r := &runner{d: d, base: d.server(i), id: uuid.NewString(), slots: slots, active: map[string]bool{}}
	var resp struct {
		Credential string `json:"credential"`
	}
	if err := r.call(context.Background(), "register", regToken, map[string]any{
		"id": r.id, "name": fmt.Sprintf("synthetic-%d", i), "slots": slots, "labels": []string{}, "version": "capacity",
	}, &resp); err != nil {
		d.t.Fatal(err)
	}
	r.credential = resp.Credential
	return r
}

// call sends a runner protocol request. Like the real runner it retries
// answers the server marks as retryable (5xx), with a short pause.
func (r *runner) call(ctx context.Context, path, token string, body, v any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		err := r.send(ctx, path, token, b, v)
		var se *statusError
		if err == nil || !errors.As(err, &se) || se.status < 500 || attempt == 10 || ctx.Err() != nil {
			return err
		}
		r.d.retried.Add(1)
		time.Sleep(time.Duration(100*(attempt+1)) * time.Millisecond)
	}
}

type statusError struct {
	path   string
	status int
	body   string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("runner %s = %d: %s", e.path, e.status, e.body)
}

func (r *runner) send(ctx context.Context, path, token string, b []byte, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.base+"/runner/v1/"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Lerryn-Runner-Protocol", "1")
	resp, err := r.d.http.Do(req) // #nosec G704 -- a server of this test
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return &statusError{path: path, status: resp.StatusCode, body: string(data)}
	}
	if v != nil && len(data) > 0 {
		return json.Unmarshal(data, v)
	}
	return nil
}

type assignment struct {
	RunID string `json:"run_id"`
}

func (r *runner) claim(ctx context.Context, n int) ([]assignment, error) {
	var resp struct {
		Assignments []assignment `json:"assignments"`
	}
	err := r.call(ctx, "claim", r.credential, map[string]any{"max": n, "wait_ms": 2000}, &resp)
	return resp.Assignments, err
}

// heartbeat keeps the leases of the runner's runs until ctx ends.
func (r *runner) heartbeat(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		r.mu.Lock()
		active := make([]string, 0, len(r.active))
		for id := range r.active {
			active = append(active, id)
		}
		r.mu.Unlock()
		_ = r.call(ctx, "heartbeat", r.credential, map[string]any{"active": active}, nil)
	}
}

func (r *runner) track(id string, on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if on {
		r.active[id] = true
	} else {
		delete(r.active, id)
	}
}

func (r *runner) started(ctx context.Context, id string) error {
	return r.call(ctx, "runs/"+id+"/started", r.credential, map[string]any{"at": time.Now().UTC()}, nil)
}

type logLine struct {
	Seq    int64     `json:"seq"`
	TS     time.Time `json:"ts"`
	Stream string    `json:"stream"`
	Text   string    `json:"text"`
}

func (r *runner) logs(ctx context.Context, id string, lines []logLine) error {
	return r.call(ctx, "runs/"+id+"/logs", r.credential, map[string]any{"lines": lines, "dropped_lines": 0, "dropped_bytes": 0}, nil)
}

func (r *runner) finish(ctx context.Context, id string) error {
	return r.call(ctx, "runs/"+id+"/finish", r.credential, map[string]any{
		"status": "completed", "exit_code": 0, "finished_at": time.Now().UTC(),
	}, nil)
}

// lineText carries the send time so readers can measure latency.
func lineText(run int, seq int64, sent time.Time) string {
	return fmt.Sprintf("cap %d %d %d", run, seq, sent.UnixNano())
}

func sentAt(text string) (time.Time, bool) {
	f := strings.Fields(text)
	if len(f) != 4 || f[0] != "cap" {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(f[3], 10, 64)
	return time.Unix(0, n), err == nil
}

func measure(t *testing.T, p profile, logPhase time.Duration) result {
	d := deploy(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := result{Profile: p, Machine: machine(), Started: time.Now().UTC(), Duration: logPhase.String(), API: map[string]dist{}}
	var info struct {
		Server struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
		} `json:"server"`
	}
	_, _ = d.api(ctx, 0, http.MethodGet, "/api/v1/discover", nil, &info)
	res.Version = info.Server.Version + " " + info.Server.Commit

	mon := d.monitor(ctx)

	// Task definitions.
	taskIDs := make([]string, p.Tasks)
	var applyLat sampler
	began := time.Now()
	parallel(16, p.Tasks, func(i int) {
		var applied struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
		}
		start := time.Now()
		if _, err := d.api(ctx, i, http.MethodPut, fmt.Sprintf("/api/v1/projects/%s/tasks/task-%04d", d.project, i), map[string]any{
			"manifest": map[string]any{
				"runtime": map[string]any{"language": "python", "version": "3.12"}, "entrypoint": []string{"python", "main.py"},
				"concurrency": "allow", "network": "none",
			},
			"files": []map[string]any{{"path": "main.py", "content": fmt.Sprintf("print('task %d')\n", i)}},
		}, &applied); err != nil {
			t.Fatal(err)
		}
		applyLat.add(time.Since(start))
		taskIDs[i] = applied.Task.ID
	})
	res.ApplyPerSecond = round(float64(p.Tasks) / time.Since(began).Seconds())
	res.ApplyLatency = applyLat.dist()
	t.Logf("%d tasks at %.0f/s", p.Tasks, res.ApplyPerSecond)

	runners := make([]*runner, p.Runners)
	for i := range runners {
		runners[i] = d.newRunner(i, p.Slots)
		go runners[i].heartbeat(ctx)
	}

	// Claims: first single runs queued one at a time onto idle runners
	// (the latency an agent sees from run_start to preparing), then
	// ClaimRuns runs queued at once while the runners drain the queue
	// (throughput). Each run reports three lines.
	var (
		queuedAt, claimedAt sync.Map
		finished            atomic.Int64
		target              atomic.Int64
	)
	allDone := make(chan struct{})
	claimCtx, stopClaims := context.WithCancel(ctx)
	var claimers sync.WaitGroup
	for _, r := range runners {
		for range p.Slots {
			claimers.Add(1)
			go func() {
				defer claimers.Done()
				for claimCtx.Err() == nil {
					as, err := r.claim(claimCtx, 1)
					if err != nil {
						continue
					}
					for _, a := range as {
						claimedAt.Store(a.RunID, time.Now())
						r.track(a.RunID, true)
						now := time.Now().UTC()
						_ = r.started(ctx, a.RunID)
						_ = r.logs(ctx, a.RunID, []logLine{
							{Seq: 1, TS: now, Stream: "stdout", Text: "one"}, {Seq: 2, TS: now, Stream: "stdout", Text: "two"},
							{Seq: 3, TS: now, Stream: "stdout", Text: "three"},
						})
						if err := r.finish(ctx, a.RunID); err != nil {
							t.Errorf("finish %s: %v", a.RunID, err)
						}
						r.track(a.RunID, false)
						if n := finished.Add(1); n == target.Load() {
							close(allDone)
						}
					}
				}
			}()
		}
	}
	queue := func(i int) string {
		var run struct {
			ID string `json:"id"`
		}
		// The time before the request: the run may be claimed before the
		// response arrives.
		start := time.Now()
		if _, err := d.api(ctx, i, http.MethodPost, "/api/v1/tasks/"+taskIDs[i%len(taskIDs)]+"/runs", map[string]any{"test": true}, &run); err != nil {
			t.Fatal(err)
		}
		queuedAt.Store(run.ID, start)
		return run.ID
	}
	latencies := func(ids []string) dist {
		var s sampler
		for _, id := range ids {
			q, ok1 := queuedAt.Load(id)
			c, ok2 := claimedAt.Load(id)
			if ok1 && ok2 {
				s.add(c.(time.Time).Sub(q.(time.Time)))
			}
		}
		return s.dist()
	}
	const idleRuns = 50
	target.Store(-1)
	idle := make([]string, idleRuns)
	for i := range idle {
		idle[i] = queue(i)
		deadline := time.Now().Add(10 * time.Second)
		for finished.Load() < int64(i+1) {
			if time.Now().After(deadline) {
				t.Fatalf("run %d was not claimed and finished within 10s", i)
			}
			time.Sleep(2 * time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
	}
	res.ClaimLatency = latencies(idle)

	target.Store(int64(idleRuns + p.ClaimRuns))
	burst := make([]string, p.ClaimRuns)
	began = time.Now()
	parallel(16, p.ClaimRuns, func(i int) { burst[i] = queue(i) })
	select {
	case <-allDone:
	case <-time.After(5 * time.Minute):
		t.Fatalf("only %d of %d runs finished", finished.Load()-idleRuns, p.ClaimRuns)
	}
	elapsed := time.Since(began)
	stopClaims()
	claimers.Wait()
	res.ClaimRunsPerSecond = round(float64(p.ClaimRuns) / elapsed.Seconds())
	res.BurstQueueWait = latencies(burst)
	t.Logf("%d runs queued, claimed and finished in %s (%.1f runs/s)", p.ClaimRuns, elapsed.Round(time.Millisecond), res.ClaimRunsPerSecond)

	// Log phase: LogRuns runs print LinesPerSecond lines each; readers
	// poll, SSE subscribers follow and API clients browse meanwhile.
	logRunIDs := make([]string, p.LogRuns)
	parallel(16, p.LogRuns, func(i int) {
		var run struct {
			ID string `json:"id"`
		}
		if _, err := d.api(ctx, i, http.MethodPost, "/api/v1/tasks/"+taskIDs[i%len(taskIDs)]+"/runs", map[string]any{"test": true}, &run); err != nil {
			t.Fatal(err)
		}
		logRunIDs[i] = run.ID
	})
	owner := map[string]*runner{}
	for len(owner) < p.LogRuns {
		progressed := false
		for _, r := range runners {
			as, err := r.claim(ctx, p.Slots)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range as {
				owner[a.RunID] = r
				r.track(a.RunID, true)
				if err := r.started(ctx, a.RunID); err != nil {
					t.Fatal(err)
				}
				progressed = true
			}
		}
		if !progressed {
			t.Fatalf("claimed %d of %d log runs", len(owner), p.LogRuns)
		}
	}

	var (
		sseLat      sampler
		sseReceived atomic.Int64
		sseWG       sync.WaitGroup
		connected   sync.WaitGroup
	)
	for i := range p.SSESubscribers {
		sseWG.Add(1)
		connected.Add(1)
		go func() {
			defer sseWG.Done()
			d.follow(ctx, i, logRunIDs[i%len(logRunIDs)], &connected, &sseLat, &sseReceived)
		}()
	}
	connected.Wait()

	var visible sampler
	pollCtx, stopPolls := context.WithCancel(ctx)
	var pollers sync.WaitGroup
	for i := range min(5, p.LogRuns) {
		pollers.Add(1)
		go func() {
			defer pollers.Done()
			d.poll(pollCtx, i+1, logRunIDs[i], &visible)
		}()
	}
	api := map[string]*sampler{}
	for _, op := range []string{"getOverview", "listRuns", "getRun", "readRunLogs", "listTasks"} {
		api[op] = &sampler{}
	}
	var browsers sync.WaitGroup
	for i := range p.APIClients {
		browsers.Add(1)
		go func() {
			defer browsers.Done()
			d.browse(pollCtx, i, logRunIDs, api)
		}()
	}

	var (
		sent     atomic.Int64
		batchLat sampler
		writers  sync.WaitGroup
	)
	logStart := time.Now()
	perBatch := max(1, p.LinesPerSecond/5)
	for i, id := range logRunIDs {
		writers.Add(1)
		go func() {
			defer writers.Done()
			r := owner[id]
			tick := time.NewTicker(200 * time.Millisecond)
			defer tick.Stop()
			var seq int64
			for time.Since(logStart) < logPhase {
				<-tick.C
				now := time.Now()
				lines := make([]logLine, perBatch)
				for j := range lines {
					seq++
					lines[j] = logLine{Seq: seq, TS: now.UTC(), Stream: "stdout", Text: lineText(i, seq, now)}
				}
				if err := r.logs(ctx, id, lines); err != nil {
					t.Errorf("logs of %s: %v", id, err)
					return
				}
				batchLat.add(time.Since(now))
				sent.Add(int64(perBatch))
			}
			if err := r.finish(ctx, id); err != nil {
				t.Errorf("finish %s: %v", id, err)
			}
			r.track(id, false)
		}()
	}
	writers.Wait()
	logElapsed := time.Since(logStart)
	// Let the readers catch up with the last batch, then stop them.
	time.Sleep(3 * time.Second)
	stopPolls()
	pollers.Wait()
	browsers.Wait()
	sseDone := make(chan struct{})
	go func() { sseWG.Wait(); close(sseDone) }()
	select {
	case <-sseDone:
	case <-time.After(30 * time.Second):
		t.Error("SSE streams did not end after their runs finished")
		cancel()
		<-sseDone
	}

	res.LinesSent = sent.Load()
	res.LinesPerSecond = round(float64(res.LinesSent) / logElapsed.Seconds())
	res.LogBatch = batchLat.dist()
	res.LogVisible = visible.dist()
	perRun := res.LinesSent / int64(p.LogRuns)
	res.SSELinesWanted = perRun * int64(p.SSESubscribers)
	res.SSELinesReceived = sseReceived.Load()
	res.SSEDelivery = sseLat.dist()
	for op, s := range api {
		res.API[op] = s.dist()
	}
	mon.stop(&res)
	res.DatabaseBytes = d.databaseBytes()
	res.RetriedCalls = d.retried.Load()
	res.ServerErrors = d.serverErrors()

	worstAPI := 0.0
	for _, s := range res.API {
		worstAPI = max(worstAPI, s.P95)
	}
	tg := defaultTargets
	wantLines := float64(p.LogRuns * p.LinesPerSecond)
	res.Verdicts = []verdict{
		{"claim throughput", fmt.Sprintf("%.1f runs/s", res.ClaimRunsPerSecond), fmt.Sprintf(">= %.0f runs/s", tg.ClaimRunsPerSecond), res.ClaimRunsPerSecond >= tg.ClaimRunsPerSecond},
		{"claim latency p95", fmt.Sprintf("%.0f ms", res.ClaimLatency.P95), fmt.Sprintf("<= %.0f ms", tg.ClaimLatency), res.ClaimLatency.P95 <= tg.ClaimLatency},
		{"log ingestion", fmt.Sprintf("%.0f lines/s", res.LinesPerSecond), fmt.Sprintf(">= %.0f lines/s (95%% of offered)", 0.95*wantLines), res.LinesPerSecond >= 0.95*wantLines},
		{"log visible p95", fmt.Sprintf("%.0f ms", res.LogVisible.P95), fmt.Sprintf("<= %.0f ms", tg.LogVisible), res.LogVisible.N > 0 && res.LogVisible.P95 <= tg.LogVisible},
		{"SSE delivery p95", fmt.Sprintf("%.0f ms", res.SSEDelivery.P95), fmt.Sprintf("<= %.0f ms", tg.SSEDelivery), res.SSEDelivery.N > 0 && res.SSEDelivery.P95 <= tg.SSEDelivery},
		{"SSE completeness", fmt.Sprintf("%d of %d lines", res.SSELinesReceived, res.SSELinesWanted), "all", res.SSELinesReceived == res.SSELinesWanted},
		{"API p95 under load", fmt.Sprintf("%.0f ms (worst operation)", worstAPI), fmt.Sprintf("<= %.0f ms", tg.APILatency), worstAPI <= tg.APILatency},
	}
	return res
}

// follow streams a run's log from server i and records each line's
// delivery time.
func (d *deployment) follow(ctx context.Context, i int, runID string, connected *sync.WaitGroup, lat *sampler, n *atomic.Int64) {
	once := sync.OnceFunc(connected.Done)
	defer once()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.server(i)+"/api/v1/runs/"+runID+"/logs/stream", nil)
	req.Header.Set("Authorization", "Bearer "+d.token)
	resp, err := d.http.Do(req) // #nosec G704 -- a server of this test
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	once()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
			if event == "end" {
				return
			}
		case strings.HasPrefix(line, "data: ") && event == "line":
			var l logLine
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &l) == nil {
				if at, ok := sentAt(l.Text); ok {
					lat.add(time.Since(at))
					n.Add(1)
				}
			}
		}
	}
}

// poll reads a run's log page by page from server i, as a client that
// cannot use SSE would, and records when each line became visible.
func (d *deployment) poll(ctx context.Context, i int, runID string, lat *sampler) {
	cursor := ""
	for ctx.Err() == nil {
		path := "/api/v1/runs/" + runID + "/logs?limit=1000"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var page struct {
			Lines      []logLine `json:"lines"`
			NextCursor string    `json:"next_cursor"`
		}
		if _, err := d.api(ctx, i, http.MethodGet, path, nil, &page); err == nil {
			for _, l := range page.Lines {
				if at, ok := sentAt(l.Text); ok {
					lat.add(time.Since(at))
				}
			}
			if page.NextCursor != "" {
				cursor = page.NextCursor
			}
			if len(page.Lines) == 1000 {
				continue
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// browse calls the read operations behind the console's overview, run
// list and run detail in a loop.
func (d *deployment) browse(ctx context.Context, i int, runs []string, lat map[string]*sampler) {
	rnd := rand.New(rand.NewPCG(uint64(i), 7)) // #nosec G404 G115 -- picks a run, not a secret; i is a small index
	ops := []struct {
		name string
		path func() string
	}{
		{"getOverview", func() string { return "/api/v1/overview" }},
		{"listRuns", func() string { return "/api/v1/runs?limit=50" }},
		{"getRun", func() string { return "/api/v1/runs/" + runs[rnd.IntN(len(runs))] }},
		{"readRunLogs", func() string { return "/api/v1/runs/" + runs[rnd.IntN(len(runs))] + "/logs?tail=200" }},
		{"listTasks", func() string { return "/api/v1/tasks?limit=50" }},
	}
	for n := 0; ctx.Err() == nil; n++ {
		op := ops[n%len(ops)]
		start := time.Now()
		if _, err := d.api(ctx, i, http.MethodGet, op.path(), nil, nil); err == nil {
			lat[op.name].add(time.Since(start))
		}
	}
}

// monitor samples the servers' memory and CPU, and the database
// container's for PostgreSQL.
type monitor struct {
	stopCh chan struct{}
	done   chan struct{}
	peak   float64
	cpu    []float64
	dbPeak string
}

func (d *deployment) monitor(ctx context.Context) *monitor {
	m := &monitor{stopCh: make(chan struct{}), done: make(chan struct{})}
	pids := make([]string, len(d.servers))
	for i, s := range d.servers {
		pids[i] = strconv.Itoa(s.cmd.Process.Pid)
	}
	go func() {
		defer close(m.done)
		var dbPeakMiB float64
		for n := 0; ; n++ {
			select {
			case <-m.stopCh:
				return
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			out, err := exec.Command("ps", "-o", "rss=,%cpu=", "-p", strings.Join(pids, ",")).Output() // #nosec G204 -- PIDs of this test's processes
			if err == nil {
				var rss, cpu float64
				for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
					f := strings.Fields(line)
					if len(f) == 2 {
						r, _ := strconv.ParseFloat(f[0], 64)
						c, _ := strconv.ParseFloat(f[1], 64)
						rss += r / 1024
						cpu += c
					}
				}
				m.peak = max(m.peak, rss)
				m.cpu = append(m.cpu, cpu)
			}
			if d.pgName != "" && n%5 == 0 {
				out, err := exec.Command("docker", "stats", "--no-stream", "--format", "{{.MemUsage}}|{{.CPUPerc}}", d.pgName).Output() // #nosec G204 -- the container this test started
				if err == nil {
					s := strings.TrimSpace(string(out))
					if mem, _, ok := strings.Cut(s, " /"); ok {
						if v := parseMiB(mem); v > dbPeakMiB {
							dbPeakMiB, m.dbPeak = v, s
						}
					}
				}
			}
		}
	}()
	return m
}

func (m *monitor) stop(res *result) {
	close(m.stopCh)
	<-m.done
	res.ServerPeakRSSMiB = round(m.peak)
	sum := 0.0
	for _, c := range m.cpu {
		sum += c
	}
	if len(m.cpu) > 0 {
		res.ServerAvgCPU = round(sum / float64(len(m.cpu)))
	}
	res.DatabasePeak = m.dbPeak
}

func parseMiB(s string) float64 {
	s = strings.TrimSpace(s)
	for unit, f := range map[string]float64{"GiB": 1024, "MiB": 1, "KiB": 1.0 / 1024, "B": 1.0 / (1 << 20)} {
		if v, ok := strings.CutSuffix(s, unit); ok {
			n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err == nil {
				return n * f
			}
		}
	}
	return 0
}

func (d *deployment) databaseBytes() int64 {
	if d.dbPath != "" {
		var total int64
		for _, suffix := range []string{"", "-wal"} {
			if fi, err := os.Stat(d.dbPath + suffix); err == nil {
				total += fi.Size()
			}
		}
		return total
	}
	out, err := exec.Command("docker", "exec", d.pgName, "psql", "-U", "lerryn", "-h", "127.0.0.1", "-tAc", "select pg_database_size('lerryn')").Output() // #nosec G204 -- the container this test started
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return n
}

// parallel runs fn(0..n-1) on workers goroutines.
func parallel(workers, n int, fn func(i int)) {
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(workers, n) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				fn(i)
			}
		}()
	}
	wg.Wait()
}

func machine() string {
	cpu := runtime.GOARCH
	if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
		cpu = strings.TrimSpace(string(out))
	} else if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(l, "model name") {
				_, cpu, _ = strings.Cut(l, ":")
				cpu = strings.TrimSpace(cpu)
				break
			}
		}
	}
	mem := ""
	if out, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
		if b, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
			mem = fmt.Sprintf(", %d GiB", b>>30)
		}
	}
	return fmt.Sprintf("%s, %d CPUs%s, %s/%s, %s", cpu, runtime.NumCPU(), mem, runtime.GOOS, runtime.GOARCH, runtime.Version())
}

func writeResults(t *testing.T, dir string, results []result) {
	if len(results) == 0 {
		return
	}
	data, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "results.json"), data, 0o600); err != nil { // #nosec G703 -- the developer chose the directory
		t.Error(err)
	}
	var b strings.Builder
	for _, r := range results {
		p := r.Profile
		fmt.Fprintf(&b, "## %s\n\n", p.Name)
		cpus := "unlimited"
		if p.ServerCPUs > 0 {
			cpus = fmt.Sprintf("GOMAXPROCS=%d", p.ServerCPUs)
		}
		fmt.Fprintf(&b, "%s; lerryn %s; %d server(s) on %s (%s); %d tasks; %d synthetic runners x %d slots; log phase %s with %d runs x %d lines/s, %d SSE subscribers, %d API clients.\n\n",
			r.Machine, r.Version, p.Servers, p.Database, cpus, p.Tasks, p.Runners, p.Slots, r.Duration, p.LogRuns, p.LinesPerSecond, p.SSESubscribers, p.APIClients)
		b.WriteString("| 指标 | 结果 | 目标 | 达标 |\n| --- | --- | --- | --- |\n")
		for _, v := range r.Verdicts {
			met := "是"
			if !v.Met {
				met = "否"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", v.Metric, v.Got, v.Target, met)
		}
		b.WriteString("\n| 分布（ms） | n | p50 | p95 | p99 | max |\n| --- | --- | --- | --- | --- | --- |\n")
		row := func(name string, d dist) {
			fmt.Fprintf(&b, "| %s | %d | %.1f | %.1f | %.1f | %.1f |\n", name, d.N, d.P50, d.P95, d.P99, d.Max)
		}
		row("applyTask", r.ApplyLatency)
		row("claim latency (idle runners)", r.ClaimLatency)
		row("queue wait (burst)", r.BurstQueueWait)
		row("log batch upload", r.LogBatch)
		row("log visible (poll)", r.LogVisible)
		row("SSE delivery", r.SSEDelivery)
		ops := make([]string, 0, len(r.API))
		for op := range r.API {
			ops = append(ops, op)
		}
		sort.Strings(ops)
		for _, op := range ops {
			row(op, r.API[op])
		}
		fmt.Fprintf(&b, "\napplyTask %.0f/s；server 峰值 RSS %.0f MiB，平均 CPU %.0f%%；数据库 %.1f MiB", r.ApplyPerSecond, r.ServerPeakRSSMiB, r.ServerAvgCPU, float64(r.DatabaseBytes)/(1<<20))
		if r.DatabasePeak != "" {
			fmt.Fprintf(&b, "，PostgreSQL 容器峰值 %s", r.DatabasePeak)
		}
		fmt.Fprintf(&b, "；runner 调用返回 5xx 后重试 %d 次。\n\n", r.RetriedCalls)
		if len(r.ServerErrors) > 0 {
			b.WriteString("server 日志中的告警和错误（前 20 行）：\n\n```\n" + strings.Join(r.ServerErrors, "\n") + "\n```\n\n")
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "results.md"), []byte(b.String()), 0o600); err != nil { // #nosec G703 -- the developer chose the directory
		t.Error(err)
	}
	t.Logf("results in %s:\n%s", dir, b.String())
}
