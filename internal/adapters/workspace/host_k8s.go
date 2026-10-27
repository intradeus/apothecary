package workspace

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"apothecary/internal/egressgw"
	"apothecary/internal/kube"
	"apothecary/internal/ports"
	wsassets "apothecary/workspace"
)

// KubeConfig runs sandboxes on a Kubernetes cluster.
type KubeConfig struct {
	// Kube is kubectl aimed at the namespace sandboxes, gateways and their
	// secrets live in.
	Kube *kube.Client
	// Images (cluster-pullable): the sandbox (docker:dind + git + node +
	// @devcontainers/cli + WireGuard), the shim and the egress gateway.
	SandboxImage, ShimImage, GatewayImage string
	PullPolicy                            string // "" = the cluster default
	// EngineURL is how a workspace reaches the engine (its Service, as a
	// FQDN so it resolves from any container in the sandbox).
	EngineURL string
	// EngineNamespace selects the engine's pods in network policies;
	// EngineCIDR replaces that selector when the engine runs outside the
	// cluster (its address as the cluster sees it).
	EngineNamespace, EngineCIDR string
	// Forward reaches pods through kubectl port-forward (an engine outside
	// the cluster) instead of directly by pod IP.
	Forward bool
	// Storage: "pvc" (default; survives a pod restart) or "emptydir".
	Storage, StorageClass, StorageSize string
	// Privileged sandboxes (default) — a nested dockerd needs it under runc.
	// Off requires RuntimeClass (e.g. sysbox-runc installed on the nodes).
	Privileged   bool
	RuntimeClass string
	// Firewall routes every sandbox through its project's egress gateway.
	Firewall     bool
	ProtectCIDRs []string
	// ClusterDomain for the sandbox's DNS search path (default cluster.local).
	ClusterDomain string
}

// k8sHost runs each run's sandbox as a Pod. The pod mounts its volume at the
// run folder's own path (<RunsDir>/<name>), so every path the adapter computes
// — the checkout, the shim dir, the devcontainer CLI's bind sources — is the
// same string on both hosts; only which filesystem it names differs. The
// engine reaches into the pod with `kubectl exec` (commands, files) and the
// shim's dial-in mux (the sidecar and exposed ports), and the workspace
// reaches the engine at its Service.
type k8sHost struct {
	a   *Adapter
	cfg KubeConfig
	gwh *egressgw.K8sHost

	mu     sync.Mutex
	tokens map[string]string        // run → relay token
	podIPs map[string]string        // run → pod IP (direct dialing)
	fwds   map[string]*kube.Forward // run → port-forward (out-of-cluster)
	stats  map[string]cgroupSample  // run → previous sample (CPU% deltas)
}

// relayPort is the dial-in mux's port in every sandbox pod (a var for tests).
var relayPort = 7070

const (
	sandboxContainer = "sandbox"

	// k8sLabel* are the labels sandbox objects carry; the gateway's network
	// policy selects a project's sandboxes by them.
	k8sLabelManaged = "apothecary.io/managed"
	k8sLabelRun     = "apothecary.io/run"
	k8sLabelSandbox = "apothecary.io/sandbox"
	k8sLabelProject = "apothecary.io/project"
	k8sLabelGateway = "apothecary.io/gateway"
)

func newK8sHost(a *Adapter, cfg *KubeConfig) *k8sHost {
	c := *cfg
	if c.Storage == "" {
		c.Storage = "pvc"
	}
	if c.StorageSize == "" {
		c.StorageSize = "20Gi"
	}
	if c.ClusterDomain == "" {
		c.ClusterDomain = "cluster.local"
	}
	h := &k8sHost{a: a, cfg: c,
		tokens: map[string]string{}, podIPs: map[string]string{},
		fwds: map[string]*kube.Forward{}, stats: map[string]cgroupSample{}}
	if c.Firewall {
		h.gwh = &egressgw.K8sHost{Kube: c.Kube, Image: c.GatewayImage, PullPolicy: c.PullPolicy, ProtectCIDRs: c.ProtectCIDRs,
			EngineNamespace: c.EngineNamespace, Forward: c.Forward, StorageClass: c.StorageClass}
	}
	return h
}

func (h *k8sHost) gatewayManager() *egressgw.Manager {
	if h.gwh == nil {
		return nil
	}
	return egressgw.NewManager(h.gwh)
}

