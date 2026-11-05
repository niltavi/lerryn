//go:build integration

package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/niltavi/lerryn/server/internal/executor"
)

// Environment variables understood by the integration tests.
const (
	// envImage overrides the test image. It needs busybox applets.
	envImage = "LERRYN_TEST_IMAGE"
	// envMountRoot is where the mount tests create host directories. When
	// the tests run inside a container against the host's daemon, it must
	// be a directory mounted at the same path inside and outside (tech
	// design 15.2).
	envMountRoot = "LERRYN_TEST_MOUNT_ROOT"
	// envRequireICC makes the inter-container isolation check fail instead
	// of skip when the daemon does not enforce it.
	envRequireICC = "LERRYN_TEST_REQUIRE_ICC"
)

// testLabel marks every container, network and image the tests create, so
// cleanup never touches anything else on the daemon.
const testLabel = "lerryn.test"

var (
	testImage = "busybox:1.37.0"
	testID    string
	testExec  *Executor
	testAPI   *client.Client
	runSeq    atomic.Int64
)

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	if v := os.Getenv(envImage); v != "" {
		testImage = v
	}
	testID = fmt.Sprintf("s4-%d-%d", time.Now().Unix(), os.Getpid())

	cli, err := client.New(client.FromEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "docker client:", err)
		return 1
	}
	testAPI = cli
	defer func() { _ = cli.Close() }()

	testExec, err = New(Options{
		NetworkName: "lerryn-test-" + testID,
		Labels:      map[string]string{testLabel: testID},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "executor:", err)
		return 1
	}
	defer func() { _ = testExec.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := ensureImage(ctx, testImage); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	code := m.Run()
	if err := cleanupAll(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "cleanup:", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

func ensureImage(ctx context.Context, ref string) error {
	if _, err := testAPI.ImageInspect(ctx, ref); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect %s: %w", ref, err)
	}
	resp, err := testAPI.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer func() { _ = resp.Close() }()
	if err := resp.Wait(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	return nil
}

// cleanupAll removes everything labelled with this process's test ID and
// fails if anything survives.
func cleanupAll(ctx context.Context) error {
	f := make(client.Filters).Add("label", testLabel+"="+testID)
	var errs []error
	cs, err := testAPI.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	for _, c := range cs.Items {
		if _, err := testAPI.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !cerrdefs.IsNotFound(err) {
			errs = append(errs, err)
		}
	}
	ns, err := testAPI.NetworkList(ctx, client.NetworkListOptions{Filters: f})
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("list networks: %w", err))...)
	}
	for _, n := range ns.Items {
		if _, err := testAPI.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
			errs = append(errs, err)
		}
	}
	cs, err = testAPI.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err == nil && len(cs.Items) > 0 {
		errs = append(errs, fmt.Errorf("%d test containers survived cleanup", len(cs.Items)))
	}
	ns, err = testAPI.NetworkList(ctx, client.NetworkListOptions{Filters: f})
	if err == nil && len(ns.Items) > 0 {
		errs = append(errs, fmt.Errorf("%d test networks survived cleanup", len(ns.Items)))
	}
	return errors.Join(errs...)
}

var unsafeIDChars = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// newSpec returns a spec with a unique run ID for this test.
func newSpec(t *testing.T, network executor.Network, command ...string) executor.Spec {
	t.Helper()
	name := unsafeIDChars.ReplaceAllString(t.Name(), "-")
	if len(name) > 60 {
		name = name[:60]
	}
	return executor.Spec{
		RunID:    fmt.Sprintf("%s-%s-%d", testID, name, runSeq.Add(1)),
		RunnerID: "runner-" + testID,
		Image:    testImage,
		Command:  command,
		Network:  network,
		Resources: executor.Resources{
			MilliCPUs:   500,
			MemoryBytes: 128 << 20,
			PIDs:        64,
			OpenFiles:   512,
		},
	}
}

// buffer is an io.Writer safe for the output goroutine and test reads.
type buffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type started struct {
	exec   executor.Execution
	stdout *buffer
	stderr *buffer
}

