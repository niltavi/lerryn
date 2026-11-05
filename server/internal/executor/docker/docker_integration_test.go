//go:build integration

package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/niltavi/lerryn/server/internal/executor"
)

func TestCheckDaemon(t *testing.T) {
	t.Parallel()
	info, err := testExec.Check(t.Context())
	t.Logf("daemon: %+v", info)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
}

// TestHardening runs one container and checks each default from inside it,
// so the assertions are about what the kernel enforces, not about what was
// requested.
func TestHardening(t *testing.T) {
	t.Parallel()
	script := `
echo "uid=$(id -u)"
echo "gid=$(id -g)"
grep -E '^(CapInh|CapPrm|CapEff|CapBnd|CapAmb|NoNewPrivs|Seccomp):' /proc/self/status | tr -d '\t' | sed 's/:/=/'
if touch /probe 2>/dev/null; then echo rootfs_write=ok; else echo rootfs_write=denied; fi
if echo x > /etc/probe 2>/dev/null; then echo etc_write=ok; else echo etc_write=denied; fi
if echo x > /tmp/probe 2>/dev/null; then echo tmp_write=ok; else echo tmp_write=denied; fi
cp /bin/busybox /tmp/bb 2>/dev/null
if /tmp/bb true 2>/dev/null; then echo tmp_exec=ok; else echo tmp_exec=denied; fi
if echo s > /run/secrets/probe 2>/dev/null; then echo secrets_write=ok; else echo secrets_write=denied; fi
echo "secrets_stat=$(stat -c %a:%u:%g /run/secrets)"
echo "nofile=$(ulimit -n)"
echo "pids_max=$(cat /sys/fs/cgroup/pids.max 2>/dev/null || echo na)"
echo "memory_max=$(cat /sys/fs/cgroup/memory.max 2>/dev/null || echo na)"
echo "swap_max=$(cat /sys/fs/cgroup/memory.swap.max 2>/dev/null || echo na)"
echo "cpu_max=$(cat /sys/fs/cgroup/cpu.max 2>/dev/null || echo na)"
echo "pid1=$(cat /proc/1/comm)"
echo "shm=$(awk '$2 == "/dev/shm" {print $4}' /proc/self/mounts)"
echo "mounts=$(awk '{print $2}' /proc/self/mounts | grep -E '^/(sbx|tmp|run/secrets|var/run/docker.sock|run/docker.sock)' | sort | tr '\n' ',')"
`
	spec := newSpec(t, executor.NetworkNone, "sh", "-c", script)
	spec.Resources = executor.Resources{
		MilliCPUs:   500,
		MemoryBytes: 96 << 20,
		PIDs:        48,
		OpenFiles:   300,
	}
	s := start(t, spec)
	labels := inspect(t, s.exec.ID()).Config.Labels
	st, err := s.exec.Wait(t.Context())
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	out := s.stdout.String()
	t.Logf("stdout:\n%s\nstderr:\n%s", out, s.stderr.String())
	if st.Code != 0 {
		t.Fatalf("probe script exited %d", st.Code)
	}
	if err := testExec.Remove(t.Context(), s.exec.ID()); err != nil {
		t.Fatalf("remove: %v", err)
	}
	kv := keyValues(out)
	expect := func(t *testing.T, key, want string) {
		t.Helper()
		if got := kv[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	t.Run("RunsAsNonRoot", func(t *testing.T) {
		expect(t, "uid", "65532")
		expect(t, "gid", "65532")
	})
	t.Run("ReadOnlyRootfsRejectsWrites", func(t *testing.T) {
		expect(t, "rootfs_write", "denied")
		expect(t, "etc_write", "denied")
	})
	t.Run("TmpfsMounts", func(t *testing.T) {
		expect(t, "tmp_write", "ok")
		expect(t, "tmp_exec", "denied")
		expect(t, "secrets_write", "ok")
		expect(t, "secrets_stat", "700:65532:65532")
	})
	t.Run("NoCapabilities", func(t *testing.T) {
		for _, k := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
			expect(t, k, "0000000000000000")
		}
	})
	t.Run("NoNewPrivileges", func(t *testing.T) {
		expect(t, "NoNewPrivs", "1")
	})
	t.Run("DefaultSeccomp", func(t *testing.T) {
		// 2 is SECCOMP_MODE_FILTER: a filter is installed.
		expect(t, "Seccomp", "2")
	})
	t.Run("ResourceLimits", func(t *testing.T) {
		expect(t, "nofile", "300")
		expect(t, "pids_max", "48")
		expect(t, "memory_max", strconv.Itoa(96<<20))
		expect(t, "cpu_max", "50000 100000")
		if kv["swap_max"] != "na" {
			expect(t, "swap_max", "0")
		}
		if !strings.Contains(kv["shm"], "size=65536k") || !strings.Contains(kv["shm"], "noexec") {
			t.Errorf("/dev/shm options = %q, want 64 MiB and noexec", kv["shm"])
		}
	})
	t.Run("InitProcess", func(t *testing.T) {
		if kv["pid1"] == "sh" {
			t.Errorf("pid1 = sh, want an init process")
		}
	})
	t.Run("OnlyRequestedMounts", func(t *testing.T) {
		expect(t, "mounts", "/run/secrets,/tmp,")
	})
	t.Run("Labels", func(t *testing.T) {
		if labels[executor.LabelRunID] != spec.RunID || labels[executor.LabelRunnerID] != spec.RunnerID || labels[executor.LabelManaged] != "true" {
			t.Errorf("labels = %v", labels)
		}
	})
}

func TestNetworkNone(t *testing.T) {
	t.Parallel()
	script := `
echo "ifaces=$(ls /sys/class/net | tr '\n' ' ')"
if wget -T 3 -q -O /dev/null http://1.1.1.1/ 2>/dev/null; then echo ip=ok; else echo ip=fail; fi
if nslookup example.com >/dev/null 2>&1; then echo dns=ok; else echo dns=fail; fi
`
	st, out, _ := runToEnd(t, newSpec(t, executor.NetworkNone, "sh", "-c", script))
	if st.Code != 0 {
		t.Fatalf("exit %d", st.Code)
	}
	kv := keyValues(out)
	if kv["ifaces"] != "lo" {
		t.Errorf("interfaces = %q, want only lo", kv["ifaces"])
	}
	if kv["ip"] != "fail" || kv["dns"] != "fail" {
		t.Errorf("network reachable with network none: %v", kv)
	}
}

func TestNetworkFull(t *testing.T) {
	t.Parallel()
	server := start(t, newSpec(t, executor.NetworkFull, "httpd", "-f", "-p", "8080", "-h", "/etc"))
	info := inspect(t, server.exec.ID())
	nets := info.NetworkSettings.Networks
	ep, ok := nets[testExec.opts.NetworkName]
	if !ok || len(nets) != 1 {
		t.Fatalf("networks = %v, want only %s", keys(nets), testExec.opts.NetworkName)
	}

	t.Run("IsolatedFromOtherTasks", func(t *testing.T) {
		spec := newSpec(t, executor.NetworkFull, "sh", "-c",
			`if wget -T 3 -q -O /dev/null "http://$PEER:8080/hostname"; then echo peer=ok; else echo peer=fail; fi; echo "ifaces=$(ls /sys/class/net | tr '\n' ' ')"`)
		spec.Env = []string{"PEER=" + ep.IPAddress.String()}
		_, out, _ := runToEnd(t, spec)
		kv := keyValues(out)
		if !strings.Contains(kv["ifaces"], "eth0") {
			t.Errorf("interfaces = %q, want eth0", kv["ifaces"])
		}
		if kv["peer"] == "ok" {
			msg := "a task reached another task's listener: the daemon does not enforce com.docker.network.bridge.enable_icc=false"
			if os.Getenv(envRequireICC) == "1" {
				t.Fatal(msg)
			}
			t.Skip(msg)
		}
	})

	if _, err := testExec.Stop(t.Context(), server.exec.ID(), time.Second); err != nil {
		t.Fatalf("stop server: %v", err)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestOutputStreams(t *testing.T) {
	t.Parallel()
	const n = 3000
	script := fmt.Sprintf(`i=0; while [ $i -lt %d ]; do echo "out $i"; echo "err $i" >&2; i=$((i+1)); done; printf tail`, n)
	st, out, errOut := runToEnd(t, newSpec(t, executor.NetworkNone, "sh", "-c", script))
	if st.Code != 0 {
		t.Fatalf("exit %d", st.Code)
	}
	// Wait returned, so every line must already be here, including the
	// last one without a newline.
	outLines := strings.Split(out, "\n")
	if len(outLines) != n+1 || outLines[n] != "tail" {
		t.Fatalf("stdout has %d lines, last %q", len(outLines), outLines[len(outLines)-1])
	}
	for i := range n {
		if outLines[i] != fmt.Sprintf("out %d", i) {
			t.Fatalf("stdout line %d = %q", i, outLines[i])
		}
	}
	if got := strings.Count(errOut, "\n"); got != n || strings.Contains(errOut, "out ") {
		t.Fatalf("stderr has %d lines; mixed streams: %v", got, strings.Contains(errOut, "out "))
	}
}

func TestExitCode(t *testing.T) {
	t.Parallel()
	st, _, _ := runToEnd(t, newSpec(t, executor.NetworkNone, "sh", "-c", "exit 3"))
	if st.Code != 3 || st.OOMKilled {
		t.Fatalf("status = %+v, want code 3", st)
	}
	if st.StartedAt.IsZero() || st.FinishedAt.Before(st.StartedAt) {
		t.Fatalf("times = %v .. %v", st.StartedAt, st.FinishedAt)
	}
}

func TestMemoryLimit(t *testing.T) {
	t.Parallel()
	// dd allocates its block size up front, so a 256 MiB block cannot fit
	// in a 64 MiB container.
	const alloc = "dd if=/dev/zero of=/dev/null bs=256M count=1"

	t.Run("MainProcessKilled", func(t *testing.T) {
		t.Parallel()
		spec := newSpec(t, executor.NetworkNone, "sh", "-c", "exec "+alloc)
		spec.Resources.MemoryBytes = 64 << 20
		st, _, _ := runToEnd(t, spec)
		if st.Code != 137 || !st.OOMKilled {
			t.Fatalf("status = %+v, want code 137 and OOMKilled", st)
		}
	})
	t.Run("ChildKilledMainSurvives", func(t *testing.T) {
		t.Parallel()
		spec := newSpec(t, executor.NetworkNone, "sh", "-c", alloc+"; echo child=$?")
		spec.Resources.MemoryBytes = 64 << 20
		st, out, _ := runToEnd(t, spec)
		// The run succeeds, yet the OOM flag is set: OOMKilled alone does
		// not mean the run failed because of memory.
		if st.Code != 0 || !st.OOMKilled || keyValues(out)["child"] != "137" {
			t.Fatalf("status = %+v, stdout %q", st, out)
		}
	})
}

func TestPidsLimitContainsForkBomb(t *testing.T) {
	t.Parallel()
	const limit = 32
	m := marker()
	// Forked subshells keep the parent's command line, so every bomb
	// process carries the marker in $0. Each bomb process forks in a loop:
	// the classic ":|:&" bomb can die out under a low limit, because every
	// process exits right after forking and failed forks end lineages. The
	// main shell forks only twice and then waits, so it survives the bomb
	// and keeps the container running.
	spec := newSpec(t, executor.NetworkNone, "sh", "-c", `echo started; sleep "$1" & b() { while :; do b & done; }; b & wait`, "bomb-"+m, m)
	spec.Resources.PIDs = limit
	spec.Resources.MilliCPUs = 250
	s := start(t, spec)
	waitForOutput(t, s.stdout, "started", 10*time.Second)

	// fork() failing with EAGAIN is the kernel refusing a new process; no
	// RLIMIT_NPROC is set, so only the pids cgroup can cause it. The process
	// count from the daemon's stats (read from the container's cgroup) must
	// never exceed the limit meanwhile. Sampling continues for a while
	// after the first refusal, when the bomb pushes hardest.
	const refused = "can't fork: Resource temporarily unavailable"
	var peak, reportedLimit uint64
	var refusedAt time.Time
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		cur, lim, err := pidsCurrent(t.Context(), s.exec.ID())
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		reportedLimit = lim
		peak = max(peak, cur)
		if refusedAt.IsZero() && strings.Contains(s.stderr.String(), refused) {
			refusedAt = time.Now()
		}
		if !refusedAt.IsZero() && time.Since(refusedAt) > 2*time.Second {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("peak pids %d, limit %d (stats reports %d)", peak, limit, reportedLimit)
	if refusedAt.IsZero() {
		t.Fatalf("no fork was refused; the bomb did not reach the limit (peak %d)", peak)
	}
	if peak > limit {
		t.Fatalf("peak pids %d exceeds limit %d", peak, limit)
	}

	if procs := hostProcesses(t, m); len(procs) < 2 {
		t.Fatalf("process scan found %d bomb processes while the bomb runs", len(procs))
	}

	stopped, err := testExec.Stop(t.Context(), s.exec.ID(), time.Second)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if inspect(t, s.exec.ID()).State.Running {
		t.Fatal("Stop returned while the container is running")
	}
	if err := testExec.Remove(t.Context(), s.exec.ID()); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if left := hostProcesses(t, m); len(left) > 0 {
		t.Fatalf("%d bomb processes survived: %v", len(left), left[:min(5, len(left))])
	}
	t.Logf("stopped: %+v", stopped)
}

// processTreeScript starts nested children that all run "sleep $M" and
// reports ready. With IGNORE_TERM=1 every process ignores SIGTERM.
const processTreeScript = `
[ "$IGNORE_TERM" = 1 ] && trap '' TERM
sleep "$M" &
sh -c 'sleep "$M" & wait' &
(sleep "$M"; true) &
echo ready
wait
`

func treeSpec(t *testing.T, m string, ignoreTerm bool) executor.Spec {
	spec := newSpec(t, executor.NetworkNone, "sh", "-c", processTreeScript)
	spec.Env = []string{"M=" + m, "IGNORE_TERM=0"}
	if ignoreTerm {
		spec.Env[1] = "IGNORE_TERM=1"
	}
	return spec
}

// TestCancelStopsProcessTree is acceptance scenario A09 at the executor
// level: after a cancel, the container and every process it started are
// gone, and "canceled" is reported only after the container stopped.
func TestCancelStopsProcessTree(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		ignoreTerm bool
		wantCode   int
	}{
		{"HonorsSIGTERM", false, 143},
		{"IgnoresSIGTERM", true, 137},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			const grace = 2 * time.Second
			m := marker()
			s := start(t, treeSpec(t, m, tc.ignoreTerm))
			waitForOutput(t, s.stdout, "ready", 10*time.Second)
			if procs := hostProcesses(t, "sleep "+m); len(procs) < 3 {
				t.Fatalf("expected at least 3 child processes before cancel, found %v", procs)
			}

			cancel := make(chan struct{})
			close(cancel)
			began := time.Now()
			res, err := executor.Supervise(t.Context(), testExec, s.exec, executor.SuperviseOptions{Grace: grace, Cancel: cancel})
			took := time.Since(began)
			if err != nil {
				t.Fatalf("supervise: %v", err)
			}
			// Checked before anything else touches the container: when
			// Supervise reports canceled, the container must already be
			// stopped.
			if state := inspect(t, s.exec.ID()).State; state.Running {
				t.Fatalf("canceled reported while container running: %+v", state)
			}
			if res.Outcome != executor.OutcomeCanceled || res.Exit.Code != tc.wantCode || res.Escalated != tc.ignoreTerm {
				t.Fatalf("result = %+v, want canceled, code %d, escalated %v", res, tc.wantCode, tc.ignoreTerm)
			}
			if tc.ignoreTerm && took < grace {
				t.Fatalf("stop took %v, less than the %v grace period", took, grace)
			}
			if !tc.ignoreTerm && took >= grace {
				t.Fatalf("stop took %v although the task honours SIGTERM", took)
			}

			if err := testExec.Remove(t.Context(), s.exec.ID()); err != nil {
				t.Fatalf("remove: %v", err)
			}
			assertGone(t, s.exec.ID())
			if left := hostProcesses(t, "sleep "+m); len(left) > 0 {
				t.Fatalf("processes survived the cancel: %v", left)
			}
			t.Logf("stop took %v: %+v", took, res)
		})
	}
}

func TestTimeoutStopsProcessTree(t *testing.T) {
	t.Parallel()
	m := marker()
	s := start(t, treeSpec(t, m, true))
	waitForOutput(t, s.stdout, "ready", 10*time.Second)

	const timeout, grace = 1500 * time.Millisecond, time.Second
	began := time.Now()
	res, err := executor.Supervise(t.Context(), testExec, s.exec, executor.SuperviseOptions{Timeout: timeout, Grace: grace})
	took := time.Since(began)
	if err != nil {
		t.Fatalf("supervise: %v", err)
	}
	if inspect(t, s.exec.ID()).State.Running {
		t.Fatal("timed out reported while container running")
	}
	if res.Outcome != executor.OutcomeTimedOut || !res.Escalated || res.Exit.Code != 137 {
		t.Fatalf("result = %+v", res)
	}
	if took < timeout+grace {
		t.Fatalf("returned after %v, before timeout plus grace", took)
	}
	if err := testExec.Remove(t.Context(), s.exec.ID()); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if left := hostProcesses(t, "sleep "+m); len(left) > 0 {
		t.Fatalf("processes survived the timeout: %v", left)
	}
}

func TestSuperviseExitBeatsTimeout(t *testing.T) {
	t.Parallel()
	s := start(t, newSpec(t, executor.NetworkNone, "sh", "-c", "echo done"))
	res, err := executor.Supervise(t.Context(), testExec, s.exec, executor.SuperviseOptions{Timeout: time.Minute})
	if err != nil {
		t.Fatalf("supervise: %v", err)
	}
	if res.Outcome != executor.OutcomeExited || res.Exit.Code != 0 || s.stdout.String() != "done\n" {
		t.Fatalf("result = %+v, stdout %q", res, s.stdout.String())
	}
}

// TestStopConfirmsBeforeReturning checks that Stop does not return success
// while the container still runs, using a task that ignores SIGTERM.
func TestStopConfirmsBeforeReturning(t *testing.T) {
	t.Parallel()
	s := start(t, newSpec(t, executor.NetworkNone, "sh", "-c", `trap '' TERM; echo ready; while :; do sleep 1; done`))
	waitForOutput(t, s.stdout, "ready", 10*time.Second)

	const grace = 2 * time.Second
	began := time.Now()
	stopped, err := testExec.Stop(t.Context(), s.exec.ID(), grace)
	took := time.Since(began)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	state := inspect(t, s.exec.ID()).State
	if state.Running || state.Status != container.StateExited {
		t.Fatalf("Stop returned but state is %+v", state)
	}
	if !stopped.Escalated || stopped.AlreadyExited || stopped.Exit.Code != 137 || took < grace {
		t.Fatalf("stopped = %+v after %v", stopped, took)
	}

	// A second stop finds it already exited.
	again, err := testExec.Stop(t.Context(), s.exec.ID(), grace)
	if err != nil || !again.AlreadyExited || again.Exit.Code != 137 {
		t.Fatalf("second stop = %+v, %v", again, err)
	}
	// Wait agrees with Stop.
	st, err := s.exec.Wait(t.Context())
	if err != nil || st.Code != 137 {
		t.Fatalf("wait = %+v, %v", st, err)
	}
	if err := testExec.Remove(t.Context(), s.exec.ID()); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := testExec.Stop(t.Context(), s.exec.ID(), grace); !errors.Is(err, executor.ErrNotFound) {
		t.Fatalf("stop after remove: %v, want ErrNotFound", err)
	}
}

func mountRoot(t *testing.T) string {
	t.Helper()
	root := os.Getenv(envMountRoot)
	if root == "" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp(root, "s4-mount-")
	if err != nil {
		t.Fatalf("mkdir under %s: %v", envMountRoot, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) }) //nolint:gosec // dir comes from os.MkdirTemp
	return dir
}