func runSecretName(name string) string { return "apo-run-" + name }
func dataClaimName(name string) string { return name + "-data" }

func (h *k8sHost) podRunner(name string) kube.PodRunner {
	return kube.PodRunner{C: h.cfg.Kube, Pod: name, Container: sandboxContainer}
}

// runInner: every inner call is a `kubectl exec` into the sandbox container,
// where the devcontainer CLI, docker CLI and git run against the pod's own
// dockerd. Through runWith, so a kubectl transport failure is retried like a
// failed spawn.
func (h *k8sHost) runInner(ctx context.Context, name, bin string, args []string, stdin []byte, tee io.Writer, extraEnv ...string) ([]byte, []byte, int, error) {
	return h.a.runWith(ctx, h.podRunner(name), bin, args, stdin, tee, extraEnv...)
}

func (h *k8sHost) fs(name string) sandboxFS { return execFS{h: h, name: name} }

// --- prepare -----------------------------------------------------------------

func (h *k8sHost) prepare(ctx context.Context, name string, spec ports.EnvSpec, plog func(string, ...any), detect func(context.Context, string) (Definition, error)) (prep, error) {
	a := h.a
	folder := a.folderFor(name)
	if !a.noSandboxGuard && strings.HasPrefix(folder, "/tmp/") {
		return prep{}, fmt.Errorf("kubernetes sandbox: RunsDir %q is under /tmp — dind masks /tmp, so the run volume would be hidden; configure a persistent RunsDir", a.cfg.RunsDir)
	}
	// The engine keeps its own side of the run folder: logs and markers.
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return prep{}, err
	}
	var att egressgw.Attachment
	if a.gw != nil {
		plog("→ bringing up the project's egress gateway\n")
		var err error
		if att, err = a.gw.EnsureRunEgress(ctx, spec.Project, spec.ProjectName, name, policyFor(spec)); err != nil {
			return prep{}, err
		}
		a.recordProject(name, spec.Project)
	}
	plog("→ bringing up the sandbox pod\n")
	if err := h.ensurePod(ctx, name, spec, att); err != nil {
		return prep{}, err
	}
	src := a.srcDir(name)
	if err := h.ensureCheckout(ctx, name, src, spec, plog); err != nil {
		return prep{}, err
	}
	def, err := detect(ctx, src)
	if err != nil {
		return prep{}, err
	}
	if err := h.checkShim(ctx, name); err != nil {
		return prep{}, err
	}
	return prep{src: src, shimDir: filepath.Join(folder, "shim"), gwIP: att.GatewayIP, def: def}, nil
}

// relayToken is the run's dial-in token (Secret apo-run-<name>), created once.
func (h *k8sHost) relayToken(ctx context.Context, name string, create bool, project string) (string, error) {
	h.mu.Lock()
	if t := h.tokens[name]; t != "" {
		h.mu.Unlock()
		return t, nil
	}
	h.mu.Unlock()
	var sec kube.Secret
	found, err := h.cfg.Kube.Get(ctx, "secret", runSecretName(name), &sec)
	if err != nil {
		return "", err
	}
	tok := string(sec.Data["relay-token"])
	if !found || tok == "" {
		if !create {
			return "", fmt.Errorf("sandbox %s has no relay token", name)
		}
		b := make([]byte, 24)
		_, _ = rand.Read(b)
		tok = hex.EncodeToString(b)
		if err := h.cfg.Kube.Apply(ctx, kube.Secret{TypeMeta: kube.SecretType(),
			Metadata:   kube.ObjectMeta{Name: runSecretName(name), Labels: h.labels(name, project)},
			StringData: map[string]string{"relay-token": tok}}); err != nil {
			return "", err
		}
	}
	h.mu.Lock()
	h.tokens[name] = tok
	h.mu.Unlock()
	return tok, nil
}

func (h *k8sHost) labels(name, project string) map[string]string {
	l := map[string]string{k8sLabelManaged: "true", k8sLabelRun: name}
	if project != "" {
		l[k8sLabelProject] = ports.ProjectHostKey(project)
	}
	return l
}

