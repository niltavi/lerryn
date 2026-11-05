// Package docker implements executor.Executor on the Docker Engine API. It
// also works with Podman's Docker-compatible API.
package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/niltavi/lerryn/server/internal/executor"
)

// DefaultNetworkName is the bridge network for task containers with
// network "full". Keeping tasks off the default bridge keeps them away from
// whatever else runs on that bridge.
const DefaultNetworkName = "lerryn-tasks"

// api is the part of the Docker client the executor uses. *client.Client
// implements it; tests substitute fakes.
type api interface {
	ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerAttach(ctx context.Context, id string, options client.ContainerAttachOptions) (client.ContainerAttachResult, error)
	ContainerStart(ctx context.Context, id string, options client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerWait(ctx context.Context, id string, options client.ContainerWaitOptions) client.ContainerWaitResult
	ContainerInspect(ctx context.Context, id string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerKill(ctx context.Context, id string, options client.ContainerKillOptions) (client.ContainerKillResult, error)
	ContainerRemove(ctx context.Context, id string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error)
	ImageInspect(ctx context.Context, image string, opts ...client.ImageInspectOption) (client.ImageInspectResult, error)
	ImagePull(ctx context.Context, ref string, options client.ImagePullOptions) (client.ImagePullResponse, error)
	NetworkCreate(ctx context.Context, name string, options client.NetworkCreateOptions) (client.NetworkCreateResult, error)
	NetworkInspect(ctx context.Context, name string, options client.NetworkInspectOptions) (client.NetworkInspectResult, error)
	Info(ctx context.Context, options client.InfoOptions) (client.SystemInfoResult, error)
}

// Options configures the Docker executor.
type Options struct {
	// Host is the daemon address, for example unix:///var/run/docker.sock.
	// Empty means DOCKER_HOST, then the platform default.
	Host string
	// NetworkName is the bridge network for network "full". Empty means
	// DefaultNetworkName. The executor creates it when missing.
	NetworkName string
	// Labels are added to every container and network the executor
	// creates, for example to tell instances on one host apart. Keys must
	// not collide with the executor's own labels.
	Labels map[string]string
	// AllowedMountRoots, when set, restricts mount sources to these
	// directories. A runner sets it to its data directory so a bug that
	// produces a wrong path cannot expose the rest of the host.
	AllowedMountRoots []string
	// Logger receives operational warnings. Nil means slog.Default().
	Logger *slog.Logger
}

// Executor runs task containers on a Docker daemon.
type Executor struct {
	api  api
	opts Options
	log  *slog.Logger

	closeClient func() error
	// pollInterval bounds how long a missed or failed wait notification
	// can delay noticing that a container stopped.
	pollInterval time.Duration

	netMu    sync.Mutex
	netReady bool
}

var (
	_ executor.Executor     = (*Executor)(nil)
	_ executor.ImageEnsurer = (*Executor)(nil)
)

// New connects to the daemon described by opts. It does not contact the
// daemon; call Check to verify it can enforce the default hardening.
func New(opts Options) (*Executor, error) {
	clientOpts := []client.Opt{client.FromEnv}
	if opts.Host != "" {
		clientOpts = append(clientOpts, client.WithHost(opts.Host))
	}
	cli, err := client.New(clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	e, err := newWithAPI(cli, opts)
	if err != nil {
		_ = cli.Close()
		return nil, err
	}
	e.closeClient = cli.Close
	return e, nil
}

func newWithAPI(a api, opts Options) (*Executor, error) {
	if opts.NetworkName == "" {
		opts.NetworkName = DefaultNetworkName
	}
	for k := range opts.Labels {
		if k == executor.LabelManaged || k == executor.LabelRunID || k == executor.LabelRunnerID {
			return nil, fmt.Errorf("label %q is reserved", k)
		}
	}
	for _, r := range opts.AllowedMountRoots {
		if !path.IsAbs(r) || path.Clean(r) != r {
			return nil, fmt.Errorf("allowed mount root %q must be a clean absolute path", r)
		}
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Executor{api: a, opts: opts, log: log, pollInterval: 500 * time.Millisecond}, nil
}

// Close releases the client connection.
func (e *Executor) Close() error {
	if e.closeClient == nil {
		return nil
	}
	return e.closeClient()
}

// DaemonInfo summarises what the daemon can enforce.
type DaemonInfo struct {
	ServerVersion   string
	OperatingSystem string
	KernelVersion   string
	CgroupVersion   string
	CgroupDriver    string
	SecurityOptions []string
	Rootless        bool
}

// Check verifies that the daemon can apply the default hardening: seccomp
// and the memory, CPU and PID limits. A runner refuses to take runs when it
// fails, instead of running tasks with less isolation than documented.
func (e *Executor) Check(ctx context.Context) (DaemonInfo, error) {
	res, err := e.api.Info(ctx, client.InfoOptions{})
	if err != nil {
		return DaemonInfo{}, fmt.Errorf("docker info: %w", err)
	}
	info := res.Info
	di := DaemonInfo{
		ServerVersion:   info.ServerVersion,
		OperatingSystem: info.OperatingSystem,
		KernelVersion:   info.KernelVersion,
		CgroupVersion:   info.CgroupVersion,
		CgroupDriver:    info.CgroupDriver,
		SecurityOptions: info.SecurityOptions,
	}
	hasSeccomp := false
	for _, o := range info.SecurityOptions {
		if seccompEnabled(o) {
			hasSeccomp = true
		}
		if o == "name=rootless" {
			di.Rootless = true
		}
	}
	var missing []string
	if !hasSeccomp {
		missing = append(missing, "seccomp")
	}
	if !info.MemoryLimit {
		missing = append(missing, "memory limit")
	}
	if !info.CPUCfsQuota {
		missing = append(missing, "cpu quota")
	}
	if !info.PidsLimit {
		missing = append(missing, "pids limit")
	}
	if len(missing) > 0 {
		return di, fmt.Errorf("%w: daemon lacks %s", executor.ErrHardening, strings.Join(missing, ", "))
	}
	return di, nil
}

// DesktopDocker reports whether the daemon is a desktop Docker (OrbStack,
// Docker Desktop) running in a VM managed by the desktop app. Those apply
// the container hardening, but their networking does not honour the task
// network's enable_icc=false, so tasks with network "full" can reach each
// other (tech design section 20). The runner still starts; it says so.
func (d DaemonInfo) DesktopDocker() bool {
	switch {
	case d.OperatingSystem == "OrbStack", strings.HasPrefix(d.OperatingSystem, "Docker Desktop"):
		return true
	}
	return false
}

// seccompEnabled reports whether a daemon security option, such as
// "name=seccomp,profile=builtin", enables seccomp. A daemon started with
// --seccomp-profile=unconfined still lists seccomp, but its containers run
// without a filter, so that counts as no seccomp.
func seccompEnabled(option string) bool {
	fields := strings.Split(option, ",")
	if fields[0] != "name=seccomp" {
		return false
	}
	for _, f := range fields[1:] {
		if f == "profile=unconfined" {
			return false
		}
	}
	return true
}

// Start implements executor.Executor.
func (e *Executor) Start(ctx context.Context, spec executor.Spec, out executor.Output) (executor.Execution, error) {
	cc, err := buildCreateConfig(spec, e.opts)
	if err != nil {
		return nil, err
	}
	if err := e.checkMountRoots(spec.Mounts); err != nil {
		return nil, err
	}
	if err := e.checkImage(ctx, spec.Image); err != nil {
		return nil, err
	}
	if spec.Network == executor.NetworkFull {
		if err := e.ensureNetwork(ctx); err != nil {
			return nil, err
		}
	}

	created, err := e.api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:             cc.Name,
		Config:           cc.Config,
		HostConfig:       cc.HostConfig,
		NetworkingConfig: cc.Networking,
	})
	if err != nil {
		return nil, fmt.Errorf("create container for run %s: %w", spec.RunID, err)
	}
	id := created.ID
	// From here on a failure must not leave the container behind.
	fail := func(err error) (executor.Execution, error) {
		rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if _, rmErr := e.api.ContainerRemove(rmCtx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); rmErr != nil && !cerrdefs.IsNotFound(rmErr) {
			err = errors.Join(err, fmt.Errorf("remove container %s: %w", id, rmErr))
		}
		return nil, err
	}
	if len(created.Warnings) > 0 {
		// Docker reports discarded limits (for example when a cgroup
		// controller is not delegated) as warnings and runs the container
		// anyway. Fail closed instead.
		return fail(fmt.Errorf("%w: run %s: %s", executor.ErrHardening, spec.RunID, strings.Join(created.Warnings, "; ")))
	}

	// Attach before start so no output is missed; with the "none" log
	// driver this stream is the only copy of the output.
	withStdin := spec.Stdin != nil
	att, err := e.api.ContainerAttach(ctx, id, client.ContainerAttachOptions{Stream: true, Stdin: withStdin, Stdout: true, Stderr: true})
	if err != nil {
		return fail(fmt.Errorf("attach to container %s: %w", id, err))
	}
	var stdin closeWriter
	if withStdin {
		// Without a half-close the container's stdin would never end, and
		// the sbx entrypoint would wait for it forever.
		cw, ok := att.Conn.(closeWriter)
		if !ok {
			att.Close()
			return fail(fmt.Errorf("attach to container %s: the connection to the daemon cannot close stdin", id))
		}
		stdin = cw
	}

	x := &execution{id: id, api: e.api, done: make(chan struct{}), outputDone: make(chan struct{})}
	go x.copyOutput(att.HijackedResponse, out, e.log)

	if _, err := e.api.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		att.Close()
		<-x.outputDone
		return fail(fmt.Errorf("start container %s: %w", id, err))
	}
	if withStdin {
		if err := writeStdin(stdin, spec.Stdin); err != nil {
			// The container is running and waits for the rest of its
			// input; the forced removal in fail stops it.
			att.Close()
			<-x.outputDone
			return fail(fmt.Errorf("write stdin of container %s: %w", id, err))
		}
	}
	go x.watch(context.WithoutCancel(ctx))
	return x, nil
}

func (e *Executor) checkMountRoots(mounts []executor.Mount) error {
	if len(e.opts.AllowedMountRoots) == 0 {
		return nil
	}
	for _, m := range mounts {
		ok := slices.ContainsFunc(e.opts.AllowedMountRoots, func(root string) bool {
			return m.Source == root || strings.HasPrefix(m.Source, strings.TrimSuffix(root, "/")+"/")
		})
		if !ok {
			return fmt.Errorf("%w: mount source %q is outside the allowed roots", executor.ErrInvalidSpec, m.Source)
		}
	}
	return nil
}

// checkImage rejects images that declare VOLUME. Docker gives each such
// path a writable anonymous volume, which would bypass the read-only root
// and the "only mount what the run needs" rule.
func (e *Executor) checkImage(ctx context.Context, image string) error {
	res, err := e.api.ImageInspect(ctx, image)
	if err != nil {
		return fmt.Errorf("inspect image %s: %w", image, err)
	}
	if res.Config != nil && len(res.Config.Volumes) > 0 {
		vols := slices.Sorted(maps.Keys(res.Config.Volumes))
		return fmt.Errorf("%w: image %s declares volumes %s", executor.ErrHardening, image, strings.Join(vols, ", "))
	}
	return nil
}

// EnsureImage implements executor.ImageEnsurer. It pulls only when the
// daemon does not have ref, so a pinned image that is already present never
// touches the registry, and a runner without registry access keeps working
// with pre-loaded images.
func (e *Executor) EnsureImage(ctx context.Context, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("%w: image reference is empty", executor.ErrInvalidSpec)
	}
	res, err := e.api.ImageInspect(ctx, ref)
	if err == nil {
		return res.ID, nil
	}
	if !cerrdefs.IsNotFound(err) {
		return "", fmt.Errorf("inspect image %s: %w", ref, err)
	}
	e.log.Info("pulling image", "image", ref)
	pull, err := e.api.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return "", fmt.Errorf("pull image %s: %w", ref, err)
	}
	// Wait reads the progress stream to the end and turns an error message
	// in it into an error; the pull is only done when the stream ends.
	err = pull.Wait(ctx)
	_ = pull.Close()
	if err != nil {
		return "", fmt.Errorf("pull image %s: %w", ref, err)
	}
	res, err = e.api.ImageInspect(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("inspect image %s after pull: %w", ref, err)
	}
	return res.ID, nil
}

