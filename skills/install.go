// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// MarkerFile is written into every installed skill directory. It records
// which version installed the skill, and lets a later install replace the
// directory without touching skills the user wrote under the same name.
const MarkerFile = ".lerryn-skill"

// Marker is the content of MarkerFile.
type Marker struct {
	Skill   string `json:"skill"`
	Version string `json:"version"`
}

// InstallOptions controls Install.
type InstallOptions struct {
	// Names selects skills; all of Names when empty.
	Names []string
	// Version is recorded in each skill's marker file.
	Version string
	// Force replaces a same-named directory that was not installed by
	// Install, such as a skill the user wrote.
	Force bool
}

// Installed reports one installed skill.
type Installed struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Replaced is true when an earlier copy was replaced.
	Replaced bool `json:"replaced"`
}

// ErrNotManaged is returned when a skill directory exists that Install did
// not create and Force is not set.
var ErrNotManaged = errors.New("directory exists and was not installed by lerryn; use --force to replace it")

// Install writes the selected skills into dir, one subdirectory per skill.
// Each skill is written to a temporary directory next to its destination
// and renamed into place, so a failed install leaves the previous copy, and
// files dropped from a skill do not linger. Other entries of dir are not
// touched.
func Install(dir string, opts InstallOptions) ([]Installed, error) {
	names := opts.Names
	if len(names) == 0 {
		names = Names
	}
	for _, n := range names {
		if !slices.Contains(Names, n) {
			return nil, fmt.Errorf("unknown skill %q", n)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- skills are not secret; clients read them
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	// Check every destination first, so one unmanaged directory does not
	// leave the set half installed.
	replace := map[string]bool{}
	for _, n := range names {
		dest := filepath.Join(dir, n)
		managed, exists, err := inspect(dest)
		if err != nil {
			return nil, err
		}
		if exists && !managed && !opts.Force {
			return nil, fmt.Errorf("%s: %w", dest, ErrNotManaged)
		}
		replace[n] = exists
	}
	out := make([]Installed, 0, len(names))
	for _, n := range names {
		dest := filepath.Join(dir, n)
		if err := installOne(dir, n, opts.Version); err != nil {
			return out, err
		}
		out = append(out, Installed{Name: n, Path: dest, Replaced: replace[n]})
	}
	return out, nil
}

// inspect reports whether dest exists and, if so, whether Install wrote it.
// A symbolic link counts as not managed; replacing it removes the link,
// never its target.
func inspect(dest string) (managed, exists bool, err error) {
	fi, err := os.Lstat(dest)
	if errors.Is(err, fs.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("inspect %s: %w", dest, err)
	}
	if !fi.IsDir() {
		return false, true, nil
	}
	mi, err := os.Lstat(filepath.Join(dest, MarkerFile))
	if err != nil {
		return false, true, nil //nolint:nilerr // no marker: not ours
	}
	return mi.Mode().IsRegular(), true, nil
}

func installOne(dir, name, version string) (err error) {
	tmp, err := os.MkdirTemp(dir, "."+name+".tmp-")
	if err != nil {
		return fmt.Errorf("install %s: %w", name, err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(tmp)
		}
	}()
	// MkdirTemp creates 0700; clients running as the same user read it,
	// but keep the usual directory mode.
	if err := os.Chmod(tmp, 0o755); err != nil { // #nosec G302 -- skills are not secret
		return fmt.Errorf("install %s: %w", name, err)
	}
	sub, err := fs.Sub(files, name)
	if err != nil {
		return fmt.Errorf("install %s: %w", name, err)
	}
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == "." {
			return err
		}
		target := filepath.Join(tmp, filepath.FromSlash(p))
		if d.IsDir() {
			return os.Mkdir(target, 0o755) // #nosec G301 -- skills are not secret
		}
		data, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644) // #nosec G306 -- skills are not secret
	})
	if err != nil {
		return fmt.Errorf("install %s: %w", name, err)
	}
	marker, _ := json.Marshal(Marker{Skill: name, Version: version})
	if err := os.WriteFile(filepath.Join(tmp, MarkerFile), append(marker, '\n'), 0o644); err != nil { // #nosec G306 -- not secret
		return fmt.Errorf("install %s: %w", name, err)
	}

	dest := filepath.Join(dir, name)
	var old string
	if _, err := os.Lstat(dest); err == nil {
		old = tmp + "-old" // unique, since tmp is
		if err := os.Rename(dest, old); err != nil {
			return fmt.Errorf("install %s: move the previous copy aside: %w", name, err)
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		if old != "" {
			_ = os.Rename(old, dest)
		}
		return fmt.Errorf("install %s: %w", name, err)
	}
	if old != "" {
		if err := os.RemoveAll(old); err != nil {
			return fmt.Errorf("install %s: remove the previous copy %s: %w", name, old, err)
		}
	}
	return nil
}
