package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// upstreamReply is what the fake internet host answers with.
const upstreamReply = "upstream-says-hi"

// interception stands in for the kernel half of the transparent proxy: every
// connection handed to handle "was redirected from" dst, and whatever handle
// dials lands on a loopback server instead of :80/:443.
type interception struct {
	dst      *net.TCPAddr
	dialed   atomic.Int32
	dialAddr atomic.Pointer[string]
	dialErr  error
	received chan []byte // what the upstream read, once per connection
	addr     string
}

func newInterception(t *testing.T, dst *net.TCPAddr) *interception {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ix := &interception{dst: dst, received: make(chan []byte, 4), addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				b, _ := io.ReadAll(c)
				ix.received <- b
				_, _ = c.Write([]byte(upstreamReply))
			}()
		}
	}()

	origDst, origDial := originalDstOf, dialUpstream
	t.Cleanup(func() { originalDstOf, dialUpstream = origDst, origDial })
	originalDstOf = func(*net.TCPConn) (*net.TCPAddr, error) { return ix.dst, nil }
	dialUpstream = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		ix.dialed.Add(1)
		ix.dialAddr.Store(&addr)
		if ix.dialErr != nil {
			return nil, ix.dialErr
		}
		return net.DialTimeout(network, ix.addr, timeout)
	}
	return ix
}

// pairThroughHandle connects a sandbox-side client to a running g.handle.
func pairThroughHandle(t *testing.T, g *gateway) (client *net.TCPConn, done <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		g.handle(srv.(*net.TCPConn))
	}()
	// Runs before the seams are restored (cleanups are LIFO).
	t.Cleanup(func() {
		c.Close()
		<-finished
	})
	return c.(*net.TCPConn), finished
}

// exchange sends payload as a sandbox would, half-closes, and returns all the
// client got back once handle is finished.
func exchange(t *testing.T, g *gateway, payload []byte) []byte {
	t.Helper()
	client, done := pairThroughHandle(t, g)
	if _, err := client.Write(payload); err != nil {
		t.Fatalf("client write: %v", err)
	}
	_ = client.CloseWrite()
	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
	got, err := io.ReadAll(client)
	// A drop with bytes still unread reaches the client as a reset.
	if err != nil && !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("client read: %v", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("handle did not finish")
	}
	return got
}

func httpReq(host string) []byte {
	return []byte("GET / HTTP/1.1\r\nHost: " + host + "\r\n\r\n")
}

// garbage is bytes on :443 that are no ClientHello, so there is no SNI to read.
var garbage = []byte("\x17\x03\x03not-a-client-hello")

