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

	"apothecary/internal/actionspec"
	"apothecary/internal/agentspec"
	"apothecary/internal/signalspec"
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
	Name string `yaml:"name" json:"name"`
	// Version is the grammar the document is written in; it must equal
	// CurrentVersion. Required so a break is a refusal, never a mis-parse.
	Version     int         `yaml:"version" json:"version"`
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
	ID          string   `yaml:"id" json:"id"`
	Type        string   `yaml:"type" json:"type"`
	Description string   `yaml:"description" json:"description,omitempty"`
	Agent       string   `yaml:"agent" json:"agent,omitempty"`
	Model       string   `yaml:"model" json:"model,omitempty"`
	Harness     string   `yaml:"harness" json:"harness,omitempty"`
	MaxTurns    *int     `yaml:"max_turns" json:"max_turns,omitempty"`
	Timeout     string   `yaml:"timeout" json:"timeout,omitempty"`
	Input       *DocIO   `yaml:"input" json:"input,omitempty"`
	Output      *DocIO   `yaml:"output" json:"output,omitempty"`
	Run         string   `yaml:"run" json:"run,omitempty"`
	RunRef      string   `yaml:"run_ref" json:"run_ref,omitempty"`
	Question    string   `yaml:"question" json:"question,omitempty"`
	AutoApprove string   `yaml:"auto_approve" json:"auto_approve,omitempty"`
	AutoReject  string   `yaml:"auto_reject" json:"auto_reject,omitempty"`
	Action      string   `yaml:"action" json:"action,omitempty"`
	For         string   `yaml:"for" json:"for,omitempty"`
	OnSuccess   string   `yaml:"on_success" json:"on_success,omitempty"`
	OnFail      *DocEdge `yaml:"on_fail" json:"on_fail,omitempty"`
	OnReject    *DocEdge `yaml:"on_reject" json:"on_reject,omitempty"`
	OnTimeout   *DocEdge `yaml:"on_timeout" json:"on_timeout,omitempty"`

	// Retired fields, still decoded so the parser can answer with the
	// upgrade (FLOWLANG §7) instead of the strict decoder's bare "field not
	// found". They carry json:"-": the editor never sees or round-trips them.
	Verdict      bool   `yaml:"verdict" json:"-"`
	Ping         string `yaml:"ping" json:"-"`
	Payload      string `yaml:"payload" json:"-"`
	Body         string `yaml:"body" json:"-"`
	AutoComplete string `yaml:"auto_complete" json:"-"`
	AutoAnswer   string `yaml:"auto_answer" json:"-"`
}

// retired lists the v1 spellings this stage still carries, each with the
// upgrade to make. Checked before anything else so an old document gets one
// actionable error rather than a cascade of "field does not apply".
func (r *DocStage) retired() []string {
	var msgs []string
	if r.Verdict {
		msgs = append(msgs, `"verdict" is retired: declare a verdict-typed output on the agent instead (outputs: [{name: verdict, type: verdict, path: artifacts/verdict.json}])`)
	}
	if r.Ping != "" {
		msgs = append(msgs, `"ping" is retired: every human_gate is the same machine now — say what you are asking in "question"`)
	}
	if r.Payload != "" {
		msgs = append(msgs, fmt.Sprintf(`"payload" is retired: renamed "input" (an artifact path, %s or %s)`, SentinelMRURL, SentinelWorkspace))
	}
	if r.Body != "" {
		msgs = append(msgs, `"body" is retired: use type: action, action: create_mr with input: {body: <path>}`)
	}
	if r.AutoComplete != "" {
		msgs = append(msgs, fmt.Sprintf(`"auto_complete" is retired: use "auto_approve" (the signal approves the gate) — or a %s stage to just block on it`, StageWait))
	}
	if r.AutoAnswer != "" {
		msgs = append(msgs, `"auto_answer" is retired: say which answer it supplies — "auto_approve" or "auto_reject"`)
	}
	return msgs
}

// edges maps the declared edge kinds to their raw fields. Adding a kind
// (DESIGN3 §2.1): one DocStage field, one row here, one edgeKinds row, one
// raise site in the engine — parse, validation, routing and the edge_taken
// event generalize.
func (r *DocStage) edges() map[string]*DocEdge {
	return map[string]*DocEdge{EdgeFail: r.OnFail, EdgeReject: r.OnReject, EdgeTimeout: r.OnTimeout}
}

// edgeKindSpec declares one recoverable edge kind: where it may appear. What
// an edge carries is not declared — it follows from the kind (FeedSentinel),
// and the target decides whether to bind it.
type edgeKindSpec struct {
	on []StageType
}

