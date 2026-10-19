//go:build live

package devcontainer

// Live integration test against the real local toolchain. Excluded from
// normal runs. It drives the REAL provisioner path — a DinD sandbox on the
// host daemon, `devcontainer up` against the inner daemon, the shim bridge —
// so it inherits the provisioner's own requirements:
//
//   - root: the inner daemon's socket surfaces on the host as root:docker
//     (the dind image's gid) 0660, exactly as apothecaryd sees it (root in
//     deploy/). Run it as root, or inside the engine image (recipe below),
//     which also brings the pinned @devcontainers/cli.
//   - APOTHECARY_LIVE_RUNS_DIR: a RunsDir NOT under /tmp (dind masks /tmp —
//     the guard in sandbox.go), bind-mounted at the SAME path when the test
//     runs in a container (Invariant A).
//   - APOTHECARY_SANDBOX_RUNTIME (optional, e.g. sysbox-runc): the test then
//     asserts the sandbox came up unprivileged under that runtime.
//   - APOTHECARY_LIVE_GATEWAY=1 (optional): also stand up the per-project
//     egress gateway (--internal network, DNS + pull-through mirror via the
//     gateway) instead of the default bridge. Torn down at the end.
//
// From the repo root, with the local engine image built
// (docker compose -p apothecary-local -f deploy/docker-compose.local.yaml build):
//
//	go test -c -tags live -o /tmp/live.test ./internal/adapters/devcontainer/
//	R=$HOME/.cache/apothecary-live; mkdir -p $R
//	docker run --rm --entrypoint /live.test \
//	  -v /var/run/docker.sock:/var/run/docker.sock -v $R:$R \
//	  -v /tmp/live.test:/live.test:ro \
//	  -e APOTHECARY_LIVE_RUNS_DIR=$R -e APOTHECARY_SANDBOX_RUNTIME=sysbox-runc \
//	  apothecary/apothecaryd:local -test.v -test.run TestLiveLifecycle

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"apothecary/internal/portrelay"
	"apothecary/internal/ports"
	"apothecary/internal/wsimage"
)

func TestLiveLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// A tiny "target repo" WITH its own devcontainer.
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Deliberately node-less image: the static shim binary must carry the
	// whole engine bridge on its own.
	devc := `{
  "image": "debian:bookworm-slim",
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

	// RunsDir: a DinD sandbox parity-mounts it, so it cannot be t.TempDir()
	// (dind masks /tmp) and, in a container, must sit at the same host path.
	runsDir := os.Getenv("APOTHECARY_LIVE_RUNS_DIR")
	if runsDir == "" {
		t.Skip("APOTHECARY_LIVE_RUNS_DIR unset: needs a RunsDir outside /tmp — see the header comment")
	}
	if os.Geteuid() != 0 {
		t.Skip("must run as root: the inner daemon's socket is root:docker 0660 on the host, as apothecaryd sees it")
	}
	runsDir, err = filepath.Abs(runsDir)
	must(err)
	must(os.MkdirAll(runsDir, 0o755))

	const project = "livetest"
	sandboxRuntime := os.Getenv("APOTHECARY_SANDBOX_RUNTIME")
	// The real thing: docker-built static shim (golang image pull on first
	// ever run; content-addressed cache after) and, opted in, the real
	// per-project gateway image.
	builder := wsimage.New(runsDir)
	cfg := Config{
		RunsDir: runsDir, ProvisionTimeout: 8 * time.Minute, EnginePort: enginePort,
		ShimBin:        builder.EnsureShim,
		SandboxRuntime: sandboxRuntime,
	}
	if os.Getenv("APOTHECARY_LIVE_GATEWAY") == "1" {
		cfg.GatewayImage = builder.EnsureGatewayImage
		// Nothing reaps a project's gateway (egressgw/manager.go); this test
		// owns its project, so it removes the gateway + caches it created.
		defer func() {
			for _, args := range [][]string{
				{"rm", "-f", "apo-gw-" + project},
				{"volume", "rm", "-f", "apo-gw-" + project + "-cache", "apo-gw-" + project + "-buildcache"},
			} {
				_ = exec.Command("docker", args...).Run()
			}
		}()
	}
	a := New(cfg)
	defer a.Close()

	id, err := a.Provision(ctx, ports.EnvSpec{RunID: "LIVETEST1", RepoURL: "file://" + repo, Project: project})
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

	// Sandbox posture on the HOST daemon (the sandbox shares the env's name):
	// privileged under the default runtime, or unprivileged under the opted-in
	// user-namespace runtime — never both, never neither.
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		"{{.HostConfig.Privileged}} {{.HostConfig.Runtime}}", string(id)).Output()
	must(err)
	posture := strings.TrimSpace(string(out))
	t.Logf("sandbox posture (privileged runtime): %s", posture)
	switch {
	case sandboxRuntime != "" && posture != "false "+sandboxRuntime:
		t.Errorf("sandbox posture = %q, want unprivileged under %s", posture, sandboxRuntime)
	case sandboxRuntime == "" && !strings.HasPrefix(posture, "true "):
		t.Errorf("sandbox posture = %q, want privileged under the default runtime", posture)
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

	// Links: terminal command (app links are the port relay's business now).
	links, err := a.Links(ctx, id)
	must(err)
	var kinds []string
	for _, l := range links {
		kinds = append(kinds, l.Kind)
	}
	t.Logf("links: %v", kinds)

	// Declared ports from the repo's devcontainer.
	if ap := a.AppPorts(ctx, id); len(ap) != 1 || ap[0] != 3000 {
		t.Errorf("AppPorts = %v, want [3000]", ap)
	}

	// Port exposure, full chain and zero extra runtimes: the "app" on
	// :3000 is the shim's own forward relay looped back to the engine
	// socket, so a browser GET traverses
	//   portrelay -> app-3000.sock -> reverse relay -> 127.0.0.1:3000
	//   -> forward relay -> engine.sock -> the live-pong server.
	cid, err := a.containerID(ctx, string(id))
	must(err)
	relay := exec.CommandContext(ctx, "docker", "exec", "-d", cid,
		"/opt/apothecary/shim", "relay", "3000:/opt/apothecary/engine.sock")
	relay.Env = append(os.Environ(), a.dockerEnv(string(id))...) // the workspace lives on the INNER daemon
	must(relay.Run())
	pc, err := a.ExposePort(ctx, id, 3000)
	must(err)
	mgr := portrelay.New("127.0.0.1")
	defer mgr.CloseAll()
	exposedURL, err := mgr.Expose("live-run", 3000, pc)
	must(err)
	resp, err := http.Get(exposedURL)
	must(err)
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(page) != "live-pong" {
		t.Errorf("exposed app GET = %q via %s", page, exposedURL)
	}
	t.Logf("exposed :3000 at %s", exposedURL)
}
