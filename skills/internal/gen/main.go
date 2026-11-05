// SPDX-License-Identifier: Apache-2.0

// Command gen writes the skill references that come from the API contract
// (tech design 11.5): the manifest field reference, the MCP tool and CLI
// table and the error code table. It runs from skills/ under go generate.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/niltavi/lerryn/api"
)

func main() {
	out := flag.String("out", ".", "skills directory to write into")
	flag.Parse()
	files, err := Generate(api.Spec)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
	for _, f := range files {
		p := filepath.Join(*out, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { // #nosec G301 -- generated docs
			fmt.Fprintln(os.Stderr, "gen:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil { // #nosec G306 -- generated docs
			fmt.Fprintln(os.Stderr, "gen:", err)
			os.Exit(1)
		}
	}
}
