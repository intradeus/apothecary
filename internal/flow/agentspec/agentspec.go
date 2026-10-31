// Package agentspec models the neutral, harness-agnostic agent
// definitions (DESIGN.md §5.1). Since FLOWLANG v3 an agent is declared
// INSIDE the flow document that runs it (PLAN5 M26): this package owns the
// schema, its validation and its content hash, and internal/flow embeds
// Spec directly as the shape of a flow's `agents:` entries. Each harness
// adapter translates specs into its native config (§5.2).
//
// It also owns the port types (FLOWLANG §4): every value a stage reads or
// writes is typed, and an agent's contract is where most of them are first
// stated.
package agentspec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// AskAgent is the name of the agent the engine prepares for a flow's Ask
// pane (FLOWLANG §1 ask): read-only, no shell, no questions of its own.
const AskAgent = "ask"

type Spec struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description,omitempty"`
	// ReadOnly denies the harness's edit/write/patch tools. Honesty note
	// (§5.1): with AllowBash true the agent can still mutate files through
	// shell — accepted v1 residual for the reviewer.
	ReadOnly  bool   `yaml:"read_only" json:"read_only,omitempty"`
	AllowBash bool   `yaml:"allow_bash" json:"allow_bash,omitempty"`
	Prompt    string `yaml:"prompt" json:"prompt"`
	// Inputs/Outputs are the agent's contract (FLOWLANG §4): what it reads
	// and writes, each typed — never where. A stage running the agent states
	// every one of them again as a port, with its source or destination, and
	// the parser holds the two together.
	Inputs  []InputDecl  `yaml:"inputs" json:"inputs,omitempty"`
	Outputs []OutputDecl `yaml:"outputs" json:"outputs,omitempty"`
	// MCP names the flow's `mcp_servers:` the agent may use (FLOWLANG §1):
	// tools beyond its harness's own. Checked against the flow by the parser.
	// Resolved for a run when a stage opens the agent's session (secrets
	// filled then, never here): see agentport.AgentInput.MCPServers.
	MCP []string `yaml:"mcp" json:"mcp,omitempty"`
}

// InputDecl is one value the agent reads, rendered into its prompt.
type InputDecl struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description,omitempty"`
	// Optional inputs need not be bound by a stage. An input a stage binds
	// to edge.failure or edge.verdict must be optional: those are empty
	// unless the stage was entered by that kind of edge.
	Optional bool `yaml:"optional" json:"optional,omitempty"`
	// Type and Schema say what the agent reads: a port type, one of the
	// flow's `schemas:`, or both (the schema narrowing a file type). At least
	// one is required.
	Type   string `yaml:"type" json:"type,omitempty"`
	Schema string `yaml:"schema" json:"schema,omitempty"`
}

// OutputDecl is one file the agent must write. Where is the stage's to say.
type OutputDecl struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description,omitempty"`
	Type        string `yaml:"type" json:"type,omitempty"`
	Schema      string `yaml:"schema" json:"schema,omitempty"`
	// LegacyPath is `path:`, which an output had before stages said where
	// it goes; read only to say so.
	LegacyPath string `yaml:"path" json:"-"`
}

