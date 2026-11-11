package browser

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"
)

// The routing proxy is the browser's only way out: Chromium runs in a pod
// whose network policy allows nothing but the engine's session mux, its
// --proxy-server a loopback relay inside the pod that pipes to this proxy
// over that mux. Every request names a host, and a host the run serves —
// localhost, 127.0.0.1, ::1, *.localhost — goes to the run's workspace port
// of the same number (Playwright sends loopback through the proxy too: it
// adds <-loopback> to Chromium's bypass list). Anything else is refused: the browser must not be a way around the
// project's egress gateway, and an app under development that needs the
// internet reaches it from its own workspace, where the gateway judges it.

// Dial reaches a port the run's workspace serves.
type Dial func(ctx context.Context, port int) (net.Conn, error)

// proxy is one session's routing proxy.
type proxy struct {
	dial Dial
	log  *slog.Logger
	rp   *httputil.ReverseProxy
}

func newProxy(dial Dial, log *slog.Logger) *proxy {
	p := &proxy{dial: dial, log: log}
	p.rp = &httputil.ReverseProxy{
		// The request is already absolute-form (proxy style): send it as is.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.Host = pr.In.Host
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				_, port, err := splitTarget(addr, "80")
				if err != nil {
					return nil, err
				}
				return dial(ctx, port)
			},
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       30 * time.Second,
			ResponseHeaderTimeout: 2 * time.Minute,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "apothecary browser: the run's "+r.URL.Host+" did not answer: "+err.Error(), http.StatusBadGateway)
		},
	}
	return p
}

// isLoopback reports whether host names the run itself.
func isLoopback(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// splitTarget parses host[:port] with a default port.
func splitTarget(hostport, defPort string) (host string, port int, err error) {
	host, p, err := net.SplitHostPort(hostport)
	if err != nil {
		host, p = hostport, defPort
	}
	port, err = strconv.Atoi(p)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, &net.AddrError{Err: "bad port", Addr: hostport}
	}
	return host, port, nil
}

func (p *proxy) refuse(w http.ResponseWriter, host string) {
	p.log.Info("browser: refused a request outside the run", "host", host)
	http.Error(w, "apothecary browser: "+host+" is outside the run — this browser reaches the run's services at localhost only", http.StatusForbidden)
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if r.URL.Host == "" {
		http.Error(w, "apothecary browser: a proxy request names its host", http.StatusBadRequest)
		return
	}
	if r.URL.Scheme != "http" {
		http.Error(w, "apothecary browser: only http is proxied in the clear (https goes through CONNECT)", http.StatusBadRequest)
		return
	}
	host, _, err := splitTarget(r.URL.Host, "80")
	if err != nil {
		http.Error(w, "apothecary browser: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !isLoopback(host) {
		p.refuse(w, host)
		return
	}
	p.rp.ServeHTTP(w, r)
}

// connect tunnels a CONNECT (https, and every WebSocket) to the run's port.
func (p *proxy) connect(w http.ResponseWriter, r *http.Request) {
	host, port, err := splitTarget(r.Host, "443")
	if err != nil {
		http.Error(w, "apothecary browser: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !isLoopback(host) {
		p.refuse(w, host)
		return
	}
	up, err := p.dial(r.Context(), port)
	if err != nil {
		http.Error(w, "apothecary browser: the run's port "+strconv.Itoa(port)+" did not answer: "+err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "apothecary browser: cannot tunnel", http.StatusInternalServerError)
		return
	}
	c, brw, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	if _, err := c.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		c.Close()
		up.Close()
		return
	}
	// Whatever the client sent after its CONNECT is already buffered.
	var client io.Reader = c
	if n := brw.Reader.Buffered(); n > 0 {
		head, _ := brw.Reader.Peek(n)
		client = io.MultiReader(bytes.NewReader(bytes.Clone(head)), c)
	}
	splice(c, client, up)
}

// splice pumps both ways. Once the run's side is done, the client gets a
// moment to finish its own half before both are closed.
func splice(c net.Conn, from io.Reader, up net.Conn) {
	defer c.Close()
	defer up.Close()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(up, from)
		halfClose(up)
		close(done)
	}()
	_, _ = io.Copy(c, up)
	halfClose(c)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

func halfClose(c net.Conn) {
	type closeWriter interface{ CloseWrite() error }
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
	} else {
		_ = c.Close()
	}
}
