package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Listen != ":33333" || cfg.Database.Driver != DatabaseSQLite || cfg.Runner.Mode != RunnerEmbedded {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadExampleFile(t *testing.T) {
	// Keeps deploy/lerryn.example.yaml in sync with the Config struct.
	cfg, err := Load(filepath.Join("..", "..", "..", "deploy", "lerryn.example.yaml"))
	if err != nil {
		t.Fatalf("Load example: %v", err)
	}
	if !cfg.Auth.OIDC.Enabled() || cfg.Mirrors.PyPI == "" || cfg.Runner.NodeImage == "" || cfg.Runner.ShellImage == "" {
		t.Fatalf("example file not fully applied: %+v", cfg)
	}
}

func TestLoadPrecedence(t *testing.T) {
	path := writeFile(t, `
server:
  listen: ":9000"
runner:
  labels: [from-file]
`)
	t.Setenv("LERRYN_SERVER_LISTEN", ":9100")
	t.Setenv("LERRYN_RUNNER_LABELS", "intranet,gpu")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Listen != ":9100" {
		t.Errorf("env should override file: listen = %q", cfg.Server.Listen)
	}
	if !slices.Equal(cfg.Runner.Labels, []string{"intranet", "gpu"}) {
		t.Errorf("labels = %v", cfg.Runner.Labels)
	}
	if cfg.Database.Driver != DatabaseSQLite {
		t.Errorf("unset fields should keep defaults: driver = %q", cfg.Database.Driver)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := writeFile(t, "server:\n  lisen: \":9000\"\n")
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for a misspelled key")
	}
}

func TestValidateReportsAllErrors(t *testing.T) {
	cfg := Default()
	cfg.Database.Driver = "mysql"
	cfg.Runner.Slots = 0
	cfg.Runner.AllowedNetworks = []string{"gateway-only"}
	cfg.Runner.NodeImage, cfg.Runner.ShellImage = "", ""
	cfg.Auth.OIDC.Issuer = "https://login.example.com"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{"database.driver", "runner.slots", "gateway-only", "auth.oidc.client_id", "runner.node_image", "runner.shell_image"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestValidateExtraOrigins(t *testing.T) {
	tests := []struct {
		name    string
		origins []string
		wantErr bool
	}{
		{"none", nil, false},
		{"https and http with port", []string{"https://lerryn.example.com", "http://192.168.50.50:33333"}, false},
		{"trailing slash", []string{"https://lerryn.example.com/"}, false},
		{"path", []string{"https://lerryn.example.com/console"}, true},
		{"no scheme", []string{"lerryn.example.com"}, true},
		{"other scheme", []string{"ftp://lerryn.example.com"}, true},
		{"credentials", []string{"https://user@lerryn.example.com"}, true},
		{"query", []string{"https://lerryn.example.com?a=1"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Server.ExtraOrigins = tt.origins
			err := cfg.Validate()
			if (err != nil) != tt.wantErr || (err != nil && !strings.Contains(err.Error(), "server.extra_origins")) {
				t.Fatalf("error = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadExtraOriginsFromEnv(t *testing.T) {
	t.Setenv("LERRYN_SERVER_EXTRA_ORIGINS", "https://a.example.com,https://b.example.com")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !slices.Equal(cfg.Server.ExtraOrigins, []string{"https://a.example.com", "https://b.example.com"}) {
		t.Fatalf("extra_origins = %v", cfg.Server.ExtraOrigins)
	}
	// compose.yaml passes an empty value when .env leaves it unset.
	t.Setenv("LERRYN_SERVER_EXTRA_ORIGINS", "")
	if cfg, err = Load(""); err != nil || len(cfg.Server.ExtraOrigins) != 0 {
		t.Fatalf("empty env: extra_origins = %v, err = %v", cfg.Server.ExtraOrigins, err)
	}
}

func TestValidateMetrics(t *testing.T) {
	tests := []struct {
		name    string
		metrics Metrics
		wantErr string
	}{
		{"disabled", Metrics{}, ""},
		{"own listener", Metrics{Enabled: true, Listen: ":9090"}, ""},
		{"public listener with token", Metrics{Enabled: true, TokenFile: "/run/secrets/metrics"}, ""},
		{"public listener without token", Metrics{Enabled: true}, "metrics.token_file"},
		{"same address as the server", Metrics{Enabled: true, Listen: ":33333"}, "metrics.listen"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Metrics = tt.metrics
			err := cfg.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("error = %v, want one mentioning %s", err, tt.wantErr)
			}
		})
	}
}

func TestValidateRetention(t *testing.T) {
	tests := []struct {
		name      string
		retention Retention
		wantErr   string
	}{
		{"keep everything", Retention{}, ""},
		{"all limits", Retention{RunsDays: 365, LogsDays: 30, ArtifactsDays: 90, LogsMaxMiB: 10240, ArtifactsMaxMiB: 51200}, ""},
		{"logs as long as runs", Retention{RunsDays: 30, LogsDays: 30}, ""},
		{"logs without a run limit", Retention{LogsDays: 400}, ""},
		{"negative", Retention{ArtifactsMaxMiB: -1}, "retention.artifacts_max_mib"},
		{"logs outlive runs", Retention{RunsDays: 30, LogsDays: 31}, "retention.logs_days"},
		{"artifacts outlive runs", Retention{RunsDays: 30, ArtifactsDays: 60}, "retention.artifacts_days"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Retention = tt.retention
			err := cfg.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("error = %v, want one mentioning %s", err, tt.wantErr)
			}
		})
	}
}

func TestValidateMultiNode(t *testing.T) {
	cfg := Default()
	cfg.Storage.Driver = StorageS3
	cfg.Storage.S3.Endpoint = "minio:9000"
	cfg.Runner.Mode = RunnerRemote
	cfg.Runner.ServerURL = "lerryn:8080"
	cfg.Runner.Labels = []string{"default", "Intranet"}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{"storage.s3.endpoint", "storage.s3.bucket", "storage.s3.access_key_id_file", "runner.server_url", `"Intranet"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	cfg.Storage.S3 = S3{Endpoint: "https://s3.example.com", Bucket: "lerryn", AccessKeyIDFile: "/k", SecretAccessKeyFile: "/s"}
	cfg.Runner.ServerURL = "https://lerryn.example.com"
	cfg.Runner.Labels = []string{"default", "intranet"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid multi-node config: %v", err)
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lerryn.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