// Stop implements executor.Executor.
func (e *Executor) Stop(ctx context.Context, id string, grace time.Duration) (executor.Stopped, error) {
	if grace < 0 {
		grace = 0
	}
	st, err := e.state(ctx, id)
	if err != nil {
		return executor.Stopped{}, err
	}
	if !st.Running {
		return executor.Stopped{Exit: exitStatus(st), AlreadyExited: true}, nil
	}

	// Register for the exit before signalling so it cannot be missed.
	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	exited := e.waitNotRunning(waitCtx, id)

	sent, err := e.kill(ctx, id, "SIGTERM")
	if err != nil {
		return executor.Stopped{}, err
	}
	st, err = e.awaitStopped(ctx, id, exited, grace)
	if err != nil {
		return executor.Stopped{}, fmt.Errorf("%w: %s after SIGTERM: %w", executor.ErrNotStopped, id, err)
	}
	if st != nil {
		return executor.Stopped{Exit: exitStatus(st), AlreadyExited: !sent}, nil
	}

	sent, err = e.kill(ctx, id, "SIGKILL")
	if err != nil {
		return executor.Stopped{}, err
	}
	st, err = e.awaitStopped(ctx, id, exited, 0)
	if err != nil {
		return executor.Stopped{}, fmt.Errorf("%w: %s after SIGKILL: %w", executor.ErrNotStopped, id, err)
	}
	return executor.Stopped{Exit: exitStatus(st), Escalated: sent}, nil
}

