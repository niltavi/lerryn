package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/niltavi/lerryn/server/internal/executor"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

// Error codes from the API contract's ErrorCode that a runner reports.
const (
	errValidationFailed        = "validation_failed"
	errDependencyInstallFailed = "dependency_install_failed"
	errScriptError             = "script_error"
	errTimeout                 = "timeout"
	errOutOfMemory             = "out_of_memory"
	// sigkillExit is the exit code of a main process ended by SIGKILL.
	sigkillExit = 128 + 9
	// errInternal covers failures on the runner's side, such as an image
	// that cannot be pulled. The contract has no narrower code for them yet.
	errInternal = "internal_error"
	// errInterrupted marks runs whose container was lost or stopped by a
	// runner restart (tech design 7.6).
	errInterrupted = "run_interrupted"
)

// verdict is how a run's execution ended for the runner.
type verdict struct {
	// finish follows logs, events, eligible artifacts and any successful task state.
	finish *runproto.Finish
	// abandon leaves the container and the run directory as they are and
	// reports nothing: the runner is shutting down, or a container could
	// not be confirmed stopped. Reconciliation or the lease expiry settles
	// the run.
	abandon bool
}

// runExec is the execution of one claimed run.
type runExec struct {
	r     *Runner
	run   *activeRun
	runID string
	log   *slog.Logger
	// dir is the run directory, <DataDir>/runs/<run id>.
	dir string
	// stages follows the events file: the stage state reported to the
	// server and the stage a failure is attributed to. Log labels follow
	// a tracker of their own, driven by stage markers (markers.go).
	stages *stageTracker
	logs   *logPipeline
	events *eventReader
	// marker is the run's LERRYN_LOG_MARKER token.
	marker string

	envID       string
	containerID string
	startedAt   time.Time
	startedSent bool
}

func (r *Runner) execute(ctx context.Context, run *activeRun) {
	runID := run.a.RunID.String()
	stages := &stageTracker{}
	logs := newLogPipeline(r.limits, &stageTracker{}, r.opts.Redact, r.now)
	marker := rand.Text()
	logs.useMarkers(marker)
	dir := filepath.Join(r.opts.DataDir, runsDirName, runID)
	x := &runExec{
		r:      r,
		run:    run,
		runID:  runID,
		log:    r.log.With("run_id", runID),
		dir:    dir,
		stages: stages,
		logs:   logs,
		events: newEventReader(filepath.Join(dir, eventsDirName), stages, logs, r.now),
		marker: marker,
	}
	logs.beforeOutput = func() { x.events.poll(false) }
	x.log.Info("run claimed", "task_id", run.a.TaskID, "version_id", run.a.VersionID)

	// prepCtx ends when the run must stop, so slow preparation (fetching
	// files, pulling an image) does not delay a cancel. Containers are
	// supervised with ctx instead: stopping them goes through Supervise,
	// which confirms the stop.
	prepCtx, cancelPrep := context.WithCancel(ctx)
	defer cancelPrep()
	go func() {
		select {
		case <-run.stop:
			cancelPrep()
		case <-prepCtx.Done():
		}
	}()

	uploadCtx, stopUploads := context.WithCancel(ctx)
	uploading := make(chan struct{})
	go func() {
		defer close(uploading)
		x.uploadLoop(uploadCtx)
	}()

	v := x.prepareAndRun(ctx, prepCtx)
	stopUploads()
	<-uploading
	defer x.events.close()

	switch {
	case ctx.Err() != nil || v.abandon:
		x.log.Info("leaving run to reconciliation", "container", x.containerID)
	case x.run.isLost() || v.finish == nil:
		x.discard(ctx)
	default:
		x.report(ctx, *v.finish)
	}
}