var edgeKinds = map[string]edgeKindSpec{
	EdgeFail: {on: []StageType{StageAgent, StageCommand, StageWait}},
	// A rejection carries its rationale — an agent's verdict comments or a
	// gate's feedback text. Either way the target reads one $DECISION.
	EdgeReject: {on: []StageType{StageAgent, StageHumanGate}},
	// on_timeout routes stage timeouts separately from failures ("stuck ≠
	// broken": retry the same stage fresh vs. go fix the code). When a stage
	// declares no on_timeout, the engine falls back to on_fail.
	EdgeTimeout: {on: []StageType{StageAgent, StageCommand, StageWait}},
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

// DocIO is a stage's input or output binding in the document model: an ordered
// mapping of name to value (FLOWLANG §4). One form for every stage type — the
// name is checked against a contract where one exists and is a free label
// otherwise. Pairs preserves document order: prompts and editor round-trips
// must stay deterministic, and encoding/json would sort a Go map's keys.
type DocIO struct {
	Pairs []IOPair
}

// UnmarshalYAML accepts the mapping form only. A custom unmarshaler bypasses
// the decoder's KnownFields strictness, so duplicate keys and non-scalar
// values are rejected by hand (as DocEdge does).
func (d *DocIO) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.MappingNode:
		pairs, err := decodeYAMLPairs(n)
		if err != nil {
			return err
		}
		d.Pairs = pairs
		return nil
	case yaml.ScalarNode:
		if n.Tag == "!!null" { // `input:` with no value — treat as absent
			return nil
		}
		return fmt.Errorf("line %d: must be a {name: value} mapping, not a bare value — name what %q is for", n.Line, n.Value)
	case yaml.SequenceNode:
		return fmt.Errorf("line %d: must be a {name: value} mapping, not a list — name each entry", n.Line)
	}
	return fmt.Errorf("line %d: must be a {name: value} mapping", n.Line)
}

// MarshalJSON emits the mapping in pair order.
func (d DocIO) MarshalJSON() ([]byte, error) { return marshalOrderedPairs(d.Pairs) }

// UnmarshalJSON reads a JSON object into ordered pairs. Defensive: the server
// does not decode Doc from JSON today, but a symmetric codec keeps round-trip
// tests and any future caller honest.
func (d *DocIO) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	pairs, err := unmarshalOrderedPairs(trimmed)
	d.Pairs = pairs
	return err
}

