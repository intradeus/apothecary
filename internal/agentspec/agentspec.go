// Package agentspec models the neutral, harness-agnostic agent
// definitions (DESIGN.md §5.1). Since v3 they live in the per-project agent
// library (DESIGN3 §1); Parse remains for the one-time repo import of the
// retired .apothecary/agents/*.yaml contract. Each harness adapter
// translates specs into its native config (§5.2).
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
	// Inputs/Outputs are the agent's declared I/O contract (DESIGN4 §1). A
	// flow stage binds each declared input to a workspace path (or $CHANGED)
	// and may rebind output paths; prompt assembly renders named, described
	// sections and enforces the output paths. All optional — a spec with no
	// decls behaves exactly as before (legacy list/string stage bindings).
	Inputs  []InputDecl  `yaml:"inputs" json:"inputs,omitempty"`
	Outputs []OutputDecl `yaml:"outputs" json:"outputs,omitempty"`
}

// InputDecl is one declared input the agent expects in its prompt.
type InputDecl struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description,omitempty"`
	// Optional inputs need not be bound by a stage. The reserved input named
	// InputFeedback (filled by edge feed:, never stage-bound) MUST be optional.
	Optional bool `yaml:"optional" json:"optional,omitempty"`
}

// OutputDecl is one file the agent must produce. Path is the default location;
// a stage may rebind it.
type OutputDecl struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description,omitempty"`
	Path        string `yaml:"path" json:"path"`
}

// InputFeedback is the reserved input name filled by edge feed: content (the
// "PREVIOUS ATTEMPT FEEDBACK" channel). It is never bound in a stage input
// map; declaring it only renames/describes the retry prompt section.
const InputFeedback = "feedback"

// descMax bounds a decl description so N inputs cannot bloat every prompt.
const descMax = 500

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
	// The name becomes workspace file paths and CLI arguments (role files,
	// --agent): keep it a strict slug so repo-controlled filenames can never
	// carry shell- or path-meaningful characters.
	if !validSlug(s.Name) {
		return nil, fmt.Errorf("agent spec %s: name must match [a-z0-9][a-z0-9._-]* (lowercase slug)", stem)
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return nil, fmt.Errorf("agent spec %s: prompt is required", stem)
	}
	if err := ValidateIO(s.Inputs, s.Outputs); err != nil {
		return nil, fmt.Errorf("agent spec %s: %w", stem, err)
	}
	return &s, nil
}

// ValidateIO checks a spec's declared inputs and outputs: names are unique
// slugs, descriptions are bounded, the reserved feedback input is optional,
// and every output has a unique, safe artifact path. Shared by the file
// parser and the DB-backed agent library so both reject the same shapes.
func ValidateIO(inputs []InputDecl, outputs []OutputDecl) error {
	seenIn := make(map[string]bool, len(inputs))
	for _, in := range inputs {
		if !validSlug(in.Name) {
			return fmt.Errorf("input name must match [a-z0-9][a-z0-9._-]* (lowercase slug), got %q", in.Name)
		}
		if seenIn[in.Name] {
			return fmt.Errorf("input %q declared twice", in.Name)
		}
		seenIn[in.Name] = true
		if len(in.Description) > descMax {
			return fmt.Errorf("input %q: description exceeds %d characters", in.Name, descMax)
		}
		if in.Name == InputFeedback && !in.Optional {
			return fmt.Errorf("input %q is filled by edge feedback and must be optional", InputFeedback)
		}
	}
	seenOut := make(map[string]bool, len(outputs))
	seenPath := make(map[string]bool, len(outputs))
	for _, out := range outputs {
		if !validSlug(out.Name) {
			return fmt.Errorf("output name must match [a-z0-9][a-z0-9._-]* (lowercase slug), got %q", out.Name)
		}
		if seenOut[out.Name] {
			return fmt.Errorf("output %q declared twice", out.Name)
		}
		seenOut[out.Name] = true
		if len(out.Description) > descMax {
			return fmt.Errorf("output %q: description exceeds %d characters", out.Name, descMax)
		}
		if !validArtifactPath(out.Path) {
			return fmt.Errorf("output %q: path %q must be a repo-relative artifact path (no leading /, no . or .. segments, no $)", out.Name, out.Path)
		}
		if seenPath[out.Path] {
			return fmt.Errorf("output path %q declared twice", out.Path)
		}
		seenPath[out.Path] = true
	}
	return nil
}

// validArtifactPath mirrors flow.validArtifactPath (agentspec must not import
// flow — flow imports agentspec). A repo-root-relative path: non-empty, no
// leading "/" or "$", no "." or ".." segments.
func validArtifactPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "$") {
		return false
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// ValidName reports whether s is a legal agent name (lowercase slug; the
// name becomes workspace file paths and CLI arguments). Shared by the
// file parser and the DB-backed agent library (DESIGN3 §1).
func ValidName(s string) bool { return validSlug(s) }

func validSlug(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case i > 0 && (r == '-' || r == '_' || r == '.'):
		default:
			return false
		}
	}
	return true
}
