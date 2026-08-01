package kinds

import (
	devcontaineradapter "apothecary/internal/adapters/devcontainer"
)

// Devcontainer is the provisioner/devcontainer kind: the target repo's own
// .devcontainer/devcontainer.json, built and run by the reference
// @devcontainers/cli against the engine host's docker daemon. No server,
// no secrets — both CLIs are local.
type Devcontainer struct{}

func (Devcontainer) Descriptor() Descriptor {
	return Descriptor{
		Port: "provisioner", Kind: "devcontainer", Label: "Dev Container (repo-defined)",
		Fields: []Field{
			{Name: "devcontainer_path", Label: "devcontainer CLI", Placeholder: "devcontainer"},
			{Name: "docker_path", Label: "docker CLI", Placeholder: "docker"},
			{Name: "firewall", Label: "Egress firewall (on/off)", Placeholder: "on", Default: "on"},
			{Name: "dind_image", Label: "DinD sandbox image", Placeholder: "docker:29-dind"},
		},
	}
}

func (Devcontainer) New(cfg Config, deps ProvisionerDeps) (Provisioner, error) {
	return devcontaineradapter.New(devcontaineradapter.Config{
		DevcontainerBin: cfg.Str("devcontainer_path", "devcontainer"),
		DockerBin:       cfg.Str("docker_path", "docker"),
		RunsDir:         deps.RunsDir,
		EnginePort:      deps.EnginePort,
		ShimBin:         deps.ShimBin,
		GatewayImage:    deps.GatewayImage,
		GatewayOff:      cfg.Str("firewall", "on") == "off",
		DinDImage:       cfg.Str("dind_image", ""), // "" ⇒ adapter default
		// Deployment-wide, not a form field: it names a runtime the HOST
		// daemon must have installed (APOTHECARY_SANDBOX_RUNTIME).
		SandboxRuntime: deps.SandboxRuntime,
	}), nil
}

var _ ProvisionerKind = Devcontainer{}
