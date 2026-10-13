package main

import (
	"context"
	"os"
	"os/signal"

	"business-brew/internal/cli"
	"business-brew/internal/wrapper"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if wrapper.IsInstaller(os.Args[0]) {
		os.Exit((wrapper.App{}).Run(ctx, os.Args[0], os.Args[1:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "shims" {
		os.Exit(deployShims(os.Args[2:]))
	}
	if len(os.Args) > 2 && os.Args[1] == "wrap" && wrapper.IsInstaller(os.Args[2]) {
		os.Exit((wrapper.App{}).Run(ctx, os.Args[2], os.Args[3:]))
	}
	os.Exit((cli.App{}).Run(ctx, os.Args[1:]))
}
