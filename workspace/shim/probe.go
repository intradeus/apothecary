package main

// probe is an image-agnostic TCP reachability check: `shim probe <host:port>`
// exits 0 when the address accepts a connection, 1 otherwise. `--wait <dur>`
// retries until connectable or the deadline (used to wait for code-server to
// finish booting before the engine exposes it). Pure stdlib, so it works in
// any workspace image — repo images may lack bash/nc/curl.
//
// `--tls` also completes a TLS handshake with the host as SNI: behind the
// egress gateway a bare connect proves only the hop to the gateway (it
// accepts before dialing upstream), the handshake proves the whole path. The
// certificate is not verified — reachability, not trust, and the image may
// carry no CA bundle. On failure the last error goes to stderr.

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"time"
)

func runProbe(args []string) {
	var wait time.Duration
	var addr string
	withTLS := false
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
		case "--tls":
			withTLS = true
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
		err := probeOnce(addr, withTLS)
		if err == nil {
			os.Exit(0)
		}
		if !time.Now().Before(deadline) {
			fmt.Fprintf(os.Stderr, "probe %s: %v\n", addr, err)
			os.Exit(1)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func probeOnce(addr string, withTLS bool) error {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	if !withTLS {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	tc := tls.Client(c, &tls.Config{ServerName: host, InsecureSkipVerify: true})
	if err := tc.Handshake(); err != nil {
		return fmt.Errorf("tls handshake: %w", err)
	}
	return nil
}