func TestHandleFiltersByHostAndPolicy(t *testing.T) {
	dstIP := net.ParseIP("203.0.113.7").To4()
	allowList := File{Mode: ModeAllow, Domains: []string{"good.example", "*.cdn.example"}}
	denyList := File{Mode: ModeDeny, Domains: []string{"bad.example", "*.ads.example"}}
	cases := []struct {
		name      string
		pol       File
		port      int
		payload   []byte
		dnsName   string // the destination IP was resolved from this name
		wantAllow bool
		wantHost  string
		wantProto string
	}{
		// allow mode: deny-all + allowlist.
		{"allow mode passes a listed SNI", allowList, 443, buildClientHello("good.example"), "", true, "good.example", "tls"},
		{"allow mode folds SNI case", allowList, 443, buildClientHello("GOOD.Example"), "", true, "good.example", "tls"},
		{"allow mode ignores an SNI's root dot", allowList, 443, buildClientHello("good.example."), "", true, "good.example.", "tls"},
		{"allow mode passes a wildcard SNI", allowList, 443, buildClientHello("a.cdn.example"), "", true, "a.cdn.example", "tls"},
		{"allow mode wildcard excludes the apex", allowList, 443, buildClientHello("cdn.example"), "", false, "cdn.example", "tls"},
		{"allow mode refuses a lookalike suffix", allowList, 443, buildClientHello("evilgood.example"), "", false, "evilgood.example", "tls"},
		{"allow mode refuses an unlisted SNI", allowList, 443, buildClientHello("evil.example"), "", false, "evil.example", "tls"},
		{"allow mode passes a listed Host", allowList, 80, httpReq("good.example"), "", true, "good.example", "http"},
		{"allow mode strips the Host port", allowList, 80, httpReq("good.example:8080"), "", true, "good.example", "http"},
		{"allow mode refuses an unlisted Host", allowList, 80, httpReq("evil.example"), "", false, "evil.example", "http"},
		// The SNI, not the DNS history of the IP, is what gets judged when present.
		{"allow mode prefers the SNI over the IP's DNS name", allowList, 443, buildClientHello("evil.example"), "good.example", false, "evil.example", "tls"},

		// deny mode: allow-all + denylist.
		{"deny mode refuses a listed SNI", denyList, 443, buildClientHello("bad.example"), "", false, "bad.example", "tls"},
		{"deny mode refuses a wildcard Host", denyList, 80, httpReq("x.ads.example"), "", false, "x.ads.example", "http"},
		{"deny mode passes everything else", denyList, 443, buildClientHello("fine.example"), "", true, "fine.example", "tls"},
		{"deny mode passes an unlisted Host", denyList, 80, httpReq("fine.example"), "", true, "fine.example", "http"},

		// Force-block beats the allowlist.
		{"a blocked domain beats the allowlist",
			File{Mode: ModeAllow, Domains: []string{"good.example"}, BlockedDomains: []string{"good.example"}},
			443, buildClientHello("good.example"), "", false, "good.example", "tls"},
		{"a blocked IP beats the allowlist",
			File{Mode: ModeAllow, Domains: []string{"good.example"}, BlockedDomains: []string{dstIP.String()}},
			443, buildClientHello("good.example"), "", false, "good.example", "tls"},

		// No readable name: judged by what the IP was resolved from.
		{"no SNI, resolved name allowlisted", allowList, 443, garbage, "good.example", true, "good.example", "tls"},
		{"no SNI, resolved name not allowlisted", allowList, 443, garbage, "evil.example", false, "evil.example", "tls"},
		{"no SNI, never resolved, allow mode", allowList, 443, garbage, "", false, dstIP.String(), "tls"},
		{"no SNI, never resolved, deny mode", denyList, 443, garbage, "", true, dstIP.String(), "tls"},
		{"no SNI, resolved name denylisted", denyList, 443, garbage, "bad.example", false, "bad.example", "tls"},
		{"no SNI, allowlisted IP literal",
			File{Mode: ModeAllow, Domains: []string{dstIP.String()}}, 443, garbage, "", true, dstIP.String(), "tls"},
		{"no Host header on :80", allowList, 80, []byte("GET / HTTP/1.0\r\n\r\n"), "", false, dstIP.String(), "http"},
		{"another port is never parsed for a name", allowList, 8443, buildClientHello("good.example"), "", false, dstIP.String(), "other"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newTestGateway(t)
			g.applyRoutes(Routes{RunSubnets: map[string]string{"run-1": "127.0.0.0/8"}})
			if err := g.installPolicy(c.pol); err != nil {
				t.Fatal(err)
			}
			if c.dnsName != "" {
				g.dns.remember(c.dnsName, []net.IP{dstIP}, time.Hour)
			}
			ix := newInterception(t, &net.TCPAddr{IP: dstIP, Port: c.port})

			got := exchange(t, g, c.payload)

			recs := g.audit.snapshot("run-1")
			if len(recs) != 1 {
				t.Fatalf("audit records = %+v, want exactly one for run-1", recs)
			}
			r := recs[0]
			if r.Host != c.wantHost || r.Port != c.port || r.Proto != c.wantProto || r.DstIP != dstIP.String() {
				t.Errorf("audit = %+v, want host=%q port=%d proto=%q", r, c.wantHost, c.port, c.wantProto)
			}
			if !c.wantAllow {
				if ix.dialed.Load() != 0 {
					t.Error("a denied connection reached the upstream dialer")
				}
				if len(got) != 0 {
					t.Errorf("denied client received %q", got)
				}
				if r.Verdict != "deny" || r.BytesIn != 0 || r.BytesOut != 0 {
					t.Errorf("audit = %+v, want a deny with no bytes", r)
				}
				if d := g.denials["run-1\x00"+c.wantHost]; d == nil || d.Count != 1 {
					t.Errorf("denials = %+v, want one for %q", g.denials, c.wantHost)
				}
				return
			}
			if string(got) != upstreamReply {
				t.Errorf("client received %q, want the upstream's reply", got)
			}
			// The peeked bytes must be forwarded verbatim, exactly once.
			if up := <-ix.received; !bytes.Equal(up, c.payload) {
				t.Errorf("upstream received %q, want the client's %q", up, c.payload)
			}
			if a := ix.dialAddr.Load(); a == nil || *a != (&net.TCPAddr{IP: dstIP, Port: c.port}).String() {
				t.Errorf("dialed %v, want the original destination", a)
			}
			if r.Verdict != "allow" || r.BytesOut != int64(len(c.payload)) || r.BytesIn != int64(len(upstreamReply)) {
				t.Errorf("audit = %+v, want allow with %d out, %d in", r, len(c.payload), len(upstreamReply))
			}
			if len(g.denials) != 0 {
				t.Errorf("an allowed connection recorded denials: %+v", g.denials)
			}
		})
	}
}

