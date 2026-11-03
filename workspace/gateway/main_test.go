package main

import (
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The whole daemon, in process, from flags: it comes back with the policy and
// routes in its state dir, filters by them, answers the control API, keeps the
// control port closed to run links, and reports what it denied.
func TestMainBootsFromItsStateDirAndEnforces(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(filepath.Join(state, "egress-policy.json"),
		File{Mode: ModeAllow, Domains: []string{"good.example"}}); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(filepath.Join(state, "routes.json"), Routes{
		RunSubnets: map[string]string{"run-1": "127.0.0.0/8"},
		Peers:      map[string]Peer{"run-1": {PublicKey: "peer-key", IP: "10.90.0.2"}},
	}); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(root, "token")
	if err := os.WriteFile(tokenFile, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(root, "wg.key")
	if err := os.WriteFile(keyFile, []byte("key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyDir := filepath.Join(root, "etc-wireguard")
	origKeyDir := wgKeyDir
	wgKeyDir = keyDir
	defer func() { wgKeyDir = origKeyDir }()
	wgLog := fakeTools(t)
	useRouteTable(t, "eth0")
	ix := newInterception(t, &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 443})

	// Seams: every listener lands on loopback and is ours to close (closing the
	// filter's is what makes main return), ticks are ours to send.
	var mu sync.Mutex
	var rules []string
	origNFT, origTick, origP, origS := applyNFT, watchTick, listenPacket, listenStream
	ticks := make(chan time.Time)
	applyNFT = func(s string) error {
		mu.Lock()
		defer mu.Unlock()
		rules = append(rules, s)
		return nil
	}
	watchTick = func() <-chan time.Time { return ticks }
	bound := make(chan net.Listener, 2)
	boundPC := make(chan net.PacketConn, 1)
	listenPacket = func(network, _ string) (net.PacketConn, error) {
		pc, err := net.ListenPacket(network, "127.0.0.1:0")
		if err == nil {
			boundPC <- pc
		}
		return pc, err
	}
	filterReady := make(chan net.Listener, 1)
	controlUp := make(chan net.Listener, 1)
	// The port the flags name, for nft to close; what really listens is below.
	const controlPort = 9998
	listenStream = func(network, addr string) (net.Listener, error) {
		ln, err := net.Listen(network, "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		switch addr {
		case ":53":
			bound <- ln
		case ":9999":
			filterReady <- ln
		case "127.0.0.1:" + strconv.Itoa(controlPort):
			controlUp <- ln
		default:
			t.Errorf("something bound on %q", addr)
		}
		return ln, nil
	}
	origArgs, origFlags := os.Args, flag.CommandLine
	os.Args = []string{"apothecary-gateway",
		"--state-dir", state,
		"--listen-port", "9999",
		"--log-conns=true",
		"--control-listen", "127.0.0.1:" + strconv.Itoa(controlPort),
		"--control-token-file", tokenFile,
		"--protect-cidrs", "169.254.0.0/16",
		"--wg-key", keyFile,
	}
	flag.CommandLine = flag.NewFlagSet("apothecary-gateway", flag.ContinueOnError)

	mainDone := make(chan struct{})
	go func() {
		defer close(mainDone)
		main()
	}()

	var filter net.Listener
	select {
	case filter = <-filterReady:
	case <-time.After(30 * time.Second):
		t.Fatal("the filter never came up")
	}
	dnsTCP := <-bound
	dnsUDP := <-boundPC

	// nft got the state dir's mode, the flags' protection and the closed port.
	mu.Lock()
	first := rules[0]
	mu.Unlock()
	for _, want := range []string{
		`iifname != "eth0" tcp dport { 80, 443 } redirect to :9999`,
		`iifname != "eth0" ip daddr { 169.254.0.0/16 } drop`,
		`iifname != { "eth0", "lo" } tcp dport ` + strconv.Itoa(controlPort) + ` drop`,
	} {
		if !strings.Contains(first, want) {
			t.Errorf("rules lack %q:\n%s", want, first)
		}
	}
	got := calls(t, wgLog)
	if !strings.Contains(got, "wg set wg0 peer peer-key allowed-ips 10.90.0.2/32") {
		t.Errorf("the persisted peer was not restored:\n%s", got)
	}
	// wg reads the key where a host's AppArmor profile for wg allows it.
	staged := filepath.Join(keyDir, "wg0.key")
	if !strings.Contains(got, "wg set wg0 private-key "+staged+" ") {
		t.Errorf("wg did not read its key from %s:\n%s", staged, got)
	}
	if b, err := os.ReadFile(staged); err != nil || string(b) != "key\n" {
		t.Errorf("staged key = %q, %v", b, err)
	}
	if info, err := os.Stat(staged); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("staged key mode = %v, %v", info.Mode(), err)
	}

	// The persisted policy is live on the wire.
	send := func(host string) []byte {
		c, err := net.Dial("tcp", filter.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, err := c.Write(buildClientHello(host)); err != nil {
			t.Fatal(err)
		}
		_ = c.(*net.TCPConn).CloseWrite()
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		got, _ := io.ReadAll(c)
		return got
	}
	if got := send("good.example"); string(got) != upstreamReply {
		t.Errorf("allowed client got %q", got)
	}
	if got := send("evil.example"); len(got) != 0 {
		t.Errorf("denied client got %q", got)
	}
	if ix.dialed.Load() != 1 {
		t.Errorf("dialed %d times, want only the allowed one", ix.dialed.Load())
	}

	// The control API needs the token from the file.
	base := "http://" + (<-controlUp).Addr().String()
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("control API: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	req := func(token string) int {
		r, _ := http.NewRequest("GET", base+"/policy", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := req("wrong"); got != http.StatusUnauthorized {
		t.Errorf("wrong token = %d", got)
	}
	if got := req("s3cret"); got != http.StatusOK {
		t.Errorf("right token = %d", got)
	}

	// One tick flushes the denial and the audit into the state dir. The audit
	// is the iteration's last act, so once it is on disk watch is parked on
	// the channel again and the seams are safe to restore.
	ticks <- time.Now()
	waitForFile(t, filepath.Join(state, "egress-audit.json"))
	var df DenialsFile
	if err := readJSON(filepath.Join(state, "egress-denials.json"), &df); err != nil {
		t.Fatal(err)
	}
	if len(df.Denials) != 1 || df.Denials[0].Run != "run-1" || df.Denials[0].Host != "evil.example" {
		t.Errorf("denials = %+v", df.Denials)
	}

	close(ticks)
	filter.Close()
	dnsTCP.Close()
	dnsUDP.Close()
	select {
	case <-mainDone:
	case <-time.After(10 * time.Second):
		t.Fatal("main did not return when the filter closed")
	}
	os.Args, flag.CommandLine = origArgs, origFlags
	applyNFT, watchTick, listenPacket, listenStream = origNFT, origTick, origP, origS
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