// prepareAndRun prepares the run, runs its container to the end and says
// how it ended.
func (x *runExec) prepareAndRun(ctx, prepCtx context.Context) verdict {
	a := x.run.a
	if err := x.makeDirs(); err != nil {
		return *x.fail(errInternal, fmt.Sprintf("prepare the run directory: %v", err))
	}
	if !x.r.allowed[a.Manifest.Network] {
		return *x.fail(errValidationFailed, fmt.Sprintf("network %q is not allowed on this runner", a.Manifest.Network))
	}
	rt, ok := x.r.runtimes[a.Manifest.Runtime.Language]
	if msg := checkRuntime(rt, ok, a.Manifest.Runtime.Language, a.Manifest.Runtime.Lockfile); msg != "" {
		return *x.fail(errValidationFailed, msg)
	}

	var files []runproto.File
	err := x.r.call(prepCtx, 3, func(ctx context.Context) error {
		var err error
		files, err = x.r.coord.Files(ctx, a.VersionID)
		return err
	})
	if v := x.prepFailed(ctx, err, "fetch the task files"); v != nil {
		return *v
	}
	taskDir := filepath.Join(x.dir, taskDirName)
	if err := writeTaskFiles(taskDir, files); err != nil {
		return *x.fail(errInternal, fmt.Sprintf("write the task files: %v", err))
	}
	if err := x.writeScriptTools(a.Manifest.Tools); err != nil {
		return *x.fail(errInternal, fmt.Sprintf("write the script tools: %v", err))
	}

	image, err := x.r.ex.EnsureImage(prepCtx, rt.image)
	if v := x.prepFailed(ctx, err, "pull the runtime image "+rt.image); v != nil {
		return *v
	}
	if image == "" {
		image = rt.image
	}
	sdkDir, err := x.r.sdkPath(rt.language)
	if err != nil {
		return *x.fail(errInternal, err.Error())
	}

	var envDir string
	if a.Manifest.Runtime.Lockfile != "" {
		var v *verdict
		envDir, x.envID, v = x.prepareEnv(ctx, prepCtx, rt, image, files)
		if v != nil {
			return *v
		}
	}
	if err := prepareTaskDir(rt, taskDir, envDir); err != nil {
		return *x.fail(errInternal, fmt.Sprintf("prepare the task directory: %v", err))
	}
	var inputType string
	if a.HasInput {
		var v *verdict
		if inputType, v = x.fetchInput(ctx, prepCtx); v != nil {
			return *v
		}
	}
	var secrets *runSecrets
	if len(a.Manifest.Credentials) > 0 {
		var v *verdict
		if secrets, v = x.fetchSecrets(ctx, prepCtx); v != nil {
			return *v
		}
	}
	if a.HasState {
		if v := x.fetchState(ctx, prepCtx); v != nil {
			secrets.wipe()
			return *v
		}
	}
	if x.run.stopRequested() {
		secrets.wipe()
		return *x.stoppedBeforeStart(ctx)
	}

	out, flush := x.logs.taskOutput()
	spec := x.taskSpec(rt, image, sdkDir, envDir)
	if a.HasInput {
		spec.Env = append(spec.Env, "LERRYN_INPUT_CONTENT_TYPE="+inputType)
		spec.Mounts = append(spec.Mounts, executor.Mount{Source: filepath.Join(x.dir, inDirName), Target: inMount, ReadOnly: true})
	}
	if secrets != nil {
		spec.Stdin = secrets.payload
	}
	exec, err := x.r.ex.Start(prepCtx, spec, out)
	secrets.wipe()
	if v := x.prepFailed(ctx, err, "start the container"); v != nil {
		return *v
	}
	x.containerID = exec.ID()
	x.startedAt = x.r.now().UTC()
	x.log.Info("run started", "container", exec.ID(), "env_id", x.envID)
	// Reporting a start must not delay the time limit or cancellation.
	// Once execution ends, any unfinished report is retried with the final
	// uploads, before Finish advances the server's state machine.
	startedCtx, cancelStarted := context.WithCancel(prepCtx)
	started := make(chan error, 1)
	go func() { started <- x.markStarted(startedCtx) }()

	res, err := executor.Supervise(ctx, x.r.ex, exec, executor.SuperviseOptions{
		Timeout: a.Manifest.Timeout,
		Grace:   x.r.stopGrace,
		Cancel:  x.run.stop,
	})
	cancelStarted()
	x.startedSent = <-started == nil
	if ctx.Err() != nil {
		return verdict{abandon: true}
	}
	if err != nil {
		return x.supervisionFailed(ctx, err)
	}
	flush()
	return verdict{finish: x.finishFor(res)}
}

