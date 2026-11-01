package egressgw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/kube"
)

const secretAnswer = `{"metadata":{"name":"apo-gw-acme"},"data":{"wg-private-key":"cHJpdg==","wg-public-key":"cHVi","control-token":"dG9r"}}`

// failing is a kubeScript with one call that fails, by the words it carries.
type failing struct {
	*kubeScript
	word, stderr string
	err          error
}

func (f failing) Run(ctx context.Context, bin string, args []string, stdin []byte, tee io.Writer, env ...string) ([]byte, []byte, int, error) {
	if strings.Contains(strings.Join(args, " "), f.word) {
		if f.err != nil {
			return nil, nil, 0, f.err
		}
		return nil, []byte(f.stderr), 1, nil
	}
	return f.kubeScript.Run(ctx, bin, args, stdin, nil, env...)
}

func TestK8sHTTPClientIsLazy(t *testing.T) {
	h := &K8sHost{}
	c := h.httpClient()
	if c == nil || c.Timeout != 3*time.Second || h.httpClient() != c || h.fwd == nil {
		t.Fatalf("client = %+v, fwd = %v", c, h.fwd)
	}
}

func TestRandomHex(t *testing.T) {
	a, b := randomHex(16), randomHex(16)
	if len(a) != 32 || a == b {
		t.Errorf("randomHex = %q, %q", a, b)
	}
}

func TestGatewaySecretCreatesOnceAndKeeps(t *testing.T) {
	ctx := context.Background()
	k := &kubeScript{answers: map[string]string{}}
	h := &K8sHost{Kube: &kube.Client{Namespace: "ws", R: k}}
	// Absent: a fresh identity, applied.
	sec, err := h.gatewaySecret(ctx, "ACME")
	if err != nil || sec["wg-private-key"] == "" || sec["wg-public-key"] == "" || len(sec["control-token"]) != 64 {
		t.Fatalf("created = %v, %v", sec, err)
	}
	s := k.applied("Secret")
	if s == nil || s["metadata"].(map[string]any)["name"] != "apo-gw-acme" {
		t.Fatalf("applied = %v", s)
	}

	// Present: reused as it is; a rotated token would cut the engine off.
	k = &kubeScript{answers: map[string]string{"get secret apo-gw-acme": secretAnswer}}
	h.Kube.R = k
	sec, err = h.gatewaySecret(ctx, "ACME")
	if err != nil || sec["control-token"] != "tok" || sec["wg-private-key"] != "priv" {
		t.Fatalf("reused = %v, %v", sec, err)
	}
	if k.applied("Secret") != nil {
		t.Error("an existing identity was rewritten")
	}

	// Half a secret is no secret: no token, a new identity.
	k = &kubeScript{answers: map[string]string{"get secret": `{"data":{"wg-private-key":"cHJpdg=="}}`}}
	h.Kube.R = k
	if sec, err := h.gatewaySecret(ctx, "ACME"); err != nil || sec["control-token"] == "tok" || sec["control-token"] == "" {
		t.Fatalf("partial = %v, %v", sec, err)
	}
}

func TestGatewaySecretFailuresAreNotAbsence(t *testing.T) {
	ctx := context.Background()
	f := failing{kubeScript: &kubeScript{}, word: "get", stderr: "Unable to connect"}
	h := &K8sHost{Kube: &kube.Client{R: f}}
	if _, err := h.gatewaySecret(ctx, "p"); err == nil {
		t.Error("an API failure read as absent: a new identity would replace the live one")
	}
	if f.kubeScript.applied("Secret") != nil {
		t.Error("a secret was written after a failed read")
	}
	h = &K8sHost{Kube: &kube.Client{R: failing{kubeScript: &kubeScript{}, word: "apply", stderr: "Forbidden"}}}
	if _, err := h.gatewaySecret(ctx, "p"); err == nil {
		t.Error("an unstored identity was returned")
	}
}

func healthyServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeKubectl stands in for kubectl's binary in port-forward paths: Client.Bin
// points at it, so no real kubectl runs.
func fakeKubectl(t *testing.T, port string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fake-kubectl")
	script := fmt.Sprintf("#!/bin/sh\necho \"Forwarding from 127.0.0.1:%s -> 9041\"\nexec sleep 60\n", port)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// EnsureGateway through the port-forward path, the one reachable without a
// cluster DNS: everything it applies, in order, then a healthy control API.
func TestK8sEnsureGatewayAppliesEverything(t *testing.T) {
	srv := healthyServer(t)
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	k := &kubeScript{answers: map[string]string{"get secret": secretAnswer}}
	h := &K8sHost{Kube: &kube.Client{Bin: fakeKubectl(t, port), Namespace: "ws", R: k}, Image: "gw:1", Forward: true, StorageClass: "fast"}
	defer h.Close()
	c, err := h.EnsureGateway(context.Background(), "ACME", "Acme App")
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "http://127.0.0.1:"+port || c.Token != "tok" {
		t.Errorf("control = %+v", c)
	}
	for _, kind := range []string{"PersistentVolumeClaim", "Deployment", "Service", "NetworkPolicy"} {
		if k.applied(kind) == nil {
			t.Errorf("%s not applied", kind)
		}
	}
	pvc := k.applied("PersistentVolumeClaim")
	b, _ := json.Marshal(pvc)
	if !strings.Contains(string(b), `"storage":"20Gi"`) || !strings.Contains(string(b), `"storageClassName":"fast"`) {
		t.Errorf("pvc = %s", b)
	}
	dep, _ := json.Marshal(k.applied("Deployment"))
	if !strings.Contains(string(dep), `"apothecary.io/project-name":"Acme App"`) || !strings.Contains(string(dep), `"type":"Recreate"`) {
		t.Errorf("deployment = %s", dep)
	}
	rolled := false
	for _, a := range k.calls {
		if strings.Join(a, " ") == "--namespace ws rollout status deployment/apo-gw-acme --timeout=180s" {
			rolled = true
		}
	}
	if !rolled {
		t.Errorf("no rollout wait: %v", k.calls)
	}

	// A second ensure reuses the forward.
	h.mu.Lock()
	first := h.fwd["ACME"]
	h.mu.Unlock()
	if _, err := h.control(context.Background(), "ACME", "tok"); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	second := h.fwd["ACME"]
	h.mu.Unlock()
	if first == nil || first != second {
		t.Errorf("forwards = %p, %p", first, second)
	}
}

func TestK8sEnsureGatewayFailures(t *testing.T) {
	ctx := context.Background()
	for _, word := range []string{"get", "create", "apply", "rollout"} {
		h := &K8sHost{Kube: &kube.Client{R: failing{kubeScript: &kubeScript{answers: map[string]string{"get secret": secretAnswer}}, word: word, stderr: "boom"}}}
		c, err := h.EnsureGateway(ctx, "p", "")
		if err == nil {
			t.Errorf("failure at %q went unreported: %+v", word, c)
		}
		if word == "rollout" && !strings.Contains(err.Error(), "did not roll out") {
			t.Errorf("rollout = %v", err)
		}
	}
}

func TestK8sEnsureGatewayForwardFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	k := &kubeScript{answers: map[string]string{"get secret": secretAnswer}}
	h := &K8sHost{Kube: &kube.Client{Bin: filepath.Join(t.TempDir(), "absent"), R: k}, Forward: true}
	if _, err := h.EnsureGateway(ctx, "p", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
}

func TestK8sControlDirect(t *testing.T) {
	h := &K8sHost{Kube: &kube.Client{Namespace: "ws"}}
	c, err := h.control(context.Background(), "ACME", "tok")
	if err != nil || c.BaseURL != "http://apo-gw-acme.ws.svc:9041" || c.Token != "tok" {
		t.Errorf("control = %+v, %v", c, err)
	}
}

func TestK8sControlLookup(t *testing.T) {
	ctx := context.Background()
	full := map[string]string{"get secret": secretAnswer, "get deployment": `{"metadata":{"name":"apo-gw-acme"}}`}
	h := &K8sHost{Kube: &kube.Client{Namespace: "ws", R: &kubeScript{answers: full}}}
	c, ok := h.Control(ctx, "acme")
	if !ok || c.Token != "tok" {
		t.Errorf("found = %+v, %v", c, ok)
	}
	// Nothing is created by a lookup.
	noSecret := &kubeScript{answers: map[string]string{}}
	h.Kube.R = noSecret
	if _, ok := h.Control(ctx, "acme"); ok {
		t.Error("control without a secret")
	}
	if noSecret.applied("Secret") != nil {
		t.Error("Control wrote a secret")
	}
	h.Kube.R = &kubeScript{answers: map[string]string{"get secret": secretAnswer}}
	if _, ok := h.Control(ctx, "acme"); ok {
		t.Error("control without a deployment: the gateway was removed")
	}
	h.Kube.R = failing{kubeScript: &kubeScript{answers: full}, word: "secret", stderr: "boom"}
	if _, ok := h.Control(ctx, "acme"); ok {
		t.Error("an API failure found a gateway")
	}
	h.Kube.R = failing{kubeScript: &kubeScript{answers: full}, word: "deployment", stderr: "boom"}
	if _, ok := h.Control(ctx, "acme"); ok {
		t.Error("an API failure found a gateway")
	}
	// A forward that cannot start means no control, not a panic.
	cctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	h = &K8sHost{Kube: &kube.Client{Bin: filepath.Join(t.TempDir(), "absent"), R: &kubeScript{answers: full}}, Forward: true}
	if _, ok := h.Control(cctx, "acme"); ok {
		t.Error("a dead forward found a gateway")
	}
}

func TestK8sAttachRunFailures(t *testing.T) {
	ctx := context.Background()
	gw := map[string]string{"get secret apo-gw-p": secretAnswer}
	svc := `{"metadata":{"name":"apo-gw-p"},"spec":{"clusterIP":"10.43.7.7"}}`
	cases := []struct {
		name    string
		answers map[string]string
		word    string
	}{
		{"gateway secret", map[string]string{}, "get --request-timeout=30s secret apo-gw-p"},
		{"no service", gw, ""},
		{"service with no address", map[string]string{"get secret apo-gw-p": secretAnswer, "get service apo-gw-p": `{"spec":{"clusterIP":""}}`}, ""},
		{"service read", gw, "get --request-timeout=30s service"},
		{"listing peers", map[string]string{"get secret apo-gw-p": secretAnswer, "get service apo-gw-p": svc}, "secrets"},
		{"storing the identity", map[string]string{"get secret apo-gw-p": secretAnswer, "get service apo-gw-p": svc, "get secrets": `{"items":[]}`}, "apply"},
	}
	for _, c := range cases {
		var r kube.Runner = &kubeScript{answers: c.answers}
		if c.word != "" {
			r = failing{kubeScript: &kubeScript{answers: c.answers}, word: c.word, stderr: "boom"}
		}
		h := &K8sHost{Kube: &kube.Client{R: r}}
		if att, err := h.AttachRun(ctx, "p", "", "r1"); err == nil {
			t.Errorf("%s: attached %+v", c.name, att)
		}
	}
}

func TestK8sDetachDeletesTheIdentity(t *testing.T) {
	k := &kubeScript{}
	h := &K8sHost{Kube: &kube.Client{R: k}}
	if err := h.DetachRun(context.Background(), "p", "r1"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(k.calls[0], " "); !strings.HasPrefix(got, "delete secret apo-wg-r1") {
		t.Errorf("delete = %q", got)
	}
}

func TestFreeIPExhaustion(t *testing.T) {
	used := map[string]bool{}
	for n := 2; n < 65535; n++ {
		used[fmt.Sprintf("10.90.%d.%d", n/256, n%256)] = true
	}
	if ip, err := freeIP(used); err == nil {
		t.Errorf("a full network gave out %s", ip)
	}
}

// Sandboxes of other projects must not reach the WireGuard port, and only the
// engine may reach the control API; protect CIDRs stay out of the egress rule.
func TestGatewayNetworkPolicyEngineScope(t *testing.T) {
	h := &K8sHost{Kube: &kube.Client{Namespace: "ws"}}
	np := h.networkPolicy("ACME", gwLabels("ACME"))
	wg := np.Spec.Ingress[0]
	if got := wg.From[0].PodSelector.MatchLabels; got[labelProject] != "acme" || got[labelSandbox] != "true" {
		t.Errorf("wireguard peers = %+v", got)
	}
	ctl := np.Spec.Ingress[1]
	if ctl.From[0].NamespaceSelector != nil {
		t.Errorf("no engine namespace configured, but one is selected: %+v", ctl.From[0])
	}
	if ctl.From[0].PodSelector.MatchLabels[EngineAppLabel] != EngineAppValue || ctl.Ports[0].Port != 9041 {
		t.Errorf("control rule = %+v", ctl)
	}
}

func TestK8sServiceSelectsTheGateway(t *testing.T) {
	s := (&K8sHost{}).service("ACME", gwLabels("ACME"))
	if s.Spec.Selector[labelGateway] != "acme" || s.Metadata.Name != "apo-gw-acme" || len(s.Spec.Ports) != 2 {
		t.Errorf("service = %+v", s)
	}
}

func TestK8sCloseStopsForwards(t *testing.T) {
	srv := healthyServer(t)
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	h := &K8sHost{Kube: &kube.Client{Bin: fakeKubectl(t, port), Namespace: "ws", R: &kubeScript{}}, Forward: true}
	ctx := context.Background()
	if _, err := h.control(ctx, "a", "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.control(ctx, "b", "t"); err != nil {
		t.Fatal(err)
	}
	h.Close()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.fwd) != 0 {
		t.Errorf("forwards left: %d", len(h.fwd))
	}
}

func TestK8sEnsureGatewayUnhealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	k := &kubeScript{answers: map[string]string{"get secret": secretAnswer}}
	h := &K8sHost{Kube: &kube.Client{Bin: fakeKubectl(t, port), R: k}, Forward: true}
	defer h.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if c, err := h.EnsureGateway(ctx, "p", ""); err == nil {
		t.Errorf("an unhealthy gateway was handed out: %+v", c)
	}
}
