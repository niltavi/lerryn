package config

import (
	"strings"
	"testing"
)

func TestValidateTrustedProxies(t *testing.T) {
	tests := []struct {
		name    string
		proxies []string
		wantErr bool
	}{
		{"none", nil, false},
		{"ipv4 and ipv6 networks", []string{"172.30.10.0/24", " 10.0.0.0/8 ", "fd00::/8"}, false},
		{"bare address", []string{"10.0.0.1"}, true},
		{"not an address", []string{"caddy"}, true},
		{"prefix too long", []string{"10.0.0.0/33"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Server.TrustedProxies = tt.proxies
			err := cfg.Validate()
			if got := err != nil && strings.Contains(err.Error(), "server.trusted_proxies"); got != tt.wantErr {
				t.Fatalf("error = %v, want an error about server.trusted_proxies: %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadTrustedProxiesFromEnv(t *testing.T) {
	t.Setenv("LERRYN_SERVER_TRUSTED_PROXIES", "172.30.10.0/24,not-a-cidr")
	_, err := Load("")
	if err == nil || !strings.Contains(err.Error(), `"not-a-cidr"`) {
		t.Fatalf("Load: err = %v, want the invalid network rejected", err)
	}
}

func TestValidateRunnerDataDir(t *testing.T) {
	tests := []struct {
		dir     string
		wantErr bool
	}{
		{"/var/lib/lerryn/runner", false},
		{"/srv/lerryn", false},
		{"/home/runner/data", false},
		{"/", true},
		{"/etc", true},
		{"/usr/", true},
		{"/var/run", true},
		{"/run", true},
		{"/var/lib", true},
		{"/proc", true},
		{"/proc/1/root", true},
		{"/sys/fs", true},
		{"/dev/shm", true},
		{"/var/../etc", true},
	}
	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			cfg := Default()
			cfg.Runner.DataDir = tt.dir
			err := cfg.Validate()
			if got := err != nil && strings.Contains(err.Error(), "runner.data_dir"); got != tt.wantErr {
				t.Fatalf("error = %v, want an error about runner.data_dir: %v", err, tt.wantErr)
			}
		})
	}
}
