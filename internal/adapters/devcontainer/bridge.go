package devcontainer

import (
	"context"

	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
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

// bridgeSocks maps engine loopback ports to socket names in the shim dir. Egress
// no longer rides a bridged proxy socket — it is filtered externally by the
// per-project gateway — so only the engine port is bridged.
func (a *Adapter) bridgeSocks() map[string]int {
	return map[string]int{"engine.sock": a.cfg.EnginePort}
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

// containerID resolves the env's running container via the id label (on the
// run's inner daemon).
func (a *Adapter) containerID(ctx context.Context, name string) (string, error) {
	cidOut, _, code, err := a.runInner(ctx, name, a.cfg.DockerBin,
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

// relayTarget picks the host the in-container relay should dial for port.
//
// The relay runs inside the WORKSPACE container, so its loopback only reaches
// what the workspace itself serves. In a compose devcontainer the interesting
// ports often belong to SIBLING services (a database, a cache): those have
// their own network namespace and answer at <service>:<port> — the compose
// service name is a DNS alias on the shared network — never on the workspace's
// 127.0.0.1. Relaying to loopback there just gets connection-refused.
//
// Siblings are matched on declared image ports (Config.ExposedPorts), not a
// runtime probe: a service's port is declarative, while an app's dev-server
// port is started at runtime and never declared — so an unmatched port
// correctly falls back to loopback, preserving today's behavior for
// non-compose workspaces and for the workspace's own ports.
func (a *Adapter) relayTarget(ctx context.Context, name, cid string, port int) string {
	const loopback = "127.0.0.1"
	project := a.inspectField(ctx, name, cid, `{{index .Config.Labels "com.docker.compose.project"}}`)
	if project == "" {
		return loopback // not a compose devcontainer: nothing else to reach
	}
	out, _, code, err := a.runInner(ctx, name, a.cfg.DockerBin,
		[]string{"ps", "-q", "--filter", "label=com.docker.compose.project=" + project}, nil, nil)
	if err != nil || code != 0 {
		return loopback
	}
	var candidates []string
	for _, sib := range strings.Fields(string(out)) {
		if strings.HasPrefix(cid, sib) || strings.HasPrefix(sib, cid) {
			continue // the workspace container itself
		}
		meta := a.inspectField(ctx, name, sib,
			`{{index .Config.Labels "com.docker.compose.service"}} {{json .Config.ExposedPorts}}`)
		svc, exposed, _ := strings.Cut(meta, " ")
		if svc == "" || !strings.Contains(exposed, fmt.Sprintf("%q", strconv.Itoa(port)+"/tcp")) {
			continue
		}
		candidates = append(candidates, svc)
	}
	if len(candidates) == 0 {
		// No service DECLARES this port. It may still be a runtime listener a
		// sibling scan discovered (an undeclared db, a service that binds a
		// port it never exposed in its image) — route there if we saw it.
		if svc := a.scannedSibling(ports.EnvID(name), port); svc != "" {
			return svc
		}
		return loopback
	}
	// Deterministic on ties, and loud about it — silently guessing between two
	// services would be a confusing way to reach the wrong database.
	sort.Strings(candidates)
	if len(candidates) > 1 {
		slog.Warn("devcontainer: several compose services declare this port; relaying to the first",
			"port", port, "services", candidates, "chosen", candidates[0])
	}
	return candidates[0]
}

// scannedSibling returns the compose service a prior ScanSiblingPorts saw
// listening on port, or "" — the expose-routing supplement for undeclared
// sibling ports.
func (a *Adapter) scannedSibling(env ports.EnvID, port int) string {
	a.sibPortsMu.Lock()
	defer a.sibPortsMu.Unlock()
	return a.sibPorts[env][port]
}

// ScanSiblingPorts (ports.SiblingPortScanner) discovers listeners inside the
// run's compose SIBLING containers — a database or cache the in-workspace
// scanner cannot see. Only wildcard binds (0.0.0.0/::) are reported: a
// loopback-only listener in a sibling is unreachable across containers, so
// listing it would only offer an expose that connection-refuses. Results also
// prime the expose-routing cache (see scannedSibling). Best-effort per
// sibling: no shell, no cat, or no procfs just yields nothing.
func (a *Adapter) ScanSiblingPorts(ctx context.Context, env ports.EnvID) []ports.ObservedPort {
	name := string(env)
	cid, err := a.containerID(ctx, name)
	if err != nil {
		return nil
	}
	project := a.inspectField(ctx, name, cid, `{{index .Config.Labels "com.docker.compose.project"}}`)
	if project == "" {
		return nil // not a compose devcontainer: no siblings
	}
	out, _, code, err := a.runInner(ctx, name, a.cfg.DockerBin,
		[]string{"ps", "-q", "--filter", "label=com.docker.compose.project=" + project}, nil, nil)
	if err != nil || code != 0 {
		return nil
	}
	exclude := map[int]bool{}
	for _, p := range a.bridgeSocks() {
		exclude[p] = true
	}
	var observed []ports.ObservedPort
	routes := map[int]string{}
	for _, sib := range strings.Fields(string(out)) {
		if strings.HasPrefix(cid, sib) || strings.HasPrefix(sib, cid) {
			continue // the workspace container itself — scanned in-container
		}
		svc := a.inspectField(ctx, name, sib, `{{index .Config.Labels "com.docker.compose.service"}}`)
		if svc == "" {
			continue
		}
		tables, _, code, err := a.runInner(ctx, name, a.cfg.DockerBin,
			[]string{"exec", sib, "sh", "-c", "cat /proc/net/tcp /proc/net/tcp6 2>/dev/null"}, nil, nil)
		if err != nil || code != 0 {
			continue // scratch image, no shell, exec disabled — skip silently
		}
		for _, port := range parseWildcardListeners(string(tables)) {
			if port < 9 || exclude[port] {
				continue
			}
			if _, seen := routes[port]; seen {
				continue // first sibling wins the port (deterministic via ps order)
			}
			routes[port] = svc
			observed = append(observed, ports.ObservedPort{Port: port, Service: svc})
		}
	}
	a.sibPortsMu.Lock()
	a.sibPorts[env] = routes
	a.sibPortsMu.Unlock()
	sort.Slice(observed, func(i, j int) bool { return observed[i].Port < observed[j].Port })
	return observed
}

// parseWildcardListeners returns the ports of LISTEN (st=0A) rows whose local
// address is a wildcard bind (0.0.0.0 or ::) in a /proc/net/tcp{,6} table —
// the sibling listeners actually reachable from another container.
func parseWildcardListeners(table string) []int {
	var out []int
	seen := map[int]bool{}
	lines := strings.Split(table, "\n")
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) < 4 || f[3] != "0A" {
			continue
		}
		hexIP, hexPort, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		// Wildcard only: all-zero v4 (8 chars) or v6 (32 chars).
		if hexIP != strings.Repeat("0", len(hexIP)) || (len(hexIP) != 8 && len(hexIP) != 32) {
			continue
		}
		port, err := strconv.ParseInt(hexPort, 16, 32)
		if err != nil || port < 1 || port > 65535 || seen[int(port)] {
			continue
		}
		seen[int(port)] = true
		out = append(out, int(port))
	}
	return out
}

// inspectField returns one `docker inspect --format` value from the run's
// inner daemon, or "" on any failure (callers treat that as "unknown" and
// fall back).
func (a *Adapter) inspectField(ctx context.Context, name, ref, format string) string {
	out, _, code, err := a.runInner(ctx, name, a.cfg.DockerBin,
		[]string{"inspect", ref, "--format", format}, nil, nil)
	if err != nil || code != 0 {
		return ""
	}
	v := strings.TrimSpace(string(out))
	if v == "<no value>" {
		return ""
	}
	return v
}

// startRelay launches the mounted static shim's relay detached inside the
// container — no runtime dependency on anything in the workspace image. It
// also starts the port-discovery scanner alongside it (same shim binary,
// same detached-exec model, same reattach-driven respawn).
func (a *Adapter) startRelay(ctx context.Context, name string) error {
	cid, err := a.containerID(ctx, name)
	if err != nil {
		return err
	}
	args := append([]string{"exec", "-d", cid, "/opt/apothecary/shim", "relay"}, a.relayPairs()...)
	_, stderr, code, err := a.runInner(ctx, name, a.cfg.DockerBin, args, nil, nil)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("start relay in %s: exit %d: %s", name, code, tail(string(stderr), 512))
	}
	a.startPortScan(ctx, name, cid)
	return nil
}

// startPortScan launches the shim's listening-port discovery loop detached.
// Best-effort and auxiliary: a failure is logged, never fatal to provision.
// The scanner's own singleton guard makes a re-run (reattach after an engine
// restart where the container survived) a quiet no-op. The bridge's own
// forward-relay ports are excluded so they never show up as "discovered".
func (a *Adapter) startPortScan(ctx context.Context, name, cid string) {
	var excl []string
	for _, port := range a.bridgeSocks() {
		excl = append(excl, strconv.Itoa(port))
	}
	// code-server has its own "Open editor" affordance — don't also surface it
	// as a discovered app port.
	excl = append(excl, strconv.Itoa(ports.CodeServerPort))
	args := []string{"exec", "-d", cid, "/opt/apothecary/shim", "portscan", "--exclude", strings.Join(excl, ",")}
	_, stderr, code, err := a.runInner(ctx, name, a.cfg.DockerBin, args, nil, nil)
	if err != nil || code != 0 {
		slog.Warn("devcontainer: port discovery scanner did not start",
			"env", name, "exit", code, "err", err, "stderr", tail(string(stderr), 256))
	}
}

// StartEditor (ports.EditorStarter) launches code-server in the workspace on
// the given loopback port, ready to be exposed. Lazy + idempotent: it first
// probes the port (image-agnostic, via the static shim) and no-ops if
// code-server is already listening; otherwise it starts it detached and waits
// for the listener. code-server itself is installed by the injected feature;
// the baked --extensions-dir carries the pre-installed extensions. It opens
// $HOME/repo (the checkout symlink) — a shell expands $HOME for the container's
// own user.
func (a *Adapter) StartEditor(ctx context.Context, env ports.EnvID, port int) error {
	name := string(env)
	cid, err := a.containerID(ctx, name)
	if err != nil {
		return err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	// Already up? (e.g. a prior Open editor, or code-server survived a plain
	// engine restart.)
	if _, _, code, _ := a.runInner(ctx, name, a.cfg.DockerBin,
		[]string{"exec", cid, "/opt/apothecary/shim", "probe", addr}, nil, nil); code == 0 {
		return nil
	}
	start := fmt.Sprintf(
		`exec code-server --auth none --disable-telemetry --bind-addr %s --extensions-dir %s "$HOME/repo"`,
		addr, codeServerExtDir)
	if _, stderr, code, err := a.runInner(ctx, name, a.cfg.DockerBin,
		[]string{"exec", "-d", cid, "sh", "-c", start}, nil, nil); err != nil || code != 0 {
		return fmt.Errorf("start code-server in %s: exit %d: %s", name, code, tail(string(stderr), 512))
	}
	// Wait (bounded) for the listener before the engine exposes it, so the
	// first browser load succeeds.
	if _, _, code, err := a.runInner(ctx, name, a.cfg.DockerBin,
		[]string{"exec", cid, "/opt/apothecary/shim", "probe", "--wait", "20s", addr}, nil, nil); err != nil || code != 0 {
		return fmt.Errorf("code-server in %s did not start listening on %d within 20s", name, port)
	}
	return nil
}

// --- ports.WorkspacePortExposer ---------------------------------------------

// AppPorts: the ports the repo's devcontainer declares (forwardPorts /
// appPort) — auto-exposed by the engine at env_ready.
func (a *Adapter) AppPorts(ctx context.Context, env ports.EnvID) []int {
	return a.declaredPorts(ctx, string(env))
}

// ExposePort reaches the port through a REVERSE relay: the shim listens on a
// bind-mounted unix socket in-container and pipes each connection to whoever
// serves that port (the workspace itself, or a sibling compose service — see
// relayTarget); the engine dials the socket file host-side. Same channel as
// the engine bridge, so it works on hosts that drop container→host IP traffic
// in either direction.
func (a *Adapter) ExposePort(ctx context.Context, env ports.EnvID, port int) (ports.PortConn, error) {
	name := string(env)
	sock := fmt.Sprintf("app-%d.sock", port)
	hostPath := filepath.Join(a.folderFor(name), "shim", sock)
	cid, err := a.containerID(ctx, name)
	if err != nil {
		return nil, err
	}
	spec := fmt.Sprintf("/opt/apothecary/%s:%s:%d", sock, a.relayTarget(ctx, name, cid, port), port)
	_, stderr, code, err := a.runInner(ctx, name, a.cfg.DockerBin,
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
