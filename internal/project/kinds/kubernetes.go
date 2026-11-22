package kinds

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"apothecary/internal/adapters/kube"
	workspaceadapter "apothecary/internal/adapters/workspace"
	"apothecary/internal/ports/envport"
)

// KubernetesDefaults are the deployment's answers (boot env) to everything a
// Kubernetes integration leaves empty.
type KubernetesDefaults struct {
	Namespace, EngineNamespace, EngineURL             string
	Kubeconfig                                        string
	SandboxImage, ShimImage, ToolsImage, GatewayImage string
	PullPolicy                                        string
	ProtectCIDRs                                      []string
	Transport                                         string // direct | portforward
	// Runtime and RuntimeClass are the sandbox runtime an integration gets
	// when it leaves its own fields empty (APOTHECARY_K8S_RUNTIME, the older
	// APOTHECARY_K8S_PRIVILEGED already folded into it by the boot, and
	// APOTHECARY_K8S_RUNTIME_CLASS). Runtime "" means privileged.
	Runtime, RuntimeClass string
	// Runtimes are the runtimes projects may choose among when the
	// integration approves none itself (APOTHECARY_K8S_RUNTIMES): the ones
	// the cluster's nodes run. Empty: the default runtime alone.
	Runtimes []string
}

// Kubernetes is the provisioner/kubernetes kind: each run's sandbox is a pod
// (docker:dind + the devcontainer CLI) on a Kubernetes cluster, egress goes
// through the project's gateway over WireGuard, and the workspace inside is
// whatever the repo defines (its devcontainer.json), built by the reference
// @devcontainers/cli inside the pod.
type Kubernetes struct{ Defaults KubernetesDefaults }

