// Command sonda is the Sonda analytics server and its admin CLI.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/meltinbitfarm/sonda/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
