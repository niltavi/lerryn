// Command lerryn is the single binary for every role: server, runner,
// all-in-one, maintenance commands, and the CLI client.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/niltavi/lerryn/server/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := newRootCmd().ExecuteContext(ctx)
	stop()
	if err != nil {
		if !cli.Reported(err) {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(cli.ExitCode(err))
	}
}
