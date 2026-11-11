// Package wstunnel carries one TCP stream inside one WebSocket: the way a
// person's machine reaches a workspace port or the SSH door through the
// engine's HTTPS address, with nothing else open. WebSocket rather than a
// bare HTTP upgrade because every ingress controller and load balancer in
// front of the engine (an ALB, Traefik, nginx, a VPN's proxy) passes it.
//
// Both ends speak the same thing: binary messages whose payloads, in order,
// are the stream. Each end pings on a timer, so a proxy's idle timeout never
// cuts a quiet tunnel (an open database connection, an idle shell), and a
// peer that is gone shows as a ping that cannot be written.
package wstunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// SSHHostSuffix ends the hostname a person ssh-es to for a workspace
// (`ssh <workspace>.apothecary`): the apothecary CLI's ssh config routes
// every such host through its ssh-proxy, and the run page prints it.
const SSHHostSuffix = ".apothecary"

// PingEvery is how often a tunnel pings: under the 60 s idle timeout of the
// usual load balancers. A var for tests; each conn copies it when made.
var PingEvery = 25 * time.Second

const writeWait = 10 * time.Second

var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 32 << 10,
	// The engine's own guard decides who may open a tunnel (the session's
	// owner, from the UI's own origin or with a bearer token) before Accept
	// runs; the upgrader does not second-guess it.
	CheckOrigin: func(*http.Request) bool { return true },
}

// Accept upgrades r to a tunnel. On failure the upgrader has answered w.
func Accept(w http.ResponseWriter, r *http.Request) (net.Conn, error) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, err
	}
	return newConn(ws), nil
}

// Dial opens a tunnel to url (ws:// or wss://) with the given request
// headers (the bearer token). A refusal before the upgrade comes back as an
// *HTTPError carrying the engine's status and message.
func Dial(ctx context.Context, url string, header http.Header) (net.Conn, error) {
	d := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 30 * time.Second,
		ReadBufferSize:   32 << 10,
		WriteBufferSize:  32 << 10,
	}
	ws, resp, err := d.DialContext(ctx, url, header)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
			return nil, &HTTPError{Status: resp.StatusCode, Body: string(body)}
		}
		return nil, err
	}
	return newConn(ws), nil
}

// HTTPError is the engine refusing a tunnel before it opened.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return http.StatusText(e.Status)
	}
	return http.StatusText(e.Status) + ": " + e.Body
}

// Pipe copies a to b and b to a until either side ends, then closes both.
func Pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		// One direction ended: let the other side see EOF, then stop.
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}

// conn is a WebSocket as a net.Conn: one reader, writers serialised, a
// pinger while open. Control frames (pings, pongs, close) are gorilla's
// concurrent-safe WriteControl, so they need no lock of ours.
type conn struct {
	ws    *websocket.Conn
	every time.Duration

	rmu sync.Mutex
	r   io.Reader // the current message's rest

	wmu sync.Mutex

	done      chan struct{}
	closeOnce sync.Once
}

func newConn(ws *websocket.Conn) *conn {
	c := &conn{ws: ws, every: PingEvery, done: make(chan struct{})}
	go c.ping()
	return c
}

func (c *conn) ping() {
	t := time.NewTicker(c.every)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			if err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
				_ = c.Close()
				return
			}
		}
	}
}

func (c *conn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	for {
		if c.r == nil {
			typ, r, err := c.ws.NextReader()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					return 0, io.EOF
				}
				return 0, err
			}
			if typ != websocket.BinaryMessage {
				continue
			}
			c.r = r
		}
		n, err := c.r.Read(p)
		if errors.Is(err, io.EOF) {
			c.r = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *conn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
	if err := c.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// CloseWrite says the stream is over in this direction: a normal close
// frame, after which the peer reads EOF. Reads continue until the peer
// closes too (Close).
func (c *conn) CloseWrite() error {
	return c.ws.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(writeWait))
}

func (c *conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.done)
		err = c.ws.Close()
	})
	return err
}

func (c *conn) LocalAddr() net.Addr                { return c.ws.LocalAddr() }
func (c *conn) RemoteAddr() net.Addr               { return c.ws.RemoteAddr() }
func (c *conn) SetDeadline(t time.Time) error      { return c.ws.NetConn().SetDeadline(t) }
func (c *conn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *conn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }
