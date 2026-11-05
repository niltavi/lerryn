// Package app assembles the HTTP surface of the server role: the public
// API, the MCP endpoint and the embedded web console.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/buildinfo"
	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/console"
	"github.com/niltavi/lerryn/server/internal/httpapi"
	"github.com/niltavi/lerryn/server/internal/mcp"
	"github.com/niltavi/lerryn/server/internal/metrics"
	"github.com/niltavi/lerryn/server/internal/runnerhttp"
)

const (
	readinessTimeout = 2 * time.Second
	shutdownTimeout  = 15 * time.Second
)

// ReadinessCheck is a dependency that must be healthy before the server
// accepts traffic, for example the database.
type ReadinessCheck struct {
	Name  string
	Check func(context.Context) error
}

// Server serves the HTTP surface of the server role.
type Server struct {
	log      *slog.Logger
	checks   []ReadinessCheck
	cfg      config.Config
	accounts httpapi.Accounts
	projects httpapi.Projects
	admin    httpapi.Admin
	audit    httpapi.Audit
	tasks    httpapi.Tasks
	runs     httpapi.Runs
	notify   httpapi.Notifications
	creds    httpapi.Credentials
	idem     httpapi.Idempotency
	// fill serves the credential fill page /c/{requestId}.
	fill    http.Handler
	tools   *mcp.Registry
	metrics *metrics.Metrics
	// metricsToken, when metrics is set and mountMetrics is true, is the
	// bearer token /metrics on this listener requires.
	metricsToken string
	mountMetrics bool
	// streamsDone is closed when shutdown starts, ending event streams.
	streamsDone     chan struct{}
	closeStreamsMux sync.Once
	// runnerAPI serves the runner protocol under runnerhttp.BasePath.
	runnerAPI http.Handler
	// runnerAdmin serves the admin runner operations of the public API.
	runnerAdmin httpapi.RunnerAdmin
	// triggers serves the trigger operations and the webhook calls.
	triggers httpapi.Triggers
	// proxies is server.trusted_proxies, parsed by WithConfig.
	proxies auth.TrustedProxies
}

// WithTriggers sets the trigger service behind the trigger operations and
// mounts POST /hooks/{triggerId} for webhook calls. It returns s for
// chaining.
func (s *Server) WithTriggers(t httpapi.Triggers) *Server {
	s.triggers = t
	return s
}

// WithRunnerAPI mounts the runner protocol (tech design 4.5) at
// runnerhttp.BasePath. It returns s for chaining.
func (s *Server) WithRunnerAPI(h http.Handler) *Server {
	s.runnerAPI = h
	return s
}

// WithRunnerAdmin sets the service behind the admin runner operations. It
// returns s for chaining.
func (s *Server) WithRunnerAdmin(r httpapi.RunnerAdmin) *Server {
	s.runnerAdmin = r
	return s
}

// New returns a server that reports ready only when every check passes.
// It uses the default configuration until WithConfig is called.
func New(log *slog.Logger, checks ...ReadinessCheck) *Server {
	return &Server{log: log, checks: checks, cfg: config.Default(), streamsDone: make(chan struct{})}
}

// WithConfig sets the deployment configuration the API reports from, such
// as server.public_url for console links. It returns s for chaining.
func (s *Server) WithConfig(cfg config.Config) *Server {
	s.cfg = cfg
	proxies, err := auth.ParseTrustedProxies(cfg.Server.TrustedProxies)
	if err != nil {
		// config.Validate rejects such a list, so only a caller that skipped
		// it gets here. Trusting no proxy is the safe fallback.
		s.log.Error("ignore server.trusted_proxies", "error", err)
	}
	s.proxies = proxies
	return s
}

// WithAdmin sets the project, account administration and audit services.
// It returns s for chaining.
func (s *Server) WithAdmin(projects httpapi.Projects, admin httpapi.Admin, audit httpapi.Audit) *Server {
	s.projects, s.admin, s.audit = projects, admin, audit
	return s
}

// WithAccounts sets the account service that authenticates requests. It
// returns s for chaining.
func (s *Server) WithAccounts(accounts httpapi.Accounts) *Server {
	s.accounts = accounts
	return s
}

// WithRuns sets the task and run services behind the task and run
// operations. It returns s for chaining.
func (s *Server) WithRuns(tasks httpapi.Tasks, runs httpapi.Runs) *Server {
	s.tasks, s.runs = tasks, runs
	return s
}

// WithNotifications sets the service behind the notification channel
// operations. It returns s for chaining.
func (s *Server) WithNotifications(n httpapi.Notifications) *Server {
	s.notify = n
	return s
}

// WithCredentials sets the credential service behind the credential
// operations and mounts the server-rendered fill page at /c/. It returns
// s for chaining.
func (s *Server) WithCredentials(creds httpapi.Credentials, fill http.Handler) *Server {
	s.creds, s.fill = creds, fill
	return s
}

// WithIdempotency sets where responses to Idempotency-Key requests are
// remembered. It returns s for chaining.
func (s *Server) WithIdempotency(idem httpapi.Idempotency) *Server {
	s.idem = idem
	return s
}

// WithMCP mounts /mcp with the given tools. /mcp also needs an account
// service, since every MCP request must carry an access token. It returns s
// for chaining.
func (s *Server) WithMCP(tools *mcp.Registry) *Server {
	s.tools = tools
	return s
}

