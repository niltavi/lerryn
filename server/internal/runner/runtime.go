package runner

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/niltavi/lerryn/runtimes/sdk"
	"github.com/niltavi/lerryn/server/internal/executor"
)

// Runtime languages, as in the manifest (tech design 7.3).
const (
	langPython = sdk.Python
	langNode   = sdk.Node
	langShell  = sdk.Shell
)

// Dependency files, and where a Node environment is mounted.
const (
	lockfileUV    = "uv.lock"
	pyprojectFile = "pyproject.toml"
	lockfileNPM   = "package-lock.json"
	packageJSON   = "package.json"
	// nodeModulesDir is node_modules in a Node environment. The run
	// container mounts it inside the task directory, where Node's module
	// resolution finds it from any task file, for import as for require.
	nodeModulesDir   = "node_modules"
	nodeModulesMount = taskMount + "/" + nodeModulesDir
	// sdkNodeModulesMount holds the Node SDK. It is a parent of /sbx/task,
	// so "@lerryn/sbx" resolves from task files whether or not the task
	// has its own node_modules.
	sdkNodeModulesMount = "/sbx/node_modules"
)

// runtimeDef is how this runner runs one runtime language.
type runtimeDef struct {
	language string
	// image is the configured runtime image; empty when this runner was
	// not given one, and runs of the language then fail.
	image string
	// lockfile is the lock file dependencies are installed from, empty
	// for a runtime without dependency installation.
	lockfile string
	// depFiles are the task's top-level files the installation reads,
	// the lockfile first. Only these influence an environment, which is
	// cached under a hash of them.
	depFiles []string
	// installCommand installs the dependencies into envMount.
	installCommand []string
	// depsInEnv places the dependency files in the environment directory
	// itself, where the installation runs, instead of a read-only copy
	// mounted at /sbx/task. npm ci installs next to package.json.
	depsInEnv bool
}

func (r *Runner) newRuntimes() map[string]runtimeDef {
	return map[string]runtimeDef{
		langPython: {
			language: langPython,
			image:    r.opts.PythonImage,
			lockfile: lockfileUV,
			depFiles: []string{lockfileUV, pyprojectFile},
			// Installs the locked dependencies, but not the task itself,
			// into UV_PROJECT_ENVIRONMENT.
			installCommand: []string{"uv", "sync", "--frozen", "--no-install-project", "--no-dev", "--project", taskMount},
		},
		langNode: {
			language:       langNode,
			image:          r.opts.NodeImage,
			lockfile:       lockfileNPM,
			depFiles:       []string{lockfileNPM, packageJSON},
			installCommand: []string{"npm", "ci", "--omit=dev", "--no-audit", "--no-fund"},
			depsInEnv:      true,
		},
		langShell: {
			language: langShell,
			image:    r.opts.ShellImage,
		},
	}
}

// installEnv is the environment of an installation container.
func (r *Runner) installEnv(rt runtimeDef) []string {
	env := []string{"PATH=" + basePath, "HOME=/tmp", "NO_COLOR=1"}
	switch rt.language {
	case langPython:
		env = append(env,
			"UV_PROJECT_ENVIRONMENT="+envMount,
			"UV_CACHE_DIR=/tmp/uv-cache",
			"UV_PYTHON_DOWNLOADS=never",
			"UV_LINK_MODE=copy",
		)
		if r.opts.PyPIMirror != "" {
			env = append(env, "UV_INDEX_URL="+r.opts.PyPIMirror)
		}
	case langNode:
		env = append(env,
			"npm_config_cache=/tmp/npm-cache",
			"npm_config_update_notifier=false",
			"npm_config_color=false",
			"npm_config_progress=false",
		)
		if r.opts.NPMMirror != "" {
			env = append(env, "npm_config_registry="+r.opts.NPMMirror)
		}
	}
	return env
}

// finishEnv completes a freshly installed environment before it is cached.
func finishEnv(rt runtimeDef, dir string) error {
	if rt.language != langNode {
		return nil
	}
	// npm ci creates no node_modules for a lockfile without packages, but
	// the run container mounts it.
	return mkdirExisting(filepath.Join(dir, nodeModulesDir))
}

// runtimeEnv and runtimeMounts are what a runtime adds to the run
// container: the SDK's module path and the dependency environment.
func runtimeEnv(rt runtimeDef, envDir string) (pathPrefix string, env []string) {
	switch rt.language {
	case langPython:
		env = []string{
			"PYTHONUNBUFFERED=1",
			"PYTHONDONTWRITEBYTECODE=1",
			"PYTHONPATH=" + sdkMount,
		}
		if envDir != "" {
			pathPrefix = envMount + "/bin:"
			env = append(env, "VIRTUAL_ENV="+envMount)
		}
	case langNode:
		env = []string{
			"npm_config_cache=/tmp/npm-cache",
			"npm_config_update_notifier=false",
		}
		if envDir != "" {
			pathPrefix = nodeModulesMount + "/.bin:"
		}
	}
	return pathPrefix, env
}

func runtimeMounts(rt runtimeDef, sdkDir, envDir string) []executor.Mount {
	var mounts []executor.Mount
	switch rt.language {
	case langPython:
		if envDir != "" {
			mounts = append(mounts, executor.Mount{Source: envDir, Target: envMount, ReadOnly: true})
		}
	case langNode:
		mounts = append(mounts, executor.Mount{Source: filepath.Join(sdkDir, nodeModulesDir), Target: sdkNodeModulesMount, ReadOnly: true})
		if envDir != "" {
			mounts = append(mounts, executor.Mount{Source: filepath.Join(envDir, nodeModulesDir), Target: nodeModulesMount, ReadOnly: true})
		}
	}
	return mounts
}

// prepareTaskDir adds what the runtime needs in the written task directory.
func prepareTaskDir(rt runtimeDef, taskDir, envDir string) error {
	if rt.language == langNode && envDir != "" {
		// The mount point must exist: the task directory is mounted
		// read-only, so the container runtime cannot create it.
		return mkdirExisting(filepath.Join(taskDir, nodeModulesDir))
	}
	return nil
}

// mkdirExisting creates a directory readable by the task user, or accepts
// one that is already there.
func mkdirExisting(dir string) error {
	err := mkdirReadable(dir)
	if err == nil || !errors.Is(err, fs.ErrExist) {
		return err
	}
	if err := checkDir(dir); err != nil {
		return err
	}
	err = os.Chmod(dir, 0o755) // #nosec G302 -- the task user reads it through the "other" bits
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	// A directory the install container created belongs to the task user,
	// and a runner running as someone else (see mkdirWritable) cannot
	// change its mode. It is fine as long as the task user can already
	// read it.
	if info, serr := os.Stat(dir); serr == nil && info.Mode().Perm()&0o005 == 0o005 {
		return nil
	}
	return err
}

// checkRuntime reports why a run of the manifest's runtime cannot run here,
// or "" when it can.
func checkRuntime(rt runtimeDef, ok bool, language, lockfile string) string {
	switch {
	case !ok:
		return fmt.Sprintf("runtime %q is not supported by this runner", language)
	case rt.image == "":
		return fmt.Sprintf("this runner has no image configured for the %s runtime", language)
	case lockfile != "" && lockfile != rt.lockfile:
		if rt.lockfile == "" {
			return fmt.Sprintf("the %s runtime does not install dependencies; remove runtime.lockfile", language)
		}
		return fmt.Sprintf("lockfile %q is not supported for the %s runtime; use %s", lockfile, language, rt.lockfile)
	}
	return ""
}