// fetchInput asks the server for the run's input and writes it to the
// run's input directory, which is mounted read-only at /sbx/in. It returns
// the input's media type.
func (x *runExec) fetchInput(ctx, prepCtx context.Context) (string, *verdict) {
	var in runproto.Input
	err := x.r.call(prepCtx, 3, func(ctx context.Context) error {
		var err error
		in, err = x.r.coord.Input(ctx, x.r.opts.ID, x.run.a.RunID)
		return err
	})
	if v := x.prepFailed(ctx, err, "fetch the run input"); v != nil {
		return "", v
	}
	if len(in.Content) > runproto.MaxInputBytes {
		return "", x.fail(errInternal, fmt.Sprintf("the run input is larger than %d bytes", runproto.MaxInputBytes))
	}
	if err := writeInput(filepath.Join(x.dir, inDirName), in.Content); err != nil {
		return "", x.fail(errInternal, fmt.Sprintf("write the run input: %v", err))
	}
	// The media type comes from the webhook caller; keep it to one line
	// so it cannot add environment entries.
	return oneLine(in.ContentType), nil
}

// prepFailed turns an error from a preparation step into a verdict, or
// returns nil when there was none. A step that failed because the run was
// asked to stop ends the run as cancelled.
func (x *runExec) prepFailed(ctx context.Context, err error, step string) *verdict {
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return &verdict{abandon: true}
	case errors.Is(err, runproto.ErrLeaseLost):
		x.run.markLost()
		return &verdict{}
	case x.run.stopRequested():
		return x.stoppedBeforeStart(ctx)
	default:
		x.log.Warn("run preparation failed", "step", step, "err", x.logs.redactText(err.Error()))
		return x.fail(errInternal, fmt.Sprintf("could not %s: %v", step, err))
	}
}

// fail ends the run as failed before or without its process running.
func (x *runExec) fail(code, msg string) *verdict {
	x.logs.system("run failed: %s", msg)
	return &verdict{finish: &runproto.Finish{
		Status:       runproto.StatusFailed,
		ErrorCode:    code,
		ErrorMessage: msg,
		FinishedAt:   x.r.now().UTC(),
	}}
}

// stoppedBeforeStart ends a run that was asked to stop before its process
// started. There is no container to confirm stopped. A lost run is
// discarded by the caller whatever this returns.
func (x *runExec) stoppedBeforeStart(ctx context.Context) *verdict {
	if ctx.Err() != nil {
		return &verdict{abandon: true}
	}
	x.logs.system("run cancelled before its process started")
	return &verdict{finish: &runproto.Finish{
		Status:     runproto.StatusCancelled,
		FinishedAt: x.r.now().UTC(),
	}}
}

// supervisionFailed handles a container whose end could not be observed.
// If it is gone, or a further stop confirms it is no longer running, the
// run is interrupted; otherwise nothing is reported, so the run is never
// shown as stopped while its container might still run (tech design 7.6).
func (x *runExec) supervisionFailed(ctx context.Context, err error) verdict {
	x.log.Error("supervising the run's container failed", "container", x.containerID, "err", err)
	msg := "the runner lost track of the run's container"
	if !errors.Is(err, executor.ErrNotFound) {
		stopCtx, cancel := context.WithTimeout(ctx, x.r.stopGrace+30*time.Second)
		_, stopErr := x.r.ex.Stop(stopCtx, x.containerID, x.r.stopGrace)
		cancel()
		if stopErr != nil && !errors.Is(stopErr, executor.ErrNotFound) {
			x.log.Error("container not confirmed stopped; leaving the run to its lease", "container", x.containerID, "err", stopErr)
			return verdict{abandon: true}
		}
		if errors.Is(stopErr, executor.ErrNotFound) {
			err = stopErr
		}
	}
	if errors.Is(err, executor.ErrNotFound) {
		msg = "the run's container disappeared while the run was in progress"
	}
	x.logs.system("run interrupted: %s", msg)
	return verdict{finish: &runproto.Finish{
		Status:       runproto.StatusInterrupted,
		ErrorCode:    errInterrupted,
		ErrorMessage: msg,
		FinishedAt:   x.r.now().UTC(),
	}}
}

