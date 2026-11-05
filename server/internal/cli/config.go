package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Environment variables the CLI reads. Flags take precedence over them, and
// they take precedence over the config file.
const (
	EnvConfig = "LERRYN_CLI_CONFIG"
	EnvServer = "LERRYN_SERVER"
	EnvToken  = "LERRYN_TOKEN" // #nosec G101 -- an environment variable name, not a credential
)

// Config is what `lerryn login` stores: the server and an access token for
// it. The file holds a credential, so it is written with mode 0600 and
// refused when other users can read it.
type Config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
	// TokenID is set when login created the token, so logout can revoke it.
	TokenID  string `json:"token_id,omitempty"`
	Username string `json:"username,omitempty"`
}

// configPath returns where the config file lives: $LERRYN_CLI_CONFIG, else
// $XDG_CONFIG_HOME/lerryn/cli.json, else ~/.config/lerryn/cli.json. Windows
// uses the roaming AppData directory instead of ~/.config.
func configPath(getenv func(string) string) (string, error) {
	if p := getenv(EnvConfig); p != "" {
		return p, nil
	}
	if d := getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "lerryn", "cli.json"), nil
	}
	if runtime.GOOS == "windows" {
		d, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("locate config directory: %w", err)
		}
		return filepath.Join(d, "lerryn", "cli.json"), nil
	}
	home := getenv("HOME")
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", fmt.Errorf("locate home directory: %w", err)
		}
	}
	return filepath.Join(home, ".config", "lerryn", "cli.json"), nil
}

// loadConfig reads the config file. A missing file is an empty config.
func loadConfig(path string) (Config, error) {
	f, err := os.Open(path) // #nosec G304 -- the path is the user's own config location
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := checkPrivate(f, path); err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.NewDecoder(f).Decode(&c); err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	return c, nil
}

// checkPrivate refuses a config file that other users can read or write,
// as ssh does for private keys: the token in it acts as the user.
func checkPrivate(f *os.File, path string) error {
	if runtime.GOOS == "windows" {
		return nil // permission bits do not describe Windows ACLs
	}
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("config file %s is accessible by other users (mode %04o); run chmod 600 %s",
			path, info.Mode().Perm(), path)
	}
	return nil
}

// saveConfig writes the config atomically with mode 0600, creating the
// directory with mode 0700.
func saveConfig(path string, c Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	// CreateTemp creates the file with mode 0600, so the token is never
	// readable by others, not even between write and rename.
	tmp, err := os.CreateTemp(dir, ".cli-*.json")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		_ = tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// normalizeServer checks a server URL and returns it without a trailing
// slash, for example https://lerryn.example.com or https://host/lerryn.
func normalizeServer(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("server URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("server URL %q: want an http or https URL, such as https://lerryn.example.com", raw)
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("server URL %q: want scheme, host and an optional path only", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

// sameServer compares two normalized server URLs.
func sameServer(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(a, "/"), strings.TrimRight(b, "/"))
}
