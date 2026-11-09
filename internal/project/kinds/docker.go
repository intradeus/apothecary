package kinds

import (
	workspaceadapter "apothecary/internal/adapters/workspace"
)

// Docker is the provisioner/docker kind: each run's sandbox is a
// Docker-in-Docker container on the engine host's docker daemon, and the
// workspace inside it is whatever the repo defines (its devcontainer.json),
// built by the reference @devcontainers/cli. No server, no secrets — both
// CLIs are local.
type Docker struct{}

func (Docker) Descriptor() Descriptor {
	return Descriptor{
		Port: "provisioner", Kind: "docker", Label: "Docker (sandbox per run)",
		Fields: []Field{
			{Name: "devcontainer_path", Label: "devcontainer CLI", Placeholder: "devcontainer"},
			{Name: "docker_path", Label: "docker CLI", Placeholder: "docker"},
			{Name: "firewall", Label: "Egress firewall (on/off)", Placeholder: "on", Default: "on"},
			{Name: "dind_image", Label: "DinD sandbox image", Placeholder: "docker:29-dind"},
		},
	}
}

func (Docker) New(cfg Config, deps ProvisionerDeps) (Provisioner, error) {
	return workspaceadapter.New(workspaceadapter.Config{
		DevcontainerBin: cfg.Str("devcontainer_path", "devcontainer"),
		DockerBin:       cfg.Str("docker_path", "docker"),
		RunsDir:         deps.RunsDir,
		EnginePort:      deps.EnginePort,
		ShimBin:         deps.ShimBin,
		ToolsDir:        deps.ToolsDir,
		GatewayImage:    deps.GatewayImage,
		GatewayOff:      cfg.Str("firewall", "on") == "off",
		DinDImage:       cfg.Str("dind_image", ""), // "" ⇒ adapter default
		// Deployment-wide, not a form field: it names a runtime the HOST
		// daemon must have installed (APOTHECARY_SANDBOX_RUNTIME).
		SandboxRuntime:  deps.SandboxRuntime,
		WorkspaceDocker: deps.WorkspaceDocker,
	}), nil
}

var _ ProvisionerKind = Docker{}
