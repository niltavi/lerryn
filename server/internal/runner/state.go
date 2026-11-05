package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

func (x *runExec) stateCheck(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if x.run.isLost() {
		return runproto.ErrLeaseLost
	}
	return nil
}

// fetchState installs only a complete, validated input before the container
// starts. A missing server object is an error, never an empty initial state.
func (x *runExec) fetchState(ctx, prepCtx context.Context) *verdict {
	var snapshot runproto.StateSnapshot
	err := x.r.call(prepCtx, 3, func(ctx context.Context) error {
		if err := x.stateCheck(ctx); err != nil {
			return err
		}
		var err error
		snapshot, err = x.r.coord.GetState(ctx, x.r.opts.ID, x.run.a.RunID)
		if lost := x.stateCheck(ctx); lost != nil {
			return lost
		}
		return err
	})
	if err == nil {
		err = x.restoreState(prepCtx, snapshot)
	}
	return x.prepFailed(ctx, err, "prepare the task state")
}

// uploadState freezes one complete snapshot for all retries. A ready
// candidate is published by the server only in the successful Finish transaction.
func (x *runExec) uploadState(ctx context.Context) error {
	var snapshot runproto.StateSnapshot
	err := x.r.call(ctx, 0, func(ctx context.Context) error {
		var err error
		snapshot, err = x.collectState(ctx)
		return err
	})
	if err != nil {
		return errors.Join(err, ctx.Err())
	}
	return x.r.call(ctx, 0, func(ctx context.Context) error {
		if err := x.stateCheck(ctx); err != nil {
			return err
		}
		err := x.r.coord.PutState(ctx, x.r.opts.ID, x.run.a.RunID, snapshot)
		if lost := x.stateCheck(ctx); lost != nil {
			return lost
		}
		return err
	})
}

func (x *runExec) restoreState(ctx context.Context, snapshot runproto.StateSnapshot) (err error) {
	if err := x.stateCheck(ctx); err != nil {
		return err
	}
	if _, _, err := runproto.EncodeState(snapshot); err != nil {
		return fmt.Errorf("%w: invalid task state: %w", runproto.ErrRejected, err)
	}
	entries := slices.Clone(snapshot.Entries)
	slices.SortFunc(entries, func(a, b runproto.StateEntry) int { return strings.Compare(a.Path, b.Path) })
	stage, err := os.MkdirTemp(x.dir, tmpPrefix+"state-")
	if err != nil {
		return fmt.Errorf("create task state staging directory: %w", err)
	}
	defer func() {
		if stage != "" {
			err = errors.Join(err, os.RemoveAll(stage))
		}
	}()
	root, err := os.OpenRoot(stage)
	if err != nil {
		return fmt.Errorf("open task state staging directory: %w", err)
	}
	defer func() {
		if root != nil {
			err = errors.Join(err, root.Close())
		}
	}()
	for _, entry := range entries {
		if err := x.stateCheck(ctx); err != nil {
			return err
		}
		if entry.Directory {
			// Every parent was explicitly validated. Exclusive creation
			// also rejects aliases on case-folding or normalizing filesystems.
			if err := root.Mkdir(entry.Path, 0o700); err != nil {
				return fmt.Errorf("%w: create task state directory: %w", runproto.ErrRejected, err)
			}
			continue
		}
		if err := x.writeStateFile(ctx, root, entry); err != nil {
			return err
		}
	}
	// Keep all parents accessible to the writer until extraction finished.
	for _, entry := range entries {
		if !entry.Directory {
			continue
		}
		if err := x.stateCheck(ctx); err != nil {
			return err
		}
		if os.Geteuid() == 0 {
			if err := root.Chown(entry.Path, taskUID, taskGID); err != nil {
				return fmt.Errorf("set task state directory owner: %w", err)
			}
		}
		if err := root.Chmod(entry.Path, stateMode(true, false)); err != nil {
			return fmt.Errorf("set task state directory mode: %w", err)
		}
	}
	if err := root.Close(); err != nil {
		root = nil
		return fmt.Errorf("close task state staging directory: %w", err)
	}
	root = nil
	if err := makeWritable(stage); err != nil {
		return err
	}
	if err := x.stateCheck(ctx); err != nil {
		return err
	}
	target := filepath.Join(x.dir, stateDirName)
	if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("task state destination already exists or cannot be inspected: %w", errors.Join(err, fs.ErrExist))
	}
	if err := os.Rename(stage, target); err != nil {
		return fmt.Errorf("install task state: %w", err)
	}
	stage = ""
	return nil
}