// --protect-cidrs outranks everything the policy can say, including an explicit
// allow of that exact host or IP.
func TestHandleNeverReachesProtectedDestinations(t *testing.T) {
	meta := net.ParseIP("169.254.169.254").To4()
	_, linkLocal, _ := net.ParseCIDR("169.254.0.0/16")
	cases := []struct {
		name     string
		pol      File
		payload  []byte
		wantHost string
	}{
		{"allowlisted name", File{Mode: ModeAllow, Domains: []string{"metadata.example"}}, buildClientHello("metadata.example"), "metadata.example"},
		{"allowlisted IP", File{Mode: ModeAllow, Domains: []string{meta.String()}}, garbage, meta.String()},
		{"deny mode allows everything else", File{Mode: ModeDeny}, buildClientHello("fine.example"), "fine.example"},
		{"deny mode, no SNI", File{Mode: ModeDeny}, garbage, meta.String()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newTestGateway(t)
			g.protect = []*net.IPNet{linkLocal}
			if err := g.installPolicy(c.pol); err != nil {
				t.Fatal(err)
			}
			ix := newInterception(t, &net.TCPAddr{IP: meta, Port: 443})
			if got := exchange(t, g, c.payload); len(got) != 0 {
				t.Errorf("client received %q", got)
			}
			if ix.dialed.Load() != 0 {
				t.Fatal("a protected destination was dialed")
			}
			if d := g.denials["\x00"+c.wantHost]; d == nil {
				t.Errorf("denials = %+v, want one for %q", g.denials, c.wantHost)
			}
		})
	}
}

