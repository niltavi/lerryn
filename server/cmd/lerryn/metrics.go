package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/metrics"
)

// metricsSetup is what setupMetrics prepares: the metrics to record into
// and, when metrics have their own listener, the function serving it.
type metricsSetup struct {
	metrics *metrics.Metrics
	// serve runs the separate metrics listener until ctx ends; nil when
	// /metrics is served on server.listen or metrics are disabled.
	serve func(ctx context.Context) error
}

// setupMetrics builds the metrics for the server stack and attaches them
// to its HTTP server. Disabled metrics yield a zero setup, whose nil
// *Metrics records nothing.
func setupMetrics(cfg config.Config, log *slog.Logger, st serverStack) (metricsSetup, error) {
	if !cfg.Metrics.Enabled {
		return metricsSetup{}, nil
	}
	token, err := readMetricsToken(cfg.Metrics.TokenFile)
	if err != nil {
		return metricsSetup{}, err
	}
	m := metrics.New()
	m.RegisterStore(st.db, log)

	if cfg.Metrics.Listen == "" {
		// Config validation guarantees a token on the public listener.
		st.srv.WithMetrics(m, true, token)
		return metricsSetup{metrics: m}, nil
	}
	st.srv.WithMetrics(m, false, "")
	ln, err := net.Listen("tcp", cfg.Metrics.Listen)
	if err != nil {
		return metricsSetup{}, fmt.Errorf("listen on %s for metrics: %w", cfg.Metrics.Listen, err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.Handler(token))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	serve := func(ctx context.Context) error {
		errc := make(chan error, 1)
		go func() { errc <- srv.Serve(ln) }()
		log.InfoContext(ctx, "metrics listening", "addr", ln.Addr().String())
		select {
		case err := <-errc:
			return fmt.Errorf("serve metrics: %w", err)
		case <-ctx.Done():
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve metrics: %w", err)
		}
		return nil
	}
	return metricsSetup{metrics: m, serve: serve}, nil
}

// readMetricsToken reads the scrape token from path, which may be empty
// for no token. Surrounding whitespace, such as a trailing newline, is
// not part of the token.
func readMetricsToken(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- the operator chooses the token path
	if err != nil {
		return "", fmt.Errorf("read metrics.token_file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("metrics.token_file %s is empty", path)
	}
	return token, nil
}