// Port types (FLOWLANG §4). A file type is what a file holds:
//
//   - TypeFile is any file; the engine only checks it exists. It is the
//     supertype of the others: a reader of a file accepts any of them.
//   - TypeVerdict is a judgement — {"verdict":"APPROVE"|"REJECT",
//     "summary"?, "comments":[a remark, or {path, line?, end_line?, quote?,
//     body}]} — which the router reads: REJECT follows the stage's on_reject
//     edge. It is also the one file type a source of the engine gives:
//     edge.verdict, what the last verdict (an agent's, or a person's at a
//     gate) said, read as text.
//   - TypeReviewThreads is a review of a merge request, which post_review
//     reads: {summary, notes: [{path, line, kind, body, rationale, …}],
//     replies: [{thread, body, resolve, …}]} (reviewfile.Review).
//   - TypeReviewReplies is a review round's answers, which
//     reply_review_comments reads: a list of {thread, did, reply, resolve,
//     rationale, …} (reviewfile.Reply).
//   - TypeMRThreads is what is on the merge request, which
//     load_review_comments writes and the reviewing and fixing agents — and
//     a gate's page — read: {merge_request, threads: [...], dropped: [...]}
//     (reviewfile.Threads).
//   - TypeCommitMessage is a commit's message, which commit reads:
//     {subject, body?}.
//   - TypeFiles is a directory of files (its path ends in "/"): screenshots,
//     a report's pages. It holds at least one file; create_mr and
//     ticket_comment attach one. Unlike the others it is not one file, so
//     `file` does not accept it.
//
// A file type exists only for a structure something built in reads; a
// structure only the flow's own stages pass around is a flow schema.
//
// A value type is what the engine provides (run.*, edge.*): a ticket, a
// changeset (git's list of changed files), a failure, a link — and a
// verdict, from edge.verdict.
// Only a source of the engine gives one, and only an input reads one.
const (
	TypeFile          = "file"
	TypeVerdict       = "verdict"
	TypeReviewThreads = "review_threads"
	TypeReviewReplies = "review_replies"
	TypeMRThreads     = "mr_threads"
	TypeCommitMessage = "commit_message"
	TypeFiles         = "files"

	TypeTicket    = "ticket"
	TypeChangeset = "changeset"
	TypeFailure   = "failure"
	TypeLink      = "link"
)

// FileTypes and ValueTypes list the port types by kind.
var (
	FileTypes  = []string{TypeFile, TypeVerdict, TypeReviewThreads, TypeReviewReplies, TypeMRThreads, TypeCommitMessage, TypeFiles}
	ValueTypes = []string{TypeTicket, TypeChangeset, TypeFailure, TypeLink}
)

// IsFileType reports whether t is what a file holds.
func IsFileType(t string) bool { return slices.Contains(FileTypes, t) }

// IsValueType reports whether t is a value the engine provides.
func IsValueType(t string) bool { return slices.Contains(ValueTypes, t) }

// IsStructuredType reports whether a file of type t has a shape the engine
// decodes — JSON or YAML, so its path says which.
func IsStructuredType(t string) bool { return IsFileType(t) && t != TypeFile && t != TypeFiles }

// CheckShape validates one port's type and schema, wherever it is declared:
// at least one of them; a known type; a schema only on a file, and never
// beside `file` (the schema alone says what the file is).
func CheckShape(typ, schema string, outputOnly bool) error {
	if typ == "" && schema == "" {
		return fmt.Errorf("type or schema is required: one of %s, or a schema declared under schemas", strings.Join(append(slices.Clone(FileTypes), ValueTypes...), ", "))
	}
	if typ != "" && !IsFileType(typ) && !IsValueType(typ) {
		return fmt.Errorf("unknown type %q (%s)", typ, strings.Join(append(slices.Clone(FileTypes), ValueTypes...), ", "))
	}
	if outputOnly && IsValueType(typ) {
		return fmt.Errorf("type %q is a value the engine provides; what is written is a file (%s)", typ, strings.Join(FileTypes, ", "))
	}
	if schema != "" {
		if !validSlug(schema) {
			return fmt.Errorf("schema must name one of the flow's schemas (a lowercase slug), got %q", schema)
		}
		if IsValueType(typ) {
			return fmt.Errorf("a schema describes a file; a %s is a value the engine provides", typ)
		}
		if typ == TypeFile {
			return fmt.Errorf("type file beside a schema: the schema alone says what the file is — drop type")
		}
		if typ == TypeFiles {
			return fmt.Errorf("a schema describes one file; files is a directory of them")
		}
	}
	return nil
}

// Input returns the declared input named name, or nil.
func (s *Spec) Input(name string) *InputDecl {
	if s == nil {
		return nil
	}
	for i := range s.Inputs {
		if s.Inputs[i].Name == name {
			return &s.Inputs[i]
		}
	}
	return nil
}

// Output returns the declared output named name, or nil.
func (s *Spec) Output(name string) *OutputDecl {
	if s == nil {
		return nil
	}
	for i := range s.Outputs {
		if s.Outputs[i].Name == name {
			return &s.Outputs[i]
		}
	}
	return nil
}