// ensurePod creates the run's claim, network policy and pod as needed and
// waits for the pod to be Ready (its dockerd answering). A pod that ended
// (Failed/Succeeded) is replaced — the claim, and with it the checkout and the
// inner daemon's images and containers, survives.
func (h *k8sHost) ensurePod(ctx context.Context, name string, spec ports.EnvSpec, att egressgw.Attachment) error {
	k := h.cfg.Kube
	if _, err := h.relayToken(ctx, name, true, spec.Project); err != nil {
		return err
	}
	if h.cfg.Storage == "pvc" {
		pvc := kube.PersistentVolumeClaim{TypeMeta: kube.PVCType(),
			Metadata: kube.ObjectMeta{Name: dataClaimName(name), Labels: h.labels(name, spec.Project)},
			Spec: kube.PVCSpec{AccessModes: []string{"ReadWriteOnce"},
				Resources: kube.ResourceRequirements{Requests: map[string]string{"storage": h.cfg.StorageSize}}}}
		if h.cfg.StorageClass != "" {
			pvc.Spec.StorageClassName = kube.Ptr(h.cfg.StorageClass)
		}
		if err := k.Create(ctx, pvc); err != nil {
			return err
		}
	}
	np, err := h.networkPolicy(name, spec.Project)
	if err != nil {
		return err
	}
	if err := k.Apply(ctx, np); err != nil {
		return err
	}
	var pod kube.Pod
	found, err := k.Get(ctx, "pod", name, &pod)
	if err != nil {
		return err
	}
	if found && (pod.Status.Phase == "Failed" || pod.Status.Phase == "Succeeded") {
		if err := k.Delete(ctx, "pod", name, true); err != nil {
			return err
		}
		found = false
	}
	if !found {
		p, err := h.podManifest(ctx, name, spec, att)
		if err != nil {
			return err
		}
		if err := k.Create(ctx, p); err != nil {
			return err
		}
	}
	h.forgetPod(name)
	if err := k.WaitReady(ctx, name, 5*time.Minute); err != nil {
		return fmt.Errorf("sandbox pod %s never became ready (inspect: kubectl -n %s describe pod %s): %w", name, k.Namespace, name, err)
	}
	return nil
}

// engineAddr is the engine's host and port from its workspace URL.
func (h *k8sHost) engineAddr() (string, int, error) {
	u, err := url.Parse(h.cfg.EngineURL)
	if err != nil || u.Hostname() == "" {
		return "", 0, fmt.Errorf("kubernetes: engine URL %q is not a URL", h.cfg.EngineURL)
	}
	port := 80
	if p := u.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}
	return u.Hostname(), port, nil
}

// networkPolicy: only the engine may dial the pod (its relay port); with the
// firewall on, the pod may send nothing but WireGuard to its gateway and HTTP
// to the engine — so the tunnel, and the gateway behind it, is the only way
// out.
func (h *k8sHost) networkPolicy(name, project string) (kube.NetworkPolicy, error) {
	_, enginePort, err := h.engineAddr()
	if err != nil {
		return kube.NetworkPolicy{}, err
	}
	engine := kube.NetworkPolicyPeer{PodSelector: &kube.LabelSelector{MatchLabels: map[string]string{egressgw.EngineAppLabel: egressgw.EngineAppValue}}}
	if h.cfg.EngineNamespace != "" {
		engine.NamespaceSelector = kube.NamespaceSelector(h.cfg.EngineNamespace)
	}
	if h.cfg.EngineCIDR != "" {
		engine = kube.NetworkPolicyPeer{IPBlock: &kube.IPBlock{CIDR: h.cfg.EngineCIDR}}
	}
	spec := kube.NetworkPolicySpec{
		PodSelector: kube.LabelSelector{MatchLabels: map[string]string{k8sLabelRun: name}},
		PolicyTypes: []string{"Ingress"},
		Ingress: []kube.NetworkPolicyRule{{From: []kube.NetworkPolicyPeer{engine},
			Ports: []kube.NetworkPolicyPort{{Protocol: "TCP", Port: relayPort}}}},
	}
	if h.cfg.Firewall {
		spec.PolicyTypes = append(spec.PolicyTypes, "Egress")
		spec.Egress = []kube.NetworkPolicyRule{
			{To: []kube.NetworkPolicyPeer{{PodSelector: &kube.LabelSelector{MatchLabels: map[string]string{k8sLabelGateway: ports.ProjectHostKey(project)}}}},
				Ports: []kube.NetworkPolicyPort{{Protocol: "UDP", Port: 51820}}},
			{To: []kube.NetworkPolicyPeer{engine},
				Ports: []kube.NetworkPolicyPort{{Protocol: "TCP", Port: enginePort}}},
		}
	}
	return kube.NetworkPolicy{TypeMeta: kube.NetworkPolicyType(),
		Metadata: kube.ObjectMeta{Name: runSecretName(name), Labels: h.labels(name, project)},
		Spec:     spec}, nil
}

