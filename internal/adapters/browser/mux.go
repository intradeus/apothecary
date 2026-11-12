package browser

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"net"
	"strings"
	"sync"
	"time"
)

// SessionMux is the engine's ONE TCP listener for every browser session's
// routing proxy (the browsers are Kubernetes pods). A session's relay opens
// each connection with the same preamble the shim's dial-in mux uses:
//
//	APO1 <session token> proxy\n
//
// and the mux hands it to that session's listener. A connection with an
// unknown token is closed; the browser pod's network policy already lets it
// reach nothing but this port.
type SessionMux struct {
	ln       net.Listener
	addr     string // what browser-session --engine dials
	mu       sync.Mutex
	sessions map[string]*sessionListener
}

// NewSessionMux listens on listen; advertise is the address the browser
// reaches it at (a Service name:port) — listen's own when empty.
func NewSessionMux(listen, advertise string) (*SessionMux, error) {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}
	if advertise == "" {
		advertise = ln.Addr().String()
	}
	m := &SessionMux{ln: ln, addr: advertise, sessions: map[string]*sessionListener{}}
	go m.accept()
	return m, nil
}

func (m *SessionMux) Close() error { return m.ln.Close() }

// Addr is the advertised address.
func (m *SessionMux) Addr() string { return m.addr }

// Register opens a session: its listener and the token its relay presents.
func (m *SessionMux) Register() (net.Listener, string) {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	sl := &sessionListener{conns: make(chan net.Conn), done: make(chan struct{}), addr: m.ln.Addr()}
	sl.close = func() {
		m.mu.Lock()
		delete(m.sessions, tok)
		m.mu.Unlock()
	}
	m.mu.Lock()
	m.sessions[tok] = sl
	m.mu.Unlock()
	return sl, tok
}

func (m *SessionMux) accept() {
	for {
		c, err := m.ln.Accept()
		if err != nil {
			return
		}
		go m.route(c)
	}
}

func (m *SessionMux) route(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReaderSize(c, 512)
	line, err := br.ReadSlice('\n')
	f := strings.Fields(string(line))
	if err != nil || len(f) != 3 || f[0] != "APO1" || f[2] != "proxy" {
		c.Close()
		return
	}
	m.mu.Lock()
	sl := m.sessions[f[1]]
	m.mu.Unlock()
	if sl == nil {
		c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	select {
	case sl.conns <- &bufferedConn{Conn: c, r: br}:
	case <-sl.done:
		c.Close()
	}
}

// sessionListener is one session's view of the mux.
type sessionListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
	close func()
	addr  net.Addr
}

func (s *sessionListener) Accept() (net.Conn, error) {
	select {
	case c := <-s.conns:
		return c, nil
	case <-s.done:
		return nil, net.ErrClosed
	}
}

func (s *sessionListener) Close() error {
	s.once.Do(func() {
		s.close()
		close(s.done)
	})
	return nil
}

func (s *sessionListener) Addr() net.Addr { return s.addr }

// bufferedConn reads what the preamble reader buffered first.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }
