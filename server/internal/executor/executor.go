// Package executor defines how a runner starts, watches and stops the
// container of one run. The Docker implementation lives in the docker
// subpackage; a Kubernetes implementation may follow in P1.
//
// The executor knows nothing about the runner protocol, run states or log
// redaction. It reports facts (exit code, OOM kill, whether SIGKILL was
// needed) and the runner maps them to run states and error categories.
package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Container labels. Every container an executor creates carries all three,
// so a restarted runner can find and stop what it left behind (tech design
// 7.6) without touching containers it does not own.
const (
	LabelManaged  = "lerryn.managed"
	LabelRunID    = "lerryn.run_id"
	LabelRunnerID = "lerryn.runner_id"
)

// Paths inside every task container that the executor mounts as tmpfs.
// Spec mounts may not target them.
const (
	TmpDir     = "/tmp"
	SecretsDir = "/run/secrets"
)

// DefaultUser is the uid:gid task processes run as when Spec.User is empty.
// 65532 is the conventional "nonroot" id used by distroless images and by
// the lerryn image itself.
const DefaultUser = "65532:65532"

// DefaultStopGrace is how long Stop waits after SIGTERM before SIGKILL.
const DefaultStopGrace = 10 * time.Second

// Default resource limits. A zero field in Resources means "use the
// default", never "unlimited": a task container always runs with limits.
const (
	DefaultMilliCPUs    = 1000
	DefaultMemoryBytes  = 1 << 30
	DefaultPIDs         = 256
	DefaultOpenFiles    = 1024
	DefaultTmpBytes     = 64 << 20
	DefaultSecretsBytes = 1 << 20
)

// Network is the network policy of a run (tech design 7.5).
type Network string

// Network policies.
const (
	// NetworkNone gives the container a loopback interface only.
	NetworkNone Network = "none"
	// NetworkFull attaches the container to the dedicated task network,
	// which has outbound access.
	NetworkFull Network = "full"
)

// Errors returned by executors. Implementations wrap them with context.
var (
	// ErrInvalidSpec means the spec failed validation; nothing was created.
	ErrInvalidSpec = errors.New("invalid run spec")
	// ErrNotFound means the container does not exist (any more).
	ErrNotFound = errors.New("container not found")
	// ErrNotStopped means the executor could not confirm that the container
	// stopped. Callers must not report the run as cancelled or timed out.
	ErrNotStopped = errors.New("container not confirmed stopped")
	// ErrHardening means the container runtime would not apply part of the
	// default hardening, for example a resource limit it cannot enforce.
	ErrHardening = errors.New("container hardening not applied")
)

// Executor runs one container per run.
type Executor interface {
	// Start creates the container described by spec, attaches out to its
	// stdout and stderr, and starts it. Output produced before Start
	// returns is not lost.
	Start(ctx context.Context, spec Spec, out Output) (Execution, error)

	// Stop sends SIGTERM to the container, waits up to grace for it to exit
	// and then sends SIGKILL. It returns only after the runtime reports the
	// container is no longer running; otherwise it returns an error wrapping
	// ErrNotStopped. Stopping a container that already exited is not an
	// error. Stop does not remove the container.
	Stop(ctx context.Context, id string, grace time.Duration) (Stopped, error)

	// Remove deletes a stopped container and confirms it is gone. Removing a
	// container that does not exist is not an error.
	Remove(ctx context.Context, id string) error

	// List returns the containers this executor created whose labels match
	// every entry of labels, running or not.
	List(ctx context.Context, labels map[string]string) ([]Container, error)
}

// ImageEnsurer is implemented by executors that can make an image available
// on the container host. Start never pulls, so a runner calls EnsureImage in
// the preparing phase, where a slow or failed pull is reported as part of
// preparation rather than as a failed start.
type ImageEnsurer interface {
	// EnsureImage pulls ref unless the host already has it and returns the
	// image's ID. The ID identifies the exact image content, so callers
	// can key caches on it and start containers from it even if ref is
	// later moved to another image.
	EnsureImage(ctx context.Context, ref string) (imageID string, err error)
}

// Execution is a started container.
type Execution interface {
	// ID is the runtime's container ID.
	ID() string

	// Wait blocks until the container exited and all of its output was
	// written to the Output, then returns the exit status. It returns early
	// with ctx's error if ctx ends first; the container keeps running.
	// Wait may be called any number of times.
	Wait(ctx context.Context) (ExitStatus, error)
}

// Output receives the container's output as it is produced. Writes happen
// from one goroutine, in the order the runtime delivered them, so stdout
// and stderr stay interleaved correctly when both point at one sink. Nil
// writers discard. Writers should not block for long: a stalled writer
// eventually stalls the task. If a writer returns an error, the rest of
// that stream is discarded so the task never blocks on a dead pipeline.
type Output struct {
	Stdout io.Writer
	Stderr io.Writer
}

