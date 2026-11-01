package workspace

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/egressgw"
	"apothecary/internal/adapters/kube"
	"apothecary/internal/ports/envport"
	wsassets "apothecary/workspace"
)

// kubeRule answers kubectl calls containing match, ahead of the fake's state.
type kubeRule struct {
	match, out, stderr string
	code               int
	err                error
}

type kubeRules struct {
	*kubeFake
	rules []kubeRule
}

func (k *kubeRules) Run(ctx context.Context, bin string, args []string, stdin []byte, tee io.Writer, env ...string) ([]byte, []byte, int, error) {
	line := strings.Join(args, " ")
	for _, r := range k.rules {
		if strings.Contains(line, r.match) {
			return []byte(r.out), []byte(r.stderr), r.code, r.err
		}
	}
	return k.kubeFake.Run(ctx, bin, args, stdin, tee, env...)
}

// k8sWith is a test adapter on the fake cluster, its answers overridden.
func k8sWith(t *testing.T, k *kubeFake, firewall bool, rules ...kubeRule) (*Adapter, *k8sHost) {
	t.Helper()
	a := newK8sTestAdapter(t, k, firewall)
	h := a.host.(*k8sHost)
	h.cfg.Kube.R = &kubeRules{kubeFake: k, rules: rules}
	return a, h
}

// provisionPod is the happy in-pod script, with no record kept.
func provisionPod() func([]string, []byte) (string, string, int) {
	var seen []string
	var stdins [][]byte
	return inPod(&seen, &stdins)
}

func relaySecret(tok string) string {
	return `{"data":{"relay-token":"` + base64.StdEncoding.EncodeToString([]byte(tok)) + `"}}`
}

func TestK8sDefaults(t *testing.T) {
	a := New(Config{RunsDir: t.TempDir(), Kube: &KubeConfig{Kube: &kube.Client{}, Firewall: true}})
	h := a.host.(*k8sHost)
	if h.cfg.Storage != "pvc" || h.cfg.StorageSize != "20Gi" || h.cfg.ClusterDomain != "cluster.local" {
		t.Errorf("defaults = %+v", h.cfg)
	}
	if a.gw == nil {
		t.Error("firewall on but no gateway manager")
	}
	off := New(Config{RunsDir: t.TempDir(), Kube: &KubeConfig{Kube: &kube.Client{}}})
	if off.gw != nil {
		t.Error("firewall off but a gateway manager")
	}
	if got := off.host.(*k8sHost).bridgePorts(); got != nil {
		t.Errorf("bridge ports = %v: the workspace reaches a Service, nothing is bridged", got)
	}
	if err := off.host.ensureBridge("w"); err != nil {
		t.Error(err)
	}
	if err := off.host.startBridgeRelay(context.Background(), "w", "c"); err != nil {
		t.Error(err)
	}
	off.host.closeBridge("w")
}

func TestK8sPrepareRefusesTmp(t *testing.T) {
	k := &kubeFake{}
	a, _ := k8sWith(t, k, false)
	a.noSandboxGuard = false
	a.cfg.RunsDir = "/tmp/runs"
	if _, err := a.Provision(context.Background(), envport.EnvSpec{Name: "w"}); err == nil || !strings.Contains(err.Error(), "under /tmp") {
		t.Fatalf("err = %v", err)
	}
	if len(k.calls) != 0 {
		t.Errorf("reached the cluster: %v", k.calls)
	}
}

func TestK8sPrepareFolderFailure(t *testing.T) {
	a, _ := k8sWith(t, &kubeFake{}, false)
	os.WriteFile(filepath.Join(a.cfg.RunsDir, "w"), nil, 0o644)
	if _, err := a.Provision(context.Background(), envport.EnvSpec{Name: "w"}); err == nil {
		t.Error("a run folder under a file succeeded")
	}
}