func (k Kubernetes) Descriptor() Descriptor {
	d := k.Defaults
	return Descriptor{
		Port: "provisioner", Kind: "kubernetes", Label: "Kubernetes (sandbox pod per run)",
		Help: "Runs each workspace as a Docker-in-Docker pod under a sandbox runtime: privileged, sysbox or kata. Empty fields use the deployment's APOTHECARY_K8S_* settings.",
		Fields: []Field{
			{Name: "kubeconfig", Label: "kubeconfig", Secret: true,
				Help: "Contents of a kubeconfig, or an absolute path. Empty: the engine's in-cluster service account."},
			{Name: "context", Label: "kubeconfig context"},
			{Name: "namespace", Label: "Namespace for workspaces", Placeholder: d.Namespace},
			{Name: "engine_url", Label: "Engine URL as workspaces reach it", Placeholder: d.EngineURL},
			{Name: "engine_cidr", Label: "Engine address (engine outside the cluster)",
				Help: "Only when the engine runs outside the cluster: its address as pods see it, e.g. 172.18.0.1/32."},
			{Name: "sandbox_image", Label: "Sandbox image", Placeholder: d.SandboxImage},
			{Name: "shim_image", Label: "Shim image", Placeholder: d.ShimImage},
			{Name: "tools_image", Label: "Tools image (harnesses + code-server)", Placeholder: d.ToolsImage},
			{Name: "gateway_image", Label: "Egress gateway image", Placeholder: d.GatewayImage},
			{Name: "image_pull_policy", Label: "Image pull policy", Placeholder: orDefault(d.PullPolicy, "IfNotPresent")},
			{Name: "storage", Label: "Workspace storage (pvc/emptydir)", Placeholder: "pvc (emptydir when kata is approved)",
				Help: "Where a sandbox keeps its checkout and inner Docker data. pvc: a claim per run, kept while the run lives. emptydir: the node's disk, gone with the pod. A kata sandbox needs emptydir (a block disk in its VM)."},
			{Name: "storage_class", Label: "Storage class", Help: "Must be a local or block filesystem: the inner docker daemon cannot run on NFS."},
			{Name: "storage_size", Label: "Storage size per run", Placeholder: "20Gi"},
			{Name: "runtime", Label: "Sandbox runtime (" + strings.Join(runtimeNames(), "/") + ")", Placeholder: orDefault(d.Runtime, string(envport.RuntimePrivileged)),
				Help: "What isolates each sandbox. privileged: sandbox root is root on its node (CI, a cluster of your own). sysbox: unprivileged, in a user namespace of its own (hostUsers: false; sysbox-runc on the nodes, containerd 2.0.5+, sysbox 0.7+; README, cluster requirements). kata: a VM per sandbox (Kata Containers with Cloud Hypervisor, KVM on the nodes; needs emptydir storage and sized sandboxes)."},
			{Name: "runtimes", Label: "Approved runtimes (comma-separated)", Placeholder: orDefault(strings.Join(d.Runtimes, ","), "the default runtime alone"),
				Help: "The runtimes a workshop may choose for its sandboxes, the default among them; only list runtimes this cluster's nodes run (Test starts a pod under each). privileged sandboxes are root on their node: approve it only on a cluster of your own."},
			{Name: "runtime_class", Label: "Runtime class", Placeholder: orDefault(d.RuntimeClass, "the runtime's own"),
				Help: "The RuntimeClass sandbox pods name, when not the runtime's own (sysbox: sysbox-runc; privileged: none, the node's default)."},
			{Name: "privileged", Label: "Privileged sandboxes (on/off, older: use the runtime)",
				Help: "Read only when the runtime is empty: on is the privileged runtime, off is sysbox (with a runtime class)."},
			{Name: "firewall", Label: "Egress firewall (on/off)", Placeholder: "on", Default: "on"},
		},
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// kubernetesProvisioner owns the kubeconfig file it materialized.
type kubernetesProvisioner struct {
	*workspaceadapter.Adapter
	cleanup func()
}

func (p *kubernetesProvisioner) Close() {
	p.Adapter.Close()
	p.cleanup()
}

func (k Kubernetes) New(cfg Config, deps ProvisionerDeps) (Provisioner, error) {
	d := k.Defaults
	kc := cfg.Secrets["kubeconfig"]
	if kc == "" {
		kc = d.Kubeconfig
	}
	path, cleanup, err := kube.Kubeconfig(filepath.Join(deps.RunsDir, "k8s"), kc)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: kubeconfig: %w", err)
	}
	ns := cfg.Str("namespace", d.Namespace)
	if ns == "" {
		cleanup()
		return nil, fmt.Errorf("kubernetes: no namespace (set the integration's or APOTHECARY_K8S_NAMESPACE)")
	}
	engineURL := cfg.Str("engine_url", d.EngineURL)
	if engineURL == "" {
		cleanup()
		return nil, fmt.Errorf("kubernetes: no engine URL (set the integration's or APOTHECARY_K8S_ENGINE_URL)")
	}
	choice, runtimeClass, err := kubernetesChoice(cfg, d)
	if err != nil {
		cleanup()
		return nil, err
	}
	storage := kubernetesStorage(cfg, choice.Approved)
	if storage != "pvc" && storage != "emptydir" {
		cleanup()
		return nil, fmt.Errorf("kubernetes: storage must be pvc or emptydir, got %q", storage)
	}
	runtime := choice.Default
	a := workspaceadapter.New(workspaceadapter.Config{
		RunsDir:         deps.RunsDir,
		WorkspaceDocker: deps.WorkspaceDocker,
		Kube: &workspaceadapter.KubeConfig{
			Kube:            &kube.Client{Kubeconfig: path, Context: cfg.Str("context", ""), Namespace: ns},
			SandboxImage:    cfg.Str("sandbox_image", d.SandboxImage),
			ShimImage:       cfg.Str("shim_image", d.ShimImage),
			ToolsImage:      cfg.Str("tools_image", d.ToolsImage),
			GatewayImage:    cfg.Str("gateway_image", d.GatewayImage),
			PullPolicy:      cfg.Str("image_pull_policy", d.PullPolicy),
			EngineURL:       engineURL,
			EngineNamespace: d.EngineNamespace,
			EngineCIDR:      cfg.Str("engine_cidr", ""),
			Forward:         d.Transport == "portforward",
			Storage:         storage,
			StorageClass:    cfg.Str("storage_class", ""),
			StorageSize:     cfg.Str("storage_size", ""),
			Runtime:         runtime,
			RuntimeClass:    runtimeClass,
			Runtimes:        choice.Approved,
			Firewall:        cfg.Str("firewall", "on") != "off",
			ProtectCIDRs:    d.ProtectCIDRs,
			Drift:           deps.Drift,
		},
	})
	return &kubernetesProvisioner{Adapter: a, cleanup: cleanup}, nil
}

// kubernetesRuntime is the integration's sandbox runtime and the runtime
// class override its pods name: its own runtime field, else its older
// privileged field (on: privileged; off, with a runtime class: sysbox), else
// the deployment's. A runtime field the older one contradicts is refused.
func kubernetesRuntime(cfg Config, d KubernetesDefaults) (envport.Runtime, string, error) {
	class := cfg.Str("runtime_class", d.RuntimeClass)
	privileged := strings.ToLower(cfg.Str("privileged", ""))
	switch privileged {
	case "", "on", "off":
	default:
		return "", "", fmt.Errorf("kubernetes: privileged must be on or off, got %q", privileged)
	}
	if name := cfg.Str("runtime", ""); name != "" {
		rt, err := envport.ParseRuntime(name)
		if err != nil {
			return "", "", fmt.Errorf("kubernetes: %w", err)
		}
		if privileged != "" && (privileged == "on") != (rt == envport.RuntimePrivileged) {
			return "", "", fmt.Errorf("kubernetes: privileged=%s contradicts runtime=%s: clear privileged", privileged, rt)
		}
		return rt, class, nil
	}
	switch privileged {
	case "on":
		return envport.RuntimePrivileged, class, nil
	case "off":
		if class == "" {
			return "", "", fmt.Errorf("kubernetes: privileged=off needs a runtime_class that can nest dockerd (e.g. sysbox-runc), or set runtime=sysbox")
		}
		return envport.RuntimeSysbox, class, nil
	}
	if d.Runtime == "" {
		return envport.RuntimePrivileged, class, nil
	}
	rt, err := envport.ParseRuntime(d.Runtime)
	if err != nil {
		return "", "", fmt.Errorf("kubernetes: the deployment's %w", err)
	}
	return rt, class, nil
}

// RuntimeChoice is what the integration offers projects (kinds.RuntimeOffering).
func (k Kubernetes) RuntimeChoice(cfg Config) (envport.RuntimeChoice, error) {
	c, _, err := kubernetesChoice(cfg, k.Defaults)
	return c, err
}

// kubernetesChoice is the integration's runtime choice: its default
// (kubernetesRuntime), and the runtimes it approves (its runtimes field,
// which must include the default; else the deployment's, the default added
// when they lack it), with what the workspace definition needs.
func kubernetesChoice(cfg Config, d KubernetesDefaults) (envport.RuntimeChoice, string, error) {
	def, class, err := kubernetesRuntime(cfg, d)
	if err != nil {
		return envport.RuntimeChoice{}, "", err
	}
	c := envport.RuntimeChoice{Default: def, Needs: workspaceadapter.Needs()}
	own := cfg.Str("runtimes", "")
	listed := d.Runtimes
	if own != "" {
		listed = strings.Split(own, ",")
	}
	for _, name := range listed {
		if strings.TrimSpace(name) == "" {
			continue
		}
		rt, err := envport.ParseRuntime(name)
		if err != nil {
			return envport.RuntimeChoice{}, "", fmt.Errorf("kubernetes: runtimes: %w", err)
		}
		if !slices.Contains(c.Approved, rt) {
			c.Approved = append(c.Approved, rt)
		}
	}
	if own == "" && !slices.Contains(c.Approved, def) {
		c.Approved = append([]envport.Runtime{def}, c.Approved...)
	}
	if err := c.Validate(); err != nil {
		return envport.RuntimeChoice{}, "", fmt.Errorf("kubernetes: %w", err)
	}
	storage := kubernetesStorage(cfg, c.Approved)
	for _, rt := range c.Approved {
		if err := workspaceadapter.CheckStorage(rt, storage); err != nil {
			return envport.RuntimeChoice{}, "", fmt.Errorf("kubernetes: %w", err)
		}
	}
	if err := workspaceadapter.CanStop(storage); err != nil {
		c.NoSleep = err.Error()
	}
	return c, class, nil
}

// kubernetesStorage is the integration's workspace storage: its own, else
// the approved runtimes' default (emptydir when one of them needs it).
func kubernetesStorage(cfg Config, approved []envport.Runtime) string {
	return strings.ToLower(cfg.Str("storage", workspaceadapter.DefaultStorage(approved)))
}

func runtimeNames() []string {
	var out []string
	for _, r := range envport.Runtimes() {
		out = append(out, string(r))
	}
	return out
}

var (
	_ ProvisionerKind = Kubernetes{}
	_ RuntimeOffering = Kubernetes{}
)
