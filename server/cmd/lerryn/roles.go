package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/niltavi/lerryn/api"
	"github.com/niltavi/lerryn/server/internal/app"
	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/buildinfo"
	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/events"
	"github.com/niltavi/lerryn/server/internal/executor/docker"
	"github.com/niltavi/lerryn/server/internal/leader"
	"github.com/niltavi/lerryn/server/internal/mcp"
	"github.com/niltavi/lerryn/server/internal/metrics"
	"github.com/niltavi/lerryn/server/internal/notify"
	"github.com/niltavi/lerryn/server/internal/runner"
	"github.com/niltavi/lerryn/server/internal/runnerhttp"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/scheduler"
	"github.com/niltavi/lerryn/server/internal/secrets"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/web"
)

// archiveInterval is how often the leader looks for ended runs whose logs
// are still in chunks (tech design 6.5).
const archiveInterval = 10 * time.Second

// retentionFirstDelay is how long a new leader waits before its first
// retention cleanup, so that a restart does not start with it.
const retentionFirstDelay = 30 * time.Second

// credentialFile is where a remote runner keeps its credential, under its
// data directory.
const credentialFile = "runner-credential"

func newServerCmd(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "server",
		Short: "Run the server role: API, MCP, console and scheduling",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, log, err := opts.load()
			if err != nil {
				return err
			}
			return runServer(cmd.Context(), cfg, log.With("role", "server"), false)
		},
	}
}

func newAllInOneCmd(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "all-in-one",
		Short: "Run the server and an embedded runner in one process",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, log, err := opts.load()
			if err != nil {
				return err
			}
			return runServer(cmd.Context(), cfg, log.With("role", "all-in-one"), true)
		},
	}
}

func newRunnerCmd(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "runner",
		Short: "Run the runner role and execute tasks for a remote server",
		Long: "Connect to the server at runner.server_url, claim runs and execute them in\n" +
			"containers on this host. On first start the runner exchanges the token in\n" +
			"runner.registration_token_file for a credential of its own.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, log, err := opts.load()
			if err != nil {
				return err
			}
			return runRemoteRunner(cmd.Context(), cfg, log.With("role", "runner"))
		},
	}
}

func newMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply database migrations",
		Long: "Apply every pending database migration and exit. Safe to run repeatedly;\n" +
			"on PostgreSQL concurrent runs take turns under an advisory lock.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// --config is a persistent flag of the root command.
			configPath, err := cmd.Flags().GetString("config")
			if err != nil {
				return err
			}
			cfg, log, err := (&rootOptions{configPath: configPath}).load()
			if err != nil {
				return err
			}
			return runMigrate(cmd.Context(), cfg, log.With("role", "migrate"), cmd.OutOrStdout())
		},
	}
}

func runMigrate(ctx context.Context, cfg config.Config, log *slog.Logger, out io.Writer) error {
	db, err := store.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	res, err := store.Migrate(ctx, db)
	for _, m := range res.Applied {
		log.InfoContext(ctx, "migration applied", "version", m.Version, "name", m.Name, "duration", m.Duration)
	}
	if err != nil {
		return err
	}
	log.InfoContext(ctx, "database schema is current", "database", cfg.Database.Driver, "version", res.Version, "applied", len(res.Applied))
	_, err = fmt.Fprintf(out, "applied %d migration(s); %s schema at version %d\n", len(res.Applied), cfg.Database.Driver, res.Version)
	return err
}

