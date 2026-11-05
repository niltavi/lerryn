package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"

	"github.com/niltavi/lerryn/server/internal/executor"
)

// fakeDaemon simulates one container's lifecycle. Unused methods of api
// panic through the nil embedded interface.
type fakeDaemon struct {
	api

	mu      sync.Mutex
	exists  bool
	running bool
	code    int
	signals []string
	// stopsOn maps a signal to how long the container takes to exit after
	// receiving it. Signals not listed are ignored.
	stopsOn map[string]time.Duration
	// waitReturnsEarly makes the wait endpoint answer at once, as it does
	// when the daemon restarts, even though the container still runs.
	waitReturnsEarly bool
	// killConflict makes the container exit just before this signal
	// arrives, so the kill gets a conflict.
	killConflict string
	exitedCh     chan struct{}

	// Start path.
	warnings []string
	// imageJSON is the image inspect response, as the daemon sends it.
	imageJSON     string
	created       int
	removed       []client.ContainerRemoveOptions
	listFilters   client.Filters
	info          system.Info
	inspectCalls  int
	runningProbes []bool
}

func newFakeDaemon(running bool) *fakeDaemon {
	return &fakeDaemon{exists: true, running: running, exitedCh: make(chan struct{}), stopsOn: map[string]time.Duration{}}
}

func (f *fakeDaemon) exit(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running {
		f.running = false
		f.code = code
		close(f.exitedCh)
	}
}

func (f *fakeDaemon) ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspectCalls++
	if !f.exists {
		return client.ContainerInspectResult{}, cerrdefs.ErrNotFound
	}
	f.runningProbes = append(f.runningProbes, f.running)
	status := container.StateExited
	if f.running {
		status = container.StateRunning
	}
	return client.ContainerInspectResult{Container: container.InspectResponse{
		State: &container.State{Status: status, Running: f.running, ExitCode: f.code},
	}}, nil
}

func (f *fakeDaemon) ContainerWait(ctx context.Context, _ string, _ client.ContainerWaitOptions) client.ContainerWaitResult {
	res := make(chan container.WaitResponse, 1)
	errc := make(chan error, 1)
	if f.waitReturnsEarly {
		errc <- errors.New("daemon restarted")
		return client.ContainerWaitResult{Result: res, Error: errc}
	}
	f.mu.Lock()
	ch := f.exitedCh
	f.mu.Unlock()
	go func() {
		select {
		case <-ch:
			res <- container.WaitResponse{}
		case <-ctx.Done():
			errc <- ctx.Err()
		}
	}()
	return client.ContainerWaitResult{Result: res, Error: errc}
}

func (f *fakeDaemon) ContainerKill(_ context.Context, _ string, opts client.ContainerKillOptions) (client.ContainerKillResult, error) {
	f.mu.Lock()
	f.signals = append(f.signals, opts.Signal)
	conflict := f.killConflict == opts.Signal
	if conflict {
		f.killConflict = ""
	}
	delay, stops := f.stopsOn[opts.Signal]
	f.mu.Unlock()
	if conflict {
		f.exit(0)
		return client.ContainerKillResult{}, cerrdefs.ErrConflict.WithMessage("container is not running")
	}
	if stops {
		code := 143
		if opts.Signal == "SIGKILL" {
			code = 137
		}
		time.AfterFunc(delay, func() { f.exit(code) })
	}
	return client.ContainerKillResult{}, nil
}

func (f *fakeDaemon) ContainerRemove(_ context.Context, _ string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, opts)
	if f.running && !opts.Force {
		return client.ContainerRemoveResult{}, cerrdefs.ErrConflict.WithMessage("container is running")
	}
	f.exists = false
	return client.ContainerRemoveResult{}, nil
}

func (f *fakeDaemon) ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	var res image.InspectResponse
	if f.imageJSON != "" {
		if err := json.Unmarshal([]byte(f.imageJSON), &res); err != nil {
			return client.ImageInspectResult{}, err
		}
	}
	return client.ImageInspectResult{InspectResponse: res}, nil
}

func (f *fakeDaemon) ContainerCreate(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created++
	return client.ContainerCreateResult{ID: "c1", Warnings: f.warnings}, nil
}

func (f *fakeDaemon) ContainerList(_ context.Context, opts client.ContainerListOptions) (client.ContainerListResult, error) {
	f.listFilters = opts.Filters
	return client.ContainerListResult{Items: []container.Summary{{
		ID:     "c1",
		Names:  []string{"/lerryn-run-r1"},
		State:  container.StateRunning,
		Labels: map[string]string{executor.LabelRunID: "r1", executor.LabelRunnerID: "runner-1"},
	}}}, nil
}

