package kinds

import (
	coderadapter "factory/internal/adapters/coder"
)

// Coder is the provisioner/coder kind (DESIGN2 §4.1).
type Coder struct{}

func (Coder) Descriptor() Descriptor {
	return Descriptor{
		Port: "provisioner", Kind: "coder", Label: "Coder",
		Fields: []Field{
			{Name: "url", Label: "Coder server URL", Placeholder: "http://localhost:7080", Required: true},
			{Name: "token", Label: "Session token", Secret: true, Required: true},
			{Name: "template", Label: "Template name", Default: "factory", Required: true},
		},
	}
}

func (Coder) New(cfg Config, deps ProvisionerDeps) (Provisioner, error) {
	template := deps.Template
	if template == "" {
		template = cfg.Str("template", "factory")
	}
	return coderadapter.New(coderadapter.Config{
		URL:           cfg.Str("url", ""),
		Token:         cfg.Secrets["token"],
		Template:      template,
		RunsDir:       deps.RunsDir,
		EnginePort:    deps.EnginePort,
		ExtraForwards: cfg.Ints("extra_forwards"),
	}), nil
}

var _ ProvisionerKind = Coder{}
