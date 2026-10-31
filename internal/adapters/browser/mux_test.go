package browser

import (
	"bufio"
	"io"
	"net"
	"testing"
	"time"
)

func TestSessionMuxRoutesByToken(t *testing.T) {
	m, err := NewSessionMux("127.0.0.1:0", "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ln1, tok1 := m.Register()
	ln2, tok2 := m.Register()
	defer ln1.Close()
	defer ln2.Close()
	if tok1 == tok2 || len(tok1) != 48 {
		t.Fatalf("tokens %q %q", tok1, tok2)
	}
	go func() {
		c, err := ln2.Accept()
		if err != nil {
			return
		}
		line, _ := bufio.NewReader(c).ReadString('\n')
		c.Write([]byte("s2:" + line))
		c.Close()
	}()
	c, err := net.Dial("tcp", m.Addr())
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("APO1 " + tok2 + " proxy\nGET / HTTP/1.1\n"))
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, _ := io.ReadAll(c)
	if string(got) != "s2:GET / HTTP/1.1\n" {
		t.Fatalf("got %q", got)
	}
}

func TestSessionMuxDropsUnknownAndClosedSessions(t *testing.T) {
	m, err := NewSessionMux("127.0.0.1:0", "browser.svc:7071")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.Addr() != "browser.svc:7071" {
		t.Fatalf("Addr = %q", m.Addr())
	}
	ln, tok := m.Register()
	ln.Close()
	if _, err := ln.Accept(); err == nil {
		t.Fatal("closed session still accepts")
	}
	for _, pre := range []string{"APO1 " + tok + " proxy\n", "APO1 nope proxy\n", "garbage\n"} {
		c, err := net.Dial("tcp", m.ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		c.Write([]byte(pre))
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		b, _ := io.ReadAll(c)
		c.Close()
		if len(b) != 0 {
			t.Errorf("%q: got %q, want closed", pre, b)
		}
	}
}
