package main

// relay pumps the engine bridge (loopback parity for workspaces whose
// hosts block container→host IP traffic — verified live; bind-mounted
// unix sockets are the sanctioned channel):
//
//	forward  <port>:<sock>          listen in-container 127.0.0.1:<port>,
//	                                pipe each conn into the unix socket
//	                                (shim → engine, proxy → engine)
//	reverse  --reverse <sock>:<host>:<port>
//	                                listen ON the bind-mounted unix socket,
//	                                pipe each conn to <host>:<port> inside
//	                                the container (engine → workspace app,
//	                                the port-exposure path)
//
// EADDRINUSE on a forward listener means a live relay from a previous
// provision already owns the port: exit 0 quietly and let it serve. (If
// the engine's ports changed across a restart, the container is restarted
// too, which clears both the old relay and its listeners.)

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
)

func runRelay(args []string) {
	started := 0
	var serveAddr, dir, tokenFile string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--stdio":
			// One connection over this process's stdio, then exit.
			i++
			if i >= len(args) {
				fatal("--stdio needs <sock>")
			}
			if err := pumpStdio(args[i], os.Stdin, os.Stdout); err != nil {
				fatal("stdio %s: %v", args[i], err)
			}
			os.Exit(0)
		case "--serve", "--dir", "--token-file":
			flag := args[i]
			i++
			if i >= len(args) {
				fatal("%s needs a value", flag)
			}
			switch flag {
			case "--serve":
				serveAddr = args[i]
			case "--dir":
				dir = args[i]
			default:
				tokenFile = args[i]
			}
		case "--reverse":
			i++
			if i >= len(args) {
				fatal("--reverse needs <sock>:<host>:<port>")
			}
			startReverse(args[i])
			started++
		default:
			startForward(args[i])
			started++
		}
	}
	if serveAddr != "" {
		if dir == "" || tokenFile == "" {
			fatal("--serve needs --dir and --token-file")
		}
		if _, err := startMux(serveAddr, dir, readTokenFile(tokenFile)); err != nil {
			fatal("serve %s: %v", serveAddr, err)
		}
		started++
	}
	if started == 0 {
		fatal("no relay pairs given")
	}
	select {} // serve forever; the container (or docker exec teardown) reaps us
}

// startForward: "<port>:<sock>" — TCP loopback listener → unix socket.
func startForward(pair string) {
	port, sock, ok := strings.Cut(pair, ":")
	if !ok {
		fatal("bad forward pair %q (want <port>:<sock>)", pair)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			os.Exit(0) // a previous relay already serves this port
		}
		fatal("listen 127.0.0.1:%s: %v", port, err)
	}
	go serve(ln, func() (net.Conn, error) { return net.Dial("unix", sock) })
}

// startReverse: "<sock>:<host>:<port>" — unix socket listener → in-container TCP.
func startReverse(spec string) {
	sock, target, ok := strings.Cut(spec, ":")
	if !ok || !strings.Contains(target, ":") {
		fatal("bad reverse spec %q (want <sock>:<host>:<port>)", spec)
	}
	_ = os.Remove(sock) // stale socket from a previous relay
	ln, err := net.Listen("unix", sock)
	if err != nil {
		fatal("listen unix %s: %v", sock, err)
	}
	// The engine-side dialer runs as the engine user; the socket file is in
	// the shared bind mount — open it up regardless of uid mapping.
	_ = os.Chmod(sock, 0o666)
	go serve(ln, func() (net.Conn, error) { return net.Dial("tcp", target) })
}

func serve(ln net.Listener, dial func() (net.Conn, error)) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			d, err := dial()
			if err != nil {
				return
			}
			defer d.Close()
			go func() {
				_, _ = io.Copy(d, c)
				halfClose(d)
			}()
			_, _ = io.Copy(c, d)
			halfClose(c)
		}(c)
	}
}

// halfClose signals write-EOF without tearing down the read side, so
// request/response protocols drain fully.
func halfClose(c net.Conn) {
	type closeWriter interface{ CloseWrite() error }
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "relay: "+format+"\n", args...)
	os.Exit(1)
}
