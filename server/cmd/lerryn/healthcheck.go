package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/niltavi/lerryn/server/internal/config"
)

// healthcheckTimeout bounds one probe; container runtimes kill a
// healthcheck that hangs, but report it less clearly.
const healthcheckTimeout = 5 * time.Second

// newHealthcheckCmd probes /readyz of the local server. The image is
// distroless and has no curl or wget, so Compose healthchecks call the
// binary itself.
func newHealthcheckCmd() *cobra.Command {
	var url string
	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Exit 0 if the local server answers /readyz with 200, for container healthchecks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if url == "" {
				configPath, err := cmd.Flags().GetString("config")
				if err != nil {
					return err
				}
				cfg, err := config.Load(configPath)
				if err != nil {
					return err
				}
				if url, err = readyzURL(cfg.Server.Listen); err != nil {
					return err
				}
			}
			return probe(cmd.Context(), url)
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "URL to probe (default: /readyz on server.listen via the loopback address)")
	return cmd
}

// readyzURL turns a listen address such as ":8080" or "0.0.0.0:8080" into
// the loopback URL of its readiness endpoint.
func readyzURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("server.listen %q: %w", listen, err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
		if ip != nil && ip.To4() == nil {
			host = "::1"
		}
	}
	return "http://" + net.JoinHostPort(host, port) + "/readyz", nil
}

func probe(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, healthcheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("probe %s: %w", url, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe %s: status %d", url, resp.StatusCode)
	}
	return nil
}
