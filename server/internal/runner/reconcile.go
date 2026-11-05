package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/executor"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

// reconcile cleans up after an earlier instance of this runner (tech design
// 7.6): every container labelled with this runner's ID is stopped and
// removed, and its run is finished as interrupted. Taking over containers
// that are still running is left for a later version.
//
// It returns an error, and is retried, only when a container could not be
// stopped or removed; a failed Finish is logged and left to the lease.
func (r *Runner) reconcile(ctx context.Context) error {
	containers, err := r.ex.List(ctx, map[string]string{executor.LabelRunnerID: r.opts.ID})
	if err != nil {
		return fmt.Errorf("list leftover containers: %w", err)
	}
	var errs []error
	var runs []uuid.UUID
	seen := map[uuid.UUID]bool{}
	for _, c := range containers {
		if c.RunnerID != r.opts.ID {
			continue
		}
		log := r.log.With("container", c.ID, "run_id", c.RunID)
		stopCtx, cancel := context.WithTimeout(ctx, r.stopGrace+30*time.Second)
		_, err := r.ex.Stop(stopCtx, c.ID, r.stopGrace)
		cancel()
		if err != nil && !errors.Is(err, executor.ErrNotFound) {
			errs = append(errs, fmt.Errorf("stop leftover container %s: %w", c.ID, err))
			continue
		}
		if err := r.ex.Remove(ctx, c.ID); err != nil {
			errs = append(errs, fmt.Errorf("remove leftover container %s: %w", c.ID, err))
			continue
		}
		log.Info("removed leftover container", "was_running", c.Running)
		if id, ok := runIDFromLabel(c.RunID); ok && !seen[id] {
			seen[id] = true
			runs = append(runs, id)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}

	for _, id := range runs {
		err := r.coord.Finish(ctx, r.opts.ID, id, runproto.Finish{
			Status:       runproto.StatusInterrupted,
			ErrorCode:    errInterrupted,
			ErrorMessage: "the runner restarted while the run was in progress; its container was stopped",
			FinishedAt:   r.now().UTC(),
		})
		switch {
		case err == nil:
			r.log.Info("leftover run marked interrupted", "run_id", id)
		case errors.Is(err, runproto.ErrLeaseLost):
			// Already settled, by the lease expiry or by an earlier Finish.
		default:
			r.log.Warn("could not mark leftover run interrupted; its lease will expire", "run_id", id, "err", err)
		}
	}

	r.removeLeftovers()
	return nil
}

// runIDFromLabel parses a container's run ID label. Installation
// containers carry the ID of the run they served plus a suffix.
func runIDFromLabel(label string) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSuffix(label, depsRunSuffix))
	return id, err == nil
}

// removeLeftovers deletes run directories and unfinished builds. At start
// no run of this runner is active, so every run directory is left over from
// a run that is now settled.
func (r *Runner) removeLeftovers() {
	var dirs []string
	runs := filepath.Join(r.opts.DataDir, runsDirName)
	if entries, err := os.ReadDir(runs); err == nil {
		for _, e := range entries {
			dirs = append(dirs, filepath.Join(runs, e.Name()))
		}
	}
	for _, parent := range []string{envsDirName, sdkDirName} {
		parent = filepath.Join(r.opts.DataDir, parent)
		entries, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), tmpPrefix) {
				dirs = append(dirs, filepath.Join(parent, e.Name()))
			}
		}
	}
	for _, d := range dirs {
		if err := removeAll(d); err != nil {
			r.log.Warn("could not remove leftover directory", "err", err)
		}
	}
}