func (f *fakeDaemon) Info(context.Context, client.InfoOptions) (client.SystemInfoResult, error) {
	return client.SystemInfoResult{Info: f.info}, nil
}

func newTestExecutor(t *testing.T, d *fakeDaemon, opts Options) *Executor {
	t.Helper()
	e, err := newWithAPI(d, opts)
	if err != nil {
		t.Fatal(err)
	}
	e.pollInterval = 5 * time.Millisecond
	return e
}

func TestStop(t *testing.T) {
	const grace = 100 * time.Millisecond
	tests := []struct {
		name          string
		setup         func(*fakeDaemon)
		wantSignals   []string
		wantCode      int
		wantEscalated bool
		wantAlready   bool
		minDuration   time.Duration
	}{
		{
			name:        "honours SIGTERM",
			setup:       func(d *fakeDaemon) { d.stopsOn["SIGTERM"] = 10 * time.Millisecond },
			wantSignals: []string{"SIGTERM"},
			wantCode:    143,
		},
		{
			name:          "ignores SIGTERM",
			setup:         func(d *fakeDaemon) { d.stopsOn["SIGKILL"] = 10 * time.Millisecond },
			wantSignals:   []string{"SIGTERM", "SIGKILL"},
			wantCode:      137,
			wantEscalated: true,
			minDuration:   grace,
		},
		{
			name: "wait endpoint returns before the container stops",
			setup: func(d *fakeDaemon) {
				d.waitReturnsEarly = true
				d.stopsOn["SIGTERM"] = 60 * time.Millisecond
			},
			wantSignals: []string{"SIGTERM"},
			wantCode:    143,
			minDuration: 60 * time.Millisecond,
		},
		{
			name:        "already exited",
			setup:       func(d *fakeDaemon) { d.exit(0) },
			wantSignals: nil,
			wantCode:    0,
			wantAlready: true,
		},
		{
			name:        "exits between inspect and signal",
			setup:       func(d *fakeDaemon) { d.killConflict = "SIGTERM" },
			wantSignals: []string{"SIGTERM"},
			wantCode:    0,
			wantAlready: true,
		},
		{
			name:        "exits before escalation",
			setup:       func(d *fakeDaemon) { d.killConflict = "SIGKILL" },
			wantSignals: []string{"SIGTERM", "SIGKILL"},
			wantCode:    0,
			minDuration: grace,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newFakeDaemon(true)
			tc.setup(d)
			e := newTestExecutor(t, d, Options{})
			began := time.Now()
			got, err := e.Stop(t.Context(), "c1", grace)
			took := time.Since(began)
			if err != nil {
				t.Fatalf("Stop() error = %v", err)
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			if d.running {
				t.Fatal("Stop returned success while the container runs")
			}
			if !slices.Equal(d.signals, tc.wantSignals) {
				t.Errorf("signals = %v, want %v", d.signals, tc.wantSignals)
			}
			if got.Exit.Code != tc.wantCode || got.Escalated != tc.wantEscalated || got.AlreadyExited != tc.wantAlready {
				t.Errorf("Stop() = %+v", got)
			}
			if took < tc.minDuration {
				t.Errorf("Stop() took %v, want at least %v", took, tc.minDuration)
			}
			// The last inspect, which the result is based on, saw the
			// container stopped.
			if n := len(d.runningProbes); n == 0 || d.runningProbes[n-1] {
				t.Errorf("result not based on an inspect showing the container stopped: %v", d.runningProbes)
			}
		})
	}
}

func TestStopNeverConfirmed(t *testing.T) {
	// The container ignores every signal, as when the daemon is wedged.
	d := newFakeDaemon(true)
	e := newTestExecutor(t, d, Options{})
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	_, err := e.Stop(ctx, "c1", 50*time.Millisecond)
	if !errors.Is(err, executor.ErrNotStopped) {
		t.Fatalf("Stop() error = %v, want ErrNotStopped", err)
	}
	if !slices.Equal(d.signals, []string{"SIGTERM", "SIGKILL"}) {
		t.Fatalf("signals = %v", d.signals)
	}
}

