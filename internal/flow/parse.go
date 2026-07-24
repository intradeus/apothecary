package flow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"apothecary/internal/agentspec"
)

// Error is a flow validation error pointing at a line in the source file.
type Error struct {
	File string
	Line int
	Msg  string
}

func (e *Error) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Msg)
	}
	return fmt.Sprintf("%s: %s", e.File, e.Msg)
}

const (
	defaultTimeout       = 30 * time.Minute
	defaultMaxTurns      = 50
	defaultMaxCostUSD    = 10.0
	defaultMaxTotalTurns = 300
)

var slugRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Doc* mirror the YAML schema exactly (unknown fields rejected by the
// strict decoder; pointers distinguish "absent" from explicit zeros). They
// are exported with JSON tags as the graph editor's document model
// (DESIGN3 §2.3): ParseDoc hands the raw shape to the UI, which serializes
// its edits back to YAML.
type Doc struct {
	Name        string      `yaml:"name" json:"name"`
	Description string      `yaml:"description" json:"description,omitempty"`
	Defaults    DocDefaults `yaml:"defaults" json:"defaults"`
	Budget      DocBudget   `yaml:"budget" json:"budget"`
	Stages      []DocStage  `yaml:"stages" json:"stages"`
}

type DocDefaults struct {
	Timeout  string `yaml:"timeout" json:"timeout,omitempty"`
	Harness  string `yaml:"harness" json:"harness,omitempty"`
	Model    string `yaml:"model" json:"model"`
	MaxTurns *int   `yaml:"max_turns" json:"max_turns,omitempty"`
}

type DocBudget struct {
	MaxCostUSD    *float64 `yaml:"max_cost_usd" json:"max_cost_usd,omitempty"`
	MaxTotalTurns *int     `yaml:"max_total_turns" json:"max_total_turns,omitempty"`
}

type DocStage struct {
	ID           string      `yaml:"id" json:"id"`
	Type         string      `yaml:"type" json:"type"`
	Agent        string      `yaml:"agent" json:"agent,omitempty"`
	Model        string      `yaml:"model" json:"model,omitempty"`
	Harness      string      `yaml:"harness" json:"harness,omitempty"`
	MaxTurns     *int        `yaml:"max_turns" json:"max_turns,omitempty"`
	Timeout      string      `yaml:"timeout" json:"timeout,omitempty"`
	Input        *DocInputs  `yaml:"input" json:"input,omitempty"`
	Output       *DocOutputs `yaml:"output" json:"output,omitempty"`
	Verdict      bool        `yaml:"verdict" json:"verdict,omitempty"`
	Run          string      `yaml:"run" json:"run,omitempty"`
	RunRef       string      `yaml:"run_ref" json:"run_ref,omitempty"`
	Ping         string      `yaml:"ping" json:"ping,omitempty"`
	Payload      string      `yaml:"payload" json:"payload,omitempty"`
	AutoComplete string      `yaml:"auto_complete" json:"auto_complete,omitempty"`
	Body         string      `yaml:"body" json:"body,omitempty"`
	OnSuccess    string      `yaml:"on_success" json:"on_success,omitempty"`
	OnFail       *DocEdge    `yaml:"on_fail" json:"on_fail,omitempty"`
	OnReject     *DocEdge    `yaml:"on_reject" json:"on_reject,omitempty"`
	OnTimeout    *DocEdge    `yaml:"on_timeout" json:"on_timeout,omitempty"`
}

// edges maps the declared edge kinds to their raw fields. Adding a kind
// (DESIGN3 §2.1): one DocStage field, one row here, one edgeKinds row, one
// raise site in the engine — parse, validation, routing and the edge_taken
// event generalize.
func (r *DocStage) edges() map[string]*DocEdge {
	return map[string]*DocEdge{EdgeFail: r.OnFail, EdgeReject: r.OnReject, EdgeTimeout: r.OnTimeout}
}

// edgeKindSpec declares one recoverable edge kind: where it may appear and
// which feeds it can carry.
type edgeKindSpec struct {
	on     []StageType
	feedOK func(f Feed, st StageType) bool
}

var edgeKinds = map[string]edgeKindSpec{
	EdgeFail: {
		on:     []StageType{StageAgent, StageCommand},
		feedOK: func(f Feed, _ StageType) bool { return f == "" || f == FeedOutput },
	},
	EdgeReject: {
		on: []StageType{StageAgent, StageHumanGate},
		feedOK: func(f Feed, st StageType) bool {
			return f == "" || (f == FeedComments && st == StageAgent) || (f == FeedFeedback && st == StageHumanGate)
		},
	},
	// on_timeout routes stage timeouts separately from failures ("stuck ≠
	// broken": retry the same stage fresh vs. go fix the code). When a stage
	// declares no on_timeout, the engine falls back to on_fail — existing
	// flows behave exactly as before.
	EdgeTimeout: {
		on:     []StageType{StageAgent, StageCommand},
		feedOK: func(f Feed, _ StageType) bool { return f == "" || f == FeedOutput },
	},
}

type DocEdge struct {
	To   string   `yaml:"to" json:"to"`
	Max  *int     `yaml:"max" json:"max,omitempty"`
	Feed string   `yaml:"feed" json:"feed,omitempty"`
	Then *DocEdge `yaml:"then" json:"then,omitempty"`
}

// UnmarshalYAML accepts the mapping form ({to, max, feed, then}) and, as
// sugar, a bare terminal name — `on_reject: abort` reads exactly like what
// it does. A custom unmarshaler bypasses the decoder's KnownFields
// strictness, so unknown keys are rejected by hand.
func (e *DocEdge) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Decode(&e.To)
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			var err error
			switch k.Value {
			case "to":
				err = v.Decode(&e.To)
			case "max":
				e.Max = new(int)
				err = v.Decode(e.Max)
			case "feed":
				err = v.Decode(&e.Feed)
			case "then":
				e.Then = &DocEdge{}
				err = v.Decode(e.Then)
			default:
				return fmt.Errorf("line %d: field %s not found in type flow.DocEdge", k.Line, k.Value)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("line %d: edge must be a mapping or %q", n.Line, TerminalAbort)
}

// IOPair is one named binding in a stage's map-form input/output. Line is
// retained for error reporting; it is not part of the JSON document shape.
type IOPair struct {
	Name  string
	Value string
	Line  int
}

// DocInputs is a stage's input binding in the document model: either the
// legacy ordered list (ticket | $CHANGED | artifact path) or the named map
// form ({name: path|$CHANGED}). Mapped records which form was written so an
// empty map is distinguishable from an empty list. Pairs preserves document
// order — prompts and editor round-trips must stay deterministic.
type DocInputs struct {
	Mapped bool
	List   []string
	Pairs  []IOPair
}

// DocOutputs is a stage's output binding: either the legacy single path or
// the named map form ({decl-name: path}) that rebinds declared output paths.
type DocOutputs struct {
	Mapped bool
	Str    string
	Pairs  []IOPair
}

// UnmarshalYAML accepts the legacy list or the {name: path} mapping. A custom
// unmarshaler bypasses the decoder's KnownFields strictness, so duplicate
// keys and non-scalar values are rejected by hand (as DocEdge does).
func (d *DocInputs) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.SequenceNode:
		return n.Decode(&d.List)
	case yaml.MappingNode:
		d.Mapped = true
		pairs, err := decodeYAMLPairs(n, "input")
		if err != nil {
			return err
		}
		d.Pairs = pairs
		return nil
	case yaml.ScalarNode:
		if n.Tag == "!!null" { // `input:` with no value — treat as absent
			return nil
		}
	}
	return fmt.Errorf("line %d: input must be a list or a {name: path} mapping", n.Line)
}

