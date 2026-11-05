package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestConfigPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows uses the AppData directory")
	}
	tests := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{EnvConfig: "/x/cli.json", "XDG_CONFIG_HOME": "/xdg", "HOME": "/home/a"}, "/x/cli.json"},
		{map[string]string{"XDG_CONFIG_HOME": "/xdg", "HOME": "/home/a"}, "/xdg/lerryn/cli.json"},
		{map[string]string{"HOME": "/home/a"}, "/home/a/.config/lerryn/cli.json"},
	}
	for _, tt := range tests {
		got, err := configPath(envMap(tt.env))
		if err != nil || got != tt.want {
			t.Errorf("configPath(%v) = %q, %v; want %q", tt.env, got, err, tt.want)
		}
	}
}

func TestConfigRoundTripIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not apply")
	}
	dir := filepath.Join(t.TempDir(), "nested", "lerryn")
	path := filepath.Join(dir, "cli.json")
	want := Config{Server: "https://w.example.com", Token: "wl_pat_x", TokenID: "id", Username: "alice"}
	if err := saveConfig(path, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %04o, want 0600", info.Mode().Perm())
	}
	dinfo, _ := os.Stat(dir)
	if dinfo.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %04o, want 0700", dinfo.Mode().Perm())
	}
	got, err := loadConfig(path)
	if err != nil || got != want {
		t.Fatalf("loadConfig = %+v, %v", got, err)
	}

	// Overwriting keeps the mode, and leaves no temporary files behind.
	want.Token = "wl_pat_y"
	if err := saveConfig(path, want); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("config dir holds %d entries, want 1", len(entries))
	}

	// A file others can read is refused, not silently used.
	for _, mode := range []os.FileMode{0o640, 0o604, 0o666} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("mode %04o: err = %v", mode, err)
		}
	}
}

func TestLoadMissingConfig(t *testing.T) {
	c, err := loadConfig(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || c != (Config{}) {
		t.Fatalf("loadConfig = %+v, %v", c, err)
	}
}

func TestNormalizeServer(t *testing.T) {
	tests := map[string]string{
		"https://w.example.com":         "https://w.example.com",
		"https://w.example.com/":        "https://w.example.com",
		" http://localhost:8080 ":       "http://localhost:8080",
		"https://proxy.example/lerryn/": "https://proxy.example/lerryn",
	}
	for in, want := range tests {
		got, err := normalizeServer(in)
		if err != nil || got != want {
			t.Errorf("normalizeServer(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "w.example.com", "ftp://w", "https://", "https://u:p@w.example.com", "https://w?x=1", "https://w#f"} {
		if got, err := normalizeServer(bad); err == nil {
			t.Errorf("normalizeServer(%q) = %q, want an error", bad, got)
		}
	}
}

func TestExitCode(t *testing.T) {
	if ExitCode(nil) != ExitOK {
		t.Error("nil")
	}
	if ExitCode(os.ErrNotExist) != ExitFailure {
		t.Error("plain error")
	}
	if ExitCode(usageError(os.ErrInvalid)) != ExitUsage || ExitCode(authError(os.ErrInvalid)) != ExitAuth {
		t.Error("typed errors")
	}
	if Reported(usageError(os.ErrInvalid)) || !Reported(&ExitError{Code: 4, Reported: true}) {
		t.Error("Reported")
	}
}