// finishFor maps how the container ended to the run's terminal state
// (tech design 5.4).
func (x *runExec) finishFor(res executor.Result) *runproto.Finish {
	code := res.Exit.Code
	f := &runproto.Finish{ExitCode: &code, FinishedAt: res.Exit.FinishedAt}
	if f.FinishedAt.IsZero() {
		f.FinishedAt = x.r.now().UTC()
	}
	m := x.run.a.Manifest
	switch res.Outcome {
	case executor.OutcomeCanceled:
		f.Status = runproto.StatusCancelled
		x.logs.system("run cancelled; the container was stopped (exit code %d)", code)
	case executor.OutcomeTimedOut:
		f.Status = runproto.StatusTimedOut
		f.ErrorCode = errTimeout
		f.ErrorStage = x.stages.errorStage()
		f.ErrorMessage = fmt.Sprintf("the run exceeded its time limit of %s and was stopped", m.Timeout)
		x.logs.system("run timed out: %s", f.ErrorMessage)
	default:
		switch {
		case code == 0:
			f.Status = runproto.StatusCompleted
			if res.Exit.OOMKilled {
				// The main process carried on after the kernel killed
				// another process for exceeding the memory limit.
				x.logs.system("warning: a process of this run was killed for exceeding the memory limit of %s, but the main process exited with code 0",
					formatBytes(m.MemoryBytes))
			}
			x.logs.system("process exited with code 0")
			return f
		case res.Exit.OOMKilled:
			f.Status = runproto.StatusFailed
			f.ErrorCode = errOutOfMemory
			f.ErrorMessage = fmt.Sprintf("a process exceeded the memory limit of %s and was killed; the main process exited with code %d",
				formatBytes(m.MemoryBytes), code)
		case code == sigkillExit:
			// The daemon's OOM flag is not reliable on cgroup v2: when the
			// OOM kill ends the container at once, the cgroup is gone
			// before containerd reports the event, and the flag stays
			// false. Nothing else in the container may SIGKILL the main
			// process unless the task does it itself, and this exit was
			// not a stop by the runner, so the memory limit is the likely
			// cause; the message says it is inferred.
			f.Status = runproto.StatusFailed
			f.ErrorCode = errOutOfMemory
			f.ErrorMessage = fmt.Sprintf("the main process was killed by SIGKILL (exit code %d) without a stop from the runner; "+
				"this is almost always the kernel enforcing the memory limit of %s", code, formatBytes(m.MemoryBytes))
		default:
			f.Status = runproto.StatusFailed
			f.ErrorCode = errScriptError
			f.ErrorMessage = fmt.Sprintf("the process exited with code %d", code)
		}
		f.ErrorStage = x.stages.errorStage()
		x.logs.system("run failed: %s", f.ErrorMessage)
	}
	return f
}

