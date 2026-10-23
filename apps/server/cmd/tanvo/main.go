// Command tanvo is the Tanvo server: `tanvo serve --role api|worker|consumer|all`,
// `tanvo migrate` and `tanvo version`.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/tanvoxyz/tanvo/apps/server/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := cli.Run(ctx, os.Args[1:], cli.Env{})
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tanvo:", err)
		os.Exit(1)
	}
}
