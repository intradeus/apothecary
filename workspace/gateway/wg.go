package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
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
//
// Nor reach one: every run of the project is a peer on this one wg0, so the
// tunnel's subnet joins the protected destinations (main) — the forward chain
// drops a peer's packets addressed to another peer, and the filter never
// dials one, whatever the policy says.

const wgDev = "wg0"

// The two WireGuards wg0 may be: the kernel's, or wireguard-go over TUN on a
// kernel without the module (a Kata guest's: the gateway of a k3d cluster
// inside a Kata workspace runs on that guest's kernel). Every wg and ip call
// after the link exists is the same on both; the sandbox's apo-wg-up makes
// the same choice, and either side interoperates with the other.
const (
	wgKernel    = "kernel"
	wgUserspace = "userspace"
)

// wgUserspaceEnv forces wireguard-go on a kernel that has the module: the
// live tests' switch (the egress-gw adapter sets it), so CI runs both.
const wgUserspaceEnv = "APO_WG_USERSPACE"

type wgLink struct {
	mtu int
	// subnet is wg0's network: the run links' addresses, which no run may
	// reach through the gateway.
	subnet *net.IPNet
	run    func(name string, args ...string) (string, error) // swapped in tests
	// mode is the WireGuard wg0 is (wgKernel or wgUserspace); kernelWhy the
	// kernel's refusal that made it userspace, said again if that fails too.
	mode, kernelWhy string

	// What setupWG configured, put back on a wireguard-go that died: wg0,
	// its keys, address and peers all go with the process.
	keyFile, addr string
	listen        int

	mu    sync.Mutex      // serializes setting wg0 up against peer changes
	peers map[string]Peer // the peer set last reconciled, for a restart
	wgo   *wgProc         // the running wireguard-go (userspace only)
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

// The userspace link's paths and pacing (swapped in tests): the TUN device
// wireguard-go opens; the directory of its control socket, which wg must
// reach; the binary; how long it gets to bring up wg0; how long a restart
// waits after a failed one.
var (
	tunPath          = "/dev/net/tun"
	wgRunDir         = "/var/run/wireguard"
	wireguardGo      = "wireguard-go"
	wgUpTimeout      = 5 * time.Second
	wgRestartBackoff = 2 * time.Second
)

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
// netns, a restarted daemon finds its own wg0 and reuses it): the kernel's
// WireGuard, else wireguard-go when the kernel refuses the link type, or
// always wireguard-go when userspace is asked for. A userspace link needs
// superviseWG running, which puts it back if wireguard-go dies.
func setupWG(ext, keyFile string, listen int, addr string, mtu int, userspace bool) (*wgLink, error) {
	_, subnet, err := net.ParseCIDR(addr)
	if err != nil {
		return nil, fmt.Errorf("wg address %q: %w", addr, err)
	}
	l := &wgLink{run: runCmd, subnet: subnet, keyFile: keyFile, listen: listen, addr: addr, mode: wgKernel}
	if userspace {
		l.mode = wgUserspace
	}
	if mtu <= 0 {
		mtu = uplinkMTU(ext) - 60 // IPv4 outer header 20 + UDP 8 + WireGuard 32
	}
	l.mtu = mtu
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.up(); err != nil {
		l.stopUserspace()
		return nil, err
	}
	return l, nil
}

// up brings wg0 into being, if it is not, and configures it. Called with
// l.mu held.
func (l *wgLink) up() error {
	if l.mode == wgKernel {
		if _, err := l.run("ip", "link", "show", wgDev); err != nil {
			if _, err := l.run("ip", "link", "add", wgDev, "type", "wireguard"); err != nil {
				// The kernel's own words: "Unknown device type" without
				// the module, "Operation not permitted" without NET_ADMIN.
				l.kernelWhy = err.Error()
				slog.Warn("gateway: no kernel WireGuard link; falling back to wireguard-go", "err", err)
				l.mode = wgUserspace
			}
		}
	}
	if l.mode == wgUserspace {
		if _, err := l.run("ip", "link", "show", wgDev); err != nil {
			if err := l.startUserspace(); err != nil {
				if l.kernelWhy != "" {
					return fmt.Errorf("%w (the kernel's WireGuard was refused first: %s)", err, l.kernelWhy)
				}
				return err
			}
		}
	}
	if _, err := l.run("wg", "set", wgDev, "private-key", l.keyFile, "listen-port", strconv.Itoa(l.listen)); err != nil {
		var ee *exec.ExitError
		code := -1
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return fmt.Errorf("wg set %s private-key %s listen-port %d: exit %d (output withheld: wg quotes a malformed key)", wgDev, l.keyFile, l.listen, code)
	}
	if _, err := l.run("ip", "address", "replace", l.addr, "dev", wgDev); err != nil {
		return err
	}
	if _, err := l.run("ip", "link", "set", wgDev, "mtu", strconv.Itoa(l.mtu), "up"); err != nil {
		return err
	}
	return nil
}

// wgProc is a running wireguard-go: done receives its exit, out its last
// lines (it logs only errors by default).
type wgProc struct {
	cmd  *exec.Cmd
	done chan error
	out  *tailLog
}

// startUserspace makes the TUN device if the runtime handed out none (with
// the container's default CAP_MKNOD: misc device 10:200), keeps the control
// socket where a host's wg AppArmor profile lets wg reach it, starts
// wireguard-go in the foreground as this process's child and waits for wg0.
func (l *wgLink) startUserspace() error {
	if fi, err := os.Stat(tunPath); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		if err := os.MkdirAll(filepath.Dir(tunPath), 0o755); err != nil {
			return fmt.Errorf("no %s, and it cannot be made here: %w", tunPath, err)
		}
		if _, err := l.run("mknod", "-m", "0600", tunPath, "c", "10", "200"); err != nil {
			return fmt.Errorf("no %s, and it cannot be made here: no WireGuard link: %w", tunPath, err)
		}
	}
	// wireguard-go's control socket is wgRunDir/wg0.sock, a fixed path; the
	// profile lets wg reach only wgKeyDir, so the socket lives there.
	sockDir := filepath.Join(wgKeyDir, "run")
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		return err
	}
	if target, err := os.Readlink(wgRunDir); err != nil || target != sockDir {
		if err := os.RemoveAll(wgRunDir); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(wgRunDir), 0o755); err != nil {
			return err
		}
		if err := os.Symlink(sockDir, wgRunDir); err != nil {
			return err
		}
	}
	p := &wgProc{done: make(chan error, 1), out: &tailLog{max: 20}}
	p.cmd = exec.Command(wireguardGo, "-f", wgDev)
	// Without it, wireguard-go opens with a banner saying the kernel has
	// WireGuard and it is not needed: false here, and a misleading log.
	p.cmd.Env = append(os.Environ(), "WG_PROCESS_FOREGROUND=1")
	p.cmd.Stdout, p.cmd.Stderr = p.out, p.out
	if err := p.cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", wireguardGo, err)
	}
	go func() { p.done <- p.cmd.Wait() }()
	l.wgo = p
	for deadline := time.Now().Add(wgUpTimeout); ; time.Sleep(100 * time.Millisecond) {
		if _, err := l.run("ip", "link", "show", wgDev); err == nil {
			slog.Info("gateway: wg0 is wireguard-go", "pid", p.cmd.Process.Pid)
			return nil
		}
		select {
		case err := <-p.done:
			p.done <- err // superviseWG reads it too
			return fmt.Errorf("wireguard-go exited before wg0 came up (%v): %s", err, p.out.String())
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wireguard-go did not bring up wg0 within %s: %s", wgUpTimeout, p.out.String())
		}
	}
}