func (x *runExec) writeStateFile(ctx context.Context, root *os.Root, entry runproto.StateEntry) (err error) {
	file, err := root.OpenFile(entry.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("%w: create task state file: %w", runproto.ErrRejected, err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("inspect new task state file: %w", errors.Join(err, fs.ErrInvalid))
	}
	if _, err := io.Copy(file, stateReader{ctx: ctx, x: x, Reader: bytes.NewReader(entry.Content)}); err != nil {
		return fmt.Errorf("write task state file: %w", err)
	}
	if os.Geteuid() == 0 {
		if err := file.Chown(taskUID, taskGID); err != nil {
			return fmt.Errorf("set task state file owner: %w", err)
		}
	}
	if err := file.Chmod(stateMode(false, entry.Executable)); err != nil {
		return fmt.Errorf("set task state file mode: %w", err)
	}
	return nil
}

func stateMode(directory, executable bool) fs.FileMode {
	mode := fs.FileMode(0o600)
	if directory || executable {
		mode = 0o700
	}
	if uid := os.Geteuid(); uid != 0 && uid != taskUID {
		// The private run directory hides this development-only mount
		// from host users; the container uses a different UID.
		mode |= mode >> 3
		mode |= mode >> 6
	}
	return mode
}

func (x *runExec) collectState(ctx context.Context) (snapshot runproto.StateSnapshot, err error) {
	snapshot = runproto.StateSnapshot{Version: runproto.StateFormatVersion, Entries: []runproto.StateEntry{}}
	if err := x.stateCheck(ctx); err != nil {
		return snapshot, err
	}
	dir := filepath.Join(x.dir, stateDirName)
	if err := checkDir(dir); err != nil {
		return snapshot, fmt.Errorf("inspect task state directory: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return snapshot, fmt.Errorf("open task state directory: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	var total int64
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if err := x.stateCheck(ctx); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		if len(snapshot.Entries) == runproto.MaxStateEntries {
			return fmt.Errorf("%w: task state exceeds its entry limit", runproto.ErrRejected)
		}
		if len(path) > runproto.MaxStatePathBytes {
			return fmt.Errorf("%w: task state exceeds its path limit", runproto.ErrRejected)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := runproto.StateEntry{Path: path, Directory: info.IsDir()}
		if !info.IsDir() {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%w: task state contains a non-regular entry", runproto.ErrRejected)
			}
			item.Content, item.Executable, err = x.readStateFile(ctx, root, path, info, int64(runproto.MaxStateBytes)-total)
			if err != nil {
				return err
			}
			total += int64(len(item.Content))
		}
		snapshot.Entries = append(snapshot.Entries, item)
		return nil
	})
	if err != nil {
		return snapshot, fmt.Errorf("collect task state: %w", err)
	}
	if _, _, err := runproto.EncodeState(snapshot); err != nil {
		return snapshot, fmt.Errorf("%w: invalid collected task state: %w", runproto.ErrRejected, err)
	}
	return snapshot, nil
}

func (x *runExec) readStateFile(ctx context.Context, root *os.Root, path string, expected fs.FileInfo, remaining int64) (content []byte, executable bool, err error) {
	file, err := openNonBlocking(root, path)
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	current, err := root.Lstat(path)
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(expected, info) || !os.SameFile(current, info) {
		return nil, false, fmt.Errorf("%w: task state file changed during collection", runproto.ErrRejected)
	}
	if info.Size() > remaining {
		return nil, false, fmt.Errorf("%w: task state exceeds its content limit", runproto.ErrRejected)
	}
	content, err = io.ReadAll(stateReader{ctx: ctx, x: x, Reader: io.LimitReader(file, remaining+1)})
	if err != nil {
		return nil, false, err
	}
	if int64(len(content)) > remaining {
		return nil, false, fmt.Errorf("%w: task state exceeds its content limit", runproto.ErrRejected)
	}
	if int64(len(content)) != info.Size() {
		return nil, false, errors.New("task state file size changed during collection")
	}
	return content, info.Mode().Perm()&0o111 != 0, nil
}

type stateReader struct {
	ctx context.Context
	x   *runExec
	io.Reader
}

func (r stateReader) Read(p []byte) (int, error) {
	if err := r.x.stateCheck(r.ctx); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
