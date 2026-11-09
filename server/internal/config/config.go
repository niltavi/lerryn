// Package config loads the lerryn configuration from a YAML file and
// LERRYN_* environment variables.
//
// Settings an admin can change at runtime, such as open registration or
// local password login, live in the database and are not part of this file.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/caarlos0/env/v11"
	"go.yaml.in/yaml/v3"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

// Database drivers.
const (
	DatabaseSQLite   = "sqlite"
	DatabasePostgres = "postgres"
)

// Storage drivers.
const (
	StorageLocal = "local"
	StorageS3    = "s3"
)

// Runner modes: embedded runs inside the all-in-one process, remote
// connects to a server over the runner protocol.
const (
	RunnerEmbedded = "embedded"
	RunnerRemote   = "remote"
)

// Network policies a runner may allow for task containers.
const (
	NetworkNone = "none"
	NetworkFull = "full"
)

// EnvPrefix is prepended to every environment variable name, for example
// LERRYN_SERVER_LISTEN overrides server.listen.
const EnvPrefix = "LERRYN_"

// Config is the complete file-based configuration.
type Config struct {
	Server    Server    `yaml:"server" envPrefix:"SERVER_"`
	Database  Database  `yaml:"database" envPrefix:"DATABASE_"`
	Storage   Storage   `yaml:"storage" envPrefix:"STORAGE_"`
	Secrets   Secrets   `yaml:"secrets" envPrefix:"SECRETS_"`
	Auth      Auth      `yaml:"auth" envPrefix:"AUTH_"`
	Runner    Runner    `yaml:"runner" envPrefix:"RUNNER_"`
	Mirrors   Mirrors   `yaml:"mirrors" envPrefix:"MIRRORS_"`
	MCP       MCP       `yaml:"mcp" envPrefix:"MCP_"`
	Metrics   Metrics   `yaml:"metrics" envPrefix:"METRICS_"`
	AI        AI        `yaml:"ai" envPrefix:"AI_"`
	Notify    Notify    `yaml:"notify" envPrefix:"NOTIFY_"`
	Scheduler Scheduler `yaml:"scheduler" envPrefix:"SCHEDULER_"`
	Retention Retention `yaml:"retention" envPrefix:"RETENTION_"`
	Log       Log       `yaml:"log" envPrefix:"LOG_"`
}

// Server configures the HTTP listener.
type Server struct {
	Listen string `yaml:"listen" env:"LISTEN"`
	// PublicURL is the externally reachable base URL, used to build console
	// links returned to agents and in notifications.
	PublicURL string `yaml:"public_url" env:"PUBLIC_URL"`
	// Environment identifies the deployment to clients. Empty means unspecified.
	Environment string `yaml:"environment" env:"ENVIRONMENT"`
	// ExtraOrigins are other addresses the console is opened at, such as a
	// tunnel's domain in front of a LAN deployment. Session writes from them
	// are accepted like those from public_url; links returned to agents
	// still use public_url.
	ExtraOrigins []string `yaml:"extra_origins" env:"EXTRA_ORIGINS" envSeparator:","`
	// RunnerRegistrationTokenFile, when set, names a file whose content
	// remote runners may present as a registration token, besides the
	// tokens an admin creates. It lets a deployment start runners before
	// anyone has signed in.
	RunnerRegistrationTokenFile string `yaml:"runner_registration_token_file" env:"RUNNER_REGISTRATION_TOKEN_FILE"`
	// RequireTestBeforeActivate refuses to activate a task version that
	// has no successful test run (tech design 5.2).
	RequireTestBeforeActivate bool `yaml:"require_test_before_activate" env:"REQUIRE_TEST_BEFORE_ACTIVATE"`
	// TrustedProxies lists the CIDR networks of the reverse proxies in
	// front of the server. Only requests from these networks may name the
	// client address in X-Forwarded-For; empty trusts no proxy and keys
	// rate limits and audit records on the peer address.
	TrustedProxies []string `yaml:"trusted_proxies" env:"TRUSTED_PROXIES" envSeparator:","`
}

