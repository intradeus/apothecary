package engine

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"apothecary/internal/ports"
)

// Post-provisioning egress check: a freshly provisioned workspace proves it
// can leave through the egress gateway before any stage runs. Without it a
// broken path is silent — the bootstrap may never touch the network (a
// clone-first provisioner checks out engine-side) and the first agent turn
// hangs on its model request until the stage timeout.

// egressProbeWait bounds the probe's retries: long enough to ride out a
// gateway still settling, short next to the hang it replaces.
const egressProbeWait = 15 * time.Second

// egressProbeTarget is the host:port the check connects to: the repository's
// host — every run must reach it to push, so every policy allows it — else
// the first concrete allowlisted domain. tls: an https endpoint, so the probe
// completes a handshake and proves the gateway let the host through, not just
// that the gateway accepted the connection. Empty: nothing to probe.
func egressProbeTarget(res *Resolution) (addr string, tls bool) {
	if u, err := url.Parse(res.RepoRef.URL); err == nil && u.Hostname() != "" {
		switch u.Scheme {
		case "https":
			return net.JoinHostPort(u.Hostname(), portOr(u.Port(), "443")), true
		case "http":
			return net.JoinHostPort(u.Hostname(), portOr(u.Port(), "80")), false
		}
	}
	for _, d := range res.EgressDomains {
		if !strings.HasPrefix(d, "*.") {
			return net.JoinHostPort(d, "443"), true
		}
	}
	return "", false
}

func portOr(p, def string) string {
	if p == "" {
		return def
	}
	return p
}

// probeEgress runs the check in the workspace through the shim (pure Go, so
// it works whatever the image carries). A non-nil error is the run's
// env_failed reason.
func probeEgress(ctx context.Context, envp ports.EnvProvisioner, id ports.EnvID, res *Resolution) error {
	addr, withTLS := egressProbeTarget(res)
	if addr == "" {
		return nil
	}
	script := "/opt/apothecary/shim probe --wait " + egressProbeWait.String()
	if withTLS {
		script += " --tls"
	}
	script += " " + shellQuote(addr)
	out, err := envp.Exec(ctx, id, ports.Command{Script: script, Timeout: egressProbeWait + time.Minute})
	if err != nil {
		return fmt.Errorf("egress check: %w", err)
	}
	if out.ExitCode == 0 {
		return nil
	}
	why := strings.TrimSpace(tail(out.Output, 400))
	if why == "" {
		why = fmt.Sprintf("exit %d", out.ExitCode)
	}
	return fmt.Errorf("egress check: the workspace cannot reach %s (%s), so no agent could reach its model provider "+
		"or the repository. If the egress policy allows that host, the path to the project's egress gateway is "+
		"broken: check the run's WireGuard tunnel (wg0 in the sandbox) and the gateway's log", addr, why)
}