// stopUserspace kills a wireguard-go left behind by a failed setup.
func (l *wgLink) stopUserspace() {
	if l.wgo != nil && l.wgo.cmd.Process != nil {
		_ = l.wgo.cmd.Process.Kill()
	}
}

// superviseWG restarts a dead wireguard-go and puts wg0 back as it was: the
// link, its key and port, address, MTU and every peer. Without it one dead
// process would cut every run of the project off. Returns when quit closes;
// does nothing on a kernel link.
func (l *wgLink) superviseWG(quit <-chan struct{}) {
	for {
		l.mu.Lock()
		p := l.wgo
		l.mu.Unlock()
		if p == nil {
			return
		}
		var err error
		select {
		case <-quit:
			return
		case err = <-p.done:
		}
		slog.Warn("gateway: wireguard-go exited; restarting it", "err", err, "output", p.out.String())
		for {
			l.mu.Lock()
			err := l.up()
			if err == nil {
				l.reconcileLocked(l.peers)
			}
			l.mu.Unlock()
			if err == nil {
				slog.Info("gateway: wg0 back (userspace WireGuard)", "peers", len(l.peers))
				break
			}
			slog.Warn("gateway: wireguard-go restart failed; retrying", "err", err)
			select {
			case <-quit:
				return
			case <-time.After(wgRestartBackoff):
			}
		}
	}
}

// tailLog keeps the last max lines written to it, and logs each.
type tailLog struct {
	mu      sync.Mutex
	max     int
	lines   []string
	partial string
}

func (t *tailLog) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	parts := strings.Split(t.partial+string(b), "\n")
	t.partial = parts[len(parts)-1]
	for _, line := range parts[:len(parts)-1] {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		slog.Info("wireguard-go", "line", line)
		t.lines = append(t.lines, line)
		if len(t.lines) > t.max {
			t.lines = t.lines[1:]
		}
	}
	return len(b), nil
}

func (t *tailLog) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := append([]string(nil), t.lines...)
	if p := strings.TrimSpace(t.partial); p != "" {
		out = append(out, p)
	}
	return strings.Join(out, " | ")
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
// (allowed only its own /32) and removes any peer no longer routed. The set
// is kept, for a restarted wireguard-go.
func (l *wgLink) reconcilePeers(peers map[string]Peer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.peers = peers
	l.reconcileLocked(peers)
}

func (l *wgLink) reconcileLocked(peers map[string]Peer) {
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
