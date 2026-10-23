// Package agentspec models the neutral, harness-agnostic agent
// definitions (DESIGN.md §5.1). Since FLOWLANG v3 an agent is declared
// INSIDE the flow document that runs it (PLAN5 M26): this package owns the
// schema, its validation and its content hash, and internal/flow embeds
// Spec directly as the shape of a flow's `agents:` entries. Each harness
// adapter translates specs into its native config (§5.2).
package agentspec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

type Spec struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description,omitempty"`
	// ReadOnly denies the harness's edit/write/patch tools. Honesty note
	// (§5.1): with AllowBash true the agent can still mutate files through
	// shell — accepted v1 residual for the reviewer.
	ReadOnly  bool   `yaml:"read_only" json:"read_only,omitempty"`
	AllowBash bool   `yaml:"allow_bash" json:"allow_bash,omitempty"`
	Prompt    string `yaml:"prompt" json:"prompt"`
	// Inputs/Outputs are the agent's declared I/O contract (DESIGN4 §1). A
	// flow stage binds each declared input to a workspace path (or a sentinel)
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
	// Optional inputs need not be bound by a stage. An input a flow binds to
	// $DECISION or $FAILURE must be optional: those are empty on a stage's
	// first attempt (the flow validator enforces it, since only the flow knows
	// what a stage bound).
	Optional bool `yaml:"optional" json:"optional,omitempty"`
}

// OutputDecl is one file the agent must produce. Path is the default location;
// a stage may rebind it. Type gives the file a contract beyond "it exists"
// (FLOWLANG §4): the empty type is an opaque file; OutputTypeVerdict is the
// APPROVE/REJECT decision the engine parses and routes on.
type OutputDecl struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description,omitempty"`
	Path        string `yaml:"path" json:"path"`
	// Type is omitempty on purpose: adding it left every existing agent's
	// content hash untouched (confstore.AgentHash marshals this struct).
	Type string `yaml:"type" json:"type,omitempty"`
}

// Output types. OutputTypeFile (the empty default) is an opaque artifact:
// the engine only checks it exists. OutputTypeVerdict is the structured
// decision {"verdict":"APPROVE"|"REJECT","comments":[...]} — the engine
// parses it and routes REJECT down the stage's on_reject edge. A spec may
// declare at most one verdict output; the flow stage needs no flag for it.
//
// OutputTypeReviewReplies is a review round's answers (PLAN6 M31): a YAML
// list of {thread, reply, resolve}, validated when the stage ends against
// the threads the round loaded, before anything is posted.
//
// OutputTypeReviewThreads is a review of a merge request, for post_review:
// YAML {summary, threads: [{path, line, body}]}, validated when the stage
// ends.
const (
	OutputTypeFile          = ""
	OutputTypeVerdict       = "verdict"
	OutputTypeReviewReplies = "review_replies"
	OutputTypeReviewThreads = "review_threads"
)

// ReviewThreadsOutput returns the spec's review_threads-typed output, or nil.
func (s *Spec) ReviewThreadsOutput() *OutputDecl {
	if s == nil {
		return nil
	}
	for i := range s.Outputs {
		if s.Outputs[i].Type == OutputTypeReviewThreads {
			return &s.Outputs[i]
		}
	}
	return nil
}

// ReviewRepliesOutput returns the spec's review_replies-typed output, or nil.
func (s *Spec) ReviewRepliesOutput() *OutputDecl {
	if s == nil {
		return nil
	}
	for i := range s.Outputs {
		if s.Outputs[i].Type == OutputTypeReviewReplies {
			return &s.Outputs[i]
		}
	}
	return nil
}

// VerdictOutput returns the spec's verdict-typed output, or nil when the
// agent renders no decision. Its presence is what makes a stage's on_reject
// edge reachable (there is no stage-level verdict flag).
func (s *Spec) VerdictOutput() *OutputDecl {
	if s == nil {
		return nil
	}
	for i := range s.Outputs {
		if s.Outputs[i].Type == OutputTypeVerdict {
			return &s.Outputs[i]
		}
	}
	return nil
}

// descMax bounds a decl description so N inputs cannot bloat every prompt.
const descMax = 500

// Validate checks a spec independent of where it was authored: the name is a
// strict slug (it becomes workspace file paths and CLI arguments, so
// repo-controlled text can never carry shell- or path-meaningful
// characters), the prompt is present, and the I/O contract holds. The single
// gate for flow-inline agents (FLOWLANG §1) and the retired file importer.
func Validate(s *Spec) error {
	if !validSlug(s.Name) {
		return fmt.Errorf("name must match [a-z0-9][a-z0-9._-]* (lowercase slug), got %q", s.Name)
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return fmt.Errorf("prompt is required")
	}
	return ValidateIO(s.Inputs, s.Outputs)
}

// content is the canonical hashed shape: a stable field order (json.Marshal
// emits declaration order) so the same definition always addresses the same
// content. Inputs/Outputs are omitempty so a spec with no declared I/O
// hashes identically to one whose slices are nil.
type content struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	ReadOnly    bool         `json:"read_only"`
	AllowBash   bool         `json:"allow_bash"`
	Prompt      string       `json:"prompt"`
	Inputs      []InputDecl  `json:"inputs,omitempty"`
	Outputs     []OutputDecl `json:"outputs,omitempty"`
}

// Hash is an agent's content address: sha256[:16] of the canonical JSON.
// Recorded per attempt in stage_started.agent_hash, so outcome analytics
// join to the exact prompt version that produced them (DESIGN3 §1).
func Hash(s *Spec) string {
	data, _ := json.Marshal(content{
		Name: s.Name, Description: s.Description,
		ReadOnly: s.ReadOnly, AllowBash: s.AllowBash, Prompt: s.Prompt,
		Inputs: s.Inputs, Outputs: s.Outputs,
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
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
	}
	seenOut := make(map[string]bool, len(outputs))
	seenPath := make(map[string]bool, len(outputs))
	verdicts, replies, reviews := 0, 0, 0
	for _, out := range outputs {
		if !validSlug(out.Name) {
			return fmt.Errorf("output name must match [a-z0-9][a-z0-9._-]* (lowercase slug), got %q", out.Name)
		}
		if seenOut[out.Name] {
			return fmt.Errorf("output %q declared twice", out.Name)
		}
		seenOut[out.Name] = true
		switch out.Type {
		case OutputTypeFile:
		case OutputTypeVerdict:
			verdicts++
			if verdicts > 1 {
				return fmt.Errorf("output %q: an agent may declare at most one %s output", out.Name, OutputTypeVerdict)
			}
		case OutputTypeReviewReplies:
			replies++
			if replies > 1 {
				return fmt.Errorf("output %q: an agent may declare at most one %s output", out.Name, OutputTypeReviewReplies)
			}
		case OutputTypeReviewThreads:
			reviews++
			if reviews > 1 {
				return fmt.Errorf("output %q: an agent may declare at most one %s output", out.Name, OutputTypeReviewThreads)
			}
		default:
			return fmt.Errorf("output %q: unknown type %q (%q, %q, %q or %q)", out.Name, out.Type, OutputTypeFile, OutputTypeVerdict, OutputTypeReviewReplies, OutputTypeReviewThreads)
		}
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
