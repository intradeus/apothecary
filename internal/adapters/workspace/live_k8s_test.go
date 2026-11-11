//go:build live

package workspace

// Live test of the Kubernetes host against a real cluster (k3d locally:
// task test:integration:k8s, or task k8s:cluster k8s:images k8s:test-live to
// keep the cluster). It needs:
//
//   - APOTHECARY_LIVE_KUBECONFIG: a kubeconfig for a THROWAWAY cluster. The
//     test creates and deletes its own namespace; it never uses kubectl's
//     default config.
//   - APOTHECARY_LIVE_IMAGE_PREFIX: where the sandbox, shim, tools and
//     egress-gw images of this tree were pushed (tag :local).
//   - APOTHECARY_LIVE_K8S_NAMESPACE (default apothecary-live).
//   - APOTHECARY_LIVE_GATEWAY=0 to skip the egress gateway (default on).
//   - APOTHECARY_LIVE_KEEP=1 to leave the namespace for inspection.
//   - APOTHECARY_LIVE_PULL_POLICY (default Always): IfNotPresent when the
//     images were imported into the cluster's nodes instead of pushed.
//
// The test runs OUTSIDE the cluster, like an engine on a laptop: it reaches
// pods through kubectl port-forward. Pods cannot reach a docker host's own
// addresses on hardened hosts, so the "engine" a workspace calls is a stub in
// the cluster (busybox httpd answering live-pong, labelled as the engine), and
// the repo is served by an in-cluster git daemon.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/kube"
	"apothecary/internal/flow/builtin"
	"apothecary/internal/platform/wstunnel"
	"apothecary/internal/ports/egressport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/workspace/portrelay"
)

// livePullPolicy is how the cluster gets this tree's images: pulled from
// the registry they were pushed to, or already on the nodes (imported).
func livePullPolicy() string {
	if p := os.Getenv("APOTHECARY_LIVE_PULL_POLICY"); p != "" {
		return p
	}
	return "Always"
}

func liveKube(t *testing.T) (*kube.Client, string) {
	t.Helper()
	kc := os.Getenv("APOTHECARY_LIVE_KUBECONFIG")
	if kc == "" {
		t.Skip("APOTHECARY_LIVE_KUBECONFIG unset: needs a throwaway cluster — see the header comment")
	}
	ns := os.Getenv("APOTHECARY_LIVE_K8S_NAMESPACE")
	if ns == "" {
		ns = "apothecary-live"
	}
	return &kube.Client{Kubeconfig: kc, Namespace: ns}, ns
}