// Database selects the metadata store.
type Database struct {
	Driver string `yaml:"driver" env:"DRIVER"`
	DSN    string `yaml:"dsn" env:"DSN"`
	// MaxOpenConns caps the PostgreSQL connections of one process; zero
	// means DefaultPostgresMaxOpenConns. SQLite ignores it.
	MaxOpenConns int `yaml:"max_open_conns" env:"MAX_OPEN_CONNS"`
}

// DefaultPostgresMaxOpenConns keeps two servers and the maintenance
// commands well below PostgreSQL's default max_connections of 100.
const DefaultPostgresMaxOpenConns = 30

// Storage selects the object store for logs, artifacts and task bundles.
type Storage struct {
	Driver string `yaml:"driver" env:"DRIVER"`
	Path   string `yaml:"path" env:"PATH"`
	S3     S3     `yaml:"s3" envPrefix:"S3_"`
}

// S3 configures an S3-compatible object store. Keys are read from files,
// so they do not appear in the environment.
type S3 struct {
	Endpoint string `yaml:"endpoint" env:"ENDPOINT"`
	Region   string `yaml:"region" env:"REGION"`
	Bucket   string `yaml:"bucket" env:"BUCKET"`
	// Prefix is prepended to every object key.
	Prefix string `yaml:"prefix" env:"PREFIX"`
	// PathStyle addresses the bucket in the URL path rather than the host
	// name; most self-hosted services need it.
	PathStyle           bool   `yaml:"path_style" env:"PATH_STYLE"`
	AccessKeyIDFile     string `yaml:"access_key_id_file" env:"ACCESS_KEY_ID_FILE"`
	SecretAccessKeyFile string `yaml:"secret_access_key_file" env:"SECRET_ACCESS_KEY_FILE"`
}

// Secrets configures credential encryption.
type Secrets struct {
	MasterKeyFile string `yaml:"master_key_file" env:"MASTER_KEY_FILE"`
}

// Auth configures external identity providers. Built-in accounts need no
// file configuration.
type Auth struct {
	OIDC OIDC `yaml:"oidc" envPrefix:"OIDC_"`
	// InitialAdmin creates the first admin at startup when the deployment
	// has no account yet (tech design 12.1).
	InitialAdmin InitialAdmin `yaml:"initial_admin" envPrefix:"INITIAL_ADMIN_"`
}

// InitialAdmin names the admin account a server creates on an empty
// deployment. The password is read from a file, so it does not appear in
// the environment.
type InitialAdmin struct {
	Username     string `yaml:"username" env:"USERNAME"`
	PasswordFile string `yaml:"password_file" env:"PASSWORD_FILE"`
}

// OIDC configures the single supported OIDC provider.
type OIDC struct {
	Issuer           string `yaml:"issuer" env:"ISSUER"`
	ClientID         string `yaml:"client_id" env:"CLIENT_ID"`
	ClientSecretFile string `yaml:"client_secret_file" env:"CLIENT_SECRET_FILE"`
	// UsernameClaim names the ID token claim new users take their username
	// from.
	UsernameClaim string `yaml:"username_claim" env:"USERNAME_CLAIM"`
}

// Enabled reports whether an OIDC provider is configured.
func (o OIDC) Enabled() bool { return o.Issuer != "" }

// Runner configures the runner role.
type Runner struct {
	Mode            string   `yaml:"mode" env:"MODE"`
	Labels          []string `yaml:"labels" env:"LABELS" envSeparator:","`
	Slots           int      `yaml:"slots" env:"SLOTS"`
	AllowedNetworks []string `yaml:"allowed_networks" env:"ALLOWED_NETWORKS" envSeparator:","`
	// DataDir holds run directories and cached environments. Task
	// containers may mount nothing outside it. When the runner itself runs
	// in a container, the directory must have the same path inside and
	// outside (tech design 15.2).
	DataDir string `yaml:"data_dir" env:"DATA_DIR"`
	// PythonImage is the runtime image for Python 3.12 tasks. It must
	// contain python3 and uv.
	PythonImage string `yaml:"python_image" env:"PYTHON_IMAGE"`
	// NodeImage is the runtime image for Node 22 tasks. It must contain
	// node, npm, sh and awk.
	NodeImage string `yaml:"node_image" env:"NODE_IMAGE"`
	// ShellImage is the runtime image for Shell tasks. It must contain
	// bash, sh and awk.
	ShellImage string `yaml:"shell_image" env:"SHELL_IMAGE"`
	// ServerURL is the server a remote runner connects to.
	ServerURL string `yaml:"server_url" env:"SERVER_URL"`
	// RegistrationTokenFile names the file with the registration token a
	// remote runner exchanges for its own credential on first start. The
	// credential is kept in <data_dir>/runner-credential.
	RegistrationTokenFile string `yaml:"registration_token_file" env:"REGISTRATION_TOKEN_FILE"`
}

