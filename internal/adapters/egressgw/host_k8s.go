package egressgw

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"apothecary/internal/adapters/egressgw/policyfile"
	"apothecary/internal/adapters/kube"
	"apothecary/internal/platform/telemetry"
	"apothecary/internal/ports/envport"
)

// K8sHost runs each project's gateway as a Deployment on a Kubernetes cluster.
// A pod has no docker network to be multi-homed onto, so each run's sandbox
// pod reaches its gateway over its own WireGuard peer: the pod's default route
// is the tunnel, its network policy lets it send nothing but WireGuard to the
// gateway (and HTTP to the engine), and the gateway judges what comes out of
// wg0 with the same filter a docker run network gets. Every peer is allowed
// exactly its own /32, so attribution is the same lookup — and unforgeable.
type K8sHost struct {
	Kube            *kube.Client
	Image           string   // the gateway image (cluster-pullable)
	PullPolicy      string   // "" = the cluster default
	ProtectCIDRs    []string // never reachable through a gateway
	EngineNamespace string   // where the engine's pods are (network policy)
	// Forward selects how the engine reaches a gateway's control API: false =
	// its Service (engine in the cluster), true = kubectl port-forward.
	Forward      bool
	StorageSize  string // gateway PVC (caches); default 20Gi
	StorageClass string

	client *http.Client
	mu     sync.Mutex
	fwd    map[string]*kube.Forward
}

const (
	wgNet       = "10.90.0.0/16"
	wgGatewayIP = "10.90.0.1"
	wgPort      = 51820
	// The labels the gateway's and runs' objects carry.
	labelManaged = "apothecary.io/managed"
	labelGateway = "apothecary.io/gateway"
	labelProject = "apothecary.io/project"
	labelRun     = "apothecary.io/run"
	labelSandbox = "apothecary.io/sandbox"
	labelWGFor   = "apothecary.io/wg-project"
	// EngineAppLabel is the label the engine's pods carry (Helm chart).
	EngineAppLabel = "app.kubernetes.io/name"
	EngineAppValue = "apothecaryd"
)

func (h *K8sHost) httpClient() *http.Client {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client == nil {
		h.client = &http.Client{Transport: telemetry.HTTPTransport(http.DefaultTransport), Timeout: 3 * time.Second}
	}
	if h.fwd == nil {
		h.fwd = map[string]*kube.Forward{}
	}
	return h.client
}

// WGSecretName is the Secret holding a run's WireGuard identity.
func WGSecretName(run string) string { return "apo-wg-" + run }

func gwLabels(project string) map[string]string {
	key := envport.ProjectHostKey(project)
	return map[string]string{labelManaged: "true", labelGateway: key, labelProject: key}
}

