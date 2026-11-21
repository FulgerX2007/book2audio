// Command book2audio turns a PDF into a Russian audiobook split by chapters.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/FulgerX2007/book2audio/internal/cli"
)

func main() {
	// SIGINT/SIGTERM cancel the context shared by every subcommand, so a stage
	// can stop its workers and leave the manifest consistent.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
