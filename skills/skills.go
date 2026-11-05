// SPDX-License-Identifier: Apache-2.0

// Package skills embeds the Lerryn Agent Skills (tech design 11), so the
// server and the lerryn CLI ship the skills that match their contract, and
// installs them into a client's skills directory.
//
// Each skill is a directory with a SKILL.md, whose YAML front matter holds
// name and description, and optional references/ files the agent reads on
// demand. Some references are generated from the API contract by
// ./internal/gen; run `make generate` after changing api/.
package skills

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:generate go run ./internal/gen

// Names lists the skills in the order they are presented.
var Names = []string{"lerryn-automation", "lerryn-credentials", "lerryn-troubleshoot", "lerryn-operate"}

//go:embed lerryn-automation lerryn-credentials lerryn-troubleshoot lerryn-operate
var files embed.FS

// FS returns the embedded skill tree: one directory per skill.
func FS() fs.FS { return files }

// Skill is one embedded skill.
type Skill struct {
	// Name and Description come from the SKILL.md front matter.
	Name        string
	Description string
	License     string
	// Files are the skill's files, slash-separated and relative to the
	// skill directory, sorted; SKILL.md is among them.
	Files []string
}

// frontMatter is the part of SKILL.md this package reads. Unknown keys are
// allowed: clients define more (allowed-tools, metadata).
type frontMatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	License     string `yaml:"license"`
}

// List returns the embedded skills in the order of Names.
func List() ([]Skill, error) {
	out := make([]Skill, 0, len(Names))
	for _, name := range Names {
		s, err := load(name)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// Get returns one embedded skill.
func Get(name string) (Skill, error) {
	if !slices.Contains(Names, name) {
		return Skill{}, fmt.Errorf("unknown skill %q (have %s)", name, strings.Join(Names, ", "))
	}
	return load(name)
}

func load(name string) (Skill, error) {
	data, err := files.ReadFile(name + "/SKILL.md")
	if err != nil {
		return Skill{}, fmt.Errorf("read skill %s: %w", name, err)
	}
	fm, err := ParseFrontMatter(data)
	if err != nil {
		return Skill{}, fmt.Errorf("skill %s: %w", name, err)
	}
	if fm.Name != name {
		return Skill{}, fmt.Errorf("skill directory %s declares name %q", name, fm.Name)
	}
	s := Skill{Name: fm.Name, Description: fm.Description, License: fm.License}
	err = fs.WalkDir(files, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		s.Files = append(s.Files, strings.TrimPrefix(p, name+"/"))
		return nil
	})
	if err != nil {
		return Skill{}, fmt.Errorf("list skill %s: %w", name, err)
	}
	slices.Sort(s.Files)
	return s, nil
}

// ReadFile returns a file of a skill, by a path relative to the skill.
func ReadFile(skill, file string) ([]byte, error) {
	if !slices.Contains(Names, skill) || !fs.ValidPath(file) {
		return nil, fs.ErrNotExist
	}
	return files.ReadFile(path.Join(skill, file))
}

// FrontMatter is the metadata block at the top of a SKILL.md.
type FrontMatter struct {
	Name        string
	Description string
	License     string
}

var errNoFrontMatter = errors.New("SKILL.md does not start with a --- front matter block")

// ParseFrontMatter reads the YAML front matter of a SKILL.md.
func ParseFrontMatter(data []byte) (FrontMatter, error) {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	rest, ok := bytes.CutPrefix(data, []byte("---\n"))
	if !ok {
		return FrontMatter{}, errNoFrontMatter
	}
	block, _, ok := bytes.Cut(rest, []byte("\n---\n"))
	if !ok {
		return FrontMatter{}, errNoFrontMatter
	}
	var fm frontMatter
	if err := yaml.Unmarshal(block, &fm); err != nil {
		return FrontMatter{}, fmt.Errorf("parse front matter: %w", err)
	}
	return FrontMatter(fm), nil
}
