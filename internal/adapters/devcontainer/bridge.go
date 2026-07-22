package devcontainer

import (
	"context"

	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/ports"
)

// The engine bridge: engine loopback ports are exposed INSIDE the
// workspace at the same 127.0.0.1 addresses (loopback parity with the
// coder reverse tunnel) via two hops that only use docker-sanctioned
// channels — no container->host IP networking, which hardened docker
// hosts drop (found live: even a plain gateway-IP listener is unreachable
// from containers on this class of host):
//
//	workspace 127.0.0.1:<port>          (`docker exec -d shim relay`)
//	  -> /opt/apothecary/<name>.sock    (bind-mounted shim dir)
//	  -> engine 127.0.0.1:<port>        (this file's unix->tcp pump)

// bridgeSocks maps engine loopback ports to socket names in the shim dir;
// proxy is present only when proxy mode is on.
func (a *Adapter) bridgeSocks() map[string]int {
	socks := map[string]int{"engine.sock": a.cfg.EnginePort}
	if a.cfg.ProxyPort > 0 {
		socks["proxy.sock"] = a.cfg.ProxyPort
	}
	return socks
}

// relayPairs are the relay's forward argv: "<in-container port>:<socket path>".
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

// containerID resolves the env's running container via the id label.
func (a *Adapter) containerID(ctx context.Context, name string) (string, error) {
	cidOut, _, code, err := a.run(ctx, a.cfg.DockerBin,
		[]string{"ps", "-q", "--filter", "label=" + label(name)}, nil, nil)
	if err != nil {
		return "", err
	}
	cid, _, _ := strings.Cut(strings.TrimSpace(string(cidOut)), "\n")
	if code != 0 || cid == "" {
		return "", fmt.Errorf("workspace %s: no running container", name)
	}
	return cid, nil
}

// startRelay launches the mounted static shim's relay detached inside the
// container — no runtime dependency on anything in the workspace image.
func (a *Adapter) startRelay(ctx context.Context, name string) error {
	cid, err := a.containerID(ctx, name)
	if err != nil {
		return err
	}
	args := append([]string{"exec", "-d", cid, "/opt/apothecary/shim", "relay"}, a.relayPairs()...)
	_, stderr, code, err := a.run(ctx, a.cfg.DockerBin, args, nil, nil)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("start relay in %s: exit %d: %s", name, code, tail(string(stderr), 512))
	}
	return nil
}

// --- ports.WorkspacePortExposer ---------------------------------------------

// AppPorts: the ports the repo's devcontainer declares (forwardPorts /
// appPort) — auto-exposed by the engine at env_ready.
func (a *Adapter) AppPorts(ctx context.Context, env ports.EnvID) []int {
	return a.declaredPorts(ctx, string(env))
}

// ExposePort reaches env's 127.0.0.1:port through a REVERSE relay: the
// shim listens on a bind-mounted unix socket in-container and pipes each
// connection to the app port; the engine dials the socket file host-side.
// Same channel as the engine bridge, so it works on hosts that drop
// container→host IP traffic in either direction.
func (a *Adapter) ExposePort(ctx context.Context, env ports.EnvID, port int) (ports.PortConn, error) {
	name := string(env)
	sock := fmt.Sprintf("app-%d.sock", port)
	hostPath := filepath.Join(a.folderFor(name), "shim", sock)
	cid, err := a.containerID(ctx, name)
	if err != nil {
		return nil, err
	}
	spec := fmt.Sprintf("/opt/apothecary/%s:127.0.0.1:%d", sock, port)
	_, stderr, code, err := a.run(ctx, a.cfg.DockerBin,
		[]string{"exec", "-d", cid, "/opt/apothecary/shim", "relay", "--reverse", spec}, nil, nil)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("expose port %d in %s: exit %d: %s", port, name, code, tail(string(stderr), 512))
	}
	// The relay creates the socket in-container; the bind mount surfaces it
	// here. Bounded wait, then hand out the dialer.
	for range 50 {
		if _, err := os.Stat(hostPath); err == nil {
			return unixPortConn{path: hostPath}, nil
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("expose port %d in %s: relay socket never appeared", port, name)
}

// unixPortConn dials the reverse-relay socket per browser connection. The
// relay process dies with the container and the socket file with the env
// folder, so Close has nothing to own.
type unixPortConn struct{ path string }

func (u unixPortConn) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", u.path)
}

func (u unixPortConn) Close() error { return nil }
