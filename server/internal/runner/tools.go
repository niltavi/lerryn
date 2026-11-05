package runner

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/niltavi/lerryn/server/internal/executor"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

// Script tools are mounted read-only under toolsMount, one directory per
// tool (tech design 7.8). The runner writes them under toolsDirName in
// the run directory.
const (
	toolsDirName = "tools"
	toolsMount   = "/sbx/tools"
)

// toolEnvName is the environment name stem of a tool: LERRYN_TOOL_ and
// the name uppercased with '-' as '_'.
func toolEnvName(name string) string {
	return "LERRYN_TOOL_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// toolEnv returns the environment entries that hand a run its tools
// (tech design 7.8): LERRYN_TOOL_<NAME>_URL for an HTTP or MCP tool,
// LERRYN_TOOL_<NAME>_DIR for a script tool, and LERRYN_TOOLS with all of
// them as a JSON array of {name, kind, url | dir and tree_hash}. Nothing
// without tools.
func toolEnv(tools []runproto.Tool) []string {
	if len(tools) == 0 {
		return nil
	}
	type entry struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		URL      string `json:"url,omitempty"`
		Dir      string `json:"dir,omitempty"`
		TreeHash string `json:"tree_hash,omitempty"`
	}
	list := make([]entry, len(tools))
	env := make([]string, 0, len(tools)+1)
	for i, t := range tools {
		list[i] = entry{Name: t.Name, Kind: t.Kind, URL: t.URL}
		if t.Kind == runproto.ToolScript {
			list[i].Dir, list[i].TreeHash = toolsMount+"/"+t.Name, t.TreeHash
			env = append(env, toolEnvName(t.Name)+"_DIR="+list[i].Dir)
			continue
		}
		env = append(env, toolEnvName(t.Name)+"_URL="+oneLine(t.URL))
	}
	// Marshalling strings cannot fail.
	b, _ := json.Marshal(list)
	return append(env, "LERRYN_TOOLS="+string(b))
}

// oneLine drops control characters, so a value cannot add environment
// entries.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// writeScriptTools writes the files of the run's script tools under
// dir/tools/<name>, readable by the task user, and says in the run's log
// which tree each one is, so the run records the code it mounted.
func (x *runExec) writeScriptTools(tools []runproto.Tool) error {
	var scripts []runproto.Tool
	for _, t := range tools {
		if t.Kind == runproto.ToolScript {
			scripts = append(scripts, t)
		}
	}
	if len(scripts) == 0 {
		return nil
	}
	parent := filepath.Join(x.dir, toolsDirName)
	if err := mkdirReadable(parent); err != nil {
		return err
	}
	for _, t := range scripts {
		// The server validates tool names like this; a name that is not
		// one path element must not reach the file system.
		if !validTaskPath(t.Name) || strings.Contains(t.Name, "/") {
			return fmt.Errorf("tool name %q is not a single path element", t.Name)
		}
		dir := filepath.Join(parent, t.Name)
		if err := mkdirReadable(dir); err != nil {
			return err
		}
		if err := writeTaskFiles(dir, t.Files); err != nil {
			return fmt.Errorf("tool %s: %w", t.Name, err)
		}
		x.logs.system("tool %s: script tool %s, %d files, mounted at %s/%s", t.Name, shortHash(t.TreeHash), len(t.Files), toolsMount, t.Name)
	}
	return nil
}

// scriptToolMounts mounts each script tool's directory read-only.
func (x *runExec) scriptToolMounts(tools []runproto.Tool) []executor.Mount {
	var out []executor.Mount
	for _, t := range tools {
		if t.Kind == runproto.ToolScript {
			out = append(out, executor.Mount{
				Source: filepath.Join(x.dir, toolsDirName, t.Name), Target: toolsMount + "/" + t.Name, ReadOnly: true,
			})
		}
	}
	return out
}

func shortHash(h string) string {
	if len(h) > 12 {
		return "sha256:" + h[:12]
	}
	if h == "" {
		return "(no tree hash)"
	}
	return "sha256:" + h
}