// Each stage of a pod provision that fails stops it there.
func TestK8sProvisionFailuresStopAtTheirStage(t *testing.T) {
	spec := envport.EnvSpec{Name: "w", RepoURL: "https://git.test/r.git"}
	cases := []struct {
		name  string
		rules []kubeRule
		exec  func([]string, []byte) (string, string, int)
		want  string
	}{
		{"token read", []kubeRule{{match: "get --request-timeout=30s secret", stderr: "Unable to connect", code: 1}}, nil, "Unable to connect"},
		{"token store", []kubeRule{{match: "apply", stderr: "Forbidden", code: 1}}, nil, "Forbidden"},
		{"claim", []kubeRule{{match: "create", stderr: "quota exceeded", code: 1}}, nil, "quota exceeded"},
		{"pod read", []kubeRule{{match: "get --request-timeout=30s pod", stderr: "boom", code: 1}}, nil, "boom"},
		{"ready", []kubeRule{{match: "wait --for=condition=Ready", stderr: "timed out", code: 1}}, nil, "never became ready"},
		{"clone exit", nil, func(argv []string, stdin []byte) (string, string, int) {
			if argv[0] == "test" {
				return "", "", 1
			}
			if strings.Contains(string(stdin), " clone -- ") {
				return "", "not found\n", 128
			}
			return "", "", 0
		}, "clone https://git.test/r.git: exit 128"},
		{"no definition", nil, func(argv []string, stdin []byte) (string, string, int) {
			if argv[0] == "bash" && strings.Contains(string(stdin), "[ -e") {
				return "", "", 1
			}
			return "", "", 0
		}, "no .devcontainer/devcontainer.json"},
		{"shim hash", nil, func(argv []string, stdin []byte) (string, string, int) {
			if argv[0] == "bash" && strings.Contains(string(stdin), "[ -e") {
				return "", "", 0
			}
			return base64.StdEncoding.EncodeToString([]byte("other\n")), "", 0
		}, "publish the shim image"},
		{"tools missing", nil, func(argv []string, stdin []byte) (string, string, int) {
			s := string(stdin)
			switch {
			case strings.Contains(s, "shim.hash"):
				return base64.StdEncoding.EncodeToString([]byte(wsassets.ShimSourceHash())), "", 0
			case strings.Contains(s, "TOOLS_HASH"):
				return "", "", 44
			}
			return "", "", 0
		}, "no seeded tools bundle"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := &kubeFake{secrets: map[string]string{}, exec: c.exec}
			if k.exec == nil {
				k.exec = provisionPod()
			}
			a, _ := k8sWith(t, k, false, c.rules...)
			_, err := a.Provision(context.Background(), spec)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// A bad engine URL never reaches the cluster with half a policy.
func TestK8sBadEngineURL(t *testing.T) {
	k := &kubeFake{secrets: map[string]string{}}
	a, h := k8sWith(t, k, false)
	h.cfg.EngineURL = "::not a url"
	if _, err := a.Provision(context.Background(), envport.EnvSpec{Name: "w"}); err == nil || !strings.Contains(err.Error(), "is not a URL") {
		t.Fatalf("err = %v", err)
	}
	if k.obj(k.created, "Pod") != nil {
		t.Error("a pod with no network policy")
	}
}

func TestK8sGatewayProvisionRecordsAndTunnels(t *testing.T) {
	k := &kubeFake{secrets: map[string]string{}, exec: provisionPod()}
	a, _ := k8sWith(t, k, false)
	g := newGwFake(t)
	a.gw = egressgw.NewManager(g)
	if _, err := a.Provision(context.Background(), envport.EnvSpec{Name: "w", Project: "P1", RepoURL: "https://git.test/r.git"}); err != nil {
		t.Fatal(err)
	}
	if a.projectFor("w") != "P1" || a.gatewayIPFor("w") != "10.77.0.2" {
		t.Errorf("markers = %q, %q", a.projectFor("w"), a.gatewayIPFor("w"))
	}
	b, _ := json.Marshal(k.obj(k.created, "Pod"))
	if !strings.Contains(string(b), `"name":"wg"`) || !strings.Contains(string(b), `"secretName":"apo-wg-w"`) {
		t.Errorf("pod = %s", b)
	}
	g.ensureErr = errors.New("gateway down")
	if _, err := a.Provision(context.Background(), envport.EnvSpec{Name: "w2", Project: "P1"}); err == nil {
		t.Error("a pod started outside the firewall")
	}
}

func TestK8sRelayToken(t *testing.T) {
	ctx := context.Background()
	k := &kubeFake{secrets: map[string]string{}}
	_, h := k8sWith(t, k, false)
	if _, err := h.relayToken(ctx, "w", false, ""); err == nil || !strings.Contains(err.Error(), "no relay token") {
		t.Errorf("no create = %v", err)
	}
	tok, err := h.relayToken(ctx, "w", true, "P1")
	if err != nil || len(tok) != 48 {
		t.Fatalf("token = %q, %v", tok, err)
	}
	sec := k.obj(k.applied, "Secret")
	if sec == nil || sec["metadata"].(map[string]any)["name"] != "apo-run-w" || sec["stringData"].(map[string]any)["relay-token"] != tok {
		t.Errorf("secret = %v", sec)
	}
	// Cached: no second read.
	n := len(k.calls)
	if again, err := h.relayToken(ctx, "w", false, ""); err != nil || again != tok || len(k.calls) != n {
		t.Errorf("cached = %q, %v, %d calls", again, err, len(k.calls)-n)
	}
	// An engine restart finds the one in the Secret.
	k.secrets["apo-run-w2"] = relaySecret("kept")
	if got, err := h.relayToken(ctx, "w2", true, ""); err != nil || got != "kept" {
		t.Errorf("existing = %q, %v", got, err)
	}
}

func TestK8sEndedPodIsReplacedAndALiveOneKept(t *testing.T) {
	ctx := context.Background()
	for phase, replaced := range map[string]bool{"Failed": true, "Succeeded": true, "Running": false, "Pending": false} {
		k := &kubeFake{pod: `{"status":{"phase":"` + phase + `"}}`, secrets: map[string]string{}}
		_, h := k8sWith(t, k, false)
		if err := h.ensurePod(ctx, "w", envport.EnvSpec{}, egressgw.Attachment{}); err != nil {
			t.Fatal(err)
		}
		if got := len(k.deleted) == 1 && k.deleted[0] == "pod/w"; got != replaced {
			t.Errorf("%s: deleted = %v", phase, k.deleted)
		}
		if got := k.obj(k.created, "Pod") != nil; got != replaced {
			t.Errorf("%s: created pod = %v", phase, got)
		}
	}
	k := &kubeFake{pod: `{"status":{"phase":"Failed"}}`, secrets: map[string]string{}}
	_, h := k8sWith(t, k, false, kubeRule{match: "delete pod", stderr: "boom", code: 1})
	if err := h.ensurePod(ctx, "w", envport.EnvSpec{}, egressgw.Attachment{}); err == nil {
		t.Error("a dead pod could not go, but the provision went on")
	}
}

func TestK8sStorageChoices(t *testing.T) {
	ctx := context.Background()
	k := &kubeFake{secrets: map[string]string{}}
	_, h := k8sWith(t, k, false)
	h.cfg.StorageClass, h.cfg.StorageSize = "fast", "5Gi"
	if err := h.ensurePod(ctx, "w", envport.EnvSpec{}, egressgw.Attachment{}); err != nil {
		t.Fatal(err)
	}
	pvc, _ := json.Marshal(k.obj(k.created, "PersistentVolumeClaim"))
	if !strings.Contains(string(pvc), `"storageClassName":"fast"`) || !strings.Contains(string(pvc), `"storage":"5Gi"`) {
		t.Errorf("pvc = %s", pvc)
	}

	k = &kubeFake{secrets: map[string]string{}}
	_, h = k8sWith(t, k, false)
	h.cfg.Storage = "emptydir"
	if err := h.ensurePod(ctx, "w", envport.EnvSpec{}, egressgw.Attachment{}); err != nil {
		t.Fatal(err)
	}
	if k.obj(k.created, "PersistentVolumeClaim") != nil {
		t.Error("a claim on emptydir storage")
	}
	pod, _ := json.Marshal(k.obj(k.created, "Pod"))
	if !strings.Contains(string(pod), `{"emptyDir":{},"name":"data"}`) && !strings.Contains(string(pod), `"name":"data","emptyDir":{}`) {
		t.Errorf("pod volumes = %s", pod)
	}
}

// A runtime class drops privilege, and with it the tunnel's privileged
// container to a bare capability.
func TestK8sUnprivilegedRuntime(t *testing.T) {
	k := &kubeFake{}
	_, h := k8sWith(t, k, false)
	h.cfg.RuntimeClass, h.cfg.Privileged = "sysbox-runc", false
	att := egressgw.Attachment{GatewayIP: "10.90.0.1", WGEndpoint: "10.43.9.9:51820", WGSecret: "s"}
	pod, err := h.podManifest(context.Background(), "w", envport.EnvSpec{ProjectName: "My App"}, att)
	if err != nil {
		t.Fatal(err)
	}
	if pod.Spec.RuntimeClassName != "sysbox-runc" || pod.Spec.Containers[0].SecurityContext != nil {
		t.Errorf("sandbox = %+v, class %q", pod.Spec.Containers[0].SecurityContext, pod.Spec.RuntimeClassName)
	}
	if pod.Metadata.Annotations["apothecary.io/project-name"] != "My App" {
		t.Errorf("annotations = %v", pod.Metadata.Annotations)
	}
	wg := pod.Spec.InitContainers[2]
	if wg.SecurityContext.Privileged != nil || wg.SecurityContext.Capabilities.Add[0] != "NET_ADMIN" {
		t.Errorf("wg = %+v", wg.SecurityContext)
	}
	// Asking for privilege keeps it even on a runtime class.
	h.cfg.Privileged = true
	pod, _ = h.podManifest(context.Background(), "w", envport.EnvSpec{}, att)
	if pod.Spec.Containers[0].SecurityContext == nil || !*pod.Spec.Containers[0].SecurityContext.Privileged {
		t.Errorf("sandbox = %+v", pod.Spec.Containers[0].SecurityContext)
	}
}

func TestK8sNetworkPolicyEngineScope(t *testing.T) {
	_, h := k8sWith(t, &kubeFake{}, false)
	h.cfg.EngineCIDR = "192.168.1.5/32"
	np, err := h.networkPolicy("w", "P1")
	if err != nil {
		t.Fatal(err)
	}
	from := np.Spec.Ingress[0].From[0]
	if from.IPBlock == nil || from.IPBlock.CIDR != "192.168.1.5/32" || from.PodSelector != nil {
		t.Errorf("an out-of-cluster engine is an address: %+v", from)
	}
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.Egress != nil {
		t.Errorf("firewall off = %+v", np.Spec)
	}
	h.cfg.EngineCIDR, h.cfg.EngineNamespace = "", ""
	np, _ = h.networkPolicy("w", "P1")
	if np.Spec.Ingress[0].From[0].NamespaceSelector != nil {
		t.Errorf("no namespace configured: %+v", np.Spec.Ingress[0].From[0])
	}
}

func TestK8sEngineAddr(t *testing.T) {
	_, h := k8sWith(t, &kubeFake{}, false)
	cases := []struct {
		url  string
		host string
		port int
		bad  bool
	}{
		{"http://engine.ns.svc:9000", "engine.ns.svc", 9000, false},
		{"http://engine.ns.svc", "engine.ns.svc", 80, false},
		{"", "", 0, true},
		{"not a url", "", 0, true},
	}
	for _, c := range cases {
		h.cfg.EngineURL = c.url
		host, port, err := h.engineAddr()
		if host != c.host || port != c.port || (err != nil) != c.bad {
			t.Errorf("%q = %q %d %v", c.url, host, port, err)
		}
	}
}

// The name is resolved here because the pod has no DNS yet; a name that does
// not resolve stops the pod from being created with no engine route.
func TestK8sEngineIP(t *testing.T) {
	_, h := k8sWith(t, &kubeFake{}, false)
	ctx := context.Background()
	h.cfg.EngineURL = "http://[fd00::1]:80"
	if ip, err := h.engineIP(ctx); err != nil || ip != "fd00::1" {
		t.Errorf("literal = %q, %v", ip, err)
	}
	h.cfg.EngineURL = "nope"
	if _, err := h.engineIP(ctx); err == nil {
		t.Error("no host resolved")
	}
	// A cancelled context fails the lookup without any query leaving.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	h.cfg.EngineURL = "http://engine.invalid:8080"
	if _, err := h.engineIP(cctx); err == nil || !strings.Contains(err.Error(), "resolve the engine's address") {
		t.Errorf("unresolvable = %v", err)
	}
	if _, err := h.podManifest(cctx, "w", envport.EnvSpec{}, egressgw.Attachment{GatewayIP: "10.90.0.1"}); err == nil {
		t.Error("a tunnelled pod with no engine route")
	}
}

func TestK8sEnsureCheckout(t *testing.T) {
	ctx := context.Background()
	var clones int
	k := &kubeFake{exec: func(argv []string, _ []byte) (string, string, int) {
		if argv[0] == "test" {
			return "", "", 0 // already cloned
		}
		clones++
		return "", "", 0
	}}
	_, h := k8sWith(t, k, false)
	if err := h.ensureCheckout(ctx, "w", "/src", envport.EnvSpec{RepoURL: "https://git.test/r.git"}, nil); err != nil || clones != 0 {
		t.Errorf("existing checkout = %v, clones = %d", err, clones)
	}
	k.exec = func(argv []string, _ []byte) (string, string, int) { return "", "", 1 }
	err := h.ensureCheckout(ctx, "w", "/src", envport.EnvSpec{RepoURL: "bad", GitPass: "x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot parse repo url") {
		t.Errorf("bad URL = %v", err)
	}
	// With no credential the clone still reads its empty line.
	var script string
	k.exec = func(argv []string, stdin []byte) (string, string, int) {
		if argv[0] == "test" {
			return "", "", 1
		}
		script = string(stdin)
		return "", "", 0
	}
	if err := h.ensureCheckout(ctx, "w", "/src", envport.EnvSpec{RepoURL: "https://git.test/r.git"}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(script, "\n\n") {
		t.Errorf("stdin = %q", script)
	}
	notCloned := &kubeFake{exec: func(argv []string, _ []byte) (string, string, int) { return "", "", 1 }}
	_, h = k8sWith(t, notCloned, false, kubeRule{match: "bash -s", stderr: "Unable to connect to the server", code: 1})
	if err := h.ensureCheckout(ctx, "w", "/src", envport.EnvSpec{}, nil); err == nil {
		t.Error("a transport failure read as a clone")
	}
}

func TestK8sDestroyFailures(t *testing.T) {
	ctx := context.Background()
	for _, word := range []string{"delete pod", "delete persistentvolumeclaim", "delete networkpolicy", "delete secret"} {
		a, _ := k8sWith(t, &kubeFake{}, false, kubeRule{match: word, stderr: "boom", code: 1})
		os.MkdirAll(a.folderFor("w"), 0o755)
		if err := a.Destroy(ctx, "w"); err == nil {
			t.Errorf("failure at %q went unreported", word)
		}
		mustExist(t, a.folderFor("w"))
	}
}

func TestK8sDestroyForgetsTheRun(t *testing.T) {
	a, h := k8sWith(t, &kubeFake{}, false)
	h.tokens["w"], h.podIPs["w"], h.stats["w"] = "t", "1.2.3.4", cgroupSample{}
	f := fakeForward(t, h, "49001")
	h.fwds["w"] = f
	if err := a.Destroy(context.Background(), "w"); err != nil {
		t.Fatal(err)
	}
	if len(h.tokens)+len(h.podIPs)+len(h.stats)+len(h.fwds) != 0 {
		t.Errorf("state left: %v %v %v %v", h.tokens, h.podIPs, h.stats, h.fwds)
	}
}

// fakeForward starts a port-forward through an executable standing in for
// kubectl (Client.Bin), so no real kubectl runs.
func fakeForward(t *testing.T, h *k8sHost, port string) *kube.Forward {
	t.Helper()
	h.cfg.Kube.Bin = fakeKubectlBin(t, port)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f, err := h.cfg.Kube.Forward(ctx, "pod/w", relayPort)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func fakeKubectlBin(t *testing.T, port string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fake-kubectl")
	script := fmt.Sprintf("#!/bin/sh\necho \"Forwarding from 127.0.0.1:%s -> 7070\"\nexec sleep 60\n", port)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// muxServer is the pod's dial-in mux on loopback: it wants the token and the
// socket on one line, acks, then serves payload.
func muxServer(t *testing.T, want, payload string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				line, _ := br.ReadString('\n')
				if line != want {
					c.Write([]byte("NO\n"))
					return
				}
				c.Write([]byte("OK\n" + payload))
				io.Copy(io.Discard, br)
			}(c)
		}
	}()
	return ln.Addr().String()
}

func setRelayPort(t *testing.T, addr string) string {
	t.Helper()
	host, port, _ := net.SplitHostPort(addr)
	n := 0
	fmt.Sscanf(port, "%d", &n)
	old := relayPort
	relayPort = n
	t.Cleanup(func() { relayPort = old })
	return host
}

func TestK8sDialFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("no token", func(t *testing.T) {
		_, h := k8sWith(t, &kubeFake{secrets: map[string]string{}}, false)
		if _, err := h.dial("w", "s").Dial(ctx); err == nil || !strings.Contains(err.Error(), "no relay token") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("token read", func(t *testing.T) {
		_, h := k8sWith(t, &kubeFake{}, false, kubeRule{match: "secret", stderr: "boom", code: 1})
		if _, err := h.dialMux(ctx, "w", "s"); err == nil {
			t.Error("dialed with no token")
		}
	})
	t.Run("no address yet", func(t *testing.T) {
		k := &kubeFake{pod: `{"status":{"phase":"Pending"}}`, secrets: map[string]string{"apo-run-w": relaySecret("t")}}
		_, h := k8sWith(t, k, false)
		if _, err := h.dial("w", "s").Dial(ctx); err == nil || !strings.Contains(err.Error(), "has no address") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("pod read", func(t *testing.T) {
		_, h := k8sWith(t, &kubeFake{}, false, kubeRule{match: "pod", stderr: "boom", code: 1})
		if _, err := h.muxAddr(ctx, "w"); err == nil {
			t.Error("an address from nowhere")
		}
	})
	t.Run("nothing listening", func(t *testing.T) {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		addr := ln.Addr().String()
		ln.Close()
		ip := setRelayPort(t, addr)
		k := &kubeFake{pod: readyPod(ip), secrets: map[string]string{"apo-run-w": relaySecret("t")}}
		_, h := k8sWith(t, k, false)
		if _, err := h.dial("w", "s").Dial(ctx); err == nil {
			t.Error("dialed nothing")
		}
	})
}

// A replaced pod has a new IP: a dial on the stale address looks again, once.
func TestK8sDialRefreshesAStaleAddress(t *testing.T) {
	ctx := context.Background()
	ip := setRelayPort(t, muxServer(t, "APO1 t sock\n", "hello"))
	k := &kubeFake{pod: readyPod(ip), secrets: map[string]string{"apo-run-w": relaySecret("t")}}
	_, h := k8sWith(t, k, false)
	h.mu.Lock()
	// The pod's old address: nothing serves there (the mux listens on IPv4
	// only), and the dial fails at once. Not 127.0.0.2: only Linux routes all
	// of 127/8 to loopback; macOS blackholes it until the dial times out.
	h.podIPs["w"] = "::1"
	h.mu.Unlock()
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := h.dial("w", "sock").Dial(dctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hello" {
		t.Errorf("read = %q, %v", buf, err)
	}
	// Half-close survives the wrapper: a request/response protocol can drain.
	if err := c.(*readerConn).CloseWrite(); err != nil {
		t.Errorf("CloseWrite = %v", err)
	}
	if err := h.dial("w", "sock").Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.podIPs["w"] != ip {
		t.Errorf("cached ip = %q", h.podIPs["w"])
	}
}

func TestReaderConnWithoutHalfClose(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	rc := &readerConn{Conn: a, r: bufio.NewReader(a)}
	if err := rc.CloseWrite(); err != nil {
		t.Errorf("CloseWrite on a conn without it = %v", err)
	}
}

// From outside the cluster the mux is reached through one port-forward per
// run, kept across dials.
func TestK8sMuxAddrThroughAForward(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, h := k8sWith(t, &kubeFake{}, false)
	h.cfg.Forward = true
	h.cfg.Kube.Bin = fakeKubectlBin(t, "49123")
	defer h.close()
	addr, err := h.muxAddr(ctx, "w")
	if err != nil || addr != "127.0.0.1:49123" {
		t.Fatalf("addr = %q, %v", addr, err)
	}
	first := h.fwds["w"]
	if again, err := h.muxAddr(ctx, "w"); err != nil || again != addr || h.fwds["w"] != first {
		t.Errorf("second = %q, %v", again, err)
	}
	h.close()
	if len(h.fwds) != 0 {
		t.Errorf("forwards after close = %v", h.fwds)
	}
	h.cfg.Kube.Bin = filepath.Join(t.TempDir(), "absent")
	cctx, ccancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer ccancel()
	if _, err := h.muxAddr(cctx, "x"); err == nil {
		t.Error("an address from a forward that never started")
	}
}

func TestK8sCloseStopsTheGatewayForwards(t *testing.T) {
	a := New(Config{RunsDir: t.TempDir(), Kube: &KubeConfig{Kube: &kube.Client{R: &kubeFake{}}, Firewall: true}})
	a.Close() // nothing running: nothing to do, nothing to hang on
}

func TestK8sWaitSock(t *testing.T) {
	ctx := context.Background()
	calls := 0
	k := &kubeFake{exec: func(argv []string, _ []byte) (string, string, int) {
		if argv[0] == "test" && argv[1] == "-S" {
			calls++
			if calls < 2 {
				return "", "", 1
			}
		}
		return "", "", 0
	}}
	_, h := k8sWith(t, k, false)
	if err := h.waitSock(ctx, "w", "app-1.sock"); err != nil || calls != 2 {
		t.Errorf("waitSock = %v after %d probes", err, calls)
	}
	k.exec = func([]string, []byte) (string, string, int) { return "", "", 1 }
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := h.waitSock(cctx, "w", "absent.sock"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("absent = %v", err)
	}
}

// A running pod keeps the shim it started with: a mismatch warns, it does not
// stop a reattach.
func TestK8sRefreshShimOnlyWarns(t *testing.T) {
	k := &kubeFake{exec: func([]string, []byte) (string, string, int) {
		return base64.StdEncoding.EncodeToString([]byte("older\n")), "", 0
	}}
	_, h := k8sWith(t, k, false)
	if err := h.refreshShim(context.Background(), "w"); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestK8sCheckShimMissing(t *testing.T) {
	k := &kubeFake{exec: func([]string, []byte) (string, string, int) { return "", "", 44 }}
	_, h := k8sWith(t, k, false)
	if err := h.checkShim(context.Background(), "w"); err == nil || !strings.Contains(err.Error(), "no seeded shim") {
		t.Errorf("err = %v", err)
	}
}

func TestK8sSandboxStateErrors(t *testing.T) {
	_, h := k8sWith(t, &kubeFake{}, false, kubeRule{match: "pod", stderr: "boom", code: 1})
	if st, _, err := h.sandboxState(context.Background(), "w"); err == nil || st != "" {
		t.Errorf("state = %q, %v (must stay unknown)", st, err)
	}
	k := &kubeFake{pod: `{"status":{"phase":"Unknown"}}`}
	_, h = k8sWith(t, k, false)
	if st, legacy, err := h.sandboxState(context.Background(), "w"); err != nil || st != "exited" || legacy {
		t.Errorf("unknown phase = %q %v %v", st, legacy, err)
	}
}

func TestK8sSample(t *testing.T) {
	ctx := context.Background()
	usage := 1000000
	k := &kubeFake{exec: func([]string, []byte) (string, string, int) {
		return fmt.Sprintf("cpu %d\nmem 2048\nmax 4096\npids 3\nnproc 4\nnet 5 6\n", usage), "", 0
	}}
	a, h := k8sWith(t, k, false)
	if _, err := a.SampleResources(ctx, "w"); err == nil || !strings.Contains(err.Error(), "first reading") {
		t.Fatalf("first = %v", err)
	}
	h.mu.Lock()
	prev := h.stats["w"]
	prev.at = time.Now().Add(-time.Second)
	h.stats["w"] = prev
	h.mu.Unlock()
	usage += 500000
	s, err := a.SampleResources(ctx, "w")
	if err != nil || s.MemUsed != 2048 || s.MemLimit != 4096 || s.PIDs != 3 || s.CPUs != 4 || s.NetRx != 5 || s.NetTx != 6 || s.CPUPerc <= 0 || s.CPUPerc > 60 {
		t.Fatalf("sample = %+v, %v", s, err)
	}
	// A counter that went backwards is a restarted pod: no sample, not a
	// negative one.
	usage = 10
	if _, err := a.SampleResources(ctx, "w"); err == nil {
		t.Error("a sample across a restart")
	}
	k.exec = func([]string, []byte) (string, string, int) { return "", "", 3 }
	if _, err := a.SampleResources(ctx, "w"); err == nil || !strings.Contains(err.Error(), "exit 3") {
		t.Errorf("exit = %v", err)
	}
	k.exec = func([]string, []byte) (string, string, int) { return "mem 1\n", "", 0 }
	if _, err := a.SampleResources(ctx, "w"); err == nil || !strings.Contains(err.Error(), "no cpu.stat") {
		t.Errorf("garbage = %v", err)
	}
	_, h = k8sWith(t, &kubeFake{}, false, kubeRule{match: "exec", stderr: "Unable to connect to the server", code: 1})
	if _, err := h.sample(ctx, "w"); err == nil {
		t.Error("transport failure read as a sample")
	}
}

func TestCpuSampleZeroWall(t *testing.T) {
	at := time.Unix(1, 0)
	if got := cpuSample(cgroupSample{at: at}, cgroupSample{at: at, usageUS: 5}); got.CPUPerc != 0 {
		t.Errorf("cpu = %v", got.CPUPerc)
	}
}

func TestParseCgroupSampleEdges(t *testing.T) {
	// A quota above the node, an unset limit, a short line: all survive.
	s, err := parseCgroupSample("cpu 1\nmax 100\nquota max 100000\nnproc 4\nnet 1\nx\n")
	if err != nil || s.cpus != 4 || s.memLimit != 100 || s.rx != 0 {
		t.Errorf("sample = %+v, %v", s, err)
	}
	s, _ = parseCgroupSample("cpu 1\nquota 800000 100000\nnproc 4\n")
	if s.cpus != 4 {
		t.Errorf("a quota above the node's cores = %d", s.cpus)
	}
	// No nproc: the quota alone decides, rounded up.
	s, _ = parseCgroupSample("cpu 1\nquota 150000 100000\n")
	if s.cpus != 2 {
		t.Errorf("quota alone = %d", s.cpus)
	}
}

func TestK8sProbe(t *testing.T) {
	ctx := context.Background()
	version := kubeRule{match: "version", out: `{"serverVersion":{"gitVersion":"v1.31.0"}}`}
	yes := kubeRule{match: "auth can-i", out: "yes\n"}
	t.Run("healthy", func(t *testing.T) {
		a, h := k8sWith(t, &kubeFake{}, true, version, yes)
		h.cfg.Forward = true
		if err := a.Test(ctx); err != nil {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		a, _ := k8sWith(t, &kubeFake{}, false, kubeRule{match: "version", stderr: "Unable to connect", code: 1})
		if err := a.Test(ctx); err == nil || !strings.Contains(err.Error(), "cluster unreachable") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("rbac names what is missing", func(t *testing.T) {
		a, h := k8sWith(t, &kubeFake{}, true, version,
			kubeRule{match: "can-i create secrets", out: "no\n", code: 1},
			kubeRule{match: "can-i create services", out: "no\n", code: 1},
			kubeRule{match: "can-i create pods/portforward", out: "no\n", code: 1}, yes)
		h.cfg.Forward = true
		err := a.Test(ctx)
		if err == nil || !strings.Contains(err.Error(), "secrets, pods/portforward, services") || !strings.Contains(err.Error(), "namespace ws") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("rbac unanswered", func(t *testing.T) {
		a, _ := k8sWith(t, &kubeFake{}, false, version, kubeRule{match: "can-i", stderr: "boom", code: 1})
		if err := a.Test(ctx); err == nil {
			t.Error("an unanswered check passed")
		}
	})
	t.Run("images", func(t *testing.T) {
		for _, clear := range []func(*k8sHost){
			func(h *k8sHost) { h.cfg.SandboxImage = "" },
			func(h *k8sHost) { h.cfg.ShimImage = "" },
			func(h *k8sHost) { h.cfg.ToolsImage = "" },
			func(h *k8sHost) { h.cfg.GatewayImage = "" },
		} {
			a, h := k8sWith(t, &kubeFake{}, true, version, yes)
			clear(h)
			if err := a.Test(ctx); err == nil || !strings.Contains(err.Error(), "images must be set") {
				t.Errorf("err = %v", err)
			}
		}
		// The gateway image only matters with the firewall on.
		a, h := k8sWith(t, &kubeFake{}, false, version, yes)
		h.cfg.GatewayImage = ""
		if err := a.Test(ctx); err != nil {
			t.Errorf("firewall off = %v", err)
		}
	})
	t.Run("engine url", func(t *testing.T) {
		a, h := k8sWith(t, &kubeFake{}, false, version, yes)
		h.cfg.EngineURL = ""
		if err := a.Test(ctx); err == nil || !strings.Contains(err.Error(), "is not a URL") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestExecFS(t *testing.T) {
	ctx := context.Background()
	var script string
	answer := func(out string, code int) func([]string, []byte) (string, string, int) {
		return func(_ []string, stdin []byte) (string, string, int) {
			script = string(stdin)
			return out, "denied\n", code
		}
	}
	k := &kubeFake{}
	_, h := k8sWith(t, k, false)
	f := execFS{h: h, name: "w"}

	k.exec = answer(base64.StdEncoding.EncodeToString([]byte("data"))+"\n", 0)
	if b, err := f.ReadFile(ctx, "/p/f"); err != nil || string(b) != "data" {
		t.Errorf("read = %q, %v", b, err)
	}
	k.exec = answer("", 1)
	if _, err := f.ReadFile(ctx, "/p/f"); err == nil || !strings.Contains(err.Error(), "read /p/f: exit 1") {
		t.Errorf("read exit 1 = %v", err)
	}
	k.exec = answer("", 2)
	if _, err := f.ReadFile(ctx, "/p/f"); err == nil || !strings.Contains(err.Error(), "sandbox w: exit 2") {
		t.Errorf("read exit 2 = %v", err)
	}
	k.exec = answer("", 44)
	if _, err := f.ReadFile(ctx, "/p/f"); !os.IsNotExist(err) && !strings.Contains(fmt.Sprint(err), "not exist") {
		t.Errorf("missing = %v", err)
	}

	k.exec = answer("", 0)
	if err := f.WriteFile(ctx, "/p/it's", []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, `chmod 640 '/p/it'\''s'`) || !strings.HasSuffix(script, base64.StdEncoding.EncodeToString([]byte("x"))+"\n") {
		t.Errorf("write script = %q", script)
	}
	k.exec = answer("", 1)
	if err := f.WriteFile(ctx, "/p/f", nil, 0o600); err == nil || !strings.Contains(err.Error(), "write /p/f") {
		t.Errorf("write = %v", err)
	}
	if err := f.MkdirAll(ctx, "/p"); err == nil || !strings.Contains(err.Error(), "mkdir /p") {
		t.Errorf("mkdir = %v", err)
	}
	k.exec = answer("", 0)
	if err := f.MkdirAll(ctx, "/p d"); err != nil || !strings.Contains(script, "mkdir -p '/p d'") {
		t.Errorf("mkdir = %v, %q", err, script)
	}

	if !f.Exists(ctx, "/p") || !f.IsRegular(ctx, "/p") {
		t.Error("exit 0 is yes")
	}
	k.exec = answer("", 1)
	if f.Exists(ctx, "/p") || f.IsRegular(ctx, "/p") {
		t.Error("exit 1 is no")
	}
	k.exec = answer("/d/a\n\n/d/b\n", 0)
	if files, err := f.Walk(ctx, "/d"); err != nil || strings.Join(files, ",") != "/d/a,/d/b" {
		t.Errorf("walk = %v, %v", files, err)
	}
	k.exec = answer("", 2)
	if _, err := f.Walk(ctx, "/d"); err == nil {
		t.Error("a failed find read as an empty dir")
	}
}

// A compose rebuild on Kubernetes empties the pod's daemon and nothing else:
// the checkout is on the volume under the pod, so neither may go.
func TestK8sResetSandboxKeepsThePodAndItsVolume(t *testing.T) {
	var seen []string
	k := &kubeFake{pod: readyPod("10.0.0.9"), secrets: map[string]string{}, exec: func(argv []string, _ []byte) (string, string, int) {
		seen = append(seen, strings.Join(argv, " "))
		if strings.HasPrefix(seen[len(seen)-1], "docker ps") {
			return "c1\nc2\n", "", 0
		}
		return "", "", 0
	}}
	_, h := k8sWith(t, k, false)
	if err := h.resetSandbox(context.Background(), "w"); err != nil {
		t.Fatal(err)
	}
	if len(k.deleted) != 0 {
		t.Errorf("deleted %v: the checkout went with them", k.deleted)
	}
	want := []string{"docker ps -aq", "docker rm -f -v c1 c2", "docker volume prune -af", "docker network prune -f"}
	if !slices.Equal(seen, want) {
		t.Errorf("in the pod: %q, want %q", seen, want)
	}
}

func TestK8sResetSandboxEdges(t *testing.T) {
	ctx := context.Background()
	// A pod that ended has no daemon to empty: ensurePod replaces it.
	dead := &kubeFake{pod: `{"status":{"phase":"Failed"}}`, secrets: map[string]string{}, exec: func(argv []string, _ []byte) (string, string, int) {
		t.Errorf("exec in an ended pod: %v", argv)
		return "", "", 0
	}}
	if _, h := k8sWith(t, dead, false); h.resetSandbox(ctx, "w") != nil {
		t.Error("an ended pod is nothing to fail on")
	}
	// A daemon that will not empty stops the rebuild: `up` would reuse what is left.
	for _, step := range []string{"ps", "rm", "volume", "network"} {
		k := &kubeFake{pod: readyPod("10.0.0.9"), secrets: map[string]string{}, exec: func(argv []string, _ []byte) (string, string, int) {
			if argv[1] == step {
				return "", "boom", 1
			}
			return "c1\n", "", 0
		}}
		if _, h := k8sWith(t, k, false); h.resetSandbox(ctx, "w") == nil {
			t.Errorf("a failed docker %s went unnoticed", step)
		}
	}
	gone := &kubeFake{secrets: map[string]string{}}
	if _, h := k8sWith(t, gone, false, kubeRule{match: "pod w -o json", stderr: "api down", code: 1}); h.resetSandbox(ctx, "w") == nil {
		t.Error("an unreachable cluster went unnoticed")
	}
}
