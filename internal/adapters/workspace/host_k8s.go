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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"apothecary/internal/adapters/egressgw"
	"apothecary/internal/adapters/kube"
	"apothecary/internal/platform/shell"
	"apothecary/internal/ports/envport"
	"apothecary/internal/workspace/checkout"
	wsassets "apothecary/workspace"
)

// KubeConfig runs sandboxes on a Kubernetes cluster.
type KubeConfig struct {
	// Kube is kubectl aimed at the namespace sandboxes, gateways and their
	// secrets live in.
	Kube *kube.Client
	// Images (cluster-pullable): the sandbox (docker:dind + git + node +
	// @devcontainers/cli + WireGuard), the shim, the tools bundle (harness
	// CLIs + code-server, seeded onto the run volume) and the egress gateway.
	SandboxImage, ShimImage, ToolsImage, GatewayImage string
	PullPolicy                                        string // "" = the cluster default
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
	// Storage: "pvc" (default; survives a pod restart) or "emptydir", for
	// every runtime but a VM one (runtime.go, the vm trait), whose storage is
	// VMStorageClass's.
	Storage, StorageClass, StorageSize string
	// VMStorageClass, when set, gives a VM runtime's sandbox (Kata) the run's
	// claim as a raw block device (volumeMode: Block) from this class: one
	// disk in its VM that outlives the pod, which the sandbox formats and
	// mounts itself (apo-dockerd, APO_DATA_DEVICE), so it can be stopped and
	// kept. The class must provision Block volumes (EBS, TopoLVM, Longhorn,
	// Ceph RBD; not k3s's local-path). Empty: a VM sandbox's disks are
	// emptyDirs of its own, gone with its pod. A filesystem claim is never
	// an answer: it reaches the VM over virtio-fs, where docker cannot keep
	// its layers.
	VMStorageClass string
	// Runtime is what isolates each sandbox (runtime.go): privileged (the
	// default, "") or sysbox. RuntimeClass, when set, names the
	// RuntimeClass the default runtime's pods ask for instead of the
	// runtime's own; RuntimeClasses does the same for any approved runtime
	// (it wins over RuntimeClass for the default).
	Runtime        envport.Runtime
	RuntimeClass   string
	RuntimeClasses map[envport.Runtime]string
	// WireGuardUserspace makes every sandbox's tunnel wireguard-go even on a
	// kernel with the module (APO_WG_USERSPACE): the live test's switch, so
	// the path a Kata guest takes is covered where the module exists.
	WireGuardUserspace bool
	// Runtimes are the other runtimes an env may ask for (EnvSpec.Runtime):
	// the ones the integration approves. The default is always built.
	Runtimes []envport.Runtime
	// Firewall routes every sandbox through its project's egress gateway.
	Firewall     bool
	ProtectCIDRs []string
	// ClusterDomain for the sandbox's DNS search path (default cluster.local).
	ClusterDomain string
	// Drift hears whether a workspace's seeded shim/tools match this engine; nil = log only.
	Drift func(ctx context.Context, part string, err error)
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
	// rt shapes the sandbox pod; rtErr is a runtime the adapter cannot
	// build, which every pod creation and the integration's test report.
	rt    sandboxRuntime
	rtErr error
	// probeEvery is how often a runtime probe polls its pod (0: 2s), and
	// probeTimeout how long it waits (0: runtimeProbeTimeout).
	probeEvery, probeTimeout time.Duration

	mu     sync.Mutex
	tokens map[string]string        // run → relay token
	podIPs map[string]string        // run → pod IP (direct dialing)
	podVM  map[string]bool          // run → its pod is a VM (out-of-cluster dials go by exec)
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
		tokens: map[string]string{}, podIPs: map[string]string{}, podVM: map[string]bool{},
		fwds: map[string]*kube.Forward{}, stats: map[string]cgroupSample{}}
	h.rt, h.rtErr = lookupRuntime(c.Runtime)
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
// failed spawn; one still refused by the container runtime after the retries
// is errSandboxBroken.
func (h *k8sHost) runInner(ctx context.Context, name, bin string, args []string, stdin []byte, tee io.Writer, extraEnv ...string) ([]byte, []byte, int, error) {
	out, errb, code, err := h.a.runWith(ctx, h.podRunner(name), bin, args, stdin, tee, extraEnv...)
	if kube.IsExecRefused(err) {
		err = fmt.Errorf("%w: %w", errSandboxBroken, err)
	}
	return out, errb, code, err
}

// refusesExec probes the pod with a trivial exec: true only when its
// container runtime refuses to start the process. Any other failure (the API
// unreachable, the container not started yet) proves nothing about the pod.
func (h *k8sHost) refusesExec(ctx context.Context, name string) bool {
	_, _, _, err := h.runInner(ctx, name, "true", nil, nil, nil)
	return errors.Is(err, errSandboxBroken)
}

func (h *k8sHost) fs(name string) sandboxFS { return execFS{h: h, name: name} }

// contained: the runtime's trait, never a check on its name.
func (h *k8sHost) contained(want envport.Runtime) bool {
	rt, err := h.runtimeFor(want)
	return err == nil && rt.caps().Contained
}

