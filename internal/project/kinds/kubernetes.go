package kinds

import (
	"fmt"
	"path/filepath"
	"strings"

	"apothecary/internal/adapters/kube"
	workspaceadapter "apothecary/internal/adapters/workspace"
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
	// RuntimeClass and Privileged are the sandbox shape an integration gets
	// when it leaves its own fields empty (APOTHECARY_K8S_RUNTIME_CLASS,
	// APOTHECARY_K8S_PRIVILEGED=on|off). Privileged "" means on.
	RuntimeClass, Privileged string
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
		Help: "Runs each workspace as a privileged Docker-in-Docker pod (or a sysbox RuntimeClass). Empty fields use the deployment's APOTHECARY_K8S_* settings.",
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
			{Name: "storage", Label: "Workspace storage (pvc/emptydir)", Placeholder: "pvc", Default: "pvc"},
			{Name: "storage_class", Label: "Storage class", Help: "Must be a local or block filesystem: the inner docker daemon cannot run on NFS."},
			{Name: "storage_size", Label: "Storage size per run", Placeholder: "20Gi"},
			{Name: "privileged", Label: "Privileged sandboxes (on/off)", Placeholder: orDefault(d.Privileged, "on"),
				Help: "off needs a runtime class able to nest dockerd unprivileged (sysbox). The pod then also gets a user namespace of its own (hostUsers: false), which containerd 2.x requires for sysbox: containerd 2.0.5+, sysbox 0.7+ (README, cluster requirements)."},
			{Name: "runtime_class", Label: "Runtime class", Placeholder: orDefault(d.RuntimeClass, "sysbox-runc"),
				Help: "A class that nests dockerd unprivileged, installed on the nodes. With privileged off the pod is created with hostUsers: false (README, cluster requirements)."},
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
	storage := strings.ToLower(cfg.Str("storage", "pvc"))
	if storage != "pvc" && storage != "emptydir" {
		cleanup()
		return nil, fmt.Errorf("kubernetes: storage must be pvc or emptydir, got %q", storage)
	}
	runtimeClass := cfg.Str("runtime_class", d.RuntimeClass)
	privileged := cfg.Str("privileged", orDefault(d.Privileged, "on")) != "off"
	if !privileged && runtimeClass == "" {
		cleanup()
		return nil, fmt.Errorf("kubernetes: privileged=off needs a runtime_class that can nest dockerd (e.g. sysbox-runc)")
	}
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
			Privileged:      privileged,
			RuntimeClass:    runtimeClass,
			Firewall:        cfg.Str("firewall", "on") != "off",
			ProtectCIDRs:    d.ProtectCIDRs,
			Drift:           deps.Drift,
		},
	})
	return &kubernetesProvisioner{Adapter: a, cleanup: cleanup}, nil
}

var _ ProvisionerKind = Kubernetes{}