// newKeypair is a WireGuard keypair, base64 as `wg genkey | wg pubkey` prints.
func newKeypair() (priv, pub string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(k.Bytes()), base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// gatewaySecret returns the project gateway's identity, creating it once.
func (h *K8sHost) gatewaySecret(ctx context.Context, project string) (map[string]string, error) {
	name := gwName(project)
	var sec kube.Secret
	found, err := h.Kube.Get(ctx, "secret", name, &sec)
	if err != nil {
		return nil, err
	}
	if found && len(sec.Data["wg-private-key"]) > 0 && len(sec.Data["control-token"]) > 0 {
		out := map[string]string{}
		for k, v := range sec.Data {
			out[k] = string(v)
		}
		return out, nil
	}
	priv, pub, err := newKeypair()
	if err != nil {
		return nil, err
	}
	data := map[string]string{"wg-private-key": priv, "wg-public-key": pub, "control-token": randomHex(32)}
	if err := h.Kube.Apply(ctx, kube.Secret{TypeMeta: kube.SecretType(),
		Metadata: kube.ObjectMeta{Name: name, Labels: gwLabels(project)}, StringData: data}); err != nil {
		return nil, err
	}
	return data, nil
}

// EnsureGateway applies the project's gateway (Secret, PVC, Deployment,
// Service, NetworkPolicy), waits for its rollout and control API.
func (h *K8sHost) EnsureGateway(ctx context.Context, project, projectName string) (Control, error) {
	name := gwName(project)
	sec, err := h.gatewaySecret(ctx, project)
	if err != nil {
		return Control{}, err
	}
	labels := gwLabels(project)
	size := h.StorageSize
	if size == "" {
		size = "20Gi"
	}
	pvc := kube.PersistentVolumeClaim{TypeMeta: kube.PVCType(),
		Metadata: kube.ObjectMeta{Name: name, Labels: labels},
		Spec: kube.PVCSpec{AccessModes: []string{"ReadWriteOnce"},
			Resources: kube.ResourceRequirements{Requests: map[string]string{"storage": size}}}}
	if h.StorageClass != "" {
		pvc.Spec.StorageClassName = kube.Ptr(h.StorageClass)
	}
	if err := h.Kube.Create(ctx, pvc); err != nil {
		return Control{}, err
	}
	annotations := map[string]string{}
	if projectName != "" {
		annotations["apothecary.io/project-name"] = projectName
	}
	for _, obj := range []any{h.deployment(project, labels, annotations), h.service(project, labels), h.networkPolicy(project, labels)} {
		if err := h.Kube.Apply(ctx, obj); err != nil {
			return Control{}, err
		}
	}
	if err := h.Kube.RolloutStatus(ctx, name, 3*time.Minute); err != nil {
		return Control{}, fmt.Errorf("egressgw: gateway %s did not roll out: %w", name, err)
	}
	c, err := h.control(ctx, project, sec["control-token"])
	if err != nil {
		return Control{}, err
	}
	if err := WaitHealthy(ctx, h.httpClient(), c.BaseURL, time.Minute); err != nil {
		return Control{}, err
	}
	return c, nil
}

func (h *K8sHost) deployment(project string, labels, annotations map[string]string) kube.Deployment {
	name := gwName(project)
	args := []string{
		"--wg-key", "/secrets/wg-private-key",
		"--wg-listen", strconv.Itoa(wgPort),
		"--wg-addr", wgGatewayIP + "/16",
		"--control-listen", ":" + controlPort,
		"--control-token-file", "/secrets/control-token",
		"--state-dir", "/state",
		"--mirror", "--buildcache", "--buildcache-dir", buildCacheDir, "--gallery",
		"--listen-port", "9040",
		"--dns-upstream", "resolv.conf",
	}
	if len(h.ProtectCIDRs) > 0 {
		args = append(args, "--protect-cidrs", strings.Join(h.ProtectCIDRs, ","))
	}
	mode := int32(0o400)
	podLabels := map[string]string{}
	for k, v := range labels {
		podLabels[k] = v
	}
	return kube.Deployment{TypeMeta: kube.DeploymentType(),
		Metadata: kube.ObjectMeta{Name: name, Labels: labels, Annotations: annotations},
		Spec: kube.DeploymentSpec{
			Replicas: kube.Ptr(int32(1)),
			// Recreate: the PVC is ReadWriteOnce, and two gateways would race
			// for the same peers anyway.
			Strategy: &kube.DeploymentStrategy{Type: "Recreate"},
			Selector: kube.LabelSelector{MatchLabels: map[string]string{labelGateway: envport.ProjectHostKey(project)}},
			Template: kube.PodTemplateSpec{
				Metadata: kube.ObjectMeta{Labels: podLabels},
				Spec: kube.PodSpec{
					AutomountServiceAccountToken: kube.Ptr(false),
					EnableServiceLinks:           kube.Ptr(false),
					InitContainers: []kube.Container{{
						// Forwarding sysctls are "unsafe" for securityContext.sysctls
						// (a kubelet allowlist would be needed), so a short
						// privileged step sets them in the pod's own netns.
						Name: "sysctl", Image: h.Image, ImagePullPolicy: h.PullPolicy,
						Command:         []string{"sh", "-c", "sysctl -w net.ipv4.ip_forward=1 net.ipv4.conf.all.rp_filter=0 net.ipv4.conf.default.rp_filter=0 net.ipv6.conf.all.disable_ipv6=1 && (modprobe wireguard 2>/dev/null || true)"},
						SecurityContext: &kube.SecurityContext{Privileged: kube.Ptr(true)},
					}},
					Containers: []kube.Container{{
						Name: "gateway", Image: h.Image, ImagePullPolicy: h.PullPolicy, Args: args,
						SecurityContext: &kube.SecurityContext{Capabilities: &kube.Capabilities{Add: []string{"NET_ADMIN", "NET_RAW"}}},
						Ports: []kube.ContainerPort{
							{Name: "wg", ContainerPort: wgPort, Protocol: "UDP"},
							{Name: "control", ContainerPort: 9041},
						},
						ReadinessProbe: &kube.Probe{HTTPGet: &kube.HTTPGetAction{Path: "/healthz", Port: 9041}, PeriodSeconds: 5},
						VolumeMounts: []kube.VolumeMount{
							{Name: "data", MountPath: "/var/lib/registry", SubPath: "registry"},
							{Name: "data", MountPath: buildCacheDir, SubPath: "buildcache"},
							{Name: "data", MountPath: "/state", SubPath: "state"},
							{Name: "secrets", MountPath: "/secrets", ReadOnly: true},
						},
					}},
					Volumes: []kube.Volume{
						{Name: "data", PersistentVolumeClaim: &kube.PersistentVolumeClaimVolumeSource{ClaimName: name}},
						{Name: "secrets", Secret: &kube.SecretVolumeSource{SecretName: name, DefaultMode: &mode}},
					},
				},
			},
		},
	}
}

func (h *K8sHost) service(project string, labels map[string]string) kube.Service {
	return kube.Service{TypeMeta: kube.ServiceType(),
		Metadata: kube.ObjectMeta{Name: gwName(project), Labels: labels},
		Spec: kube.ServiceSpec{
			Selector: map[string]string{labelGateway: envport.ProjectHostKey(project)},
			Ports: []kube.ServicePort{
				{Name: "wg", Port: wgPort, Protocol: "UDP"},
				{Name: "control", Port: 9041, Protocol: "TCP"},
			},
		}}
}

// enginePeer selects the engine's pods.
func (h *K8sHost) enginePeer() kube.NetworkPolicyPeer {
	p := kube.NetworkPolicyPeer{PodSelector: &kube.LabelSelector{MatchLabels: map[string]string{EngineAppLabel: EngineAppValue}}}
	if h.EngineNamespace != "" {
		p.NamespaceSelector = kube.NamespaceSelector(h.EngineNamespace)
	}
	return p
}

// networkPolicy: only the project's sandboxes may reach the WireGuard port,
// only the engine the control API; the gateway itself may reach the cluster
// DNS and anything outside the protected ranges.
func (h *K8sHost) networkPolicy(project string, labels map[string]string) kube.NetworkPolicy {
	key := envport.ProjectHostKey(project)
	return kube.NetworkPolicy{TypeMeta: kube.NetworkPolicyType(),
		Metadata: kube.ObjectMeta{Name: gwName(project), Labels: labels},
		Spec: kube.NetworkPolicySpec{
			PodSelector: kube.LabelSelector{MatchLabels: map[string]string{labelGateway: key}},
			PolicyTypes: []string{"Ingress", "Egress"},
			Ingress: []kube.NetworkPolicyRule{
				{From: []kube.NetworkPolicyPeer{{PodSelector: &kube.LabelSelector{MatchLabels: map[string]string{labelProject: key, labelSandbox: "true"}}}},
					Ports: []kube.NetworkPolicyPort{{Protocol: "UDP", Port: wgPort}}},
				{From: []kube.NetworkPolicyPeer{h.enginePeer()},
					Ports: []kube.NetworkPolicyPort{{Protocol: "TCP", Port: 9041}}},
			},
			Egress: []kube.NetworkPolicyRule{
				{To: []kube.NetworkPolicyPeer{{NamespaceSelector: kube.NamespaceSelector("kube-system"),
					PodSelector: &kube.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}}}},
					Ports: []kube.NetworkPolicyPort{{Protocol: "UDP", Port: 53}, {Protocol: "TCP", Port: 53}}},
				{To: []kube.NetworkPolicyPeer{{IPBlock: &kube.IPBlock{CIDR: "0.0.0.0/0", Except: h.ProtectCIDRs}}}},
			},
		}}
}