// runtimeFor is the row an env's sandbox is built from: want "" is the
// integration's default runtime; another must be one the integration
// approves (the resolver already checked the project; this is the adapter
// refusing to build what it was not offered).
func (h *k8sHost) runtimeFor(want envport.Runtime) (sandboxRuntime, error) {
	if h.rtErr != nil {
		return sandboxRuntime{}, h.rtErr
	}
	if want == "" || want == h.rt.name {
		return h.rt, nil
	}
	if !slices.Contains(h.cfg.Runtimes, want) {
		return sandboxRuntime{}, fmt.Errorf("kubernetes: sandbox runtime %s is not approved on this provisioner (approved: %v)", want, h.approved())
	}
	return lookupRuntime(want)
}

// approved is every runtime the integration builds: its approved list, the
// default among them.
func (h *k8sHost) approved() []envport.Runtime {
	out := []envport.Runtime{h.rt.name}
	for _, r := range h.cfg.Runtimes {
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// classFor is the RuntimeClass rt's pods name: the integration's override
// for rt (RuntimeClasses), else, for its default runtime, RuntimeClass,
// else the runtime's own.
func (h *k8sHost) classFor(rt sandboxRuntime) string {
	if c := h.cfg.RuntimeClasses[rt.name]; c != "" {
		return c
	}
	if rt.name == h.rt.name {
		return rt.className(h.cfg.RuntimeClass)
	}
	return rt.class
}

// isVMPod says whether a sandbox pod runs under a VM runtime: its runtime
// class is one of the approved VM runtimes' (the manifest carries no other
// mark of its runtime).
func (h *k8sHost) isVMPod(pod kube.Pod) bool {
	for _, name := range h.approved() {
		if rt, err := lookupRuntime(name); err == nil && rt.vm && h.classFor(rt) == pod.Spec.RuntimeClassName {
			return true
		}
	}
	return false
}

func (*k8sHost) containedHint() string {
	return "the Kubernetes provisioner integration's runtime set to sysbox (sysbox-runc on the nodes, its runtime class)"
}

// --- prepare -----------------------------------------------------------------

func (h *k8sHost) prepare(ctx context.Context, name string, spec envport.EnvSpec, rebuild bool, plog func(string, ...any), detect func(context.Context, string) (Definition, error)) (prep, error) {
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
		a.openProvisionEgress(ctx, spec.Project, name, plog)
		a.recordGateway(name, spec.Project, att.GatewayIP)
	}
	plog("→ bringing up the sandbox pod %s (namespace %s)\n", name, h.cfg.Kube.Namespace)
	if err := h.ensurePod(ctx, name, spec, rebuild, att, plog); err != nil {
		return prep{}, err
	}
	src := a.srcDir(name)
	if err := h.ensureCheckout(ctx, name, src, spec, plog); err != nil {
		return prep{}, err
	}
	plog("→ checking the shim and tools bundle seeded into the pod\n")
	def, err := detect(ctx, src)
	if err != nil {
		return prep{}, err
	}
	if err := h.checkShim(ctx, name); err != nil {
		return prep{}, err
	}
	if err := h.checkTools(ctx, name); err != nil {
		return prep{}, err
	}
	return prep{src: src, shimDir: filepath.Join(folder, "shim"), toolsDir: filepath.Join(folder, "tools"), gwIP: att.GatewayIP, def: def}, nil
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
		l[k8sLabelProject] = envport.ProjectHostKey(project)
	}
	return l
}