// UnmarshalYAML accepts the legacy single path or a {decl-name: path} mapping.
func (d *DocOutputs) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			return nil
		}
		return n.Decode(&d.Str)
	case yaml.MappingNode:
		d.Mapped = true
		pairs, err := decodeYAMLPairs(n, "output")
		if err != nil {
			return err
		}
		d.Pairs = pairs
		return nil
	}
	return fmt.Errorf("line %d: output must be a path or a {name: path} mapping", n.Line)
}

// decodeYAMLPairs reads an ordered mapping node into IOPairs, rejecting
// duplicate keys and non-scalar values. kind names the field in errors.
func decodeYAMLPairs(n *yaml.Node, kind string) ([]IOPair, error) {
	seen := map[string]bool{}
	var pairs []IOPair
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if v.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("line %d: %s %q: value must be a path or %s", v.Line, kind, k.Value, InputChanged)
		}
		if seen[k.Value] {
			return nil, fmt.Errorf("line %d: %s %q bound twice", k.Line, kind, k.Value)
		}
		seen[k.Value] = true
		pairs = append(pairs, IOPair{Name: k.Value, Value: v.Value, Line: k.Line})
	}
	return pairs, nil
}

// MarshalJSON emits the list form as a JSON array and the map form as a JSON
// object built in pair order — encoding/json sorts map keys, which would
// reorder bindings and churn the flow hash on every editor round-trip.
func (d DocInputs) MarshalJSON() ([]byte, error) {
	if !d.Mapped {
		return json.Marshal(d.List)
	}
	return marshalOrderedPairs(d.Pairs)
}

