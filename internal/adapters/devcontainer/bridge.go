package devcontainer

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"apothecary/internal/ports"
)

// The engine bridge: engine loopback ports are exposed INSIDE the
// workspace at the same 127.0.0.1 addresses (loopback parity with the
// coder reverse tunnel) via two hops that only use docker-sanctioned
// channels — no container->host IP networking, which hardened docker
// hosts drop (found live: even a plain gateway-IP listener is unreachable
// from containers on this class of host):
//
//	workspace 127.0.0.1:<port>          (relay.mjs, `docker exec -d node`)
//	  -> /opt/apothecary/<name>.sock    (bind-mounted shim dir)
//	  -> engine 127.0.0.1:<port>        (this file's unix->tcp pump)

//go:embed relay.mjs
var relayMJS []byte

// bridgeSocks maps engine loopback ports to socket names in the shim dir;
// proxy is present only when proxy mode is on.
func (a *Adapter) bridgeSocks() map[string]int {
	socks := map[string]int{"engine.sock": a.cfg.EnginePort}
	if a.cfg.ProxyPort > 0 {
		socks["proxy.sock"] = a.cfg.ProxyPort
	}
	return socks
}

// relayPairs are relay.mjs's argv: "<in-container port>:<socket path>".
func (a *Adapter) relayPairs() []string {
	var pairs []string
	for sock, port := range a.bridgeSocks() {
		pairs = append(pairs, strconv.Itoa(port)+":/opt/apothecary/"+sock)
	}
	// Deterministic argv (map order) for tests and log readability.
	if len(pairs) > 1 && pairs[0] > pairs[1] {
		pairs[0], pairs[1] = pairs[1], pairs[0]
	}
	return pairs
}

// ensureBridge opens (once per env) the engine-side unix listeners in the
// mounted shim dir and pumps them to the engine's loopback ports.
func (a *Adapter) ensureBridge(env ports.EnvID) error {
	a.bridgeMu.Lock()
	defer a.bridgeMu.Unlock()
	if _, ok := a.bridges[env]; ok {
		return nil
	}
	shimDir := filepath.Join(a.folderFor(string(env)), "shim")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		return err
	}
	var lns []net.Listener
	for sock, port := range a.bridgeSocks() {
		path := filepath.Join(shimDir, sock)
		_ = os.Remove(path) // stale socket from a previous engine process
		ln, err := a.listen("unix", path)
		if err != nil {
			for _, l := range lns {
				_ = l.Close()
			}
			return fmt.Errorf("bridge socket %s: %w", sock, err)
		}
		// The container-side user's uid usually matches ours
		// (updateRemoteUserUID), but root-user images do not — open the
		// socket to whoever the relay runs as.
		_ = os.Chmod(path, 0o666)
		go pumpToLoopback(ln, port)
		lns = append(lns, ln)
	}
	a.bridges[env] = lns
	return nil
}

// pumpToLoopback forwards every accepted connection to the engine's own
// loopback port; exits when the listener closes.
func pumpToLoopback(ln net.Listener, port int) {
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			d, err := net.Dial("tcp", target)
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

func (a *Adapter) closeBridge(env ports.EnvID) {
	a.bridgeMu.Lock()
	defer a.bridgeMu.Unlock()
	for _, ln := range a.bridges[env] {
		_ = ln.Close()
	}
	delete(a.bridges, env)
}

// startRelay launches relay.mjs detached inside the container. node comes
// from the workspace's own environment: it is already a hard requirement
// of the ask_human shim, and the harnesses themselves are npm-distributed.
// `devcontainer exec` resolves it through the remote user's login env
// (nvm-style feature installs included); `docker exec -d` then runs that
// exact binary, because detached execs get no login environment.
func (a *Adapter) startRelay(ctx context.Context, name string) error {
	nodeOut, _, code, err := a.run(ctx, a.cfg.DevcontainerBin,
		[]string{"exec", "--id-label", label(name), "bash", "-lc", "command -v node"}, nil, nil)
	if err != nil {
		return err
	}
	nodePath := strings.TrimSpace(string(nodeOut))
	if code != 0 || nodePath == "" {
		return fmt.Errorf("workspace has no `node`, which the apothecary shim and engine relay require — add the node feature to the repo's devcontainer.json (e.g. \"ghcr.io/devcontainers/features/node:1\": {})")
	}
	cidOut, _, code, err := a.run(ctx, a.cfg.DockerBin,
		[]string{"ps", "-q", "--filter", "label=" + label(name)}, nil, nil)
	if err != nil {
		return err
	}
	cid, _, _ := strings.Cut(strings.TrimSpace(string(cidOut)), "\n")
	if code != 0 || cid == "" {
		return fmt.Errorf("workspace %s: no running container for relay", name)
	}
	args := append([]string{"exec", "-d", cid, nodePath, "/opt/apothecary/relay.mjs"}, a.relayPairs()...)
	_, stderr, code, err := a.run(ctx, a.cfg.DockerBin, args, nil, nil)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("start relay in %s: exit %d: %s", name, code, tail(string(stderr), 512))
	}
	return nil
}