func TestStopNotFound(t *testing.T) {
	d := newFakeDaemon(false)
	d.exists = false
	e := newTestExecutor(t, d, Options{})
	if _, err := e.Stop(t.Context(), "c1", time.Second); !errors.Is(err, executor.ErrNotFound) {
		t.Fatalf("Stop() error = %v, want ErrNotFound", err)
	}
}

func TestRemove(t *testing.T) {
	t.Run("stopped", func(t *testing.T) {
		d := newFakeDaemon(false)
		e := newTestExecutor(t, d, Options{})
		if err := e.Remove(t.Context(), "c1"); err != nil {
			t.Fatal(err)
		}
		if len(d.removed) != 1 || d.removed[0].Force || !d.removed[0].RemoveVolumes {
			t.Fatalf("remove options = %+v", d.removed)
		}
	})
	t.Run("running is refused, not forced", func(t *testing.T) {
		d := newFakeDaemon(true)
		e := newTestExecutor(t, d, Options{})
		if err := e.Remove(t.Context(), "c1"); err == nil {
			t.Fatal("Remove() of a running container succeeded")
		}
		if !d.running || !d.exists {
			t.Fatal("running container was removed")
		}
	})
	t.Run("missing", func(t *testing.T) {
		d := newFakeDaemon(false)
		d.exists = false
		e := newTestExecutor(t, d, Options{})
		if err := e.Remove(t.Context(), "c1"); err != nil {
			t.Fatalf("Remove() of a missing container = %v", err)
		}
	})
}

func TestStartFailsClosed(t *testing.T) {
	t.Run("daemon discards a limit", func(t *testing.T) {
		d := newFakeDaemon(false)
		d.warnings = []string{"Your kernel does not support pids limit capabilities or the cgroup is not mounted. PIDs limit discarded."}
		e := newTestExecutor(t, d, Options{})
		_, err := e.Start(t.Context(), baseSpec(), executor.Output{})
		if !errors.Is(err, executor.ErrHardening) || !strings.Contains(err.Error(), "PIDs limit discarded") {
			t.Fatalf("Start() error = %v, want ErrHardening", err)
		}
		if len(d.removed) != 1 || !d.removed[0].Force {
			t.Fatalf("created container not removed: %+v", d.removed)
		}
	})
	t.Run("image declares volumes", func(t *testing.T) {
		d := newFakeDaemon(false)
		d.imageJSON = `{"Config": {"Volumes": {"/data": {}}}}`
		e := newTestExecutor(t, d, Options{})
		_, err := e.Start(t.Context(), baseSpec(), executor.Output{})
		if !errors.Is(err, executor.ErrHardening) || !strings.Contains(err.Error(), "/data") {
			t.Fatalf("Start() error = %v, want ErrHardening", err)
		}
		if d.created != 0 {
			t.Fatal("container created despite image volumes")
		}
	})
	t.Run("mount outside allowed roots", func(t *testing.T) {
		d := newFakeDaemon(false)
		e := newTestExecutor(t, d, Options{AllowedMountRoots: []string{"/var/lib/lerryn"}})
		spec := baseSpec()
		spec.Mounts = []executor.Mount{
			{Source: "/var/lib/lerryn/runs/1/work", Target: "/sbx/work"},
			{Source: "/var/lib/lerrynx/other", Target: "/sbx/other"},
		}
		_, err := e.Start(t.Context(), spec, executor.Output{})
		if !errors.Is(err, executor.ErrInvalidSpec) || !strings.Contains(err.Error(), "/var/lib/lerrynx/other") {
			t.Fatalf("Start() error = %v, want ErrInvalidSpec for the outside mount", err)
		}
		if d.created != 0 {
			t.Fatal("container created despite a disallowed mount")
		}
	})
	t.Run("invalid spec", func(t *testing.T) {
		d := newFakeDaemon(false)
		e := newTestExecutor(t, d, Options{})
		spec := baseSpec()
		spec.User = "0:0"
		if _, err := e.Start(t.Context(), spec, executor.Output{}); !errors.Is(err, executor.ErrInvalidSpec) {
			t.Fatalf("Start() error = %v", err)
		}
		if d.created != 0 {
			t.Fatal("container created for an invalid spec")
		}
	})
}

