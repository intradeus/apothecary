package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// wireConn is a websocket's far end: what it writes is what the server reads.
func wireConn(in []byte) (*wsConn, *bytes.Buffer) {
	var out bytes.Buffer
	return &wsConn{c: wireWriter{w: &out}, br: bufio.NewReader(bytes.NewReader(in))}, &out
}

type wireWriter struct {
	net.Conn
	w io.Writer
}

func (c wireWriter) Write(p []byte) (int, error) { return c.w.Write(p) }
func (c wireWriter) Close() error                { return nil }

// frame builds one client frame, masked as RFC 6455 requires, with the
// shortest length encoding unless long asks for the 8-byte one.
func frame(fin bool, op byte, data []byte, long bool) []byte {
	b0 := op
	if fin {
		b0 |= 0x80
	}
	out := []byte{b0}
	switch n := len(data); {
	case long:
		out = append(out, 0x80|127)
		out = binary.BigEndian.AppendUint64(out, uint64(n))
	case n < 126:
		out = append(out, 0x80|byte(n))
	default:
		out = append(out, 0x80|126)
		out = binary.BigEndian.AppendUint16(out, uint16(n))
	}
	mask := []byte{9, 8, 7, 6}
	out = append(out, mask...)
	for i, c := range data {
		out = append(out, c^mask[i%4])
	}
	return out
}

func TestWSReadsEveryLengthEncoding(t *testing.T) {
	for _, c := range []struct {
		name string
		size int
		long bool
	}{{"short", 5, false}, {"16-bit", 300, false}, {"64-bit", 70000, true}} {
		data := bytes.Repeat([]byte("x"), c.size)
		ws, _ := wireConn(frame(true, wsOpBinary, data, c.long))
		op, got, err := ws.read()
		if err != nil || op != wsOpBinary || !bytes.Equal(got, data) {
			t.Errorf("%s: op %d, %d bytes, %v", c.name, op, len(got), err)
		}
	}
}

// An unmasked frame reads as written: a lenient server, as the terminal is
// only ever reached through the engine.
func TestWSReadsAnUnmaskedFrame(t *testing.T) {
	ws, _ := wireConn([]byte{0x80 | wsOpText, 2, 'h', 'i'})
	if op, got, err := ws.read(); err != nil || op != wsOpText || string(got) != "hi" {
		t.Errorf("op %d, %q, %v", op, got, err)
	}
}

// A message comes back whole across fragments, under its first frame's type,
// and pings are answered on the way without ending it.
func TestWSReassemblesFragmentsAroundAPing(t *testing.T) {
	var in []byte
	in = append(in, frame(false, wsOpText, []byte("hel"), false)...)
	in = append(in, frame(true, wsOpPing, []byte("p"), false)...)
	in = append(in, frame(true, wsOpPong, nil, false)...)
	in = append(in, frame(true, wsOpCont, []byte("lo"), false)...)
	ws, out := wireConn(in)
	op, got, err := ws.read()
	if err != nil || op != wsOpText || string(got) != "hello" {
		t.Fatalf("op %d, %q, %v", op, got, err)
	}
	if !bytes.Equal(out.Bytes(), []byte{0x80 | wsOpPong, 1, 'p'}) {
		t.Errorf("pong = %v", out.Bytes())
	}
}

// A close is answered with a close and reads as the end of the stream.
func TestWSCloseIsEchoed(t *testing.T) {
	ws, out := wireConn(frame(true, wsOpClose, []byte{0x03, 0xe8}, false))
	if _, _, err := ws.read(); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v", err)
	}
	if !bytes.Equal(out.Bytes(), []byte{0x80 | wsOpClose, 2, 0x03, 0xe8}) {
		t.Errorf("close reply = %v", out.Bytes())
	}
}