// ExitStatus describes how a container's main process ended.
type ExitStatus struct {
	// Code is the exit code of the container's main process. 128+n means
	// it was killed by signal n, for example 137 for SIGKILL.
	Code int
	// OOMKilled reports that the kernel OOM killer killed at least one
	// process in the container. It can be true with Code 0 when only a
	// child process was killed and the main process carried on.
	OOMKilled  bool
	StartedAt  time.Time
	FinishedAt time.Time
}

// Stopped is the result of a confirmed Stop.
type Stopped struct {
	Exit ExitStatus
	// AlreadyExited is true when the container had exited before Stop sent
	// any signal, so the run ended on its own.
	AlreadyExited bool
	// Escalated is true when the container did not exit within the grace
	// period after SIGTERM and was killed with SIGKILL.
	Escalated bool
}

// Container is a container found by List.
type Container struct {
	ID       string
	Name     string
	RunID    string
	RunnerID string
	// Running is false for containers that exited but were not removed.
	Running bool
	Labels  map[string]string
}

// Spec describes the container of one run. It is plain data so that the
// mapping to a runtime configuration can be tested without a runtime.
type Spec struct {
	RunID    string
	RunnerID string

	// Image should be pinned by digest or ID in production. Start does not
	// pull images; see ImageEnsurer.
	Image string
	// Command is the argv to execute. It replaces the image's ENTRYPOINT
	// and CMD, so nothing the image declares runs implicitly.
	Command    []string
	Env        []string
	WorkingDir string
	// User is a numeric "uid:gid". Empty means DefaultUser. Root (uid 0 or
	// gid 0) is rejected.
	User string

	// Mounts are the only host directories visible in the container.
	Mounts  []Mount
	Network Network
	// Resources are the hard limits. Zero fields take the defaults.
	Resources Resources
	// StopGrace is recorded on the container so that a daemon-initiated
	// stop (for example a daemon shutdown) gives the task the same grace
	// period as Stop. Zero means DefaultStopGrace.
	StopGrace time.Duration
	// Stdin, when not nil, is written to the container's standard input
	// right after it starts, and the input is then closed, so the main
	// process reads it to the end. It carries a run's credentials to the
	// sbx entrypoint (tech design 12.5): unlike environment variables it
	// is not part of the container's configuration, so inspecting the
	// container does not show it. Nil leaves standard input empty. At
	// most MaxStdinBytes.
	Stdin []byte
}

// MaxStdinBytes bounds Spec.Stdin. It matches the credential tmpfs.
const MaxStdinBytes = DefaultSecretsBytes

// Mount bind-mounts a host directory into the container.
type Mount struct {
	// Source is an absolute path as seen by the container runtime's host.
	// When the runner itself runs in a container, the runner's path must be
	// identical to the host path (tech design 15.2).
	Source   string
	Target   string
	ReadOnly bool
}

// Resources are hard limits for one container.
type Resources struct {
	// MilliCPUs is the CPU quota in thousandths of a CPU; 1000 is one CPU.
	MilliCPUs int64
	// MemoryBytes is the memory limit. Swap is disabled.
	MemoryBytes int64
	// PIDs caps the number of processes and threads in the container.
	PIDs int64
	// OpenFiles is the RLIMIT_NOFILE soft and hard limit.
	OpenFiles int64
	// TmpBytes is the size of the /tmp tmpfs; it counts towards memory.
	TmpBytes int64
	// SecretsBytes is the size of the credential tmpfs.
	SecretsBytes int64
}

// WithDefaults returns r with zero fields replaced by the defaults.
func (r Resources) WithDefaults() Resources {
	def := func(v *int64, d int64) {
		if *v == 0 {
			*v = d
		}
	}
	def(&r.MilliCPUs, DefaultMilliCPUs)
	def(&r.MemoryBytes, DefaultMemoryBytes)
	def(&r.PIDs, DefaultPIDs)
	def(&r.OpenFiles, DefaultOpenFiles)
	def(&r.TmpBytes, DefaultTmpBytes)
	def(&r.SecretsBytes, DefaultSecretsBytes)
	return r
}

// Lower bounds below which a task cannot even start a shell. They catch
// unit mistakes such as a memory limit given in MiB instead of bytes.
const (
	minMilliCPUs   = 10
	minMemoryBytes = 6 << 20
	minPIDs        = 8
	minOpenFiles   = 16
	minTmpfsBytes  = 4 << 10
)

