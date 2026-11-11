// Command apothecary is the terminal client of an Apothecary engine: it
// signs in, brings a run's ports to localhost and routes SSH into
// workspaces, all through the engine's HTTPS address. The commands live in
// internal/client/cli; this is only the wiring to the real terminal.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"apothecary/internal/client/cli"
)

// version is stamped at build time (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], cli.OSEnv(version))
	stop()
	os.Exit(code)
}