// Every run of a project is a peer on the gateway's one wg0, so the tunnel
// subnet is protected (main appends it): a run naming an allowlisted host, in
// deny mode, or inside its provisioning window still never gets the filter to
// dial another run's address.
func TestHandleNeverDialsAnotherRun(t *testing.T) {
	sibling := net.ParseIP("10.90.0.3").To4()
	_, runLinks, _ := net.ParseCIDR("10.90.0.1/16")
	cases := []struct {
		name    string
		pol     File
		port    int
		payload []byte
		open    bool
	}{
		{"allowlisted SNI", File{Mode: ModeAllow, Domains: []string{"github.com"}}, 443, buildClientHello("github.com"), false},
		{"allowlisted Host", File{Mode: ModeAllow, Domains: []string{"github.com"}}, 80, []byte("GET / HTTP/1.1\r\nHost: github.com\r\n\r\n"), false},
		{"allowlisted IP", File{Mode: ModeAllow, Domains: []string{sibling.String()}}, 443, garbage, false},
		{"deny mode", File{Mode: ModeDeny}, 443, buildClientHello("fine.example"), false},
		{"provisioning window", File{Mode: ModeAllow}, 443, buildClientHello("anything.example"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newTestGateway(t)
			g.protect = []*net.IPNet{runLinks}
			if err := g.installPolicy(c.pol); err != nil {
				t.Fatal(err)
			}
			if c.open {
				g.applyRoutes(Routes{RunSubnets: map[string]string{"run-1": "127.0.0.0/8"},
					Open: map[string]time.Time{"run-1": time.Now().Add(time.Hour)}})
			}
			ix := newInterception(t, &net.TCPAddr{IP: sibling, Port: c.port})
			if got := exchange(t, g, c.payload); len(got) != 0 {
				t.Errorf("client received %q", got)
			}
			if ix.dialed.Load() != 0 {
				t.Fatal("another run's address was dialed")
			}
		})
	}
}

// With no policy loaded yet there is nothing to allow by: the gateway drops,
// and says nothing (there is no verdict to report).
func TestHandleFailsClosedWithoutAPolicy(t *testing.T) {
	g := newTestGateway(t)
	ix := newInterception(t, &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 443})
	if got := exchange(t, g, buildClientHello("good.example")); len(got) != 0 {
		t.Errorf("client received %q", got)
	}
	if ix.dialed.Load() != 0 || len(g.audit.snapshot("")) != 0 {
		t.Errorf("dialed %d, audit %+v: want neither", ix.dialed.Load(), g.audit.snapshot(""))
	}
}

// A connection whose destination cannot be recovered is closed, not forwarded.
func TestHandleDropsWhenTheOriginalDestinationIsUnknown(t *testing.T) {
	g := newTestGateway(t)
	if err := g.installPolicy(File{Mode: ModeDeny}); err != nil {
		t.Fatal(err)
	}
	ix := newInterception(t, nil)
	originalDstOf = func(*net.TCPConn) (*net.TCPAddr, error) { return nil, errors.New("no conntrack entry") }
	if got := exchange(t, g, buildClientHello("fine.example")); len(got) != 0 {
		t.Errorf("client received %q", got)
	}
	if ix.dialed.Load() != 0 || len(g.audit.snapshot("")) != 0 {
		t.Error("an unattributable connection must be dropped without a trace")
	}
}

// An allowed connection whose upstream is down closes the client. It is not a
// denial: nothing should surface in the run's "add to egress" list.
func TestHandleClosesTheClientWhenUpstreamDialFails(t *testing.T) {
	g := newTestGateway(t)
	if err := g.installPolicy(File{Mode: ModeDeny}); err != nil {
		t.Fatal(err)
	}
	ix := newInterception(t, &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 443})
	ix.dialErr = errors.New("connection refused")
	if got := exchange(t, g, buildClientHello("fine.example")); len(got) != 0 {
		t.Errorf("client received %q", got)
	}
	if ix.dialed.Load() != 1 {
		t.Fatalf("dialed %d times, want 1", ix.dialed.Load())
	}
	if len(g.denials) != 0 {
		t.Errorf("denials = %+v, want none", g.denials)
	}
}

// Repeats of the same (run, host) aggregate rather than growing records.
func TestHandleAggregatesRepeatedDenials(t *testing.T) {
	g := newTestGateway(t)
	if err := g.installPolicy(File{Mode: ModeAllow}); err != nil {
		t.Fatal(err)
	}
	newInterception(t, &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 443})
	for range 3 {
		exchange(t, g, buildClientHello("evil.example"))
	}
	if d := g.denials["\x00evil.example"]; d == nil || d.Count != 3 {
		t.Errorf("denial = %+v, want count 3", d)
	}
	if recs := g.audit.snapshot(""); len(recs) != 1 || recs[0].Count != 3 {
		t.Errorf("audit = %+v, want one record of 3", recs)
	}
}