// decodeYAMLPairs reads an ordered mapping node into IOPairs, rejecting
// duplicate keys and non-scalar values.
func decodeYAMLPairs(n *yaml.Node) ([]IOPair, error) {
	seen := map[string]bool{}
	var pairs []IOPair
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if v.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("line %d: %q: value must be a path or a sentinel", v.Line, k.Value)
		}
		if seen[k.Value] {
			return nil, fmt.Errorf("line %d: %q bound twice", k.Line, k.Value)
		}
		seen[k.Value] = true
		pairs = append(pairs, IOPair{Name: k.Value, Value: v.Value, Line: k.Line})
	}
	return pairs, nil
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
	// Every document states its grammar, so an unfamiliar one is refused
	// rather than half-understood.
	switch raw.Version {
	case CurrentVersion:
	case 0:
		return nil, p.errf(1, "version is required (this build speaks version %d)", CurrentVersion)
	default:
		return nil, p.errf(p.keyLine("version"), "unsupported flow version %d (this build speaks version %d)", raw.Version, CurrentVersion)
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
	// Retired spellings first: one actionable upgrade beats a cascade of
	// "field does not apply to type" from a document written against v1.
	if msgs := raw.retired(); len(msgs) > 0 {
		return fail("%s", msgs[0])
	}
	st := StageType(raw.Type)
	switch st {
	case StageAgent, StageCommand, StageHumanGate, StageAction, StageWait:
	case "":
		return fail("type is required")
	case "forge_mr":
		return fail(`type "forge_mr" is retired: use type: action, action: %s with input: {body: <path>}`, actionspec.CreateMR)
	default:
		return fail("unknown type %q (%s|%s|%s|%s|%s)", raw.Type,
			StageAgent, StageCommand, StageHumanGate, StageAction, StageWait)
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
		// A wait stage may bound how long it blocks; absent, it waits forever
		// (like a gate — both park on something outside the run).
		{"timeout", raw.Timeout != "", []StageType{StageAgent, StageWait}},
		{"input", raw.Input != nil, []StageType{StageAgent, StageCommand, StageHumanGate, StageAction}},
		{"output", raw.Output != nil, []StageType{StageAgent, StageCommand}},
		{"run", raw.Run != "", []StageType{StageCommand}},
		{"run_ref", raw.RunRef != "", []StageType{StageCommand}},
		{"question", raw.Question != "", []StageType{StageHumanGate}},
		{"auto_approve", raw.AutoApprove != "", []StageType{StageHumanGate}},
		{"auto_reject", raw.AutoReject != "", []StageType{StageHumanGate}},
		{"action", raw.Action != "", []StageType{StageAction}},
		{"for", raw.For != "", []StageType{StageWait}},
	}
	for kind, re := range raw.edges() {
		fields = append(fields, fieldSet{kind, re != nil, edgeKinds[kind].on})
	}
	for _, fs := range fields {
		if fs.set && !containsType(fs.on, st) {
			return fail("field %q does not apply to type %s (allowed on: %s)", fs.name, st, typeList(fs.on))
		}
	}

	s := &Stage{ID: raw.ID, Type: st, Line: line, Description: raw.Description}

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
		// input/output: bindings against the agent's declared contract. The
		// names are checked against the real decls by ValidateBindings (it needs
		// the project's agent library); here only the values.
		var berr error
		if s.Inputs, berr = bindings(raw.Input, "input", agentInputValue); berr != nil {
			return fail("%s", berr)
		}
		if s.Outputs, berr = bindings(raw.Output, "output", pathValue); berr != nil {
			return fail("%s", berr)
		}

	case StageCommand:
		if (raw.Run != "") == (raw.RunRef != "") {
			return fail("exactly one of run / run_ref is required")
		}
		if raw.RunRef != "" && !slugRe.MatchString(raw.RunRef) {
			return fail("run_ref %q is not a slug (key into project.commands)", raw.RunRef)
		}
		s.Run, s.RunRef = raw.Run, raw.RunRef
		// input/output: the paths the script reads and writes. A command has no
		// contract, so the names are free labels — they document what each file
		// is for. Declaring them makes the dataflow visible and lets the engine
		// check the inputs before running and the outputs after.
		var berr error
		if s.Inputs, berr = bindings(raw.Input, "input", pathValue); berr != nil {
			return fail("%s", berr)
		}
		if s.Outputs, berr = bindings(raw.Output, "output", pathValue); berr != nil {
			return fail("%s", berr)
		}

	case StageHumanGate:
		s.Question = raw.Question
		// A gate shows the human one thing: an artifact to review, or a link.
		// One entry today; the form leaves room for more without a grammar change.
		var berr error
		if s.Inputs, berr = bindings(raw.Input, "input", gateInputValue); berr != nil {
			return fail("%s", berr)
		}
		if len(s.Inputs) > 1 {
			return fail("input: a human_gate shows one thing; got %d bindings", len(s.Inputs))
		}
		for _, f := range []struct{ field, slug string }{
			{"auto_approve", raw.AutoApprove}, {"auto_reject", raw.AutoReject},
		} {
			if f.slug != "" && signalspec.Get(f.slug) == nil {
				return fail("unknown %s signal %q (%s)", f.field, f.slug, signalspec.Slugs())
			}
		}
		if raw.AutoApprove != "" && raw.AutoApprove == raw.AutoReject {
			return fail("signal %q cannot both approve and reject this gate", raw.AutoApprove)
		}
		s.AutoApprove, s.AutoReject = raw.AutoApprove, raw.AutoReject

	case StageAction:
		if raw.Action == "" {
			return fail("action is required (%s)", actionspec.Slugs())
		}
		act := actionspec.Get(raw.Action)
		if act == nil {
			return fail("unknown action %q (%s)", raw.Action, actionspec.Slugs())
		}
		s.Action = raw.Action
		// Bind the action's signature: apothecary owns the registry, so unlike
		// agent bindings these names are checkable right here.
		bound := map[string]bool{}
		if raw.Input != nil {
			for _, pr := range raw.Input.Pairs {
				sig := act.Input(pr.Name)
				if sig == nil {
					return fail("input %q is not an input of action %q (%s)", pr.Name, act.Slug, act.InputNames())
				}
				if sig.Kind == actionspec.KindFile && !validArtifactPath(pr.Value) {
					return fail("input %q: path %q must be a relative artifact path", pr.Name, pr.Value)
				}
				bound[pr.Name] = true
				s.Inputs = append(s.Inputs, IOBinding{Name: pr.Name, Value: pr.Value})
			}
		}
		for i := range act.Inputs {
			if sig := &act.Inputs[i]; sig.Required && !bound[sig.Name] {
				return fail("action %q requires input %q (%s)", act.Slug, sig.Name, sig.Description)
			}
		}

	case StageWait:
		if raw.For == "" {
			return fail("for is required (%s)", signalspec.Slugs())
		}
		if signalspec.Get(raw.For) == nil {
			return fail("unknown signal %q (%s)", raw.For, signalspec.Slugs())
		}
		s.WaitFor = raw.For
		if raw.Timeout != "" {
			t, err := time.ParseDuration(raw.Timeout)
			if err != nil || t <= 0 {
				return fail("invalid timeout %q", raw.Timeout)
			}
			s.Timeout = t
		}
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
	// Note: an agent stage's on_reject needs no stage-level flag. Whether the
	// agent renders a verdict is a property of its contract (a verdict-typed
	// output), which is per-project data the parser cannot see — flows stay
	// portable. ValidateBindings warns when the edge can never fire.
	return s, nil
}