// engineIP is the engine Service's address for the sandbox's /32 route: the
// pod's WireGuard set-up runs before it has any DNS, so the name is resolved
// here, by the engine.
func (h *k8sHost) engineIP(ctx context.Context) (string, error) {
	host, _, err := h.engineAddr()
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("kubernetes: resolve the engine's address %q for the sandbox route: %v", host, err)
	}
	return ips[0].String(), nil
}

func (h *k8sHost) podManifest(ctx context.Context, name string, spec ports.EnvSpec, att egressgw.Attachment) (kube.Pod, error) {
	folder := h.a.folderFor(name)
	labels := h.labels(name, spec.Project)
	labels[k8sLabelSandbox] = "true"
	annotations := map[string]string{}
	if spec.ProjectName != "" {
		annotations["apothecary.io/project-name"] = spec.ProjectName
	}
	secretMode := int32(0o400)
	runMount := kube.VolumeMount{Name: "data", SubPath: "run", MountPath: folder}
	privileged := func() *kube.SecurityContext {
		if h.cfg.RuntimeClass != "" && !h.cfg.Privileged {
			return nil
		}
		return &kube.SecurityContext{Privileged: kube.Ptr(true)}
	}
	sandbox := kube.Container{
		Name: sandboxContainer, Image: h.cfg.SandboxImage, ImagePullPolicy: h.cfg.PullPolicy,
		// apo-dockerd wraps the dind entrypoint: dockerd on the default socket,
		// a CGNAT address pool (no clash with the cluster's ranges), and —
		// with a gateway — its DNS, image mirror, build cache and the tunnel's
		// MTU.
		Command:         []string{"apo-dockerd"},
		Env:             []kube.EnvVar{{Name: "DOCKER_TLS_CERTDIR", Value: ""}},
		SecurityContext: privileged(),
		ReadinessProbe: &kube.Probe{Exec: &kube.ExecAction{Command: []string{"docker", "version"}},
			PeriodSeconds: 3, TimeoutSeconds: 5, FailureThreshold: 100},
		VolumeMounts: []kube.VolumeMount{
			{Name: "data", SubPath: "docker", MountPath: "/var/lib/docker"},
			runMount,
			{Name: "apo", MountPath: "/run/apo"},
		},
	}
	seed := kube.Container{Name: "shim-seed", Image: h.cfg.ShimImage, ImagePullPolicy: h.cfg.PullPolicy,
		Args: []string{"seed", filepath.Join(folder, "shim")}, VolumeMounts: []kube.VolumeMount{runMount}}
	relay := kube.Container{Name: "relay", Image: h.cfg.ShimImage, ImagePullPolicy: h.cfg.PullPolicy,
		RestartPolicy: "Always", // a native sidecar: up before the sandbox, restarted on its own
		Args: []string{"relay", "--serve", "0.0.0.0:" + strconv.Itoa(relayPort),
			"--dir", filepath.Join(folder, "shim"), "--token-file", "/secrets/run/relay-token"},
		Ports:        []kube.ContainerPort{{Name: "relay", ContainerPort: relayPort}},
		VolumeMounts: []kube.VolumeMount{runMount, {Name: "run-secret", MountPath: "/secrets/run", ReadOnly: true}}}
	pod := kube.Pod{TypeMeta: kube.PodType(),
		Metadata: kube.ObjectMeta{Name: name, Labels: labels, Annotations: annotations},
		Spec: kube.PodSpec{
			RestartPolicy:                 "Always",
			TerminationGracePeriodSeconds: kube.Ptr(int64(30)),
			AutomountServiceAccountToken:  kube.Ptr(false),
			EnableServiceLinks:            kube.Ptr(false),
			Hostname:                      name,
			InitContainers:                []kube.Container{seed},
			Containers:                    []kube.Container{sandbox},
			Volumes: []kube.Volume{
				{Name: "apo", EmptyDir: &kube.EmptyDirVolumeSource{}},
				{Name: "run-secret", Secret: &kube.SecretVolumeSource{SecretName: runSecretName(name), DefaultMode: &secretMode}},
			},
		}}
	if h.cfg.RuntimeClass != "" {
		pod.Spec.RuntimeClassName = h.cfg.RuntimeClass
	}
	if h.cfg.Storage == "pvc" {
		pod.Spec.Volumes = append(pod.Spec.Volumes, kube.Volume{Name: "data",
			PersistentVolumeClaim: &kube.PersistentVolumeClaimVolumeSource{ClaimName: dataClaimName(name)}})
	} else {
		pod.Spec.Volumes = append(pod.Spec.Volumes, kube.Volume{Name: "data", EmptyDir: &kube.EmptyDirVolumeSource{}})
	}
	if att.GatewayIP != "" {
		engineIP, err := h.engineIP(ctx)
		if err != nil {
			return kube.Pod{}, err
		}
		gwHost, _, _ := net.SplitHostPort(att.WGEndpoint)
		ns := h.cfg.Kube.Namespace
		pod.Spec.DNSPolicy = "None"
		pod.Spec.DNSConfig = &kube.PodDNSConfig{
			Nameservers: []string{att.GatewayIP},
			Searches:    []string{ns + ".svc." + h.cfg.ClusterDomain, "svc." + h.cfg.ClusterDomain, h.cfg.ClusterDomain},
			Options:     []kube.PodDNSConfigOpt{{Name: "ndots", Value: "2"}},
		}
		wgSec := kube.SecurityContext{Privileged: kube.Ptr(true)}
		if h.cfg.RuntimeClass != "" && !h.cfg.Privileged {
			wgSec = kube.SecurityContext{Capabilities: &kube.Capabilities{Add: []string{"NET_ADMIN"}}}
		}
		wg := kube.Container{Name: "wg", Image: h.cfg.SandboxImage, ImagePullPolicy: h.cfg.PullPolicy,
			RestartPolicy: "Always", // set up the tunnel, then watch it
			Command:       []string{"apo-wg-up"},
			Env: []kube.EnvVar{
				{Name: "GW_ENDPOINT", Value: att.WGEndpoint},
				{Name: "GW_SVC_IP", Value: gwHost},
				{Name: "ENGINE_IP", Value: engineIP},
			},
			SecurityContext: &wgSec,
			StartupProbe: &kube.Probe{Exec: &kube.ExecAction{Command: []string{"test", "-f", "/run/apo/wg-ready"}},
				PeriodSeconds: 1, FailureThreshold: 60},
			VolumeMounts: []kube.VolumeMount{
				{Name: "wg-secret", MountPath: "/secrets/wg", ReadOnly: true},
				{Name: "apo", MountPath: "/run/apo"},
			}}
		pod.Spec.InitContainers = append(pod.Spec.InitContainers, wg)
		pod.Spec.Volumes = append(pod.Spec.Volumes, kube.Volume{Name: "wg-secret",
			Secret: &kube.SecretVolumeSource{SecretName: att.WGSecret, DefaultMode: &secretMode}})
		pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, kube.EnvVar{Name: "APO_GATEWAY_IP", Value: att.GatewayIP})
	}
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, relay)
	return pod, nil
}

