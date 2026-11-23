//go:build live

package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/kube"
	"apothecary/internal/platform/livetest"
)

// A Kata sandbox without a VM storage class keeps its checkout and inner
// docker on emptyDirs, each a block disk in the guest whose size the guest
// cannot see (it shows the node's whole disk). What bounds it is the
// kubelet: it measures the disk's backing file on the node and evicts the
// pod past the emptyDir's sizeLimit (storage_size). This holds that on a
// Kata node (task sandbox:test:kata; live_k8s_test.go's header says what it
// needs): a Kata pod that writes past a small sizeLimit is evicted, and the
// eviction names the volume.
func TestLiveKataDiskLimit(t *testing.T) {
	if os.Getenv("APOTHECARY_LIVE_RUNTIME") != "kata" {
		livetest.Skip(t, "APOTHECARY_LIVE_RUNTIME is not kata: this checks a Kata node's emptyDir limit (task sandbox:test:kata)")
	}
	c, ns := liveKube(t)
	prefix := os.Getenv("APOTHECARY_LIVE_IMAGE_PREFIX")
	if prefix == "" {
		livetest.Skip(t, "APOTHECARY_LIVE_IMAGE_PREFIX unset: where this tree's images were pushed")
	}
	ns += "-disk"
	admin := &kube.Client{Kubeconfig: c.Kubeconfig}
	c = &kube.Client{Kubeconfig: c.Kubeconfig, Namespace: ns}
	_ = exec.Command("kubectl", "--kubeconfig", c.Kubeconfig, "delete", "namespace", ns, "--wait=true").Run()
	kubectl(t, admin, "", "create", "namespace", ns)
	kubectl(t, admin, "", "label", "namespace", ns, "pod-security.kubernetes.io/enforce=privileged")
	defer exec.Command("kubectl", "--kubeconfig", c.Kubeconfig, "delete", "namespace", ns, "--wait=false").Run()

	// The sandbox's disk shape (runtime.go, the vm trait): a sized Kata pod,
	// privileged in its VM, an emptyDir with a sizeLimit. It writes twice
	// the limit, then waits to be stopped.
	pod := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata: {name: disk-limit}
spec:
  runtimeClassName: kata
  restartPolicy: Never
  containers:
    - name: fill
      image: %s/sandbox:local
      imagePullPolicy: %s
      securityContext: {privileged: true}
      resources:
        limits: {cpu: "1", memory: 1Gi}
        requests: {cpu: 250m, memory: 1Gi}
      command: [sh, -c, "df -h /data | tail -n 1; dd if=/dev/urandom of=/data/fill bs=1M count=512 conv=fsync 2>&1 | tail -n 1; sync; echo written; sleep 600"]
      volumeMounts: [{name: data, mountPath: /data}]
  volumes:
    - {name: data, emptyDir: {sizeLimit: 256Mi}}
`, prefix, livePullPolicy())
	kubectl(t, c, pod, "apply", "-f", "-")

	// The kubelet's eviction manager looks every 10s; the write takes some.
	deadline := time.Now().Add(5 * time.Minute)
	var phase, reason, message string
	for time.Now().Before(deadline) {
		out := kubectl(t, c, "", "get", "pod", "disk-limit", "-o", "jsonpath={.status.phase}|{.status.reason}|{.status.message}")
		parts := strings.SplitN(out, "|", 3)
		phase, reason, message = parts[0], parts[1], parts[2]
		if reason == "Evicted" || phase == "Succeeded" || phase == "Failed" {
			break
		}
		time.Sleep(5 * time.Second)
	}
	logs, _ := exec.Command("kubectl", "--kubeconfig", c.Kubeconfig, "-n", ns, "logs", "disk-limit").CombinedOutput()
	if reason != "Evicted" {
		t.Fatalf("a Kata pod written past its emptyDir's sizeLimit was not evicted: phase %q reason %q %s\npod log:\n%s", phase, reason, message, logs)
	}
	if !strings.Contains(message, "data") {
		t.Errorf("the eviction does not name the volume: %s", message)
	}
	t.Logf("evicted: %s (pod log: %s)", message, strings.TrimSpace(string(logs)))
}