func kubectl(t *testing.T, c *kube.Client, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("kubectl", append(c.Global(), args...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// liveFixtures are the in-cluster stand-ins: the engine stub and the repo.
const liveFixtures = `
apiVersion: v1
kind: Pod
metadata:
  name: engine-stub
  labels: {app.kubernetes.io/name: apothecaryd}
spec:
  containers:
    - name: httpd
      image: busybox:1.37
      command: [sh, -c, "mkdir -p /www && echo live-pong > /www/index.html && exec httpd -f -p 8080 -h /www"]
      ports: [{containerPort: 8080}]
---
apiVersion: v1
kind: Service
metadata: {name: engine-stub}
spec:
  selector: {app.kubernetes.io/name: apothecaryd}
  ports: [{port: 8080, targetPort: 8080}]
---
apiVersion: v1
kind: Pod
metadata:
  name: git
  labels: {app: live-git}
spec:
  containers:
    - name: git
      image: SANDBOX_IMAGE
      command: [bash, -c]
      args:
        - |
          set -e
          apk add --no-cache git-daemon >/dev/null
          mkdir -p /srv/repo/.devcontainer && cd /srv/repo
          cat > .devcontainer/devcontainer.json <<'J'
          {"image": "debian:bookworm-slim", "containerEnv": {"SMOKE": "live-42"},
           "forwardPorts": [3000], "postCreateCommand": "touch /tmp/pc-ran"}
          J
          echo from-the-repo > file.txt
          git init -q && git add -A && git -c user.email=l@t -c user.name=l commit -qm live
          git config daemon.uploadpack true
          exec git daemon --reuseaddr --export-all --base-path=/srv --listen=0.0.0.0 /srv
      ports: [{containerPort: 9418}]
      # Ready once the daemon listens, not when the container starts: it
      # installs git-daemon first.
      readinessProbe: {tcpSocket: {port: 9418}, periodSeconds: 2}
---
apiVersion: v1
kind: Service
metadata: {name: git}
spec:
  selector: {app: live-git}
  ports: [{port: 9418, targetPort: 9418}]
`

func TestLiveK8sLifecycle(t *testing.T) {
	c, ns := liveKube(t)
	prefix := os.Getenv("APOTHECARY_LIVE_IMAGE_PREFIX")
	if prefix == "" {
		t.Skip("APOTHECARY_LIVE_IMAGE_PREFIX unset: where this tree's images were pushed")
	}
	img := func(name string) string { return prefix + "/" + name + ":local" }
	gateway := os.Getenv("APOTHECARY_LIVE_GATEWAY") != "0"
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	_ = exec.Command("kubectl", "--kubeconfig", c.Kubeconfig, "delete", "namespace", ns, "--wait=true").Run()
	kubectl(t, &kube.Client{Kubeconfig: c.Kubeconfig}, "", "create", "namespace", ns)
	kubectl(t, &kube.Client{Kubeconfig: c.Kubeconfig}, "", "label", "namespace", ns, "pod-security.kubernetes.io/enforce=privileged")
	if os.Getenv("APOTHECARY_LIVE_KEEP") != "1" {
		defer exec.Command("kubectl", "--kubeconfig", c.Kubeconfig, "delete", "namespace", ns, "--wait=false").Run()
	}
	kubectl(t, c, strings.ReplaceAll(liveFixtures, "SANDBOX_IMAGE", img("sandbox")), "apply", "-f", "-")
	kubectl(t, c, "", "wait", "--for=condition=Ready", "pod/engine-stub", "pod/git", "--timeout=180s")
	engineIP := kubectl(t, c, "", "get", "svc", "engine-stub", "-o", "jsonpath={.spec.clusterIP}")

	// The run folder's path is the same string on the engine's disk (logs,
	// markers) and in the pod (the volume's mount point), and must not be
	// under /tmp, which the pod's dind masks.
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	runsDir := cache + "/apothecary-live-k8s"
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	a := New(Config{
		RunsDir: runsDir, ProvisionTimeout: 20 * time.Minute,
		Kube: &KubeConfig{
			Kube:         c,
			SandboxImage: img("sandbox"), ShimImage: img("shim"), ToolsImage: img("tools"), GatewayImage: img("egress-gw"),
			PullPolicy:      livePullPolicy(),
			EngineURL:       "http://" + engineIP + ":8080",
			EngineNamespace: ns,
			Forward:         true,
			Storage:         "pvc",
			Privileged:      true,
			Firewall:        gateway,
		},
	})
	defer a.Close()

	name := "apo-live-" + fmt.Sprint(time.Now().Unix()%100000)
	spec := envport.EnvSpec{
		RunID: "01LIVE" + name, Name: name, Project: "LIVEPROJ", ProjectName: "live",
		RepoURL: "git://git." + ns + ".svc.cluster.local/repo",
		// Docker Hub (the image; the harnesses and code-server ride in as the
		// tools bundle, nothing else is fetched into the image) — plus the raw
		// git port, the escape hatch's job.
		EgressDomains:      builtin.EgressDefaults(),
		EgressAllowedPorts: []int{9418},
	}
	start := time.Now()
	env, err := a.Provision(ctx, spec)
	if err != nil {
		if b, _ := a.TailExecLog(ctx, envport.EnvID(name), 8<<10); len(b) > 0 {
			t.Logf("exec.log tail:\n%s", b)
		}
		t.Fatalf("provision: %v", err)
	}
	t.Logf("provisioned %s in %s", env, time.Since(start).Truncate(time.Second))
	defer func() {
		if os.Getenv("APOTHECARY_LIVE_KEEP") == "1" {
			return
		}
		dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer dcancel()
		if err := a.Destroy(dctx, env); err != nil {
			t.Errorf("destroy: %v", err)
		}
		var pod kube.Pod
		if found, _ := c.Get(dctx, "pod", name, &pod); found {
			t.Error("pod survived Destroy")
		}
	}()

	if st, err := a.Status(ctx, env); err != nil || st != envport.EnvRunning {
		t.Fatalf("status = %s %v", st, err)
	}
	res, err := a.Exec(ctx, env, envport.Command{Script: `echo "$SMOKE"; cat "$HOME/repo/file.txt"; test -f /tmp/pc-ran && echo hooks-ran`})
	if err != nil || res.ExitCode != 0 || !strings.Contains(res.Output, "live-42") ||
		!strings.Contains(res.Output, "from-the-repo") || !strings.Contains(res.Output, "hooks-ran") {
		t.Fatalf("exec = %+v %v", res, err)
	}
	if res, err := a.Exec(ctx, env, envport.Command{Script: "exit 7"}); err != nil || res.ExitCode != 7 {
		t.Fatalf("exit code = %+v %v", res, err)
	}
	blob := []byte("round-trip \x00\x01\xfe binary\n")
	if err := a.WriteFile(ctx, env, "/apothecary/rt.bin", blob, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := a.ReadFile(ctx, env, "/apothecary/rt.bin"); err != nil || string(got) != string(blob) {
		t.Fatalf("read back %q %v", got, err)
	}
	if got := a.AppPorts(ctx, env); len(got) != 1 || got[0] != 3000 {
		t.Errorf("AppPorts = %v", got)
	}
	// The tools bundle, seeded onto the run volume and linked into
	// /opt/apothecary; the browser editor, started through Exec with the
	// project's extension installed through the gateway's gallery.
	if res, err := a.Exec(ctx, env, envport.Command{Script: `command -v opencode codex-acp omp code-server && opencode --version && codex --version`}); err != nil || res.ExitCode != 0 ||
		!strings.Contains(res.Output, "/opt/apothecary/opencode") || !strings.Contains(res.Output, "/opt/apothecary/code-server") {
		t.Fatalf("tools bundle: %+v %v", res, err)
	} else {
		t.Logf("tools: %s", strings.TrimSpace(res.Output))
	}
	if err := a.StartEditor(ctx, env, envport.CodeServerPort, envport.EditorSettings{Extensions: []string{"golang.go"}}); err != nil {
		t.Fatalf("editor: %v", err)
	}
	if res, err := a.Exec(ctx, env, envport.Command{Script: `cat /apothecary/code-server/extensions.installed; ls /apothecary/code-server/extensions`}); err != nil || !strings.Contains(res.Output, "golang.go") {
		t.Errorf("editor extensions: %+v %v", res, err)
	}
	// The workspace reaches the engine at its Service.
	if res, err := a.Exec(ctx, env, envport.Command{Script: fmt.Sprintf(`exec 3<>/dev/tcp/%s/8080; printf 'GET / HTTP/1.0\r\n\r\n' >&3; cat <&3`, engineIP)}); err != nil || !strings.Contains(res.Output, "live-pong") {
		t.Fatalf("engine from the workspace: %+v %v", res, err)
	}

	// The sidecar, dialed in through the pod's mux (port-forwarded).
	sc, err := a.Sidecar(ctx, env)
	if err != nil {
		t.Fatalf("sidecar: %v", err)
	}
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return sc.Dial(ctx) }}}
	if r, err := hc.Get("http://sidecar/health"); err != nil || r.StatusCode != 200 {
		t.Fatalf("sidecar health: %v %v", r, err)
	} else {
		r.Body.Close()
	}

	// An exposed port: the "app" on 3000 is a relay to the sidecar's socket.
	if res, err := a.Exec(ctx, env, envport.Command{Script: "(setsid /opt/apothecary/shim relay 3000:/opt/apothecary/sidecar.sock >/dev/null 2>&1 &) ; sleep 1"}); err != nil || res.ExitCode != 0 {
		t.Fatalf("start app: %+v %v", res, err)
	}
	pc, err := a.ExposePort(ctx, env, 3000)
	if err != nil {
		t.Fatalf("expose: %v", err)
	}
	relay := portrelay.New("127.0.0.1")
	defer relay.CloseAll()
	url, err := relay.Expose(string(env), 3000, pc)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := http.Get(url + "health"); err != nil || r.StatusCode != 200 {
		t.Fatalf("exposed port: %v %v", r, err)
	} else {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		t.Logf("exposed %s → %s", url, strings.TrimSpace(string(b)))
	}

	// The CLI's tunnels: a workspace port, and the workspace's SSH server, each
	// carried inside one WebSocket exactly as the engine serves them
	// (httpapi servePortTunnel / serveSSHTunnel), from a client outside.
	tunnel := func(target envport.PortConn) net.Conn {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			up, err := target.Dial(r.Context())
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			ws, err := wstunnel.Accept(w, r)
			if err != nil {
				up.Close()
				return
			}
			wstunnel.Pipe(ws, up)
		}))
		t.Cleanup(srv.Close)
		conn, err := wstunnel.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		if err != nil {
			t.Fatalf("tunnel: %v", err)
		}
		return conn
	}
	tc := tunnel(pc)
	_ = tc.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := tc.Write([]byte("GET /health HTTP/1.0\r\nHost: app\r\n\r\n")); err != nil {
		t.Fatalf("through the tunnel: %v", err)
	}
	if b, _ := io.ReadAll(tc); !strings.Contains(string(b), " 200 ") {
		t.Fatalf("port through the tunnel answered %q", b)
	}
	tc.Close()
	sshc, err := a.SSH(ctx, env)
	if err != nil {
		t.Fatalf("ssh: %v", err)
	}
	sc2 := tunnel(sshc)
	_ = sc2.SetDeadline(time.Now().Add(30 * time.Second))
	banner := make([]byte, 8)
	if _, err := io.ReadFull(sc2, banner); err != nil || string(banner) != "SSH-2.0-" {
		t.Fatalf("ssh through the tunnel: %q %v", banner, err)
	}
	sc2.Close()
	t.Log("tunnels: a workspace port and its SSH server answer through a WebSocket")

	if s1, err := a.SampleResources(ctx, env); err == nil {
		t.Logf("first sample unexpectedly complete: %+v", s1)
	}
	time.Sleep(2 * time.Second)
	if s, err := a.SampleResources(ctx, env); err != nil || s.MemUsed == 0 {
		t.Errorf("sample = %+v %v", s, err)
	} else {
		t.Logf("sample: cpu %.1f%% of %d, mem %d, pids %d", s.CPUPerc, s.CPUs, s.MemUsed, s.PIDs)
	}

	if gateway {
		h := a.host.(*k8sHost)
		curl := func(target string) (int, string) {
			_, stderr, code, err := h.runInner(ctx, name, "curl", []string{"-sS", "-m", "10", "-o", "/dev/null", "-w", "%{http_code}", target}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			return code, string(stderr)
		}
		// Not allowed: refused at the gateway, and the denial names the run.
		if code, _ := curl("https://www.wikipedia.org/"); code == 0 {
			t.Fatal("www.wikipedia.org reachable before it was allowed")
		}
		deadline := time.Now().Add(15 * time.Second)
		var denied bool
		for !denied && time.Now().Before(deadline) {
			for _, d := range a.EgressDenials(ctx, env) {
				denied = denied || d.Host == "www.wikipedia.org"
			}
			time.Sleep(time.Second)
		}
		if !denied {
			t.Fatalf("no denial recorded for www.wikipedia.org: %+v", a.EgressDenials(ctx, env))
		}
		if err := a.AllowEgress(ctx, env, "www.wikipedia.org"); err != nil {
			t.Fatal(err)
		}
		if code, stderr := curl("https://www.wikipedia.org/"); code != 0 {
			t.Fatalf("allowed host still refused: %d %s", code, stderr)
		}
		// A route around the tunnel is closed by the network policy.
		if _, _, code, _ := h.runInner(ctx, name, "sh", []string{"-c", "ip route replace 1.1.1.1/32 via $(cat /run/apo/orig-gw) dev eth0 && curl -sS -m 5 -o /dev/null https://1.1.1.1/"}, nil, nil); code == 0 {
			t.Error("bypassing the tunnel reached the internet: the network policy is not enforced")
		}
		var audit []egressport.EgressAuditRecord
		for i := 0; i < 10 && len(audit) == 0; i++ {
			time.Sleep(time.Second)
			audit = a.EgressAudit(ctx, env)
		}
		b, _ := json.Marshal(audit)
		t.Logf("audit: %s", b)
	}

	// An engine restart: a fresh adapter reattaches the running workspace.
	a.Close()
	if err := a.Reattach(ctx, env); err != nil {
		t.Fatalf("reattach: %v", err)
	}
	if res, err := a.Exec(ctx, env, envport.Command{Script: "echo still-here"}); err != nil || !strings.Contains(res.Output, "still-here") {
		t.Fatalf("exec after reattach: %+v %v", res, err)
	}
}