// ensureCheckout clones the repo inside the pod, once. The credential rides
// stdin (never argv, never the environment) into a 0600 file the clone uses
// and removes.
func (h *k8sHost) ensureCheckout(ctx context.Context, name, src string, spec ports.EnvSpec, plog func(string, ...any)) error {
	if _, _, code, err := h.runInner(ctx, name, "test", []string{"-d", filepath.Join(src, ".git")}, nil, nil); err == nil && code == 0 {
		return nil
	}
	cred := ""
	if spec.GitPass != "" {
		line, err := credentialLine(spec.RepoURL, spec.GitUser, spec.GitPass)
		if err != nil {
			return err
		}
		cred = line
	}
	// One line of script, then the credential: bash reads the script line,
	// `read` takes the next.
	script := fmt.Sprintf(`IFS= read -r CRED; mkdir -p %[1]s; f=%[1]s/git-credentials; umask 077; `+
		`if [ -n "$CRED" ]; then printf '%%s\n' "$CRED" > "$f"; set -- -c "credential.helper=store --file=$f"; else set --; fi; `+
		`git "$@" clone -- %[2]s %[3]s; rc=$?; rm -f "$f"; exit $rc`,
		shellQuote(filepath.Dir(src)), shellQuote(spec.RepoURL), shellQuote(src))
	stdin := []byte(script + "\n" + cred + "\n")
	_, stderr, code, err := h.runInner(ctx, name, "bash", []string{"-s"}, stdin, nil)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("clone %s: exit %d: %s", spec.RepoURL, code, tail(string(stderr), 2048))
	}
	return nil
}

