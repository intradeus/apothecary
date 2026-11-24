package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// isolateUserspace points the userspace link's paths into dir and shortens
// its pacing, so no test touches the host's /dev/net/tun or
// /var/run/wireguard (a test run as root would make them).
func isolateUserspace(t *testing.T, dir string) {
	t.Helper()
	tun, run, key, wgo, up, back := tunPath, wgRunDir, wgKeyDir, wireguardGo, wgUpTimeout, wgRestartBackoff
	tunPath = filepath.Join(dir, "dev", "net", "tun")
	wgRunDir = filepath.Join(dir, "var", "run", "wireguard")
	if wgKeyDir == "/etc/wireguard" { // a test that set its own keeps it
		wgKeyDir = filepath.Join(dir, "etc", "wireguard")
	}
	wireguardGo = "wireguard-go" // the stand-in on PATH
	wgUpTimeout, wgRestartBackoff = 2*time.Second, 50*time.Millisecond
	t.Cleanup(func() {
		tunPath, wgRunDir, wgKeyDir, wireguardGo, wgUpTimeout, wgRestartBackoff = tun, run, key, wgo, up, back
	})
}

// kataTools is a node without kernel WireGuard (a Kata guest's): ip refuses
// the link type, and wg0 exists exactly while the stand-in wireguard-go
// runs. $APO_FAKE_WGO=die makes wireguard-go exit at once, =slow never bring
// up wg0; $APO_FAKE_FAIL fails a matching ip, wg or mknod call as fakeTools'
// do. Returns the call log and the state dir.
func kataTools(t *testing.T) (logPath, dir string) {
	t.Helper()
	dir = t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	up := filepath.Join(dir, "wg0-up")
	common := `echo "$(basename "$0") $*" >> "` + logPath + `"
if [ -n "$APO_FAKE_FAIL" ]; then
  case "$(basename "$0") $*" in *"$APO_FAKE_FAIL"*) echo "fake failure" >&2; exit 3;; esac
fi
`
	tools := map[string]string{
		"ip": common + `case "$*" in
  "link add wg0 type wireguard") echo "Error: Unknown device type." >&2; exit 2;;
  "link show wg0") [ -e "` + up + `" ] || { echo 'Device "wg0" does not exist.' >&2; exit 1; };;
esac
`,
		"wg":    common,
		"mknod": common,
		// Foreground, as the real one with -f: wg0 lives as long as it does.
		"wireguard-go": common + `echo $$ >> "` + filepath.Join(dir, "pids") + `"
case "$APO_FAKE_WGO" in
  die) echo "fake: cannot open tun" >&2; exit 1;;
  slow) sleep 30 >/dev/null 2>&1 & s=$!; trap 'kill $s; exit 0' TERM; wait; exit 0;;
esac
touch "` + up + `"
echo "fake wireguard-go up"
sleep 30 >/dev/null 2>&1 & s=$!
trap 'kill $s; rm -f "` + up + `"; exit 0' TERM
wait
rm -f "` + up + `"
`,
	}
	for name, body := range tools {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"exit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("APO_FAKE_FAIL", "")
	t.Setenv("APO_FAKE_WGO", "")
	isolateUserspace(t, dir)
	return logPath, dir
}

// stopWG stops a test's wireguard-go once the test ends.
func stopWG(t *testing.T, l *wgLink) {
	t.Helper()
	t.Cleanup(func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.wgo != nil {
			_ = l.wgo.cmd.Process.Signal(syscall.SIGTERM)
			<-l.wgo.exited
		}
	})
}

// supervise runs l's supervisor for the rest of the test. At the end it
// stops the supervisor and waits for it to return before it stops
// wireguard-go: a supervisor still running would see that exit and
// restart, or read the pacing the next test resets.
func supervise(t *testing.T, l *wgLink) {
	t.Helper()
	quit, done := make(chan struct{}), make(chan struct{})
	go func() { l.superviseWG(quit); close(done) }()
	stopWG(t, l) // cleanups run last-registered first: this one after the next
	t.Cleanup(func() { close(quit); <-done })
}

