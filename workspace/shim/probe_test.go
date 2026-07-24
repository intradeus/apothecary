package main

import (
	"net"
	"os/exec"
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

func buildShim(t *testing.T) string {
	t.Helper()
	bin := t.TempDir() + "/shim"
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build shim: %v\n%s", err, out)
	}
	return bin
}