// ensurePod creates the run's claim, network policy and pod as needed and
// waits for the pod to be Ready (its dockerd answering). A pod that ended
// (Failed/Succeeded) is replaced, and so is a running one whose container
// runtime refuses exec (sysbox restarted under it) — the claim, and with it
// the checkout and the inner daemon's images and containers, survives.
func (h *k8sHost) ensurePod(ctx context.Context, name string, spec envport.EnvSpec, rebuild bool, att egressgw.Attachment, plog func(string, ...any)) error {
	k := h.cfg.Kube
	if _, err := h.relayToken(ctx, name, true, spec.Project); err != nil {
		return err
	}
	rt, err := h.runtimeFor(spec.Runtime)
	if err != nil {
		return err
	}
	if h.keepsClaim(rt) {
		pvc := kube.PersistentVolumeClaim{TypeMeta: kube.PVCType(),
			Metadata: kube.ObjectMeta{Name: dataClaimName(name), Labels: h.labels(name, spec.Project)},
			Spec: kube.PVCSpec{AccessModes: []string{"ReadWriteOnce"},
				Resources: kube.ResourceRequirements{Requests: map[string]string{"storage": h.cfg.StorageSize}}}}
		class := h.cfg.StorageClass
		if rt.vm {
			// A disk in the VM: the claim as a raw device.
			class, pvc.Spec.VolumeMode = h.cfg.VMStorageClass, "Block"
		}
		if class != "" {
			pvc.Spec.StorageClassName = kube.Ptr(class)
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
		plog("→ replacing the sandbox pod: the last one ended (%s); its volume and checkout are kept\n", pod.Status.Phase)
		if err := k.Delete(ctx, "pod", name, true); err != nil {
			return err
		}
		found = false
	} else if found && pod.Status.Phase == "Running" && h.refusesExec(ctx, name) {
		kept := "its volume and checkout are kept"
		if !h.keepsClaim(rt) {
			kept = "its checkout is on an emptyDir and goes with it"
		}
		plog("→ replacing the sandbox pod: it no longer accepts exec (container runtime restarted?); %s\n", kept)
		if err := k.Delete(ctx, "pod", name, true); err != nil {
			return err
		}
		found = false
	}
	// A rebuild applies the workshop's sandbox size and runtime: a pod of
	// another shape is replaced (k8s_shape.go).
	var want *kube.Pod
	if found && rebuild {
		p, err := h.podManifest(ctx, name, spec, att)
		if err != nil {
			return err
		}
		want = &p
		replace, note := shapeChange(pod, p, name)
		if note != "" {
			plog("→ keeping the sandbox pod: %s\n", note)
		}
		if replace {
			plog("→ replacing the sandbox pod: the workshop's sandbox size or runtime changed; its volume and checkout are kept\n")
			if err := k.Delete(ctx, "pod", name, true); err != nil {
				return err
			}
			found = false
		}
	}
	if found {
		plog("→ reusing the sandbox pod (%s)\n", orDefault(pod.Status.Phase, "unknown phase"))
	} else {
		p := want
		if p == nil {
			made, err := h.podManifest(ctx, name, spec, att)
			if err != nil {
				return err
			}
			p = &made
		}
		plog("→ creating the sandbox pod (image %s)\n", h.cfg.SandboxImage)
		if err := k.Create(ctx, *p); err != nil {
			return err
		}
	}
	h.forgetPod(name)
	plog("→ waiting for the sandbox pod to be ready: scheduled, images pulled, its docker daemon up (up to 5m)\n")
	if err := h.waitPodReady(ctx, name, 5*time.Minute, plog); err != nil {
		err = fmt.Errorf("sandbox pod %s never became ready (inspect: kubectl -n %s describe pod %s): %w", name, k.Namespace, name, err)
		// The wait may have used up ctx: the question gets a moment of its own.
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if w, werr := k.LastWarning(wctx, name); werr == nil && w != "" {
			err = fmt.Errorf("%w; the pod's last warning: %s", err, w)
		}
		return err
	}
	return nil
}

// podProgressEvery paces the "still waiting" lines of waitPodReady.
var podProgressEvery = 10 * time.Second

// waitPodReady waits for the pod to be Ready, telling the provisioning log
// what it is waiting on while it does — its phase and each container not
// ready yet, with why (an image pulling, a crash loop): a pod that never
// comes up must not read as a log that just stopped. A line is written when
// the answer changes.
func (h *k8sHost) waitPodReady(ctx context.Context, name string, timeout time.Duration, plog func(string, ...any)) error {
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(podProgressEvery)
		defer t.Stop()
		last := ""
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
			var pod kube.Pod
			if found, err := h.cfg.Kube.Get(ctx, "pod", name, &pod); err != nil || !found {
				continue
			}
			s := podProgress(pod.Status)
			if pod.Status.Phase == "Pending" || pod.Status.Phase == "" {
				// Not scheduled yet: the scheduler says why (a node without
				// the memory or CPU the sandbox's size asks for, a runtime
				// class no node has), and only in an event.
				if w, err := h.cfg.Kube.LastWarning(ctx, name); err == nil && w != "" {
					s += "; last warning: " + shell.Tail(w, 300)
				}
			}
			if s != last {
				plog("  … pod %s\n", s)
				last = s
			}
		}
	}()
	err := h.cfg.Kube.WaitReady(ctx, name, timeout)
	close(stop)
	wg.Wait()
	return err
}