// liveWireguardGo lists the stand-in wireguard-go processes still running
// (or not yet reaped) of the ones started under dir.
func liveWireguardGo(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "pids"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var live []string
	for _, pid := range strings.Fields(string(b)) {
		n, _ := strconv.Atoi(pid)
		if n > 0 && syscall.Kill(n, 0) == nil {
			live = append(live, pid)
		}
	}
	return live
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not within 5s", what)
		}
	}
}

// A kernel that refuses the link type gets wireguard-go, configured with the
// same wg and ip calls as the kernel's link.
func TestSetupWGFallsBackToWireguardGo(t *testing.T) {
	log, dir := kataTools(t)
	l, err := setupWG("no-such-uplink", "/keys/wg", 51820, "10.90.0.1/16", 1400, false)
	if err != nil {
		t.Fatal(err)
	}
	stopWG(t, l)
	if l.mode != wgUserspace || !strings.Contains(l.kernelWhy, "Unknown device type") {
		t.Errorf("mode = %q (%q), want userspace after the kernel's refusal", l.mode, l.kernelWhy)
	}
	got := calls(t, log)
	for _, want := range []string{
		"ip link add wg0 type wireguard\n",
		"mknod -m 0600 " + tunPath + " c 10 200\n",
		"wireguard-go -f wg0\n",
		"wg set wg0 private-key /keys/wg listen-port 51820\n" +
			"ip address replace 10.90.0.1/16 dev wg0\n" +
			"ip link set wg0 mtu 1400 up\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("commands lack %q:\n%s", want, got)
		}
	}
	// wg reaches the control socket under the key dir, where a host's wg
	// AppArmor profile lets it.
	if target, err := os.Readlink(wgRunDir); err != nil || target != filepath.Join(wgKeyDir, "run") {
		t.Errorf("%s -> %q (%v), want the key dir's run/", wgRunDir, target, err)
	}
	if !strings.Contains(l.wgo.out.String(), "fake wireguard-go up") {
		t.Errorf("wireguard-go's output not kept: %q", l.wgo.out.String())
	}
	_ = dir
}