// control is the gateway's API address: its Service, or a port-forward.
func (h *K8sHost) control(ctx context.Context, project, token string) (Control, error) {
	h.httpClient()
	if !h.Forward {
		return Control{BaseURL: fmt.Sprintf("http://%s.%s.svc:%s", gwName(project), h.Kube.Namespace, controlPort), Token: token}, nil
	}
	h.mu.Lock()
	f := h.fwd[project]
	h.mu.Unlock()
	if f == nil {
		var err error
		if f, err = h.Kube.Forward(ctx, "svc/"+gwName(project), 9041); err != nil {
			return Control{}, err
		}
		h.mu.Lock()
		if old := h.fwd[project]; old != nil {
			f.Close()
			f = old
		} else {
			h.fwd[project] = f
		}
		h.mu.Unlock()
	}
	return Control{BaseURL: "http://" + f.Addr(), Token: token}, nil
}

// Control finds a running gateway's API without creating anything.
func (h *K8sHost) Control(ctx context.Context, project string) (Control, bool) {
	var sec kube.Secret
	if found, err := h.Kube.Get(ctx, "secret", gwName(project), &sec); err != nil || !found {
		return Control{}, false
	}
	var d kube.Deployment
	if found, err := h.Kube.Get(ctx, "deployment", gwName(project), &d); err != nil || !found {
		return Control{}, false
	}
	c, err := h.control(ctx, project, string(sec.Data["control-token"]))
	if err != nil {
		return Control{}, false
	}
	return c, true
}

