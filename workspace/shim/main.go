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
//	portscan    listening-port discovery, reported to the engine
//	probe       TCP reachability check (image-agnostic readiness)
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
	case "portscan":
		runPortScan(os.Args[2:])
	case "probe":
		runProbe(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: shim ask-human | shim relay [<port>:<sock>]... [--reverse <sock>:<host>:<port>]... | shim portscan [--exclude <ports>] | shim probe [--wait <dur>] <host:port>")
	os.Exit(2)
}