// podProgress says where a pod is on its way to Ready: its phase, then each
// container that is not ready and why.
func podProgress(s kube.PodStatus) string {
	parts := []string{orDefault(s.Phase, "Pending")}
	all := append(append([]kube.ContainerStatus(nil), s.InitContainerStatuses...), s.ContainerStatuses...)
	for _, c := range all {
		if c.Ready {
			continue
		}
		st := "not ready"
		switch {
		case c.State.Waiting != nil:
			st = orDefault(c.State.Waiting.Reason, "waiting")
			if m := strings.TrimSpace(c.State.Waiting.Message); m != "" {
				st += ": " + shell.Tail(m, 200)
			}
		case c.State.Terminated != nil:
			st = "exited " + strconv.Itoa(c.State.Terminated.ExitCode)
			if r := c.State.Terminated.Reason; r != "" {
				st += " (" + r + ")"
			}
		case c.State.Running != nil:
			st = "running, not ready yet"
		}
		if c.RestartCount > 0 {
			st += fmt.Sprintf(", %d restarts", c.RestartCount)
		}
		parts = append(parts, c.Name+": "+st)
	}
	return strings.Join(parts, "; ")
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
			{To: []kube.NetworkPolicyPeer{{PodSelector: &kube.LabelSelector{MatchLabels: map[string]string{k8sLabelGateway: envport.ProjectHostKey(project)}}}},
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

func (h *k8sHost) podManifest(ctx context.Context, name string, spec envport.EnvSpec, att egressgw.Attachment) (kube.Pod, error) {
	rt, err := h.runtimeFor(spec.Runtime)
	if err != nil {
		return kube.Pod{}, err
	}
	folder := h.a.folderFor(name)
	labels := h.labels(name, spec.Project)
	labels[k8sLabelSandbox] = "true"
	annotations := map[string]string{}
	if spec.ProjectName != "" {
		annotations["apothecary.io/project-name"] = spec.ProjectName
	}
	secretMode := int32(0o400)
	if rt.vm {
		if spec.Size.MilliCPU <= 0 || spec.Size.MemoryMiB <= 0 {
			return kube.Pod{}, fmt.Errorf("kubernetes: a %s sandbox needs a size, CPU and memory (they size its VM; a ceiling alone does not): set the workshop's, or the instance's default in System → Limits", rt.name)
		}
	}
	runMount := kube.VolumeMount{Name: "data", SubPath: "run", MountPath: folder}
	dockerMount := kube.VolumeMount{Name: "data", SubPath: "docker", MountPath: "/var/lib/docker"}
	// The sandbox container's own mounts: the run folder and docker's data,
	// or, on a VM's raw-block claim, the seeded run volume aside (vmSeedDir).
	sandboxMounts := []kube.VolumeMount{dockerMount, runMount}
	vmClaim := rt.vm && h.keepsClaim(rt)
	if rt.vm {
		// A volume each, no subPath: a disk each in the guest.
		runMount = kube.VolumeMount{Name: "run", MountPath: folder}
		dockerMount = kube.VolumeMount{Name: "docker", MountPath: "/var/lib/docker"}
		sandboxMounts = []kube.VolumeMount{dockerMount, runMount}
	}
	if vmClaim {
		// The claim is a device the sandbox formats and mounts: the run folder
		// and docker's data on it, and the shim and tools the init containers
		// seed into the run volume bound over the folder's (apo-dockerd). The
		// init containers and the relay keep the run volume at the folder.
		sandboxMounts = []kube.VolumeMount{{Name: "run", MountPath: vmSeedDir}}
	}
	privileged := func() *kube.SecurityContext {
		if !rt.privileged {
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
		VolumeMounts: append(sandboxMounts, kube.VolumeMount{Name: "apo", MountPath: "/run/apo"}),
	}
	if rt.vm {
		sandbox.Env = append(sandbox.Env, kube.EnvVar{Name: "APO_DEV_PRELUDE", Value: "1"})
	}
	if vmClaim {
		sandbox.VolumeDevices = []kube.VolumeDevice{{Name: "data", DevicePath: vmDataDevice}}
		sandbox.Env = append(sandbox.Env,
			kube.EnvVar{Name: "APO_DATA_DEVICE", Value: vmDataDevice},
			kube.EnvVar{Name: "APO_RUN_DIR", Value: folder},
			kube.EnvVar{Name: "APO_SEED_DIR", Value: vmSeedDir})
	}
	seed := kube.Container{Name: "shim-seed", Image: h.cfg.ShimImage, ImagePullPolicy: h.cfg.PullPolicy,
		Args: []string{"seed", filepath.Join(folder, "shim")}, VolumeMounts: []kube.VolumeMount{runMount}}
	// The tools bundle, copied onto the run volume once per bundle version
	// (seed.sh skips a volume that already holds this image's TOOLS_HASH).
	tools := kube.Container{Name: "tools-seed", Image: h.cfg.ToolsImage, ImagePullPolicy: h.cfg.PullPolicy,
		Args: []string{filepath.Join(folder, "tools")}, VolumeMounts: []kube.VolumeMount{runMount}}
	relay := kube.Container{Name: "relay", Image: h.cfg.ShimImage, ImagePullPolicy: h.cfg.PullPolicy,
		RestartPolicy: "Always", // a native sidecar: up before the sandbox, restarted on its own
		Args: []string{"relay", "--serve", "0.0.0.0:" + strconv.Itoa(relayPort),
			"--dir", filepath.Join(folder, "shim"), "--token-file", "/secrets/run/relay-token"},
		Ports:        []kube.ContainerPort{{Name: "relay", ContainerPort: relayPort}},
		Resources:    sidecarResources(),
		VolumeMounts: []kube.VolumeMount{runMount, {Name: "run-secret", MountPath: "/secrets/run", ReadOnly: true}}}
	sandbox.Resources = sandboxResources(spec.Size, spec.SizeCap)
	pod := kube.Pod{TypeMeta: kube.PodType(),
		Metadata: kube.ObjectMeta{Name: name, Labels: labels, Annotations: annotations},
		Spec: kube.PodSpec{
			RestartPolicy:                 "Always",
			TerminationGracePeriodSeconds: kube.Ptr(int64(30)),
			AutomountServiceAccountToken:  kube.Ptr(false),
			EnableServiceLinks:            kube.Ptr(false),
			Hostname:                      name,
			InitContainers:                []kube.Container{seed, tools},
			Containers:                    []kube.Container{sandbox},
			Volumes: []kube.Volume{
				{Name: "apo", EmptyDir: &kube.EmptyDirVolumeSource{}},
				{Name: "run-secret", Secret: &kube.SecretVolumeSource{SecretName: runSecretName(name), DefaultMode: &secretMode}},
			},
		}}
	pod.Spec.RuntimeClassName = h.classFor(rt)
	if rt.ownUserNamespace {
		// The pod's own Kubernetes user namespace (the runtime's comment says
		// why sysbox needs it).
		pod.Spec.HostUsers = kube.Ptr(false)
	}
	if vmClaim {
		// The seeded shim and tools only (the tools bundle is about 2.3 GB):
		// reseeded by each pod, never kept.
		pod.Spec.Volumes = append(pod.Spec.Volumes,
			kube.Volume{Name: "run", EmptyDir: &kube.EmptyDirVolumeSource{SizeLimit: vmSeedSize}},
			kube.Volume{Name: "data", PersistentVolumeClaim: &kube.PersistentVolumeClaimVolumeSource{ClaimName: dataClaimName(name)}})
	} else if rt.vm {
		pod.Spec.Volumes = append(pod.Spec.Volumes,
			kube.Volume{Name: "run", EmptyDir: &kube.EmptyDirVolumeSource{SizeLimit: h.cfg.StorageSize}},
			kube.Volume{Name: "docker", EmptyDir: &kube.EmptyDirVolumeSource{SizeLimit: h.cfg.StorageSize}})
	} else if h.cfg.Storage == "pvc" {
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
		if !rt.privileged {
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
			Resources:       sidecarResources(),
			StartupProbe: &kube.Probe{Exec: &kube.ExecAction{Command: []string{"test", "-f", "/run/apo/wg-ready"}},
				PeriodSeconds: 1, FailureThreshold: 60},
			VolumeMounts: []kube.VolumeMount{
				{Name: "wg-secret", MountPath: "/secrets/wg", ReadOnly: true},
				{Name: "apo", MountPath: "/run/apo"},
			}}
		if h.cfg.WireGuardUserspace {
			wg.Env = append(wg.Env, kube.EnvVar{Name: "APO_WG_USERSPACE", Value: "1"})
		}
		pod.Spec.InitContainers = append(pod.Spec.InitContainers, wg)
		pod.Spec.Volumes = append(pod.Spec.Volumes, kube.Volume{Name: "wg-secret",
			Secret: &kube.SecretVolumeSource{SecretName: att.WGSecret, DefaultMode: &secretMode}})
		pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, kube.EnvVar{Name: "APO_GATEWAY_IP", Value: att.GatewayIP})
	}
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, relay)
	annotations[shapeAnnotation] = podShape(rt, pod)
	return pod, nil
}

// cpuRequestShare is the share of a sandbox's CPU limit it requests: CPU is
// overcommitted (an idle sandbox holds no core), memory never is (a request
// below the limit is memory the node may not have when the sandbox reaches
// for it). minCPURequest keeps a tiny limit from requesting nothing.
const (
	cpuRequestShare = 4 // request = limit / cpuRequestShare
	minCPURequest   = 100
)

// sandboxResources are the sandbox container's requests and limits for its
// size: memory requested at its limit, CPU limited at the size and
// requested at a share of it. A field the size leaves unset is limited at
// its cap (the instance's ceiling) and requested at a floor: the ceiling
// bounds the sandbox without reserving it, so a large one never makes an
// unsized sandbox unschedulable. Neither set sets nothing, as before sizes.
func sandboxResources(size, ceiling envport.SandboxSize) *kube.ResourceRequirements {
	if size.IsZero() && ceiling.IsZero() {
		return nil
	}
	r := &kube.ResourceRequirements{Requests: map[string]string{}, Limits: map[string]string{}}
	if m := size.MilliCPU; m > 0 {
		r.Limits["cpu"] = strconv.Itoa(m) + "m"
		r.Requests["cpu"] = strconv.Itoa(min(m, max(m/cpuRequestShare, minCPURequest))) + "m"
	} else if c := ceiling.MilliCPU; c > 0 {
		// Without a request, Kubernetes would request the limit.
		r.Limits["cpu"] = strconv.Itoa(c) + "m"
		r.Requests["cpu"] = strconv.Itoa(min(c, minCPURequest)) + "m"
	}
	if m := size.MemoryMiB; m > 0 {
		mem := strconv.Itoa(m) + "Mi"
		r.Limits["memory"], r.Requests["memory"] = mem, mem
	} else if c := ceiling.MemoryMiB; c > 0 {
		r.Limits["memory"] = strconv.Itoa(c) + "Mi"
		r.Requests["memory"] = strconv.Itoa(min(c, cappedMemoryRequest)) + "Mi"
	}
	return r
}

// cappedMemoryRequest is what an unsized sandbox under a memory ceiling
// requests (MiB): enough for its docker daemon and an idle workspace, so
// the scheduler does not pack such sandboxes onto a node as if they were
// free.
const cappedMemoryRequest = 1024

// sidecarResources are the tunnel's and the relay's: requests only, so the
// scheduler counts them, and no limit (a CPU limit on wireguard-go would
// throttle the sandbox's whole network).
func sidecarResources() *kube.ResourceRequirements {
	return &kube.ResourceRequirements{Requests: map[string]string{"cpu": "10m", "memory": "32Mi"}}
}

// ensureCheckout clones the repo inside the pod, once. The credential rides
// stdin (never argv, never the environment) into a 0600 file the clone uses
// and removes.
func (h *k8sHost) ensureCheckout(ctx context.Context, name, src string, spec envport.EnvSpec, plog func(string, ...any)) error {
	if _, _, code, err := h.runInner(ctx, name, "test", []string{"-d", filepath.Join(src, ".git")}, nil, nil); err == nil && code == 0 {
		plog("→ keeping the checkout already in the pod\n")
		return nil
	}
	plog("→ cloning the repository into the pod\n")
	cred := ""
	if spec.GitPass != "" {
		line, err := checkout.CredentialLine(spec.RepoURL, spec.GitUser, spec.GitPass)
		if err != nil {
			return err
		}
		cred = line
	}
	// The clone, then the branch step inside it (checkout.BranchScript): the
	// checkout is on the run's branch before the devcontainer is built from
	// it. `--` keeps the URL and the path from being read as options.
	branch := ""
	if spec.Branch != "" {
		branch = fmt.Sprintf(" && cd %s && { %s; }", shell.Quote(src), checkout.BranchScript(spec.Branch, spec.SourceBranch, spec.Fetch))
	}
	// One line of script, then the credential: bash reads the script line,
	// `read` takes the next. The credential reaches every git command of the
	// script through git's environment config — never argv, never the
	// checkout's config — and is removed when the script ends: the branch
	// step's fetch of a merge request's head needs it as much as the clone.
	// Only the credential write runs under umask 077 (a subshell): git must
	// write the checkout with the modes it records, or a container running
	// as a non-root user cannot read a file bind-mounted from it.
	script := fmt.Sprintf(`IFS= read -r CRED; mkdir -p %[1]s; f=%[1]s/git-credentials; `+
		`if [ -n "$CRED" ]; then (umask 077; printf '%%s\n' "$CRED" > "$f"); export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=credential.helper GIT_CONFIG_VALUE_0="store --file=$f"; fi; `+
		`git clone -- %[2]s %[3]s%[4]s; rc=$?; rm -f "$f"; exit $rc`,
		shell.Quote(filepath.Dir(src)), shell.Quote(spec.RepoURL), shell.Quote(src), branch)
	stdin := []byte(script + "\n" + cred + "\n")
	_, stderr, code, err := h.runInner(ctx, name, "bash", []string{"-s"}, stdin, nil)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("checkout %s: exit %d: %s", spec.RepoURL, code, shell.Tail(string(stderr), 2048))
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
		err := fmt.Errorf("sandbox %s: shim image %s was built from shim sources %s, this engine expects %s — publish the shim image of this engine's version", name, h.cfg.ShimImage, got, want)
		h.drift(ctx, "shim", err)
		return err
	}
	h.drift(ctx, "shim", nil)
	return nil
}

