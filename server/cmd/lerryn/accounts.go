package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// oidcCallbackPath is where the provider sends the browser back; it is
// registered with the provider as <server.public_url><oidcCallbackPath>.
const oidcCallbackPath = "/api/v1/auth/oidc/callback"

// buildAccounts assembles the account service, with single sign-on when
// auth.oidc is configured. The client secret is read from its file once at
// startup and never logged.
func buildAccounts(cfg config.Config, db *store.DB, log *slog.Logger) (*service.Accounts, error) {
	opts := []service.AccountOption{service.WithPublicURL(cfg.Server.PublicURL)}
	if o := cfg.Auth.OIDC; o.Enabled() {
		var secret string
		if o.ClientSecretFile != "" {
			b, err := os.ReadFile(o.ClientSecretFile) // #nosec G304 -- the operator chooses the path
			if err != nil {
				return nil, fmt.Errorf("read auth.oidc.client_secret_file: %w", err)
			}
			secret = strings.TrimSpace(string(b))
		}
		provider, err := auth.NewOIDC(auth.OIDCConfig{
			Issuer:        o.Issuer,
			ClientID:      o.ClientID,
			ClientSecret:  secret,
			RedirectURL:   strings.TrimRight(cfg.Server.PublicURL, "/") + oidcCallbackPath,
			UsernameClaim: o.UsernameClaim,
		})
		if err != nil {
			return nil, err
		}
		opts = append(opts, service.WithOIDC(provider))
		log.Info("single sign-on enabled", "issuer", o.Issuer)
	}
	return service.NewAccounts(db, log, opts...), nil
}