// Mirrors points dependency installation at internal package mirrors.
type Mirrors struct {
	PyPI string `yaml:"pypi" env:"PYPI"`
	NPM  string `yaml:"npm" env:"NPM"`
}

// MCP configures the /mcp endpoint.
type MCP struct {
	// DisableLocalhostProtection must be set when a reverse proxy on the same
	// host forwards to a loopback address and keeps the public Host header;
	// otherwise the SDK's DNS rebinding guard answers 403.
	DisableLocalhostProtection bool `yaml:"disable_localhost_protection" env:"DISABLE_LOCALHOST_PROTECTION"`
	// JSONResponse answers each request with one JSON body instead of an SSE
	// stream, for proxies that buffer streaming responses.
	JSONResponse bool `yaml:"json_response" env:"JSON_RESPONSE"`
	// MaxResultBytes caps the API response returned per tool call; zero
	// keeps the built-in default.
	MaxResultBytes int `yaml:"max_result_bytes" env:"MAX_RESULT_BYTES"`
}

// Metrics configures the Prometheus endpoint (tech design 15.6). It is off
// by default: the numbers reveal queue sizes and request patterns, so an
// operator opts in and decides who may scrape them.
type Metrics struct {
	Enabled bool `yaml:"enabled" env:"ENABLED"`
	// Listen serves /metrics on its own address, normally one reachable
	// only from the monitoring network. Empty serves it on server.listen,
	// next to the public API, which then requires TokenFile.
	Listen string `yaml:"listen" env:"LISTEN"`
	// TokenFile holds the bearer token a scraper must present. It is
	// required on the public listener and optional on a separate one.
	TokenFile string `yaml:"token_file" env:"TOKEN_FILE"`
}

// AI configures outgoing requests to model providers.
type AI struct {
	// AllowedNetworks permits selected internal model gateways.
	AllowedNetworks []string `yaml:"allowed_networks" env:"ALLOWED_NETWORKS" envSeparator:","`
}

// Notify configures outgoing notifications (tech design 14).
type Notify struct {
	// AllowedNetworks lists CIDR networks notifications may reach even
	// though they are internal, such as an intranet webhook receiver.
	// Loopback, private and link-local addresses are refused otherwise.
	AllowedNetworks []string `yaml:"allowed_networks" env:"ALLOWED_NETWORKS" envSeparator:","`
}

// Scheduler configures triggers, the run queue and retries (tech design
// 8). Zero turns a limit off.
type Scheduler struct {
	// MaxQueuedRuns caps the queued runs of the deployment, and
	// MaxQueuedRunsPerProject those of one project. Beyond them scheduled
	// and webhook runs are recorded as skipped and API starts answer
	// queue_full.
	MaxQueuedRuns           int `yaml:"max_queued_runs" env:"MAX_QUEUED_RUNS"`
	MaxQueuedRunsPerProject int `yaml:"max_queued_runs_per_project" env:"MAX_QUEUED_RUNS_PER_PROJECT"`
	// ProjectConcurrency caps how many runs of one project execute at
	// once; the rest wait in the queue.
	ProjectConcurrency int `yaml:"project_concurrency" env:"PROJECT_CONCURRENCY"`
	// MissedAfterSeconds is how late a scheduled time may be processed
	// before it counts as missed by downtime.
	MissedAfterSeconds int `yaml:"missed_after_seconds" env:"MISSED_AFTER_SECONDS"`
	// WebhookRatePerMinute caps the calls each webhook trigger accepts per
	// minute and server.
	WebhookRatePerMinute int `yaml:"webhook_rate_per_minute" env:"WEBHOOK_RATE_PER_MINUTE"`
}

