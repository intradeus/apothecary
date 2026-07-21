// Package egressproxy is the hostname-filtering egress proxy (M18): the
// permanent fix for the resolve-then-pin firewall's failure modes (silent
// blocks, IP rotation, per-provider domain sleuthing).
//
// It runs engine-side on loopback and is reverse-forwarded into every
// workspace by the coder adapter (the same mechanism as the engine port).
// The workspace firewall collapses to "loopback + coder gateway only";
// all HTTP(S) leaves through HTTPS_PROXY/HTTP_PROXY pointing here, and
// filtering happens BY HOSTNAME — exact or `*.wildcard` patterns —
// against a per-run policy the engine installs at provision time
// (derived domains ∪ project extra_domains).
//
// Identification: proxy Basic credentials `runID : HMAC(secret, runID)` —
// deterministic, so engine restarts don't invalidate what bootstrap wrote
// into the workspace. The token identifies the run's policy; it is not a
// high-value secret (the proxy only grants that run's own allowlist).
//
// Every denial is recorded per run (host + count) and surfaced in the run
// page, so "the firewall ate my model provider" becomes a visible one-line
// diagnosis instead of a silent 30-minute hang.
package egressproxy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"sort"
	"strings"
	"sync"
	"time"
)

// Denial is one blocked host, aggregated (PLAN: UI shows "blocked: X ×N").
type Denial struct {
	Host  string    `json:"host"`
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
}

type policy struct {
	exact     map[string]bool
	wildcards []string // ".suffix" forms, matched against host tails
	denials   map[string]*Denial
}

type Server struct {
	secret []byte
	log    *slog.Logger

	mu       sync.Mutex
	policies map[string]*policy

	ln   net.Listener
	srv  *http.Server
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

func New(secret []byte) *Server {
	d := &net.Dialer{Timeout: 15 * time.Second}
	return &Server{
		secret:   secret,
		log:      slog.Default(),
		policies: map[string]*policy{},
		dial:     d.DialContext,
	}
}

// Start binds the listener (loopback address) and serves in the background.
func (s *Server) Start(listen string) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("egress proxy listen %s: %w", listen, err)
	}
	s.ln = ln
	s.srv = &http.Server{Handler: s}
	go func() { _ = s.srv.Serve(ln) }()
	return nil
}

func (s *Server) Close() {
	if s.srv != nil {
		_ = s.srv.Close()
	}
}

// Port returns the bound port (0 before Start).
func (s *Server) Port() int {
	if s.ln == nil {
		return 0
	}
	if a, ok := s.ln.Addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

// Credentials returns the per-run proxy Basic credentials. Deterministic:
// survives engine restarts without re-writing workspace files.
func (s *Server) Credentials(runID string) (user, pass string) {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte("egress-proxy:" + runID))
	return runID, hex.EncodeToString(m.Sum(nil))[:32]
}

// SetPolicy installs (or replaces) a run's allowed hostname patterns:
// exact hosts, or `*.suffix` wildcards. Case-insensitive. Denial history
// is kept across policy updates.
func (s *Server) SetPolicy(runID string, hostPatterns []string) {
	p := &policy{exact: map[string]bool{}, denials: map[string]*Denial{}}
	for _, h := range hostPatterns {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(h, "*."); ok {
			p.wildcards = append(p.wildcards, "."+rest)
			continue
		}
		p.exact[h] = true
	}
	s.mu.Lock()
	if old, ok := s.policies[runID]; ok {
		p.denials = old.denials
	}
	s.policies[runID] = p
	s.mu.Unlock()
}

func (s *Server) DropPolicy(runID string) {
	s.mu.Lock()
	delete(s.policies, runID)
	s.mu.Unlock()
}

// AllowHost adds one host (exact, or `*.suffix`) to a run's live allowlist
// and clears its denial — the "add to egress" fast path. The workspace
// already routes through the proxy, so the agent's next request to the
// host succeeds instantly, no restart. Returns false if the run has no
// active policy (proxy mode off, or the run already ended).
func (s *Server) AllowHost(runID, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.policies[runID]
	if !ok {
		return false
	}
	if rest, isWild := strings.CutPrefix(host, "*."); isWild {
		p.wildcards = append(p.wildcards, "."+rest)
	} else {
		p.exact[host] = true
	}
	delete(p.denials, host) // no longer blocked
	return true
}

