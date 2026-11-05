// Package runner executes claimed runs in containers (tech design 4.6, 7).
//
// A Runner registers with the server, cleans up what an earlier instance with
// the same ID left behind on this host, and then claims runs while it has
// free slots. Each run gets a directory under DataDir, an optional dependency
// environment and one container. The container's output and the events the
// task writes through sbx are streamed to the server while it runs, and the
// run is finished only after the container is confirmed stopped and every
// line and event was delivered.
//
// The runner talks to the server only through runproto.Coordinator, so the
// same code runs embedded in all-in-one and, later, as a remote runner.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/executor"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

// Executor is what the runner needs from a container executor: running
// containers, and making the runtime image available before that.
// *docker.Executor implements it.
type Executor interface {
	executor.Executor
	executor.ImageEnsurer
}

// Defaults for the optional Options fields.
const (
	DefaultClaimInterval  = time.Second
	DefaultInstallTimeout = 15 * time.Minute
	// DefaultReportTimeout bounds how long a finished run keeps retrying
	// to deliver its last logs, events, eligible artifacts, task state and
	// terminal status. After that the runner gives up and lease expiry marks
	// the run interrupted because delivery did not complete.
	DefaultReportTimeout = 10 * time.Minute
)

// Network policies, as in the manifest and the runner configuration.
const (
	networkNone = string(executor.NetworkNone)
	networkFull = string(executor.NetworkFull)
)

// Options configures a Runner.
type Options struct {
	// ID is stable across restarts of this runner; containers are labelled
	// with it so a restarted runner finds what it left behind. It follows
	// the executor's ID rule: letters, digits, '_', '.', '-'.
	ID string
	// Name is for people, for example the host name.
	Name string
	// Slots is how many runs execute at once.
	Slots int
	// Labels are offered to the server; the runner claims only runs whose
	// required labels it all has.
	Labels []string
	// Version is the release reported to the server.
	Version string
	// DataDir holds run directories, cached environments and the sbx SDK.
	// It must be an absolute path that the container runtime sees at the
	// same location (tech design 15.2). The runner owns everything under
	// runs/, envs/ and sdk/.
	DataDir string
	// PythonImage is the runtime image for Python tasks. It must contain
	// python3 and uv.
	PythonImage string
	// NodeImage is the runtime image for Node tasks. It must contain node,
	// npm, sh and awk. Empty makes Node runs fail on this runner.
	NodeImage string
	// ShellImage is the runtime image for Shell tasks. It must contain
	// bash, sh and awk. Empty makes Shell runs fail on this runner.
	ShellImage string
	// PyPIMirror, when set, is passed to uv as UV_INDEX_URL during
	// dependency installation.
	PyPIMirror string
	// NPMMirror, when set, is the npm registry used during dependency
	// installation.
	NPMMirror string
	// AllowedNetworks lists the network policies task containers may use,
	// "none" and/or "full". Empty allows both. Dependency installation
	// always uses "full", because it has to reach the package index.
	AllowedNetworks []string
	Executor        Executor
	Coordinator     runproto.Coordinator
	// Logger receives the runner's own diagnostics, never task output.
	// Nil means slog.Default().
	Logger *slog.Logger

	// Redact is applied to every log line before it is numbered and sent,
	// so it is the place where credential redaction plugs in (tech design
	// 12.6). Nil leaves lines unchanged.
	Redact func(string) string
	// StopGrace is the SIGTERM grace period for cancel and timeout. Zero
	// means executor.DefaultStopGrace.
	StopGrace time.Duration
	// InstallTimeout bounds dependency installation. Zero means
	// DefaultInstallTimeout.
	InstallTimeout time.Duration
	// InstallResources limits the installation container. Zero fields
	// take installation defaults sized for building a typical environment.
	InstallResources executor.Resources
	// ClaimInterval is how often the runner asks for runs while it has a
	// free slot. Zero means DefaultClaimInterval.
	ClaimInterval time.Duration
	// ReportTimeout bounds the delivery of a finished run's last logs,
	// events, eligible artifacts, task state and terminal status. Zero means
	// DefaultReportTimeout.
	ReportTimeout time.Duration
}