// MarshalJSON emits the single path as a JSON string, or the map form as an
// ordered JSON object.
func (d DocOutputs) MarshalJSON() ([]byte, error) {
	if !d.Mapped {
		return json.Marshal(d.Str)
	}
	return marshalOrderedPairs(d.Pairs)
}

// UnmarshalJSON accepts the array/object (inputs) or string/object (outputs)
// shapes. Defensive: the server does not decode Doc from JSON today, but a
// symmetric codec keeps round-trip tests and any future caller honest.
func (d *DocInputs) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	switch trimmed[0] {
	case '[':
		return json.Unmarshal(trimmed, &d.List)
	case '{':
		d.Mapped = true
		pairs, err := unmarshalOrderedPairs(trimmed)
		d.Pairs = pairs
		return err
	}
	return fmt.Errorf("input must be a JSON array or object")
}

func (d *DocOutputs) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] == '{' {
		d.Mapped = true
		pairs, err := unmarshalOrderedPairs(trimmed)
		d.Pairs = pairs
		return err
	}
	return json.Unmarshal(trimmed, &d.Str)
}

// marshalOrderedPairs writes IOPairs as a JSON object preserving order.
func marshalOrderedPairs(pairs []IOPair) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(p.Name)
		if err != nil {
			return nil, err
		}
		val, err := json.Marshal(p.Value)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(val)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// unmarshalOrderedPairs walks a JSON object token stream into ordered IOPairs.
func unmarshalOrderedPairs(data []byte) ([]IOPair, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("expected a JSON object")
	}
	var pairs []IOPair
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("expected a string key")
		}
		var val string
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		pairs = append(pairs, IOPair{Name: key, Value: val})
	}
	return pairs, nil
}

// Load reads, parses and validates a flow file, setting Flow.Hash.
func Load(path string) (*Flow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(filepath.Base(path), data)
}

// Parse parses and validates a flow document. filename is used in errors only.
func Parse(filename string, data []byte) (*Flow, error) {
	p := &parser{file: filename}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, p.wrapYAML(err)
	}

	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	var raw Doc
	if err := dec.Decode(&raw); err != nil {
		return nil, p.wrapYAML(err)
	}

	if len(doc.Content) > 0 {
		p.root = doc.Content[0]
	}

	f, err := p.build(&raw)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	f.Hash = hex.EncodeToString(sum[:])
	return f, nil
}