func TestMounts(t *testing.T) {
	t.Parallel()
	root := mountRoot(t)
	taskDir := filepath.Join(root, "task")
	workDir := filepath.Join(root, "work")
	for _, d := range []string{taskDir, workDir} {
		if err := os.Mkdir(d, 0o755); err != nil { //nolint:gosec // the task user must traverse it
			t.Fatal(err)
		}
	}
	// The task user is not the test user; a runner would chown instead.
	if err := os.Chmod(workDir, 0o777); err != nil { //nolint:gosec // the task user must write it
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "input.txt"), []byte("hello from host\n"), 0o644); err != nil { //nolint:gosec // the task user must read it
		t.Fatal(err)
	}

	spec := newSpec(t, executor.NetworkNone, "sh", "-c", `
echo "input=$(cat /sbx/task/input.txt)"
if echo x > /sbx/task/new 2>/dev/null; then echo task_write=ok; else echo task_write=denied; fi
if echo result > /sbx/work/out.txt; then echo work_write=ok; else echo work_write=denied; fi
echo "sbx=$(ls /sbx | tr '\n' ' ')"
`)
	spec.Mounts = []executor.Mount{
		{Source: taskDir, Target: "/sbx/task", ReadOnly: true},
		{Source: workDir, Target: "/sbx/work"},
	}
	spec.WorkingDir = "/sbx/work"
	st, out, errOut := runToEnd(t, spec)
	if st.Code != 0 {
		t.Fatalf("exit %d: %s", st.Code, errOut)
	}
	kv := keyValues(out)
	if kv["input"] != "hello from host" || kv["task_write"] != "denied" || kv["work_write"] != "ok" || kv["sbx"] != "task work" {
		t.Fatalf("mount checks: %v", kv)
	}
	got, err := os.ReadFile(filepath.Join(workDir, "out.txt")) //nolint:gosec // path built by the test
	if err != nil || string(got) != "result\n" {
		t.Fatalf("host sees %q, %v", got, err)
	}
	if fi, err := os.Stat(filepath.Join(workDir, "out.txt")); err == nil {
		t.Logf("file written by the task is owned by %s on the test host", ownerOf(fi))
	}

	t.Run("MissingSourceFails", func(t *testing.T) {
		missing := newSpec(t, executor.NetworkNone, "true")
		missing.Mounts = []executor.Mount{{Source: filepath.Join(root, "does-not-exist"), Target: "/sbx/work"}}
		_, err := testExec.Start(t.Context(), missing, executor.Output{})
		if err == nil {
			t.Fatal("start succeeded with a missing mount source")
		}
		t.Logf("missing source error: %v", err)
		left, lerr := testExec.List(t.Context(), map[string]string{executor.LabelRunID: missing.RunID})
		if lerr != nil || len(left) != 0 {
			t.Fatalf("failed start left containers: %v, %v", left, lerr)
		}
	})
}

