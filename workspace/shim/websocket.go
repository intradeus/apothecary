package main

// A minimal RFC 6455 WebSocket server — enough for one terminal stream. The
// shim is built from its own source tree with two pinned dependencies and
// nothing else (see wsimage.EnsureShim), so this is stdlib by choice as
// much as necessity, and small: no extensions, no subprotocols, server role
// only.

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

const (
	wsOpCont   = 0x0
	wsOpText   = 0x1
	wsOpBinary = 0x2
	wsOpClose  = 0x8
	wsOpPing   = 0x9
	wsOpPong   = 0xA
)

type wsConn struct {
	c   net.Conn
	br  *bufio.Reader
	wmu sync.Mutex
}

func wsAccept(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		!headerHasToken(r.Header.Get("Connection"), "upgrade") {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return nil, errors.New("not a websocket request")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return nil, errors.New("missing key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return nil, errors.New("hijack unsupported")
	}
	c, brw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := c.Write([]byte(resp)); err != nil {
		c.Close()
		return nil, err
	}
	return &wsConn{c: c, br: brw.Reader}, nil
}

func headerHasToken(v, tok string) bool {
	for f := range strings.SplitSeq(v, ",") {
		if strings.EqualFold(strings.TrimSpace(f), tok) {
			return true
		}
	}
	return false
}

// read returns the next complete data message (text or binary), answering
// pings and reassembling fragments on the way.
func (ws *wsConn) read() (op byte, payload []byte, err error) {
	var msg []byte
	var msgOp byte
	for {
		fin, opcode, data, err := ws.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch opcode {
		case wsOpPing:
			_ = ws.write(wsOpPong, data)
			continue
		case wsOpPong:
			continue
		case wsOpClose:
			_ = ws.write(wsOpClose, data)
			return 0, nil, io.EOF
		case wsOpText, wsOpBinary:
			msgOp, msg = opcode, data
		case wsOpCont:
			msg = append(msg, data...)
		}
		if fin {
			return msgOp, msg, nil
		}
	}
}

const wsMaxFrame = 4 << 20

func (ws *wsConn) readFrame() (fin bool, op byte, data []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(ws.br, h[:]); err != nil {
		return
	}
	fin = h[0]&0x80 != 0
	op = h[0] & 0x0f
	masked := h[1]&0x80 != 0
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(ws.br, b[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(ws.br, b[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if n > wsMaxFrame {
		err = errors.New("websocket frame too large")
		return
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(ws.br, mask[:]); err != nil {
			return
		}
	}
	data = make([]byte, n)
	if _, err = io.ReadFull(ws.br, data); err != nil {
		return
	}
	if masked {
		for i := range data {
			data[i] ^= mask[i%4]
		}
	}
	return
}

func (ws *wsConn) write(op byte, data []byte) error {
	ws.wmu.Lock()
	defer ws.wmu.Unlock()
	hdr := []byte{0x80 | op}
	switch n := len(data); {
	case n < 126:
		hdr = append(hdr, byte(n))
	case n <= 0xffff:
		hdr = append(hdr, 126, byte(n>>8), byte(n))
	default:
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		hdr = append(append(hdr, 127), b[:]...)
	}
	if _, err := ws.c.Write(hdr); err != nil {
		return err
	}
	_, err := ws.c.Write(data)
	return err
}

func (ws *wsConn) close() error {
	_ = ws.write(wsOpClose, []byte{0x03, 0xe8}) // 1000 normal closure
	return ws.c.Close()
}
