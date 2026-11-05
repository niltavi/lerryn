package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/niltavi/lerryn/runtimes/sdk"
)

// sdkPath returns the directory holding the sbx SDK of a runtime, writing it
// on first use. The directory is named after a hash of its content, so a
// runner upgrade with a changed SDK writes a new directory instead of
// changing files under a running task, and runtimes with different trees
// never share one.
func (r *Runner) sdkPath(language string) (string, error) {
	r.sdkMu.Lock()
	defer r.sdkMu.Unlock()
	if dir := r.sdkDirs[language]; dir != "" {
		return dir, nil
	}
	files, err := sdk.Files(language)
	if err != nil {
		return "", err
	}
	dir, err := materializeSDK(filepath.Join(r.opts.DataDir, sdkDirName), files)
	if err != nil {
		return "", fmt.Errorf("write sbx sdk: %w", err)
	}
	r.sdkDirs[language] = dir
	return dir, nil
}

func materializeSDK(parent string, files []sdk.File) (string, error) {
	if len(files) == 0 {
		return "", errors.New("the embedded sdk is empty")
	}
	h := sha256.New()
	for _, f := range files {
		_, _ = fmt.Fprintf(h, "%s\x00%t\x00%d\x00", f.Path, f.Executable, len(f.Data))
		h.Write(f.Data)
	}
	dir := filepath.Join(parent, hex.EncodeToString(h.Sum(nil))[:16])
	if checkDir(dir) == nil {
		return dir, nil
	}

	tmp, err := os.MkdirTemp(parent, tmpPrefix+"sdk-")
	if err != nil {
		return "", err
	}
	if err := writeSDK(tmp, files); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp)
		// Another runner process sharing the data dir won the race.
		if checkDir(dir) == nil {
			return dir, nil
		}
		return "", err
	}
	return dir, nil
}

func writeSDK(dir string, files []sdk.File) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, f := range files {
		if d := path.Dir(f.Path); d != "." {
			if err := root.MkdirAll(d, 0o755); err != nil {
				return err
			}
		}
		mode := fs.FileMode(0o644)
		if f.Executable {
			mode = 0o755
		}
		if err := writeFile(root, f.Path, f.Data, mode); err != nil {
			return err
		}
	}
	return fixDirModes(root)
}
