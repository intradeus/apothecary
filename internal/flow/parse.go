package flow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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
	ID           string   `yaml:"id" json:"id"`
	Type         string   `yaml:"type" json:"type"`
	Agent        string   `yaml:"agent" json:"agent,omitempty"`
	Model        string   `yaml:"model" json:"model,omitempty"`
	Harness      string   `yaml:"harness" json:"harness,omitempty"`
	MaxTurns     *int     `yaml:"max_turns" json:"max_turns,omitempty"`
	Timeout      string   `yaml:"timeout" json:"timeout,omitempty"`
	Input        []string `yaml:"input" json:"input,omitempty"`
	Output       string   `yaml:"output" json:"output,omitempty"`
	Verdict      bool     `yaml:"verdict" json:"verdict,omitempty"`
	Run          string   `yaml:"run" json:"run,omitempty"`
	RunRef       string   `yaml:"run_ref" json:"run_ref,omitempty"`
	Ping         string   `yaml:"ping" json:"ping,omitempty"`
	Payload      string   `yaml:"payload" json:"payload,omitempty"`
	AutoComplete string   `yaml:"auto_complete" json:"auto_complete,omitempty"`
	Body         string   `yaml:"body" json:"body,omitempty"`
	OnSuccess    string   `yaml:"on_success" json:"on_success,omitempty"`
	OnFail       *DocEdge `yaml:"on_fail" json:"on_fail,omitempty"`
	OnReject     *DocEdge `yaml:"on_reject" json:"on_reject,omitempty"`
}

// edges maps the declared edge kinds to their raw fields. Adding a kind
// (DESIGN3 §2.1): one DocStage field, one row here, one edgeKinds row, one
// raise site in the engine — parse, validation, routing and the edge_taken
// event generalize.
func (r *DocStage) edges() map[string]*DocEdge {
	return map[string]*DocEdge{EdgeFail: r.OnFail, EdgeReject: r.OnReject}
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
}

type DocEdge struct {
	To   string `yaml:"to" json:"to"`
	Max  *int   `yaml:"max" json:"max,omitempty"`
	Feed string `yaml:"feed" json:"feed,omitempty"`
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
	// harness: any slug is valid document-side (DESIGN2 §6.3) — whether the
	// kind exists is preflight's question at run creation, keeping flows
	// portable data. Empty means "the project's harness kind".
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
	if raw.Harness != "" && !slugRe.MatchString(raw.Harness) {
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
		{"output", raw.Output != "", []StageType{StageAgent}},
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
		for _, in := range raw.Input {
			if in != InputTicket && in != InputChanged && !validArtifactPath(in) {
				return fail("input entry %q must be %q, %q or a relative artifact path", in, InputTicket, InputChanged)
			}
		}
		s.Input = raw.Input
		if raw.Output != "" && !validArtifactPath(raw.Output) {
			return fail("output %q must be a relative artifact path", raw.Output)
		}
		s.Output = raw.Output

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
		case PingPlanApproval, PingFinalReview:
			s.Ping = PingType(raw.Ping)
		case "":
			return fail("ping is required (%s|%s)", PingPlanApproval, PingFinalReview)
		default:
			return fail("unknown ping %q (%s|%s)", raw.Ping, PingPlanApproval, PingFinalReview)
		}
		if raw.Payload != "" && raw.Payload != PayloadMRURL && !validArtifactPath(raw.Payload) {
			return fail("payload %q must be a relative artifact path or %q", raw.Payload, PayloadMRURL)
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
	if raw == nil {
		return nil, nil
	}
	fail := func(format string, args ...any) (*Edge, error) {
		return nil, p.errf(line, "stage %q: %s: %s", s.ID, kind, fmt.Sprintf(format, args...))
	}
	if raw.To == "" {
		return fail("to is required")
	}
	if raw.Max == nil {
		return fail("max is required")
	}
	if *raw.Max < 1 {
		return fail("max must be >= 1, got %d", *raw.Max)
	}
	e := &Edge{To: raw.To, Max: *raw.Max, Feed: Feed(raw.Feed)}
	// Feed must match what the edge kind can actually carry (registry).
	if spec, ok := edgeKinds[kind]; !ok || !spec.feedOK(e.Feed, s.Type) {
		return fail("feed %q is not valid for %s on a %s stage", raw.Feed, kind, s.Type)
	}
	return e, nil
}

// validateFlow holds the cross-stage rules (DESIGN3 §2.1: explicit graph).
func (p *parser) validateFlow(f *Flow, ids map[string]int) error {
	forgeMRAt := -1
	for i := range f.Stages {
		s := &f.Stages[i]
		for kind, e := range s.Edges {
			if _, ok := ids[e.To]; !ok {
				return p.errf(s.Line, "stage %q: %s target %q does not exist", s.ID, kind, e.To)
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
			queue = append(queue, e.To)
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
