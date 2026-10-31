package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"apothecary/internal/egressgw"
	"apothecary/internal/platform/shell"
	"apothecary/internal/ports/envport"
)

// dockerHost runs each sandbox as a Docker-in-Docker container on the engine's
// own docker daemon (sandbox.go). The run folder is bind-mounted into the
// sandbox at the identical path, so the engine works on the checkout with
// plain file I/O and reaches the inner daemon, the engine bridge and the
// workspace's sockets through unix sockets in that folder (bridge.go) — the
// only channel hardened docker hosts leave open between containers and the
// host. The sandbox lifecycle methods stay on Adapter (they predate the seam
// and the unit tests drive them directly); this type is the seam's view.
type dockerHost struct{ a *Adapter }

func (h dockerHost) prepare(ctx context.Context, name string, spec envport.EnvSpec, plog func(string, ...any), detect func(context.Context, string) (Definition, error)) (prep, error) {
	a := h.a
	src, err := a.ensureSrc(ctx, name, spec)
	if err != nil {
		return prep{}, err
	}
	def, err := detect(ctx, src)
	if err != nil {
		return prep{}, err
	}
	shimDir, err := a.ensureShim(ctx, name)
	if err != nil {
		return prep{}, err
	}
	// The tools bundle: cached per engine version; the first provision after
	// an upgrade pays for the build unless cmdServe warmed it at boot.
	plog("→ preparing the tools bundle (first time after an upgrade: a download of every harness + code-server)\n")
	toolsDir, err := a.ensureTools(ctx)
	if err != nil {
		return prep{}, err
	}
	// Egress firewall: the run's DinD sandbox routes ALL egress (the workspace
	// AND every compose sibling) through the per-project gateway
	// (internal/egressgw). Bring it up BEFORE the sandbox so the sandbox can join
	// the run's --internal network and default-route into its tunnel to the
	// gateway — which is also why a privileged sandbox can't bypass it (its only
	// NIC has no external route).
	plog("→ bringing up per-run sandbox + egress gateway\n")
	var att egressgw.Attachment
	var gwIP string
	if a.gw != nil {
		var gerr error
		att, gerr = a.gw.EnsureRunEgress(ctx, spec.Project, spec.ProjectName, name, policyFor(spec))
		if gerr != nil {
			return prep{}, gerr
		}
		gwIP = att.GatewayIP
		// Record the owning project NOW (not after the build): if the build is
		// blocked on a denied domain, "add to egress" must be able to find this
		// run's gateway mid-provision to unblock it.
		a.recordGateway(name, spec.Project, gwIP)
	}
	// Each run gets its own nested docker daemon. Everything from here —
	// devcontainer up, exec, docker ps/inspect/rename — targets that inner
	// daemon (via runInner), so the repo's compose runs verbatim with no
	// host-daemon collisions and Destroy is total.
	if err := a.ensureSandbox(ctx, name, spec, att, filepath.Dir(toolsDir)); err != nil {
		return prep{}, err
	}
	return prep{src: src, shimDir: shimDir, toolsDir: toolsDir, gwIP: gwIP, def: def}, nil
}

// runInner is a.run against the run's inner daemon (host daemon for legacy
// envs — dockerEnv nil). extraEnv is appended after the daemon selector.
func (h dockerHost) runInner(ctx context.Context, name, bin string, args []string, stdin []byte, tee io.Writer, extraEnv ...string) ([]byte, []byte, int, error) {
	return h.a.run(ctx, bin, args, stdin, tee, append(h.a.dockerEnv(name), extraEnv...)...)
}

func (dockerHost) fs(string) sandboxFS { return osFS{} }

func (h dockerHost) sandboxState(ctx context.Context, name string) (string, bool, error) {
	st, err := h.a.sandboxState(ctx, name)
	if err != nil {
		return "", false, err
	}
	// No sandbox and no inner socket: a pre-DinD env, whose workspace lives
	// on the host daemon itself.
	return st, st == "" && h.a.dockerEnv(name) == nil, nil
}

func (h dockerHost) destroySandbox(ctx context.Context, name string) error {
	return h.a.destroyDockerSandbox(ctx, name)
}

func (h dockerHost) ensureBridge(env envport.EnvID) error { return h.a.ensureBridge(env) }
func (h dockerHost) closeBridge(env envport.EnvID)        { h.a.closeBridge(env) }

func (h dockerHost) bridgePorts() []int {
	var out []int
	for _, p := range h.a.bridgeSocks() {
		out = append(out, p)
	}
	return out
}

// startBridgeRelay launches the mounted static shim's relay detached inside
// the container: the workspace's own 127.0.0.1:<engine port> → engine.sock.
func (h dockerHost) startBridgeRelay(ctx context.Context, name, cid string) error {
	a := h.a
	args := append([]string{"exec", "-d", cid, "/opt/apothecary/shim", "relay"}, a.relayPairs()...)
	_, stderr, code, err := a.runInner(ctx, name, a.cfg.DockerBin, args, nil, nil)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("start relay in %s: exit %d: %s", name, code, shell.Tail(string(stderr), 512))
	}
	return nil
}

// engineURL: loopback parity — the relay re-exposes the engine port on the
// workspace's own loopback.
func (h dockerHost) engineURL() string {
	return "http://127.0.0.1:" + strconv.Itoa(h.a.cfg.EnginePort)
}

func (h dockerHost) dial(name, sock string) envport.PortConn {
	return unixPortConn{path: filepath.Join(h.a.folderFor(name), "shim", sock)}
}

// waitSock: the relay creates the socket in-container; the bind mount
// surfaces it here.
func (h dockerHost) waitSock(ctx context.Context, name, sock string) error {
	path := filepath.Join(h.a.folderFor(name), "shim", sock)
	for range 50 {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("%s never appeared", sock)
}

func (h dockerHost) refreshShim(ctx context.Context, name string) error {
	_, err := h.a.ensureShim(ctx, name)
	return err
}

func (h dockerHost) sample(ctx context.Context, name string) (envport.ResourceSample, error) {
	return h.a.dockerSample(ctx, name)
}

func (h dockerHost) test(ctx context.Context) error { return h.a.dockerTest(ctx) }

// close stops every engine-side bridge listener (engine shutdown; workspaces
// keep running and Reattach rebuilds bridges on the next boot).
func (h dockerHost) close() {
	a := h.a
	a.bridgeMu.Lock()
	defer a.bridgeMu.Unlock()
	for env, lns := range a.bridges {
		for _, ln := range lns {
			_ = ln.Close()
		}
		delete(a.bridges, env)
	}
}

var _ Host = dockerHost{}