// checkShim compares the seeded shim with the one this engine expects.
func (h *k8sHost) checkShim(ctx context.Context, name string) error {
	b, err := h.fs(name).ReadFile(ctx, filepath.Join(h.a.folderFor(name), "shim", "shim.hash"))
	if err != nil {
		return fmt.Errorf("sandbox %s: no seeded shim (%v) — is %s an apothecary shim image?", name, err, h.cfg.ShimImage)
	}
	got, want := strings.TrimSpace(string(b)), wsassets.ShimSourceHash()
	if got != want {
		return fmt.Errorf("sandbox %s: shim image %s was built from shim sources %s, this engine expects %s — publish the shim image of this engine's version", name, h.cfg.ShimImage, got, want)
	}
	return nil
}

// --- state / teardown ----------------------------------------------------------

func (h *k8sHost) sandboxState(ctx context.Context, name string) (string, bool, error) {
	var pod kube.Pod
	found, err := h.cfg.Kube.Get(ctx, "pod", name, &pod)
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, nil
	}
	switch pod.Status.Phase {
	case "Running":
		if pod.Status.Ready() {
			return "running", false, nil
		}
		return "created", false, nil
	case "Pending":
		return "created", false, nil
	default: // Succeeded, Failed, Unknown
		return "exited", false, nil
	}
}

func (h *k8sHost) destroySandbox(ctx context.Context, name string) error {
	k := h.cfg.Kube
	if err := k.Delete(ctx, "pod", name, true); err != nil {
		return err
	}
	for _, obj := range [][2]string{{"persistentvolumeclaim", dataClaimName(name)}, {"networkpolicy", runSecretName(name)}, {"secret", runSecretName(name)}} {
		if err := k.Delete(ctx, obj[0], obj[1], false); err != nil {
			return err
		}
	}
	h.forgetPod(name)
	h.mu.Lock()
	delete(h.tokens, name)
	delete(h.stats, name)
	if f := h.fwds[name]; f != nil {
		f.Close()
		delete(h.fwds, name)
	}
	h.mu.Unlock()
	return nil
}

// --- the engine bridge: a Service, no relay ------------------------------------

func (h *k8sHost) ensureBridge(ports.EnvID) error                         { return nil }
func (h *k8sHost) startBridgeRelay(context.Context, string, string) error { return nil }
func (h *k8sHost) closeBridge(ports.EnvID)                                {}
func (h *k8sHost) bridgePorts() []int                                     { return nil }
func (h *k8sHost) engineURL() string                                      { return h.cfg.EngineURL }

// --- dialing in ----------------------------------------------------------------

func (h *k8sHost) dial(name, sock string) ports.PortConn {
	return podPortConn{h: h, name: name, sock: sock}
}

// podPortConn reaches a socket in the run's shim dir through the pod's dial-in
// mux: by pod IP from inside the cluster, through a port-forward from outside.
type podPortConn struct {
	h          *k8sHost
	name, sock string
}

func (p podPortConn) Dial(ctx context.Context) (net.Conn, error) {
	c, err := p.h.dialMux(ctx, p.name, p.sock)
	if err != nil {
		// A replaced pod has a new IP (or a dead forward): look again once.
		p.h.forgetPod(p.name)
		c, err = p.h.dialMux(ctx, p.name, p.sock)
	}
	return c, err
}

func (p podPortConn) Close() error { return nil }

