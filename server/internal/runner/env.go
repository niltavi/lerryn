package runner

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/niltavi/lerryn/server/internal/executor"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

// Dependency handling (tech design 7.3).
const (
	// depsRunSuffix is appended to the run ID of an installation
	// container, so its name and labels differ from the run's own
	// container while reconciliation can still tell which run it served.
	depsRunSuffix = ".deps"
)

// basePath is PATH in task and installation containers before the SDK and
// environment directories are put in front. Images may declare their own
// PATH, but the runner sets the whole environment, so it lists the usual
// directories itself.
const basePath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// environmentID identifies a dependency environment: the runtime image, the
// install command and the files it installs from. Each part is
// length-prefixed so different splits of the same bytes cannot collide, and
// the format tag with the install command keeps environments built
// differently apart.
func environmentID(image string, installCommand []string, files ...[]byte) string {
	h := sha256.New()
	// The cache format predates Lerryn; renaming it would rebuild otherwise
	// identical dependency environments after an upgrade.
	tag := "waleng-env-v1\x00" + strings.Join(installCommand, " ")
	parts := append([][]byte{[]byte(tag), []byte(image)}, files...)
	for _, part := range parts {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// prepareEnv makes sure the run's dependency environment exists and returns
// its directory and ID. Environments are cached by ID under envs/ and built
// at most once at a time per ID: a second run needing the same environment
// waits and then uses the cached result. If a build fails, the next waiter
// tries again with its own logs, since a failure is often temporary.
//
// A non-nil verdict means the run ends here.
func (x *runExec) prepareEnv(ctx, prepCtx context.Context, rt runtimeDef, image string, files []runproto.File) (dir, id string, v *verdict) {
	contents := make([][]byte, len(rt.depFiles))
	for i, name := range rt.depFiles {
		found := false
		for _, f := range files {
			if f.Path == name {
				contents[i], found = f.Content, true
				break
			}
		}
		if !found {
			return "", "", x.fail(errDependencyInstallFailed, fmt.Sprintf(
				"runtime.lockfile is %s, so the task needs both %s at its top level", rt.lockfile, strings.Join(rt.depFiles, " and ")))
		}
	}

	id = environmentID(image, rt.installCommand, contents...)
	dir = filepath.Join(x.r.opts.DataDir, envsDirName, id)
	unlock, err := x.r.envLocks.lock(prepCtx, id)
	if err != nil {
		return "", "", x.stoppedBeforeStart(ctx)
	}
	defer unlock()

	if checkDir(dir) == nil {
		x.logs.system("using cached dependency environment %s", shortID(id))
		return dir, id, nil
	}
	if v := x.install(ctx, prepCtx, rt, image, id, dir, contents); v != nil {
		return "", "", v
	}
	return dir, id, nil
}

func (x *runExec) install(ctx, prepCtx context.Context, rt runtimeDef, image, id, dir string, contents [][]byte) *verdict {
	started := x.r.now()
	x.logs.system("installing dependencies from %s into environment %s", rt.lockfile, shortID(id))

	tmp, err := os.MkdirTemp(filepath.Join(x.r.opts.DataDir, envsDirName), tmpPrefix+shortID(id)+"-")
	if err != nil {
		return x.fail(errInternal, fmt.Sprintf("prepare dependency installation: %v", err))
	}
	keep := false
	defer func() {
		if !keep {
			if err := removeAll(tmp); err != nil {
				x.log.Warn("could not remove unfinished environment", "err", err)
			}
		}
	}()
	if err := makeWritable(tmp); err != nil {
		return x.fail(errInternal, fmt.Sprintf("prepare dependency installation: %v", err))
	}

	mounts := []executor.Mount{{Source: tmp, Target: envMount}}
	workDir := envMount
	if rt.depsInEnv {
		err = writeDepsFiles(tmp, false, rt.depFiles, contents)
	} else {
		depsDir := filepath.Join(x.dir, depsDirName)
		err = writeDepsFiles(depsDir, true, rt.depFiles, contents)
		mounts = append([]executor.Mount{{Source: depsDir, Target: taskMount, ReadOnly: true}}, mounts...)
		workDir = taskMount
	}
	if err != nil {
		return x.fail(errInternal, fmt.Sprintf("prepare dependency installation: %v", err))
	}

	spec := executor.Spec{
		RunID:      x.runID + depsRunSuffix,
		RunnerID:   x.r.opts.ID,
		Image:      image,
		Command:    rt.installCommand,
		Env:        x.r.installEnv(rt),
		WorkingDir: workDir,
		Mounts:     mounts,
		// Installation reaches the package index whatever the task's own
		// network policy is.
		Network:   executor.NetworkFull,
		Resources: x.r.installResources,
		StopGrace: x.r.stopGrace,
	}
	out, flush := x.logs.output()
	exec, err := x.r.ex.Start(prepCtx, spec, out)
	if err != nil {
		if x.run.stopRequested() {
			return x.stoppedBeforeStart(ctx)
		}
		return x.fail(errDependencyInstallFailed, fmt.Sprintf("start the dependency installation: %v", err))
	}
	res, err := executor.Supervise(ctx, x.r.ex, exec, executor.SuperviseOptions{
		Timeout: x.r.installTimeout,
		Grace:   x.r.stopGrace,
		Cancel:  x.run.stop,
	})
	if ctx.Err() != nil {
		// The runner is shutting down; the container is left for the
		// next start to clean up, and so is the unfinished environment.
		keep = true
		return &verdict{abandon: true}
	}
	if err != nil {
		// The container could not be confirmed stopped; nothing about
		// the run is reported, and its lease runs out (tech design 7.6).
		x.log.Error("dependency installation container not confirmed stopped", "container", exec.ID(), "err", err)
		keep = true
		return &verdict{abandon: true}
	}
	flush()
	x.removeContainer(ctx, exec.ID())

	switch {
	case res.Outcome == executor.OutcomeCanceled:
		return x.stoppedBeforeStart(ctx)
	case res.Outcome == executor.OutcomeTimedOut:
		return x.fail(errDependencyInstallFailed, fmt.Sprintf("dependency installation did not finish within %s", x.r.installTimeout))
	case res.Exit.Code != 0:
		msg := fmt.Sprintf("dependency installation failed: %s exited with code %d", strings.Join(rt.installCommand[:2], " "), res.Exit.Code)
		if res.Exit.OOMKilled {
			msg += fmt.Sprintf(" after exceeding the installation memory limit of %s", formatBytes(x.r.installResources.MemoryBytes))
		}
		return x.fail(errDependencyInstallFailed, msg)
	}
	if err := finishEnv(rt, tmp); err != nil {
		return x.fail(errInternal, fmt.Sprintf("store dependency environment: %v", err))
	}

	if err := os.Rename(tmp, dir); err != nil {
		if checkDir(dir) != nil {
			return x.fail(errInternal, fmt.Sprintf("store dependency environment: %v", err))
		}
		// Built concurrently by another runner sharing the data dir.
	} else {
		keep = true
	}
	x.logs.system("dependency environment %s ready in %s", shortID(id), x.r.now().Sub(started).Round(100*time.Millisecond))
	return nil
}

// writeDepsFiles writes the dependency files into dir, creating it first
// when create is set.
func writeDepsFiles(dir string, create bool, names []string, contents [][]byte) error {
	if create {
		if err := mkdirReadable(dir); err != nil {
			return err
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	errs := make([]error, len(names))
	for i, name := range names {
		errs[i] = writeFile(root, name, contents[i], 0o644)
	}
	return errors.Join(errs...)
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
