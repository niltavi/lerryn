package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

const minimalManifest = "runtime: {language: python, version: \"3.12\"}\nentrypoint: [python, main.py]\n"

func TestReadTaskDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "digest")
	write(t, filepath.Join(dir, "lerryn.yaml"), minimalManifest+"limits: {cpu: 0.5}\n", 0o600)
	write(t, filepath.Join(dir, "main.py"), "print(1)\n", 0o600)
	write(t, filepath.Join(dir, "lib", "util.py"), "x = 1\n", 0o600)
	write(t, filepath.Join(dir, "run.sh"), "#!/bin/sh\n", 0o700)
	write(t, filepath.Join(dir, ".git", "config"), "x", 0o600)
	write(t, filepath.Join(dir, ".venv", "lib", "a.py"), "x", 0o600)
	write(t, filepath.Join(dir, "lib", "__pycache__", "util.pyc"), "x", 0o600)
	write(t, filepath.Join(dir, "node_modules", "a", "index.js"), "x", 0o600)

	td, err := readTaskDir(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if td.name != "digest" {
		t.Errorf("name = %q, want the directory name", td.name)
	}
	if td.manifest["entrypoint"] == nil || td.manifest["limits"].(map[string]any)["cpu"] != 0.5 {
		t.Errorf("manifest = %v", td.manifest)
	}
	var got []string
	for _, f := range td.files {
		got = append(got, f.Path)
		if f.Path == "run.sh" && runtime.GOOS != "windows" && !f.Executable {
			t.Error("run.sh is not executable")
		}
		if f.Path == "main.py" && f.Executable {
			t.Error("main.py is executable")
		}
	}
	if s := strings.Join(got, " "); s != "lib/util.py main.py run.sh" {
		t.Errorf("files = %s", s)
	}
}

func TestReadTaskDirManifestName(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "custom.yaml"), "name: weekday-digest\n"+minimalManifest, 0o600)
	write(t, filepath.Join(dir, "main.py"), "print(1)\n", 0o600)
	td, err := readTaskDir(dir, filepath.Join(dir, "custom.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if td.name != "weekday-digest" {
		t.Errorf("name = %q", td.name)
	}
	if _, ok := td.manifest["name"]; ok {
		t.Error("name must not be sent as a manifest field")
	}
	if len(td.files) != 1 || td.files[0].Path != "main.py" {
		t.Errorf("files = %+v; the manifest itself must not be uploaded", td.files)
	}
}

func TestReadTaskDirErrors(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(dir string)
		wantErr string
	}{
		{"no manifest", func(dir string) { write(t, filepath.Join(dir, "main.py"), "x", 0o600) }, "no lerryn.yaml"},
		{"no files", func(dir string) { write(t, filepath.Join(dir, "lerryn.yaml"), minimalManifest, 0o600) }, "no task files"},
		{"binary file", func(dir string) {
			write(t, filepath.Join(dir, "lerryn.yaml"), minimalManifest, 0o600)
			write(t, filepath.Join(dir, "data.bin"), "\xff\xfe\x00", 0o600)
		}, "not UTF-8"},
		{"symlink", func(dir string) {
			write(t, filepath.Join(dir, "lerryn.yaml"), minimalManifest, 0o600)
			outside := filepath.Join(t.TempDir(), "secret.txt")
			write(t, outside, "secret", 0o600)
			if err := os.Symlink(outside, filepath.Join(dir, "link.txt")); err != nil {
				t.Skip("symlinks unavailable:", err)
			}
		}, "symbolic link"},
		{"bad name", func(dir string) {
			write(t, filepath.Join(dir, "lerryn.yaml"), "name: 3\n"+minimalManifest, 0o600)
			write(t, filepath.Join(dir, "main.py"), "x", 0o600)
		}, "name must be"},
		{"bad yaml", func(dir string) { write(t, filepath.Join(dir, "lerryn.yaml"), "runtime: [", 0o600) }, "lerryn.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(dir)
			_, err := readTaskDir(dir, "")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestRunParams(t *testing.T) {
	file := filepath.Join(t.TempDir(), "p.json")
	write(t, file, `{"a": 1, "b": "x"}`, 0o600)
	tests := []struct {
		pairs   []string
		json    string
		want    string
		wantErr bool
	}{
		{nil, "", "null", false},
		{[]string{"n=3", "s=hello", "b=true", "o={\"k\":1}"}, "", `{"b":true,"n":3,"o":{"k":1},"s":"hello"}`, false},
		{[]string{"b=override"}, "@" + file, `{"a":1,"b":"override"}`, false},
		{nil, `{"x":[1,2]}`, `{"x":[1,2]}`, false},
		{[]string{"novalue"}, "", "", true},
		{nil, "[1]", "", true},
		{nil, "null", "", true},
		{[]string{"x=1"}, "null", "", true},
	}
	for _, tt := range tests {
		got, err := runParams(tt.pairs, tt.json)
		if (err != nil) != tt.wantErr {
			t.Errorf("runParams(%v, %q) err = %v", tt.pairs, tt.json, err)
			continue
		}
		if tt.wantErr {
			continue
		}
		if s := mustJSON(t, got); s != tt.want {
			t.Errorf("runParams(%v, %q) = %s, want %s", tt.pairs, tt.json, s, tt.want)
		}
	}
}