// awaitStopped returns the container's state once it is no longer running.
// It returns nil, nil if limit (when positive) elapses first. The wait
// notification only triggers a check: the wait endpoint can return early,
// for example when the daemon restarts, so only an inspect that shows the
// container not running counts as confirmation.
func (e *Executor) awaitStopped(ctx context.Context, id string, exited <-chan struct{}, limit time.Duration) (*container.State, error) {
	var deadline <-chan time.Time
	if limit > 0 {
		t := time.NewTimer(limit)
		defer t.Stop()
		deadline = t.C
	}
	poll := time.NewTicker(e.pollInterval)
	defer poll.Stop()
	for {
		select {
		case <-exited:
			exited = nil // check now, then keep polling
		case <-poll.C:
		case <-deadline:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		st, err := e.state(ctx, id)
		if err != nil {
			return nil, err
		}
		if !st.Running {
			return st, nil
		}
	}
}

// waitNotRunning closes the returned channel when the daemon's wait
// endpoint returns, successfully or not.
func (e *Executor) waitNotRunning(ctx context.Context, id string) <-chan struct{} {
	ch := make(chan struct{})
	res := e.api.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	go func() {
		defer close(ch)
		select {
		case <-res.Result:
		case err := <-res.Error:
			// Only a hint to look now; the inspect that follows decides.
			e.log.Debug("container wait returned an error", "container", id, "err", err)
		case <-ctx.Done():
		}
	}()
	return ch
}

// kill reports whether the daemon accepted the signal. A conflict means
// the process exited before receiving it; Stop still confirms that by inspect.
func (e *Executor) kill(ctx context.Context, id, signal string) (sent bool, err error) {
	_, err = e.api.ContainerKill(ctx, id, client.ContainerKillOptions{Signal: signal})
	switch {
	case err == nil:
		return true, nil
	case cerrdefs.IsConflict(err):
		// The container exited between the inspect and the signal.
		return false, nil
	case cerrdefs.IsNotFound(err):
		return false, fmt.Errorf("%w: %s", executor.ErrNotFound, id)
	default:
		return false, fmt.Errorf("send %s to %s: %w", signal, id, err)
	}
}

func (e *Executor) state(ctx context.Context, id string) (*container.State, error) {
	res, err := e.api.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s", executor.ErrNotFound, id)
		}
		return nil, fmt.Errorf("inspect %s: %w", id, err)
	}
	if res.Container.State == nil {
		return nil, fmt.Errorf("inspect %s: no state", id)
	}
	return res.Container.State, nil
}