func TestServeDispatchesUntilTheListenerCloses(t *testing.T) {
	g := newTestGateway(t)
	if err := g.installPolicy(File{Mode: ModeAllow}); err != nil {
		t.Fatal(err)
	}
	newInterception(t, &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 443})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		g.serve(ln)
	}()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write(buildClientHello("evil.example")); err != nil {
		t.Fatal(err)
	}
	_ = c.(*net.TCPConn).CloseWrite()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if got, err := io.ReadAll(c); err != nil || len(got) != 0 {
		t.Fatalf("client read = %q, %v; want a closed connection", got, err)
	}
	// The verdict is recorded before the conn closes, but serve's goroutine is
	// ours to read from only under the mutex.
	g.dmu.Lock()
	d := g.denials["\x00evil.example"]
	g.dmu.Unlock()
	if d == nil {
		t.Error("the served connection was not judged")
	}

	ln.Close()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("serve kept running after the listener closed")
	}
}

// With conntrack loaded, an unredirected loopback socket's "original"
// destination is its real one, which pins the byte order of the decode. Without
// conntrack it must be an error, never a zero address handle would then dial.
func TestOriginalDstReadsTheConntrackTuple(t *testing.T) {
	a, b := loopbackPair(t)
	got, err := originalDst(a.(*net.TCPConn))
	if err != nil {
		t.Logf("no conntrack here: %v", err)
		return
	}
	want := b.LocalAddr().(*net.TCPAddr)
	if !got.IP.Equal(want.IP) || got.Port != want.Port {
		t.Fatalf("originalDst = %v, want %v", got, want)
	}
}

// A closed socket has no descriptor to ask.
func TestOriginalDstOnAClosedSocket(t *testing.T) {
	a, _ := loopbackPair(t)
	a.Close()
	if addr, err := originalDst(a.(*net.TCPConn)); err == nil {
		t.Fatalf("originalDst = %v, want an error", addr)
	}
}

func TestRunOr(t *testing.T) {
	if got := runOr(""); got != "-" {
		t.Errorf("runOr(\"\") = %q", got)
	}
	if got := runOr("run-1"); got != "run-1" {
		t.Errorf("runOr = %q", got)
	}
}

// pipe half-closes so the reverse direction can still finish.
func TestPipeHalfClosesTheWriter(t *testing.T) {
	w := &halfCloser{}
	if n := pipe(w, bytes.NewReader([]byte("abc"))); n != 3 {
		t.Errorf("pipe = %d, want 3", n)
	}
	if !w.closed || w.String() != "abc" {
		t.Errorf("writer = %q closed=%v", w.String(), w.closed)
	}
	// A plain writer has no half-close, and that is not an error.
	var plain bytes.Buffer
	if n := pipe(&plain, bytes.NewReader([]byte("ab"))); n != 2 || plain.String() != "ab" {
		t.Errorf("pipe to a plain writer = %d, %q", n, plain.String())
	}
}

type halfCloser struct {
	bytes.Buffer
	closed bool
}

func (h *halfCloser) CloseWrite() error { h.closed = true; return nil }

// Reader side of the sniffers: what a sandbox can throw at the parsers.