// A frame past the cap is refused before anything is allocated for it, and a
// stream that ends mid-frame is an error at every stage.
func TestWSRefusesBrokenFrames(t *testing.T) {
	huge := binary.BigEndian.AppendUint64([]byte{0x80 | wsOpBinary, 127}, wsMaxFrame+1)
	if _, _, err := func() (byte, []byte, error) { ws, _ := wireConn(huge); return ws.read() }(); err == nil ||
		!strings.Contains(err.Error(), "too large") {
		t.Errorf("oversized frame: %v", err)
	}
	whole := frame(true, wsOpBinary, bytes.Repeat([]byte("x"), 300), false)
	for _, cut := range []int{1, 3, 5, 20} { // header, 16-bit length, mask, payload
		ws, _ := wireConn(whole[:cut])
		if _, _, err := ws.read(); err == nil {
			t.Errorf("cut at %d read as a frame", cut)
		}
	}
	long := frame(true, wsOpBinary, []byte("x"), true)
	ws, _ := wireConn(long[:5]) // inside the 64-bit length
	if _, _, err := ws.read(); err == nil {
		t.Error("a cut 64-bit length read as a frame")
	}
}

func TestWSWriteLengthEncodings(t *testing.T) {
	for _, c := range []struct {
		size int
		head []byte
	}{
		{5, []byte{0x80 | wsOpBinary, 5}},
		{300, []byte{0x80 | wsOpBinary, 126, 1, 44}},
		{70000, append([]byte{0x80 | wsOpBinary, 127}, binary.BigEndian.AppendUint64(nil, 70000)...)},
	} {
		ws, out := wireConn(nil)
		if err := ws.write(wsOpBinary, make([]byte, c.size)); err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(out.Bytes(), c.head) || out.Len() != len(c.head)+c.size {
			t.Errorf("%d bytes: wrote %d, head %v", c.size, out.Len(), out.Bytes()[:len(c.head)])
		}
	}
}

// failingConn refuses its writes, from the nth on.
type failingConn struct {
	net.Conn
	after int
}

func (c *failingConn) Write(p []byte) (int, error) {
	if c.after--; c.after < 0 {
		return 0, errors.New("broken pipe")
	}
	return len(p), nil
}

func TestWSWriteReportsABrokenConnection(t *testing.T) {
	for after := range 2 { // the header, then the payload
		ws := &wsConn{c: &failingConn{after: after}}
		if err := ws.write(wsOpBinary, []byte("x")); err == nil {
			t.Errorf("write with %d good writes succeeded", after)
		}
	}
}

// An upgrade needs the right headers and a connection that can be taken over.
func TestWSAcceptRefuses(t *testing.T) {
	req := func(h map[string]string) *http.Request {
		r := httptest.NewRequest("GET", "/term", nil)
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	upgrade := map[string]string{"Upgrade": "WebSocket", "Connection": "keep-alive, Upgrade"}
	full := map[string]string{"Upgrade": "websocket", "Connection": "upgrade", "Sec-WebSocket-Key": "a2V5"}
	for _, c := range []struct {
		h    map[string]string
		code int
	}{
		{nil, http.StatusBadRequest},
		{map[string]string{"Upgrade": "websocket", "Connection": "close"}, http.StatusBadRequest},
		{upgrade, http.StatusBadRequest},       // no key
		{full, http.StatusInternalServerError}, // a recorder cannot be hijacked
	} {
		rec := httptest.NewRecorder()
		if _, err := wsAccept(rec, req(c.h)); err == nil || rec.Code != c.code {
			t.Errorf("%v: code %d, %v, want %d", c.h, rec.Code, err, c.code)
		}
	}
}

// hijackRefused is a server whose hijack fails, or whose client is gone by
// the time of the handshake's answer.
type hijackRefused struct {
	http.ResponseWriter
	conn net.Conn
	err  error
}

func (h hijackRefused) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.conn, bufio.NewReadWriter(bufio.NewReader(strings.NewReader("")), nil), h.err
}

func TestWSAcceptHijackFailures(t *testing.T) {
	r := httptest.NewRequest("GET", "/term", nil)
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Connection", "upgrade")
	r.Header.Set("Sec-WebSocket-Key", "a2V5")
	if _, err := wsAccept(hijackRefused{httptest.NewRecorder(), nil, errors.New("no")}, r); err == nil {
		t.Error("a failed hijack upgraded")
	}
	client, server := net.Pipe()
	client.Close()
	if _, err := wsAccept(hijackRefused{httptest.NewRecorder(), server, nil}, r); err == nil {
		t.Error("an upgrade to a gone client succeeded")
	}
}

func TestWSCloseSendsNormalClosure(t *testing.T) {
	ws, out := wireConn(nil)
	if err := ws.close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), []byte{0x80 | wsOpClose, 2, 0x03, 0xe8}) {
		t.Errorf("close frame = %v", out.Bytes())
	}
}
