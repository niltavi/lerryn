package docker

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"

	"github.com/niltavi/lerryn/server/internal/executor"
)

func baseSpec() executor.Spec {
	return executor.Spec{
		RunID:    "0192f1a2-0000-7000-8000-000000000001",
		RunnerID: "runner-1",
		Image:    "lerryn-runtime-python@sha256:0000",
		Command:  []string{"python", "main.py", "--flag"},
		Env:      []string{"A=1"},
		Network:  executor.NetworkNone,
	}
}

var testOpts = Options{NetworkName: "lerryn-tasks", Labels: map[string]string{"lerryn.instance": "prod"}}

func mustBuild(t *testing.T, spec executor.Spec) createConfig {
	t.Helper()
	cc, err := buildCreateConfig(spec, testOpts)
	if err != nil {
		t.Fatalf("buildCreateConfig() error = %v", err)
	}
	return cc
}

// TestBuildCreateConfigHardening pins every default from tech design 7.2.
// A change here is a change to the sandbox and needs review.
func TestBuildCreateConfigHardening(t *testing.T) {
	cc := mustBuild(t, baseSpec())
	cfg, host := cc.Config, cc.HostConfig

	checks := []struct {
		name string
		ok   bool
	}{
		{"non-root default user", cfg.User == executor.DefaultUser},
		{"read-only rootfs", host.ReadonlyRootfs},
		{"not privileged", !host.Privileged},
		{"all capabilities dropped", slices.Equal(host.CapDrop, []string{"ALL"}) && len(host.CapAdd) == 0},
		{"no-new-privileges", slices.Contains(host.SecurityOpt, "no-new-privileges:true")},
		{"default seccomp kept", !slices.ContainsFunc(host.SecurityOpt, func(s string) bool { return strings.HasPrefix(s, "seccomp") })},
		{"no apparmor override", !slices.ContainsFunc(host.SecurityOpt, func(s string) bool { return strings.HasPrefix(s, "apparmor") })},
		{"init process", host.Init != nil && *host.Init},
		{"private ipc", host.IpcMode == container.IPCModePrivate},
		{"bounded /dev/shm", host.ShmSize == shmBytes},
		{"private cgroupns", host.CgroupnsMode == container.CgroupnsModePrivate},
		{"host pid not shared", host.PidMode == ""},
		{"no userns override", host.UsernsMode == ""},
		{"no devices", len(host.Devices) == 0 && len(host.DeviceRequests) == 0},
		{"no legacy binds", len(host.Binds) == 0 && len(host.VolumesFrom) == 0},
		{"no restart", host.RestartPolicy.Name == container.RestartPolicyDisabled},
		{"not auto-removed", !host.AutoRemove},
		{"no log driver copy", host.LogConfig.Type == "none"},
		{"oom score raised", host.OomScoreAdj == oomScoreAdj},
		{"no published ports", len(host.PortBindings) == 0 && !host.PublishAllPorts},
		{"no stdin or tty", !cfg.OpenStdin && !cfg.Tty && !cfg.AttachStdin && !cfg.StdinOnce},
		{"SIGTERM stop signal", cfg.StopSignal == "SIGTERM"},
		{"default grace recorded", cfg.StopTimeout != nil && *cfg.StopTimeout == 10},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s: not applied", c.name)
		}
	}
}

func TestBuildCreateConfigTmpfs(t *testing.T) {
	spec := baseSpec()
	spec.User = "1000:2000"
	spec.Resources.TmpBytes = 32 << 20
	cc := mustBuild(t, spec)
	tmp := cc.HostConfig.Tmpfs[executor.TmpDir]
	secrets := cc.HostConfig.Tmpfs[executor.SecretsDir]
	for _, want := range []string{"noexec", "nosuid", "nodev", "size=33554432", "mode=1777"} {
		if !strings.Contains(tmp, want) {
			t.Errorf("/tmp options %q lack %q", tmp, want)
		}
	}
	for _, want := range []string{"noexec", "nosuid", "nodev", "size=1048576", "mode=0700", "uid=1000", "gid=2000"} {
		if !strings.Contains(secrets, want) {
			t.Errorf("secrets options %q lack %q", secrets, want)
		}
	}
	if len(cc.HostConfig.Tmpfs) != 2 {
		t.Errorf("tmpfs mounts = %v, want exactly /tmp and the secrets dir", cc.HostConfig.Tmpfs)
	}
}

