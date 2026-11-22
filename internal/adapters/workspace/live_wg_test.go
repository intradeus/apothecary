//go:build live

package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"apothecary/internal/platform/livetest"
)

// The sandbox's tunnel script (apo-wg-up) against a kernel-WireGuard peer,
// in two containers of the sandbox image on the docker host the k3d cluster
// runs on (live_k8s_test.go's header; it needs the image of this tree,
// APOTHECARY_LIVE_IMAGE_PREFIX/sandbox:local, in that docker):
//
//   - userspace (APO_WG_USERSPACE=1, the path a Kata guest's kernel takes):
//     wg0 is wireguard-go, traffic crosses to the peer, and a killed
//     wireguard-go is restarted by the watchdog with the link put back;
//   - kernel: wg0 is the kernel's, as on every runtime with the module.
//
// The pod's other parts (the gateway's firewall, routes into the cluster)
// are TestLiveK8sLifecycle's; this is the tunnel alone, both ways up.
func TestLiveK8sWireGuardFallback(t *testing.T) {
	prefix := os.Getenv("APOTHECARY_LIVE_IMAGE_PREFIX")
	if prefix == "" {
		livetest.Skip(t, "APOTHECARY_LIVE_IMAGE_PREFIX unset: where this tree's images were built")
	}
	if err := exec.Command("docker", "version").Run(); err != nil {
		livetest.Skip(t, "no docker: the tunnel runs in two containers of the sandbox image")
	}
	img := prefix + "/sandbox:local"
	runT := func(t *testing.T, args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run := func(args ...string) string { t.Helper(); return runT(t, args...) }
	try := func(args ...string) (string, error) {
		out, err := exec.Command("docker", args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	key := func() (private, public string) {
		private = run("run", "--rm", "--entrypoint", "wg", img, "genkey")
		cmd := exec.Command("docker", "run", "--rm", "-i", "--entrypoint", "wg", img, "pubkey")
		cmd.Stdin = strings.NewReader(private + "\n")
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return private, strings.TrimSpace(string(out))
	}

	tag := fmt.Sprint(time.Now().UnixNano() % 1000000)
	net := "apo-live-wg-" + tag
	// The network's range is docker's pick from its own pools, and every
	// address is read back from it: a fixed subnet fails the test on a
	// docker that already holds it (another run's network, a VPN route).
	run("network", "create", net)
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", net).Run() })
	gwPriv, gwPub := key()
	sbPriv, sbPub := key()

	// The peer: kernel WireGuard, as the gateway runs it.
	gw := "apo-live-wg-gw-" + tag
	run("run", "-d", "--name", gw, "--privileged", "--network", net, "--entrypoint", "sh", img, "-c",
		"set -e; ip link add wg0 type wireguard; mkdir -p /etc/wireguard; echo "+gwPriv+" > /etc/wireguard/gw.key; "+
			"wg set wg0 listen-port 51820 private-key /etc/wireguard/gw.key peer "+sbPub+
			" allowed-ips 10.99.0.2/32; ip address add 10.99.0.1/24 dev wg0; ip link set wg0 up; exec sleep infinity")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", gw).Run() })
	gwIP := run("inspect", "-f", "{{(index .NetworkSettings.Networks \""+net+"\").IPAddress}}", gw)
	// The engine's /32 route only needs an address on the network: its
	// bridge's own.
	engineIP := run("network", "inspect", "-f", "{{(index .IPAM.Config 0).Gateway}}", net)
	if gwIP == "" || engineIP == "" {
		t.Fatalf("network %s: gateway at %q, bridge at %q", net, gwIP, engineIP)
	}

	for _, c := range []struct {
		name, mode string
		env        []string
	}{
		{"userspace", "userspace", []string{"-e", "APO_WG_USERSPACE=1"}},
		{"kernel", "kernel", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			run := func(args ...string) string { t.Helper(); return runT(t, args...) }
			// /secrets/wg in a tree of its own, copied in whole.
			root := t.TempDir()
			secrets := filepath.Join(root, "secrets", "wg")
			if err := os.MkdirAll(secrets, 0o755); err != nil {
				t.Fatal(err)
			}
			for f, v := range map[string]string{"wg-private-key": sbPriv, "wg-ip": "10.99.0.2", "gw-public-key": gwPub} {
				if err := os.WriteFile(filepath.Join(secrets, f), []byte(v+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			sb := "apo-live-wg-sb-" + c.name + "-" + tag
			args := []string{"create", "--name", sb, "--privileged", "--network", net,
				"-e", "GW_ENDPOINT=" + gwIP + ":51820", "-e", "GW_SVC_IP=" + gwIP, "-e", "ENGINE_IP=" + engineIP,
				"-e", "APO_WG_WATCH_EVERY=1"}
			args = append(append(args, c.env...), "--entrypoint", "apo-wg-up", img)
			run(args...)
			// Copied in, not bind-mounted: this test may itself run in a
			// container of that docker, whose paths the daemon cannot see.
			run("cp", filepath.Join(root, "secrets"), sb+":/")
			run("start", sb)
			t.Cleanup(func() {
				if t.Failed() {
					logs, _ := try("logs", sb)
					t.Logf("%s logs:\n%s", sb, firstLines(logs, 20))
					for _, ctr := range []string{sb, gw} {
						show, _ := try("exec", ctr, "sh", "-c", "wg show; ip route")
						t.Logf("%s:\n%s", ctr, show)
					}
					gwLogs, _ := try("logs", gw)
					t.Logf("%s logs:\n%s", gw, gwLogs)
				}
				_ = exec.Command("docker", "rm", "-f", sb).Run()
			})
			waitFor := func(what string, ok func() bool) {
				t.Helper()
				for deadline := time.Now().Add(30 * time.Second); !ok(); time.Sleep(250 * time.Millisecond) {
					if time.Now().After(deadline) {
						t.Fatalf("%s: not within 30s", what)
					}
				}
			}
			waitFor("wg-ready", func() bool { _, err := try("exec", sb, "test", "-f", "/run/apo/wg-ready"); return err == nil })
			if mode := run("exec", sb, "cat", "/run/apo/wg-mode"); mode != c.mode {
				t.Fatalf("mode = %q, want %q", mode, c.mode)
			}
			if logs := run("logs", sb); !strings.Contains(logs, "("+c.mode+" WireGuard)") {
				t.Errorf("the log does not say which WireGuard runs:\n%s", logs)
			}
			// Across the tunnel: the peer's address is reachable only over wg0.
			ping := func() bool { _, err := try("exec", sb, "ping", "-c", "1", "-W", "1", "10.99.0.1"); return err == nil }
			waitFor("a ping over the tunnel", ping)
			if c.mode != "userspace" {
				if _, err := try("exec", sb, "pgrep", "wireguard-go"); err == nil {
					t.Error("wireguard-go runs beside the kernel's WireGuard")
				}
				return
			}
			// A dead wireguard-go takes wg0 with it; the watchdog brings both back.
			run("exec", sb, "pkill", "-KILL", "wireguard-go")
			waitFor("the watchdog's restart", func() bool {
				logs, _ := try("logs", sb)
				return strings.Contains(logs, "wireguard-go exited; restarting it")
			})
			waitFor("a ping after the restart", ping)
		})
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
