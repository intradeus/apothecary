package main

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubDNSListeners binds the DNS forwarder to loopback and closes what it
// opened; the addresses arrive on the channels.
func stubDNSListeners(t *testing.T) (udp chan net.PacketConn, tcp chan net.Listener) {
	t.Helper()
	udp, tcp = make(chan net.PacketConn, 1), make(chan net.Listener, 1)
	origP, origS := listenPacket, listenStream
	t.Cleanup(func() { listenPacket, listenStream = origP, origS })
	listenPacket = func(network, _ string) (net.PacketConn, error) {
		pc, err := net.ListenPacket(network, "127.0.0.1:0")
		if err == nil {
			t.Cleanup(func() { pc.Close() })
			udp <- pc
		}
		return pc, err
	}
	listenStream = func(network, _ string) (net.Listener, error) {
		ln, err := net.Listen(network, "127.0.0.1:0")
		if err == nil {
			t.Cleanup(func() { ln.Close() })
			tcp <- ln
		}
		return ln, err
	}
	return udp, tcp
}

// fakeResolver answers every UDP query with a fixed response.
func fakeResolver(t *testing.T, answer []byte) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			_, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(answer, addr)
		}
	}()
	return pc.LocalAddr().String()
}

// A lookup through the forwarder comes back to the client unchanged, and is
// what makes a later no-SNI connection judgeable by name.
func TestDNSForwarderTeachesTheNoSNIVerdict(t *testing.T) {
	ip := net.ParseIP("203.0.113.9").To4()
	answer := buildDNSResponse("good.example", ip, 300)
	upstream := fakeResolver(t, answer)
	udp, _ := stubDNSListeners(t)

	g := newTestGateway(t)
	g.logDNS = true
	g.applyRoutes(Routes{RunSubnets: map[string]string{"run-1": "127.0.0.0/8"}})
	go g.serveDNSUDP(upstream)
	pc := <-udp

	c, err := net.Dial("udp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write([]byte("query")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 512)
	n, err := c.Read(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(got[:n]) != string(answer) {
		t.Fatalf("client got %x, want the upstream's %x", got[:n], answer)
	}

	names := g.dns.names(ip)
	if len(names) != 1 || names[0] != "good.example" {
		t.Fatalf("snooped names = %v", names)
	}
	pol := newPolicy(File{Mode: ModeAllow, Domains: []string{"good.example"}})
	if !pol.verdictNoSNI(names, ip) {
		t.Fatal("a resolved, allowlisted name must pass with no SNI")
	}
}

// No upstream, no answer: the client's own retry logic decides, the forwarder
// survives, and nothing is learned.
func TestDNSForwarderStaysUpWhenUpstreamIsDown(t *testing.T) {
	// A bound-then-closed port refuses the datagram.
	dead, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upstream := dead.LocalAddr().String()
	dead.Close()

	udp, _ := stubDNSListeners(t)
	g := newTestGateway(t)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		g.serveDNSUDP(upstream)
	}()
	pc := <-udp
	c, err := net.Dial("udp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("query")); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if n, err := c.Read(make([]byte, 16)); err == nil {
		t.Fatalf("client got %d bytes from a dead upstream", n)
	}

	pc.Close()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("serveDNSUDP kept running on a closed socket")
	}
}

func TestDNSTCPForwardsTheStream(t *testing.T) {
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	go func() {
		c, err := up.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 5)
		if _, err := io.ReadFull(c, buf); err == nil {
			_, _ = c.Write([]byte(strings.ToUpper(string(buf))))
		}
	}()

	_, tcp := stubDNSListeners(t)
	g := newTestGateway(t)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		g.serveDNSTCP(up.Addr().String())
	}()
	ln := <-tcp
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write([]byte("query")); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "QUERY" {
		t.Fatalf("client got %q", got)
	}

	ln.Close()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("serveDNSTCP kept running on a closed listener")
	}
}

func TestDNSTCPClosesTheClientWhenUpstreamIsDown(t *testing.T) {
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upstream := dead.Addr().String()
	dead.Close()

	_, tcp := stubDNSListeners(t)
	g := newTestGateway(t)
	go g.serveDNSTCP(upstream)
	ln := <-tcp
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if got, _ := io.ReadAll(c); len(got) != 0 {
		t.Fatalf("client got %q", got)
	}
}