// Default installation limits. uv keeps its cache in /tmp, which is a
// tmpfs counted against the memory limit, so both are larger than a
// typical task needs.
var defaultInstallResources = executor.Resources{
	MilliCPUs:   2000,
	MemoryBytes: 2 << 30,
	PIDs:        512,
	TmpBytes:    1 << 30,
}

// Runner claims runs from a Coordinator and executes them.
type Runner struct {
	opts    Options
	log     *slog.Logger
	ex      Executor
	coord   runproto.Coordinator
	allowed map[string]bool

	stopGrace        time.Duration
	installTimeout   time.Duration
	installResources executor.Resources
	reportTimeout    time.Duration
	claimInterval    time.Duration

	// Protocol timing; tests shorten them.
	heartbeatInterval time.Duration
	logFlushInterval  time.Duration
	eventPollInterval time.Duration
	retryBase         time.Duration
	retryMax          time.Duration
	limits            logLimits
	now               func() time.Time

	mu     sync.Mutex
	active map[uuid.UUID]*activeRun
	// freed is signalled when a run ends, so a free slot is refilled
	// without waiting for the next claim tick.
	freed chan struct{}

	envLocks keyedLocks
	runtimes map[string]runtimeDef

	sdkMu sync.Mutex
	// sdkDirs maps a runtime language to its written SDK directory.
	sdkDirs map[string]string
}

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// New checks opts and returns a Runner. It does not touch the file system,
// the executor or the coordinator; Run does.
func New(opts Options) (*Runner, error) {
	var errs []error
	if !validID.MatchString(opts.ID) {
		errs = append(errs, fmt.Errorf("runner id %q must match %s", opts.ID, validID))
	}
	if opts.Slots < 1 {
		errs = append(errs, fmt.Errorf("slots must be at least 1, got %d", opts.Slots))
	}
	if !filepath.IsAbs(opts.DataDir) || filepath.Clean(opts.DataDir) != opts.DataDir {
		errs = append(errs, fmt.Errorf("data dir %q must be a clean absolute path", opts.DataDir))
	}
	if opts.PythonImage == "" {
		errs = append(errs, errors.New("python image is required"))
	}
	if opts.Executor == nil {
		errs = append(errs, errors.New("executor is required"))
	}
	if opts.Coordinator == nil {
		errs = append(errs, errors.New("coordinator is required"))
	}
	allowed := map[string]bool{}
	for _, n := range opts.AllowedNetworks {
		if n != networkNone && n != networkFull {
			errs = append(errs, fmt.Errorf("allowed network %q must be %q or %q", n, networkNone, networkFull))
		}
		allowed[n] = true
	}
	if len(opts.AllowedNetworks) == 0 {
		allowed[networkNone], allowed[networkFull] = true, true
	}
	if opts.StopGrace < 0 || opts.InstallTimeout < 0 || opts.ClaimInterval < 0 || opts.ReportTimeout < 0 {
		errs = append(errs, errors.New("durations must not be negative"))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("runner options: %w", err)
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	r := &Runner{
		opts:              opts,
		log:               log.With("runner_id", opts.ID),
		ex:                opts.Executor,
		coord:             opts.Coordinator,
		allowed:           allowed,
		stopGrace:         orDefault(opts.StopGrace, executor.DefaultStopGrace),
		installTimeout:    orDefault(opts.InstallTimeout, DefaultInstallTimeout),
		installResources:  installResources(opts.InstallResources),
		reportTimeout:     orDefault(opts.ReportTimeout, DefaultReportTimeout),
		claimInterval:     orDefault(opts.ClaimInterval, DefaultClaimInterval),
		heartbeatInterval: runproto.HeartbeatInterval,
		logFlushInterval:  500 * time.Millisecond,
		eventPollInterval: 250 * time.Millisecond,
		retryBase:         250 * time.Millisecond,
		retryMax:          30 * time.Second,
		limits:            defaultLogLimits(),
		now:               time.Now,
		active:            map[uuid.UUID]*activeRun{},
		freed:             make(chan struct{}, 1),
		sdkDirs:           map[string]string{},
	}
	r.runtimes = r.newRuntimes()
	return r, nil
}

func orDefault(d, def time.Duration) time.Duration {
	if d == 0 {
		return def
	}
	return d
}

func installResources(r executor.Resources) executor.Resources {
	def := func(v *int64, d int64) {
		if *v == 0 {
			*v = d
		}
	}
	def(&r.MilliCPUs, defaultInstallResources.MilliCPUs)
	def(&r.MemoryBytes, defaultInstallResources.MemoryBytes)
	def(&r.PIDs, defaultInstallResources.PIDs)
	def(&r.TmpBytes, defaultInstallResources.TmpBytes)
	return r
}

// Run registers the runner, cleans up containers an earlier instance left
// behind, and then claims and executes runs until ctx ends.
//
// When ctx ends the runner stops claiming and returns once every run
// goroutine has let go. Containers that are still running are left as they
// are, without reporting anything: the next start with the same ID stops
// them and marks their runs interrupted (tech design 7.6). Run returns nil
// on such a shutdown, and an error only when it cannot start at all.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.ensureLayout(); err != nil {
		return err
	}
	reg := runproto.Registration{ID: r.opts.ID, Name: r.opts.Name, Slots: r.opts.Slots, Labels: r.opts.Labels, Version: r.opts.Version}
	if ok, err := r.retryUntilDone(ctx, "register", func() error { return r.coord.Register(ctx, reg) }); !ok {
		return err
	}
	if ok, err := r.retryUntilDone(ctx, "reconcile", func() error { return r.reconcile(ctx) }); !ok {
		return err
	}
	r.log.Info("runner started", "slots", r.opts.Slots, "data_dir", r.opts.DataDir)

	var runs sync.WaitGroup
	heartbeats := make(chan struct{})
	go func() {
		defer close(heartbeats)
		r.heartbeatLoop(ctx)
	}()

	tick := time.NewTicker(r.claimInterval)
	defer tick.Stop()
	for {
		r.claim(ctx, &runs)
		select {
		case <-ctx.Done():
			runs.Wait()
			<-heartbeats
			r.log.Info("runner stopped")
			return nil
		case <-tick.C:
		case <-r.freed:
		}
	}
}