// Remove implements executor.Executor.
func (e *Executor) Remove(ctx context.Context, id string) error {
	// Not forced: removing a running container would skip the graceful
	// stop. Callers stop first.
	_, err := e.api.ContainerRemove(ctx, id, client.ContainerRemoveOptions{RemoveVolumes: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove %s: %w", id, err)
	}
	if _, err := e.state(ctx, id); !errors.Is(err, executor.ErrNotFound) {
		if err == nil {
			return fmt.Errorf("remove %s: container still exists", id)
		}
		return fmt.Errorf("confirm removal of %s: %w", id, err)
	}
	return nil
}

// List implements executor.Executor.
func (e *Executor) List(ctx context.Context, labels map[string]string) ([]executor.Container, error) {
	f := make(client.Filters).Add("label", executor.LabelManaged+"=true")
	for k, v := range labels {
		f.Add("label", k+"="+v)
	}
	res, err := e.api.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	out := make([]executor.Container, 0, len(res.Items))
	for _, c := range res.Items {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, executor.Container{
			ID:       c.ID,
			Name:     name,
			RunID:    c.Labels[executor.LabelRunID],
			RunnerID: c.Labels[executor.LabelRunnerID],
			Running:  c.State == container.StateRunning || c.State == container.StateRestarting || c.State == container.StatePaused,
			Labels:   c.Labels,
		})
	}
	return out, nil
}

