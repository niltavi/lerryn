package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/executor"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

// callLog records calls across the fakes in order, so tests can check what
// happened before what.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, fmt.Sprintf(format, args...))
}

func (l *callLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

func (l *callLog) index(prefix string) int {
	return slices.IndexFunc(l.snapshot(), func(c string) bool { return strings.HasPrefix(c, prefix) })
}

// fakeCoordinator is an in-memory server side of the runner protocol. It
// enforces the protocol rules a real server would: consecutive sequence
// numbers, and nothing reported after Finish or for runs it does not
// assign to the caller.
type fakeCoordinator struct {
	log *callLog

	mu           sync.Mutex
	registered   []runproto.Registration
	queue        []runproto.Assignment
	assigned     map[uuid.UUID]bool
	files        map[uuid.UUID][]runproto.File
	claims       int
	heartbeats   int
	cancel       map[uuid.UUID]bool
	lost         map[uuid.UUID]bool
	started      map[uuid.UUID]string
	logs         map[uuid.UUID][]runproto.LogLine
	dropped      map[uuid.UUID]runproto.Dropped
	events       map[uuid.UUID][]runproto.Event
	finishes     map[uuid.UUID]runproto.Finish
	finishErr    map[uuid.UUID]error
	violations   []string
	logsAtEnd    map[uuid.UUID]int
	eventsAtEnd  map[uuid.UUID]int
	secrets      map[uuid.UUID][]runproto.Secret
	secretsErr   map[uuid.UUID]error
	inputs       map[uuid.UUID]runproto.Input
	artifacts    map[uuid.UUID]map[string]string
	stateInputs  map[uuid.UUID]runproto.StateSnapshot
	stateOutputs map[uuid.UUID]runproto.StateSnapshot
	hasState     map[uuid.UUID]bool

	// appendLogsErr, when set, decides the error for each AppendLogs call
	// before it is applied.
	appendLogsErr func(runID uuid.UUID, call int) error
	appendLogsN   int
	finished      chan uuid.UUID
}

func newFakeCoordinator(log *callLog) *fakeCoordinator {
	return &fakeCoordinator{
		log:          log,
		assigned:     map[uuid.UUID]bool{},
		files:        map[uuid.UUID][]runproto.File{},
		cancel:       map[uuid.UUID]bool{},
		lost:         map[uuid.UUID]bool{},
		started:      map[uuid.UUID]string{},
		logs:         map[uuid.UUID][]runproto.LogLine{},
		dropped:      map[uuid.UUID]runproto.Dropped{},
		events:       map[uuid.UUID][]runproto.Event{},
		finishes:     map[uuid.UUID]runproto.Finish{},
		finishErr:    map[uuid.UUID]error{},
		logsAtEnd:    map[uuid.UUID]int{},
		eventsAtEnd:  map[uuid.UUID]int{},
		secrets:      map[uuid.UUID][]runproto.Secret{},
		secretsErr:   map[uuid.UUID]error{},
		inputs:       map[uuid.UUID]runproto.Input{},
		artifacts:    map[uuid.UUID]map[string]string{},
		stateInputs:  map[uuid.UUID]runproto.StateSnapshot{},
		stateOutputs: map[uuid.UUID]runproto.StateSnapshot{},
		hasState:     map[uuid.UUID]bool{},
		finished:     make(chan uuid.UUID, 100),
	}
}

func (c *fakeCoordinator) enqueue(a runproto.Assignment, files []runproto.File) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queue = append(c.queue, a)
	c.files[a.VersionID] = files
}

func (c *fakeCoordinator) violate(format string, args ...any) {
	c.violations = append(c.violations, fmt.Sprintf(format, args...))
}

func (c *fakeCoordinator) Register(_ context.Context, r runproto.Registration) error {
	c.log.add("register")
	c.mu.Lock()
	defer c.mu.Unlock()
	c.registered = append(c.registered, r)
	return nil
}

