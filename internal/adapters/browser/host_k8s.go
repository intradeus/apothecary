package browser

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"time"

	"apothecary/internal/adapters/kube"
	"apothecary/internal/ports/envport"
)

// K8sHost runs each project's browser as a pod. A session's Chromium reaches
// the routing proxy over TCP: the engine's SessionMux, opened with the
// session's token. The pod's network policy lets it reach that port and
// nothing else — no network, with one door. The shim comes from the
// release's shim image, seeded into the pod by an init container.
type K8sHost struct {
	Kube *kube.Client
	// ShimImage is the shim the pod's init container seeds (the same one a
	// sandbox pod seeds).
	ShimImage string
	// Mux serves the sessions' routing proxies.
	Mux *SessionMux
	// EngineNamespace selects the engine's pods in the network policy;
	// EngineCIDR replaces that when the engine runs outside the cluster.
	EngineNamespace, EngineCIDR string
	PullPolicy                  string
}

const browserLabel = "apothecary.io/browser"

func podName(project string) string { return "apo-browser-" + envport.ProjectHostKey(project) }

func (h *K8sHost) Name(project string) string { return podName(project) }

func (h *K8sHost) Ensure(ctx context.Context, project, projectName, img string, replace bool) error {
	name := podName(project)
	if err := h.Kube.Apply(ctx, h.networkPolicy(project)); err != nil {
		return err
	}
	var pod kube.Pod
	found, err := h.Kube.Get(ctx, "pod", name, &pod)
	if err != nil {
		return err
	}
	if found {
		stale := len(pod.Spec.Containers) == 0 || pod.Spec.Containers[0].Image != img ||
			len(pod.Spec.InitContainers) == 0 || pod.Spec.InitContainers[0].Image != h.ShimImage
		ended := pod.Status.Phase == "Failed" || pod.Status.Phase == "Succeeded"
		if !ended && (!stale || !replace) {
			return h.Kube.WaitReady(ctx, name, 5*time.Minute)
		}
		if err := h.Kube.Delete(ctx, "pod", name, true); err != nil {
			return err
		}
	}
	key := envport.ProjectHostKey(project)
	labels := map[string]string{"apothecary.io/managed": "true", "apothecary.io/project": key, browserLabel: key}
	annotations := map[string]string{}
	if projectName != "" {
		annotations["apothecary.io/project-name"] = projectName
	}
	p := kube.Pod{TypeMeta: kube.PodType(),
		Metadata: kube.ObjectMeta{Name: name, Labels: labels, Annotations: annotations},
		Spec: kube.PodSpec{
			RestartPolicy:                "Always",
			AutomountServiceAccountToken: kube.Ptr(false),
			EnableServiceLinks:           kube.Ptr(false),
			// The shim image's entrypoint is the shim: `seed` copies itself.
			InitContainers: []kube.Container{{
				Name: "shim-seed", Image: h.ShimImage, ImagePullPolicy: h.PullPolicy,
				Args:         []string{"seed", shimDir},
				VolumeMounts: []kube.VolumeMount{{Name: "shim", MountPath: shimDir}},
			}},
			Containers: []kube.Container{{
				Name: "browser", Image: img, ImagePullPolicy: h.PullPolicy,
				Command: idleCommand,
				SecurityContext: &kube.SecurityContext{
					AllowPrivilegeEscalation: kube.Ptr(false),
					Capabilities:             &kube.Capabilities{Drop: []string{"ALL"}},
				},
				VolumeMounts: []kube.VolumeMount{
					{Name: "shm", MountPath: "/dev/shm"},
					{Name: "shim", MountPath: shimDir, ReadOnly: true},
				},
			}},
			Volumes: []kube.Volume{
				{Name: "shm", EmptyDir: &kube.EmptyDirVolumeSource{Medium: "Memory", SizeLimit: "1Gi"}},
				{Name: "shim", EmptyDir: &kube.EmptyDirVolumeSource{}},
			},
		}}
	if err := h.Kube.Create(ctx, p); err != nil {
		return err
	}
	return h.Kube.WaitReady(ctx, name, 5*time.Minute)
}