// start starts spec and makes sure the container is gone after the test,
// whatever the test did.
func start(t *testing.T, spec executor.Spec) started {
	t.Helper()
	s := started{stdout: &buffer{}, stderr: &buffer{}}
	ex, err := testExec.Start(t.Context(), spec, executor.Output{Stdout: s.stdout, Stderr: s.stderr})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	s.exec = ex
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := testAPI.ContainerRemove(ctx, ex.ID(), client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		if err != nil && !cerrdefs.IsNotFound(err) {
			t.Errorf("cleanup %s: %v", ex.ID(), err)
		}
	})
	return s
}

// runToEnd starts spec, waits for it to exit and removes it.
func runToEnd(t *testing.T, spec executor.Spec) (executor.ExitStatus, string, string) {
	t.Helper()
	s := start(t, spec)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	st, err := s.exec.Wait(ctx)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := testExec.Remove(ctx, s.exec.ID()); err != nil {
		t.Fatalf("remove: %v", err)
	}
	return st, s.stdout.String(), s.stderr.String()
}

func waitForOutput(t *testing.T, b *buffer, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(b.String(), want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("output %q did not appear within %v; got %q", want, timeout, b.String())
}

// keyValues parses "key=value" lines.
func keyValues(out string) map[string]string {
	kv := map[string]string{}
	for line := range strings.Lines(out) {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			kv[k] = v
		}
	}
	return kv
}

func inspect(t *testing.T, id string) container.InspectResponse {
	t.Helper()
	res, err := testAPI.ContainerInspect(t.Context(), id, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect %s: %v", id, err)
	}
	return res.Container
}

func assertGone(t *testing.T, id string) {
	t.Helper()
	_, err := testAPI.ContainerInspect(t.Context(), id, client.ContainerInspectOptions{})
	if !cerrdefs.IsNotFound(err) {
		t.Fatalf("container %s still exists (inspect err: %v)", id, err)
	}
}

// marker returns a number to embed in process command lines so the host
// process scan can find exactly this test's processes.
func marker() string {
	return fmt.Sprintf("%d", 70_000_000+rand.IntN(9_999_999)) //nolint:gosec // a test marker, not a secret
}

// hostProcesses lists processes anywhere on the daemon's host whose command
// line contains needle. It runs a short-lived root container in the host
// PID namespace, which sees processes of every container, including ones
// that escaped a container's cgroup or PID namespace. On macOS the "host"
// is the Linux VM that runs the daemon.
func hostProcesses(t *testing.T, needle string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	// The needle comes in through the environment so this script's own
	// command line never matches it.
	script := `for f in /proc/[0-9]*/cmdline; do
  c=$(tr '\0' ' ' < "$f" 2>/dev/null) || continue
  case "$c" in *"$NEEDLE"*) echo "${f%/cmdline} $c";; esac
done`
	init := false
	created, err := testAPI.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:  testImage,
			User:   "0:0",
			Env:    []string{"NEEDLE=" + needle},
			Cmd:    []string{"sh", "-c", script},
			Labels: map[string]string{testLabel: testID},
		},
		HostConfig: &container.HostConfig{
			PidMode:     container.PidMode("host"),
			NetworkMode: "none",
			Init:        &init,
		},
	})
	if err != nil {
		t.Fatalf("create process scanner: %v", err)
	}
	defer func() {
		_, _ = testAPI.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := testAPI.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start process scanner: %v", err)
	}
	wr := testAPI.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case <-wr.Result:
	case err := <-wr.Error:
		t.Fatalf("wait for process scanner: %v", err)
	}
	logs, err := testAPI.ContainerLogs(ctx, created.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		t.Fatalf("process scanner logs: %v", err)
	}
	defer func() { _ = logs.Close() }()
	var out, errOut bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &errOut, logs); err != nil {
		t.Fatalf("read process scanner logs: %v", err)
	}
	var procs []string
	for line := range strings.Lines(out.String()) {
		if line = strings.TrimSpace(line); line != "" {
			procs = append(procs, line)
		}
	}
	return procs
}

func ownerOf(fi os.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", st.Uid, st.Gid)
	}
	return "unknown"
}

// pidsCurrent reads the container's current process count from the
// daemon's stats endpoint.
func pidsCurrent(ctx context.Context, id string) (current, limit uint64, err error) {
	res, err := testAPI.ContainerStats(ctx, id, client.ContainerStatsOptions{})
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = res.Body.Close() }()
	var st container.StatsResponse
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		return 0, 0, err
	}
	return st.PidsStats.Current, st.PidsStats.Limit, nil
}
