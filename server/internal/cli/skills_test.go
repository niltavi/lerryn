package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/skills"
)

func TestSkillsInstall(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	r := runWith(t, env, testSpec, "", "skills", "install", "-o", "json")
	if r.code != ExitOK {
		t.Fatalf("install = %d: %s", r.code, r.stderr)
	}
	var out struct {
		Directory string             `json:"directory"`
		Installed []skills.Installed `json:"installed"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatalf("%v: %s", err, r.stdout)
	}
	want := filepath.Join(home, ".claude", "skills")
	if out.Directory != want || len(out.Installed) != len(skills.Names) {
		t.Fatalf("install = %+v, want %d skills in %s", out, len(skills.Names), want)
	}
	if _, err := os.Stat(filepath.Join(want, "lerryn-automation", "SKILL.md")); err != nil {
		t.Fatal(err)
	}

	// Again, text output: the same skill is updated in place.
	r = runWith(t, env, testSpec, "", "skills", "install", "-o", "text", "--skill", "lerryn-troubleshoot")
	if r.code != ExitOK || !strings.Contains(r.stdout, "updated lerryn-troubleshoot") {
		t.Fatalf("reinstall = %d: %s %s", r.code, r.stdout, r.stderr)
	}
}

func TestSkillsInstallDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent", "skills")
	r := runWith(t, map[string]string{}, testSpec, "", "skills", "install", "--dir", dir, "--skill", "lerryn-operate", "-o", "text")
	if r.code != ExitOK || !strings.Contains(r.stdout, "installed lerryn-operate") {
		t.Fatalf("install = %d: %s %s", r.code, r.stdout, r.stderr)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "lerryn-operate" {
		t.Fatalf("dir has %v", entries)
	}
}

func TestSkillsInstallKeepsUserSkill(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "lerryn-automation"), 0o750); err != nil {
		t.Fatal(err)
	}
	r := runWith(t, map[string]string{}, testSpec, "", "skills", "install", "--dir", dir)
	if r.code != ExitFailure || !strings.Contains(r.stderr, "--force") {
		t.Fatalf("install over a user directory = %d: %s", r.code, r.stderr)
	}
	r = runWith(t, map[string]string{}, testSpec, "", "skills", "install", "--dir", dir, "--force")
	if r.code != ExitOK {
		t.Fatalf("install --force = %d: %s", r.code, r.stderr)
	}
}

func TestSkillsInstallUsage(t *testing.T) {
	for _, args := range [][]string{
		{"skills", "install", "--target", "vim"},
		{"skills", "install", "--dir", "x", "--project"},
		{"skills", "install", "extra"},
	} {
		if r := runWith(t, map[string]string{}, testSpec, "", args...); r.code != ExitUsage {
			t.Errorf("%v = %d, want %d: %s", args, r.code, ExitUsage, r.stderr)
		}
	}
	if r := runWith(t, map[string]string{}, testSpec, "", "skills", "install", "--dir", t.TempDir(), "--skill", "nope"); r.code != ExitFailure {
		t.Errorf("unknown skill = %d, want %d", r.code, ExitFailure)
	}
}

func TestSkillsList(t *testing.T) {
	r := runWith(t, map[string]string{}, testSpec, "", "skills", "list", "-o", "json")
	var out struct {
		Items []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"items"`
	}
	if r.code != ExitOK || json.Unmarshal([]byte(r.stdout), &out) != nil || len(out.Items) != len(skills.Names) {
		t.Fatalf("list = %d: %s", r.code, r.stdout)
	}
	for i, it := range out.Items {
		if it.Name != skills.Names[i] || it.Description == "" {
			t.Errorf("item %d = %+v", i, it)
		}
	}
	if r := runWith(t, map[string]string{}, testSpec, "", "skills", "list", "-o", "text"); !strings.Contains(r.stdout, "lerryn-credentials\n  ") {
		t.Errorf("text list = %q", r.stdout)
	}
}
