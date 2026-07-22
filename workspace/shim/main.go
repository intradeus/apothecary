// apothecary-shim is the single static in-workspace helper binary
// (DESIGN.md §10 + the devcontainer engine bridge). It is built FROM THIS
// SOURCE by docker itself (wsimage EnsureShim / the base-image multi-stage
// build) — pure stdlib, CGO off — so it runs in any workspace image
// including musl ones, with no node/curl/jq assumptions.
//
// Subcommands:
//
//	ask-human   MCP stdio server exposing the ask_human tool (§10)
//	relay       loopback<->unix-socket pumps for the engine bridge
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "ask-human":
		runAskHuman()
	case "relay":
		runRelay(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: shim ask-human | shim relay [<port>:<sock>]... [--reverse <sock>:<host>:<port>]...")
	os.Exit(2)
}