// retryUntilDone calls fn until it succeeds, backing off between failures.
// It reports false if ctx ended first, with a nil error, or if the server
// rejected the step for good, with that error.
func (r *Runner) retryUntilDone(ctx context.Context, what string, fn func() error) (bool, error) {
	delay := r.retryBase
	for {
		err := fn()
		if err == nil {
			return true, nil
		}
		if ctx.Err() != nil {
			return false, nil
		}
		if errors.Is(err, runproto.ErrRejected) {
			return false, fmt.Errorf("runner %s: %w", what, err)
		}
		r.log.Warn("runner startup step failed; retrying", "step", what, "err", err, "retry_in", delay)
		if !sleep(ctx, delay) {
			return false, nil
		}
		delay = min(delay*2, r.retryMax)
	}
}

func (r *Runner) claim(ctx context.Context, runs *sync.WaitGroup) {
	free := r.opts.Slots - r.activeCount()
	if free <= 0 || ctx.Err() != nil {
		return
	}
	claimed, err := r.coord.Claim(ctx, r.opts.ID, free)
	if err != nil {
		if ctx.Err() == nil {
			r.log.Warn("claim runs", "err", err)
		}
		return
	}
	for _, a := range claimed {
		run := r.track(a)
		if run == nil {
			r.log.Error("claimed a run that is already active; ignoring the duplicate", "run_id", a.RunID)
			continue
		}
		runs.Add(1)
		go func() {
			defer runs.Done()
			defer r.untrack(run)
			r.execute(ctx, run)
		}()
	}
}