func (h *k8sHost) drift(ctx context.Context, part string, err error) {
	if h.cfg.Drift != nil {
		h.cfg.Drift(ctx, part, err)
	}
}

// checkTools makes sure the tools bundle was seeded, and only WARNS when it
// is not the one this engine expects: a bundle of another version still
// works (the harness versions differ, nothing of the engine's protocol), and
// the next pod start reseeds from whatever image the integration names.
func (h *k8sHost) checkTools(ctx context.Context, name string) error {
	b, err := h.fs(name).ReadFile(ctx, filepath.Join(h.a.folderFor(name), "tools", "TOOLS_HASH"))
	if err != nil {
		return fmt.Errorf("sandbox %s: no seeded tools bundle (%v) — is %s an apothecary tools image?", name, err, h.cfg.ToolsImage)
	}
	if got, want := strings.TrimSpace(string(b)), wsassets.ToolsHash(); got != want {
		slog.Warn("kubernetes: the seeded tools bundle is not this engine's (harness/code-server versions may differ)",
			"env", name, "image", h.cfg.ToolsImage, "seeded", got, "expected", want)
		h.drift(ctx, "tools", fmt.Errorf("the seeded tools bundle is %s, this engine expects %s (image %s)", got, want, h.cfg.ToolsImage))
	} else {
		h.drift(ctx, "tools", nil)
	}
	return nil
}