func (c *fakeCoordinator) Claim(_ context.Context, _ string, n int) ([]runproto.Assignment, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.claims++
	if c.claims == 1 {
		c.log.add("claim")
	}
	n = min(n, len(c.queue))
	out := slices.Clone(c.queue[:n])
	c.queue = c.queue[n:]
	for _, a := range out {
		c.assigned[a.RunID] = true
		c.hasState[a.RunID] = a.HasState
	}
	return out, nil
}

func (c *fakeCoordinator) Heartbeat(_ context.Context, _ string, active []uuid.UUID) (runproto.HeartbeatReply, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.heartbeats++
	var reply runproto.HeartbeatReply
	for _, id := range active {
		if c.cancel[id] {
			reply.Cancel = append(reply.Cancel, id)
		}
		if c.lost[id] {
			reply.Lost = append(reply.Lost, id)
		}
	}
	return reply, nil
}

func (c *fakeCoordinator) Files(_ context.Context, versionID uuid.UUID) ([]runproto.File, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	files, ok := c.files[versionID]
	if !ok {
		return nil, errors.New("no such version")
	}
	return files, nil
}

// Secrets hands out the secrets set for the run, or secretsErr.
func (c *fakeCoordinator) Secrets(_ context.Context, _ string, runID uuid.UUID) ([]runproto.Secret, error) {
	c.log.add("secrets %s", runID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkAssigned("Secrets", runID); err != nil {
		return nil, err
	}
	if err := c.secretsErr[runID]; err != nil {
		return nil, err
	}
	return c.secrets[runID], nil
}

// Input hands out the input set for the run.
func (c *fakeCoordinator) Input(_ context.Context, _ string, runID uuid.UUID) (runproto.Input, error) {
	c.log.add("input %s", runID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkAssigned("Input", runID); err != nil {
		return runproto.Input{}, err
	}
	in, ok := c.inputs[runID]
	if !ok {
		return runproto.Input{}, fmt.Errorf("%w: run %s has no input", runproto.ErrRejected, runID)
	}
	return in, nil
}

func (c *fakeCoordinator) GetState(_ context.Context, _ string, runID uuid.UUID) (runproto.StateSnapshot, error) {
	c.log.add("state get %s", runID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkAssigned("GetState", runID); err != nil {
		return runproto.StateSnapshot{}, err
	}
	if !c.hasState[runID] {
		return runproto.StateSnapshot{}, runproto.ErrRejected
	}
	if state, ok := c.stateInputs[runID]; ok {
		return cloneFakeState(state), nil
	}
	return runproto.StateSnapshot{Version: runproto.StateFormatVersion, Entries: []runproto.StateEntry{}}, nil
}

func (c *fakeCoordinator) PutState(_ context.Context, _ string, runID uuid.UUID, state runproto.StateSnapshot) error {
	c.log.add("state put %s", runID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkAssigned("PutState", runID); err != nil {
		return err
	}
	if !c.hasState[runID] {
		return runproto.ErrRejected
	}
	if _, _, err := runproto.EncodeState(state); err != nil {
		return fmt.Errorf("%w: %w", runproto.ErrRejected, err)
	}
	c.stateOutputs[runID] = cloneFakeState(state)
	return nil
}

func cloneFakeState(state runproto.StateSnapshot) runproto.StateSnapshot {
	state.Entries = slices.Clone(state.Entries)
	for i := range state.Entries {
		state.Entries[i].Content = slices.Clone(state.Entries[i].Content)
	}
	return state
}

// checkAssigned reports ErrLeaseLost the way a server would for runs that
// are not, or no longer, the caller's.
func (c *fakeCoordinator) checkAssigned(what string, runID uuid.UUID) error {
	if _, done := c.finishes[runID]; done {
		c.violate("%s for run %s after Finish", what, runID)
		return runproto.ErrLeaseLost
	}
	if c.lost[runID] || !c.assigned[runID] {
		return runproto.ErrLeaseLost
	}
	return nil
}

func (c *fakeCoordinator) Started(_ context.Context, _ string, runID uuid.UUID, envID string, _ time.Time) error {
	c.log.add("started %s", runID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkAssigned("Started", runID); err != nil {
		return err
	}
	c.started[runID] = envID
	return nil
}

func (c *fakeCoordinator) AppendLogs(_ context.Context, _ string, runID uuid.UUID, lines []runproto.LogLine, dropped runproto.Dropped) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.appendLogsN++
	if c.appendLogsErr != nil {
		if err := c.appendLogsErr(runID, c.appendLogsN); err != nil {
			return err
		}
	}
	if err := c.checkAssigned("AppendLogs", runID); err != nil {
		return err
	}
	stored := c.logs[runID]
	next := int64(len(stored)) + 1
	for _, l := range lines {
		switch {
		case l.Seq < next:
			// Resent after an error; already stored.
		case l.Seq == next:
			stored = append(stored, l)
			next++
		default:
			c.violate("run %s: log seq %d after %d", runID, l.Seq, next-1)
		}
		if len(l.Text) > runproto.MaxLineBytes {
			c.violate("run %s: log line %d is %d bytes", runID, l.Seq, len(l.Text))
		}
	}
	c.logs[runID] = stored
	c.dropped[runID] = dropped
	return nil
}

func (c *fakeCoordinator) AppendEvents(_ context.Context, _ string, runID uuid.UUID, events []runproto.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkAssigned("AppendEvents", runID); err != nil {
		return err
	}
	stored := c.events[runID]
	next := int64(len(stored)) + 1
	for _, e := range events {
		switch {
		case e.Seq < next:
		case e.Seq == next:
			stored = append(stored, e)
			next++
		default:
			c.violate("run %s: event seq %d after %d", runID, e.Seq, next-1)
		}
	}
	c.events[runID] = stored
	return nil
}

func (c *fakeCoordinator) AddArtifact(_ context.Context, _ string, runID uuid.UUID, a runproto.Artifact) error {
	c.log.add("artifact %s %s", runID, a.Path)
	content, err := io.ReadAll(a.Content)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkAssigned("AddArtifact", runID); err != nil {
		return err
	}
	if int64(len(content)) != a.Size {
		c.violate("artifact %s of run %s: size %d, sent %d bytes", a.Path, runID, a.Size, len(content))
	}
	if c.artifacts[runID] == nil {
		c.artifacts[runID] = map[string]string{}
	}
	c.artifacts[runID][a.Path] = string(content)
	return nil
}

func (c *fakeCoordinator) runArtifacts(id uuid.UUID) map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.artifacts[id]
}

func (c *fakeCoordinator) Finish(_ context.Context, _ string, runID uuid.UUID, f runproto.Finish) error {
	c.log.add("finish %s %s", runID, f.Status)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.finishErr[runID]; err != nil {
		return err
	}
	if f.Status != runproto.StatusInterrupted {
		if err := c.checkAssigned("Finish", runID); err != nil {
			return err
		}
	}
	if c.hasState[runID] && f.Status == runproto.StatusCompleted {
		if _, ready := c.stateOutputs[runID]; !ready {
			c.violate("Finish before state delivery for run %s", runID)
			return runproto.ErrRejected
		}
	}
	c.finishes[runID] = f
	c.logsAtEnd[runID] = len(c.logs[runID])
	c.eventsAtEnd[runID] = len(c.events[runID])
	c.finished <- runID
	return nil
}

func (c *fakeCoordinator) requestCancel(id uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancel[id] = true
}

func (c *fakeCoordinator) markLost(id uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lost[id] = true
}

func (c *fakeCoordinator) waitFinish(t *testing.T, id uuid.UUID, timeout time.Duration) runproto.Finish {
	t.Helper()
	deadline := time.After(timeout)
	for {
		c.mu.Lock()
		f, ok := c.finishes[id]
		c.mu.Unlock()
		if ok {
			return f
		}
		select {
		case <-c.finished:
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatalf("run %s did not finish within %v", id, timeout)
		}
	}
}

func (c *fakeCoordinator) waitStarted(t *testing.T, id uuid.UUID, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, "run started", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, ok := c.started[id]
		return ok
	})
}

func (c *fakeCoordinator) logTexts(id uuid.UUID) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, l := range c.logs[id] {
		out = append(out, l.Text)
	}
	return out
}

