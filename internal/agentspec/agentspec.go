// Package agentspec parses the neutral agent definitions in
// .apothecary/agents/<name>.yaml (DESIGN.md §5.1). These are harness-agnostic;
// each harness adapter translates them into its native config (§5.2).
package agentspec

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type Spec struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// ReadOnly denies the harness's edit/write/patch tools. Honesty note
	// (§5.1): with AllowBash true the agent can still mutate files through
	// shell — accepted v1 residual for the reviewer.
	ReadOnly  bool   `yaml:"read_only"`
	AllowBash bool   `yaml:"allow_bash"`
	Prompt    string `yaml:"prompt"`
}

// Parse decodes one spec; stem is the filename without extension and MUST
// equal the name field (§5.1: name = filename stem).
func Parse(stem string, data []byte) (*Spec, error) {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	var s Spec
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("agent spec %s: %w", stem, err)
	}
	if s.Name == "" {
		return nil, fmt.Errorf("agent spec %s: name is required", stem)
	}
	if s.Name != stem {
		return nil, fmt.Errorf("agent spec %s: name %q must equal the filename stem", stem, s.Name)
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return nil, fmt.Errorf("agent spec %s: prompt is required", stem)
	}
	return &s, nil
}