func (h *k8sHost) dialMux(ctx context.Context, name, sock string) (net.Conn, error) {
	tok, err := h.relayToken(ctx, name, false, "")
	if err != nil {
		return nil, err
	}
	addr, err := h.muxAddr(ctx, name)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	} else {
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	}
	if _, err := fmt.Fprintf(c, "APO1 %s %s\n", tok, sock); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReaderSize(c, 64)
	ack, err := br.ReadString('\n')
	if err != nil || ack != "OK\n" {
		c.Close()
		return nil, fmt.Errorf("sandbox %s: %s is not being served", name, sock)
	}
	_ = c.SetDeadline(time.Time{})
	return &readerConn{Conn: c, r: br}, nil
}

// readerConn reads what the ack reader buffered first.
type readerConn struct {
	net.Conn
	r *bufio.Reader
}

func (r *readerConn) Read(p []byte) (int, error) { return r.r.Read(p) }

// CloseWrite keeps half-close working through the wrapper.
func (r *readerConn) CloseWrite() error {
	if cw, ok := r.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (h *k8sHost) muxAddr(ctx context.Context, name string) (string, error) {
	if h.cfg.Forward {
		h.mu.Lock()
		f := h.fwds[name]
		h.mu.Unlock()
		if f == nil {
			var err error
			if f, err = h.cfg.Kube.Forward(ctx, "pod/"+name, relayPort); err != nil {
				return "", err
			}
			h.mu.Lock()
			if old := h.fwds[name]; old != nil {
				f.Close()
				f = old
			} else {
				h.fwds[name] = f
			}
			h.mu.Unlock()
		}
		return f.Addr(), nil
	}
	h.mu.Lock()
	ip := h.podIPs[name]
	h.mu.Unlock()
	if ip == "" {
		var pod kube.Pod
		found, err := h.cfg.Kube.Get(ctx, "pod", name, &pod)
		if err != nil {
			return "", err
		}
		if !found || pod.Status.PodIP == "" {
			return "", fmt.Errorf("sandbox pod %s has no address", name)
		}
		ip = pod.Status.PodIP
		h.mu.Lock()
		h.podIPs[name] = ip
		h.mu.Unlock()
	}
	return net.JoinHostPort(ip, strconv.Itoa(relayPort)), nil
}

func (h *k8sHost) forgetPod(name string) {
	h.mu.Lock()
	delete(h.podIPs, name)
	h.mu.Unlock()
}

func (h *k8sHost) waitSock(ctx context.Context, name, sock string) error {
	path := filepath.Join(h.a.folderFor(name), "shim", sock)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, _, code, err := h.runInner(ctx, name, "test", []string{"-S", path}, nil, nil); err == nil && code == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s never appeared", sock)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// refreshShim: the shim-seed init container refreshes the shim whenever the
// pod starts; a running pod keeps the one it started with. A mismatch is
// reported, not fatal — the workspace keeps working, and the next pod start
// picks up the engine's shim image.
func (h *k8sHost) refreshShim(ctx context.Context, name string) error {
	if err := h.checkShim(ctx, name); err != nil {
		slog.Warn("kubernetes: workspace runs an older shim until its pod restarts", "env", name, "err", err)
	}
	return nil
}

func (h *k8sHost) terminalCommand(name, container string) string {
	k := h.cfg.Kube
	ctxFlag := ""
	if k.Context != "" {
		ctxFlag = " --context " + k.Context
	}
	return fmt.Sprintf("kubectl%s -n %s exec -it %s -c %s -- docker exec -it %s bash", ctxFlag, k.Namespace, name, sandboxContainer, container)
}

// --- resource sampling ----------------------------------------------------------

// cgroupSample is one reading of the sandbox container's cgroup: the
// container holds the inner dockerd and, nested under it, every workspace
// and compose sibling — the run's whole footprint.
type cgroupSample struct {
	at       time.Time
	usageUS  int64
	memUsed  int64
	memLimit int64
	pids     int
	cpus     int
	rx, tx   int64
}

const sampleScript = `cd /sys/fs/cgroup 2>/dev/null || exit 3
echo "cpu $(awk '$1=="usage_usec"{print $2}' cpu.stat)"
echo "mem $(cat memory.current)"
echo "max $(cat memory.max)"
echo "pids $(cat pids.current 2>/dev/null || echo 0)"
echo "quota $(cat cpu.max 2>/dev/null)"
echo "nproc $(nproc)"
awk -F'[: ]+' '$2=="eth0"{print "net "$3" "$11}' /proc/net/dev`

func parseCgroupSample(out string) (cgroupSample, error) {
	s := cgroupSample{}
	var quota, period int64
	seen := 0
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n := func(i int) int64 { v, _ := strconv.ParseInt(f[i], 10, 64); return v }
		switch f[0] {
		case "cpu":
			s.usageUS = n(1)
			seen++
		case "mem":
			s.memUsed = n(1)
		case "max":
			if f[1] != "max" {
				s.memLimit = n(1)
			}
		case "pids":
			s.pids = int(n(1))
		case "quota":
			if f[1] != "max" && len(f) >= 3 {
				quota, period = n(1), n(2)
			}
		case "nproc":
			s.cpus = int(n(1))
		case "net":
			if len(f) >= 3 {
				s.rx, s.tx = n(1), n(2)
			}
		}
	}
	if seen == 0 {
		return s, errors.New("no cpu.stat usage")
	}
	if quota > 0 && period > 0 {
		if c := int((quota + period - 1) / period); c > 0 && (s.cpus == 0 || c < s.cpus) {
			s.cpus = c
		}
	}
	return s, nil
}

func (h *k8sHost) sample(ctx context.Context, name string) (ports.ResourceSample, error) {
	out, _, code, err := h.podRunner(name).Run(ctx, "sh", []string{"-c", sampleScript}, nil, nil)
	if err != nil {
		return ports.ResourceSample{}, err
	}
	if code != 0 {
		return ports.ResourceSample{}, fmt.Errorf("sample %s: exit %d", name, code)
	}
	cur, err := parseCgroupSample(string(out))
	if err != nil {
		return ports.ResourceSample{}, fmt.Errorf("sample %s: %w", name, err)
	}
	cur.at = time.Now()
	h.mu.Lock()
	prev, ok := h.stats[name]
	h.stats[name] = cur
	h.mu.Unlock()
	if !ok || cur.usageUS < prev.usageUS {
		return ports.ResourceSample{}, fmt.Errorf("sample %s: first reading (CPU needs two)", name)
	}
	return cpuSample(prev, cur), nil
}

// cpuSample is docker-stats-shaped: one core = 100%.
func cpuSample(prev, cur cgroupSample) ports.ResourceSample {
	wall := cur.at.Sub(prev.at).Microseconds()
	cpu := 0.0
	if wall > 0 {
		cpu = float64(cur.usageUS-prev.usageUS) / float64(wall) * 100
	}
	return ports.ResourceSample{CPUPerc: cpu, CPUs: cur.cpus, MemUsed: cur.memUsed, MemLimit: cur.memLimit,
		NetRx: cur.rx, NetTx: cur.tx, PIDs: cur.pids}
}

// --- test / close ------------------------------------------------------------

func (h *k8sHost) test(ctx context.Context) error {
	k := h.cfg.Kube
	if _, err := k.Version(ctx); err != nil {
		return fmt.Errorf("kubernetes cluster unreachable: %w", err)
	}
	need := []string{"pods", "pods/exec", "secrets", "persistentvolumeclaims", "networkpolicies"}
	if h.cfg.Forward {
		need = append(need, "pods/portforward")
	}
	if h.cfg.Firewall {
		need = append(need, "deployments", "services")
	}
	var missing []string
	for _, r := range need {
		ok, err := k.CanI(ctx, "create", r)
		if err != nil {
			return err
		}
		if !ok {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("kubernetes: may not create %s in namespace %s (see the chart's RBAC)", strings.Join(missing, ", "), k.Namespace)
	}
	if h.cfg.SandboxImage == "" || h.cfg.ShimImage == "" || (h.cfg.Firewall && h.cfg.GatewayImage == "") {
		return errors.New("kubernetes: sandbox, shim and gateway images must be set")
	}
	if _, _, err := h.engineAddr(); err != nil {
		return err
	}
	return nil
}

func (h *k8sHost) close() {
	h.mu.Lock()
	for name, f := range h.fwds {
		f.Close()
		delete(h.fwds, name)
	}
	h.mu.Unlock()
	if h.gwh != nil {
		h.gwh.Close()
	}
}

var _ Host = (*k8sHost)(nil)