// ensureNetwork creates the task network on first use. The bridge disables
// inter-container traffic so one task cannot reach another task's
// listeners; whether that holds depends on the daemon managing iptables
// (see docs/spikes/s4-container-execution.md).
//
// A network that already exists is checked before use: anyone with access
// to the daemon could have created one under this name without the
// isolation, and tasks would then silently share a bridge.
func (e *Executor) ensureNetwork(ctx context.Context) error {
	e.netMu.Lock()
	defer e.netMu.Unlock()
	if e.netReady {
		return nil
	}
	name := e.opts.NetworkName
	res, err := e.api.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		labels := make(map[string]string, len(e.opts.Labels)+1)
		maps.Copy(labels, e.opts.Labels)
		labels[executor.LabelManaged] = "true"
		_, err = e.api.NetworkCreate(ctx, name, client.NetworkCreateOptions{
			Driver: network.NetworkBridge,
			Labels: labels,
			Options: map[string]string{
				networkOptionICC: "false",
			},
		})
		if err == nil {
			e.netReady = true
			return nil
		}
		if cerrdefs.IsConflict(err) {
			// Another runner on this host created it first; check what it
			// created.
			res, err = e.api.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
		}
	}
	if err != nil {
		return fmt.Errorf("task network %s: %w", name, err)
	}
	if err := checkNetwork(name, res.Network); err != nil {
		return err
	}
	e.netReady = true
	return nil
}

// networkOptionICC is the bridge driver option that controls traffic
// between containers on the network.
const networkOptionICC = "com.docker.network.bridge.enable_icc"

// checkNetwork verifies that an existing task network isolates tasks from
// each other as one the executor creates does.
func checkNetwork(name string, n network.Inspect) error {
	var problems []string
	if n.Driver != network.NetworkBridge {
		problems = append(problems, fmt.Sprintf("uses the %q driver instead of %q", n.Driver, network.NetworkBridge))
	}
	if n.Options[networkOptionICC] != "false" {
		problems = append(problems, fmt.Sprintf("does not set %s=false", networkOptionICC))
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w: the existing task network %s %s; remove it (docker network rm %s) so the runner can create it with inter-container traffic disabled",
		executor.ErrHardening, name, strings.Join(problems, " and "), name)
}

