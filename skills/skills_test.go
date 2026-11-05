// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// Agent Skills rules for the front matter: a lowercase hyphenated name
// that matches the directory, and a description clients can show.
var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func TestSkillsFrontMatter(t *testing.T) {
	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != len(Names) {
		t.Fatalf("List = %d skills, want %d", len(list), len(Names))
	}
	for _, s := range list {
		t.Run(s.Name, func(t *testing.T) {
			if !skillName.MatchString(s.Name) || len(s.Name) > 64 {
				t.Errorf("name %q breaks the Agent Skills naming rule", s.Name)
			}
			if n := utf8.RuneCountInString(s.Description); n < 40 || n > 1024 {
				t.Errorf("description is %d characters, want 40 to 1024", n)
			}
			if strings.ContainsAny(s.Description, "<>") {
				t.Errorf("description contains angle brackets")
			}
			if s.License != "Apache-2.0" {
				t.Errorf("license = %q, want Apache-2.0 (skills/ is Apache-2.0)", s.License)
			}
			if !slices.Contains(s.Files, "SKILL.md") {
				t.Errorf("files = %v, no SKILL.md", s.Files)
			}
		})
	}
}

var mdLink = regexp.MustCompile(`\]\(([^)#]+)(#[^)]*)?\)`)

// Every relative link in a skill points at a file of the same skill, and
// every reference file is linked from somewhere, so agents can find it.
func TestSkillLinks(t *testing.T) {
	for _, name := range Names {
		s, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		linked := map[string]bool{"SKILL.md": true}
		for _, f := range s.Files {
			if !strings.HasSuffix(f, ".md") {
				continue
			}
			data, err := ReadFile(name, f)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range mdLink.FindAllStringSubmatch(string(data), -1) {
				target := m[1]
				if strings.Contains(target, "://") {
					continue
				}
				p := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(f), target)))
				if !slices.Contains(s.Files, p) {
					t.Errorf("%s/%s links to %s, which is not in the skill", name, f, target)
				}
				linked[p] = true
			}
		}
		for _, f := range s.Files {
			if !linked[f] {
				t.Errorf("%s/%s is not linked from any file of the skill", name, f)
			}
		}
	}
}

// The writing rules for user-facing Chinese text (AGENTS.md): no em dash.
func TestSkillStyle(t *testing.T) {
	err := fs.WalkDir(FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(FS(), p)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "——") {
			t.Errorf("%s uses an em dash", p)
		}
		if !utf8.Valid(data) {
			t.Errorf("%s is not UTF-8", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestInstall(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	// A skill the user wrote must survive, and so must an unrelated file.
	other := filepath.Join(dir, "my-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(other), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Install(dir, InstallOptions{Version: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(Names) {
		t.Fatalf("installed %d skills, want %d", len(got), len(Names))
	}
	for _, in := range got {
		if in.Replaced {
			t.Errorf("%s reported as replaced on a fresh install", in.Name)
		}
		s, _ := Get(in.Name)
		for _, f := range s.Files {
			want, _ := ReadFile(in.Name, f)
			have, err := os.ReadFile(filepath.Join(in.Path, filepath.FromSlash(f)))
			if err != nil || string(have) != string(want) {
				t.Errorf("%s/%s not installed: %v", in.Name, f, err)
			}
		}
		var m Marker
		data, err := os.ReadFile(filepath.Join(in.Path, MarkerFile))
		if err != nil || json.Unmarshal(data, &m) != nil || m.Skill != in.Name || m.Version != "1.2.3" {
			t.Errorf("%s marker = %s, %v", in.Name, data, err)
		}
	}

	// A stale file from an older version disappears on reinstall.
	stale := filepath.Join(dir, "lerryn-automation", "references", "old.md")
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = Install(dir, InstallOptions{Names: []string{"lerryn-automation"}, Version: "1.2.4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Replaced {
		t.Fatalf("reinstall = %+v", got)
	}
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stale file survived the reinstall: %v", err)
	}
	if data, _ := os.ReadFile(other); string(data) != "mine" { // #nosec G304 -- test temp dir
		t.Errorf("unrelated skill changed: %q", data)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temporary entry %s left behind", e.Name())
		}
	}
}

func TestInstallRefusesUnmanaged(t *testing.T) {
	dir := t.TempDir()
	mine := filepath.Join(dir, "lerryn-operate")
	if err := os.MkdirAll(mine, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mine, "SKILL.md"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Install(dir, InstallOptions{})
	if !errors.Is(err, ErrNotManaged) {
		t.Fatalf("Install over a user directory = %v, want ErrNotManaged", err)
	}
	// Nothing was installed: the check runs before any write.
	if _, err := os.Stat(filepath.Join(dir, "lerryn-automation")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("other skills were installed before the refusal: %v", err)
	}
	if _, err := Install(dir, InstallOptions{Force: true}); err != nil {
		t.Fatalf("Install with Force: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(mine, "SKILL.md")); string(data) == "mine" { // #nosec G304 -- test temp dir
		t.Errorf("Force did not replace the directory")
	}
}

func TestInstallReplacesSymlinkNotTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "lerryn-operate")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := Install(dir, InstallOptions{Names: []string{"lerryn-operate"}}); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("Install over a symlink = %v, want ErrNotManaged", err)
	}
	if _, err := Install(dir, InstallOptions{Names: []string{"lerryn-operate"}, Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "keep")); err != nil {
		t.Errorf("the link target was modified: %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(dir, "lerryn-operate")); err != nil || !fi.IsDir() {
		t.Errorf("lerryn-operate is not a directory after install: %v", err)
	}
}

func TestInstallUnknownSkill(t *testing.T) {
	if _, err := Install(t.TempDir(), InstallOptions{Names: []string{"nope"}}); err == nil {
		t.Fatal("Install accepted an unknown skill")
	}
}

func TestParseFrontMatter(t *testing.T) {
	tests := []struct {
		in      string
		want    FrontMatter
		wantErr bool
	}{
		{in: "---\nname: a\ndescription: b\n---\nbody", want: FrontMatter{Name: "a", Description: "b"}},
		{in: "---\r\nname: a\r\ndescription: b\r\n---\r\n", want: FrontMatter{Name: "a", Description: "b"}},
		{in: "---\nname: a\nmetadata: {x: 1}\n---\n", want: FrontMatter{Name: "a"}},
		{in: "# no front matter", wantErr: true},
		{in: "---\nname: a\n", wantErr: true},
		{in: "---\nname: [\n---\n", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseFrontMatter([]byte(tt.in))
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseFrontMatter(%q) = %+v, %v", tt.in, got, err)
		}
	}
}
