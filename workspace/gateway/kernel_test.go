package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTools puts recording stand-ins for ip, wg and nft on PATH: each appends
// its argv (and nft its script) to the returned log, and fails when its command
// line contains $APO_FAKE_FAIL.
func fakeTools(t *testing.T) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
name=$(basename "$0")
echo "$name $*" >> "` + logPath + `"
if [ "$name" = nft ]; then cat >> "` + logPath + `"; fi
if [ -n "$APO_FAKE_FAIL" ]; then
  case "$name $*" in
    *"$APO_FAKE_FAIL"*) echo "fake failure" >&2; exit 3;;
  esac
fi
exit 0
`
	for _, tool := range []string{"ip", "wg", "nft"} {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("APO_FAKE_FAIL", "")
	return logPath
}

func calls(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func TestSetupWGConfiguresTheLink(t *testing.T) {
	log := fakeTools(t)
	link, err := setupWG("no-such-uplink", "/keys/wg", 51820, "10.90.0.1/16", 1400)
	if err != nil {
		t.Fatal(err)
	}
	if link.mtu != 1400 {
		t.Errorf("mtu = %d", link.mtu)
	}
	want := "ip link show wg0\n" +
		"wg set wg0 private-key /keys/wg listen-port 51820\n" +
		"ip address replace 10.90.0.1/16 dev wg0\n" +
		"ip link set wg0 mtu 1400 up\n"
	if got := calls(t, log); got != want {
		t.Errorf("commands:\n%s\nwant:\n%s", got, want)
	}
}

// The tunnel's overhead comes off the uplink's MTU when none is given, and a
// fresh container gets its wg0 created.
func TestSetupWGCreatesTheDeviceAndDerivesTheMTU(t *testing.T) {
	log := fakeTools(t)
	t.Setenv("APO_FAKE_FAIL", "ip link show wg0")
	link, err := setupWG("no-such-uplink", "/keys/wg", 51820, "10.90.0.1/16", 0)
	if err != nil {
		t.Fatal(err)
	}
	if link.mtu != 1440 { // 1500 fallback - 60
		t.Errorf("mtu = %d, want 1440", link.mtu)
	}
	if got := calls(t, log); !strings.Contains(got, "ip link add wg0 type wireguard\n") {
		t.Errorf("wg0 was not created:\n%s", got)
	}
}

func TestSetupWGStopsAtTheFirstFailure(t *testing.T) {
	for _, step := range []string{"wg set wg0 private-key", "ip address replace", "ip link set wg0"} {
		t.Run(step, func(t *testing.T) {
			fakeTools(t)
			t.Setenv("APO_FAKE_FAIL", step)
			link, err := setupWG("no-such-uplink", "/keys/wg", 51820, "10.90.0.1/16", 1400)
			if err == nil || link != nil {
				t.Fatalf("setupWG = %v, %v; want the failure", link, err)
			}
			if !strings.Contains(err.Error(), "fake failure") {
				t.Errorf("err = %v, want the tool's stderr in it", err)
			}
		})
	}
	t.Run("device cannot be created", func(t *testing.T) {
		fakeTools(t)
		t.Setenv("APO_FAKE_FAIL", "ip link") // show and add both fail
		if _, err := setupWG("no-such-uplink", "/keys/wg", 51820, "10.90.0.1/16", 1400); err == nil {
			t.Fatal("setupWG must fail without a device")
		}
	})
}

func TestUplinkMTU(t *testing.T) {
	dir := t.TempDir()
	// The uplink name is the one path component between /sys/class/net and mtu.
	ext := filepath.Join("../../..", dir)
	for _, c := range []struct {
		content string
		want    int
	}{
		{"9001\n", 9001},
		{"1280\n", 1280},
		{"1279\n", 1500}, // under IPv6's minimum: not believed
		{"garbage\n", 1500},
	} {
		if err := os.WriteFile(filepath.Join(dir, "mtu"), []byte(c.content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := uplinkMTU(ext); got != c.want {
			t.Errorf("uplinkMTU(%q) = %d, want %d", c.content, got, c.want)
		}
	}
	if got := uplinkMTU("no-such-uplink"); got != 1500 {
		t.Errorf("unreadable uplink = %d", got)
	}
}

func TestRunCmdSurfacesOutput(t *testing.T) {
	fakeTools(t)
	if out, err := runCmd("ip", "link"); err != nil || out != "" {
		t.Fatalf("runCmd = %q, %v", out, err)
	}
	t.Setenv("APO_FAKE_FAIL", "link")
	_, err := runCmd("ip", "link")
	if err == nil || !strings.Contains(err.Error(), "fake failure") || !strings.Contains(err.Error(), "ip link") {
		t.Fatalf("runCmd err = %v", err)
	}
}

func TestReconcilePeersFaults(t *testing.T) {
	t.Run("an unreadable peer list changes nothing", func(t *testing.T) {
		var got []string
		l := &wgLink{run: func(name string, args ...string) (string, error) {
			got = append(got, strings.Join(args, " "))
			return "", fmt.Errorf("wg down")
		}}
		l.reconcilePeers(map[string]Peer{"run-1": {PublicKey: "k", IP: "10.90.0.2"}})
		if len(got) != 1 {
			t.Fatalf("commands = %v, want only the list", got)
		}
	})
	t.Run("a half-filled peer is not configured and does not survive", func(t *testing.T) {
		var got []string
		l := &wgLink{run: func(name string, args ...string) (string, error) {
			c := strings.Join(args, " ")
			got = append(got, c)
			if c == "show wg0 peers" {
				return "keep\nstale\n", nil
			}
			return "", nil
		}}
		l.reconcilePeers(map[string]Peer{
			"a": {PublicKey: "", IP: "10.90.0.2"},
			"b": {PublicKey: "stale", IP: ""},
			"c": {PublicKey: "keep", IP: "10.90.0.3"},
		})
		want := []string{"show wg0 peers", "set wg0 peer keep allowed-ips 10.90.0.3/32", "set wg0 peer stale remove"}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("commands = %q, want %q", got, want)
		}
	})
	t.Run("one failed peer does not stop the rest", func(t *testing.T) {
		var got []string
		l := &wgLink{run: func(name string, args ...string) (string, error) {
			c := strings.Join(args, " ")
			got = append(got, c)
			switch {
			case c == "show wg0 peers":
				return "old1 old2", nil
			case strings.Contains(c, "peer new1"), strings.Contains(c, "peer old1 remove"):
				return "", fmt.Errorf("refused")
			}
			return "", nil
		}}
		l.reconcilePeers(map[string]Peer{
			"a": {PublicKey: "new1", IP: "10.90.0.2"},
			"b": {PublicKey: "new2", IP: "10.90.0.3"},
		})
		joined := strings.Join(got, "|")
		for _, want := range []string{"peer new2 allowed-ips", "peer old2 remove"} {
			if !strings.Contains(joined, want) {
				t.Errorf("commands %q lack %q", got, want)
			}
		}
	})
}

// Attaching a run with a WireGuard peer reaches wg, and each peer is held to
// its own /32: that is what makes the attribution unforgeable.
func TestApplyRoutesReconcilesPeersToTheirOwnAddress(t *testing.T) {
	g := newTestGateway(t)
	var got []string
	g.wg = &wgLink{run: func(name string, args ...string) (string, error) {
		got = append(got, strings.Join(args, " "))
		return "", nil
	}}
	g.applyRoutes(Routes{
		RunSubnets: map[string]string{"run-1": "172.20.0.0/24"},
		Peers:      map[string]Peer{"run-1": {PublicKey: "pk", IP: "10.90.0.2"}},
	})
	if len(got) != 2 || got[1] != "set wg0 peer pk allowed-ips 10.90.0.2/32" {
		t.Fatalf("wg commands = %q", got)
	}
	if run := g.attributeIP(net.ParseIP("10.90.0.2")); run != "run-1" {
		t.Errorf("the tunnel address attributes to %q", run)
	}
	if run := g.attributeIP(net.ParseIP("172.20.0.9")); run != "run-1" {
		t.Errorf("the network address attributes to %q", run)
	}
}

func TestApplyRoutesAttribution(t *testing.T) {
	g := newTestGateway(t)
	if got := g.attributeIP(net.ParseIP("10.0.0.1")); got != "" {
		t.Fatalf("attribution before any routes = %q", got)
	}
	g.applyRoutes(Routes{
		RunSubnets: map[string]string{"wide": "10.0.0.0/8", "narrow": "10.1.0.0/24", "broken": "not-a-cidr"},
		Peers:      map[string]Peer{"peer": {PublicKey: "k", IP: "10.2.0.1"}, "v6": {PublicKey: "k6", IP: "fd00::1"}},
	})
	for ip, want := range map[string]string{
		"10.1.0.9":  "narrow", // the longest prefix wins, whatever the map order
		"10.9.9.9":  "wide",
		"10.2.0.1":  "peer",
		"10.2.0.2":  "wide", // a /32 covers only itself
		"192.0.2.1": "",
	} {
		if got := g.attributeIP(net.ParseIP(ip)); got != want {
			t.Errorf("attributeIP(%s) = %q, want %q", ip, got, want)
		}
	}
	if got := g.attributeIP(nil); got != "" {
		t.Errorf("attributeIP(nil) = %q", got)
	}
}

func TestApplyRoutesEvictsTornDownRuns(t *testing.T) {
	g := newTestGateway(t)
	g.recordDenial("gone", "a.example")
	g.audit.record("gone", "a.example", "192.0.2.1", 443, "tls", "deny", 0, 0)
	g.recordDenial("live", "b.example")
	g.audit.record("live", "b.example", "192.0.2.1", 443, "tls", "deny", 0, 0)

	// An empty table (a fresh start, nothing attached yet) wipes nothing.
	g.applyRoutes(Routes{})
	if len(g.denials) != 2 || len(g.audit.snapshot("")) != 2 {
		t.Fatalf("an empty table evicted: %d denials, %d audit", len(g.denials), len(g.audit.snapshot("")))
	}

	g.applyRoutes(Routes{RunSubnets: map[string]string{"live": "10.0.0.0/24"}})
	if len(g.denials) != 1 || g.denials["live\x00b.example"] == nil {
		t.Errorf("denials = %+v", g.denials)
	}
	if recs := g.audit.snapshot(""); len(recs) != 1 || recs[0].Run != "live" {
		t.Errorf("audit = %+v", recs)
	}
}

func TestLoadRoutes(t *testing.T) {
	g := newTestGateway(t)
	g.loadRoutes() // no file yet: nothing installed, and no crash
	if g.routesAttr.Load() != nil {
		t.Fatal("a missing routes file must install nothing")
	}
	if err := writeAtomic(g.routesPath, Routes{RunSubnets: map[string]string{"run-1": "10.0.0.0/24"}}); err != nil {
		t.Fatal(err)
	}
	g.loadRoutes()
	if got := g.attributeIP(net.ParseIP("10.0.0.5")); got != "run-1" {
		t.Fatalf("attributeIP = %q", got)
	}
}

func TestParseCIDRs(t *testing.T) {
	nets, err := parseCIDRs(" 10.0.0.0/8 ,, 169.254.0.0/16,")
	if err != nil || len(nets) != 2 || nets[1].String() != "169.254.0.0/16" {
		t.Fatalf("parseCIDRs = %v, %v", nets, err)
	}
	if nets, err := parseCIDRs(""); err != nil || nets != nil {
		t.Fatalf("parseCIDRs(\"\") = %v, %v", nets, err)
	}
	if _, err := parseCIDRs("10.0.0.0/8,nope"); err == nil {
		t.Fatal("a bad entry must fail the flag, not be dropped")
	}
}

func TestRenderNFTAllowModeEscapeHatches(t *testing.T) {
	rules := renderNFT("eth0", 9040, File{Mode: ModeAllow,
		AllowedCIDRs: []string{" 192.0.2.0/24 ", ""}, AllowedPorts: []int{22, 0, -1, 5432}}, nil, 0)
	for _, want := range []string{
		`iifname != "eth0" tcp dport { 80, 443 } redirect to :9040`,
		`iifname != "eth0" ip daddr { 192.0.2.0/24 } accept`,
		`iifname != "eth0" tcp dport { 22, 5432 } accept`,
		`oifname "eth0" masquerade`,
		"add chain ip6 apo6 forward { type filter hook forward priority filter ; policy drop ; }",
		"add chain ip apo forward { type filter hook forward priority filter ; policy drop ; }",
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("rules lack %q:\n%s", want, rules)
		}
	}
	if strings.Contains(rules, `iifname != "eth0" accept`) {
		t.Errorf("allow mode must not accept raw egress wholesale:\n%s", rules)
	}
	if strings.Contains(rules, "chain ip apo input") {
		t.Errorf("no control port, no input chain:\n%s", rules)
	}
}

// A bare allow-mode policy opens nothing at all at L3.
func TestRenderNFTAllowModeWithoutHatchesAcceptsNothing(t *testing.T) {
	rules := renderNFT("eth0", 9040, File{Mode: ModeAllow}, nil, 0)
	var accepts []string
	for _, line := range strings.Split(rules, "\n") {
		if strings.HasPrefix(line, "add rule ip apo forward") && strings.HasSuffix(line, "accept") {
			accepts = append(accepts, line)
		}
	}
	if len(accepts) != 1 || !strings.Contains(accepts[0], "ct state established,related") {
		t.Fatalf("forward accepts = %q, want only return traffic", accepts)
	}
}

func TestRenderNFTDenyModeAcceptsRawEgress(t *testing.T) {
	rules := renderNFT("eth0", 9040, File{Mode: ModeDeny}, nil, 0)
	if !strings.Contains(rules, `add rule ip apo forward iifname != "eth0" accept`) {
		t.Fatalf("deny mode is allow-all at L3:\n%s", rules)
	}
	if strings.Contains(rules, "ip daddr") {
		t.Fatalf("no denied CIDRs, no daddr rule:\n%s", rules)
	}
}

// nft rules run in order and accept is final in a base chain, so the drop of a
// denied range has to come before the blanket accept or it never fires.
func TestRenderNFTDenyModeDropsDeniedCIDRsBeforeAccepting(t *testing.T) {
	rules := renderNFT("eth0", 9040, File{Mode: ModeDeny, DeniedCIDRs: []string{"192.0.2.0/24"}}, nil, 0)
	drop := strings.Index(rules, "ip daddr { 192.0.2.0/24 } drop")
	accept := strings.Index(rules, `add rule ip apo forward iifname != "eth0" accept`)
	if drop < 0 || accept < 0 || drop > accept {
		t.Fatalf("drop at %d, accept at %d:\n%s", drop, accept, rules)
	}
}

func TestSetAndPortRendering(t *testing.T) {
	if got := setStr([]string{" a ", "", "  ", "b"}); got != "a, b" {
		t.Errorf("setStr = %q", got)
	}
	if got := setStr(nil); got != "" {
		t.Errorf("setStr(nil) = %q", got)
	}
	if got := portSet([]int{0, 80, -1, 443}); got != "80, 443" {
		t.Errorf("portSet = %q", got)
	}
	if got := q(`e"th`); got != `"e\"th"` {
		t.Errorf("q = %s", got)
	}
}

func TestApplyNFTPipesTheScript(t *testing.T) {
	log := fakeTools(t)
	if err := applyNFT("add table ip apo\n"); err != nil {
		t.Fatal(err)
	}
	if got := calls(t, log); got != "nft -f -\nadd table ip apo\n" {
		t.Fatalf("calls = %q", got)
	}
	t.Setenv("APO_FAKE_FAIL", "nft")
	err := applyNFT("bad\n")
	if err == nil || !strings.Contains(err.Error(), "fake failure") {
		t.Fatalf("applyNFT = %v, want nft's output in the error", err)
	}
}