// runServer serves the API, MCP and the runner protocol until ctx ends.
// The leader among the servers (always this one on SQLite) also expires
// the leases of runs whose runner went away and archives logs. With
// embedded set, a runner runs in the same process (all-in-one).
func runServer(ctx context.Context, cfg config.Config, log *slog.Logger, embedded bool) error {
	log.InfoContext(ctx, "starting lerryn", "version", buildinfo.Version, "database", cfg.Database.Driver, "storage", cfg.Storage.Driver)
	st, err := buildServer(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() { _ = st.db.Close() }()
	ms, err := setupMetrics(cfg, log, st)
	if err != nil {
		return err
	}

	var rn *runner.Runner
	if embedded {
		if cfg.Runner.Mode != config.RunnerEmbedded {
			return fmt.Errorf("all-in-one needs runner.mode %q, got %q", config.RunnerEmbedded, cfg.Runner.Mode)
		}
		var closeExecutor func() error
		rn, closeExecutor, err = buildRunner(ctx, cfg, log, st.coordinator)
		if err != nil {
			return err
		}
		defer func() { _ = closeExecutor() }()
	}

	ln, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Server.Listen, err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg        sync.WaitGroup
		runnerErr error
	)
	if ms.serve != nil {
		wg.Go(func() {
			if err := ms.serve(ctx); err != nil {
				log.ErrorContext(ctx, "metrics listener stopped", "error", err)
			}
		})
	}
	if st.pgBus != nil {
		wg.Go(func() { st.pgBus.Run(ctx) })
	}
	wg.Go(func() { st.runs.Listen(ctx) })
	// Background work that must run on one server at a time. Each task
	// loops until its context ends, which happens when this server stops
	// leading.
	leaderTasks := []func(context.Context){
		func(ctx context.Context) { reconcileRuns(ctx, st.runs, ms.metrics, log) },
		func(ctx context.Context) { archiveLogs(ctx, st.runs, log) },
		func(ctx context.Context) { cleanupRetention(ctx, st.retention, ms.metrics, log) },
		st.notifications.RunDispatcher,
		// The elector runs this only while the server leads and cancels it
		// on losing the lease, so the loop itself always acts.
		scheduler.New(st.triggers, scheduler.Options{Logger: log.With("component", "scheduler")}).Run,
	}
	elector := leader.New(st.db, store.NewID().String(), log.With("component", "leader"))
	wg.Go(func() { elector.Run(ctx, leader.All(leaderTasks...)) })
	if rn != nil {
		wg.Go(func() {
			// Run returns an error only when it cannot start; the process
			// is useless without its runner, so it stops as a whole.
			if runnerErr = rn.Run(ctx); runnerErr != nil {
				cancel()
			}
		})
	}
	err = st.srv.Run(ctx, ln)
	cancel()
	wg.Wait()
	if runnerErr != nil {
		return fmt.Errorf("embedded runner: %w", runnerErr)
	}
	return err
}

// reconcileRuns interrupts runs whose runner lease expired, every
// service.ReconcileInterval until ctx ends. It runs on the leader only.
func reconcileRuns(ctx context.Context, runs *service.Runs, m *metrics.Metrics, log *slog.Logger) {
	t := time.NewTicker(service.ReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		expired, err := runs.Reconcile(ctx)
		if err != nil && ctx.Err() == nil {
			log.ErrorContext(ctx, "reconcile runs", "error", err)
		}
		m.AddLeaseExpirations(len(expired))
	}
}

// archiveLogs moves the logs of ended runs to the object store, every
// archiveInterval, or at once again while there is a backlog.
func archiveLogs(ctx context.Context, runs *service.Runs, log *slog.Logger) {
	t := time.NewTicker(archiveInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for ctx.Err() == nil {
			n, err := runs.ArchiveLogs(ctx)
			if err != nil && ctx.Err() == nil {
				log.ErrorContext(ctx, "archive logs", "error", err)
			}
			if n < service.ArchiveBatch {
				break
			}
		}
	}
}

// cleanupRetention applies the retention policy every
// service.RetentionInterval, starting soon after the server becomes the
// leader. It runs even without a policy: it also fills in log archive
// sizes and collects objects released earlier.
func cleanupRetention(ctx context.Context, ret *service.Retention, m *metrics.Metrics, log *slog.Logger) {
	wait := retentionFirstDelay
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		wait = service.RetentionInterval
		res, err := ret.Cleanup(ctx)
		if err != nil && ctx.Err() == nil {
			log.ErrorContext(ctx, "retention cleanup", "error", err)
		}
		m.AddRetention(metrics.Retention{
			Runs: res.Runs, Logs: res.Logs, Artifacts: res.Artifacts, Objects: res.Objects,
			LogBytes: res.LogBytes, ArtifactBytes: res.ArtifactBytes,
		})
	}
}

// serverStack is what buildServer assembles: the HTTP server and the
// pieces that the background loops and an embedded runner share with it.
type serverStack struct {
	srv         *app.Server
	db          *store.DB
	runs        *service.Runs
	coordinator *service.Coordinator
	// runners serves runner administration; the admin API wires it.
	runners *service.Runners
	// pgBus is set on PostgreSQL; it must be run to hear other servers.
	pgBus *events.Postgres
	// notifications dispatches notifications; it runs on the leader.
	notifications *service.Notifications
	// triggers fires schedules and retries; the scheduler loop drives it
	// on the leader.
	triggers *service.Triggers
	// retention applies the retention policy; it runs on the leader.
	retention *service.Retention
}

