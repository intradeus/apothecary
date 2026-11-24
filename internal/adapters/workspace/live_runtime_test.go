//go:build live

package workspace

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/kube"
	"apothecary/internal/platform/livetest"
	"apothecary/internal/ports/envport"
)

// The integration's Test on a real cluster (live_k8s_test.go's header says
// what it needs; only the shim image is pulled): a probe pod under the
// privileged runtime runs to completion, and under kata on a Kata cluster,
// under sysbox on a sysbox one; a runtime whose RuntimeClass is
// missing is refused at admission; one whose class names a handler no node
// has never runs, and the refusal carries the kubelet's reason.
func TestLiveK8sRuntimeProbe(t *testing.T) {
	c, ns := liveKube(t)
	prefix := os.Getenv("APOTHECARY_LIVE_IMAGE_PREFIX")
	if prefix == "" {
		livetest.Skip(t, "APOTHECARY_LIVE_IMAGE_PREFIX unset: where this tree's images were pushed")
	}
	ns += "-rt"
	c = &kube.Client{Kubeconfig: c.Kubeconfig, Namespace: ns}
	admin := &kube.Client{Kubeconfig: c.Kubeconfig}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	_ = exec.Command("kubectl", "--kubeconfig", c.Kubeconfig, "delete", "namespace", ns, "--wait=true").Run()
	kubectl(t, admin, "", "create", "namespace", ns)
	kubectl(t, admin, "", "label", "namespace", ns, "pod-security.kubernetes.io/enforce=privileged")
	defer exec.Command("kubectl", "--kubeconfig", c.Kubeconfig, "delete", "namespace", ns, "--wait=false").Run()
	// A RuntimeClass whose handler no node's containerd knows.
	kubectl(t, admin, "apiVersion: node.k8s.io/v1\nkind: RuntimeClass\nmetadata: {name: apo-live-no-handler}\nhandler: apo-live-no-handler\n", "apply", "-f", "-")
	defer exec.Command("kubectl", "--kubeconfig", c.Kubeconfig, "delete", "runtimeclass", "apo-live-no-handler", "--wait=false").Run()

	host := func(rt envport.Runtime, class string) *k8sHost {
		a := New(Config{RunsDir: t.TempDir(), Kube: &KubeConfig{
			Kube: c, ShimImage: prefix + "/shim:local", PullPolicy: livePullPolicy(),
			Runtime: rt, RuntimeClass: class,
		}})
		t.Cleanup(a.Close)
		h := a.host.(*k8sHost)
		h.probeTimeout = 45 * time.Second
		return h
	}
	probes := func() string {
		return kubectl(t, c, "", "get", "pods", "-l", k8sLabelRuntimeProbe, "-o", "name")
	}

	if err := host(envport.RuntimePrivileged, "").probeRuntimes(ctx); err != nil {
		t.Fatalf("privileged: %v", err)
	}
	// On a cluster with Kata (APOTHECARY_LIVE_RUNTIME=kata): its probe boots
	// a VM. On one with sysbox: its probe runs under the sysbox-runc class,
	// in a user namespace of its own.
	switch os.Getenv("APOTHECARY_LIVE_RUNTIME") {
	case string(envport.RuntimeKata):
		h := host(envport.RuntimeKata, "")
		h.probeTimeout = 2 * time.Minute
		if err := h.probeRuntimes(ctx); err != nil {
			t.Fatalf("kata: %v", err)
		}
	case string(envport.RuntimeSysbox):
		if err := host(envport.RuntimeSysbox, "").probeRuntimes(ctx); err != nil {
			t.Fatalf("sysbox: %v", err)
		}
	}

	err := host(envport.RuntimeSysbox, "apo-live-no-such-class").probeRuntimes(ctx)
	if err == nil || !strings.Contains(err.Error(), "the cluster refused a pod under it") || !strings.Contains(err.Error(), "apo-live-no-such-class") {
		t.Errorf("missing class: %v", err)
	}

	err = host(envport.RuntimeSysbox, "apo-live-no-handler").probeRuntimes(ctx)
	if err == nil || !strings.Contains(err.Error(), "did not run within") || !strings.Contains(err.Error(), "apo-live-no-handler") {
		t.Errorf("missing handler: %v", err)
	}
	t.Logf("missing handler: %v", err)

	// Every probe pod is gone (deletion is asynchronous: give it a moment).
	deadline := time.Now().Add(time.Minute)
	for probes() != "" && time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
	}
	if left := probes(); left != "" {
		t.Errorf("probe pods left: %s", left)
	}
}
