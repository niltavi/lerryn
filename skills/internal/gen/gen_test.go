// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/niltavi/lerryn/api"
	"github.com/niltavi/lerryn/skills"
)

// The generated references are committed; make check does not run
// generate-check, so this test catches a contract change without
// make generate.
func TestGeneratedFilesAreCurrent(t *testing.T) {
	files, err := Generate(api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		have, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(f.Path)))
		if err != nil {
			t.Fatal(err)
		}
		if string(have) != string(f.Data) {
			t.Errorf("skills/%s is out of date; run make generate", f.Path)
		}
	}
}

// contractNames collects every name an agent can meet in the contract: MCP
// tool names, operationIds, parameter names in their MCP form, schema
// property names, enum and const values.
func contractNames(t *testing.T) (snake, ops map[string]bool) {
	t.Helper()
	var root yaml.Node
	if err := yaml.Unmarshal(api.Spec, &root); err != nil {
		t.Fatal(err)
	}
	snake = map[string]bool{"api_describe": true, "api_call": true}
	ops = map[string]bool{}
	var walk func(n *yaml.Node, parentKey string)
	walk = func(n *yaml.Node, parentKey string) {
		switch n.Kind {
		case yaml.MappingNode:
			var name, in string
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i].Value, n.Content[i+1]
				switch {
				case parentKey == "properties":
					snake[k] = true
				case k == "operationId":
					ops[v.Value] = true
				case k == "tool" || k == "const":
					snake[v.Value] = true
				case k == "enum":
					for _, e := range v.Content {
						snake[e.Value] = true
					}
				case k == "name":
					name = v.Value
				case k == "in":
					in = v.Value
				}
				walk(v, k)
			}
			if name != "" && in != "" {
				snake[snakeCase(name)] = true
			}
		case yaml.SequenceNode, yaml.DocumentNode:
			for _, c := range n.Content {
				walk(c, parentKey)
			}
		}
	}
	walk(&root, "")
	return snake, ops
}

func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == '-':
			b.WriteByte('_')
		case r >= 'A' && r <= 'Z':
			if i > 0 && s[i-1] >= 'a' && s[i-1] <= 'z' {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

var (
	codeSpan  = regexp.MustCompile("`([^`\n]+)`")
	snakeTok  = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)+$`)
	opIDTok   = regexp.MustCompile(`^(list|get|create|update|delete|set|start|cancel|rerun|read|stream|download|apply|archive|diff|validate|grant|revoke|rotate|register|remove|admin|search|test)[A-Z][A-Za-z]*$`)
	notInSpec = map[string]bool{
		// Words the skills use that are not contract names.
		"node_modules": true, // npm's directory
	}
)

// The hand-written skills name tools, operations, fields, parameters and
// codes in code spans. Every such name must exist in the contract, so a
// rename in api/ or a typo in a skill fails here instead of misleading an
// agent.
func TestSkillNamesExistInContract(t *testing.T) {
	snake, ops := contractNames(t)
	err := fs.WalkDir(skills.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		data, err := fs.ReadFile(skills.FS(), p)
		if err != nil {
			return err
		}
		// Generated files come from the contract; lerryn-operate names
		// commands and metrics from docs/zh-CN/deploy.md, not the contract.
		if strings.HasPrefix(string(data), "<!-- Code generated") || strings.HasPrefix(p, "lerryn-operate/") {
			return nil
		}
		for _, m := range codeSpan.FindAllStringSubmatch(string(data), -1) {
			tok := strings.TrimSuffix(m[1], "()")
			switch {
			case notInSpec[tok]:
			case snakeTok.MatchString(tok) && !snake[tok]:
				t.Errorf("%s: `%s` is not a tool, field, parameter or code in the contract", p, tok)
			case opIDTok.MatchString(tok) && !ops[tok]:
				t.Errorf("%s: `%s` is not an operationId in the contract", p, tok)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