// networkPolicy: no ingress at all; egress only to the engine's session mux
// (and the cluster DNS that names it).
func (h *K8sHost) networkPolicy(project string) kube.NetworkPolicy {
	key := envport.ProjectHostKey(project)
	engine := kube.NetworkPolicyPeer{PodSelector: &kube.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "apothecaryd"}}}
	if h.EngineNamespace != "" {
		engine.NamespaceSelector = kube.NamespaceSelector(h.EngineNamespace)
	}
	if h.EngineCIDR != "" {
		engine = kube.NetworkPolicyPeer{IPBlock: &kube.IPBlock{CIDR: h.EngineCIDR}}
	}
	port := 7071
	if h.Mux != nil {
		if _, p, err := net.SplitHostPort(h.Mux.Addr()); err == nil {
			fmt.Sscanf(p, "%d", &port)
		}
	}
	return kube.NetworkPolicy{TypeMeta: kube.NetworkPolicyType(),
		Metadata: kube.ObjectMeta{Name: podName(project), Labels: map[string]string{"apothecary.io/managed": "true", browserLabel: key}},
		Spec: kube.NetworkPolicySpec{
			PodSelector: kube.LabelSelector{MatchLabels: map[string]string{browserLabel: key}},
			PolicyTypes: []string{"Ingress", "Egress"},
			Egress: []kube.NetworkPolicyRule{
				{To: []kube.NetworkPolicyPeer{engine},
					Ports: []kube.NetworkPolicyPort{{Protocol: "TCP", Port: port}}},
				// The engine's Service name has to resolve.
				{To: []kube.NetworkPolicyPeer{{NamespaceSelector: kube.NamespaceSelector("kube-system"),
					PodSelector: &kube.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}}}},
					Ports: []kube.NetworkPolicyPort{{Protocol: "UDP", Port: 53}, {Protocol: "TCP", Port: 53}}},
			},
		}}
}

// Stop deletes the idle browser's pod (Ensure recreates it).
func (h *K8sHost) Stop(ctx context.Context, project string) error {
	return h.Kube.Delete(ctx, "pod", podName(project), false)
}

func (h *K8sHost) Running(ctx context.Context) ([]string, error) {
	var list struct {
		Items []kube.Pod `json:"items"`
	}
	if err := h.Kube.List(ctx, "pods", browserLabel, &list); err != nil {
		return nil, err
	}
	var out []string
	for _, p := range list.Items {
		if p.Status.Phase == "Running" {
			out = append(out, p.Metadata.Labels["apothecary.io/project"])
		}
	}
	return out, nil
}

func (h *K8sHost) Inspect(ctx context.Context, project string) (string, bool, error) {
	var pod kube.Pod
	found, err := h.Kube.Get(ctx, "pod", podName(project), &pod)
	if err != nil || !found || len(pod.Spec.Containers) == 0 {
		return "", false, err
	}
	return pod.Spec.Containers[0].Image, pod.Status.Phase == "Running", nil
}

// Listen registers the session on the engine's mux.
func (h *K8sHost) Listen(_ context.Context, _, _ string) (net.Listener, []string, error) {
	if h.Mux == nil {
		return nil, nil, fmt.Errorf("browser: no session listener (APOTHECARY_K8S_BROWSER_PROXY_LISTEN)")
	}
	ln, tok := h.Mux.Register()
	return ln, []string{"--engine", h.Mux.Addr(), "--token", tok}, nil
}

// Exec streams one `kubectl exec -i` into the browser pod.
func (h *K8sHost) Exec(ctx context.Context, project string, argv []string, stdin io.Reader, stdout io.Writer) error {
	args := append(h.Kube.Global(), kube.PodRunner{Pod: podName(project), Container: "browser"}.ExecArgs(true, nil, argv)...)
	cmd := exec.CommandContext(ctx, kubectlBin(h.Kube), args...)
	cmd.Stdin, cmd.Stdout = stdin, stdout
	errb := &tailBuffer{max: 4 << 10}
	cmd.Stderr = errb
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil && !strings.Contains(err.Error(), "WaitDelay") {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func kubectlBin(c *kube.Client) string {
	if c.Bin != "" {
		return c.Bin
	}
	return "kubectl"
}

var _ Host = (*K8sHost)(nil)