func TestBuildCreateConfigResources(t *testing.T) {
	tests := []struct {
		name string
		in   executor.Resources
		want executor.Resources
	}{
		{"defaults", executor.Resources{}, executor.Resources{}.WithDefaults()},
		{"explicit", executor.Resources{MilliCPUs: 1500, MemoryBytes: 512 << 20, PIDs: 100, OpenFiles: 4096},
			executor.Resources{MilliCPUs: 1500, MemoryBytes: 512 << 20, PIDs: 100, OpenFiles: 4096}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := baseSpec()
			spec.Resources = tc.in
			r := mustBuild(t, spec).HostConfig.Resources
			want := tc.want.WithDefaults()
			if r.NanoCPUs != want.MilliCPUs*1_000_000 {
				t.Errorf("NanoCPUs = %d", r.NanoCPUs)
			}
			if r.Memory != want.MemoryBytes || r.MemorySwap != want.MemoryBytes {
				t.Errorf("Memory = %d, MemorySwap = %d, want both %d (no swap)", r.Memory, r.MemorySwap, want.MemoryBytes)
			}
			if r.PidsLimit == nil || *r.PidsLimit != want.PIDs {
				t.Errorf("PidsLimit = %v, want %d", r.PidsLimit, want.PIDs)
			}
			if len(r.Ulimits) != 1 || r.Ulimits[0].Name != "nofile" || r.Ulimits[0].Soft != want.OpenFiles || r.Ulimits[0].Hard != want.OpenFiles {
				t.Errorf("Ulimits = %+v, want nofile %d", r.Ulimits, want.OpenFiles)
			}
			if r.OomKillDisable != nil && *r.OomKillDisable {
				t.Error("OOM killer disabled")
			}
		})
	}
}

func TestBuildCreateConfigCommand(t *testing.T) {
	spec := baseSpec()
	spec.WorkingDir = "/sbx/work"
	cc := mustBuild(t, spec)
	if !slices.Equal(cc.Config.Entrypoint, []string{"python"}) || !slices.Equal(cc.Config.Cmd, []string{"main.py", "--flag"}) {
		t.Errorf("entrypoint %v cmd %v", cc.Config.Entrypoint, cc.Config.Cmd)
	}
	if cc.Config.WorkingDir != "/sbx/work" || !slices.Equal(cc.Config.Env, []string{"A=1"}) || cc.Config.Image != spec.Image {
		t.Errorf("config = %+v", cc.Config)
	}
	if cc.Name != "lerryn-run-"+spec.RunID {
		t.Errorf("name = %q", cc.Name)
	}
	// The config must not share slices with the spec.
	spec.Command[1] = "changed"
	if cc.Config.Cmd[0] != "main.py" {
		t.Error("Cmd aliases the spec's Command")
	}
}

func TestBuildCreateConfigLabels(t *testing.T) {
	cc := mustBuild(t, baseSpec())
	want := map[string]string{
		executor.LabelManaged:  "true",
		executor.LabelRunID:    "0192f1a2-0000-7000-8000-000000000001",
		executor.LabelRunnerID: "runner-1",
		"lerryn.instance":      "prod",
	}
	if len(cc.Config.Labels) != len(want) {
		t.Errorf("labels = %v, want %v", cc.Config.Labels, want)
	}
	for k, v := range want {
		if cc.Config.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, cc.Config.Labels[k], v)
		}
	}
}

func TestBuildCreateConfigNetwork(t *testing.T) {
	none := mustBuild(t, baseSpec())
	if none.HostConfig.NetworkMode != "none" || none.Networking != nil {
		t.Errorf("none: mode %q, networking %+v", none.HostConfig.NetworkMode, none.Networking)
	}

	spec := baseSpec()
	spec.Network = executor.NetworkFull
	full := mustBuild(t, spec)
	if full.HostConfig.NetworkMode != "lerryn-tasks" {
		t.Errorf("full: mode %q, want the dedicated network", full.HostConfig.NetworkMode)
	}
	if full.Networking == nil || len(full.Networking.EndpointsConfig) != 1 || full.Networking.EndpointsConfig["lerryn-tasks"] == nil {
		t.Errorf("full: endpoints %+v", full.Networking)
	}
}

func TestBuildCreateConfigMounts(t *testing.T) {
	spec := baseSpec()
	spec.Mounts = []executor.Mount{
		{Source: "/var/lib/lerryn/runs/1/task", Target: "/sbx/task", ReadOnly: true},
		{Source: "/var/lib/lerryn/runs/1/work", Target: "/sbx/work"},
	}
	mounts := mustBuild(t, spec).HostConfig.Mounts
	if len(mounts) != 2 {
		t.Fatalf("mounts = %+v", mounts)
	}
	for i, m := range mounts {
		in := spec.Mounts[i]
		if m.Type != mount.TypeBind || m.Source != in.Source || m.Target != in.Target || m.ReadOnly != in.ReadOnly {
			t.Errorf("mount %d = %+v, want %+v", i, m, in)
		}
		if m.BindOptions == nil || m.BindOptions.CreateMountpoint || !m.BindOptions.NonRecursive || m.BindOptions.Propagation != mount.PropagationRPrivate {
			t.Errorf("mount %d bind options = %+v", i, m.BindOptions)
		}
	}
}

func TestBuildCreateConfigGrace(t *testing.T) {
	spec := baseSpec()
	spec.StopGrace = 2500 * time.Millisecond // rounds up
	if got := *mustBuild(t, spec).Config.StopTimeout; got != 3 {
		t.Errorf("StopTimeout = %d, want 3", got)
	}
}

func TestBuildCreateConfigRejectsInvalidSpec(t *testing.T) {
	spec := baseSpec()
	spec.User = "0:0"
	if _, err := buildCreateConfig(spec, testOpts); !errors.Is(err, executor.ErrInvalidSpec) {
		t.Fatalf("buildCreateConfig() error = %v, want ErrInvalidSpec", err)
	}
}
