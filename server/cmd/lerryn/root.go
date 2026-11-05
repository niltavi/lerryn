package main

import (
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/niltavi/lerryn/api"
	"github.com/niltavi/lerryn/server/internal/cli"
	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/logging"
)

type rootOptions struct {
	configPath string
}

func newRootCmd() *cobra.Command {
	opts := &rootOptions{}
	cmd := &cobra.Command{
		Use:           "lerryn",
		Short:         "Lerryn, an automation sandbox operated by AI agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.PersistentFlags().StringVar(&opts.configPath, "config", os.Getenv("LERRYN_CONFIG"),
		"path to the YAML config file (env LERRYN_CONFIG)")

	cmd.AddCommand(
		newServerCmd(opts),
		newAllInOneCmd(opts),
		newRunnerCmd(opts),
		newMigrateCmd(),
		newBackupCmd(),
		newRestoreCmd(),
		newExportCmd(),
		newImportCmd(),
		newMigrateDBCmd(),
		newSecretsCmd(),
		newAdminCmd(),
		newHealthcheckCmd(),
		newVersionCmd(),
	)
	cli.AddCommands(cmd, cli.Options{Spec: api.Spec})
	return cmd
}

// load reads the configuration and builds the logger shared by every role.
func (o *rootOptions) load() (config.Config, *slog.Logger, error) {
	cfg, err := config.Load(o.configPath)
	if err != nil {
		return config.Config{}, nil, err
	}
	log, err := logging.New(os.Stderr, cfg.Log.Level, cfg.Log.Format)
	if err != nil {
		return config.Config{}, nil, err
	}
	return cfg, log, nil
}
