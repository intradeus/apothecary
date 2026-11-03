package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// The WireGuard run link. Each run's sandbox brings up its own wg0 whose
// default route is a peer of this gateway's wg0: everything the sandbox (and
// every container inside it) sends arrives here on wg0 — "not EXT" — and
// meets the nft REDIRECT, filter, MASQUERADE and drop backstop. On
// Kubernetes a pod has no docker network to be multi-homed onto, and its
// network policy only lets it talk to this gateway's WireGuard port (and the
// engine), so the tunnel is the only way out. On docker the run's --internal
// network still carries the tunnel itself, but the egress no longer crosses
// the host's bridge addressed to the internet: a host with bridge netfilter
// on (br_netfilter, loaded by a local k3s) drops that before it gets here.
//
// Each peer is allowed exactly its /32 (attributed to its run), and cryptokey
// routing makes it unforgeable — a sandbox cannot send as another run.

const wgDev = "wg0"

type wgLink struct {
	mtu int
	run func(name string, args ...string) (string, error) // swapped in tests
}

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// wgKeyDir is where wg reads its private key from: /etc/wireguard, the one
// place a host's AppArmor profile for /usr/bin/wg lets it read keys (Ubuntu
// 26.04 ships one, /etc/apparmor.d/wg, and it attaches to a container's wg
// too when the container runs unconfined). Swapped in tests.
var wgKeyDir = "/etc/wireguard"

// stageWGKey writes the private key — from file (--wg-key) or else env
// ($APO_GW_WG_KEY) — to wgKeyDir/wg0.key, readable by this process only,
// and returns that path for wg.
func stageWGKey(file, env string) (string, error) {
	key := env
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		key = string(b)
	}
	if err := os.MkdirAll(wgKeyDir, 0o700); err != nil {
		return "", err
	}
	path := wgKeyDir + "/" + wgDev + ".key"
	if err := os.WriteFile(path, []byte(strings.TrimSpace(key)+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// setupWG creates wg0 (idempotently: a restarted container gets a fresh
// netns, a restarted daemon finds its own wg0 and reuses it).
func setupWG(ext, keyFile string, listen int, addr string, mtu int) (*wgLink, error) {
	l := &wgLink{run: runCmd}
	if mtu <= 0 {
		mtu = uplinkMTU(ext) - 60 // IPv4 outer header 20 + UDP 8 + WireGuard 32
	}
	l.mtu = mtu
	if _, err := l.run("ip", "link", "show", wgDev); err != nil {
		if _, err := l.run("ip", "link", "add", wgDev, "type", "wireguard"); err != nil {
			return nil, err
		}
	}
	if _, err := l.run("wg", "set", wgDev, "private-key", keyFile, "listen-port", strconv.Itoa(listen)); err != nil {
		var ee *exec.ExitError
		code := -1
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return nil, fmt.Errorf("wg set %s private-key %s listen-port %d: exit %d (output withheld: wg quotes a malformed key)", wgDev, keyFile, listen, code)
	}
	if _, err := l.run("ip", "address", "replace", addr, "dev", wgDev); err != nil {
		return nil, err
	}
	if _, err := l.run("ip", "link", "set", wgDev, "mtu", strconv.Itoa(mtu), "up"); err != nil {
		return nil, err
	}
	return l, nil
}

// sysClassNet is the kernel's network-interface tree (swapped in tests).
var sysClassNet = "/sys/class/net"

// uplinkMTU reads the uplink's MTU (1500 when unreadable).
func uplinkMTU(ext string) int {
	b, err := os.ReadFile(sysClassNet + "/" + ext + "/mtu")
	if err != nil {
		return 1500
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 1280 {
		return 1500
	}
	return n
}

// reconcilePeers makes wg0's peer set exactly peers: adds or updates each one
// (allowed only its own /32) and removes any peer no longer routed.
func (l *wgLink) reconcilePeers(peers map[string]Peer) {
	out, err := l.run("wg", "show", wgDev, "peers")
	if err != nil {
		slog.Warn("wg: list peers", "err", err)
		return
	}
	have := map[string]bool{}
	for _, k := range strings.Fields(out) {
		have[k] = true
	}
	want := map[string]bool{}
	runs := make([]string, 0, len(peers))
	for run := range peers {
		runs = append(runs, run)
	}
	sort.Strings(runs) // deterministic command order (tests, logs)
	for _, run := range runs {
		p := peers[run]
		if p.PublicKey == "" || p.IP == "" {
			continue
		}
		want[p.PublicKey] = true
		if _, err := l.run("wg", "set", wgDev, "peer", p.PublicKey, "allowed-ips", p.IP+"/32"); err != nil {
			slog.Warn("wg: peer", "ip", p.IP, "run_id", run, "err", err)
		}
	}
	var stale []string
	for k := range have {
		if !want[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	for _, k := range stale {
		if _, err := l.run("wg", "set", wgDev, "peer", k, "remove"); err != nil {
			slog.Warn("wg: remove peer", "err", err)
		}
	}
}
