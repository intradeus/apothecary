package main

// The dial-in mux: how an engine that does not share a filesystem with the
// workspace (Kubernetes) reaches the unix sockets the shim serves in its shim
// dir — the sidecar (sidecar.sock) and exposed app ports (app-<port>.sock).
//
// `relay --serve <addr>` runs beside the workspace (a sidecar container in the
// sandbox pod, on the same volume) and listens on TCP. Each connection opens
// with one preamble line naming the socket, then carries raw bytes:
//
//	APO1 <token> <socket name>\n
//
// The token is the run's relay token (a pod secret the engine also holds), so
// reaching the port is not enough; the network policy already lets only the
// engine reach it. Socket names are plain file names in --dir, never paths.
//
// Once the socket is connected the mux answers "OK\n" and the stream is raw;
// a refused or failed connection is just closed. The acknowledgement is what
// lets the engine tell "nothing serves that socket yet" from "connected".
//
// `browser-session --engine` speaks the same preamble to the engine's browser
// proxy listener, with the socket name "proxy".

import (
	"bufio"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	preambleMagic = "APO1"
	muxAck        = "OK\n"
)

var sockNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*\.sock$`)

// writePreamble opens a mux connection.
func writePreamble(w io.Writer, token, target string) error {
	_, err := fmt.Fprintf(w, "%s %s %s\n", preambleMagic, token, target)
	return err
}

// readPreamble reads and checks one preamble line; it returns the target and
// a reader positioned at the first payload byte.
func readPreamble(c net.Conn, token string) (string, *bufio.Reader, error) {
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer c.SetReadDeadline(time.Time{})
	br := bufio.NewReaderSize(c, 512)
	line, err := br.ReadSlice('\n')
	if err != nil {
		return "", nil, fmt.Errorf("preamble: %w", err)
	}
	f := strings.Fields(string(line))
	if len(f) != 3 || f[0] != preambleMagic {
		return "", nil, fmt.Errorf("preamble: malformed")
	}
	if token == "" || subtle.ConstantTimeCompare([]byte(f[1]), []byte(token)) != 1 {
		return "", nil, fmt.Errorf("preamble: bad token")
	}
	return f[2], br, nil
}

// startMux listens on addr and splices each authenticated connection to the
// named unix socket in dir.
func startMux(addr, dir, token string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				name, br, err := readPreamble(c, token)
				if err != nil || !sockNameRE.MatchString(name) {
					return
				}
				d, err := net.Dial("unix", filepath.Join(dir, name))
				if err != nil {
					return
				}
				defer d.Close()
				if _, err := c.Write([]byte(muxAck)); err != nil {
					return
				}
				go func() {
					_, _ = io.Copy(d, br) // buffered payload first, then the rest
					halfClose(d)
				}()
				_, _ = io.Copy(c, d)
				halfClose(c)
			}(c)
		}
	}()
	return ln, nil
}

// pumpStdio splices stdin/stdout with the unix socket sock until either side
// ends — one connection carried over an exec stream.
func pumpStdio(sock string, in io.Reader, out io.Writer) error {
	c, err := net.Dial("unix", sock)
	if err != nil {
		return err
	}
	defer c.Close()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(out, c)
		close(done)
	}()
	_, _ = io.Copy(c, in)
	halfClose(c)
	<-done
	return nil
}

func readTokenFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		fatal("token file: %v", err)
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		fatal("token file %s is empty", path)
	}
	return t
}
