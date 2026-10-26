// apothecary-shim is the single static in-workspace helper binary
// (DESIGN.md §10 + the devcontainer engine bridge). It is built FROM THIS
// SOURCE by docker itself (wsimage EnsureShim / the base-image multi-stage
// build) — stdlib plus one pinned pure-Go dependency (gojq, for `jq`), CGO
// off — so it runs in any workspace image including musl ones, with no
// node/curl/jq assumptions.
//
// Subcommands:
//
//	ask-human   MCP stdio server exposing the ask_human tool (§10)
//	relay       loopback<->unix-socket pumps for the engine bridge
//	portscan    listening-port discovery, reported to the engine
//	probe       TCP reachability check (image-agnostic readiness)
//	sidecar     the per-workspace runtime: ACP agent host, files, git,
//	            terminal — served on a unix socket to the engine
//	jq          the jq CLI (gojq) for command-stage scripts, on their PATH
//	            as `apothecary-shim jq <jq args>`
//	service     the project's services: start, stop, status, logs — each
//	            run by a detached supervisor that probes its health
//	browser-mcp the builtin `browser` MCP server: the agent's stdio, bridged
//	            through the engine to a Chromium in the project's browser
//	browser-session  (in the browser container) one MCP session: Playwright's
//	            MCP server, its Chromium proxied to the routing socket
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
	case "sidecar":
		runSidecar(os.Args[2:])
	case "jq":
		runJQ(os.Args[2:])
	case "service":
		runService(os.Args[2:])
	case "browser-mcp":
		runBrowserMCP()
	case "browser-session":
		runBrowserSession(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: shim ask-human | shim relay [<port>:<sock>]... [--reverse <sock>:<host>:<port>]... | shim portscan [--exclude <ports>] | shim probe [--wait <dur>] <host:port> | shim sidecar [--daemon] [--listen <sock>] [--repo <dir>] | shim jq <jq args> | shim service start|stop|status|logs | shim browser-mcp | shim browser-session --sock <sock> -- <argv>")
	os.Exit(2)
}
