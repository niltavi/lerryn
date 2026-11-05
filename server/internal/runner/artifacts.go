package runner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

// uploadArtifacts sends the files the task left in /sbx/out to the
// server. It runs after the container stopped and before Finish, so the
// run's artifacts are complete when it is reported ended.
//
// The directory was writable by the task, so everything in it is
// untrusted: it is read through an os.Root, symbolic links and special
// files are skipped rather than followed, and the per-run limits are
// applied before anything is sent. Eligible files must be delivered before
// Finish; failures are returned to the bounded reporting phase.
func (x *runExec) uploadArtifacts(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if x.run.isLost() {
		return runproto.ErrLeaseLost
	}
	// Preparation created this directory before starting the container,
	// so its absence is a collection failure.
	root, err := os.OpenRoot(filepath.Join(x.dir, outDirName))
	if err != nil {
		x.logs.system("could not read the output directory: %v", err)
		return fmt.Errorf("read output directory: %w", err)
	}
	defer func() { _ = root.Close() }()

	type file struct {
		path string
		size int64
	}
	var (
		files []file
		total int64
	)
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if x.run.isLost() {
			return runproto.ErrLeaseLost
		}
		if err != nil {
			x.logs.system("could not read %s in the output directory: %v", p, err)
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			x.logs.system("artifact %s skipped: not a regular file", p)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			x.logs.system("could not inspect artifact %s: %v", p, err)
			return err
		}
		switch size := info.Size(); {
		case len(files) >= runproto.MaxArtifacts:
			x.logs.system("artifact %s skipped: a run keeps at most %d artifacts", p, runproto.MaxArtifacts)
		case size > runproto.MaxArtifactBytes:
			x.logs.system("artifact %s skipped: %s is over the limit of %s per file", p, formatBytes(size), formatBytes(runproto.MaxArtifactBytes))
		case total+size > runproto.MaxRunArtifactBytes:
			x.logs.system("artifact %s skipped: the run's artifacts would exceed %s", p, formatBytes(runproto.MaxRunArtifactBytes))
		default:
			files = append(files, file{path: p, size: size})
			total += size
		}
		return nil
	})
	if err != nil {
		x.logs.system("could not list the output directory: %v", err)
		return fmt.Errorf("list output directory: %w", err)
	}

	var uploaded int
	var uploadedBytes int64
	defer func() {
		if uploaded > 0 {
			x.log.Info("artifacts uploaded", "count", uploaded, "bytes", uploadedBytes)
		}
	}()
	var failed error
	for _, f := range files {
		err := x.r.call(ctx, 0, func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if x.run.isLost() {
				return runproto.ErrLeaseLost
			}
			content, err := root.Open(f.path)
			if err != nil {
				return err
			}
			defer func() { _ = content.Close() }()
			err = x.r.coord.AddArtifact(ctx, x.r.opts.ID, x.run.a.RunID, runproto.Artifact{Path: f.path, Size: f.size, Content: content})
			if x.run.isLost() {
				return runproto.ErrLeaseLost
			}
			return err
		})
		switch {
		case err == nil:
			uploaded++
			uploadedBytes += f.size
		case errors.Is(err, runproto.ErrLeaseLost):
			x.run.markLost()
			return errors.Join(failed, err)
		case ctx.Err() != nil:
			return errors.Join(failed, err, ctx.Err())
		default:
			x.log.Warn("upload artifact", "path", x.logs.redactText(f.path), "err", x.logs.redactText(err.Error()))
			x.logs.system("could not upload artifact %s: %v", f.path, err)
			failed = errors.Join(failed, fmt.Errorf("upload artifact %s: %w", f.path, err))
		}
	}
	return failed
}