// buildServer opens the database and the object store, applies pending
// migrations and assembles the HTTP server and its services. Migrating at
// startup is safe with several replicas: store.Migrate serialises
// concurrent runs. The caller closes st.db after the server stops.
func buildServer(ctx context.Context, cfg config.Config, log *slog.Logger) (st serverStack, err error) {
	tools, err := mcp.Load(ctx, api.Spec)
	if err != nil {
		return st, fmt.Errorf("build mcp tools: %w", err)
	}
	blobs, err := openBlobStore(ctx, cfg.Storage)
	if err != nil {
		return st, err
	}
	var registrationToken string
	if path := cfg.Server.RunnerRegistrationTokenFile; path != "" {
		if registrationToken, err = readSecretFile(path); err != nil {
			return st, fmt.Errorf("server.runner_registration_token_file: %w", err)
		}
	}
	db, err := store.Open(ctx, cfg.Database)
	if err != nil {
		return st, err
	}
	defer func() {
		if err != nil {
			_ = db.Close()
		}
	}()
	res, err := store.Migrate(ctx, db)
	if err != nil {
		return st, err
	}
	log.InfoContext(ctx, "database schema is current", "version", res.Version, "applied", len(res.Applied))

	var bus events.Bus = events.NewLocal()
	var pgBus *events.Postgres
	if cfg.Database.Driver == config.DatabasePostgres {
		if pgBus, err = events.NewPostgres(ctx, db, cfg.Database.DSN, log.With("component", "events")); err != nil {
			return st, err
		}
		bus = pgBus
	}

	keys, err := loadMasterKey(ctx, cfg, log)
	if err != nil {
		return st, err
	}
	authz := service.NewAuthorizer(db)
	sc := cfg.Scheduler
	queue := service.QueueLimits{MaxQueued: sc.MaxQueuedRuns, MaxQueuedPerProject: sc.MaxQueuedRunsPerProject}
	creds := service.NewCredentials(db, authz, keys, log)
	runs := service.NewRuns(db, authz, log).WithEvents(bus, pgBus != nil).WithLogArchive(blobs).WithQueueLimits(queue).
		WithPlaintextCheck(creds)
	coordinator := service.NewCoordinator(db, blobs, runs, log).WithProjectConcurrency(sc.ProjectConcurrency).UseCredentials(creds)
	triggers := service.NewTriggers(db, authz, blobs, log.With("component", "triggers"), service.TriggerOptions{
		Queue:            queue,
		MissedAfter:      time.Duration(sc.MissedAfterSeconds) * time.Second,
		WebhookPerMinute: sc.WebhookRatePerMinute,
	}).WithEvents(bus).WithPlaintextCheck(creds)
	runners := service.NewRunners(db, coordinator, log).WithStaticRegistrationToken(registrationToken)
	guard, err := notify.NewGuard(cfg.Notify.AllowedNetworks)
	if err != nil {
		return st, err
	}
	notifications := service.NewNotifications(db, authz, service.NotificationsOptions{
		PublicURL: cfg.Server.PublicURL,
		Guard:     guard,
		Sealer:    service.KeyringSealer{Keys: keys},
		Logger:    log.With("component", "notify"),
	})
	// A run that ends, here or on another server, is dispatched at once
	// instead of at the next poll.
	runs.OnRunEnded(func(uuid.UUID) { notifications.Wake() })
	schema := app.ReadinessCheck{
		Name:  "database",
		Check: func(ctx context.Context) error { return store.CheckMigrations(ctx, db) },
	}
	accounts, err := buildAccounts(cfg, db, log)
	if err != nil {
		return st, err
	}
	admin := service.NewAdmin(accounts, log)
	if err := bootstrapAdmin(ctx, cfg, admin, log); err != nil {
		return st, err
	}
	proxies, err := auth.ParseTrustedProxies(cfg.Server.TrustedProxies)
	if err != nil {
		return st, fmt.Errorf("server.trusted_proxies: %w", err)
	}
	fill, err := web.NewFill(web.FillOptions{
		PublicURL:      cfg.Server.PublicURL,
		ExtraOrigins:   cfg.Server.ExtraOrigins,
		Credentials:    creds,
		Accounts:       accounts,
		CSRFKey:        keys.DeriveKey("fill-csrf"),
		Logger:         log,
		Location:       time.Local,
		TrustedProxies: proxies,
	})
	if err != nil {
		return st, err
	}
	srv := app.New(log, schema).
		WithConfig(cfg).
		WithAccounts(accounts).
		WithAdmin(service.NewProjects(db, authz, log), admin, service.NewAudit(db, log)).
		WithRuns(service.NewTasks(db, authz, blobs, log).WithTestBeforeActivate(func(context.Context) (bool, error) {
			return cfg.Server.RequireTestBeforeActivate, nil
		}).WithPlaintextCheck(creds), runs).
		WithNotifications(notifications).
		WithCredentials(creds, fill).
		WithIdempotency(service.NewIdempotency(db, log)).
		WithMCP(tools).
		WithRunnerAdmin(runners).
		WithTriggers(triggers).
		WithRunnerAPI(runnerhttp.NewHandler(runnerhttp.Options{
			Coordinator: coordinator,
			Runners:     runners,
			Events:      bus,
			Logger:      log,
		}))
	return serverStack{
		srv:           srv,
		db:            db,
		runs:          runs,
		coordinator:   coordinator,
		runners:       runners,
		pgBus:         pgBus,
		notifications: notifications,
		triggers:      triggers,
		retention:     service.NewRetention(db, blobs, retentionPolicy(cfg.Retention), log.With("component", "retention")),
	}, nil
}