// Retention configures how long run records, logs and artifacts are kept
// and how much log and artifact data is kept in total (tech design 6.6).
// Zero keeps data without limit, which is the default: a deployment
// decides to delete, nothing is deleted by surprise.
type Retention struct {
	// RunsDays deletes ended runs this many days after they finished,
	// with their logs, artifacts and events. The latest run of each task
	// is kept.
	RunsDays int `yaml:"runs_days" env:"RUNS_DAYS"`
	// LogsDays and ArtifactsDays remove the log or artifacts of an ended
	// run after this many days; the run record stays and says so.
	LogsDays      int `yaml:"logs_days" env:"LOGS_DAYS"`
	ArtifactsDays int `yaml:"artifacts_days" env:"ARTIFACTS_DAYS"`
	// LogsMaxMiB and ArtifactsMaxMiB cap the stored logs and artifacts of
	// the deployment; above a cap the oldest are removed first.
	LogsMaxMiB      int `yaml:"logs_max_mib" env:"LOGS_MAX_MIB"`
	ArtifactsMaxMiB int `yaml:"artifacts_max_mib" env:"ARTIFACTS_MAX_MIB"`
}

// Log configures the process logger.
type Log struct {
	Level  string `yaml:"level" env:"LEVEL"`
	Format string `yaml:"format" env:"FORMAT"`
}

// Default returns the configuration used when nothing overrides it.
func Default() Config {
	return Config{
		Server:   Server{Listen: ":33333", PublicURL: "http://localhost:33333", RequireTestBeforeActivate: true},
		Database: Database{Driver: DatabaseSQLite, DSN: "/var/lib/lerryn/lerryn.db"},
		Storage:  Storage{Driver: StorageLocal, Path: "/var/lib/lerryn/storage"},
		Secrets:  Secrets{MasterKeyFile: "/var/lib/lerryn/master.key"},
		Auth:     Auth{OIDC: OIDC{UsernameClaim: "preferred_username"}},
		Runner: Runner{
			Mode:            RunnerEmbedded,
			Labels:          []string{"default"},
			Slots:           4,
			AllowedNetworks: []string{NetworkNone, NetworkFull},
			DataDir:         "/var/lib/lerryn/runner",
			PythonImage:     "ghcr.io/astral-sh/uv:0.13.6-python3.12-trixie-slim",
			NodeImage:       "node:22.23.4-trixie",
			ShellImage:      "bash:5.3.20-alpine3.24",
		},
		Scheduler: Scheduler{
			MaxQueuedRuns:           1000,
			MaxQueuedRunsPerProject: 200,
			MissedAfterSeconds:      60,
			WebhookRatePerMinute:    60,
		},
		Log: Log{Level: "info", Format: "json"},
	}
}

