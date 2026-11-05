package runner

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

// Layout under DataDir.
const (
	runsDirName = "runs"
	envsDirName = "envs"
	sdkDirName  = "sdk"
	// tmpPrefix marks directories being built. Run removes leftovers at
	// start; nothing else ever reads them.
	tmpPrefix = ".tmp-"
)

// Directories inside a run directory and where they are mounted.
const (
	taskDirName   = "task"
	eventsDirName = "events"
	workDirName   = "work"
	// outDirName holds the task's artifacts, uploaded when the run ends
	// (tech design 7.4).
	outDirName   = "out"
	stateDirName = "state"
	// inDirName holds the run input, such as a webhook body (tech design
	// 7.4); created only for runs that have one.
	inDirName = "in"
	// depsDirName holds copies of pyproject.toml and uv.lock for the
	// installation container, so nothing else from the task can influence
	// an environment that is cached under a hash of those two files.
	depsDirName = "deps"

	taskMount   = "/sbx/task"
	eventsMount = "/sbx/events"
	workMount   = "/sbx/work"
	outMount    = "/sbx/out"
	stateMount  = "/sbx/state"
	sdkMount    = "/sbx/sdk"
	envMount    = "/sbx/env"
	inMount     = "/sbx/in"

	inputFileName = "body"

	eventsFileName = "events.ndjson"
	eventsFile     = eventsMount + "/" + eventsFileName
)

// taskUID and taskGID are the user task containers run as, the executor's
// DefaultUser.
const (
	taskUID = 65532
	taskGID = 65532
)

// ensureLayout creates DataDir and the directories the runner owns.
//
// Everything the runner creates is private to the runner's user (0700),
// except what a container must read or write. Mount sources are reached by
// the container runtime directly, so a container does not need to traverse
// the private parents; other users on the host cannot.
func (r *Runner) ensureLayout() error {
	if err := os.MkdirAll(r.opts.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	for _, name := range []string{runsDirName, envsDirName, sdkDirName} {
		if err := mkdirPrivate(filepath.Join(r.opts.DataDir, name)); err != nil {
			return err
		}
	}
	return nil
}

func mkdirPrivate(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return checkDir(dir)
}

// checkDir confirms dir is a directory and not a symbolic link. The
// container runtime resolves links in mount sources on the host, so a link
// in place of a directory the runner created would mount something else.
func checkDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("check %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory (mode %s)", dir, fi.Mode())
	}
	return nil
}

// mkdirReadable creates dir so that the task user can read it but not
// write it.
func mkdirReadable(dir string) error {
	if err := os.Mkdir(dir, 0o755); err != nil { // #nosec G301 -- the task user reads it through the "other" bits
		return fmt.Errorf("create %s: %w", dir, err)
	}
	// Mkdir is subject to the umask, which may hide the directory from the
	// task user.
	if err := os.Chmod(dir, 0o755); err != nil { // #nosec G302 -- as above
		return fmt.Errorf("chmod %s: %w", dir, err)
	}
	return nil
}

// mkdirWritable creates dir so that the task user (65532) can write it.
//
// The runner may run as root (chown to the task user), as the task user
// itself (the lerryn image runs as 65532, nothing to do), or as anyone else,
// typically a developer. In that last case the directory is made writable
// for everyone; its parent is private to the runner, so in practice only
// the container can reach it. Files the task creates in subdirectories then
// belong to 65532 and the runner may be unable to delete them; that only
// affects such development setups, and is logged when it happens.
func mkdirWritable(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return makeWritable(dir)
}

func makeWritable(dir string) error {
	switch os.Geteuid() {
	case 0:
		if err := os.Chown(dir, taskUID, taskGID); err != nil {
			return fmt.Errorf("chown %s to the task user: %w", dir, err)
		}
		return os.Chmod(dir, 0o700) // #nosec G302 -- a directory, owned by the task user
	case taskUID:
		return os.Chmod(dir, 0o700) // #nosec G302 -- a directory, owned by the task user
	default:
		return os.Chmod(dir, 0o777) // #nosec G302 -- the task user differs from the runner's; the parent is 0700
	}
}

// writeTaskFiles writes a task version's files under dir. Paths come from
// the server, which validates them; they are checked again here, and all
// writes go through an os.Root so a bad path cannot leave dir.
func writeTaskFiles(dir string, files []runproto.File) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open task dir: %w", err)
	}
	defer func() { _ = root.Close() }()
	for _, f := range files {
		if !validTaskPath(f.Path) {
			return fmt.Errorf("task file path %q is not a clean relative path", f.Path)
		}
		if d := path.Dir(f.Path); d != "." {
			if err := root.MkdirAll(d, 0o755); err != nil {
				return fmt.Errorf("create directory for %s: %w", f.Path, err)
			}
		}
		mode := fs.FileMode(0o644)
		if f.Executable {
			mode = 0o755
		}
		if err := writeFile(root, f.Path, f.Content, mode); err != nil {
			return err
		}
	}
	return fixDirModes(root)
}

// writeInput writes the run input to dir/body, readable but not
// writable by the task user.
func writeInput(dir string, content []byte) error {
	if err := mkdirReadable(dir); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open input dir: %w", err)
	}
	defer func() { _ = root.Close() }()
	return writeFile(root, inputFileName, content, 0o644)
}

// writeFile writes a file through root with exactly mode, whatever the
// umask.
func writeFile(root *os.Root, name string, content []byte, mode fs.FileMode) error {
	if err := root.WriteFile(name, content, mode); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := root.Chmod(name, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", name, err)
	}
	return nil
}

// fixDirModes makes every directory in root 0755, undoing the umask: the
// task user reads the tree through the "other" bits.
func fixDirModes(root *os.Root) error {
	return fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if err := root.Chmod(p, 0o755); err != nil {
			return fmt.Errorf("chmod %s: %w", p, err)
		}
		return nil
	})
}

func validTaskPath(p string) bool {
	return p != "." && fs.ValidPath(p) && !strings.ContainsAny(p, "\\\x00")
}

// removeAll deletes dir and reports what it could not delete.
func removeAll(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}
	return nil
}