// WithMetrics counts and times every request in m. With mount set, it
// also serves /metrics on this handler behind the bearer token, which
// must not be empty there: this listener is the public one. It returns s
// for chaining.
func (s *Server) WithMetrics(m *metrics.Metrics, mount bool, token string) *Server {
	s.metrics, s.mountMetrics, s.metricsToken = m, mount, token
	return s
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	api := s.apiHandler()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.Handle(httpapi.BasePath, api)
	if s.runnerAPI != nil {
		mux.Handle(runnerhttp.BasePath, s.runnerAPI)
	}
	if s.triggers != nil {
		// Webhook calls carry a trigger token, not an access token, so
		// they bypass the API's authentication; the trigger service checks
		// the token.
		mux.Handle("POST /hooks/{triggerId}", httpapi.NewHooks(s.apiOptions()))
	}
	if s.fill != nil {
		mux.Handle("/c/", s.fill)
	}
	if s.tools != nil && s.accounts != nil {
		// Tool calls go to the API handler, not the root mux, so they pass
		// through the same middleware as REST requests and cannot reach /mcp.
		// The token is checked up front as well, so a client without one gets
		// a plain 401 instead of tools that all fail.
		mux.Handle("/mcp", httpapi.RequireAccessToken(s.accounts, s.proxies, mcp.NewHandler(s.tools, api, mcp.Options{
			MaxResultBytes:             s.cfg.MCP.MaxResultBytes,
			JSONResponse:               s.cfg.MCP.JSONResponse,
			DisableLocalhostProtection: s.cfg.MCP.DisableLocalhostProtection,
			Logger:                     s.log,
		})))
	}
	if s.metrics != nil && s.mountMetrics && s.metricsToken != "" {
		mux.Handle("GET /metrics", s.metrics.Handler(s.metricsToken))
	}
	// The console takes every other path, so it cannot shadow the routes
	// above: the mux prefers the more specific pattern.
	mux.Handle("/", console.New())
	return s.metrics.InstrumentHTTP(mux, operationLabel)
}

// operationLabel names a request for metrics: the contract operationId for
// API requests and a fixed name for the other routes. Unknown paths share
// one label, so scanners cannot create new series.
func operationLabel(r *http.Request) string {
	switch {
	case strings.HasPrefix(r.URL.Path, httpapi.BasePath):
		if op, ok := httpapi.OperationID(r); ok {
			return op
		}
		return "unknown"
	case strings.HasPrefix(r.URL.Path, "/hooks/"):
		return "triggerWebhook"
	case r.URL.Path == "/mcp":
		return "mcp"
	case strings.HasPrefix(r.URL.Path, "/c/"):
		return "credential_fill"
	case r.URL.Path == "/healthz", r.URL.Path == "/readyz", r.URL.Path == "/metrics":
		return strings.TrimPrefix(r.URL.Path, "/")
	default:
		return "unknown"
	}
}

// apiHandler assembles the public API from the configuration and build
// information. Capabilities without a source yet report the defaults the
// tech design documents: open registration and local password login are
// admin settings stored in the database (section 12.1), and the
// test-before-activate switch (section 5.2) has no setting yet.
func (s *Server) apiHandler() http.Handler {
	return httpapi.New(s.apiOptions())
}

// apiOptions are the options of the API handler and the webhook handler.
func (s *Server) apiOptions() httpapi.Options {
	login := []httpapi.LoginMethod{httpapi.LoginMethodPassword}
	if s.cfg.Auth.OIDC.Enabled() {
		login = append(login, httpapi.LoginMethodOidc)
	}
	networks := make([]httpapi.NetworkPolicy, 0, len(s.cfg.Runner.AllowedNetworks))
	for _, n := range s.cfg.Runner.AllowedNetworks {
		networks = append(networks, httpapi.NetworkPolicy(n))
	}
	return httpapi.Options{
		PublicURL:    s.cfg.Server.PublicURL,
		ExtraOrigins: s.cfg.Server.ExtraOrigins,
		Version:      buildinfo.Version,
		Commit:       buildinfo.Commit,
		BuildDate:    buildinfo.Date,
		Capabilities: httpapi.Capabilities{
			LoginMethods:              login,
			RegistrationOpen:          true,
			NetworkPolicies:           networks,
			RequireTestBeforeActivate: s.cfg.Server.RequireTestBeforeActivate,
		},
		Accounts:       s.accounts,
		Projects:       s.projects,
		Admin:          s.admin,
		Audit:          s.audit,
		Tasks:          s.tasks,
		Runs:           s.runs,
		RunnerAdmin:    s.runnerAdmin,
		Notifications:  s.notify,
		Triggers:       s.triggers,
		Credentials:    s.creds,
		Idempotency:    s.idem,
		StreamsDone:    s.streamsDone,
		Logger:         s.log,
		TrustedProxies: s.proxies,
	}
}

// Run serves on ln until ctx is cancelled, then drains in-flight requests
// for up to shutdownTimeout.
func (s *Server) Run(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if d, ok := s.runnerAPI.(interface{ Drain() }); ok {
		// Runners hold claim requests open; answer them so they do not
		// delay the shutdown.
		srv.RegisterOnShutdown(d.Drain)
	}
	// Event streams never end on their own; Shutdown would wait for them.
	srv.RegisterOnShutdown(func() { s.closeStreamsMux.Do(func() { close(s.streamsDone) }) })
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	s.log.InfoContext(ctx, "server listening", "addr", ln.Addr().String())

	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	s.log.Info("shutting down", "timeout", shutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()

	failed := []string{}
	for _, c := range s.checks {
		if err := c.Check(ctx); err != nil {
			s.log.WarnContext(ctx, "readiness check failed", "check", c.Name, "error", err)
			failed = append(failed, c.Name)
		}
	}
	if len(failed) > 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "failed": failed})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
