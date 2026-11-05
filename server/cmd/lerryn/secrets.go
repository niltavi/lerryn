package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/secrets"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// newSecretsCmd groups the master key maintenance commands (tech design
// 12.3): rotating the key only re-wraps the data keys of stored values.
func newSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Maintain the credential master key",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "rotate-key",
		Short: "Generate a new master key and re-wrap every stored credential with it",
		Long: "Generate a new master key, put it first in secrets.master_key_file (earlier keys stay\n" +
			"in the file, so values wrapped with them still open), and re-wrap the data key of every\n" +
			"credential version and notification channel with the new key. Stop all servers first,\n" +
			"then copy the new key file to every server before starting them again. Back up the new\n" +
			"file separately from the database.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, log, err := loadFromFlags(cmd, "secrets")
			if err != nil {
				return err
			}
			return runRotateKey(cmd.Context(), cfg, log, cmd.OutOrStdout())
		},
	}, &cobra.Command{
		Use:   "rewrap",
		Short: "Re-wrap stored values that are not under the current master key",
		Long: "Re-wrap the data key of every credential version and notification channel that is not\n" +
			"wrapped by the first key in secrets.master_key_file. Run it after a rotation that was\n" +
			"interrupted, or before removing earlier keys from the file. Safe to repeat.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, log, err := loadFromFlags(cmd, "secrets")
			if err != nil {
				return err
			}
			keys, _, err := secrets.LoadKeyFile(cfg.Secrets.MasterKeyFile)
			if err != nil {
				return err
			}
			return rewrap(cmd.Context(), cfg, log, keys, cmd.OutOrStdout())
		},
	})
	return cmd
}

func runRotateKey(ctx context.Context, cfg config.Config, log *slog.Logger, out io.Writer) error {
	path := cfg.Secrets.MasterKeyFile
	data, err := os.ReadFile(path) // #nosec G304 -- the configured key file
	if err != nil {
		return fmt.Errorf("master key file: %w", err)
	}
	old, err := secrets.ParseKeyFile(data)
	clear(data)
	if err != nil {
		return err
	}
	fresh, err := secrets.GenerateKey()
	if err != nil {
		return err
	}
	keys := append([][]byte{fresh}, old...)
	kr, err := secrets.NewLocalKeyring(keys...)
	if err != nil {
		return err
	}
	// The file is written before any row changes: a row wrapped with the
	// new key must never exist without the key on disk.
	if err := secrets.ReplaceKeyFile(path, secrets.FormatKeyFile(keys...)); err != nil {
		return err
	}
	for _, k := range keys {
		clear(k)
	}
	log.InfoContext(ctx, "master key rotated", "path", path, "key_id", kr.CurrentID())
	return rewrap(ctx, cfg, log, kr, out)
}

func rewrap(ctx context.Context, cfg config.Config, log *slog.Logger, keys *secrets.LocalKeyring, out io.Writer) error {
	db, err := store.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err := store.CheckMigrations(ctx, db); err != nil {
		return err
	}
	n, err := service.NewCredentials(db, service.NewAuthorizer(db), keys, log).RewrapAll(ctx)
	if err != nil {
		return fmt.Errorf("re-wrap credentials: %w", err)
	}
	c, err := service.RewrapChannelSecrets(ctx, db, keys)
	if err != nil {
		return fmt.Errorf("re-wrap notification channels: %w", err)
	}
	_, err = fmt.Fprintf(out, "current master key %s; re-wrapped %d credential version(s) and %d notification channel(s)\n",
		keys.CurrentID(), n, c)
	return err
}