func TestClientHelloSNIMalformed(t *testing.T) {
	hello := buildClientHello("good.example")[5:] // the handshake, no record header
	truncate := func(n int) []byte { return hello[:n] }
	mutate := func(i int, v byte) []byte {
		b := append([]byte(nil), hello...)
		b[i] = v
		return b
	}
	// Offsets in buildClientHello's layout.
	const sid, ciphers, comp, extLen, ext = 38, 39, 43, 45, 47
	nameLen := ext + 4 + 3 // extension header, list length, entry type
	for name, in := range map[string][]byte{
		"empty":                      nil,
		"not a ClientHello":          mutate(0, 0x02),
		"too short":                  truncate(37),
		"ends after the random":      truncate(38),
		"session id runs past":       mutate(sid, 0xff),
		"cipher suites run past":     mutate(ciphers, 0xff),
		"compression runs past":      mutate(comp, 0xff),
		"ends before the extensions": truncate(extLen),
		"extension runs past the block": func() []byte {
			b := mutate(ext+2, 0x7f)
			return b
		}(),
		"server_name entry not a host name": mutate(ext+4+2, 1),
		"name length runs past":             mutate(nameLen, 0xff),
		"server_name too short": func() []byte {
			// A one-byte server_name extension.
			b := append([]byte(nil), hello[:ext]...)
			return append(b, 0, 0, 0, 1, 0)
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if got := clientHelloSNI(in); got != "" {
				t.Errorf("clientHelloSNI = %q, want no SNI", got)
			}
		})
	}

	t.Run("control: unmodified", func(t *testing.T) {
		if got := clientHelloSNI(hello); got != "good.example" {
			t.Fatalf("clientHelloSNI = %q", got)
		}
	})
	t.Run("skips other extensions", func(t *testing.T) {
		// A padding extension ahead of server_name.
		b := append([]byte(nil), hello[:ext]...)
		b = append(b, 0x00, 0x15, 0x00, 0x02, 0xaa, 0xbb)
		b = append(b, hello[ext:]...)
		total := len(hello) - ext + 6
		b[extLen], b[extLen+1] = byte(total>>8), byte(total)
		if got := clientHelloSNI(b); got != "good.example" {
			t.Fatalf("clientHelloSNI = %q", got)
		}
	})
	t.Run("no server_name extension", func(t *testing.T) {
		b := append([]byte(nil), hello[:ext]...)
		b = append(b, 0x00, 0x15, 0x00, 0x00)
		b[extLen], b[extLen+1] = 0, 4
		if got := clientHelloSNI(b); got != "" {
			t.Fatalf("clientHelloSNI = %q", got)
		}
	})
	t.Run("extension length clamps to the data", func(t *testing.T) {
		b := append([]byte(nil), hello...)
		b[extLen], b[extLen+1] = 0xff, 0xff
		if got := clientHelloSNI(b); got != "good.example" {
			t.Fatalf("clientHelloSNI = %q", got)
		}
	})
}

func TestPeekSNIShortAndForeignInput(t *testing.T) {
	hello := buildClientHello("good.example")
	for name, in := range map[string][]byte{
		"empty":              nil,
		"short header":       hello[:3],
		"not a handshake":    append([]byte{0x17}, hello[1:]...),
		"header only":        hello[:5],
		"one payload byte":   hello[:6],
		"truncated ":         hello[:30],
		"cut inside the sni": hello[:len(hello)-4],
	} {
		t.Run(name, func(t *testing.T) {
			if got := peekSNI(bufio.NewReader(bytes.NewReader(in))); got != "" {
				t.Errorf("peekSNI = %q, want none", got)
			}
		})
	}
}

// A hello bigger than the peek window is judged on what fits; the SNI sits
// early enough in real hellos for that to work, and nothing panics or hangs.
func TestPeekSNIOversizedRecord(t *testing.T) {
	hello := buildClientHello("good.example")
	// Claim a record far larger than the reader's buffer, with more to come.
	big := append([]byte(nil), hello...)
	big[3], big[4] = 0xff, 0xff
	big = append(big, make([]byte, 40<<10)...)
	if got := peekSNI(bufio.NewReaderSize(bytes.NewReader(big), 16<<10)); got != "good.example" {
		t.Fatalf("peekSNI = %q", got)
	}
}

