// SPDX-License-Identifier: Apache-2.0

// Package sdk embeds the sbx helpers that run inside task containers, so the
// runner can provide them without a custom runtime image.
//
// Each runtime gets one directory tree, mounted at /sbx/sdk:
//
//   - python: bin/sbx is the Python command, and the directory itself goes
//     on PYTHONPATH so tasks can import sbx.
//   - node: bin/sbx is the POSIX sh command, and node_modules/@lerryn/sbx
//     is the Node module; the runner mounts node_modules at /sbx/node_modules
//     so that tasks under /sbx/task resolve it.
//   - shell: bin/sbx is the POSIX sh command.
//
// Every tree also has bin/sbx-exec, the container entrypoint that installs
// the run's credentials from stdin and then execs the task (tech design
// 12.5). It is POSIX sh for all three runtimes.
package sdk

import (
	"embed"
	"fmt"
	"path"
)

// Runtime languages, as in the manifest's runtime.language.
const (
	Python = "python"
	Node   = "node"
	Shell  = "shell"
)

// The files are listed one by one: a directory pattern would skip
// __init__.py and __main__.py, and would pick up stray __pycache__
// directories from local test runs.
//
//go:embed python/bin/sbx python/sbx/__init__.py python/sbx/__main__.py python/sbx/cli.py
//go:embed sh/sbx sh/sbx-exec
//go:embed node/package.json node/index.js node/index.d.ts node/cli.js
var files embed.FS

// File is one file of an SDK tree.
type File struct {
	// Path is slash-separated and relative to the tree's root.
	Path string
	Data []byte
	// Executable files are written with mode 0755; embedded files carry
	// no mode.
	Executable bool
}

// layouts maps each runtime's tree paths to embedded files.
var layouts = map[string][][2]string{
	Python: {
		{"bin/sbx", "python/bin/sbx"},
		{"sbx/__init__.py", "python/sbx/__init__.py"},
		{"sbx/__main__.py", "python/sbx/__main__.py"},
		{"sbx/cli.py", "python/sbx/cli.py"},
		{"bin/sbx-exec", "sh/sbx-exec"},
	},
	Node: {
		{"bin/sbx", "sh/sbx"},
		{"node_modules/@lerryn/sbx/package.json", "node/package.json"},
		{"node_modules/@lerryn/sbx/index.js", "node/index.js"},
		{"node_modules/@lerryn/sbx/index.d.ts", "node/index.d.ts"},
		{"node_modules/@lerryn/sbx/cli.js", "node/cli.js"},
		{"bin/sbx-exec", "sh/sbx-exec"},
	},
	Shell: {
		{"bin/sbx", "sh/sbx"},
		{"bin/sbx-exec", "sh/sbx-exec"},
	},
}

// Files returns the SDK tree of a runtime language.
func Files(language string) ([]File, error) {
	layout, ok := layouts[language]
	if !ok {
		return nil, fmt.Errorf("no sbx sdk for runtime %q", language)
	}
	out := make([]File, 0, len(layout))
	for _, l := range layout {
		data, err := files.ReadFile(l[1])
		if err != nil {
			// Every source is embedded above; this cannot fail.
			panic(err)
		}
		out = append(out, File{Path: l[0], Data: data, Executable: path.Dir(l[0]) == "bin"})
	}
	return out, nil
}
