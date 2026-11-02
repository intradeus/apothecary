package main

// The gateway container doubles as the project's registry host. It runs up to
// two `registry serve` children (the distribution binary, baked into the
// image), and both exist because a run's inner daemon is thrown away with the
// run:
//
//   :5000  pull-through Docker Hub cache — every run's dockerd points
//          --registry-mirror at it, so one project's runs warm each other's
//          image pulls instead of each cold-pulling into its own per-run
//          /var/lib/docker volume.
//   :5001  writable BUILD CACHE — buildkit exports the devcontainer build's
//          cache here (--cache-to) and imports it on the next run
//          (--cache-from). The mirror cannot double as this one: a registry in
//          proxy mode refuses pushes.
//
// Why it lives HERE rather than in a sibling container: the gateway is already
// per-project, already multi-homed onto every run's --internal network, and its
// nft ruleset only hooks prerouting/forward — so a gateway-local listener is
// reachable from every run with no new rules, and the registry's own upstream
// fetch (gateway-originated, output path) is unfiltered by construction. That
// last part is a deliberate policy carve-out, documented in README. The
// extension gallery on :5002 (gallery.go) is the third such cache.

import (
	"bufio"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// mirrorRegistryConfig configures the pull-through Docker Hub cache.
// proxy.remoteurl is what makes it a read-through mirror rather than a
// registry; delete.enabled lets the proxy scheduler's TTL actually evict.
const mirrorRegistryConfig = `version: 0.1
log:
  level: warn
storage:
  filesystem:
    rootdirectory: /var/lib/registry
  delete:
    enabled: true
  cache:
    blobdescriptor: inmemory
http:
  addr: :5000
  secret: apothecary-mirror
proxy:
  remoteurl: https://registry-1.docker.io
`

// buildCacheRegistryConfig is the WRITABLE registry: no proxy section (a
// registry in proxy mode refuses pushes, which is exactly what a build cache
// needs to do), its own storage root, and delete enabled so a wipe can be
// surgical if it ever needs to be.
func buildCacheRegistryConfig(dir string) string {
	return `version: 0.1
log:
  level: warn
storage:
  filesystem:
    rootdirectory: ` + dir + `
  delete:
    enabled: true
  cache:
    blobdescriptor: inmemory
http:
  addr: :5001
  secret: apothecary-buildcache
`
}

// writeIfChanged writes content only when the file differs: the supervisor
// restarts its registry child on any mtime bump, so a blind rewrite at every
// start would restart it for nothing.
func writeIfChanged(path, content string) error {
	if cur, err := os.ReadFile(path); err == nil && string(cur) == content {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// registryBin is the distribution binary COPYed in by the gateway Dockerfile
// (swapped in tests).
var registryBin = "registry"

const (
	registryMinBackoff = time.Second
	registryMaxBackoff = 30 * time.Second
	// registryHealthy is how long a child must run before its exit is treated
	// as a fresh failure (backoff reset) rather than a crash loop.
	registryHealthy = 30 * time.Second
	// registryStopGrace bounds a SIGTERM'd child before it is killed.
	registryStopGrace = 5 * time.Second
)

// superviseRegistry runs `registry serve <cfg>` forever: it restarts the child
// on exit with capped backoff, and restarts it when the config file changes
// (the distribution registry has no config hot-reload, unlike our policy file).
// The config-change restart is also how the engine wipes the build cache: it
// empties the storage and touches the config, and the child comes back with a
// clean slate rather than a view of deleted files.
//
// Child output is relayed under the given label so two registries stay
// distinguishable in one log stream. Never returns; run it in a goroutine.
func superviseRegistry(label, cfgPath string) {
	backoff := registryMinBackoff
	cfgMod, _ := mtime(cfgPath)
	for {
		ranFor, cfgChanged := runRegistry(label, cfgPath, &cfgMod)
		if cfgChanged {
			// An operator-driven restart, not a failure: come back immediately
			// and with a clean slate.
			backoff = registryMinBackoff
			continue
		}
		if ranFor > registryHealthy {
			backoff = registryMinBackoff
		}
		slog.Info(label+": restarting", "in", backoff)
		time.Sleep(backoff)
		backoff = nextBackoff(backoff)
	}
}

// runRegistry starts one child and blocks until it exits or the config file
// changes under it (in which case the child is stopped). It reports how long
// the child ran and whether the config changed, and advances cfgMod.
func runRegistry(label, cfgPath string, cfgMod *time.Time) (ranFor time.Duration, cfgChanged bool) {
	start := time.Now()
	cmd := exec.Command(registryBin, "serve", cfgPath)
	// One merged stream: the registry writes its own logs to stderr, and the
	// prefix is what distinguishes them in `docker logs apo-gw-<project>`.
	out, err := cmd.StdoutPipe()
	if err != nil {
		slog.Warn(label+": pipe", "err", err)
		return 0, false
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		slog.Warn(label+": start", "err", err)
		return 0, false
	}
	go relayLines(label, out)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			slog.Warn(label+": exited", "after", time.Since(start).Truncate(time.Second), "err", err)
			return time.Since(start), false
		case <-tick.C:
			mt, ok := mtime(cfgPath)
			if !ok || !mt.After(*cfgMod) {
				continue
			}
			*cfgMod = mt
			slog.Info(label + ": config changed, restarting child")
			stopChild(cmd, done)
			return time.Since(start), true
		}
	}
}

// stopChild asks the child to exit, killing it if it overstays the grace
// period. It always drains done, so the Wait goroutine never leaks.
func stopChild(cmd *exec.Cmd, done <-chan error) {
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(registryStopGrace):
		_ = cmd.Process.Kill()
		<-done
	}
}

// relayLines echoes the child's output under a prefix until the pipe closes.
func relayLines(label string, r interface{ Read([]byte) (int, error) }) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 8<<10), 64<<10) // registry logs can be long lines
	for sc.Scan() {
		slog.Info(label + ": " + sc.Text())
	}
}

// nextBackoff doubles up to the cap.
func nextBackoff(prev time.Duration) time.Duration {
	next := prev * 2
	if next > registryMaxBackoff {
		return registryMaxBackoff
	}
	if next < registryMinBackoff {
		return registryMinBackoff
	}
	return next
}
