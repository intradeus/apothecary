package main

// probe is an image-agnostic TCP reachability check: `shim probe <host:port>`
// exits 0 when the address accepts a connection, 1 otherwise. `--wait <dur>`
// retries until connectable or the deadline (used to wait for code-server to
// finish booting before the engine exposes it). Pure stdlib, so it works in
// any workspace image — repo images may lack bash/nc/curl.

import (
	"fmt"
	"net"
	"os"
	"time"
)

func runProbe(args []string) {
	var wait time.Duration
	var addr string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--wait":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "probe: --wait needs a duration")
				os.Exit(2)
			}
			d, err := time.ParseDuration(args[i])
			if err != nil {
				fmt.Fprintf(os.Stderr, "probe: bad duration %q\n", args[i])
				os.Exit(2)
			}
			wait = d
		default:
			addr = args[i]
		}
	}
	if addr == "" {
		fmt.Fprintln(os.Stderr, "probe: missing <host:port>")
		os.Exit(2)
	}

	deadline := time.Now().Add(wait)
	for {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			c.Close()
			os.Exit(0)
		}
		if !time.Now().Before(deadline) {
			os.Exit(1)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