func (c *fakeCoordinator) logLines(id uuid.UUID) []runproto.LogLine {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.logs[id])
}

func (c *fakeCoordinator) runEvents(id uuid.UUID) []runproto.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.events[id])
}

func (c *fakeCoordinator) finish(id uuid.UUID) (runproto.Finish, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.finishes[id]
	return f, ok
}

// checkProtocol fails the test if the runner broke a protocol rule, and
// checks that each finished run had delivered everything before Finish.
func (c *fakeCoordinator) checkProtocol(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, v := range c.violations {
		t.Errorf("protocol violation: %s", v)
	}
	for id := range c.finishes {
		if c.logsAtEnd[id] != len(c.logs[id]) || c.eventsAtEnd[id] != len(c.events[id]) {
			t.Errorf("run %s: logs or events arrived after Finish", id)
		}
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// fakeExecutor runs fake containers whose behaviour the test scripts.
type fakeExecutor struct {
	log *callLog

	mu sync.Mutex
	// behave runs in its own goroutine for every started container. It
	// must make the container exit, on its own or when stopped.
	behave     func(c *fakeContainer)
	containers map[string]*fakeContainer
	specs      []executor.Spec
	stopped    []string
	removed    []string
	leftovers  []executor.Container
	listErrs   []error
	imageID    string
	// ensured lists the references EnsureImage was called with.
	ensured []string
	// blockImage makes EnsureImage wait until its context ends.
	blockImage bool
	seq        int
}

func newFakeExecutor(log *callLog) *fakeExecutor {
	return &fakeExecutor{log: log, containers: map[string]*fakeContainer{}, imageID: "sha256:fakeimage"}
}

type fakeContainer struct {
	ex   *fakeExecutor
	id   string
	spec executor.Spec
	out  executor.Output
	// stdin is a copy of spec.Stdin, which the runner wipes after Start.
	stdin []byte

	stopOnce sync.Once
	stopCh   chan struct{}
	exitOnce sync.Once
	done     chan struct{}
	status   executor.ExitStatus
	waitErr  error
}

func (e *fakeExecutor) Start(_ context.Context, spec executor.Spec, out executor.Output) (executor.Execution, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.seq++
	c := &fakeContainer{ex: e, id: fmt.Sprintf("c%d", e.seq), spec: spec, out: out, stdin: slices.Clone(spec.Stdin), stopCh: make(chan struct{}), done: make(chan struct{})}
	e.containers[c.id] = c
	e.specs = append(e.specs, spec)
	behave := e.behave
	e.mu.Unlock()
	e.log.add("start %s %s", c.id, spec.RunID)
	go behave(c)
	return c, nil
}

func (e *fakeExecutor) Stop(ctx context.Context, id string, _ time.Duration) (executor.Stopped, error) {
	e.log.add("stop %s", id)
	e.mu.Lock()
	e.stopped = append(e.stopped, id)
	c := e.containers[id]
	var left *executor.Container
	for i := range e.leftovers {
		if e.leftovers[i].ID == id {
			left = &e.leftovers[i]
		}
	}
	e.mu.Unlock()
	if c == nil {
		if left != nil {
			return executor.Stopped{AlreadyExited: !left.Running, Exit: executor.ExitStatus{Code: 137}}, nil
		}
		return executor.Stopped{}, fmt.Errorf("%w: %s", executor.ErrNotFound, id)
	}
	select {
	case <-c.done:
		return executor.Stopped{Exit: c.status, AlreadyExited: true}, nil
	default:
	}
	c.stopOnce.Do(func() { close(c.stopCh) })
	select {
	case <-c.done:
		return executor.Stopped{Exit: c.status}, nil
	case <-ctx.Done():
		return executor.Stopped{}, fmt.Errorf("%w: %s: %w", executor.ErrNotStopped, id, ctx.Err())
	}
}

func (e *fakeExecutor) Remove(_ context.Context, id string) error {
	e.log.add("remove %s", id)
	e.mu.Lock()
	defer e.mu.Unlock()
	if c := e.containers[id]; c != nil {
		select {
		case <-c.done:
		default:
			return fmt.Errorf("remove %s: container is running", id)
		}
		delete(e.containers, id)
	}
	e.leftovers = slices.DeleteFunc(e.leftovers, func(c executor.Container) bool { return c.ID == id })
	e.removed = append(e.removed, id)
	return nil
}

func (e *fakeExecutor) List(_ context.Context, labels map[string]string) ([]executor.Container, error) {
	e.log.add("list")
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.listErrs) > 0 {
		err := e.listErrs[0]
		e.listErrs = e.listErrs[1:]
		return nil, err
	}
	var out []executor.Container
	for _, c := range e.leftovers {
		if c.RunnerID == labels[executor.LabelRunnerID] {
			out = append(out, c)
		}
	}
	return out, nil
}

func (e *fakeExecutor) EnsureImage(ctx context.Context, ref string) (string, error) {
	e.mu.Lock()
	block, id := e.blockImage, e.imageID
	e.ensured = append(e.ensured, ref)
	e.mu.Unlock()
	if block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if ref == "" {
		return "", errors.New("empty image reference")
	}
	return id, nil
}

func (e *fakeExecutor) startedSpecs() []executor.Spec {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.specs)
}