func TestList(t *testing.T) {
	d := newFakeDaemon(true)
	e := newTestExecutor(t, d, Options{})
	got, err := e.List(t.Context(), map[string]string{executor.LabelRunnerID: "runner-1"})
	if err != nil {
		t.Fatal(err)
	}
	labels := d.listFilters["label"]
	if !labels[executor.LabelManaged+"=true"] || !labels[executor.LabelRunnerID+"=runner-1"] || len(labels) != 2 {
		t.Fatalf("label filters = %v", labels)
	}
	want := executor.Container{ID: "c1", Name: "lerryn-run-r1", RunID: "r1", RunnerID: "runner-1", Running: true}
	if len(got) != 1 || got[0].ID != want.ID || got[0].Name != want.Name || got[0].RunID != want.RunID || got[0].RunnerID != want.RunnerID || !got[0].Running {
		t.Fatalf("List() = %+v, want %+v", got, want)
	}
}

func TestCheck(t *testing.T) {
	full := system.Info{
		SecurityOptions: []string{"name=seccomp,profile=builtin", "name=cgroupns"},
		MemoryLimit:     true, CPUCfsQuota: true, PidsLimit: true,
		CgroupVersion: "2",
	}
	tests := []struct {
		name    string
		mutate  func(*system.Info)
		wantErr string
	}{
		{"all present", func(*system.Info) {}, ""},
		{"rootless is fine", func(i *system.Info) { i.SecurityOptions = append(i.SecurityOptions, "name=rootless") }, ""},
		{"no seccomp", func(i *system.Info) { i.SecurityOptions = []string{"name=cgroupns"} }, "seccomp"},
		{"unconfined seccomp", func(i *system.Info) { i.SecurityOptions = []string{"name=seccomp,profile=unconfined", "name=cgroupns"} }, "seccomp"},
		{"custom seccomp profile", func(i *system.Info) { i.SecurityOptions = []string{"name=seccomp,profile=/etc/docker/seccomp.json"} }, ""},
		{"seccomp-like name", func(i *system.Info) { i.SecurityOptions = []string{"name=seccompx"} }, "seccomp"},
		{"no pids limit", func(i *system.Info) { i.PidsLimit = false }, "pids limit"},
		{"no memory limit", func(i *system.Info) { i.MemoryLimit = false }, "memory limit"},
		{"no cpu quota", func(i *system.Info) { i.CPUCfsQuota = false }, "cpu quota"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newFakeDaemon(false)
			d.info = full
			d.info.SecurityOptions = slices.Clone(full.SecurityOptions)
			tc.mutate(&d.info)
			e := newTestExecutor(t, d, Options{})
			_, err := e.Check(t.Context())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Check() = %v", err)
				}
				return
			}
			if !errors.Is(err, executor.ErrHardening) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Check() = %v, want ErrHardening mentioning %q", err, tc.wantErr)
			}
		})
	}
}

func TestNewRejectsBadOptions(t *testing.T) {
	for _, opts := range []Options{
		{Labels: map[string]string{executor.LabelRunID: "x"}},
		{Labels: map[string]string{executor.LabelManaged: "false"}},
		{AllowedMountRoots: []string{"relative"}},
		{AllowedMountRoots: []string{"/var/lib/../lib"}},
	} {
		if _, err := newWithAPI(newFakeDaemon(false), opts); err == nil {
			t.Errorf("newWithAPI(%+v) succeeded", opts)
		}
	}
}

func TestForgivingWriter(t *testing.T) {
	failing := &failAfter{n: 1}
	w := &forgivingWriter{w: failing}
	for i := range 3 {
		n, err := w.Write([]byte("line\n"))
		if n != 5 || err != nil {
			t.Fatalf("write %d = %d, %v; the stream must keep draining", i, n, err)
		}
	}
	if failing.writes != 2 || w.err == nil {
		t.Fatalf("writes reaching the sink = %d, err = %v; want writes to stop after the first error", failing.writes, w.err)
	}
}

type failAfter struct {
	n      int
	writes int
}

func (f *failAfter) Write(p []byte) (int, error) {
	f.writes++
	if f.writes > f.n {
		return 0, fmt.Errorf("sink closed")
	}
	return len(p), nil
}

func TestDesktopDocker(t *testing.T) {
	for _, tc := range []struct {
		os   string
		want bool
	}{
		{"OrbStack", true},
		{"Docker Desktop", true},
		{"Docker Desktop 4.48.0 (207573)", true},
		{"Ubuntu 24.04.3 LTS", false},
		{"Debian GNU/Linux 13 (trixie)", false},
		{"ubuntu", false},
		{"", false},
	} {
		if got := (DaemonInfo{OperatingSystem: tc.os}).DesktopDocker(); got != tc.want {
			t.Errorf("DesktopDocker(%q) = %v, want %v", tc.os, got, tc.want)
		}
	}
}