func TestListByLabels(t *testing.T) {
	t.Parallel()
	a := newSpec(t, executor.NetworkNone, "sleep", "300")
	b := newSpec(t, executor.NetworkNone, "sleep", "300")
	b.RunnerID = a.RunnerID + "-other"
	sa, sb := start(t, a), start(t, b)

	byRun, err := testExec.List(t.Context(), map[string]string{executor.LabelRunID: a.RunID})
	if err != nil {
		t.Fatal(err)
	}
	if len(byRun) != 1 || byRun[0].ID != sa.exec.ID() || byRun[0].RunnerID != a.RunnerID || !byRun[0].Running {
		t.Fatalf("by run id: %+v", byRun)
	}
	byRunner, err := testExec.List(t.Context(), map[string]string{executor.LabelRunnerID: b.RunnerID})
	if err != nil {
		t.Fatal(err)
	}
	if len(byRunner) != 1 || byRunner[0].ID != sb.exec.ID() {
		t.Fatalf("by runner id: %+v", byRunner)
	}

	// What a restarted runner does (tech design 7.6): stop and remove
	// everything it owns.
	for _, c := range byRunner {
		if _, err := testExec.Stop(t.Context(), c.ID, time.Second); err != nil {
			t.Fatal(err)
		}
		if err := testExec.Remove(t.Context(), c.ID); err != nil {
			t.Fatal(err)
		}
	}
	after, err := testExec.List(t.Context(), map[string]string{executor.LabelRunnerID: b.RunnerID})
	if err != nil || len(after) != 0 {
		t.Fatalf("after cleanup: %+v, %v", after, err)
	}
}