func (e *fakeExecutor) wasRemoved(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Contains(e.removed, id)
}

func (e *fakeExecutor) wasStopped(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Contains(e.stopped, id)
}

func (c *fakeContainer) ID() string { return c.id }

func (c *fakeContainer) Wait(ctx context.Context) (executor.ExitStatus, error) {
	select {
	case <-c.done:
		return c.status, c.waitErr
	case <-ctx.Done():
		return executor.ExitStatus{}, ctx.Err()
	}
}

func (c *fakeContainer) exit(code int, oom bool) {
	c.exitOnce.Do(func() {
		c.status = executor.ExitStatus{Code: code, OOMKilled: oom, FinishedAt: time.Now().UTC()}
		close(c.done)
	})
}

func (c *fakeContainer) stdout(s string) { _, _ = c.out.Stdout.Write([]byte(s)) }
func (c *fakeContainer) stderr(s string) { _, _ = c.out.Stderr.Write([]byte(s)) }

// mountSource returns the host directory mounted at target.
func (c *fakeContainer) mountSource(target string) string {
	for _, m := range c.spec.Mounts {
		if m.Target == target {
			return m.Source
		}
	}
	panic("no mount at " + target)
}

// event appends a line to the run's events file, as sbx does.
func (c *fakeContainer) event(line string) {
	path := filepath.Join(c.mountSource(eventsMount), eventsFileName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) // #nosec G304 -- a test's own temp file
	if err != nil {
		panic(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line + "\n"); err != nil {
		panic(err)
	}
}

// vanish removes the container behind the runner's back, as "docker rm -f"
// would.
func (c *fakeContainer) vanish() {
	c.ex.mu.Lock()
	delete(c.ex.containers, c.id)
	c.ex.mu.Unlock()
	c.exitOnce.Do(func() {
		c.waitErr = errors.New("wait: container removed")
		close(c.done)
	})
}

// exitOnStop waits for a stop and exits like a process ending on SIGTERM.
func (c *fakeContainer) exitOnStop() {
	<-c.stopCh
	c.exit(143, false)
}

func (c *fakeContainer) isTask() bool { return !strings.HasSuffix(c.spec.RunID, depsRunSuffix) }