// AttachRun gives the run its WireGuard identity: a keypair and the lowest
// free address in 10.90/16 (both kept across re-provisions of the same run),
// in a Secret its sandbox pod mounts.
func (h *K8sHost) AttachRun(ctx context.Context, project, projectName, run string) (Attachment, error) {
	gw, err := h.gatewaySecret(ctx, project)
	if err != nil {
		return Attachment{}, err
	}
	var svc kube.Service
	if found, err := h.Kube.Get(ctx, "service", gwName(project), &svc); err != nil || !found || svc.Spec.ClusterIP == "" {
		return Attachment{}, fmt.Errorf("egressgw: gateway service %s has no cluster IP (found=%v): %v", gwName(project), found, err)
	}
	key := envport.ProjectHostKey(project)
	var list struct {
		Items []kube.Secret `json:"items"`
	}
	if err := h.Kube.List(ctx, "secrets", labelWGFor+"="+key, &list); err != nil {
		return Attachment{}, err
	}
	used := map[string]bool{wgGatewayIP: true}
	var mine *kube.Secret
	for i, s := range list.Items {
		if s.Metadata.Name == WGSecretName(run) {
			mine = &list.Items[i]
		}
		used[string(s.Data["wg-ip"])] = true
	}
	var priv, pub, ip string
	if mine != nil && len(mine.Data["wg-private-key"]) > 0 {
		priv, pub, ip = string(mine.Data["wg-private-key"]), string(mine.Data["wg-public-key"]), string(mine.Data["wg-ip"])
	} else {
		if priv, pub, err = newKeypair(); err != nil {
			return Attachment{}, err
		}
		if ip, err = freeIP(used); err != nil {
			return Attachment{}, err
		}
	}
	labels := map[string]string{labelManaged: "true", labelProject: key, labelRun: run, labelWGFor: key}
	if err := h.Kube.Apply(ctx, kube.Secret{TypeMeta: kube.SecretType(),
		Metadata: kube.ObjectMeta{Name: WGSecretName(run), Labels: labels},
		StringData: map[string]string{
			"wg-private-key": priv, "wg-public-key": pub, "wg-ip": ip,
			"gw-public-key": gw["wg-public-key"],
		}}); err != nil {
		return Attachment{}, err
	}
	return Attachment{
		GatewayIP:  wgGatewayIP,
		Subnet:     ip + "/32",
		Peer:       &policyfile.Peer{PublicKey: pub, IP: ip},
		WGEndpoint: net.JoinHostPort(svc.Spec.ClusterIP, strconv.Itoa(wgPort)),
		WGSecret:   WGSecretName(run),
	}, nil
}

// freeIP is the lowest unused address in 10.90/16 after the gateway's.
func freeIP(used map[string]bool) (string, error) {
	for n := 2; n < 65535; n++ {
		ip := fmt.Sprintf("10.90.%d.%d", n/256, n%256)
		if n%256 == 0 || n%256 == 255 || used[ip] {
			continue
		}
		return ip, nil
	}
	return "", fmt.Errorf("egressgw: no free WireGuard address in %s", wgNet)
}

// DetachRun deletes the run's WireGuard identity.
func (h *K8sHost) DetachRun(ctx context.Context, project, run string) error {
	return h.Kube.Delete(ctx, "secret", WGSecretName(run), false)
}

// Close stops port-forwards.
func (h *K8sHost) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	keys := make([]string, 0, len(h.fwd))
	for k := range h.fwd {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h.fwd[k].Close()
		delete(h.fwd, k)
	}
}

var _ Host = (*K8sHost)(nil)
