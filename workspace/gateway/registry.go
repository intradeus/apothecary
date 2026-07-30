package main

// The gateway container doubles as the project's pull-through Docker Hub cache:
// a `registry serve` child (the distribution binary, baked into the image)
// listens on :5000 and every run's inner dockerd points --registry-mirror at
// it, so one project's runs warm each other's image pulls instead of each
// cold-pulling into its own per-run /var/lib/docker volume.
//
// Why it lives HERE rather than in a sibling container: the gateway is already
// per-project, already multi-homed onto every run's --internal network, and its
// nft ruleset only hooks prerouting/forward — so a gateway-local listener is
// reachable from every run with no new rules, and the registry's own upstream
// fetch (gateway-originated, output path) is unfiltered by construction. That
// last part is a deliberate policy carve-out, documented in README.

import (
	"bufio"
	"log"
	"os/exec"
	"syscall"
	"time"
)

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
// Child output is relayed under a "registry:" prefix so the container's logs
// stay one stream. Never returns; run it in a goroutine.
func superviseRegistry(cfgPath string) {
	backoff := registryMinBackoff
	cfgMod, _ := mtime(cfgPath)
	for {
		ranFor, cfgChanged := runRegistry(cfgPath, &cfgMod)
		if cfgChanged {
			// An operator-driven restart, not a failure: come back immediately
			// and with a clean slate.
			backoff = registryMinBackoff
			continue
		}
		if ranFor > registryHealthy {
			backoff = registryMinBackoff
		}
		log.Printf("registry: restarting in %s", backoff)
		time.Sleep(backoff)
		backoff = nextBackoff(backoff)
	}
}

// runRegistry starts one child and blocks until it exits or the config file
// changes under it (in which case the child is stopped). It reports how long
// the child ran and whether the config changed, and advances cfgMod.
func runRegistry(cfgPath string, cfgMod *time.Time) (ranFor time.Duration, cfgChanged bool) {
	start := time.Now()
	cmd := exec.Command(registryBin, "serve", cfgPath)
	// One merged stream: the registry writes its own logs to stderr, and the
	// prefix is what distinguishes them in `docker logs apo-gw-<project>`.
	out, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("registry: pipe: %v", err)
		return 0, false
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		log.Printf("registry: start: %v", err)
		return 0, false
	}
	go relayLines(out)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			log.Printf("registry: exited after %s: %v", time.Since(start).Truncate(time.Second), err)
			return time.Since(start), false
		case <-tick.C:
			mt, ok := mtime(cfgPath)
			if !ok || !mt.After(*cfgMod) {
				continue
			}
			*cfgMod = mt
			log.Printf("registry: config changed, restarting child")
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
func relayLines(r interface{ Read([]byte) (int, error) }) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 8<<10), 64<<10) // registry logs can be long lines
	for sc.Scan() {
		log.Printf("registry: %s", sc.Text())
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