// Validate reports every problem with the spec in one error that wraps
// ErrInvalidSpec. It checks the rules that keep a task inside its sandbox;
// whether the image or mount sources exist is left to the runtime.
func (s Spec) Validate() error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if !validID.MatchString(s.RunID) {
		add("run id %q must match %s", s.RunID, validID)
	}
	if !validID.MatchString(s.RunnerID) {
		add("runner id %q must match %s", s.RunnerID, validID)
	}
	if strings.TrimSpace(s.Image) == "" {
		add("image is required")
	}
	if len(s.Command) == 0 || s.Command[0] == "" {
		add("command is required")
	}
	for _, kv := range s.Env {
		if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
			add("env entry %q must be KEY=VALUE", kv)
		}
	}
	if s.WorkingDir != "" && !isCleanAbs(s.WorkingDir) {
		add("working dir %q must be a clean absolute path", s.WorkingDir)
	}
	if s.User != "" {
		if err := checkUser(s.User); err != nil {
			errs = append(errs, err)
		}
	}
	switch s.Network {
	case NetworkNone, NetworkFull:
	default:
		add("network %q must be %q or %q", s.Network, NetworkNone, NetworkFull)
	}
	errs = append(errs, s.Resources.validate()...)
	errs = append(errs, validateMounts(s.Mounts)...)
	if s.StopGrace < 0 {
		add("stop grace must not be negative")
	}
	if len(s.Stdin) > MaxStdinBytes {
		add("stdin is %d bytes, more than %d", len(s.Stdin), MaxStdinBytes)
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrInvalidSpec, errors.Join(errs...))
}

// validID keeps IDs safe to embed in container names and labels.
var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

var numericUser = regexp.MustCompile(`^([0-9]+)(?::([0-9]+))?$`)

func checkUser(u string) error {
	m := numericUser.FindStringSubmatch(u)
	if m == nil {
		// Names would be resolved from the image's /etc/passwd, which the
		// task image controls; numeric ids leave nothing to resolve.
		return fmt.Errorf("user %q must be numeric uid:gid", u)
	}
	uid, err := strconv.ParseUint(m[1], 10, 32)
	if err != nil {
		return fmt.Errorf("user %q: uid: %w", u, err)
	}
	if uid == 0 {
		return fmt.Errorf("user %q: tasks must not run as root", u)
	}
	if m[2] == "" {
		// Without a gid Docker uses the image's primary group for the uid,
		// which may be root.
		return fmt.Errorf("user %q must include a gid", u)
	}
	gid, err := strconv.ParseUint(m[2], 10, 32)
	if err != nil {
		return fmt.Errorf("user %q: gid: %w", u, err)
	}
	if gid == 0 {
		return fmt.Errorf("user %q: tasks must not run in the root group", u)
	}
	return nil
}

func (r Resources) validate() []error {
	var errs []error
	check := func(name string, v, min int64) {
		if v != 0 && v < min {
			errs = append(errs, fmt.Errorf("%s %d is below the minimum %d", name, v, min))
		}
	}
	check("cpu (milli)", r.MilliCPUs, minMilliCPUs)
	check("memory bytes", r.MemoryBytes, minMemoryBytes)
	check("pids", r.PIDs, minPIDs)
	check("open files", r.OpenFiles, minOpenFiles)
	check("tmp bytes", r.TmpBytes, minTmpfsBytes)
	check("secrets bytes", r.SecretsBytes, minTmpfsBytes)
	return errs
}

// reservedTargets may not be mount targets: the executor owns /tmp and the
// credential tmpfs, and the kernel filesystems must keep the runtime's
// masking.
var reservedTargets = []string{"/", "/proc", "/sys", "/dev", TmpDir, SecretsDir}

// socketNames are container runtime API sockets. Mounting one into a task
// hands it the runner host (tech design 7.1).
var socketNames = []string{"docker.sock", "podman.sock", "containerd.sock", "crio.sock"}

func validateMounts(mounts []Mount) []error {
	var errs []error
	seen := make(map[string]bool, len(mounts))
	for _, m := range mounts {
		if !isCleanAbs(m.Source) {
			errs = append(errs, fmt.Errorf("mount source %q must be a clean absolute path", m.Source))
		} else if m.Source == "/" {
			errs = append(errs, errors.New("mount source must not be /"))
		} else if isSocketPath(m.Source) {
			errs = append(errs, fmt.Errorf("mount source %q is a container runtime socket", m.Source))
		}
		if !isCleanAbs(m.Target) {
			errs = append(errs, fmt.Errorf("mount target %q must be a clean absolute path", m.Target))
			continue
		}
		for _, r := range reservedTargets {
			if m.Target == r || (r != "/" && within(m.Target, r)) || within(r, m.Target) {
				errs = append(errs, fmt.Errorf("mount target %q overlaps reserved path %s", m.Target, r))
				break
			}
		}
		if seen[m.Target] {
			errs = append(errs, fmt.Errorf("mount target %q is used twice", m.Target))
		}
		seen[m.Target] = true
	}
	return errs
}

func isSocketPath(p string) bool {
	base := path.Base(p)
	for _, s := range socketNames {
		if base == s {
			return true
		}
	}
	// The runtime socket usually lives directly in /run or /var/run, so
	// mounting either directory exposes it.
	return p == "/run" || p == "/var/run"
}

func isCleanAbs(p string) bool {
	return p != "" && path.IsAbs(p) && path.Clean(p) == p
}

// within reports whether p is strictly inside dir.
func within(p, dir string) bool {
	if dir == "/" {
		return p != "/"
	}
	return strings.HasPrefix(p, dir+"/")
}
