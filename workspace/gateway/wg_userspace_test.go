package main

import (
	"os"
	"path/filepath"
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
		"wireguard-go": common + `case "$APO_FAKE_WGO" in
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

// stopWG ends a test's wireguard-go and its supervisor.
func stopWG(t *testing.T, l *wgLink, quit chan struct{}) {
	t.Helper()
	t.Cleanup(func() {
		if quit != nil {
			close(quit)
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.wgo != nil && l.wgo.cmd.Process != nil {
			_ = l.wgo.cmd.Process.Signal(syscall.SIGTERM)
			<-l.wgo.done
		}
	})
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
	stopWG(t, l, nil)
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
	stopWG(t, l, nil)
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
			kataTools(t)
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
	quit := make(chan struct{})
	stopWG(t, l, quit)
	l.reconcilePeers(map[string]Peer{"run-1": {PublicKey: "pk1", IP: "10.90.0.2"}})
	go l.superviseWG(quit)

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
			quit := make(chan struct{})
			done := make(chan struct{})
			go func() { l.superviseWG(quit); close(done) }()
			if failing {
				wgRestartBackoff = time.Hour
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
			stopWG(t, l, nil)
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