// A client that stalls mid-hello must not hold the peek past the read deadline.
func TestPeekSNIGivesUpOnAStalledClient(t *testing.T) {
	a, b := loopbackPair(t)
	hello := buildClientHello("good.example")
	if _, err := a.Write(hello[:20]); err != nil {
		t.Fatal(err)
	}
	if err := b.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := peekSNI(bufio.NewReader(b)); got != "" {
		t.Fatalf("peekSNI = %q", got)
	}
}

func loopbackPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	a, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b, err := ln.Accept()
	if err != nil {
		a.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

func TestPeekHTTPHostForms(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"mixed case header", "GET / HTTP/1.1\r\nHoSt: Api.Example\r\n\r\n", "api.example"},
		{"later header", "GET / HTTP/1.1\r\nAccept: */*\r\nHost: api.example\r\n\r\n", "api.example"},
		{"no end of head yet", "GET / HTTP/1.1\r\nHost: api.example\r\n", "api.example"},
		{"a Host in the body does not count", "POST / HTTP/1.1\r\nX: y\r\n\r\nHost: evil.example\r\n", ""},
		{"no host", "GET / HTTP/1.0\r\n\r\n", ""},
		{"empty", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := peekHTTPHost(bufio.NewReader(bytes.NewReader([]byte(c.in)))); got != c.want {
				t.Errorf("peekHTTPHost = %q, want %q", got, c.want)
			}
		})
	}
}

// When bytes are already buffered they are used as they stand; the peek leaves
// them readable for the splice.
func TestPeekHTTPHostUsesBufferedBytes(t *testing.T) {
	req := httpReq("api.example")
	br := bufio.NewReader(bytes.NewReader(req))
	if _, err := br.Peek(len(req)); err != nil {
		t.Fatal(err)
	}
	if got := peekHTTPHost(br); got != "api.example" {
		t.Fatalf("peekHTTPHost = %q", got)
	}
	if br.Buffered() != len(req) {
		t.Fatalf("buffered = %d, want the whole request", br.Buffered())
	}
}

// A plain HTTP client sends its short request and then waits for the answer.
// The Host must be read from what arrived, not after the 10s read deadline
// handle sets, or every plain-HTTP connection stalls for that long.
func TestPeekHTTPHostDoesNotWaitForAFullWindow(t *testing.T) {
	a, b := loopbackPair(t)
	if _, err := a.Write(httpReq("api.example")); err != nil {
		t.Fatal(err)
	}
	// The stand-in for handle's deadline: reached only if the peek waits.
	if err := b.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got := peekHTTPHost(bufio.NewReaderSize(b, 16<<10))
	if took := time.Since(start); took >= 2*time.Second {
		t.Errorf("peek took %s: it waited for the read deadline", took)
	}
	if got != "api.example" {
		t.Errorf("peekHTTPHost = %q", got)
	}
}

// A request head split across segments is read to its end: the Host header
// comes after the first line.
func TestPeekHTTPHostReadsAHeadSplitAcrossSegments(t *testing.T) {
	a, b := loopbackPair(t)
	req := httpReq("api.example")
	if _, err := a.Write(req[:8]); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = a.Write(req[8:])
	}()
	if err := b.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReaderSize(b, 16<<10)
	if got := peekHTTPHost(br); got != "api.example" {
		t.Errorf("peekHTTPHost = %q", got)
	}
	if br.Buffered() != len(req) {
		t.Errorf("buffered = %d, want the whole request left to forward", br.Buffered())
	}
}

// A client that stops mid-head gets what it sent: the deadline ends the wait.
func TestPeekHTTPHostGivesUpAtTheDeadline(t *testing.T) {
	a, b := loopbackPair(t)
	if _, err := a.Write([]byte("GET / HTTP/1.1\r\nHost: api.example\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := b.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := peekHTTPHost(bufio.NewReaderSize(b, 16<<10)); got != "api.example" {
		t.Errorf("peekHTTPHost = %q", got)
	}
}
