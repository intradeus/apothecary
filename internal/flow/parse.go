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

// raw* mirror the YAML schema exactly; unknown fields are rejected by the
// strict decoder. Pointers distinguish "absent" from explicit zero values.
type rawFlow struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Defaults    rawDefaults `yaml:"defaults"`
	Budget      rawBudget   `yaml:"budget"`
	Stages      []rawStage  `yaml:"stages"`
}

type rawDefaults struct {
	Timeout  string `yaml:"timeout"`
	Harness  string `yaml:"harness"`
	Model    string `yaml:"model"`
	MaxTurns *int   `yaml:"max_turns"`
}

type rawBudget struct {
	MaxCostUSD    *float64 `yaml:"max_cost_usd"`
	MaxTotalTurns *int     `yaml:"max_total_turns"`
}

type rawStage struct {
	ID           string   `yaml:"id"`
	Type         string   `yaml:"type"`
	Agent        string   `yaml:"agent"`
	Model        string   `yaml:"model"`
	Harness      string   `yaml:"harness"`
	MaxTurns     *int     `yaml:"max_turns"`
	Timeout      string   `yaml:"timeout"`
	Input        []string `yaml:"input"`
	Output       string   `yaml:"output"`
	Verdict      bool     `yaml:"verdict"`
	Run          string   `yaml:"run"`
	RunRef       string   `yaml:"run_ref"`
	Ping         string   `yaml:"ping"`
	Payload      string   `yaml:"payload"`
	AutoComplete string   `yaml:"auto_complete"`
	Body         string   `yaml:"body"`
	OnFail       *rawEdge `yaml:"on_fail"`
	OnReject     *rawEdge `yaml:"on_reject"`
}

type rawEdge struct {
	To   string `yaml:"to"`
	Max  *int   `yaml:"max"`
	Feed string `yaml:"feed"`
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
	var raw rawFlow
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

func (p *parser) build(raw *rawFlow) (*Flow, error) {
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

	return f, p.validateFlow(f, ids)
}

func (p *parser) buildDefaults(raw *rawDefaults) (Defaults, error) {
	line := p.keyLine("defaults")
	d := Defaults{
		Timeout:  defaultTimeout,
		Harness:  HarnessOpencode,
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
	if raw.Harness != "" {
		if raw.Harness != HarnessOpencode {
			return d, p.errf(line, "defaults.harness: unknown harness %q (supported: %s)", raw.Harness, HarnessOpencode)
		}
		d.Harness = raw.Harness
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

func (p *parser) buildBudget(raw *rawBudget) (Budget, error) {
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

func (p *parser) buildStage(raw *rawStage, idx int, d Defaults) (*Stage, error) {
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
		{"on_fail", raw.OnFail != nil, []StageType{StageAgent, StageCommand}},
		{"on_reject", raw.OnReject != nil, []StageType{StageAgent, StageHumanGate}},
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
			return fail("agent is required (resolves to .factory/agents/<name>.yaml)")
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
			if raw.Harness != HarnessOpencode {
				return fail("unknown harness %q (supported: %s)", raw.Harness, HarnessOpencode)
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

	var err error
	if s.OnFail, err = p.buildEdge(raw.OnFail, "on_fail", s, line); err != nil {
		return nil, err
	}
	if s.OnReject, err = p.buildEdge(raw.OnReject, "on_reject", s, line); err != nil {
		return nil, err
	}
	if s.OnReject != nil && st == StageAgent && !s.Verdict {
		return fail("on_reject on an agent stage requires verdict: true")
	}
	return s, nil
}

func (p *parser) buildEdge(raw *rawEdge, kind string, s *Stage, line int) (*Edge, error) {
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
	// Feed must match what the edge can actually carry.
	switch {
	case e.Feed == "":
	case e.Feed == FeedOutput && kind == "on_fail":
	case e.Feed == FeedComments && kind == "on_reject" && s.Type == StageAgent:
	case e.Feed == FeedFeedback && kind == "on_reject" && s.Type == StageHumanGate:
	default:
		return fail("feed %q is not valid for %s on a %s stage", raw.Feed, kind, s.Type)
	}
	return e, nil
}

// validateFlow holds the cross-stage rules.
func (p *parser) validateFlow(f *Flow, ids map[string]int) error {
	forgeMRAt := -1
	for i := range f.Stages {
		s := &f.Stages[i]
		for _, e := range []*Edge{s.OnFail, s.OnReject} {
			if e == nil {
				continue
			}
			if _, ok := ids[e.To]; !ok {
				return p.errf(s.Line, "stage %q: edge target %q does not exist", s.ID, e.To)
			}
		}
		if s.Type == StageForgeMR {
			if forgeMRAt >= 0 {
				return p.errf(s.Line, "stage %q: at most one forge_mr stage is allowed (first at line %d)", s.ID, f.Stages[forgeMRAt].Line)
			}
			forgeMRAt = i
		}
		if s.Type == StageHumanGate && s.AutoComplete != "" && i != len(f.Stages)-1 {
			return p.errf(s.Line, "stage %q: human_gate with auto_complete must be the last stage", s.ID)
		}
	}
	return nil
}

func validModel(m string) bool {
	provider, model, ok := strings.Cut(m, "/")
	return ok && provider != "" && model != ""
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