func (x *runExec) makeDirs() error {
	if err := mkdirPrivate(x.dir); err != nil {
		return err
	}
	if err := mkdirReadable(filepath.Join(x.dir, taskDirName)); err != nil {
		return err
	}
	for _, name := range []string{eventsDirName, workDirName, outDirName} {
		if err := mkdirWritable(filepath.Join(x.dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// taskSpec describes the run's container.
func (x *runExec) taskSpec(rt runtimeDef, image, sdkDir, envDir string) executor.Spec {
	a := x.run.a
	pathPrefix, rtEnv := runtimeEnv(rt, envDir)
	env := append([]string{
		"PATH=" + pathPrefix + sdkMount + "/bin:" + basePath,
		"HOME=/tmp",
		"LERRYN_RUN_ID=" + x.runID,
		"LERRYN_PARAMS=" + paramsJSON(a.Params),
		"LERRYN_EVENTS_FILE=" + eventsFile,
		"LERRYN_LOG_MARKER=" + x.marker,
		"LERRYN_SECRETS_DIR=" + secretsDir,
	}, rtEnv...)
	env = append(env, toolEnv(a.Manifest.Tools)...)
	mounts := append([]executor.Mount{
		{Source: filepath.Join(x.dir, taskDirName), Target: taskMount, ReadOnly: true},
		{Source: filepath.Join(x.dir, eventsDirName), Target: eventsMount},
		{Source: filepath.Join(x.dir, workDirName), Target: workMount},
		{Source: filepath.Join(x.dir, outDirName), Target: outMount},
		{Source: sdkDir, Target: sdkMount, ReadOnly: true},
	}, runtimeMounts(rt, sdkDir, envDir)...)
	mounts = append(mounts, x.scriptToolMounts(a.Manifest.Tools)...)
	if a.HasState {
		mounts = append(mounts, executor.Mount{Source: filepath.Join(x.dir, stateDirName), Target: stateMount})
	}
	return executor.Spec{
		RunID:    x.runID,
		RunnerID: x.r.opts.ID,
		Image:    image,
		// sbx-exec installs the credentials from stdin and execs the
		// entrypoint in the same process (tech design 7.7, 12.5).
		Command:    append([]string{sbxExec, "--"}, a.Manifest.Entrypoint...),
		Env:        env,
		WorkingDir: taskMount,
		Mounts:     mounts,
		Network:    executor.Network(a.Manifest.Network),
		Resources: executor.Resources{
			MilliCPUs:   a.Manifest.MilliCPUs,
			MemoryBytes: a.Manifest.MemoryBytes,
		},
		StopGrace: x.r.stopGrace,
	}
}

// paramsJSON is the compact JSON of the run's parameters, "{}" when there
// are none.
func paramsJSON(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "{}"
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

// markStarted reports the container running. A failure other than a lost
// lease is logged and retried after execution, so supervision can carry on.
func (x *runExec) markStarted(ctx context.Context) error {
	err := x.r.call(ctx, 3, func(ctx context.Context) error {
		return x.r.coord.Started(ctx, x.r.opts.ID, x.run.a.RunID, x.envID, x.startedAt)
	})
	switch {
	case errors.Is(err, runproto.ErrLeaseLost):
		x.run.markLost()
	case err != nil && ctx.Err() == nil:
		x.log.Warn("report run started", "err", err)
	}
	return err
}

// uploadLoop streams logs and events while the run executes.
func (x *runExec) uploadLoop(ctx context.Context) {
	logTick := time.NewTicker(x.r.logFlushInterval)
	defer logTick.Stop()
	eventTick := time.NewTicker(x.r.eventPollInterval)
	defer eventTick.Stop()
	var logErr, eventErr error
	for {
		select {
		case <-ctx.Done():
			return
		case <-logTick.C:
			logErr = x.uploadStep(ctx, "logs", x.sendLogs, logErr)
		case <-x.logs.kick:
			logErr = x.uploadStep(ctx, "logs", x.sendLogs, logErr)
		case <-eventTick.C:
			x.events.poll(false)
			x.logs.expireMarks()
			eventErr = x.uploadStep(ctx, "events", x.sendEvents, eventErr)
		}
	}
}

// uploadStep runs one upload attempt. Failures keep the data for the next
// attempt and are logged once per streak; a lost lease stops the run.
func (x *runExec) uploadStep(ctx context.Context, what string, send func(context.Context) error, prev error) error {
	if x.run.isLost() {
		return nil
	}
	err := send(ctx)
	switch {
	case err == nil:
		if prev != nil {
			x.log.Info("upload recovered", "what", what)
		}
	case errors.Is(err, runproto.ErrLeaseLost):
		x.log.Warn("lease lost while uploading; stopping the run", "what", what)
		x.run.markLost()
	case ctx.Err() != nil:
	case prev == nil:
		x.log.Warn("upload failed; will retry", "what", what, "err", err)
	}
	return err
}

func (x *runExec) sendLogs(ctx context.Context) error {
	return x.logs.send(ctx, func(ctx context.Context, lines []runproto.LogLine, dropped runproto.Dropped) error {
		return x.r.coord.AppendLogs(ctx, x.r.opts.ID, x.run.a.RunID, lines, dropped)
	})
}

func (x *runExec) sendEvents(ctx context.Context) error {
	return x.events.send(ctx, func(ctx context.Context, events []runproto.Event) error {
		return x.r.coord.AppendEvents(ctx, x.r.opts.ID, x.run.a.RunID, events)
	})
}

// report delivers the remaining logs, events, artifacts and successful task state,
// then the terminal status, and removes the container and run directory. If delivery
// does not succeed within the report timeout the runner gives up: the run's lease
// then expires and the server marks it interrupted.
func (x *runExec) report(ctx context.Context, f runproto.Finish) {
	rctx, cancel := context.WithTimeout(ctx, x.r.reportTimeout)
	defer cancel()

	f.ErrorMessage = x.logs.redactText(f.ErrorMessage)
	x.events.poll(true)
	var err error
	if x.containerID != "" && !x.startedSent {
		err = x.r.call(rctx, 0, func(ctx context.Context) error {
			return x.r.coord.Started(ctx, x.r.opts.ID, x.run.a.RunID, x.envID, x.startedAt)
		})
	}
	if err == nil {
		err = x.r.call(rctx, 0, func(ctx context.Context) error {
			return errors.Join(x.sendEvents(ctx), x.sendLogs(ctx))
		})
	}
	// Artifacts follow the task's own events, which happened before the
	// output directory was read; the log lines about skipped files go
	// out after them.
	if err == nil && x.containerID != "" {
		err = x.r.call(rctx, 0, x.uploadArtifacts)
		if err != nil {
			err = errors.Join(err, rctx.Err())
		}
		// Preserve a delivery error while sending its task warning, if the
		// lease and reporting deadline still allow further calls.
		if rctx.Err() == nil && !x.run.isLost() && !errors.Is(err, runproto.ErrLeaseLost) {
			err = errors.Join(err, x.r.call(rctx, 0, x.sendLogs))
		}
	}
	if err == nil && x.run.a.HasState && f.Status == runproto.StatusCompleted {
		err = x.uploadState(rctx)
		if err != nil {
			x.logs.system("could not deliver task state: %v", err)
			if rctx.Err() == nil && !x.run.isLost() && !errors.Is(err, runproto.ErrLeaseLost) {
				err = errors.Join(err, x.r.call(rctx, 0, x.sendLogs))
			}
		}
	}
	if err == nil {
		err = x.r.call(rctx, 0, func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if x.run.isLost() {
				return runproto.ErrLeaseLost
			}
			return x.r.coord.Finish(ctx, x.r.opts.ID, x.run.a.RunID, f)
		})
	}
	switch {
	case err == nil:
		x.log.Info("run finished", "status", f.Status, "error_code", f.ErrorCode)
	case ctx.Err() != nil:
		return
	case errors.Is(err, runproto.ErrLeaseLost) || x.run.isLost():
		x.run.markLost()
		x.discard(ctx)
		return
	default:
		x.log.Error("could not deliver the end of the run; leaving it to its lease", "err", x.logs.redactText(err.Error()))
	}
	x.cleanup(ctx)
}

// discard drops a run that is no longer ours: its container is stopped and
// removed, and nothing more is reported.
func (x *runExec) discard(ctx context.Context) {
	x.log.Warn("run no longer assigned to this runner; discarded")
	if x.containerID != "" {
		stopCtx, cancel := context.WithTimeout(ctx, x.r.stopGrace+30*time.Second)
		_, err := x.r.ex.Stop(stopCtx, x.containerID, x.r.stopGrace)
		cancel()
		if err != nil && !errors.Is(err, executor.ErrNotFound) {
			x.log.Error("could not stop the container of a lost run", "container", x.containerID, "err", err)
			return
		}
	}
	x.cleanup(ctx)
}

func (x *runExec) cleanup(ctx context.Context) {
	if x.containerID != "" {
		x.removeContainer(ctx, x.containerID)
	}
	if err := removeAll(x.dir); err != nil {
		x.log.Warn("could not remove the run directory", "err", x.logs.redactText(err.Error()))
	}
}

func (x *runExec) removeContainer(ctx context.Context, id string) {
	// The container is stopped and the run reported, so removal is quick
	// and safe to finish during shutdown; without it the container would
	// wait for the next start.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := x.r.ex.Remove(rctx, id); err != nil {
		// A stopped container left behind is removed when the runner next
		// starts.
		x.log.Warn("could not remove container", "container", id, "err", err)
	}
}

// call runs fn until it succeeds, returns ErrLeaseLost or ErrRejected,
// ctx ends, or attempts (when positive) run out, backing off between
// attempts.
func (r *Runner) call(ctx context.Context, attempts int, fn func(context.Context) error) error {
	delay := r.retryBase
	for i := 1; ; i++ {
		err := fn(ctx)
		if err == nil || errors.Is(err, runproto.ErrLeaseLost) || errors.Is(err, runproto.ErrRejected) || isCredentialError(err) || ctx.Err() != nil {
			return err
		}
		if attempts > 0 && i >= attempts {
			return err
		}
		if !sleep(ctx, delay) {
			return err
		}
		delay = min(delay*2, r.retryMax)
	}
}
