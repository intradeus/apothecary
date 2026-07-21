//go:build live

package devcontainer

// Live integration test against the real local toolchain. Excluded from
// normal runs; execute manually with:
//
//	go test -tags live -run TestLiveLifecycle -v ./internal/adapters/devcontainer/
//
// Requires: a docker daemon, the @devcontainers/cli (`npm install -g
// @devcontainers/cli`) and git on PATH.

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"apothecary/internal/ports"
)

func TestLiveLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// A tiny "target repo" WITH its own devcontainer.
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	// node:*-slim: node is a hard requirement of the shim + engine relay.
	devc := `{
  "image": "node:22-bookworm-slim",
  "containerEnv": {"SMOKE": "live-42"},
  "forwardPorts": [3000],
  "postCreateCommand": "touch /tmp/pc-ran"
}`
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(repo, ".devcontainer", "devcontainer.json"), []byte(devc), 0o644))
	must(os.WriteFile(filepath.Join(repo, "file.txt"), []byte("from-the-repo\n"), 0o644))
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.email=live@test", "-c", "user.name=live", "commit", "-qm", "live"},
	} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	// Engine-side HTTP listener; its port becomes EnginePort so the bridge
	// + in-container relay expose it on the workspace's own loopback.
	engine, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	defer engine.Close()
	go func() {
		_ = http.Serve(engine, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "live-pong") }))
	}()
	_, portStr, _ := net.SplitHostPort(engine.Addr().String())
	enginePort, _ := strconv.Atoi(portStr)

	a := New(Config{RunsDir: t.TempDir(), ProvisionTimeout: 8 * time.Minute, EnginePort: enginePort})
	defer a.Close()

	id, err := a.Provision(ctx, ports.EnvSpec{RunID: "LIVETEST1", RepoURL: "file://" + repo})
	must(err)
	defer func() {
		if err := a.Destroy(context.WithoutCancel(ctx), id); err != nil {
			t.Errorf("destroy: %v", err)
		}
		if st, err := a.Status(context.WithoutCancel(ctx), id); err != nil || st != ports.EnvAbsent {
			t.Errorf("post-destroy status = %s, %v", st, err)
		}
	}()
	t.Logf("provisioned %s", id)

	if st, err := a.Status(ctx, id); err != nil || st != ports.EnvRunning {
		t.Fatalf("status = %s, %v", st, err)
	}

	// Exec: containerEnv, lifecycle hook, and the $HOME/repo contract.
	res, err := a.Exec(ctx, id, ports.Command{Script: "echo env=$SMOKE; ls /tmp/pc-ran; cat file.txt", Dir: "repo"})
	must(err)
	for _, want := range []string{"env=live-42", "/tmp/pc-ran", "from-the-repo"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("exec output missing %q: %q (exit %d)", want, res.Output, res.ExitCode)
		}
	}
	if res, err := a.Exec(ctx, id, ports.Command{Script: "exit 7"}); err != nil || res.ExitCode != 7 {
		t.Errorf("exit propagation: %d, %v", res.ExitCode, err)
	}

	// File round-trips: absolute (needs the root-init /apothecary dir) and
	// repo-relative (through the symlink).
	blob := []byte("live \x00\x01\xfe binary\n")
	must(a.WriteFile(ctx, id, "/apothecary/live.bin", blob, 0o600))
	got, err := a.ReadFile(ctx, id, "/apothecary/live.bin")
	must(err)
	if !bytes.Equal(got, blob) {
		t.Errorf("absolute round trip = %q", got)
	}
	got, err = a.ReadFile(ctx, id, "file.txt")
	must(err)
	if string(got) != "from-the-repo\n" {
		t.Errorf("relative read = %q", got)
	}

	// Loopback parity: the engine answers on the WORKSPACE's own
	// 127.0.0.1:<EnginePort> through relay.mjs -> unix socket -> bridge
	// (no curl in slim images: /dev/tcp).
	probe := fmt.Sprintf(
		`exec 3<>/dev/tcp/127.0.0.1/%d && printf 'GET / HTTP/1.0\r\n\r\n' >&3 && timeout 5 cat <&3`, enginePort)
	res, err = a.Exec(ctx, id, ports.Command{Script: probe})
	must(err)
	if !strings.Contains(res.Output, "live-pong") {
		t.Errorf("loopback engine probe = %q (exit %d)", res.Output, res.ExitCode)
	}

	// Links: terminal command + the declared forwardPort on the container IP.
	links, err := a.Links(ctx, id)
	must(err)
	var kinds []string
	appOK := false
	for _, l := range links {
		kinds = append(kinds, l.Kind)
		if l.Kind == "app" && strings.HasSuffix(l.URL, ":3000/") {
			appOK = true
		}
	}
	if !appOK {
		t.Errorf("no app link for declared forwardPort 3000: %v", links)
	}
	t.Logf("links: %v", kinds)
}