// ParseDoc validates the document (full Parse) and returns the raw schema
// shape for the graph editor. The Doc preserves absent-vs-set (pointers,
// empty strings), so the editor round-trips without baking defaults in.
func ParseDoc(filename string, data []byte) (*Doc, error) {
	if _, err := Parse(filename, data); err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	var d Doc
	if err := dec.Decode(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

type parser struct {
	file string
	root *yaml.Node // document mapping node, for line lookups
}

func (p *parser) errf(line int, format string, args ...any) error {
	return &Error{File: p.file, Line: line, Msg: fmt.Sprintf(format, args...)}
}

// wrapYAML prefixes yaml.v3 errors (which already carry "line N:") with the
// filename and strips the library's boilerplate.
func (p *parser) wrapYAML(err error) error {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "yaml: unmarshal errors:\n  ")
	msg = strings.TrimPrefix(msg, "yaml: ")
	msg = strings.ReplaceAll(msg, "\n  ", "; ")
	return &Error{File: p.file, Msg: msg}
}

// keyLine returns the line of a top-level key, or 0.
func (p *parser) keyLine(key string) int {
	if p.root == nil || p.root.Kind != yaml.MappingNode {
		return 0
	}
	for i := 0; i+1 < len(p.root.Content); i += 2 {
		if p.root.Content[i].Value == key {
			return p.root.Content[i].Line
		}
	}
	return 0
}

// stageLine returns the line of the i-th stage node, or 0.
func (p *parser) stageLine(i int) int {
	if p.root == nil || p.root.Kind != yaml.MappingNode {
		return 0
	}
	for k := 0; k+1 < len(p.root.Content); k += 2 {
		if p.root.Content[k].Value == "stages" {
			seq := p.root.Content[k+1]
			if seq.Kind == yaml.SequenceNode && i < len(seq.Content) {
				return seq.Content[i].Line
			}
		}
	}
	return 0
}

func (p *parser) build(raw *Doc) (*Flow, error) {
	f := &Flow{Name: raw.Name, Description: raw.Description}

	if raw.Name == "" {
		return nil, p.errf(1, "flow name is required")
	}
	if !slugRe.MatchString(raw.Name) {
		return nil, p.errf(p.keyLine("name"), "flow name %q is not a slug (lowercase [a-z0-9_-], starting with a letter)", raw.Name)
	}

	var err error
	if f.Defaults, err = p.buildDefaults(&raw.Defaults); err != nil {
		return nil, err
	}
	if f.Budget, err = p.buildBudget(&raw.Budget); err != nil {
		return nil, err
	}

	if len(raw.Stages) == 0 {
		return nil, p.errf(p.keyLine("stages"), "flow must declare at least one stage")
	}
	ids := map[string]int{} // id -> stage index, for uniqueness + edge targets
	for i := range raw.Stages {
		s, err := p.buildStage(&raw.Stages[i], i, f.Defaults)
		if err != nil {
			return nil, err
		}
		if prev, dup := ids[s.ID]; dup {
			return nil, p.errf(s.Line, "stage %q: duplicate id (first declared at line %d)", s.ID, p.stageLine(prev))
		}
		ids[s.ID] = i
		f.Stages = append(f.Stages, *s)
	}

	// The success chain is explicit in the model (DESIGN3 §2.1); absent
	// on_success is YAML sugar for "next stage in document order", last
	// stage = done. After this loop every stage has a concrete target.
	for i := range f.Stages {
		if f.Stages[i].OnSuccess == "" {
			if i == len(f.Stages)-1 {
				f.Stages[i].OnSuccess = TerminalDone
			} else {
				f.Stages[i].OnSuccess = f.Stages[i+1].ID
			}
		}
	}

	return f, p.validateFlow(f, ids)
}

func (p *parser) buildDefaults(raw *DocDefaults) (Defaults, error) {
	line := p.keyLine("defaults")
	// harness + model: the FLOW owns its defaults (the project has none). Any
	// slug is valid document-side (DESIGN2 §6.3) — whether the kind exists in
	// the project is preflight's question at run creation, keeping flows
	// portable data. A stage may override; unpinned stages inherit these.
	d := Defaults{
		Timeout:  defaultTimeout,
		Harness:  raw.Harness,
		Model:    raw.Model,
		MaxTurns: defaultMaxTurns,
	}
	if raw.Timeout != "" {
		t, err := time.ParseDuration(raw.Timeout)
		if err != nil || t <= 0 {
			return d, p.errf(line, "defaults.timeout: invalid duration %q", raw.Timeout)
		}
		d.Timeout = t
	}
	if d.Harness == "" {
		return d, p.errf(line, "defaults.harness is required (the harness kind stages run on unless they override it)")
	}
	if !slugRe.MatchString(d.Harness) {
		return d, p.errf(line, "defaults.harness: %q is not a slug (lowercase [a-z0-9_-], starting with a letter)", raw.Harness)
	}
	if d.Model == "" {
		return d, p.errf(line, "defaults.model is required (format provider/model)")
	}
	if !validModel(d.Model) {
		return d, p.errf(line, "defaults.model: %q is not of the form provider/model", d.Model)
	}
	if raw.MaxTurns != nil {
		if *raw.MaxTurns <= 0 {
			return d, p.errf(line, "defaults.max_turns must be > 0, got %d", *raw.MaxTurns)
		}
		d.MaxTurns = *raw.MaxTurns
	}
	return d, nil
}

func (p *parser) buildBudget(raw *DocBudget) (Budget, error) {
	line := p.keyLine("budget")
	b := Budget{MaxCostUSD: defaultMaxCostUSD, MaxTotalTurns: defaultMaxTotalTurns}
	if raw.MaxCostUSD != nil {
		if *raw.MaxCostUSD <= 0 {
			return b, p.errf(line, "budget.max_cost_usd must be > 0, got %v", *raw.MaxCostUSD)
		}
		b.MaxCostUSD = *raw.MaxCostUSD
	}
	if raw.MaxTotalTurns != nil {
		if *raw.MaxTotalTurns <= 0 {
			return b, p.errf(line, "budget.max_total_turns must be > 0, got %d", *raw.MaxTotalTurns)
		}
		b.MaxTotalTurns = *raw.MaxTotalTurns
	}
	return b, nil
}

func (p *parser) buildStage(raw *DocStage, idx int, d Defaults) (*Stage, error) {
	line := p.stageLine(idx)
	fail := func(format string, args ...any) (*Stage, error) {
		id := raw.ID
		if id == "" {
			id = fmt.Sprintf("#%d", idx+1)
		}
		return nil, p.errf(line, "stage %q: %s", id, fmt.Sprintf(format, args...))
	}

	if raw.ID == "" {
		return fail("id is required")
	}
	if !slugRe.MatchString(raw.ID) {
		return fail("id is not a slug (lowercase [a-z0-9_-], starting with a letter)")
	}
	if raw.ID == TerminalDone {
		return fail("id %q is reserved (the on_success terminal)", TerminalDone)
	}
	if raw.ID == TerminalAbort {
		return fail("id %q is reserved (the deliberate-stop edge terminal)", TerminalAbort)
	}
	if raw.ID == ReservedStart {
		return fail("id %q is reserved (the entry marker; the entry is the first stage)", ReservedStart)
	}
	st := StageType(raw.Type)
	switch st {
	case StageAgent, StageCommand, StageHumanGate, StageForgeMR:
	case "":
		return fail("type is required")
	default:
		return fail("unknown type %q (agent|command|human_gate|forge_mr)", raw.Type)
	}

	// Reject fields that don't apply to the stage type (DESIGN.md §3: "model
	// only on agent stages"; generalized to every type-specific field).
	type fieldSet struct {
		name string
		set  bool
		on   []StageType
	}
	fields := []fieldSet{
		{"agent", raw.Agent != "", []StageType{StageAgent}},
		{"model", raw.Model != "", []StageType{StageAgent}},
		{"harness", raw.Harness != "", []StageType{StageAgent}},
		{"max_turns", raw.MaxTurns != nil, []StageType{StageAgent}},
		{"timeout", raw.Timeout != "", []StageType{StageAgent}},
		{"input", raw.Input != nil, []StageType{StageAgent}},
		{"output", raw.Output != nil, []StageType{StageAgent}},
		{"verdict", raw.Verdict, []StageType{StageAgent}},
		{"run", raw.Run != "", []StageType{StageCommand}},
		{"run_ref", raw.RunRef != "", []StageType{StageCommand}},
		{"ping", raw.Ping != "", []StageType{StageHumanGate}},
		{"payload", raw.Payload != "", []StageType{StageHumanGate}},
		{"auto_complete", raw.AutoComplete != "", []StageType{StageHumanGate}},
		{"body", raw.Body != "", []StageType{StageForgeMR}},
	}
	for kind, re := range raw.edges() {
		fields = append(fields, fieldSet{kind, re != nil, edgeKinds[kind].on})
	}
	for _, fs := range fields {
		if fs.set && !containsType(fs.on, st) {
			return fail("field %q does not apply to type %s (allowed on: %s)", fs.name, st, typeList(fs.on))
		}
	}

	s := &Stage{ID: raw.ID, Type: st, Line: line, Verdict: raw.Verdict}

	switch st {
	case StageAgent:
		if raw.Agent == "" {
			return fail("agent is required (resolves to .apothecary/agents/<name>.yaml)")
		}
		if !slugRe.MatchString(raw.Agent) {
			return fail("agent %q is not a slug", raw.Agent)
		}
		s.Agent = raw.Agent
		s.Model, s.Harness, s.MaxTurns, s.Timeout = d.Model, d.Harness, d.MaxTurns, d.Timeout
		if raw.Model != "" {
			if !validModel(raw.Model) {
				return fail("model: %q is not of the form provider/model", raw.Model)
			}
			s.Model = raw.Model
		}
		if raw.Harness != "" {
			if !slugRe.MatchString(raw.Harness) {
				return fail("harness %q is not a slug (kind existence is checked at run creation)", raw.Harness)
			}
			s.Harness = raw.Harness
		}
		if raw.MaxTurns != nil {
			if *raw.MaxTurns <= 0 {
				return fail("max_turns must be > 0, got %d", *raw.MaxTurns)
			}
			s.MaxTurns = *raw.MaxTurns
		}
		if raw.Timeout != "" {
			t, err := time.ParseDuration(raw.Timeout)
			if err != nil || t <= 0 {
				return fail("invalid timeout %q", raw.Timeout)
			}
			s.Timeout = t
		}
		// input: legacy list (ticket | $CHANGED | path) or named map form
		// {name: path|$CHANGED}. Named bindings validate against the agent's
		// declared inputs later (ValidateBindings); here only the shape.
		if in := raw.Input; in != nil {
			if in.Mapped {
				for _, pr := range in.Pairs {
					if !agentspec.ValidName(pr.Name) {
						return fail("input %q must match [a-z0-9][a-z0-9._-]* (lowercase slug)", pr.Name)
					}
					if pr.Name == agentspec.InputFeedback {
						return fail("input %q is filled by edge feedback (feed:), not bound in a stage", agentspec.InputFeedback)
					}
					if pr.Value == InputTicket {
						return fail("input %q: the ticket is always the TASK section; remove this binding", pr.Name)
					}
					if pr.Value != InputChanged && !validArtifactPath(pr.Value) {
						return fail("input %q: value %q must be %q or a relative artifact path", pr.Name, pr.Value, InputChanged)
					}
					s.Inputs = append(s.Inputs, IOBinding{Name: pr.Name, Value: pr.Value})
				}
			} else {
				for _, v := range in.List {
					if v != InputTicket && v != InputChanged && !validArtifactPath(v) {
						return fail("input entry %q must be %q, %q or a relative artifact path", v, InputTicket, InputChanged)
					}
					s.Inputs = append(s.Inputs, IOBinding{Value: v})
				}
			}
		}
		// output: legacy single path or named map {decl-name: path} rebinds.
		if out := raw.Output; out != nil {
			if out.Mapped {
				for _, pr := range out.Pairs {
					if !agentspec.ValidName(pr.Name) {
						return fail("output %q must match [a-z0-9][a-z0-9._-]* (lowercase slug)", pr.Name)
					}
					if !validArtifactPath(pr.Value) {
						return fail("output %q: path %q must be a relative artifact path", pr.Name, pr.Value)
					}
					s.Outputs = append(s.Outputs, IOBinding{Name: pr.Name, Value: pr.Value})
				}
			} else if out.Str != "" {
				if !validArtifactPath(out.Str) {
					return fail("output %q must be a relative artifact path", out.Str)
				}
				s.Outputs = append(s.Outputs, IOBinding{Value: out.Str})
			}
		}

	case StageCommand:
		if (raw.Run != "") == (raw.RunRef != "") {
			return fail("exactly one of run / run_ref is required")
		}
		if raw.RunRef != "" && !slugRe.MatchString(raw.RunRef) {
			return fail("run_ref %q is not a slug (key into project.commands)", raw.RunRef)
		}
		s.Run, s.RunRef = raw.Run, raw.RunRef

	case StageHumanGate:
		switch PingType(raw.Ping) {
		case PingPlanApproval, PingFinalReview, PingManualDebug:
			s.Ping = PingType(raw.Ping)
		case "":
			return fail("ping is required (%s|%s|%s)", PingPlanApproval, PingFinalReview, PingManualDebug)
		default:
			return fail("unknown ping %q (%s|%s|%s)", raw.Ping, PingPlanApproval, PingFinalReview, PingManualDebug)
		}
		if raw.Payload != "" && raw.Payload != PayloadMRURL && raw.Payload != PayloadWorkspace && !validArtifactPath(raw.Payload) {
			return fail("payload %q must be a relative artifact path, %q or %q", raw.Payload, PayloadMRURL, PayloadWorkspace)
		}
		s.Payload = raw.Payload
		if raw.AutoComplete != "" && raw.AutoComplete != AutoCompleteMRMerged {
			return fail("unknown auto_complete %q (supported: %s)", raw.AutoComplete, AutoCompleteMRMerged)
		}
		s.AutoComplete = raw.AutoComplete

	case StageForgeMR:
		if raw.Body == "" {
			return fail("body is required (artifact path used as MR description)")
		}
		if !validArtifactPath(raw.Body) {
			return fail("body %q must be a relative artifact path", raw.Body)
		}
		s.Body = raw.Body
	}

	if raw.OnSuccess != "" && raw.OnSuccess != TerminalDone && !slugRe.MatchString(raw.OnSuccess) {
		return fail("on_success %q must be a stage id or %q", raw.OnSuccess, TerminalDone)
	}
	s.OnSuccess = raw.OnSuccess

	s.Edges = map[string]*Edge{}
	for kind, re := range raw.edges() {
		e, err := p.buildEdge(re, kind, s, line)
		if err != nil {
			return nil, err
		}
		if e != nil {
			s.Edges[kind] = e
		}
	}
	if s.Edges[EdgeReject] != nil && st == StageAgent && !s.Verdict {
		return fail("on_reject on an agent stage requires verdict: true")
	}
	return s, nil
}

func (p *parser) buildEdge(raw *DocEdge, kind string, s *Stage, line int) (*Edge, error) {
	return p.buildEdgeLink(raw, kind, s, line, 0)
}

// buildEdgeLink validates one link of an edge chain (depth 0 = the edge
// itself, deeper = its then fallbacks).
func (p *parser) buildEdgeLink(raw *DocEdge, kind string, s *Stage, line, depth int) (*Edge, error) {
	if raw == nil {
		return nil, nil
	}
	at := kind
	if depth > 0 {
		at = fmt.Sprintf("%s (then #%d)", kind, depth)
	}
	fail := func(format string, args ...any) (*Edge, error) {
		return nil, p.errf(line, "stage %q: %s: %s", s.ID, at, fmt.Sprintf(format, args...))
	}
	if raw.To == "" {
		return fail("to is required")
	}
	if raw.To == TerminalDone {
		return fail("%q is not a valid edge target (only on_success may complete the run)", TerminalDone)
	}
	if raw.To == TerminalAbort {
		// The deliberate stop: taken on first arrival, feeds nothing, has no
		// fallback. Anything else on the link is a contradiction.
		switch {
		case raw.Max != nil:
			return fail("max does not apply when to is %q (abort is immediate)", TerminalAbort)
		case raw.Feed != "":
			return fail("feed does not apply when to is %q (there is no target stage)", TerminalAbort)
		case raw.Then != nil:
			return fail("then does not apply when to is %q (nothing runs after abort)", TerminalAbort)
		}
		return &Edge{To: TerminalAbort}, nil
	}
	if raw.Max == nil {
		return fail("max is required")
	}
	if *raw.Max == UnlimitedMax {
		// Unlimited links never exhaust, so a fallback after one is dead
		// weight; the human_gate-target rule is cross-stage (validateFlow).
		if raw.Then != nil {
			return fail("then after an unlimited link (max -1) is unreachable — it never exhausts")
		}
	} else if *raw.Max < 1 {
		return fail("max must be >= 1 (or -1 for unlimited, human_gate targets only), got %d", *raw.Max)
	}
	e := &Edge{To: raw.To, Max: *raw.Max, Feed: Feed(raw.Feed)}
	// Feed must match what the edge kind can actually carry (registry).
	if spec, ok := edgeKinds[kind]; !ok || !spec.feedOK(e.Feed, s.Type) {
		return fail("feed %q is not valid for %s on a %s stage", raw.Feed, kind, s.Type)
	}
	var err error
	if e.Then, err = p.buildEdgeLink(raw.Then, kind, s, line, depth+1); err != nil {
		return nil, err
	}
	return e, nil
}

// validateFlow holds the cross-stage rules (DESIGN3 §2.1: explicit graph).
func (p *parser) validateFlow(f *Flow, ids map[string]int) error {
	forgeMRAt := -1
	for i := range f.Stages {
		s := &f.Stages[i]
		for kind, e := range s.Edges {
			for link := e; link != nil; link = link.Then {
				if link.To == TerminalAbort {
					continue
				}
				if _, ok := ids[link.To]; !ok {
					return p.errf(s.Line, "stage %q: %s target %q does not exist", s.ID, kind, link.To)
				}
				if link.Max == UnlimitedMax {
					// Unlimited needs a human on the link: gate target (each
					// firing parks on a decision) or gate source (the link
					// only fires when a human rejects).
					if tgt := &f.Stages[ids[link.To]]; tgt.Type != StageHumanGate && s.Type != StageHumanGate {
						return p.errf(s.Line, "stage %q: %s: max -1 (unlimited) requires a human_gate on the link (source or target) — every firing must cost a human decision; budgets cannot bound cost-free retries (use a finite max)",
							s.ID, kind)
					}
				}
			}
		}
		if s.OnSuccess != TerminalDone {
			if _, ok := ids[s.OnSuccess]; !ok {
				return p.errf(s.Line, "stage %q: on_success target %q does not exist", s.ID, s.OnSuccess)
			}
		}
		if s.Type == StageForgeMR {
			if forgeMRAt >= 0 {
				return p.errf(s.Line, "stage %q: at most one forge_mr stage is allowed (first at line %d)", s.ID, f.Stages[forgeMRAt].Line)
			}
			forgeMRAt = i
		}
		if s.Type == StageHumanGate && s.AutoComplete != "" && s.OnSuccess != TerminalDone {
			return p.errf(s.Line, "stage %q: human_gate with auto_complete must complete the run (on_success: done)", s.ID)
		}
	}

	// Success-DAG rule: every stage's on_success chain must reach done. A
	// success-only cycle through command stages would consume no turns and
	// no cost — no budget would ever bound it; loops belong to edge kinds
	// carrying max (on_fail/on_reject).
	for i := range f.Stages {
		seen := map[string]bool{}
		cur := &f.Stages[i]
		var path []string
		for {
			if seen[cur.ID] {
				return p.errf(f.Stages[i].Line, "success cycle: %s -> %s — the on_success chain must reach %q (use on_fail/on_reject for loops; they carry max)",
					strings.Join(path, " -> "), cur.ID, TerminalDone)
			}
			seen[cur.ID] = true
			path = append(path, cur.ID)
			if cur.OnSuccess == TerminalDone {
				break
			}
			cur = &f.Stages[ids[cur.OnSuccess]]
		}
	}

	// Reachability: every stage reachable from the entry (first stage) via
	// on_success or any edge — an orphaned stage would never execute.
	reached := map[string]bool{}
	queue := []string{f.Stages[0].ID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if reached[id] {
			continue
		}
		reached[id] = true
		s := &f.Stages[ids[id]]
		if s.OnSuccess != TerminalDone {
			queue = append(queue, s.OnSuccess)
		}
		for _, e := range s.Edges {
			for link := e; link != nil; link = link.Then {
				if link.To != TerminalAbort {
					queue = append(queue, link.To)
				}
			}
		}
	}
	for i := range f.Stages {
		if !reached[f.Stages[i].ID] {
			return p.errf(f.Stages[i].Line, "stage %q is unreachable from the entry stage %q", f.Stages[i].ID, f.Stages[0].ID)
		}
	}
	return nil
}

func validModel(m string) bool {
	provider, model, ok := strings.Cut(m, "/")
	if !ok || provider == "" || model == "" {
		return false
	}
	// Model strings reach harness CLIs as arguments; a strict charset means
	// they can never carry shell-meaningful characters even if an adapter's
	// quoting regresses.
	for _, r := range m {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-' || r == '/' || r == ':':
		default:
			return false
		}
	}
	return true
}

// ValidArtifactPath reports whether p is a legal repo-root-relative
// artifact path (also used by the engine to validate ask_human artifact
// refs, PLAN4 M16).
func ValidArtifactPath(p string) bool { return validArtifactPath(p) }

// validArtifactPath accepts repo-root-relative paths only.
func validArtifactPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "$") {
		return false
	}
	for part := range strings.SplitSeq(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func containsType(ts []StageType, t StageType) bool {
	return slices.Contains(ts, t)
}

func typeList(ts []StageType) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = string(t)
	}
	return strings.Join(parts, ", ")
}
