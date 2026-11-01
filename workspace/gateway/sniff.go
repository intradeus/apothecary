package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"strings"
)

// peekSNI reads (WITHOUT consuming) a TLS ClientHello from br and returns the
// SNI host, or "" if absent/unparseable. Hand-rolled — no crypto/tls, no MITM.
func peekSNI(br *bufio.Reader) string {
	hdr, err := br.Peek(5) // record: type(1)=0x16, version(2), length(2)
	if err != nil || hdr[0] != 0x16 {
		return ""
	}
	recLen := int(binary.BigEndian.Uint16(hdr[3:5]))
	buf, err := br.Peek(5 + recLen)
	if err != nil {
		if buf, _ = br.Peek(br.Buffered()); len(buf) < 6 {
			return ""
		}
	}
	return clientHelloSNI(buf[5:])
}

func clientHelloSNI(b []byte) string {
	// handshake: type(1)=0x01, length(3), version(2), random(32), then variable
	if len(b) < 38 || b[0] != 0x01 {
		return ""
	}
	p := 38
	if p >= len(b) {
		return ""
	}
	p += 1 + int(b[p]) // session id
	if p+2 > len(b) {
		return ""
	}
	p += 2 + int(binary.BigEndian.Uint16(b[p:])) // cipher suites
	if p+1 > len(b) {
		return ""
	}
	p += 1 + int(b[p]) // compression methods
	if p+2 > len(b) {
		return ""
	}
	extLen := int(binary.BigEndian.Uint16(b[p:]))
	p += 2
	end := p + extLen
	if end > len(b) {
		end = len(b)
	}
	for p+4 <= end {
		etype := binary.BigEndian.Uint16(b[p:])
		elen := int(binary.BigEndian.Uint16(b[p+2:]))
		p += 4
		if p+elen > end {
			return ""
		}
		if etype == 0 { // server_name
			d := b[p : p+elen]
			// server_name_list len(2), entry: type(1)=0, name len(2), name
			if len(d) < 5 || d[2] != 0 {
				return ""
			}
			nlen := int(binary.BigEndian.Uint16(d[3:]))
			if 5+nlen > len(d) {
				return ""
			}
			return strings.ToLower(string(d[5 : 5+nlen]))
		}
		p += elen
	}
	return ""
}

// peekHTTPHost reads (WITHOUT consuming) an HTTP request head and returns the
// Host header value (no port), or "". It waits for the end of the head, not
// for a full window: a short request's client sends nothing more until it is
// answered.
func peekHTTPHost(br *bufio.Reader) string {
	headEnd := []byte("\r\n\r\n")
	var buf []byte
	for !bytes.Contains(buf, headEnd) && len(buf) < 4096 {
		// One byte past what is here: blocks only until something new arrives.
		if _, err := br.Peek(len(buf) + 1); err != nil {
			break
		}
		buf, _ = br.Peek(min(4096, br.Buffered()))
	}
	buf, _ = br.Peek(min(4096, br.Buffered()))
	i := bytes.Index(buf, headEnd)
	if i < 0 {
		i = len(buf)
	}
	for _, line := range strings.Split(string(buf[:i]), "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "host:") {
			h := strings.TrimSpace(line[5:])
			if c := strings.IndexByte(h, ':'); c >= 0 {
				h = h[:c]
			}
			return strings.ToLower(h)
		}
	}
	return ""
}