func TestRejectsImageWithVolumes(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	base, err := testAPI.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: testImage, Cmd: []string{"true"}, Labels: map[string]string{testLabel: testID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testAPI.ContainerRemove(context.Background(), base.ID, client.ContainerRemoveOptions{Force: true})
	})
	ref := "lerryn-test-volume:" + testID
	if _, err := testAPI.ContainerCommit(ctx, base.ID, client.ContainerCommitOptions{Reference: ref, Changes: []string{"VOLUME /data"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testAPI.ImageRemove(context.Background(), ref, client.ImageRemoveOptions{Force: true})
	})

	spec := newSpec(t, executor.NetworkNone, "true")
	spec.Image = ref
	_, err = testExec.Start(ctx, spec, executor.Output{})
	if !errors.Is(err, executor.ErrHardening) {
		t.Fatalf("start = %v, want ErrHardening", err)
	}
}

func TestRemoveRefusesRunningContainer(t *testing.T) {
	t.Parallel()
	s := start(t, newSpec(t, executor.NetworkNone, "sleep", "300"))
	err := testExec.Remove(t.Context(), s.exec.ID())
	if err == nil || !cerrdefs.IsConflict(errors.Unwrap(err)) && !strings.Contains(err.Error(), "running") {
		t.Fatalf("remove running container = %v, want conflict", err)
	}
	if !inspect(t, s.exec.ID()).State.Running {
		t.Fatal("container was stopped by a refused remove")
	}
}