// retentionPolicy turns the retention configuration into the service's
// policy.
func retentionPolicy(c config.Retention) service.RetentionPolicy {
	const day = 24 * time.Hour
	return service.RetentionPolicy{
		Runs:              time.Duration(c.RunsDays) * day,
		Logs:              time.Duration(c.LogsDays) * day,
		Artifacts:         time.Duration(c.ArtifactsDays) * day,
		LogsMaxBytes:      int64(c.LogsMaxMiB) << 20,
		ArtifactsMaxBytes: int64(c.ArtifactsMaxMiB) << 20,
	}
}

// openBlobStore opens the configured object store, for the server and the
// maintenance commands alike. An S3 bucket is checked at
// once, so a wrong endpoint or key fails the start instead of the first
// task submission.
func openBlobStore(ctx context.Context, cfg config.Storage) (blob.Store, error) {
	switch cfg.Driver {
	case config.StorageLocal:
		return blob.NewLocal(cfg.Path)
	case config.StorageS3:
		keyID, err := readSecretFile(cfg.S3.AccessKeyIDFile)
		if err != nil {
			return nil, fmt.Errorf("storage.s3.access_key_id_file: %w", err)
		}
		secret, err := readSecretFile(cfg.S3.SecretAccessKeyFile)
		if err != nil {
			return nil, fmt.Errorf("storage.s3.secret_access_key_file: %w", err)
		}
		s3, err := blob.NewS3(blob.S3Options{
			Endpoint:        cfg.S3.Endpoint,
			Region:          cfg.S3.Region,
			Bucket:          cfg.S3.Bucket,
			Prefix:          cfg.S3.Prefix,
			PathStyle:       cfg.S3.PathStyle,
			AccessKeyID:     keyID,
			SecretAccessKey: secret,
		})
		if err != nil {
			return nil, err
		}
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := s3.Check(checkCtx); err != nil {
			return nil, fmt.Errorf("object store: %w", err)
		}
		return s3, nil
	default:
		return nil, fmt.Errorf("unknown storage driver %q", cfg.Driver)
	}
}

// loadMasterKey reads secrets.master_key_file, generating it on the first
// start (tech design 15.2). The key never goes into the database or its
// backups, so the log says where it is and that it needs a backup of its
// own.
func loadMasterKey(ctx context.Context, cfg config.Config, log *slog.Logger) (*secrets.LocalKeyring, error) {
	path := cfg.Secrets.MasterKeyFile
	keys, st, err := secrets.LoadOrCreateKeyFile(path)
	if err != nil {
		return nil, fmt.Errorf("credential master key: %w", err)
	}
	if st.Created {
		log.WarnContext(ctx, "generated a new credential master key; back it up separately from database backups, "+
			"without it stored credentials cannot be decrypted", "path", path, "key_id", keys.CurrentID())
	}
	if st.LooseMode {
		log.WarnContext(ctx, "the credential master key file is readable by other users; restrict it to mode 0600", "path", path)
	}
	return keys, nil
}

// readSecretFile returns the trimmed content of a file holding a secret.
func readSecretFile(path string) (string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the operator chooses the path
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return s, nil
}