// A port that cannot be bound is logged and given up on: the filter keeps
// running without DNS rather than taking the gateway down.
func TestServeDNSSurvivesABindFailure(t *testing.T) {
	origP, origS := listenPacket, listenStream
	t.Cleanup(func() { listenPacket, listenStream = origP, origS })
	boom := errors.New("address in use")
	listenPacket = func(string, string) (net.PacketConn, error) { return nil, boom }
	listenStream = func(string, string) (net.Listener, error) { return nil, boom }

	g := newTestGateway(t)
	for name, serve := range map[string]func(string){"udp": g.serveDNSUDP, "tcp": g.serveDNSTCP} {
		done := make(chan struct{})
		go func() { defer close(done); serve("127.0.0.1:1") }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: did not return after the bind failed", name)
		}
	}
}

func TestServeDNSStartsBothTransports(t *testing.T) {
	udp, tcp := stubDNSListeners(t)
	g := newTestGateway(t)
	g.serveDNS("127.0.0.1:1")
	for name, ok := range map[string]bool{"udp": <-udp != nil, "tcp": <-tcp != nil} {
		if !ok {
			t.Errorf("%s not listening", name)
		}
	}
}

func TestResolvConfUpstreamEdges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolv.conf")
	if _, err := resolvConfUpstream(path); err == nil {
		t.Error("a missing file must be an error")
	}
	if err := os.WriteFile(path, []byte("search svc.cluster.local\nnameserver not-an-ip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if up, err := resolvConfUpstream(path); err == nil {
		t.Errorf("resolvConfUpstream = %q, want an error without a usable nameserver", up)
	}
}

func dnsHeader(qd, an uint16) []byte {
	return []byte{0, 1, 0x81, 0x80, byte(qd >> 8), byte(qd), byte(an >> 8), byte(an), 0, 0, 0, 0}
}

func dnsName(name string) []byte {
	var b []byte
	for _, l := range splitDots(name) {
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	return append(b, 0)
}

// answerRR is a compressed-name answer pointing back at the question (offset 12).
func answerRR(typ uint16, ttl uint32, rdata []byte) []byte {
	b := []byte{0xC0, 12, byte(typ >> 8), byte(typ), 0, 1,
		byte(ttl >> 24), byte(ttl >> 16), byte(ttl >> 8), byte(ttl),
		byte(len(rdata) >> 8), byte(len(rdata))}
	return append(b, rdata...)
}

func dnsMessage(an uint16, name string, rrs ...[]byte) []byte {
	m := dnsHeader(1, an)
	m = append(m, dnsName(name)...)
	m = append(m, 0, 1, 0, 1)
	for _, rr := range rrs {
		m = append(m, rr...)
	}
	return m
}

func TestParseDNSAnswersKinds(t *testing.T) {
	v4 := net.ParseIP("192.0.2.1").To4()
	v6 := net.ParseIP("2001:db8::1").To16()
	cname := dnsName("alias.example")

	name, ips, ttl := parseDNSAnswers(dnsMessage(3, "www.example",
		answerRR(5, 10, cname), answerRR(1, 200, v4), answerRR(28, 100, v6)))
	if name != "www.example" {
		t.Errorf("name = %q", name)
	}
	if len(ips) != 2 || !ips[0].Equal(v4) || !ips[1].Equal(v6) {
		t.Errorf("ips = %v", ips)
	}
	if ttl != 100*time.Second { // the smallest address TTL; the CNAME's does not count
		t.Errorf("ttl = %s", ttl)
	}

	if _, ips, _ := parseDNSAnswers(dnsMessage(1, "x.example", answerRR(1, 60, []byte{1, 2, 3}))); len(ips) != 0 {
		t.Errorf("an A record of 3 bytes yielded %v", ips)
	}
	if _, _, ttl := parseDNSAnswers(dnsMessage(1, "x.example", answerRR(1, 0, v4))); ttl != 300*time.Second {
		t.Errorf("a zero TTL = %s, want the 300s default", ttl)
	}
}

func TestParseDNSAnswersMalformed(t *testing.T) {
	v4 := net.ParseIP("192.0.2.1").To4()
	good := dnsMessage(1, "x.example", answerRR(1, 60, v4))
	if _, ips, _ := parseDNSAnswers(good); len(ips) != 1 {
		t.Fatalf("control: ips = %v", ips)
	}

	cases := map[string][]byte{
		"shorter than a header": good[:11],
		"question cut short":    good[:14],
		"loop in the question":  append(dnsHeader(1, 0), 0xC0, 12),
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if q, ips, _ := parseDNSAnswers(m); q != "" || len(ips) != 0 {
				t.Errorf("parseDNSAnswers = %q, %v", q, ips)
			}
		})
	}

	// Damage after the question loses the answers, keeps the name, and never panics.
	for name, m := range map[string][]byte{
		"answer header cut":    good[:len(good)-8],
		"rdata cut":            good[:len(good)-2],
		"answer count lies":    dnsMessage(9, "x.example", answerRR(1, 60, v4))[:len(good)],
		"answer name unusable": dnsMessage(1, "x.example", []byte{0xC0}, make([]byte, 12)),
	} {
		t.Run(name, func(t *testing.T) {
			q, _, _ := parseDNSAnswers(m)
			if q != "x.example" {
				t.Errorf("name = %q", q)
			}
		})
	}
}

