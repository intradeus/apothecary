//go:build live

package workspace

// Live test of the Kubernetes host against a real cluster (k3d locally:
// task sandbox:test:k8s, or task k8s:cluster k8s:images k8s:test-live to
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
//   - APOTHECARY_LIVE_WG_USERSPACE=1 to run each sandbox's tunnel as
//     wireguard-go (the Kata guest's path) on a kernel with the module.
//   - APOTHECARY_LIVE_RUNTIME (default privileged): the sandboxes' runtime.
//     kata needs a cluster with Kata (task sandbox:test:kata, deploy/kata);
//     its sandboxes are sized, on emptyDir disks, and tunnel through
//     wireguard-go (the stock guest kernel has no WireGuard).
//   - APOTHECARY_LIVE_VM_STORAGE_CLASS: a class of raw block volumes for a
//     kata sandbox's claim (the rig's apo-block); then its lifecycle sleeps
//     and wakes like the others'. Unset, a kata sandbox is on disks of its
//     own and the test checks that it refuses to sleep.
//   - APOTHECARY_LIVE_NESTED_K8S=<k3s image>: also start a Kubernetes node
//     inside the sandbox and a user-namespaced pod on it (liveNestedKubernetes),
//     what this repository's own sandbox suites need. Only for a runtime that
//     gives both (privileged, kata); task sandbox:test:kata sets it, and a
//     kata run without it skips through livetest.
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
	"apothecary/internal/platform/livetest"
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
		livetest.Skip(t, "APOTHECARY_LIVE_KUBECONFIG unset: needs a throwaway cluster — see the header comment")
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
		livetest.Skip(t, "APOTHECARY_LIVE_IMAGE_PREFIX unset: where this tree's images were pushed")
	}
	img := func(name string) string { return prefix + "/" + name + ":local" }
	gateway := os.Getenv("APOTHECARY_LIVE_GATEWAY") != "0"
	runtime := envport.RuntimePrivileged
	if v := os.Getenv("APOTHECARY_LIVE_RUNTIME"); v != "" {
		var err error
		if runtime, err = envport.ParseRuntime(v); err != nil {
			t.Fatal(err)
		}
	}
	vm := runtime == envport.RuntimeKata
	if _, err := os.Stat("/dev/kvm"); vm && err != nil {
		livetest.Skip(t, "APOTHECARY_LIVE_RUNTIME=kata without /dev/kvm here: the Kata rig runs on this host's KVM")
	}
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
			VMStorageClass:  os.Getenv("APOTHECARY_LIVE_VM_STORAGE_CLASS"),
			Runtime:         runtime,
			Firewall:        gateway,
			// The tunnel either way: kernel WireGuard by default, userspace
			// when asked.
			WireGuardUserspace: os.Getenv("APOTHECARY_LIVE_WG_USERSPACE") == "1",
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
	if vm {
		// The pod's limits size the VM.
		spec.Size = envport.SandboxSize{MilliCPU: 4000, MemoryMiB: 8192}
	}
	start := time.Now()
	env, err := a.Provision(ctx, spec)
	if err != nil {
		if c, _ := a.ReadExecLog(ctx, envport.EnvID(name), -1, 8<<10); len(c.Data) > 0 {
			t.Logf("exec.log tail:\n%s", c.Data)
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
	if gateway {
		// The tunnel is the WireGuard asked for: the wg container records it
		// on the pod's shared /run/apo.
		want := "kernel"
		if os.Getenv("APOTHECARY_LIVE_WG_USERSPACE") == "1" || vm {
			want = "userspace"
		}
		if mode := kubectl(t, c, "", "exec", name, "-c", "sandbox", "--", "cat", "/run/apo/wg-mode"); mode != want {
			t.Errorf("tunnel = %q WireGuard, want %q", mode, want)
		}
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
	if res, err := a.Exec(ctx, env, envport.Command{Script: `command -v opencode codex-acp omp claude-agent-acp claude code-server && opencode --version && codex --version`}); err != nil || res.ExitCode != 0 ||
		!strings.Contains(res.Output, "/opt/apothecary/opencode") || !strings.Contains(res.Output, "/opt/apothecary/claude-agent-acp") ||
		!strings.Contains(res.Output, "/opt/apothecary/code-server") {
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
		liveNoCrossRun(ctx, t, a, h, name, spec)
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
	// Nested Kubernetes, before a sleep replaces the pod.
	nestedImage := os.Getenv("APOTHECARY_LIVE_NESTED_K8S")
	if caps := runtime.Capabilities(); nestedImage != "" && caps.NestedKubernetes && caps.UserNamespaces {
		liveNestedKubernetes(ctx, t, a.host.(*k8sHost), name, nestedImage)
	}
	// Stop-on-idle: a sandbox on a claim (a VM's raw-block one included)
	// sleeps and wakes; a VM's on disks of its own is refused, by design, and
	// that refusal is what is checked — never a quiet pass over the part
	// that did not run.
	if err := a.CanSleep(runtime); vm && os.Getenv("APOTHECARY_LIVE_VM_STORAGE_CLASS") == "" {
		if err == nil {
			t.Error("a sandbox on emptyDir storage says it can sleep: its checkout would go with the pod")
		} else {
			t.Logf("sleep and wake: not part of a %s sandbox's lifecycle without a VM storage class (%v)", runtime, err)
		}
	} else if err != nil {
		t.Errorf("a sandbox on a claim cannot sleep: %v", err)
	} else {
		liveSleepWake(ctx, t, a, c, env, spec)
	}
	// Last, so a skip cuts nothing else short: under kata the nested check
	// is the point of the suite (what the runtime is chosen for).
	if vm && nestedImage == "" {
		livetest.Skip(t, "APOTHECARY_LIVE_NESTED_K8S unset: a kata sandbox's nested Kubernetes went unchecked (task sandbox:test:kata sets it)")
	}
}

// liveNestedKubernetes is what this repository's own sandbox suites ask of
// a sandbox (task sandbox:test:k8s inside it): a Kubernetes node on the
// sandbox's docker daemon, and on it a pod with a user namespace of its own
// (hostUsers: false, the assistant pod's shape), mapped wherever the nested
// kubelet picks in the 32-bit id space. Under sysbox that pod cannot start
// (the sandbox owns 65536 ids); a runtime that claims UserNamespaces must
// run it. The node's images come through the run's gateway (Docker Hub is
// among the egress defaults).
func liveNestedKubernetes(ctx context.Context, t *testing.T, h *k8sHost, name, image string) {
	t.Helper()
	const script = `set -eu
docker rm -f apo-nested >/dev/null 2>&1 || true
docker run -d --name apo-nested --privileged --tmpfs /run --tmpfs /var/run "$1" \
  server --disable=traefik,metrics-server,servicelb,local-storage,coredns >/dev/null
k() { docker exec -i apo-nested kubectl "$@"; }
i=0
until k get nodes 2>/dev/null | grep -q ' Ready'; do
  i=$((i + 1)); [ "$i" -lt 150 ] || { echo "the nested node is not Ready"; docker logs --tail 40 apo-nested; exit 1; }
  sleep 2
done
echo "nested node Ready after $((i * 2))s"
i=0
until k apply -f - >/dev/null 2>&1 <<'EOF'
apiVersion: v1
kind: Pod
metadata: {name: userns, namespace: default}
spec:
  hostUsers: false
  restartPolicy: Never
  containers:
    - name: c
      image: busybox:1.37
      command: [cat, /proc/self/uid_map]
EOF
do
  i=$((i + 1)); [ "$i" -lt 60 ] || { echo "the pod was never accepted"; exit 1; }
  sleep 2
done
k wait --for=jsonpath='{.status.phase}'=Succeeded pod/userns --timeout=300s >/dev/null ||
  { k describe pod userns | tail -n 25; exit 1; }
echo "uid_map: $(k logs userns)"
docker rm -f apo-nested >/dev/null
`
	start := time.Now()
	out, stderr, code, err := h.runInner(ctx, name, "sh", []string{"-c", script, "sh", image}, nil, nil)
	if err != nil || code != 0 {
		t.Fatalf("nested Kubernetes in the sandbox (exit %d, %v):\n%s%s", code, err, out, stderr)
	}
	// The pod's first mapping: inside id 0 at an outside id the nested
	// kubelet chose, never the sandbox's own 0.
	var inside, outside, count uint64
	line := strings.TrimSpace(string(out)[strings.LastIndex(string(out), "uid_map:")+len("uid_map:"):])
	if _, err := fmt.Sscan(line, &inside, &outside, &count); err != nil || inside != 0 || outside == 0 {
		t.Fatalf("the nested pod is not user-namespaced: %q (%v)", line, err)
	}
	t.Logf("nested Kubernetes: %s; the pod mapped 0 at %d (%d ids), %s in all",
		strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0], outside, count, time.Since(start).Truncate(time.Second))
}

// liveSleepWake: the run sleeps (its pod deleted, its claim kept) and wakes
// by provisioning its spec again, with the same workspace container (no
// rebuild), the checkout as it was, and the project's own containers — one
// started beside the workspace on the sandbox's docker, with a restart
// policy, as a project's services are — running again.
func liveSleepWake(ctx context.Context, t *testing.T, a *Adapter, c *kube.Client, env envport.EnvID, spec envport.EnvSpec) {
	t.Helper()
	name := string(env)
	h := a.host.(*k8sHost)
	inner := func(script string) string {
		t.Helper()
		out, stderr, code, err := h.runInner(ctx, name, "sh", []string{"-c", script}, nil, nil)
		if err != nil || code != 0 {
			t.Fatalf("%s: exit %d %v %s", script, code, err, stderr)
		}
		return strings.TrimSpace(string(out))
	}
	if _, err := a.Exec(ctx, env, envport.Command{Script: "echo kept > .apo-live-sleep"}); err != nil {
		t.Fatal(err)
	}
	before, err := a.containerID(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	// The workspace's own image is on the sandbox's docker already: nothing
	// is pulled for the project container.
	image := inner("docker inspect -f '{{.Config.Image}}' " + before)
	service := inner("docker run -d --restart unless-stopped --name apo-live-service " + image + " sleep 2147483647")
	if err := a.Sleep(ctx, env); err != nil {
		t.Fatalf("sleep: %v", err)
	}
	var pod kube.Pod
	if found, _ := c.Get(ctx, "pod", name, &pod); found {
		t.Fatal("the pod survived Sleep")
	}
	if st, err := a.Status(ctx, env); err != nil || st != envport.EnvAsleep {
		t.Fatalf("asleep: status = %s %v", st, err)
	}
	start := time.Now()
	if _, err := a.Provision(ctx, spec); err != nil {
		t.Fatalf("wake: %v", err)
	}
	t.Logf("woke in %s", time.Since(start).Truncate(time.Second))
	if st, err := a.Status(ctx, env); err != nil || st != envport.EnvRunning {
		t.Fatalf("awake: status = %s %v", st, err)
	}
	if after, err := a.containerID(ctx, name); err != nil || after != before {
		t.Errorf("workspace container %s before sleep, %s after (%v): rebuilt", before, after, err)
	}
	if res, err := a.Exec(ctx, env, envport.Command{Script: "cat .apo-live-sleep"}); err != nil || !strings.Contains(res.Output, "kept") {
		t.Errorf("checkout after waking: %+v %v", res, err)
	}
	var state string
	for i := 0; i < 30; i++ {
		if state = inner("docker inspect -f '{{.Id}} {{.State.Running}}' apo-live-service"); state == service+" true" {
			break
		}
		time.Sleep(time.Second)
	}
	if state != service+" true" {
		t.Errorf("the project's container after waking: %q, want %s running", state, service)
	}
}

// liveNoCrossRun: a second run of the same project is a peer on the same
// gateway's wg0, listening on :443 and on the project's raw-port hatch
// (9418). The first run reaches neither — not through the forward chain on
// the allowed port, not by naming an allowlisted host to the filter — while
// the second answers on its own tunnel address.
func liveNoCrossRun(ctx context.Context, t *testing.T, a *Adapter, h *k8sHost, name string, spec envport.EnvSpec) {
	t.Helper()
	peer := name + "-b"
	spec.RunID, spec.Name = "01LIVE"+peer, peer
	if !spec.Size.IsZero() {
		// The peer only listens and answers: a VM of the first run's size
		// beside it would not fit the runner (two 8 GiB Kata VMs and their
		// overhead are more than a 16 GB CI runner's node holds).
		spec.Size = envport.SandboxSize{MilliCPU: 1000, MemoryMiB: 2048}
	}
	env, err := a.Provision(ctx, spec)
	if err != nil {
		t.Fatalf("provision the second run: %v", err)
	}
	defer func() {
		if os.Getenv("APOTHECARY_LIVE_KEEP") == "1" {
			return
		}
		dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer dcancel()
		if err := a.Destroy(dctx, env); err != nil {
			t.Errorf("destroy the second run: %v", err)
		}
	}()
	sh := func(run, script string) (string, int) {
		out, stderr, code, err := h.runInner(ctx, run, "sh", []string{"-c", script}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out) + string(stderr)), code
	}
	ip, code := sh(peer, `ip -4 -o addr show dev wg0 | awk '{split($4, a, "/"); print a[1]}'`)
	if code != 0 || !strings.HasPrefix(ip, "10.90.") {
		t.Fatalf("the second run's tunnel address = %q (exit %d)", ip, code)
	}
	// Each accepted connection appends to the peer's hit log.
	for _, port := range []string{"443", "9418"} {
		if out, code := sh(peer, `(setsid sh -c 'while :; do nc -l -p `+port+` </dev/null >/dev/null && echo `+port+` >> /var/tmp/peer-hits; done' >/dev/null 2>&1 &); sleep 1`); code != 0 {
			t.Fatalf("listen on %s: %s", port, out)
		}
	}
	if out, code := sh(peer, `nc -w 3 `+ip+` 9418 </dev/null; sleep 1; cat /var/tmp/peer-hits && rm /var/tmp/peer-hits`); code != 0 || !strings.Contains(out, "9418") {
		t.Fatalf("the second run's listener does not answer on its own address: %q (exit %d)", out, code)
	}

	// github.com is on the project's allowlist and 9418 is its raw-port hatch.
	sh(name, `curl -sS -m 5 -k --resolve github.com:443:`+ip+` -o /dev/null https://github.com/; nc -w 3 `+ip+` 9418 </dev/null`)
	if out, _ := sh(peer, `sleep 2; cat /var/tmp/peer-hits 2>/dev/null`); out != "" {
		t.Fatalf("the first run reached the second through the gateway (ports hit: %q)", out)
	}
	t.Log("cross-run: a second run of the project is unreachable from the first through their gateway")
}