// TypedOutput returns the spec's output of the given built-in type, or nil.
// An agent declares at most one of each structured type.
func (s *Spec) TypedOutput(typ string) *OutputDecl {
	if s == nil || !IsStructuredType(typ) {
		return nil
	}
	for i := range s.Outputs {
		if s.Outputs[i].Type == typ {
			return &s.Outputs[i]
		}
	}
	return nil
}

// VerdictOutput returns the spec's verdict-typed output, or nil when the
// agent gives no verdict. Its presence is what makes a stage's on_reject
// edge reachable (there is no stage-level verdict flag).
func (s *Spec) VerdictOutput() *OutputDecl { return s.TypedOutput(TypeVerdict) }

// descMax bounds a decl description so N inputs cannot bloat every prompt.
const descMax = 500

// Validate checks a spec independent of where it was authored: the name is a
// strict slug (it becomes workspace file paths and CLI arguments, so
// repo-controlled text can never carry shell- or path-meaningful
// characters), the prompt is present, and the I/O contract holds. The single
// gate for flow-inline agents (FLOWLANG §1).
func Validate(s *Spec) error {
	if !validSlug(s.Name) {
		return fmt.Errorf("name must match [a-z0-9][a-z0-9._-]* (lowercase slug), got %q", s.Name)
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return fmt.Errorf("prompt is required")
	}
	seen := map[string]bool{}
	for _, m := range s.MCP {
		if !validSlug(m) {
			return fmt.Errorf("mcp: %q must name one of the flow's mcp_servers (a lowercase slug)", m)
		}
		if seen[m] {
			return fmt.Errorf("mcp: %q listed twice", m)
		}
		seen[m] = true
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
	MCP         []string     `json:"mcp,omitempty"`
}

// Hash is an agent's content address: sha256[:16] of the canonical JSON.
// Recorded per attempt in stage_started.agent_hash, so outcome analytics
// join to the exact prompt version that produced them (DESIGN3 §1).
func Hash(s *Spec) string {
	data, _ := json.Marshal(content{
		Name: s.Name, Description: s.Description,
		ReadOnly: s.ReadOnly, AllowBash: s.AllowBash, Prompt: s.Prompt,
		Inputs: s.Inputs, Outputs: s.Outputs, MCP: s.MCP,
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

// ValidateIO checks a spec's declared inputs and outputs: names are unique
// slugs, descriptions are bounded, every one is typed, and an agent writes
// at most one file of each structured type.
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
		if err := CheckShape(in.Type, in.Schema, false); err != nil {
			return fmt.Errorf("input %q: %v", in.Name, err)
		}
	}
	seenOut := make(map[string]bool, len(outputs))
	typed := map[string]bool{}
	for _, out := range outputs {
		if !validSlug(out.Name) {
			return fmt.Errorf("output name must match [a-z0-9][a-z0-9._-]* (lowercase slug), got %q", out.Name)
		}
		if seenOut[out.Name] {
			return fmt.Errorf("output %q declared twice", out.Name)
		}
		seenOut[out.Name] = true
		if out.LegacyPath != "" {
			return fmt.Errorf("output %q: path is the stage's to say — an agent says what it writes; a stage running it writes `output: { %s: { to: %s, type: … } }`", out.Name, out.Name, out.LegacyPath)
		}
		if err := CheckShape(out.Type, out.Schema, true); err != nil {
			return fmt.Errorf("output %q: %v", out.Name, err)
		}
		if IsStructuredType(out.Type) {
			if typed[out.Type] {
				return fmt.Errorf("output %q: an agent may declare at most one %s output", out.Name, out.Type)
			}
			typed[out.Type] = true
		}
		if len(out.Description) > descMax {
			return fmt.Errorf("output %q: description exceeds %d characters", out.Name, descMax)
		}
	}
	return nil
}

// StructuredPath reports whether a path names a file the engine can decode
// to check a shape: JSON or YAML, told apart by the extension.
func StructuredPath(p string) bool {
	return strings.HasSuffix(p, ".json") || strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml")
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
