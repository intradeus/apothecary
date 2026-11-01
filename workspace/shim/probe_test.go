package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runProbe calls os.Exit, so drive it as a subprocess via the compiled test
// binary re-invoking `go run`. Simpler: test the observable behavior by
// building the shim and running `probe` against a real/absent listener.
func TestProbe(t *testing.T) {
	bin := buildShim(t)

	// A live listener → exit 0.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if out, err := exec.Command(bin, "probe", ln.Addr().String()).CombinedOutput(); err != nil {
		t.Errorf("probe of a live listener failed: %v (%s)", err, out)
	}

	// Nothing listening → exit non-zero (bounded --wait so it returns fast).
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := free.Addr().String()
	free.Close() // now nothing is listening on addr
	if err := exec.Command(bin, "probe", "--wait", "300ms", addr).Run(); err == nil {
		t.Error("probe of a dead address must exit non-zero")
	}
}

// --tls: a listener that accepts but never speaks TLS (the egress gateway
// refusing a host looks like this) fails; a real TLS server passes.
func TestProbeTLS(t *testing.T) {
	bin := buildShim(t)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")
	if out, err := exec.Command(bin, "probe", "--tls", addr).CombinedOutput(); err != nil {
		t.Errorf("tls probe of a TLS server failed: %v (%s)", err, out)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	out, err := exec.Command(bin, "probe", "--tls", ln.Addr().String()).CombinedOutput()
	if err == nil {
		t.Error("tls probe of a listener that hangs up must exit non-zero")
	}
	if !strings.Contains(string(out), "tls handshake") {
		t.Errorf("failure should say why, got %q", out)
	}
}

func buildShim(t *testing.T) string {
	t.Helper()
	bin := t.TempDir() + "/shim"
	args := []string{"build", "-o", bin}
	// `task cover:go`: what these subprocesses run counts too. Under `go test
	// -cover` GOCOVERDIR is the test binary's own, so it is pointed here.
	if dir := os.Getenv("APOTHECARY_COVERDIR"); dir != "" {
		t.Setenv("GOCOVERDIR", dir)
		args = append(args, "-cover", "-covermode=atomic", "-tags=coverflush")
	}
	if out, err := exec.Command("go", append(args, ".")...).CombinedOutput(); err != nil {
		t.Fatalf("build shim: %v\n%s", err, out)
	}
	return bin
}

// probeOnce in-process: the plain connect, the TLS handshake, and the ways
// each fails.
func TestProbeOnce(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	// A live listener answers a plain connect.
	if err := probeOnce(ln.Addr().String(), false); err != nil {
		t.Errorf("plain connect: %v", err)
	}
	// But the TLS handshake against it fails (it hangs up).
	if err := probeOnce(ln.Addr().String(), true); err == nil || !strings.Contains(err.Error(), "tls handshake") {
		t.Errorf("tls against a non-TLS listener: %v", err)
	}

	// A real TLS server passes the handshake.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	if err := probeOnce(strings.TrimPrefix(srv.URL, "https://"), true); err != nil {
		t.Errorf("tls against a TLS server: %v", err)
	}

	// Nothing listening: the connect itself fails.
	free, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := free.Addr().String()
	free.Close()
	if err := probeOnce(addr, false); err == nil {
		t.Error("a dead address must fail")
	}

	// An address with no port cannot be split for the SNI.
	if err := probeOnce("127.0.0.1:0", true); err == nil {
		t.Error("port 0 must fail the connect")
	}
}