func (r *Runner) heartbeatLoop(ctx context.Context) {
	tick := time.NewTicker(r.heartbeatInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		r.heartbeat(ctx)
	}
}

// heartbeat renews the leases of all active runs, including those still
// preparing, and passes on the server's stop requests.
func (r *Runner) heartbeat(ctx context.Context) {
	ids := r.activeIDs()
	reply, err := r.coord.Heartbeat(ctx, r.opts.ID, ids)
	if err != nil {
		if ctx.Err() == nil {
			r.log.Warn("heartbeat", "err", err, "active", len(ids))
		}
		return
	}
	for _, id := range reply.Cancel {
		if run := r.lookup(id); run != nil {
			r.log.Info("cancel requested", "run_id", id)
			run.requestCancel()
		}
	}
	for _, id := range reply.Lost {
		if run := r.lookup(id); run != nil {
			r.log.Warn("run no longer assigned to this runner; stopping it", "run_id", id)
			run.markLost()
		}
	}
}

// activeRun is a claimed run and the signals that stop it.
type activeRun struct {
	a runproto.Assignment

	once sync.Once
	// stop is closed when the run must stop: its cancellation was
	// requested, or it is no longer assigned to this runner.
	stop      chan struct{}
	mu        sync.Mutex
	cancelled bool
	lost      bool
}

func (a *activeRun) requestCancel() {
	a.mu.Lock()
	a.cancelled = true
	a.mu.Unlock()
	a.once.Do(func() { close(a.stop) })
}

// markLost records that the server no longer considers the run ours. From
// then on nothing more about it is reported.
func (a *activeRun) markLost() {
	a.mu.Lock()
	a.lost = true
	a.mu.Unlock()
	a.once.Do(func() { close(a.stop) })
}

func (a *activeRun) isLost() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lost
}

func (a *activeRun) stopRequested() bool {
	select {
	case <-a.stop:
		return true
	default:
		return false
	}
}

func (r *Runner) track(a runproto.Assignment) *activeRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.active[a.RunID]; ok {
		return nil
	}
	run := &activeRun{a: a, stop: make(chan struct{})}
	r.active[a.RunID] = run
	return run
}

func (r *Runner) untrack(run *activeRun) {
	r.mu.Lock()
	if r.active[run.a.RunID] == run {
		delete(r.active, run.a.RunID)
	}
	r.mu.Unlock()
	select {
	case r.freed <- struct{}{}:
	default:
	}
}

func (r *Runner) lookup(id uuid.UUID) *activeRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active[id]
}

func (r *Runner) activeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.active)
}

func (r *Runner) activeIDs() []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]uuid.UUID, 0, len(r.active))
	for id := range r.active {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	return ids
}

// sleep waits for d or until ctx ends, and reports whether d elapsed.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// keyedLocks serialises work per key, with waits that a context can end.
type keyedLocks struct {
	mu sync.Mutex
	m  map[string]*keyedLock
}

type keyedLock struct {
	ch   chan struct{}
	refs int
}

func (k *keyedLocks) lock(ctx context.Context, key string) (unlock func(), err error) {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*keyedLock{}
	}
	l := k.m[key]
	if l == nil {
		l = &keyedLock{ch: make(chan struct{}, 1)}
		k.m[key] = l
	}
	l.refs++
	k.mu.Unlock()

	select {
	case l.ch <- struct{}{}:
		return func() {
			<-l.ch
			k.release(key, l)
		}, nil
	case <-ctx.Done():
		k.release(key, l)
		return nil, ctx.Err()
	}
}

func (k *keyedLocks) release(key string, l *keyedLock) {
	k.mu.Lock()
	defer k.mu.Unlock()
	l.refs--
	if l.refs == 0 {
		delete(k.m, key)
	}
}