// bindings converts a document mapping into resolved bindings: names are
// slugs, and each value is checked by the rule its stage type allows. One
// helper for all four types, because the FORM is the same everywhere and only
// the legal values differ (FLOWLANG §4).
func bindings(io *DocIO, field string, valueOK func(v string) error) ([]IOBinding, error) {
	if io == nil {
		return nil, nil
	}
	out := make([]IOBinding, 0, len(io.Pairs))
	for _, pr := range io.Pairs {
		if !agentspec.ValidName(pr.Name) {
			return nil, fmt.Errorf("%s %q: name must match [a-z0-9][a-z0-9._-]* (lowercase slug)", field, pr.Name)
		}
		if err := valueOK(pr.Value); err != nil {
			return nil, fmt.Errorf("%s %q: %w", field, pr.Name, err)
		}
		out = append(out, IOBinding{Name: pr.Name, Value: pr.Value})
	}
	return out, nil
}

// pathValue accepts a repo-relative artifact path and nothing else: a command
// reads and writes files, and an agent's outputs are files.
func pathValue(v string) error {
	if !validArtifactPath(v) {
		return fmt.Errorf("%q must be a relative artifact path", v)
	}
	return nil
}

// agentInputValue accepts a path or one of the sentinels an agent prompt can
// render: the changed-file list, and the content of the edge that re-entered
// the stage.
func agentInputValue(v string) error {
	switch v {
	case SentinelChanged, SentinelDecision, SentinelFailure:
		return nil
	case "ticket":
		return fmt.Errorf("the ticket is always the TASK section; remove this binding")
	case SentinelMRURL, SentinelWorkspace:
		return fmt.Errorf("%s is a link for a human_gate, not something an agent reads", v)
	}
	if !validArtifactPath(v) {
		return fmt.Errorf("%q must be a relative artifact path, %s, %s or %s",
			v, SentinelChanged, SentinelDecision, SentinelFailure)
	}
	return nil
}

// gateInputValue accepts a path to review or a link the engine resolves.
func gateInputValue(v string) error {
	switch v {
	case SentinelMRURL, SentinelWorkspace:
		return nil
	case "mr_url":
		return fmt.Errorf("retired: use %s", SentinelMRURL)
	case "workspace":
		return fmt.Errorf("retired: use %s", SentinelWorkspace)
	case SentinelChanged, SentinelDecision, SentinelFailure:
		return fmt.Errorf("%s is agent-prompt content, not something to show a human", v)
	}
	if !validArtifactPath(v) {
		return fmt.Errorf("%q must be a relative artifact path, %s or %s", v, SentinelMRURL, SentinelWorkspace)
	}
	return nil
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
	if raw.Feed != "" {
		// An edge no longer says what it carries: its kind decides that, and
		// the TARGET stage binds the content as an input.
		where := fmt.Sprintf("on stage %q", raw.To)
		if raw.To == TerminalAbort || raw.To == "" {
			where = "on the target stage"
		}
		return fail("%q is retired: bind %s as an input %s instead", "feed", FeedSentinel(kind), where)
	}
	if raw.To == "" {
		return fail("to is required")
	}
	if raw.To == TerminalDone {
		return fail("%q is not a valid edge target (only on_success may complete the run)", TerminalDone)
	}
	if raw.To == TerminalAbort {
		// The deliberate stop: taken on first arrival, has no fallback.
		// Anything else on the link is a contradiction.
		switch {
		case raw.Max != nil:
			return fail("max does not apply when to is %q (abort is immediate)", TerminalAbort)
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
	e := &Edge{To: raw.To, Max: *raw.Max}
	var err error
	if e.Then, err = p.buildEdgeLink(raw.Then, kind, s, line, depth+1); err != nil {
		return nil, err
	}
	return e, nil
}

// validateFlow holds the cross-stage rules (DESIGN3 §2.1: explicit graph).
func (p *parser) validateFlow(f *Flow, ids map[string]int) error {
	// Actions marked Single may appear once per flow (a run has one MR).
	singleAt := map[string]int{}
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
		if s.Type == StageAction {
			if act := actionspec.Get(s.Action); act != nil && act.Single {
				if prev, dup := singleAt[s.Action]; dup {
					return p.errf(s.Line, "stage %q: at most one %q action is allowed per flow (first at line %d)", s.ID, s.Action, prev)
				}
				singleAt[s.Action] = s.Line
			}
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
