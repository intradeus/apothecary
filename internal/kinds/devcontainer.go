package kinds

import (
	"fmt"

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
		},
	}
}

func (Devcontainer) New(cfg Config, deps ProvisionerDeps) (Provisioner, error) {
	// The template override is a Coder concept; here the workspace
	// definition is the repo's devcontainer (or the §8 pipeline image as
	// fallback), so a project pinning workspace.template is unresolvable,
	// not silently ignored.
	if deps.Template != "" {
		return nil, fmt.Errorf("the devcontainer provisioner has no custom-template path: clear the project's workspace.template")
	}
	return devcontaineradapter.New(devcontaineradapter.Config{
		DevcontainerBin: cfg.Str("devcontainer_path", "devcontainer"),
		DockerBin:       cfg.Str("docker_path", "docker"),
		RunsDir:         deps.RunsDir,
		EnginePort:      deps.EnginePort,
		ProxyPort:       deps.ProxyPort,
	}), nil
}

var _ ProvisionerKind = Devcontainer{}