func TestReadNameBounds(t *testing.T) {
	for name, c := range map[string]struct {
		m   []byte
		off int
	}{
		"offset past the end":  {[]byte{0}, 5},
		"negative offset":      {[]byte{0}, -1},
		"label past the end":   {[]byte{10, 'a'}, 0},
		"pointer cut in half":  {[]byte{0xC0}, 0},
		"pointer past the end": {[]byte{0xFF, 0xFF}, 0},
		"pointer to itself":    {[]byte{0xC0, 0}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			if got, _, ok := readName(c.m, c.off); ok {
				t.Errorf("readName = %q, want failure", got)
			}
		})
	}

	// A pointer's next-record offset is right after the pointer, not the target.
	m := append(dnsName("a.example"), 0xC0, 0, 0xEE)
	got, next, ok := readName(m, len(m)-3)
	if !ok || got != "a.example" || next != len(m)-1 {
		t.Errorf("readName = %q, %d, %v", got, next, ok)
	}
}

func TestDNSCacheBounds(t *testing.T) {
	c := newDNSCache()
	ip := net.ParseIP("192.0.2.1")

	c.remember("", []net.IP{ip}, time.Hour)
	c.remember("x.example", nil, time.Hour)
	if len(c.byIP) != 0 {
		t.Fatalf("empty facts were stored: %+v", c.byIP)
	}

	c.remember("Short.Example.", []net.IP{ip}, time.Second)
	hits := c.byIP[ip.String()]
	if len(hits) != 1 || hits[0].name != "short.example" {
		t.Fatalf("hits = %+v, want the normalised name", hits)
	}
	if left := time.Until(hits[0].exp); left < 50*time.Second || left > time.Minute {
		t.Errorf("a short TTL kept for %s, want the 1m floor", left)
	}

	c.remember("short.example", []net.IP{ip}, 24*time.Hour)
	hits = c.byIP[ip.String()]
	if len(hits) != 1 {
		t.Fatalf("a repeat must refresh, not add: %+v", hits)
	}
	if left := time.Until(hits[0].exp); left < 59*time.Minute || left > time.Hour {
		t.Errorf("a long TTL kept for %s, want the 1h ceiling", left)
	}

	// One name past the cap drops the table, rather than growing it.
	for i := range 8193 {
		c.byIP[itoa(i)] = nil
	}
	c.remember("fresh.example", []net.IP{ip}, time.Hour)
	if len(c.byIP) != 1 {
		t.Fatalf("table = %d entries, want a reset to 1", len(c.byIP))
	}

	// names keeps the live hits and drops the expired ones.
	c.byIP[ip.String()] = []dnsHit{
		{name: "old.example", exp: time.Now().Add(-time.Second)},
		{name: "live.example", exp: time.Now().Add(time.Hour)},
	}
	if got := c.names(ip); len(got) != 1 || got[0] != "live.example" {
		t.Fatalf("names = %v", got)
	}
	if len(c.byIP[ip.String()]) != 1 {
		t.Fatalf("expired hit was kept: %+v", c.byIP[ip.String()])
	}
	if got := c.names(net.ParseIP("192.0.2.2")); got != nil {
		t.Fatalf("names of an unknown IP = %v", got)
	}
}
