package egressproxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func startProxy(t *testing.T) *Server {
	t.Helper()
	s := New([]byte("test-secret"))
	if err := s.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// client routes through the proxy with the given run's credentials.
func client(t *testing.T, s *Server, user, pass string) *http.Client {
	t.Helper()
	proxyURL, err := url.Parse(fmt.Sprintf("http://%s:%s@127.0.0.1:%d", user, pass, s.Port()))
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

func TestConnectFilteringByHostname(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "hello from backend")
	}))
	defer backend.Close()
	backendAddr := strings.TrimPrefix(backend.URL, "https://")

	s := startProxy(t)
	// Route every allowed upstream dial to the local TLS backend: hostname
	// policy, auth, hijack and the byte-copy loop stay fully real; only
	// name resolution is bypassed (the proxy is the resolver by design).
	s.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, backendAddr)
	}
	s.SetPolicy("run1", []string{"api.example.test", "*.wild.test"})
	user, pass := s.Credentials("run1")
	c := client(t, s, user, pass)

	// Exact allow.
	res, err := c.Get("https://api.example.test/x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != "hello from backend" {
		t.Errorf("body = %q", body)
	}

	// Wildcard allow.
	if res, err := c.Get("https://deep.sub.wild.test/"); err != nil {
		t.Errorf("wildcard: %v", err)
	} else {
		res.Body.Close()
	}
	// Wildcard must not match the bare suffix owner-adjacent host.
	if _, err := c.Get("https://evilwild.test/"); err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("evilwild err = %v, want 403", err)
	}

	// Deny + denial record.
	if _, err := c.Get("https://blocked.test/"); err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("blocked err = %v, want 403", err)
	}
	_, _ = c.Get("https://blocked.test/") // count aggregates
	dens := s.Denials("run1")
	found := false
	for _, d := range dens {
		if d.Host == "blocked.test" && d.Count == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("denials = %+v, want blocked.test ×2", dens)
	}

	// Policy isolation: another run has no access to run1's allowlist.
	u2, p2 := s.Credentials("run2")
	c2 := client(t, s, u2, p2)
	if _, err := c2.Get("https://api.example.test/"); err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("cross-run err = %v, want 403", err)
	}
}

func TestProxyAuth(t *testing.T) {
	s := startProxy(t)
	s.SetPolicy("run1", []string{"api.example.test"})
	// Wrong password → 407.
	c := client(t, s, "run1", "wrong-token")
	if _, err := c.Get("https://api.example.test/"); err == nil || !strings.Contains(err.Error(), "Proxy Authentication Required") {
		t.Errorf("bad-auth err = %v, want 407", err)
	}
}

func TestNonTLSPortRefused(t *testing.T) {
	s := startProxy(t)
	s.SetPolicy("run1", []string{"api.example.test"})
	user, pass := s.Credentials("run1")
	// Raw CONNECT to a forbidden port.
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", s.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	basic := fmt.Sprintf("%s:%s", user, pass)
	fmt.Fprintf(conn, "CONNECT api.example.test:2222 HTTP/1.1\r\nHost: api.example.test:2222\r\nProxy-Authorization: Basic %s\r\n\r\n",
		b64(basic))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	if !strings.Contains(string(buf[:n]), "403") {
		t.Errorf("response = %q, want 403", buf[:n])
	}
}

func TestPlainHTTPAbsoluteForm(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "plain ok")
	}))
	defer backend.Close()
	host := strings.TrimPrefix(backend.URL, "http://")
	hostname, _, _ := net.SplitHostPort(host)

	s := startProxy(t)
	s.SetPolicy("run1", []string{hostname})
	user, pass := s.Credentials("run1")
	c := client(t, s, user, pass)
	res, err := c.Get(backend.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != "plain ok" {
		t.Errorf("body = %q", body)
	}
}

func b64(s string) string {
	const tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var out strings.Builder
	data := []byte(s)
	for i := 0; i < len(data); i += 3 {
		var b [3]byte
		n := copy(b[:], data[i:])
		out.WriteByte(tbl[b[0]>>2])
		out.WriteByte(tbl[(b[0]&3)<<4|b[1]>>4])
		if n > 1 {
			out.WriteByte(tbl[(b[1]&15)<<2|b[2]>>6])
		} else {
			out.WriteByte('=')
		}
		if n > 2 {
			out.WriteByte(tbl[b[2]&63])
		} else {
			out.WriteByte('=')
		}
	}
	return out.String()
}

// TestAllowHostLive: adding a host mid-run unblocks it without a new
// policy install — the "add to egress" fast path.
func TestAllowHostLive(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()
	backendAddr := strings.TrimPrefix(backend.URL, "https://")

	s := startProxy(t)
	s.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, backendAddr)
	}
	s.SetPolicy("run1", []string{"allowed.test"})
	user, pass := s.Credentials("run1")
	c := client(t, s, user, pass)

	// Blocked first; a denial is recorded.
	if _, err := c.Get("https://late.test/"); err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Fatalf("pre-allow err = %v", err)
	}
	if len(s.Denials("run1")) != 1 {
		t.Fatalf("denials = %+v", s.Denials("run1"))
	}
	// Allow it live → succeeds, denial cleared.
	if !s.AllowHost("run1", "late.test") {
		t.Fatal("AllowHost returned false")
	}
	if res, err := c.Get("https://late.test/"); err != nil {
		t.Errorf("post-allow err = %v", err)
	} else {
		res.Body.Close()
	}
	if len(s.Denials("run1")) != 0 {
		t.Errorf("denial not cleared: %+v", s.Denials("run1"))
	}
	// Unknown run → false.
	if s.AllowHost("ghost", "x.test") {
		t.Error("AllowHost on unknown run returned true")
	}
}

// TestConnectLargeBidirectional guards the half-close fix: a large upload
// + large response must round-trip through the CONNECT tunnel without
// truncation/reset (the git-push failure mode).
func TestConnectLargeBidirectional(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Write(body) // echo — response size == request size
	}))
	defer backend.Close()
	backendAddr := strings.TrimPrefix(backend.URL, "https://")

	s := startProxy(t)
	s.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, backendAddr)
	}
	s.SetPolicy("run1", []string{"big.test"})
	user, pass := s.Credentials("run1")
	c := client(t, s, user, pass)

	payload := make([]byte, 2<<20) // 2 MiB up and back
	for i := range payload {
		payload[i] = byte(i)
	}
	res, err := c.Post("https://big.test/echo", "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	got, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != len(payload) || !bytes.Equal(got, payload) {
		t.Errorf("round-trip mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}
