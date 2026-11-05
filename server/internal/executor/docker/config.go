package docker

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"

	"github.com/niltavi/lerryn/server/internal/executor"
)

// oomScoreAdj makes the host OOM killer pick task containers before the
// runner, the server or a database on the same host.
const oomScoreAdj = 500

// shmBytes sizes /dev/shm. Docker's built-in default is the same, but
// daemons can change it (OrbStack uses half the VM's memory), so it is set
// explicitly.
const shmBytes = 64 << 20

// createConfig is everything ContainerCreate needs for one run.
type createConfig struct {
	Name       string
	Config     *container.Config
	HostConfig *container.HostConfig
	Networking *network.NetworkingConfig
}

// buildCreateConfig maps a validated spec to the hardened container
// configuration. It is a pure function so that every hardening default is
// covered by unit tests without a Docker daemon.
func buildCreateConfig(spec executor.Spec, opts Options) (createConfig, error) {
	if err := spec.Validate(); err != nil {
		return createConfig{}, err
	}
	res := spec.Resources.WithDefaults()

	user := spec.User
	if user == "" {
		user = executor.DefaultUser
	}
	uid, gid, _ := strings.Cut(user, ":")

	grace := spec.StopGrace
	if grace <= 0 {
		grace = executor.DefaultStopGrace
	}
	graceSeconds := int((grace + time.Second - 1) / time.Second)

	labels := make(map[string]string, len(opts.Labels)+3)
	maps.Copy(labels, opts.Labels)
	labels[executor.LabelManaged] = "true"
	labels[executor.LabelRunID] = spec.RunID
	labels[executor.LabelRunnerID] = spec.RunnerID

	cfg := &container.Config{
		Image:      spec.Image,
		User:       user,
		Env:        append([]string(nil), spec.Env...),
		WorkingDir: spec.WorkingDir,
		// Replace both so nothing the image declares runs implicitly.
		Entrypoint:  spec.Command[:1],
		Cmd:         append([]string(nil), spec.Command[1:]...),
		Labels:      labels,
		StopSignal:  "SIGTERM",
		StopTimeout: &graceSeconds,
		// Output is read through attach; there is no TTY so stdout and
		// stderr stay separate.
		AttachStdout: true,
		AttachStderr: true,
	}
	if spec.Stdin != nil {
		// Stdin stays open until the executor has written spec.Stdin and
		// closed its side; StdinOnce then closes it in the container.
		cfg.OpenStdin, cfg.StdinOnce, cfg.AttachStdin = true, true, true
	}

	nofile := res.OpenFiles
	pids := res.PIDs
	init := true
	host := &container.HostConfig{
		// Output goes only to the runner through attach. Any log driver
		// would keep an unredacted copy on the runner host's disk.
		LogConfig:     container.LogConfig{Type: "none"},
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
		// The runner inspects the exit status and removes the container
		// itself, after it is sure the container stopped.
		AutoRemove: false,

		Privileged:     false,
		ReadonlyRootfs: true,
		CapDrop:        []string{"ALL"},
		// Leaving out a seccomp= entry keeps the daemon's default profile.
		SecurityOpt:  []string{"no-new-privileges:true"},
		IpcMode:      container.IPCModePrivate,
		ShmSize:      shmBytes,
		CgroupnsMode: container.CgroupnsModePrivate,
		OomScoreAdj:  oomScoreAdj,
		// An init process forwards SIGTERM to the task and reaps orphans.
		// Without it the task is PID 1, which ignores SIGTERM unless it
		// installs a handler, so every stop would wait out the grace period.
		Init: &init,
		Tmpfs: map[string]string{
			executor.TmpDir: fmt.Sprintf("rw,noexec,nosuid,nodev,size=%d,mode=1777", res.TmpBytes),
			// Only the task user can read credentials; nothing in it is
			// executable.
			executor.SecretsDir: fmt.Sprintf("rw,noexec,nosuid,nodev,size=%d,mode=0700,uid=%s,gid=%s",
				res.SecretsBytes, uid, gid),
		},
		Mounts: bindMounts(spec.Mounts),
		Resources: container.Resources{
			NanoCPUs: res.MilliCPUs * 1_000_000,
			Memory:   res.MemoryBytes,
			// Equal to Memory: no swap, so the memory limit is a hard limit.
			MemorySwap: res.MemoryBytes,
			PidsLimit:  &pids,
			Ulimits: []*container.Ulimit{
				{Name: "nofile", Soft: nofile, Hard: nofile},
			},
		},
	}

	var networking *network.NetworkingConfig
	switch spec.Network {
	case executor.NetworkNone:
		host.NetworkMode = network.NetworkNone
	case executor.NetworkFull:
		host.NetworkMode = container.NetworkMode(opts.NetworkName)
		networking = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{opts.NetworkName: {}},
		}
	}

	return createConfig{
		Name:       containerName(spec.RunID),
		Config:     cfg,
		HostConfig: host,
		Networking: networking,
	}, nil
}

func bindMounts(mounts []executor.Mount) []mount.Mount {
	if len(mounts) == 0 {
		return nil
	}
	out := make([]mount.Mount, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, mount.Mount{
			// Type bind fails when the source is missing. The legacy Binds
			// field would silently create an empty directory on the host,
			// which hides a runner/host path mismatch (tech design 15.2).
			Type:     mount.TypeBind,
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
			BindOptions: &mount.BindOptions{
				Propagation: mount.PropagationRPrivate,
				// Submounts under a source are not carried into the task.
				NonRecursive: true,
			},
		})
	}
	return out
}

func containerName(runID string) string {
	return "lerryn-run-" + runID
}