func exitStatus(st *container.State) executor.ExitStatus {
	return executor.ExitStatus{
		Code:       st.ExitCode,
		OOMKilled:  st.OOMKilled,
		StartedAt:  parseTime(st.StartedAt),
		FinishedAt: parseTime(st.FinishedAt),
	}
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() <= 1 {
		return time.Time{}
	}
	return t.UTC()
}

// execution is a started container.
type execution struct {
	id  string
	api api

	outputDone chan struct{}

	done   chan struct{}
	status executor.ExitStatus
	err    error
}

func (x *execution) ID() string { return x.id }

func (x *execution) Wait(ctx context.Context) (executor.ExitStatus, error) {
	select {
	case <-x.done:
		return x.status, x.err
	case <-ctx.Done():
		return executor.ExitStatus{}, ctx.Err()
	}
}

// watch waits for the container to stop and for its output to drain, then
// records the exit status. ctx must not be cancelled with the Start call:
// the run lasts longer than the request that started it.
func (x *execution) watch(ctx context.Context) {
	defer close(x.done)
	res := x.api.ContainerWait(ctx, x.id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case <-res.Result:
	case err := <-res.Error:
		x.err = fmt.Errorf("wait for %s: %w", x.id, err)
		return
	}
	// The attach stream ends when the container's stdio closes, which is
	// at exit; waiting for it keeps "exited" from overtaking the last lines.
	<-x.outputDone
	res2, err := x.api.ContainerInspect(ctx, x.id, client.ContainerInspectOptions{})
	if err != nil {
		x.err = fmt.Errorf("inspect %s after exit: %w", x.id, err)
		return
	}
	if res2.Container.State == nil || res2.Container.State.Running {
		x.err = fmt.Errorf("%w: %s reported exited but is running", executor.ErrNotStopped, x.id)
		return
	}
	x.status = exitStatus(res2.Container.State)
}

func (x *execution) copyOutput(att client.HijackedResponse, out executor.Output, log *slog.Logger) {
	defer close(x.outputDone)
	defer att.Close()
	stdout := &forgivingWriter{w: out.Stdout}
	stderr := &forgivingWriter{w: out.Stderr}
	if _, err := stdcopy.StdCopy(stdout, stderr, att.Reader); err != nil && !errors.Is(err, io.EOF) {
		log.Warn("container output stream ended with error", "container", x.id, "err", err)
	}
	for _, w := range []*forgivingWriter{stdout, stderr} {
		if w.err != nil {
			log.Warn("output writer failed; rest of stream discarded", "container", x.id, "err", w.err)
		}
	}
}

// closeWriter is a connection that can close its writing side, which the
// daemon passes on to the container as the end of its stdin.
type closeWriter interface {
	io.Writer
	CloseWrite() error
	SetWriteDeadline(t time.Time) error
}

// stdinTimeout bounds writing a container's stdin. The input is small
// and the container reads it at once, so a stall means something broke.
const stdinTimeout = 30 * time.Second

func writeStdin(w closeWriter, data []byte) error {
	if err := w.SetWriteDeadline(time.Now().Add(stdinTimeout)); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if err := w.CloseWrite(); err != nil {
		return err
	}
	return w.SetWriteDeadline(time.Time{})
}

// forgivingWriter turns the first write error into discarding, so a broken
// consumer never stops the stream being drained and never blocks the task.
type forgivingWriter struct {
	w   io.Writer
	err error
}

func (f *forgivingWriter) Write(p []byte) (int, error) {
	if f.w == nil || f.err != nil {
		return len(p), nil
	}
	if _, err := f.w.Write(p); err != nil {
		f.err = err
	}
	return len(p), nil
}