// --- state / teardown ----------------------------------------------------------

func (h *k8sHost) sandboxState(ctx context.Context, name string) (string, error) {
	var pod kube.Pod
	found, err := h.cfg.Kube.Get(ctx, "pod", name, &pod)
	if err != nil {
		return "", err
	}
	if !found {
		return "", nil
	}
	switch pod.Status.Phase {
	case "Running":
		if pod.Status.Ready() {
			return "running", nil
		}
		// kubelet's exec readiness probe may take a pod whose runtime refuses
		// exec out of Ready: tell it from one still coming up.
		if h.refusesExec(ctx, name) {
			return "broken", nil
		}
		return "created", nil
	case "Pending":
		return "created", nil
	default: // Succeeded, Failed, Unknown
		return "exited", nil
	}
}

func (h *k8sHost) destroySandbox(ctx context.Context, name string) error {
	k := h.cfg.Kube
	// Every object is tried, whatever the one before it did: a pod delete
	// that timed out (its pod often goes anyway) must not leave the volume,
	// the network policy and the relay secret behind. Absent objects are
	// success (--ignore-not-found), so a retry finishes what this left.
	var errs []error
	if err := k.Delete(ctx, "pod", name, true); err != nil {
		errs = append(errs, fmt.Errorf("delete pod %s: %w", name, err))
	}
	for _, obj := range [][2]string{{"persistentvolumeclaim", dataClaimName(name)}, {"networkpolicy", runSecretName(name)}, {"secret", runSecretName(name)}} {
		if err := k.Delete(ctx, obj[0], obj[1], false); err != nil {
			errs = append(errs, fmt.Errorf("delete %s %s: %w", obj[0], obj[1], err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
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

// stopSandbox deletes the pod alone: the claim (checkout, the inner
// daemon's images and containers), the relay secret and the network policy
// stay, and the next ensurePod starts a pod on them.
func (h *k8sHost) stopSandbox(ctx context.Context, name string) error {
	if err := h.checkStop(ctx, name); err != nil {
		return err
	}
	if err := h.cfg.Kube.Delete(ctx, "pod", name, true); err != nil {
		return fmt.Errorf("delete pod %s: %w", name, err)
	}
	h.forgetPod(name)
	h.mu.Lock()
	delete(h.stats, name)
	if f := h.fwds[name]; f != nil {
		f.Close()
		delete(h.fwds, name)
	}
	h.mu.Unlock()
	return nil
}

// canStop: a sandbox without a claim (emptyDir storage, or a VM runtime
// without a VM storage class) loses everything with its pod.
func (h *k8sHost) canStop(want envport.Runtime) error {
	rt, err := h.runtimeFor(want)
	if err != nil {
		return err
	}
	return CanStopUnder(rt.name, h.cfg.Storage, h.cfg.VMStorageClass)
}

// keepsClaim: rt's sandboxes keep the run on its claim, which outlives the
// pod (a raw-block one under a VM runtime).
func (h *k8sHost) keepsClaim(rt sandboxRuntime) bool {
	if rt.vm {
		return h.cfg.VMStorageClass != ""
	}
	return h.cfg.Storage == "pvc"
}

// checkStop is canStop for the run's live pod, by what it stands on: the
// run's claim (mounted, or a VM's raw-block disk) is kept, anything else
// goes with it, whatever the project picks now. No pod: the default
// runtime's answer.
func (h *k8sHost) checkStop(ctx context.Context, name string) error {
	var pod kube.Pod
	found, err := h.cfg.Kube.Get(ctx, "pod", name, &pod)
	if err != nil {
		return err
	}
	if !found {
		// Nothing running: the integration's default runtime says.
		return h.canStop("")
	}
	if !usesClaim(pod, name) {
		if h.isVMPod(pod) {
			return fmt.Errorf("sandbox pod %s runs in a VM (runtime class %s) on disks of its own, which go with it: it cannot be stopped and kept", name, pod.Spec.RuntimeClassName)
		}
		return CanStop("emptydir")
	}
	return nil
}

// resetSandbox empties the pod's daemon and leaves the pod: the checkout is on
// the volume under it, which an emptyDir would lose with the pod, and a live
// pod keeps its tunnel to the gateway. It runs after prepare, whose ensurePod
// has already replaced a pod that refused exec. An ended pod has nothing to
// empty.
func (h *k8sHost) resetSandbox(ctx context.Context, name string) error {
	state, err := h.sandboxState(ctx, name)
	if err != nil || state != "running" {
		return err
	}
	docker := func(args ...string) ([]byte, error) {
		out, stderr, code, err := h.runInner(ctx, name, h.a.cfg.DockerBin, args, nil, nil)
		if err != nil {
			return nil, err
		}
		if code != 0 {
			return nil, fmt.Errorf("docker %s: exit %d: %s", args[0], code, shell.Tail(string(stderr), 512))
		}
		return out, nil
	}
	out, err := docker("ps", "-aq")
	if err != nil {
		return err
	}
	if ids := strings.Fields(string(out)); len(ids) > 0 {
		if _, err := docker(append([]string{"rm", "-f", "-v"}, ids...)...); err != nil {
			return err
		}
	}
	if _, err := docker("volume", "prune", "-af"); err != nil {
		return err
	}
	_, err = docker("network", "prune", "-f")
	return err
}

// --- the engine bridge: a Service, no relay ------------------------------------

func (h *k8sHost) ensureBridge(envport.EnvID) error                       { return nil }
func (h *k8sHost) startBridgeRelay(context.Context, string, string) error { return nil }
func (h *k8sHost) closeBridge(envport.EnvID)                              {}
func (h *k8sHost) bridgePorts() []int                                     { return nil }
func (h *k8sHost) engineURL() string                                      { return h.cfg.EngineURL }

// --- dialing in ----------------------------------------------------------------

func (h *k8sHost) dial(name, sock string) envport.PortConn {
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
	if vm, err := h.viaExec(ctx, name); err != nil {
		return nil, err
	} else if vm {
		return h.dialExec(ctx, name, sock)
	}
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
	delete(h.podVM, name)
	h.mu.Unlock()
}

// viaExec reports whether an engine outside the cluster dials the run's pod
// through kubectl exec rather than its port-forward: under a VM runtime the
// relay listens in the guest, while containerd's port-forward dials the port
// in the node's network namespace, where nothing listens. Inside the cluster
// the pod's IP reaches the guest, and nothing changes.
func (h *k8sHost) viaExec(ctx context.Context, name string) (bool, error) {
	if !h.cfg.Forward {
		return false, nil
	}
	h.mu.Lock()
	vm, ok := h.podVM[name]
	h.mu.Unlock()
	if ok {
		return vm, nil
	}
	var pod kube.Pod
	found, err := h.cfg.Kube.Get(ctx, "pod", name, &pod)
	if err != nil {
		return false, err
	}
	if !found {
		return false, fmt.Errorf("sandbox pod %s not found", name)
	}
	vm = h.isVMPod(pod)
	h.mu.Lock()
	h.podVM[name] = vm
	h.mu.Unlock()
	return vm, nil
}

// dialExec is dialMux through kubectl exec into the pod's relay container:
// its shim pipes the socket over stdio, after the mux's "OK" (relay
// --stdio-ack). No token: exec is authorised by the cluster.
func (h *k8sHost) dialExec(ctx context.Context, name, sock string) (net.Conn, error) {
	path := filepath.Join(h.a.folderFor(name), "shim", sock)
	c, err := h.cfg.Kube.ExecConn(ctx, name, "relay", []string{"/apothecary-shim", "relay", "--stdio-ack", path})
	if err != nil {
		return nil, err
	}
	type answer struct {
		line string
		err  error
	}
	br := bufio.NewReaderSize(c, 64)
	got := make(chan answer, 1)
	go func() {
		line, err := br.ReadString('\n')
		got <- answer{line, err}
	}()
	wait := 10 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		wait = time.Until(dl)
	}
	select {
	case a := <-got:
		if a.err != nil || a.line != "OK\n" {
			c.Close()
			return nil, fmt.Errorf("sandbox %s: %s is not being served", name, sock)
		}
	case <-time.After(wait):
		c.Close()
		return nil, fmt.Errorf("sandbox %s: no answer from %s within %s", name, sock, wait.Truncate(time.Millisecond))
	case <-ctx.Done():
		c.Close()
		return nil, ctx.Err()
	}
	return &readerConn{Conn: c, r: br}, nil
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

// --- resource sampling ----------------------------------------------------------

// cgroupSample is one reading of the sandbox container's cgroup: the
// container holds the inner dockerd and, nested under it, every workspace
// and compose sibling — the run's whole footprint. memUsed is the working
// set (memory.current less the inactive file cache, as the kubelet and
// `docker stats` count it): a build leaves gigabytes of page cache the
// kernel drops first, and counting it reads as a workspace about to OOM.
type cgroupSample struct {
	at       time.Time
	usageUS  int64
	memUsed  int64
	memLimit int64
	pids     int
	cpus     int
	rx, tx   int64
	oomKills int
}

const sampleScript = `cd /sys/fs/cgroup 2>/dev/null || exit 3
echo "cpu $(awk '$1=="usage_usec"{print $2}' cpu.stat)"
echo "mem $(cat memory.current)"
echo "inactive $(awk '$1=="inactive_file"{print $2}' memory.stat 2>/dev/null)"
echo "max $(cat memory.max)"
echo "pids $(cat pids.current 2>/dev/null || echo 0)"
echo "oom $(awk '$1=="oom_kill"{print $2}' memory.events 2>/dev/null)"
echo "quota $(cat cpu.max 2>/dev/null)"
echo "nproc $(nproc)"
awk -F'[: ]+' '$2=="eth0"{print "net "$3" "$11}' /proc/net/dev`

func parseCgroupSample(out string) (cgroupSample, error) {
	s := cgroupSample{}
	var quota, period, inactive int64
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
		case "inactive":
			inactive = n(1)
		case "max":
			if f[1] != "max" {
				s.memLimit = n(1)
			}
		case "pids":
			s.pids = int(n(1))
		case "oom":
			s.oomKills = int(n(1))
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
	s.memUsed = max(s.memUsed-inactive, 0)
	if quota > 0 && period > 0 {
		if c := int((quota + period - 1) / period); c > 0 && (s.cpus == 0 || c < s.cpus) {
			s.cpus = c
		}
	}
	return s, nil
}

func (h *k8sHost) sample(ctx context.Context, name string) (envport.ResourceSample, error) {
	out, _, code, err := h.podRunner(name).Run(ctx, "sh", []string{"-c", sampleScript}, nil, nil)
	if err != nil {
		return envport.ResourceSample{}, err
	}
	if code != 0 {
		return envport.ResourceSample{}, fmt.Errorf("sample %s: exit %d", name, code)
	}
	cur, err := parseCgroupSample(string(out))
	if err != nil {
		return envport.ResourceSample{}, fmt.Errorf("sample %s: %w", name, err)
	}
	cur.at = time.Now()
	h.mu.Lock()
	prev, ok := h.stats[name]
	h.stats[name] = cur
	h.mu.Unlock()
	h.noteOOM(ctx, name, cur, !ok || cur.usageUS < prev.usageUS)
	if !ok || cur.usageUS < prev.usageUS {
		return envport.ResourceSample{}, fmt.Errorf("sample %s: first reading (CPU needs two)", name)
	}
	return cpuSample(prev, cur), nil
}

// cpuSample is docker-stats-shaped: one core = 100%.
func cpuSample(prev, cur cgroupSample) envport.ResourceSample {
	wall := cur.at.Sub(prev.at).Microseconds()
	cpu := 0.0
	if wall > 0 {
		cpu = float64(cur.usageUS-prev.usageUS) / float64(wall) * 100
	}
	return envport.ResourceSample{CPUPerc: cpu, CPUs: cur.cpus, MemUsed: cur.memUsed, MemLimit: cur.memLimit,
		NetRx: cur.rx, NetTx: cur.tx, PIDs: cur.pids, OOMKills: cur.oomKills}
}

// --- test / close ------------------------------------------------------------

func (h *k8sHost) test(ctx context.Context) error {
	if h.rtErr != nil {
		return h.rtErr
	}
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
	if h.cfg.SandboxImage == "" || h.cfg.ShimImage == "" || h.cfg.ToolsImage == "" || (h.cfg.Firewall && h.cfg.GatewayImage == "") {
		return errors.New("kubernetes: sandbox, shim, tools and gateway images must be set")
	}
	if _, _, err := h.engineAddr(); err != nil {
		return err
	}
	// Last: a pod under each runtime the integration builds, so a cluster
	// without one fails here rather than at a run.
	return h.probeRuntimes(ctx)
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
