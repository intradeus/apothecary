//go:build live

package browser

// Live test of the Kubernetes browser host against a real cluster (k3d
// locally: task test:integration:k8s, or task k8s:cluster k8s:images
// k8s:test-live to keep the cluster): the browser pod pulls the pinned image
// (DefaultImage), its init container seeds this tree's shim, and one MCP
// session runs through the engine's session mux — the docker test's session
// (liveSession), on a pod.
//
// It needs what the workspace's Kubernetes live test needs:
//
//   - APOTHECARY_LIVE_KUBECONFIG: a kubeconfig for a THROWAWAY cluster. The
//     test creates and deletes its own namespace (<APOTHECARY_LIVE_K8S_NAMESPACE,
//     default apothecary-live>-browser).
//   - APOTHECARY_LIVE_IMAGE_PREFIX: where this tree's shim image was pushed
//     (tag :local).
//   - APOTHECARY_LIVE_KEEP=1 to leave the namespace for inspection.
//
// Unlike the workspace test it cannot stub the engine in the cluster: the
// session mux IS this process, so the browser pod must reach it. The test
// asks the cluster which of the docker host's names reaches it
// (host.k3d.internal on Linux, host.docker.internal on Docker Desktop and
// OrbStack) and fails when none does.

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/kube"
	"apothecary/workspace"
)

func TestLiveK8sBrowserSession(t *testing.T) {
	kc := os.Getenv("APOTHECARY_LIVE_KUBECONFIG")
	if kc == "" {
		t.Skip("APOTHECARY_LIVE_KUBECONFIG unset: needs a throwaway cluster — see the header comment")
	}
	prefix := os.Getenv("APOTHECARY_LIVE_IMAGE_PREFIX")
	if prefix == "" {
		t.Skip("APOTHECARY_LIVE_IMAGE_PREFIX unset: where this tree's shim image was pushed")
	}
	ns := os.Getenv("APOTHECARY_LIVE_K8S_NAMESPACE")
	if ns == "" {
		ns = "apothecary-live"
	}
	ns += "-browser"
	cluster := &kube.Client{Kubeconfig: kc}
	c := &kube.Client{Kubeconfig: kc, Namespace: ns}

	_ = exec.Command("kubectl", "--kubeconfig", kc, "delete", "namespace", ns, "--wait=true").Run()
	kubectlLive(t, cluster, "", "create", "namespace", ns)
	if os.Getenv("APOTHECARY_LIVE_KEEP") != "1" {
		defer exec.Command("kubectl", "--kubeconfig", kc, "delete", "namespace", ns, "--wait=false").Run()
	}

	host, ip := hostSeenFromPods(t, c)
	t.Logf("pods reach this process at %s (%s)", host, ip)
	port := freePort(t)
	mux, err := NewSessionMux("0.0.0.0:"+port, net.JoinHostPort(host, port))
	if err != nil {
		t.Fatal(err)
	}
	defer mux.Close()

	const project = "live-k8s"
	m := New(Config{
		Host: &K8sHost{Kube: c, ShimImage: prefix + "/shim:local", Mux: mux, EngineCIDR: ip + "/32", PullPolicy: "Always"},
		// The kubelet pulls; nothing to prepare engine-side.
		Image: func(context.Context) (string, error) { return DefaultImage, nil },
		Tag:   func() string { return DefaultImage },
		Log:   slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})
	defer m.Close()
	liveSession(t, m, project)

	// The seeded shim is this tree's, and Chromium went with its session.
	inPod := func(script string) string {
		return kubectlLive(t, c, "", "exec", podName(project), "-c", "browser", "--", "sh", "-c", script)
	}
	if got := inPod("cat " + shimDir + "/shim.hash"); got != workspace.ShimSourceHash() {
		t.Errorf("seeded shim %q, this tree's is %q (push it again: task k8s:images)", got, workspace.ShimSourceHash())
	}
	if got := inPod(chromiumCount); got != "0" {
		t.Errorf("chromium outlived its session: %s", got)
	}
}

func kubectlLive(t *testing.T, c *kube.Client, stdin string, args ...string) string {
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

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

// hostSeenFromPods is the docker host's name that reaches this process from
// a pod, and the IPv4 address it resolves to there (the browser's network
// policy lets the pod reach that address only): a probe pod tells a
// listener here, over each candidate, which one it came in on.
func hostSeenFromPods(t *testing.T, c *kube.Client) (string, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	heard := make(chan string, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if line, err := bufio.NewReader(conn).ReadString('\n'); err == nil {
				heard <- strings.TrimSpace(line)
			}
			conn.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	probe := fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata: {name: host-probe}
spec:
  restartPolicy: Never
  containers:
    - name: probe
      image: busybox:1.37
      command: [sh, -c]
      args:
        - |
          for h in host.k3d.internal host.docker.internal; do
            ip=$(nslookup "$h" 2>/dev/null | awk '/^Address: [0-9.]+$/ {a=$2} END {print a}')
            [ -n "$ip" ] && echo "$h $ip" | nc -w 3 "$ip" %d
          done
          exit 0
`, port)
	kubectlLive(t, c, probe, "apply", "-f", "-")
	select {
	case line := <-heard:
		host, ip, _ := strings.Cut(line, " ")
		return host, ip
	case <-time.After(3 * time.Minute):
		logs, _ := exec.Command("kubectl", append(c.Global(), "logs", "host-probe")...).CombinedOutput()
		t.Fatalf("no pod reached this process over host.k3d.internal or host.docker.internal (port %d); probe log:\n%s", port, logs)
	}
	return "", ""
}