// APO_WG_USERSPACE (the live tests' switch) never tries the kernel's link;
// a TUN device already there is not made again, and a socket dir already
// linked is left.
func TestSetupWGUserspaceOnRequest(t *testing.T) {
	log, _ := kataTools(t)
	if err := os.MkdirAll(filepath.Join(wgKeyDir, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(wgRunDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(wgKeyDir, "run"), wgRunDir); err != nil {
		t.Fatal(err)
	}
	tunPath = "/dev/null" // a character device: nothing to make
	l, err := setupWG("no-such-uplink", "/keys/wg", 51820, "10.90.0.1/16", 1400, true)
	if err != nil {
		t.Fatal(err)
	}
	stopWG(t, l)
	got := calls(t, log)
	if l.mode != wgUserspace || l.kernelWhy != "" {
		t.Errorf("mode = %q (%q), want userspace, asked for", l.mode, l.kernelWhy)
	}
	for _, never := range []string{"ip link add", "mknod"} {
		if strings.Contains(got, never) {
			t.Errorf("%q ran:\n%s", never, got)
		}
	}
}

func TestSetupWGUserspaceFailures(t *testing.T) {
	for _, c := range []struct {
		name, fail, wgo, want string
	}{
		{"no TUN device and none can be made", "mknod", "", "cannot be made here"},
		{"wireguard-go exits", "", "die", "fake: cannot open tun"},
		{"wireguard-go never brings up wg0", "", "slow", "did not bring up wg0"},
		{"configuring the link fails", "ip address replace", "", "fake failure"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, dir := kataTools(t)
			wgUpTimeout = 300 * time.Millisecond
			t.Setenv("APO_FAKE_FAIL", c.fail)
			t.Setenv("APO_FAKE_WGO", c.wgo)
			l, err := setupWG("no-such-uplink", "/keys/wg", 51820, "10.90.0.1/16", 1400, false)
			if err == nil || l != nil {
				t.Fatalf("setupWG = %v, %v; want the failure", l, err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
			if c.fail != "ip address replace" && !strings.Contains(err.Error(), "Unknown device type") {
				t.Errorf("err = %v, want the kernel's refusal named too", err)
			}
			// Stopped and reaped, whichever step failed: a wireguard-go left
			// running would hold the TUN device against the next start.
			if live := liveWireguardGo(t, dir); len(live) != 0 {
				t.Errorf("wireguard-go still running after the failure: pids %v", live)
			}
		})
	}
	t.Run("wireguard-go missing", func(t *testing.T) {
		kataTools(t)
		wireguardGo = filepath.Join(t.TempDir(), "absent")
		if _, err := setupWG("no-such-uplink", "/keys/wg", 51820, "10.90.0.1/16", 1400, true); err == nil || !strings.Contains(err.Error(), "start ") {
			t.Fatalf("err = %v, want the start named", err)
		}
	})
}

// A dead wireguard-go takes wg0, its key, address and peers with it: the
// supervisor starts it again and puts all of it back.
func TestSuperviseWGRestartsWireguardGo(t *testing.T) {
	log, dir := kataTools(t)
	l, err := setupWG("no-such-uplink", "/keys/wg", 51820, "10.90.0.1/16", 1400, false)
	if err != nil {
		t.Fatal(err)
	}
	l.reconcilePeers(map[string]Peer{"run-1": {PublicKey: "pk1", IP: "10.90.0.2"}})
	supervise(t, l)

	first := l.wgo
	if err := os.WriteFile(log, nil, 0o644); err != nil { // only what the restart runs
		t.Fatal(err)
	}
	// Once with a restart that fails, then one that works.
	t.Setenv("APO_FAKE_WGO", "die")
	_ = first.cmd.Process.Signal(syscall.SIGTERM)
	waitUntil(t, "a failed restart", func() bool { return strings.Count(calls(t, log), "wireguard-go -f wg0") >= 1 })
	t.Setenv("APO_FAKE_WGO", "")
	waitUntil(t, "wg0 back", func() bool {
		_, err := os.Stat(filepath.Join(dir, "wg0-up"))
		return err == nil && strings.Contains(calls(t, log), "wg set wg0 peer pk1 allowed-ips 10.90.0.2/32")
	})
	got := calls(t, log)
	for _, want := range []string{
		"wg set wg0 private-key /keys/wg listen-port 51820\n",
		"ip address replace 10.90.0.1/16 dev wg0\n",
		"ip link set wg0 mtu 1400 up\n",
		"wg set wg0 peer pk1 allowed-ips 10.90.0.2/32\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the restart did not run %q:\n%s", want, got)
		}
	}
	l.mu.Lock()
	again := l.wgo
	l.mu.Unlock()
	if again == first {
		t.Error("the same wireguard-go after a restart")
	}
}

// Restarts that time out (a wireguard-go alive but with no wg0) each stop
// their own child: retries never pile up processes racing for the TUN
// device and socket, and the next good start is the only one left.
func TestSuperviseWGStopsEveryFailedStart(t *testing.T) {
	_, dir := kataTools(t)
	wgUpTimeout = 200 * time.Millisecond
	l, err := setupWG("no-such-uplink", "/keys/wg", 51820, "10.90.0.1/16", 1400, true)
	if err != nil {
		t.Fatal(err)
	}
	supervise(t, l)
	l.mu.Lock()
	first := l.wgo
	l.mu.Unlock()

	t.Setenv("APO_FAKE_WGO", "slow")
	_ = first.cmd.Process.Signal(syscall.SIGTERM)
	starts := func() int {
		b, _ := os.ReadFile(filepath.Join(dir, "pids"))
		return len(strings.Fields(string(b)))
	}
	waitUntil(t, "three timed-out restarts", func() bool { return starts() >= 4 })
	if live := liveWireguardGo(t, dir); len(live) > 1 {
		t.Fatalf("%d wireguard-go running during the retries (pids %v), want at most the one starting", len(live), live)
	}
	t.Setenv("APO_FAKE_WGO", "")
	waitUntil(t, "wg0 back", func() bool {
		_, err := os.Stat(filepath.Join(dir, "wg0-up"))
		return err == nil
	})
	waitUntil(t, "only the good start left", func() bool { return len(liveWireguardGo(t, dir)) == 1 })
}

// Recovery is not done while a peer is missing: a failed peer list or peer
// update is retried, on the wireguard-go already running, until every peer
// is back. Routes changing meanwhile (the control API) race safely with it.
func TestSuperviseWGRetriesUntilEveryPeerIsBack(t *testing.T) {
	for _, fail := range []string{"wg set wg0 peer pk2", "wg show wg0 peers"} {
		t.Run(fail, func(t *testing.T) {
			log, dir := kataTools(t)
			l, err := setupWG("no-such-uplink", "/keys/wg", 51820, "10.90.0.1/16", 1400, true)
			if err != nil {
				t.Fatal(err)
			}
			peers := map[string]Peer{"run-1": {PublicKey: "pk1", IP: "10.90.0.2"}, "run-2": {PublicKey: "pk2", IP: "10.90.0.3"}}
			l.reconcilePeers(peers)
			supervise(t, l)
			// The control API keeps applying the same routes throughout.
			stopRoutes := make(chan struct{})
			routesDone := make(chan struct{})
			go func() {
				defer close(routesDone)
				for {
					select {
					case <-stopRoutes:
						return
					case <-time.After(10 * time.Millisecond):
						l.reconcilePeers(peers)
					}
				}
			}()
			defer func() { close(stopRoutes); <-routesDone }()

			l.mu.Lock()
			first := l.wgo
			l.mu.Unlock()
			t.Setenv("APO_FAKE_FAIL", fail)
			_ = first.cmd.Process.Signal(syscall.SIGTERM)
			// Started again once, then configured over and over while the
			// peers fail: the link is retried, not declared back.
			waitUntil(t, "retries on the new wireguard-go", func() bool {
				return strings.Count(calls(t, log), "wg set wg0 private-key") >= 4
			})
			if n := len(strings.Fields(mustRead(t, filepath.Join(dir, "pids")))); n != 2 {
				t.Errorf("%d wireguard-go started, want the first and one restart: a peer failure is no reason for another", n)
			}
			t.Setenv("APO_FAKE_FAIL", "")
			// Once the peers go through, the retries stop.
			waitUntil(t, "the retries to stop", func() bool {
				a := strings.Count(calls(t, log), "wg set wg0 private-key")
				time.Sleep(3 * wgRestartBackoff)
				return strings.Count(calls(t, log), "wg set wg0 private-key") == a
			})
			if !strings.Contains(calls(t, log), "wg set wg0 peer pk2 allowed-ips 10.90.0.3/32") {
				t.Error("pk2 was never put back")
			}
		})
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// On a kernel link there is no process to watch; quit ends a supervisor
// waiting on one, and one backing off between failed restarts.
func TestSuperviseWGStops(t *testing.T) {
	t.Run("kernel link", func(t *testing.T) {
		done := make(chan struct{})
		go func() { (&wgLink{}).superviseWG(nil); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("superviseWG waits on a kernel link")
		}
	})
	for _, failing := range []bool{false, true} {
		name := map[bool]string{false: "waiting", true: "backing off"}[failing]
		t.Run(name, func(t *testing.T) {
			log, _ := kataTools(t)
			l, err := setupWG("no-such-uplink", "/keys/wg", 51820, "10.90.0.1/16", 1400, true)
			if err != nil {
				t.Fatal(err)
			}
			if failing {
				wgRestartBackoff = time.Hour // set before the supervisor reads it
			}
			quit := make(chan struct{})
			done := make(chan struct{})
			go func() { l.superviseWG(quit); close(done) }()
			if failing {
				t.Setenv("APO_FAKE_WGO", "die")
				_ = l.wgo.cmd.Process.Signal(syscall.SIGTERM)
				waitUntil(t, "a failed restart", func() bool { return strings.Count(calls(t, log), "wireguard-go -f wg0") >= 2 })
			}
			close(quit)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("superviseWG did not return on quit")
			}
			stopWG(t, l)
		})
	}
}

func TestTailLog(t *testing.T) {
	tl := &tailLog{max: 2}
	_, _ = tl.Write([]byte("one\n\ntwo\nthr"))
	_, _ = tl.Write([]byte("ee\nfour"))
	if got := tl.String(); got != "two | three | four" {
		t.Fatalf("tail = %q, want the last two whole lines and the partial one", got)
	}
}