// runRemoteRunner runs the runner role: it claims runs from the server at
// runner.server_url and executes them on this host until ctx ends.
func runRemoteRunner(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	if cfg.Runner.Mode != config.RunnerRemote {
		return fmt.Errorf("the runner role needs runner.mode %q and runner.server_url, got mode %q", config.RunnerRemote, cfg.Runner.Mode)
	}
	if cfg.Runner.ServerURL == "" {
		return errors.New("the runner role needs runner.server_url, the URL of the server or its load balancer")
	}
	log.InfoContext(ctx, "starting lerryn runner", "version", buildinfo.Version, "server", cfg.Runner.ServerURL, "labels", cfg.Runner.Labels)
	var token string
	if path := cfg.Runner.RegistrationTokenFile; path != "" {
		var err error
		if token, err = readSecretFile(path); err != nil {
			return fmt.Errorf("runner.registration_token_file: %w", err)
		}
	}
	if err := os.MkdirAll(cfg.Runner.DataDir, 0o750); err != nil {
		return fmt.Errorf("create runner data directory: %w", err)
	}
	client, err := runnerhttp.NewClient(runnerhttp.ClientOptions{
		ServerURL:         cfg.Runner.ServerURL,
		RegistrationToken: token,
		CredentialFile:    filepath.Join(cfg.Runner.DataDir, credentialFile),
		Logger:            log.With("component", "runner_client"),
	})
	if err != nil {
		return err
	}
	if !secureServerURL(cfg.Runner.ServerURL) {
		log.WarnContext(ctx, "runner.server_url is not HTTPS; the runner credential and task data travel unencrypted")
	}
	rn, closeExecutor, err := buildRunner(ctx, cfg, log, client)
	if err != nil {
		return err
	}
	defer func() { _ = closeExecutor() }()
	if err := rn.Run(ctx); err != nil {
		if errors.Is(err, runproto.ErrRejected) {
			return fmt.Errorf("the server refused this runner: %w", err)
		}
		return err
	}
	return nil
}

// buildRunner prepares a runner that talks to coord: it connects to
// Docker, refuses a daemon that cannot apply the default hardening, and
// loads the runner's stable ID. The caller runs it and closes the executor
// afterwards.
func buildRunner(ctx context.Context, cfg config.Config, log *slog.Logger, coord runproto.Coordinator) (*runner.Runner, func() error, error) {
	log = log.With("component", "runner")
	ex, err := docker.New(docker.Options{
		AllowedMountRoots: []string{cfg.Runner.DataDir},
		Logger:            log,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("docker executor: %w", err)
	}
	info, err := ex.Check(ctx)
	if err != nil {
		_ = ex.Close()
		return nil, nil, fmt.Errorf("docker daemon: %w", err)
	}
	log.InfoContext(ctx, "docker daemon ready", "version", info.ServerVersion, "os", info.OperatingSystem,
		"cgroup", info.CgroupVersion, "rootless", info.Rootless)
	if info.DesktopDocker() {
		log.WarnContext(ctx, "desktop Docker does not isolate tasks from each other on the network: tasks with network full can reach each other's ports; use Docker Engine on Linux where that matters",
			"os", info.OperatingSystem)
	}

	id, err := runner.LoadID(cfg.Runner.DataDir)
	if err != nil {
		_ = ex.Close()
		return nil, nil, err
	}
	name, _ := os.Hostname()
	rn, err := runner.New(runner.Options{
		ID:              id,
		Name:            name,
		Slots:           cfg.Runner.Slots,
		Labels:          cfg.Runner.Labels,
		Version:         buildinfo.Version,
		DataDir:         cfg.Runner.DataDir,
		PythonImage:     cfg.Runner.PythonImage,
		NodeImage:       cfg.Runner.NodeImage,
		ShellImage:      cfg.Runner.ShellImage,
		PyPIMirror:      cfg.Mirrors.PyPI,
		NPMMirror:       cfg.Mirrors.NPM,
		AllowedNetworks: cfg.Runner.AllowedNetworks,
		Executor:        ex,
		Coordinator:     coord,
		Logger:          log,
	})
	if err != nil {
		_ = ex.Close()
		return nil, nil, err
	}
	return rn, ex.Close, nil
}

// secureServerURL reports whether the runner reaches the server over
// HTTPS. The scheme is compared after parsing, so an upper-case scheme or
// one with surrounding text is judged by what the client will actually use.
func secureServerURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.EqualFold(u.Scheme, "https")
}
