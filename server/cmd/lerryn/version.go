package main

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/niltavi/lerryn/server/internal/buildinfo"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "lerryn %s (commit %s, built %s, %s %s/%s)\n",
				buildinfo.Version, buildinfo.Commit, buildinfo.Date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return err
		},
	}
}