// Denials lists a run's blocked hosts, most recent first.
func (s *Server) Denials(runID string) []Denial {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.policies[runID]
	if !ok {
		return nil
	}
	out := make([]Denial, 0, len(p.denials))
	for _, d := range p.denials {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out
}

// allowed checks host (no port) against the run's policy.
func (s *Server) allowed(runID, host string) bool {
	host = strings.ToLower(host)
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.policies[runID]
	if !ok {
		return false
	}
	if p.exact[host] {
		return true
	}
	for _, w := range p.wildcards {
		if strings.HasSuffix(host, w) {
			return true
		}
	}
	return false
}

func (s *Server) recordDenial(runID, host string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.policies[runID]
	if !ok {
		return
	}
	d, ok := p.denials[host]
	if !ok {
		if len(p.denials) >= 100 {
			return // cap distinct hosts; counts keep growing on known ones
		}
		d = &Denial{Host: host}
		p.denials[host] = d
	}
	d.Count++
	d.Last = time.Now().UTC()
}

// auth extracts and verifies Proxy-Authorization; returns the run id.
func (s *Server) auth(r *http.Request) (string, bool) {
	h := r.Header.Get("Proxy-Authorization")
	raw, ok := strings.CutPrefix(h, "Basic ")
	if !ok {
		return "", false
	}
	dec, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", false
	}
	user, pass, ok := strings.Cut(string(dec), ":")
	if !ok {
		return "", false
	}
	_, want := s.Credentials(user)
	if subtle.ConstantTimeCompare([]byte(pass), []byte(want)) != 1 {
		return "", false
	}
	return user, true
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	runID, ok := s.auth(r)
	if !ok {
		w.Header().Set("Proxy-Authenticate", `Basic realm="apothecary-egress"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		s.handleConnect(w, r, runID)
		return
	}
	// Absolute-form plain-HTTP proxying (rare — providers are HTTPS — but
	// git/tools may probe http:// first).
	if !r.URL.IsAbs() {
		http.Error(w, "this is a proxy; use CONNECT or absolute-form requests", http.StatusBadRequest)
		return
	}
	host := r.URL.Hostname()
	if !s.allowed(runID, host) {
		s.recordDenial(runID, host)
		http.Error(w, fmt.Sprintf("apothecary egress: host %q is not in this run's allowlist", host), http.StatusForbidden)
		return
	}
	rp := &httputil.ReverseProxy{Director: func(req *http.Request) {
		req.Header.Del("Proxy-Authorization")
	}}
	rp.ServeHTTP(w, r)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request, runID string) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host, port = r.Host, "443"
	}
	// Same posture as the old firewall rules: TLS/HTTP ports only.
	if port != "443" && port != "80" {
		s.recordDenial(runID, host+":"+port)
		http.Error(w, "apothecary egress: only ports 443 and 80 are allowed", http.StatusForbidden)
		return
	}
	if !s.allowed(runID, host) {
		s.recordDenial(runID, host)
		http.Error(w, fmt.Sprintf("apothecary egress: host %q is not in this run's allowlist", host), http.StatusForbidden)
		return
	}
	upstream, err := s.dial(r.Context(), "tcp", net.JoinHostPort(host, port))
	if err != nil {
		http.Error(w, "apothecary egress: upstream dial failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	defer client.Close()
	defer upstream.Close()

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	// The client may have pipelined TLS bytes (an early ClientHello) into
	// the hijack buffer before the 200 — forward those first, or the
	// handshake stalls.
	if n := buf.Reader.Buffered(); n > 0 {
		if b, perr := buf.Reader.Peek(n); perr == nil {
			if _, werr := upstream.Write(b); werr != nil {
				return
			}
		}
	}
	// Bidirectional tunnel with HALF-CLOSE: when one direction ends, signal
	// EOF on that write side only (CloseWrite) and let the OTHER direction
	// drain — tearing both down at once truncates an in-flight transfer and
	// the peer sees a reset (found live: git push reset mid-upload). Full
	// close happens only after BOTH directions finish (deferred above).
	done := make(chan struct{}, 2)
	go func() { pipe(upstream, client); done <- struct{}{} }()
	go func() { pipe(client, upstream); done <- struct{}{} }()
	<-done
	<-done
}

// pipe copies src→dst, then half-closes dst's write side so the peer sees
// a clean EOF without losing the reverse direction's buffered data.
func pipe(dst, src net.Conn) {
	_, _ = io.Copy(dst, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