// Load builds the configuration in three layers: defaults, then the YAML
// file at path (skipped when path is empty), then LERRYN_* environment
// variables. The result is validated before it is returned.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path) // #nosec G304 -- the operator chooses the config path
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if err := env.ParseWithOptions(&cfg, env.Options{Prefix: EnvPrefix}); err != nil {
		return Config{}, fmt.Errorf("read environment: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate reports every invalid field at once, so an operator can fix the
// file in one pass.
func (c Config) Validate() error {
	var errs []error
	if c.Server.Listen == "" {
		errs = append(errs, errors.New("server.listen must not be empty"))
	}
	if u, err := url.Parse(c.Server.PublicURL); err != nil || u.Scheme == "" || u.Host == "" {
		errs = append(errs, fmt.Errorf("server.public_url must be an absolute URL, got %q", c.Server.PublicURL))
	}
	switch c.Server.Environment {
	case "", "production", "test", "staging", "development":
	default:
		errs = append(errs, errors.New("server.environment must be empty or one of production, test, staging, development"))
	}
	for _, n := range c.Server.TrustedProxies {
		if _, err := netip.ParsePrefix(strings.TrimSpace(n)); err != nil {
			errs = append(errs, fmt.Errorf("server.trusted_proxies: %q is not a CIDR network such as 172.30.0.0/24", n))
		}
	}
	for _, o := range c.Server.ExtraOrigins {
		if !isOrigin(o) {
			errs = append(errs, fmt.Errorf("server.extra_origins must hold origins like https://lerryn.example.com, got %q", o))
		}
	}
	if !slices.Contains([]string{DatabaseSQLite, DatabasePostgres}, c.Database.Driver) {
		errs = append(errs, fmt.Errorf("database.driver must be %q or %q, got %q", DatabaseSQLite, DatabasePostgres, c.Database.Driver))
	}
	if c.Database.DSN == "" {
		errs = append(errs, errors.New("database.dsn must not be empty"))
	}
	if c.Database.MaxOpenConns < 0 {
		errs = append(errs, fmt.Errorf("database.max_open_conns must not be negative, got %d", c.Database.MaxOpenConns))
	}
	if !slices.Contains([]string{StorageLocal, StorageS3}, c.Storage.Driver) {
		errs = append(errs, fmt.Errorf("storage.driver must be %q or %q, got %q", StorageLocal, StorageS3, c.Storage.Driver))
	}
	if c.Storage.Driver == StorageLocal && c.Storage.Path == "" {
		errs = append(errs, errors.New("storage.path must not be empty for the local driver"))
	}
	if c.Storage.Driver == StorageS3 {
		s := c.Storage.S3
		if u, err := url.Parse(s.Endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("storage.s3.endpoint must be an absolute http or https URL, got %q", s.Endpoint))
		}
		if s.Bucket == "" {
			errs = append(errs, errors.New("storage.s3.bucket must not be empty for the s3 driver"))
		}
		if s.AccessKeyIDFile == "" || s.SecretAccessKeyFile == "" {
			errs = append(errs, errors.New("storage.s3.access_key_id_file and storage.s3.secret_access_key_file are required for the s3 driver"))
		}
	}
	if c.Auth.OIDC.Enabled() && c.Auth.OIDC.ClientID == "" {
		errs = append(errs, errors.New("auth.oidc.client_id is required when auth.oidc.issuer is set"))
	}
	if ia := c.Auth.InitialAdmin; (ia.Username == "") != (ia.PasswordFile == "") {
		errs = append(errs, errors.New("auth.initial_admin.username and auth.initial_admin.password_file must be set together"))
	}
	if !slices.Contains([]string{RunnerEmbedded, RunnerRemote}, c.Runner.Mode) {
		errs = append(errs, fmt.Errorf("runner.mode must be %q or %q, got %q", RunnerEmbedded, RunnerRemote, c.Runner.Mode))
	}
	// The runner role requires server_url; a server that shares the file
	// with runner.mode remote does not use it.
	if c.Runner.ServerURL != "" {
		if u, err := url.Parse(c.Runner.ServerURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("runner.server_url must be an absolute http or https URL, got %q", c.Runner.ServerURL))
		}
	}
	for _, l := range c.Runner.Labels {
		if !runproto.ValidLabel(l) {
			errs = append(errs, fmt.Errorf("runner.labels: %q must be 1 to 63 lowercase letters, digits, '-', '_' or '.', starting with a letter or digit", l))
		}
	}
	if c.Runner.Slots < 1 {
		errs = append(errs, fmt.Errorf("runner.slots must be at least 1, got %d", c.Runner.Slots))
	}
	if !filepath.IsAbs(c.Runner.DataDir) {
		errs = append(errs, fmt.Errorf("runner.data_dir must be an absolute path, got %q", c.Runner.DataDir))
	} else if systemRoot(c.Runner.DataDir) {
		errs = append(errs, fmt.Errorf("runner.data_dir must be a directory of its own, such as /var/lib/lerryn/runner, not the system directory %q", c.Runner.DataDir))
	}
	if c.Runner.PythonImage == "" {
		errs = append(errs, errors.New("runner.python_image must not be empty"))
	}
	if c.Runner.NodeImage == "" {
		errs = append(errs, errors.New("runner.node_image must not be empty"))
	}
	if c.Runner.ShellImage == "" {
		errs = append(errs, errors.New("runner.shell_image must not be empty"))
	}
	for _, n := range c.Runner.AllowedNetworks {
		if n != NetworkNone && n != NetworkFull {
			errs = append(errs, fmt.Errorf("runner.allowed_networks: unknown policy %q, want %q or %q", n, NetworkNone, NetworkFull))
		}
	}
	if c.Metrics.Enabled && c.Metrics.Listen == "" && c.Metrics.TokenFile == "" {
		errs = append(errs, errors.New("metrics.token_file is required when metrics share server.listen; set it or give metrics.listen its own address"))
	}
	if c.Metrics.Enabled && c.Metrics.Listen != "" && c.Metrics.Listen == c.Server.Listen {
		errs = append(errs, fmt.Errorf("metrics.listen must differ from server.listen (%q); leave it empty to serve /metrics on server.listen", c.Server.Listen))
	}
	for _, n := range c.AI.AllowedNetworks {
		if _, err := netip.ParsePrefix(strings.TrimSpace(n)); err != nil {
			errs = append(errs, fmt.Errorf("ai.allowed_networks: %q is not a CIDR network such as 10.0.0.0/8", n))
		}
	}
	for _, n := range c.Notify.AllowedNetworks {
		if _, err := netip.ParsePrefix(strings.TrimSpace(n)); err != nil {
			errs = append(errs, fmt.Errorf("notify.allowed_networks: %q is not a CIDR network such as 10.0.0.0/8", n))
		}
	}
	sc := c.Scheduler
	for _, l := range []struct {
		name string
		v    int
	}{
		{"max_queued_runs", sc.MaxQueuedRuns},
		{"max_queued_runs_per_project", sc.MaxQueuedRunsPerProject},
		{"project_concurrency", sc.ProjectConcurrency},
	} {
		if l.v < 0 {
			errs = append(errs, fmt.Errorf("scheduler.%s must not be negative, got %d", l.name, l.v))
		}
	}
	if sc.MissedAfterSeconds < 1 {
		errs = append(errs, fmt.Errorf("scheduler.missed_after_seconds must be at least 1, got %d", sc.MissedAfterSeconds))
	}
	if sc.WebhookRatePerMinute < 1 {
		errs = append(errs, fmt.Errorf("scheduler.webhook_rate_per_minute must be at least 1, got %d", sc.WebhookRatePerMinute))
	}
	rt := c.Retention
	for _, l := range []struct {
		name string
		v    int
	}{
		{"runs_days", rt.RunsDays},
		{"logs_days", rt.LogsDays},
		{"artifacts_days", rt.ArtifactsDays},
		{"logs_max_mib", rt.LogsMaxMiB},
		{"artifacts_max_mib", rt.ArtifactsMaxMiB},
	} {
		if l.v < 0 {
			errs = append(errs, fmt.Errorf("retention.%s must not be negative, got %d", l.name, l.v))
		}
	}
	// A log or artifact never outlives its run; a longer period would only
	// mislead whoever reads the file.
	for _, l := range []struct {
		name string
		v    int
	}{{"logs_days", rt.LogsDays}, {"artifacts_days", rt.ArtifactsDays}} {
		if rt.RunsDays > 0 && l.v > rt.RunsDays {
			errs = append(errs, fmt.Errorf("retention.%s (%d) must not exceed retention.runs_days (%d): deleting a run deletes its logs and artifacts", l.name, l.v, rt.RunsDays))
		}
	}
	if c.MCP.MaxResultBytes < 0 {
		errs = append(errs, fmt.Errorf("mcp.max_result_bytes must not be negative, got %d", c.MCP.MaxResultBytes))
	}
	return errors.Join(errs...)
}

// systemRoots are directories that must not be runner.data_dir: the
// Docker executor lets a task mount anything below data_dir, so one of
// these would hand tasks the host's system files or devices.
var systemRoots = []string{
	"/", "/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/lib64", "/opt",
	"/proc", "/root", "/run", "/sbin", "/srv", "/sys", "/tmp", "/usr",
	"/var", "/var/lib", "/var/run",
}

// kernelTrees are pseudo file systems; no directory below them is a
// place for run data.
var kernelTrees = []string{"/dev", "/proc", "/sys"}

// systemRoot reports whether dir, an absolute path, is one of
// systemRoots or lies in one of kernelTrees.
func systemRoot(dir string) bool {
	dir = filepath.Clean(dir)
	if slices.Contains(systemRoots, dir) {
		return true
	}
	return slices.ContainsFunc(kernelTrees, func(t string) bool { return strings.HasPrefix(dir, t+"/") })
}

// isOrigin reports whether s is a bare http or https origin: a scheme and a
// host, with no path, query or credentials.
func isOrigin(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
		u.User == nil && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == ""
}
