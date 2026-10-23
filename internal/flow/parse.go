package flow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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

// idPathRe matches a stage id as the parser may produce it: a slug, or an
// include path of slugs joined by dots (make.ship.test).
var idPathRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)*$`)

// Doc* mirror the YAML schema exactly (unknown fields rejected by the
// strict decoder; pointers distinguish "absent" from explicit zeros). They
// are exported with JSON tags as the graph editor's document model
// (DESIGN3 §2.3): ParseDoc hands the raw shape to the UI, which serializes
// its edits back to YAML.
type Doc struct {
	Name string `yaml:"name" json:"name"`
	// Version is the grammar the document is written in; it must equal
	// CurrentVersion. Required so a break is a refusal, never a mis-parse.
	Version     int    `yaml:"version" json:"version"`
	Description string `yaml:"description" json:"description,omitempty"`
	// Parameters is the flow's one settings block (FLOWLANG §1): the six the
	// engine reads itself and the flow's own knobs, each with its default.
	Parameters DocParams `yaml:"parameters" json:"parameters,omitempty"`
	// Agents are the agent definitions the flow runs (FLOWLANG §1, v3). A
	// list rather than a mapping so the shape is exactly agentspec.Spec —
	// the same schema the retired library files spoke, with document order
	// preserved for free.
	Agents []agentspec.Spec `yaml:"agents" json:"agents,omitempty"`
	// Blocks are chains of stages written once and included where needed
	// (FLOWLANG §1 Blocks); the parser expands every include in place.
	Blocks []DocBlock `yaml:"blocks" json:"blocks,omitempty"`
	// Stages and Phases are exclusive: top-level stages are the one-phase
	// shorthand (today's flows), phases the whole life of a ticket (PLAN6).
	Stages []DocStage `yaml:"stages" json:"stages,omitempty"`
	Phases []DocPhase `yaml:"phases" json:"phases,omitempty"`
}

// DocBlock is one block: a named chain of stages a stage `include:`s. Like a
// phase without an `on:` — nothing opens it, an include runs it.
type DocBlock struct {
	ID          string     `yaml:"id" json:"id"`
	Description string     `yaml:"description" json:"description,omitempty"`
	Stages      []DocStage `yaml:"stages" json:"stages"`
}

// DocPhase is one phase: what starts it and its stages.
type DocPhase struct {
	ID          string     `yaml:"id" json:"id"`
	Description string     `yaml:"description" json:"description,omitempty"`
	On          DocWait    `yaml:"on" json:"on"`
	Stages      []DocStage `yaml:"stages" json:"stages,omitempty"`
	// Then: an outcome with nothing to do but end the run — `then: done` or
	// `then: abort` in place of stages.
	Then string `yaml:"then" json:"then,omitempty"`
}

// DocWait is a phase's `on:` — the wait point that opens it: a bare slug
// (`on: run_start`, `on: manual`, `on: mr_conflicts`) or a mapping with the
// options.
type DocWait struct {
	Signal   string  `yaml:"signal" json:"signal"`
	Start    string  `yaml:"start" json:"start,omitempty"`
	QuietFor string  `yaml:"quiet_for" json:"quiet_for,omitempty"`
	Max      *DocInt `yaml:"max" json:"max,omitempty"`
}

// UnmarshalYAML accepts the scalar sugar and the mapping form, rejecting
// unknown and repeated keys by hand (a custom unmarshaler bypasses the
// decoder's strictness, as for DocEdge).
func (t *DocWait) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Decode(&t.Signal)
	case yaml.MappingNode:
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if seen[k.Value] {
				return fmt.Errorf("line %d: %q set twice", k.Line, k.Value)
			}
			seen[k.Value] = true
			var err error
			switch k.Value {
			case "signal":
				err = v.Decode(&t.Signal)
			case "start":
				err = v.Decode(&t.Start)
			case "quiet_for":
				err = v.Decode(&t.QuietFor)
			case "max":
				t.Max = new(DocInt)
				err = v.Decode(t.Max)
			default:
				return fmt.Errorf("line %d: field %s not found in type flow.DocWait (signal, start, quiet_for, max)", k.Line, k.Value)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("line %d: on must be a slug (run_start, manual or a trigger signal) or a mapping {signal, start, quiet_for, max}", n.Line)
}

// DocParams is the document's `parameters:` block: an ordered mapping of
// name to default, or to {default, description, enum}. Order is kept so the
// editor round-trips the author's layout.
type DocParams struct {
	Entries []DocParamEntry
}

// DocParamEntry is one declared parameter, in document order.
type DocParamEntry struct {
	Name  string
	Param DocParam
	Line  int
}

// DocParam is the long form of a parameter declaration.
type DocParam struct {
	Default     DocParamValue `yaml:"default" json:"default"`
	Description string        `yaml:"description" json:"description,omitempty"`
	Enum        []string      `yaml:"enum" json:"enum,omitempty"`
}

// DocParamValue is a parameter's default as written: its YAML type is the
// parameter's type (true → bool, 10 → int, 1.5 → float, anything else a
// string) and Text its canonical spelling.
type DocParamValue struct {
	Kind ParamType
	Text string
	Set  bool
}

func (v *DocParamValue) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: a parameter's default is one value — true/false, a number or a string", n.Line)
	}
	switch n.ShortTag() {
	case "!!bool":
		b, err := strconv.ParseBool(n.Value)
		if err != nil {
			return fmt.Errorf("line %d: %q is not a bool", n.Line, n.Value)
		}
		*v = DocParamValue{Kind: ParamBool, Text: strconv.FormatBool(b), Set: true}
	case "!!int":
		i, err := strconv.ParseInt(n.Value, 0, 64)
		if err != nil {
			return fmt.Errorf("line %d: %q is not an integer", n.Line, n.Value)
		}
		*v = DocParamValue{Kind: ParamInt, Text: strconv.FormatInt(i, 10), Set: true}
	case "!!float":
		f, err := strconv.ParseFloat(n.Value, 64)
		if err != nil {
			return fmt.Errorf("line %d: %q is not a number", n.Line, n.Value)
		}
		*v = DocParamValue{Kind: ParamFloat, Text: strconv.FormatFloat(f, 'f', -1, 64), Set: true}
	case "!!str":
		*v = DocParamValue{Kind: ParamString, Text: n.Value, Set: true}
	case "!!null":
		return fmt.Errorf("line %d: a parameter needs a default value", n.Line)
	default:
		return fmt.Errorf("line %d: %q is not a parameter default (true/false, a number or a string)", n.Line, n.Value)
	}
	return nil
}

// MarshalJSON emits the default with its type: true, 10, 1.5 or "text".
func (v DocParamValue) MarshalJSON() ([]byte, error) {
	switch v.Kind {
	case ParamBool, ParamInt, ParamFloat:
		return []byte(v.Text), nil
	}
	return json.Marshal(v.Text)
}

func (v *DocParamValue) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var x any
	if err := dec.Decode(&x); err != nil {
		return err
	}
	switch t := x.(type) {
	case bool:
		*v = DocParamValue{Kind: ParamBool, Text: strconv.FormatBool(t), Set: true}
	case json.Number:
		if i, err := t.Int64(); err == nil {
			*v = DocParamValue{Kind: ParamInt, Text: strconv.FormatInt(i, 10), Set: true}
		} else {
			f, err := t.Float64()
			if err != nil {
				return err
			}
			*v = DocParamValue{Kind: ParamFloat, Text: strconv.FormatFloat(f, 'f', -1, 64), Set: true}
		}
	case string:
		*v = DocParamValue{Kind: ParamString, Text: t, Set: true}
	default:
		return fmt.Errorf("a parameter default is true/false, a number or a string")
	}
	return nil
}

// UnmarshalYAML reads the ordered mapping, both forms, rejecting repeated
// names and unknown keys by hand (a custom unmarshaler bypasses the
// decoder's strictness, as for DocIO).
func (d *DocParams) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: parameters must be a mapping of name: default, or name: {default, description, enum}", n.Line)
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if seen[k.Value] {
			return fmt.Errorf("line %d: parameter %q declared twice", k.Line, k.Value)
		}
		seen[k.Value] = true
		e := DocParamEntry{Name: k.Value, Line: k.Line}
		switch v.Kind {
		case yaml.ScalarNode:
			if err := e.Param.Default.UnmarshalYAML(v); err != nil {
				return err
			}
		case yaml.MappingNode:
			keys := map[string]bool{}
			for j := 0; j+1 < len(v.Content); j += 2 {
				kk, vv := v.Content[j], v.Content[j+1]
				if keys[kk.Value] {
					return fmt.Errorf("line %d: %q set twice", kk.Line, kk.Value)
				}
				keys[kk.Value] = true
				var err error
				switch kk.Value {
				case "default":
					err = e.Param.Default.UnmarshalYAML(vv)
				case "description":
					err = vv.Decode(&e.Param.Description)
				case "enum":
					err = vv.Decode(&e.Param.Enum)
				default:
					return fmt.Errorf("line %d: field %s not found in type flow.DocParam (default, description, enum)", kk.Line, kk.Value)
				}
				if err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("line %d: parameter %q must be a default value or a mapping {default, description, enum}", v.Line, k.Value)
		}
		d.Entries = append(d.Entries, e)
	}
	return nil
}

// MarshalJSON emits the block as an ordered object of long forms, one shape
// for the editor whatever the author wrote.
func (d DocParams) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, e := range d.Entries {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(e.Name)
		if err != nil {
			return nil, err
		}
		val, err := json.Marshal(e.Param)
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

// UnmarshalJSON reads either form back, keeping key order.
func (d *DocParams) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("parameters: expected a JSON object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("parameters: expected a string key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		e := DocParamEntry{Name: key}
		if t := bytes.TrimSpace(raw); len(t) > 0 && t[0] == '{' {
			if err := json.Unmarshal(t, &e.Param); err != nil {
				return err
			}
		} else if err := e.Param.Default.UnmarshalJSON(t); err != nil {
			return err
		}
		d.Entries = append(d.Entries, e)
	}
	return nil
}

// DocInt is an integer field that may read a parameter instead:
// `max_turns: 25` or `max_turns: $turns`.
type DocInt struct {
	Int int
	Ref string // "$name" when the field reads a parameter
}

func (d *DocInt) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: expected an integer or a $parameter", n.Line)
	}
	switch n.ShortTag() {
	case "!!int":
		i, err := strconv.Atoi(n.Value)
		if err != nil {
			return fmt.Errorf("line %d: %q is not an integer", n.Line, n.Value)
		}
		d.Int = i
	case "!!str":
		if !strings.HasPrefix(n.Value, "$") {
			return fmt.Errorf("line %d: %q is not an integer or a $parameter", n.Line, n.Value)
		}
		d.Ref = n.Value
	default:
		return fmt.Errorf("line %d: %q is not an integer or a $parameter", n.Line, n.Value)
	}
	return nil
}

// MarshalJSON emits the number, or the "$name" string when the field reads
// a parameter.
func (d DocInt) MarshalJSON() ([]byte, error) {
	if d.Ref != "" {
		return json.Marshal(d.Ref)
	}
	return json.Marshal(d.Int)
}

func (d *DocInt) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var x any
	if err := dec.Decode(&x); err != nil {
		return err
	}
	switch t := x.(type) {
	case json.Number:
		i, err := t.Int64()
		if err != nil {
			return fmt.Errorf("%s is not an integer", t)
		}
		d.Int = int(i)
	case string:
		if !strings.HasPrefix(t, "$") {
			return fmt.Errorf("%q is not an integer or a $parameter", t)
		}
		d.Ref = t
	default:
		return fmt.Errorf("expected an integer or a $parameter")
	}
	return nil
}

type DocStage struct {
	ID string `yaml:"id" json:"id"`
	// Include names a block this stage runs in its place (FLOWLANG §1
	// Blocks); such a stage has no type.
	Include     string   `yaml:"include" json:"include,omitempty"`
	Type        string   `yaml:"type" json:"type,omitempty"`
	Description string   `yaml:"description" json:"description,omitempty"`
	When        string   `yaml:"when" json:"when,omitempty"`
	Agent       string   `yaml:"agent" json:"agent,omitempty"`
	Model       string   `yaml:"model" json:"model,omitempty"`
	Harness     string   `yaml:"harness" json:"harness,omitempty"`
	MaxTurns    *DocInt  `yaml:"max_turns" json:"max_turns,omitempty"`
	Timeout     string   `yaml:"timeout" json:"timeout,omitempty"`
	Input       *DocIO   `yaml:"input" json:"input,omitempty"`
	Output      *DocIO   `yaml:"output" json:"output,omitempty"`
	Run         string   `yaml:"run" json:"run,omitempty"`
	RunRef      string   `yaml:"run_ref" json:"run_ref,omitempty"`
	EachRepo    bool     `yaml:"each_repo" json:"each_repo,omitempty"`
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

	// line is the stage's source line; scope the include path it was
	// expanded from (empty for a stage written in its phase). Set by the
	// parser, never part of the document.
	line  int
	scope string
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
	// An action fails too when its outcome is wrong in a way a retry or an
	// agent can fix (a merge that conflicts); its infrastructure errors stay
	// terminal. With no on_fail, any failure is terminal.
	EdgeFail: {on: []StageType{StageAgent, StageCommand, StageWait, StageAction}},
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
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			// A custom unmarshaler bypasses yaml.v3's duplicate-key check, so
			// `{to: a, to: b}` would silently keep the last value.
			if seen[k.Value] {
				return fmt.Errorf("line %d: %q set twice", k.Line, k.Value)
			}
			seen[k.Value] = true
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
	return fmt.Errorf("line %d: edge must be a mapping, or one of %q, %q, %q, %q", n.Line, TerminalEnd, TerminalDone, TerminalAbort, ReservedDebug)
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
			return nil, fmt.Errorf("line %d: %q: value must be a single value (a path, a sentinel or a literal), not a mapping or a list", v.Line, k.Value)
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

// Structural limits (FLOWLANG §6, PLAN5 M22). A flow is a hand-written
// control-flow document, so these sit far above anything sane and exist to
// bound what a malicious or generated file can cost the parser and the
// editor rather than to constrain authors.
const (
	maxDocBytes   = 1 << 20 // 1 MiB, matching the HTTP body cap
	maxStages     = 128
	maxAgents     = 64
	maxChainDepth = 8
	// maxIncludeDepth bounds how deep blocks include blocks: a phase's
	// include expands at most this many levels.
	maxIncludeDepth = 3
	maxParams       = 32 // the flow's own, beyond the engine-known six
)

// Parse parses and validates a flow document with its parameters at their
// defaults. filename is used in errors only.
func Parse(filename string, data []byte) (*Flow, error) { return parseBound(filename, data, nil) }

// ParseBound parses a flow document with parameter values bound — the
// project's, then the run's — in place of the defaults (FLOWLANG §1). The
// same validation runs on the bound values, so a binding that breaks a rule
// is a *Error naming the parameter.
func ParseBound(filename string, data []byte, bindings map[string]string) (*Flow, error) {
	return parseBound(filename, data, bindings)
}

func parseBound(filename string, data []byte, bindings map[string]string) (*Flow, error) {
	p := &parser{file: filename, bindings: bindings, params: map[string]*Param{}, used: map[string]bool{}}

	if len(data) > maxDocBytes {
		return nil, p.errf(1, "flow document is %d bytes; the limit is %d", len(data), maxDocBytes)
	}

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
	// A second document would be silently ignored, so a flow split by `---`
	// would run as only its first half.
	var extra Doc
	if err := dec.Decode(&extra); err == nil {
		return nil, p.errf(1, "a flow file holds one document; everything after the first %q separator would be ignored", "---")
	}

	if len(doc.Content) > 0 {
		p.root = doc.Content[0]
		// Anchors and aliases would let one document expand into another on
		// re-emission, breaking the canonical round-trip the editor relies on
		// and hiding structure from a reader.
		if err := p.rejectAliases(p.root); err != nil {
			return nil, err
		}
	}
	total := len(raw.Stages)
	for i := range raw.Phases {
		total += len(raw.Phases[i].Stages)
	}
	if total > maxStages {
		return nil, p.errf(p.keyLine("stages"), "flow declares %d stages; the limit is %d", total, maxStages)
	}
	if len(raw.Agents) > maxAgents {
		return nil, p.errf(p.keyLine("agents"), "flow declares %d agents; the limit is %d", len(raw.Agents), maxAgents)
	}

	f, err := p.build(&raw)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	f.Hash = hex.EncodeToString(sum[:])
	f.file, f.src = filename, slices.Clone(data)
	if len(bindings) > 0 {
		f.Bindings = maps.Clone(bindings)
	}
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
	// blocks are the declared blocks by id (blockOrder in document order);
	// topIncludes maps each include written in a phase to the expanded id
	// of its first stage, the stage a route to the include lands on.
	blocks      map[string]*blockDef
	blockOrder  []string
	topIncludes map[string]string
	// stageLines are the source lines of the flattened stages, in order
	// (top-level stages, or every phase's stages one phase after another).
	stageLines []int
	// bindings are the parameter values this parse applies (nil = the
	// defaults); params the declared parameters by name, plist them in
	// document order; used which were read; refs where (for the editor).
	bindings map[string]string
	params   map[string]*Param
	plist    []Param
	used     map[string]bool
	refs     []ParamRef
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

// rejectAliases refuses YAML anchors/aliases and merge keys anywhere in the
// document (FLOWLANG §6). They are legal YAML but hostile here: the graph
// editor re-emits a canonical form, so an aliased document would not survive
// a round-trip unchanged, and a reader cannot see a stage's real shape.
func (p *parser) rejectAliases(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.AliasNode {
		return p.errf(n.Line, "YAML aliases (*name) are not allowed — write the value out")
	}
	if n.Anchor != "" {
		return p.errf(n.Line, "YAML anchors (&%s) are not allowed — write the value out", n.Anchor)
	}
	for _, c := range n.Content {
		if err := p.rejectAliases(c); err != nil {
			return err
		}
	}
	return nil
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

// stageLine returns the line of the i-th (flattened) stage node, or 0.
func (p *parser) stageLine(i int) int {
	if i < len(p.stageLines) {
		return p.stageLines[i]
	}
	return p.seqLine("stages", i)
}

// seqNodes returns a top-level sequence's element nodes, or nil.
func (p *parser) seqNodes(key string) []*yaml.Node {
	if p.root == nil || p.root.Kind != yaml.MappingNode {
		return nil
	}
	for k := 0; k+1 < len(p.root.Content); k += 2 {
		if p.root.Content[k].Value == key && p.root.Content[k+1].Kind == yaml.SequenceNode {
			return p.root.Content[k+1].Content
		}
	}
	return nil
}

// phaseStageLines lists the lines of a phase node's stages.
func phaseStageLines(ph *yaml.Node) []int {
	if ph == nil || ph.Kind != yaml.MappingNode {
		return nil
	}
	for k := 0; k+1 < len(ph.Content); k += 2 {
		if ph.Content[k].Value == "stages" && ph.Content[k+1].Kind == yaml.SequenceNode {
			var out []int
			for _, c := range ph.Content[k+1].Content {
				out = append(out, c.Line)
			}
			return out
		}
	}
	return nil
}

// seqLine returns the line of the i-th element of a top-level sequence, or 0.
func (p *parser) seqLine(key string, i int) int {
	if p.root == nil || p.root.Kind != yaml.MappingNode {
		return 0
	}
	for k := 0; k+1 < len(p.root.Content); k += 2 {
		if p.root.Content[k].Value == key {
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
	if err = p.buildParameters(f, &raw.Parameters); err != nil {
		return nil, err
	}
	if f.Agents, err = p.buildAgents(raw.Agents); err != nil {
		return nil, err
	}

	// Every flow is phases. Top-level stages are the one-phase shorthand: a
	// phase named ShorthandPhase opened by run_start, which is exactly what
	// the engine runs — there is no second execution mode.
	rawPhases := raw.Phases
	if len(rawPhases) > 0 && len(raw.Stages) > 0 {
		return nil, p.errf(p.keyLine("stages"), "a flow declares either top-level stages (one phase) or phases, not both — move these stages into a phase (`on: %s`)", signalspec.RunStart)
	}
	if len(rawPhases) == 0 {
		if len(raw.Stages) == 0 {
			return nil, p.errf(p.keyLine("stages"), "flow must declare at least one stage")
		}
		rawPhases = []DocPhase{{ID: ShorthandPhase, On: DocWait{Signal: signalspec.RunStart}, Stages: raw.Stages}}
		f.shorthand = true
	}
	// Every stage remembers its line before blocks are expanded, which
	// copies stages out of where they were written.
	nodes := p.seqNodes("phases")
	for i := range rawPhases {
		var lines []int
		if !f.shorthand && i < len(nodes) {
			lines = phaseStageLines(nodes[i])
		}
		for j := range rawPhases[i].Stages {
			l := p.seqLine("phases", i)
			switch {
			case f.shorthand:
				l = p.seqLine("stages", j)
			case j < len(lines):
				l = lines[j]
			}
			rawPhases[i].Stages[j].line = l
		}
	}
	if err = p.buildBlocks(raw.Blocks); err != nil {
		return nil, err
	}
	if err = p.indexIncludes(rawPhases); err != nil {
		return nil, err
	}
	var rawStages []DocStage
	if f.Phases, rawStages, err = p.buildPhases(rawPhases, f.shorthand); err != nil {
		return nil, err
	}
	ids := map[string]int{} // id -> stage index, for uniqueness + edge targets
	for i := range rawStages {
		s, err := p.buildStage(&rawStages[i], i, f.Defaults)
		if err != nil {
			return nil, err
		}
		if prev, dup := ids[s.ID]; dup {
			return nil, p.errf(s.Line, "stage %q: duplicate id (first declared at line %d) — stage ids are one namespace across phases", s.ID, p.stageLine(prev))
		}
		ids[s.ID] = i
		f.Stages = append(f.Stages, *s)
	}
	for pi := range f.Phases {
		ph := &f.Phases[pi]
		for i := ph.First; i <= ph.Last; i++ {
			f.Stages[i].Phase = ph.ID
		}
	}
	// References made from stages learn their phase now that stages have one.
	for i := range p.refs {
		if r := &p.refs[i]; r.Stage != "" && r.Phase == "" {
			if st := f.Stage(r.Stage); st != nil {
				r.Phase = st.Phase
			}
		}
	}

	// The success chain is explicit in the model (DESIGN3 §2.1); absent
	// on_success is YAML sugar for "next stage in document order", and the
	// last stage of a phase ends the PHASE (end) — never the run, unless
	// nothing could follow: the one phase of the shorthand, and an outcome,
	// end the run (done) when their chain is over. After this loop every
	// stage has a concrete target.
	for i := range f.Stages {
		if f.Stages[i].OnSuccess == "" {
			if i == len(f.Stages)-1 || f.Stages[i+1].Phase != f.Stages[i].Phase {
				f.Stages[i].OnSuccess = TerminalEnd
				if ph := f.Phase(f.Stages[i].Phase); f.shorthand || (ph != nil && ph.IsOutcome()) {
					f.Stages[i].OnSuccess = TerminalDone
				}
			} else {
				f.Stages[i].OnSuccess = f.Stages[i+1].ID
			}
		}
	}

	if err := p.validateFlow(f, ids); err != nil {
		return nil, err
	}
	if err := p.validatePhases(f, ids); err != nil {
		return nil, err
	}
	// Contracts are checked here rather than in a second, project-aware pass:
	// v3 flows carry their agents, so a binding that violates one is a parse
	// error like any other (PLAN5 M26).
	warns, err := p.validateContracts(f)
	if err != nil {
		return nil, err
	}
	f.Params, f.Refs = p.plist, p.refs
	for i := range f.Params {
		if pr := &f.Params[i]; !pr.Known && !p.used[pr.Name] {
			warns = append(warns, fmt.Sprintf("parameter %q is declared but nothing reads it ($%s)", pr.Name, pr.Name))
		}
	}
	for _, b := range p.unusedBlocks(raw.Blocks) {
		warns = append(warns, fmt.Sprintf("block %q is never included (nothing checks its stages until something does)", b))
	}
	f.Warnings = warns
	return f, nil
}

// buildParameters reads the flow's one settings block (FLOWLANG §1): the six
// engine-known parameters, validated by the rules their old homes had and
// landing in Defaults and Budget, and the flow's own, typed by their
// defaults. A bound value (p.bindings) replaces the default everywhere a
// value is checked, so a project's binding is validated exactly like the
// author's default.
func (p *parser) buildParameters(f *Flow, raw *DocParams) error {
	line := p.keyLine("parameters")
	errf := func(at int, format string, args ...any) error {
		if at == 0 {
			at = line
		}
		return p.errf(at, "parameters: "+format, args...)
	}
	custom := 0
	for i := range raw.Entries {
		e := &raw.Entries[i]
		if !ParamNameRe.MatchString(e.Name) {
			return errf(e.Line, "%q is not a parameter name (lowercase [a-z0-9_], starting with a letter)", e.Name)
		}
		if !e.Param.Default.Set {
			return errf(e.Line, "%s: default is required", e.Name)
		}
		pr := Param{Name: e.Name, Type: e.Param.Default.Kind, Default: e.Param.Default.Text,
			Description: e.Param.Description, Enum: e.Param.Enum, Line: e.Line}
		if kt, known := KnownParams[e.Name]; known {
			pr.Known = true
			switch {
			case pr.Type == kt:
			case kt == ParamFloat && pr.Type == ParamInt: // max_cost_usd: 10
				pr.Type = kt
			default:
				return errf(e.Line, "%s takes a %s default, got a %s", e.Name, kt, pr.Type)
			}
		} else if custom++; custom > maxParams {
			return errf(e.Line, "declares more than %d parameters of its own", maxParams)
		}
		if len(pr.Description) > 500 {
			return errf(e.Line, "%s: description is longer than 500 characters", e.Name)
		}
		if len(pr.Enum) > 0 {
			if pr.Type != ParamString {
				return errf(e.Line, "%s: enum applies to a string parameter (this one is %s)", e.Name, pr.Type)
			}
			seen := map[string]bool{}
			for _, v := range pr.Enum {
				if v == "" || seen[v] {
					return errf(e.Line, "%s: enum values must be distinct and non-empty", e.Name)
				}
				seen[v] = true
			}
			if !contains(pr.Enum, pr.Default) {
				return errf(e.Line, "%s: default %q is not one of its enum (%s)", e.Name, pr.Default, strings.Join(pr.Enum, " | "))
			}
		}
		p.plist = append(p.plist, pr)
	}
	// The known ones the document omitted exist all the same, at the
	// engine's defaults — so a project can bind, say, max_cost_usd on a flow
	// that never mentions it. harness and model have no default: a flow that
	// does not say what runs its agents is incomplete.
	have := map[string]bool{}
	for _, pr := range p.plist {
		have[pr.Name] = true
	}
	for _, name := range KnownParamOrder {
		if !have[name] {
			p.plist = append(p.plist, Param{Name: name, Type: KnownParams[name], Default: knownDefaults[name], Known: true, Line: line})
		}
	}
	for i := range p.plist {
		p.params[p.plist[i].Name] = &p.plist[i]
	}
	// Bound values: declared, and of their parameter's type — checked once,
	// here, so a bad binding is one error at the block rather than a
	// surprise at the site that reads it.
	if err := ValidateBindings(p.plist, p.bindings); err != nil {
		return errf(0, "%v", err)
	}
	// The engine-known values, validated by the rules their old homes had —
	// in document order, so the first mistake in the document is the one
	// reported.
	val := func(pr *Param) (string, string) {
		if v, ok := p.bindings[pr.Name]; ok {
			return v, " (bound)"
		}
		return pr.Default, ""
	}
	var d Defaults
	var b Budget
	for i := range p.plist {
		pr := &p.plist[i]
		if !pr.Known {
			continue
		}
		v, via := val(pr)
		switch pr.Name {
		case "harness":
			if pr.Default == "" {
				return errf(0, "harness is required (the harness kind stages run on unless they override it)")
			}
			if !slugRe.MatchString(v) {
				return errf(pr.Line, "harness: %q%s is not a slug (lowercase [a-z0-9_-], starting with a letter)", v, via)
			}
			d.Harness = v
		case "model":
			if pr.Default == "" {
				return errf(0, "model is required (format provider/model)")
			}
			if !validModel(v) {
				return errf(pr.Line, "model: %q%s is not of the form provider/model", v, via)
			}
			d.Model = v
		case "timeout":
			t, err := time.ParseDuration(v)
			if err != nil || t <= 0 {
				return errf(pr.Line, "timeout: invalid duration %q%s", v, via)
			}
			d.Timeout = t
		case "max_turns":
			if d.MaxTurns, _ = strconv.Atoi(v); d.MaxTurns <= 0 {
				return errf(pr.Line, "max_turns must be > 0, got %s%s", v, via)
			}
		case "max_cost_usd":
			if b.MaxCostUSD, _ = strconv.ParseFloat(v, 64); b.MaxCostUSD <= 0 {
				return errf(pr.Line, "max_cost_usd must be > 0, got %s%s", v, via)
			}
		case "max_total_turns":
			if b.MaxTotalTurns, _ = strconv.Atoi(v); b.MaxTotalTurns <= 0 {
				return errf(pr.Line, "max_total_turns must be > 0, got %s%s", v, via)
			}
		}
	}
	f.Defaults, f.Budget = d, b
	return nil
}

// knownDefaults are the engine-known parameters' values when a document
// omits them (harness and model have none: they are required).
var knownDefaults = map[string]string{
	"timeout":         "30m",
	"max_turns":       strconv.Itoa(defaultMaxTurns),
	"max_cost_usd":    strconv.FormatFloat(defaultMaxCostUSD, 'f', -1, 64),
	"max_total_turns": strconv.Itoa(defaultMaxTotalTurns),
}

// paramSite is one place a $parameter may be read: what to record on the
// reference and which type the site takes (an enum site also says which
// values an enum parameter must stay within).
type paramSite struct {
	phase, stage, field, input string
	typ                        ParamType
	values                     []string
}

// lookup resolves a field's raw text: a literal is returned as is; a
// $parameter must be declared and of the site's type (an enum site needs an
// enum within its values), is recorded, and yields the bound value or the
// parameter's default. via names the parameter for error messages, "" for a
// literal.
func (p *parser) lookup(raw string, site paramSite) (value, via string, err error) {
	if !strings.HasPrefix(raw, "$") {
		return raw, "", nil
	}
	if !IsParamRef(raw) {
		if IsSentinel(raw) {
			return "", "", fmt.Errorf("%s is a sentinel, not a parameter — parameters are $lowercase names declared under parameters", raw)
		}
		return "", "", fmt.Errorf("%q is not a parameter reference (parameters are $lowercase_name)", raw)
	}
	name := raw[1:]
	pr := p.params[name]
	if pr == nil {
		return "", "", fmt.Errorf("parameter %q is not declared (declared: %s)", name, p.paramNames())
	}
	if pr.Type != site.typ {
		return "", "", fmt.Errorf("parameter %q is a %s, and this field takes a %s", name, pr.Type, site.typ)
	}
	if site.values != nil {
		if len(pr.Enum) == 0 {
			return "", "", fmt.Errorf("parameter %q needs an enum within %s to be read here", name, strings.Join(site.values, " | "))
		}
		for _, v := range pr.Enum {
			if !slices.Contains(site.values, v) {
				return "", "", fmt.Errorf("parameter %q allows %q, which this field does not (%s)", name, v, strings.Join(site.values, " | "))
			}
		}
	}
	p.used[name] = true
	p.refs = append(p.refs, ParamRef{Param: name, Phase: site.phase, Stage: site.stage, Field: site.field, Input: site.input})
	via = fmt.Sprintf(" (parameter %q)", name)
	if v, ok := p.bindings[name]; ok {
		return v, via, nil
	}
	return pr.Default, via, nil
}

// lookupInt resolves an integer field: the literal, or an int parameter.
func (p *parser) lookupInt(raw *DocInt, site paramSite) (int, string, error) {
	if raw.Ref == "" {
		return raw.Int, "", nil
	}
	v, via, err := p.lookup(raw.Ref, site)
	if err != nil {
		return 0, "", err
	}
	n, _ := strconv.Atoi(v) // the parameter's type guarantees it parses
	return n, via, nil
}

// lookupDuration resolves a duration field: the literal, or a string
// parameter; either must parse as a positive duration.
func (p *parser) lookupDuration(raw string, site paramSite) (time.Duration, string, error) {
	v, via, err := p.lookup(raw, site)
	if err != nil {
		return 0, "", err
	}
	t, err := time.ParseDuration(v)
	if err != nil || t <= 0 {
		return 0, via, fmt.Errorf("invalid %s %q%s", site.field, v, via)
	}
	return t, via, nil
}

// buildWhen parses a stage condition: one of the run facts, or a bool
// parameter — which folds right here, so the engine never sees it.
func (p *parser) buildWhen(raw string, site paramSite) (*When, error) {
	// Negation is spelled `not x`: a leading `!` would be a YAML tag.
	term, not := strings.CutPrefix(raw, "not ")
	term = strings.TrimSpace(term)
	switch {
	case term == WhenTracker || term == WhenSubject:
		return &When{Term: term, Not: not}, nil
	case strings.HasPrefix(term, "$"):
		v, _, err := p.lookup(term, site)
		if err != nil {
			return nil, err
		}
		b, _ := strconv.ParseBool(v) // the parameter's type guarantees it parses
		if b != not {
			return nil, nil // holds: no condition to carry
		}
		return &When{Term: WhenNever}, nil
	}
	return nil, fmt.Errorf("%q is not a condition: %s, %s or a $bool parameter, each optionally negated as `not x`", raw, WhenTracker, WhenSubject)
}

func (p *parser) paramNames() string {
	names := make([]string, 0, len(p.plist))
	for _, pr := range p.plist {
		names = append(names, pr.Name)
	}
	return strings.Join(names, ", ")
}

// buildAgents validates the document's agent definitions (FLOWLANG §1, v3).
// Since the flow now carries them, every contract check that used to need a
// per-project library is a parse-time fact.
func (p *parser) buildAgents(raw []agentspec.Spec) ([]agentspec.Spec, error) {
	line := p.keyLine("agents")
	seen := map[string]int{}
	for i := range raw {
		sp := &raw[i]
		at := p.seqLine("agents", i)
		if at == 0 {
			at = line
		}
		if sp.Name == "" {
			return nil, p.errf(at, "agent #%d: name is required", i+1)
		}
		if err := agentspec.Validate(sp); err != nil {
			return nil, p.errf(at, "agent %q: %v", sp.Name, err)
		}
		if prev, dup := seen[sp.Name]; dup {
			return nil, p.errf(at, "agent %q: declared twice (first at line %d)", sp.Name, prev)
		}
		seen[sp.Name] = at
	}
	return raw, nil
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

	// A block's stage was checked under its own id; its expanded one (a
	// dotted path) is the parser's.
	if msg := idProblem(raw.ID); msg != "" && raw.scope == "" {
		return fail("%s", msg)
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
		// A wait stage may bound how long it waits; absent, it waits forever
		// (like a gate — both park on something outside the run). A command
		// bounds its script.
		{"timeout", raw.Timeout != "", []StageType{StageAgent, StageCommand, StageWait}},
		{"input", raw.Input != nil, []StageType{StageAgent, StageCommand, StageHumanGate, StageAction}},
		{"output", raw.Output != nil, []StageType{StageAgent, StageCommand}},
		{"run", raw.Run != "", []StageType{StageCommand}},
		{"run_ref", raw.RunRef != "", []StageType{StageCommand}},
		{"each_repo", raw.EachRepo, []StageType{StageCommand}},
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

	s := &Stage{ID: raw.ID, Type: st, Line: line, Description: raw.Description, Include: raw.scope}
	// site describes a field of this stage that may read a $parameter.
	site := func(field string, typ ParamType, values []string) paramSite {
		return paramSite{stage: raw.ID, field: field, typ: typ, values: values}
	}

	switch st {
	case StageAgent:
		if raw.Agent == "" {
			return fail("agent is required (a name in the flow's agents block)")
		}
		if !slugRe.MatchString(raw.Agent) {
			return fail("agent %q is not a slug", raw.Agent)
		}
		s.Agent = raw.Agent
		s.Model, s.Harness, s.MaxTurns, s.Timeout = d.Model, d.Harness, d.MaxTurns, d.Timeout
		if raw.Model != "" {
			v, via, err := p.lookup(raw.Model, site("model", ParamString, nil))
			if err != nil {
				return fail("model: %v", err)
			}
			if !validModel(v) {
				return fail("model: %q%s is not of the form provider/model", v, via)
			}
			s.Model = v
		}
		if raw.Harness != "" {
			v, via, err := p.lookup(raw.Harness, site("harness", ParamString, nil))
			if err != nil {
				return fail("harness: %v", err)
			}
			if !slugRe.MatchString(v) {
				return fail("harness %q%s is not a slug (kind existence is checked at run creation)", v, via)
			}
			s.Harness = v
		}
		if raw.MaxTurns != nil {
			n, via, err := p.lookupInt(raw.MaxTurns, site("max_turns", ParamInt, nil))
			if err != nil {
				return fail("max_turns: %v", err)
			}
			if n <= 0 {
				return fail("max_turns must be > 0, got %d%s", n, via)
			}
			s.MaxTurns = n
		}
		if raw.Timeout != "" {
			t, _, err := p.lookupDuration(raw.Timeout, site("timeout", ParamString, nil))
			if err != nil {
				return fail("%v", err)
			}
			s.Timeout = t
		}
		// input/output: bindings against the agent's declared contract. Only
		// the VALUES are checked here; the names are checked against the
		// flow's own agent decls in validateContracts, once every agent has
		// been built.
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
		s.Run, s.EachRepo = raw.Run, raw.EachRepo
		if raw.RunRef != "" {
			v, via, err := p.lookup(raw.RunRef, site("run_ref", ParamString, nil))
			if err != nil {
				return fail("run_ref: %v", err)
			}
			if !slugRe.MatchString(v) {
				return fail("run_ref %q%s is not a slug (key into project.commands)", v, via)
			}
			s.RunRef = v
		}
		if raw.Timeout != "" {
			t, _, err := p.lookupDuration(raw.Timeout, site("timeout", ParamString, nil))
			if err != nil {
				return fail("%v", err)
			}
			s.Timeout = t
		}
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
			if sig := signalspec.Get(f.slug); sig != nil && (sig.Trigger || sig.Entry) {
				return fail("%s: %q opens a phase, it does not answer a gate (declare a phase `on: %s`)", f.field, f.slug, f.slug)
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
				var values []string
				if sig.Kind == actionspec.KindEnum {
					values = sig.Values
				}
				v, via, err := p.lookup(pr.Value, paramSite{stage: raw.ID, field: "input", input: pr.Name, typ: ParamString, values: values})
				if err != nil {
					return fail("input %q: %v", pr.Name, err)
				}
				if sig.Kind == actionspec.KindFile && !validArtifactPath(v) {
					return fail("input %q: path %q%s must be a relative artifact path", pr.Name, v, via)
				}
				if sig.Kind == actionspec.KindEnum && !slices.Contains(sig.Values, v) {
					return fail("input %q: %q%s is not one of %s", pr.Name, v, via, strings.Join(sig.Values, " | "))
				}
				// A text input is the tracker's vocabulary, not ours: only
				// blankness is checkable here.
				if sig.Kind == actionspec.KindText && strings.TrimSpace(v) == "" {
					return fail("input %q: a value is required%s (%s)", pr.Name, via, sig.Description)
				}
				bound[pr.Name] = true
				s.Inputs = append(s.Inputs, IOBinding{Name: pr.Name, Value: v})
			}
		}
		for i := range act.Inputs {
			if sig := &act.Inputs[i]; sig.Required && !bound[sig.Name] {
				return fail("action %q requires input %q (%s)", act.Slug, sig.Name, sig.Description)
			}
		}
		// An action may take a value from an artifact OR from the flow —
		// exactly one, the way a command takes run OR run_ref.
		if group := act.ExactlyOne; len(group) > 0 {
			n := 0
			for _, name := range group {
				if bound[name] {
					n++
				}
			}
			if n != 1 {
				return fail("action %q takes exactly one of %s", act.Slug, strings.Join(group, " / "))
			}
		}
		// A file an action writes is part of the dataflow like an agent's
		// output: later stages bind it, the validator sees it produced.
		for _, o := range act.Outputs {
			if o.Kind == actionspec.KindFile && o.Path != "" {
				s.Outputs = append(s.Outputs, IOBinding{Name: o.Name, Value: o.Path})
			}
		}

	case StageWait:
		if raw.For == "" {
			return fail("for is required (%s)", waitableSlugs())
		}
		sig := signalspec.Get(raw.For)
		if sig == nil {
			return fail("unknown signal %q (%s)", raw.For, waitableSlugs())
		}
		if sig.Trigger || sig.Entry {
			return fail("%q opens a phase, it is not something to wait for (declare a phase `on: %s`)", raw.For, raw.For)
		}
		// A wait stage is a wait point mid-chain: it parks the phase until
		// the signal holds, then continues (a person in the loop after a
		// signal is a human_gate with auto_approve).
		w := Wait{Signal: raw.For, Start: StartAuto, Max: 1}
		if raw.Timeout != "" {
			t, _, err := p.lookupDuration(raw.Timeout, site("timeout", ParamString, nil))
			if err != nil {
				return fail("%v", err)
			}
			w.Timeout, s.Timeout = t, t
		}
		s.WaitFor, s.Wait = raw.For, &w
	}

	// when: a condition on any stage type.
	if raw.When != "" {
		w, err := p.buildWhen(raw.When, site("when", ParamBool, nil))
		if err != nil {
			return fail("when: %v", err)
		}
		s.When = w
	}

	if raw.OnSuccess == ReservedDebug {
		return fail("on_success cannot be %q — only an edge (on_fail/on_reject/on_timeout) parks for a human", ReservedDebug)
	}
	if raw.OnSuccess != "" && !routeTerminal(raw.OnSuccess) && !idPathRe.MatchString(raw.OnSuccess) {
		return fail("on_success %q must be a stage id, %q (the phase ends), %q (the run is done) or %q (the run is abandoned)", raw.OnSuccess, TerminalEnd, TerminalDone, TerminalAbort)
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
	// output), which the document itself now declares — validateContracts
	// refuses the edge when the agent can never produce one.
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
// render: the changed-file lists, and the content of the edge that re-entered
// the stage.
func agentInputValue(v string) error {
	switch v {
	case SentinelChanged, SentinelUncommitted, SentinelDecision, SentinelFailure:
		return nil
	case "ticket":
		return fmt.Errorf("the ticket is always the TASK section; remove this binding")
	case SentinelMRURL, SentinelWorkspace:
		return fmt.Errorf("%s is a link for a human_gate, not something an agent reads", v)
	}
	if !validArtifactPath(v) {
		return fmt.Errorf("%q must be a relative artifact path, %s, %s, %s or %s",
			v, SentinelChanged, SentinelUncommitted, SentinelDecision, SentinelFailure)
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
	case SentinelChanged, SentinelUncommitted, SentinelDecision, SentinelFailure:
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
	if depth > maxChainDepth {
		return fail("then chain is deeper than %d links", maxChainDepth)
	}
	if raw.Feed != "" {
		// An edge no longer says what it carries: its kind decides that, and
		// the TARGET stage binds the content as an input.
		where := fmt.Sprintf("on stage %q", raw.To)
		if routeTerminal(raw.To) || raw.To == "" {
			where = "on the target stage"
		}
		return fail("%q is retired: bind %s as an input %s instead", "feed", FeedSentinel(kind), where)
	}
	if raw.To == "" {
		return fail("to is required")
	}
	if raw.To == TerminalEnd || raw.To == TerminalDone {
		// Ending the phase from an edge — a "best effort" stage whose failure
		// is not worth routing anywhere — or the run itself. Like abort,
		// nothing follows.
		what := "the phase ends"
		if raw.To == TerminalDone {
			what = "the run is done"
		}
		switch {
		case raw.Max != nil:
			return fail("max does not apply when to is %q (%s)", raw.To, what)
		case raw.Then != nil:
			return fail("then does not apply when to is %q (nothing runs after: %s)", raw.To, what)
		}
		return &Edge{To: raw.To}, nil
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
	if raw.To == ReservedDebug {
		// The debug leaf: the run parks for a human and comes back to this
		// stage when they continue. Unlimited by nature (bounded by human
		// attention), and nothing can follow it but the stage itself.
		switch {
		case raw.Max != nil:
			return fail("max does not apply when to is %q (the human decides when to stop)", ReservedDebug)
		case raw.Then != nil:
			return fail("then does not apply when to is %q (continuing re-enters this stage; nothing else follows)", ReservedDebug)
		}
		return &Edge{To: ReservedDebug}, nil
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
	// Actions marked Single may appear once per phase (a phase run opens one
	// merge request).
	singleAt := map[string]int{} // action|phase -> line
	for i := range f.Stages {
		s := &f.Stages[i]
		for kind, e := range s.Edges {
			for link := e; link != nil; link = link.Then {
				if routeTerminal(link.To) {
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
		if !routeTerminal(s.OnSuccess) {
			if _, ok := ids[s.OnSuccess]; !ok {
				return p.errf(s.Line, "stage %q: on_success target %q does not exist", s.ID, s.OnSuccess)
			}
		}
		if s.Type == StageAction {
			if act := actionspec.Get(s.Action); act != nil && act.Single {
				key := s.Action + "|" + s.Phase
				if prev, dup := singleAt[key]; dup {
					return p.errf(s.Line, "stage %q: at most one %q action is allowed per phase (first at line %d)", s.ID, s.Action, prev)
				}
				singleAt[key] = s.Line
			}
		}

	}

	// Success-DAG rule: every stage's on_success chain must reach an end (end
	// or done). A success-only cycle through command stages would consume no
	// turns and no cost — no budget would ever bound it; loops belong to
	// edge kinds carrying max (on_fail/on_reject).
	for i := range f.Stages {
		seen := map[string]bool{}
		cur := &f.Stages[i]
		var path []string
		for {
			if seen[cur.ID] {
				return p.errf(f.Stages[i].Line, "success cycle: %s -> %s — the on_success chain must reach %q or %q (use on_fail/on_reject for loops; they carry max)",
					strings.Join(path, " -> "), cur.ID, TerminalEnd, TerminalDone)
			}
			seen[cur.ID] = true
			path = append(path, cur.ID)
			if routeTerminal(cur.OnSuccess) {
				break
			}
			cur = &f.Stages[ids[cur.OnSuccess]]
		}
	}

	// Reachability: every stage reachable from its phase's first stage via
	// on_success or any edge — an orphaned stage would never execute.
	reached := map[string]bool{}
	var queue []string
	for _, ph := range f.Phases {
		if ph.First <= ph.Last { // a then-phase has no stages
			queue = append(queue, f.Stages[ph.First].ID)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if reached[id] {
			continue
		}
		reached[id] = true
		s := &f.Stages[ids[id]]
		if !routeTerminal(s.OnSuccess) {
			queue = append(queue, s.OnSuccess)
		}
		for _, e := range s.Edges {
			for link := e; link != nil; link = link.Then {
				if !routeTerminal(link.To) {
					queue = append(queue, link.To)
				}
			}
		}
	}
	for i := range f.Stages {
		if !reached[f.Stages[i].ID] {
			first := f.Stages[i].ID
			if ph := f.Phase(f.Stages[i].Phase); ph != nil {
				first = f.Stages[ph.First].ID
			}
			return p.errf(f.Stages[i].Line, "stage %q is unreachable from the first stage of its phase (%q)", f.Stages[i].ID, first)
		}
	}
	return nil
}

// buildPhases validates the phases' own fields and flattens their stages,
// recording each phase's span and every stage's source line. shorthand marks
// the desugared one-phase document, whose stage lines come from the
// top-level stages sequence.
func (p *parser) buildPhases(raw []DocPhase, shorthand bool) ([]Phase, []DocStage, error) {
	var phases []Phase
	var stages []DocStage
	seen := map[string]int{}
	p.stageLines = nil
	entrypoints := 0
	for i := range raw {
		rp := &raw[i]
		line := p.seqLine("phases", i)
		if shorthand {
			line = p.keyLine("stages")
		}
		fail := func(format string, args ...any) ([]Phase, []DocStage, error) {
			id := rp.ID
			if id == "" {
				id = fmt.Sprintf("#%d", i+1)
			}
			return nil, nil, p.errf(line, "phase %q: %s", id, fmt.Sprintf(format, args...))
		}
		if rp.ID == "" {
			return fail("id is required")
		}
		if !slugRe.MatchString(rp.ID) {
			return fail("id is not a slug (lowercase [a-z0-9_-], starting with a letter)")
		}
		if prev, dup := seen[rp.ID]; dup {
			return fail("duplicate phase id (first declared at line %d)", prev)
		}
		seen[rp.ID] = line
		if len(rp.Stages) == 0 && rp.Then == "" {
			return fail("a phase needs stages — or, for an outcome that only ends the run, `then: %s` or `then: %s`", TerminalDone, TerminalAbort)
		}
		if len(rp.Stages) > 0 && rp.Then != "" {
			return fail("stages or then, not both: a phase with stages ends the run from its last stage")
		}
		on, err := p.buildWait(&rp.On, rp.ID)
		if err != nil {
			return fail("on: %v", err)
		}
		if on.Signal == signalspec.RunStart {
			entrypoints++
		}
		if rp.Then != "" {
			if rp.Then != TerminalDone && rp.Then != TerminalAbort {
				return fail("then %q must be %s (the run completes) or %s (the run is abandoned)", rp.Then, TerminalDone, TerminalAbort)
			}
			if sig := signalspec.Get(on.Signal); sig == nil || !sig.Outcome {
				return fail("then is for an outcome phase (on: %s): a phase that does work has stages", outcomeSlugs())
			}
		}
		// The phase's stages with every include written out.
		own, err := p.expand(rp.Stages, &expandScope{})
		if err != nil {
			return nil, nil, err
		}
		if len(stages)+len(own) > maxStages {
			return nil, nil, p.errf(line, "phase %q: the flow expands to more than %d stages (blocks included)", rp.ID, maxStages)
		}
		ph := Phase{ID: rp.ID, Description: rp.Description, On: on, Then: rp.Then, First: len(stages), Last: len(stages) + len(own) - 1, Line: line}
		phases = append(phases, ph)
		stages = append(stages, own...)
		for j := range own {
			p.stageLines = append(p.stageLines, own[j].line)
		}
	}
	if entrypoints == 0 {
		return nil, nil, p.errf(p.keyLine("phases"), "no phase begins a run: at least one must open `on: %s` (an entrypoint a run is created at)", signalspec.RunStart)
	}
	return phases, stages, nil
}

// buildWait validates a phase's opening wait point on its own (FLOWLANG §1):
// what it opens on and the options that apply to it. Whether the flow can
// satisfy it (a merge request to react to) is a warning, not a rule.
func (p *parser) buildWait(raw *DocWait, phaseID string) (Wait, error) {
	w := Wait{Signal: raw.Signal, Start: StartManual, Max: UnlimitedMax}
	site := func(field string, typ ParamType, values []string) paramSite {
		return paramSite{phase: phaseID, field: field, typ: typ, values: values}
	}
	if w.Signal == "" {
		return w, fmt.Errorf("signal is required: %s (an entrypoint), %s (started by hand), a trigger (%s) or an outcome (%s)",
			signalspec.RunStart, signalspec.Manual, triggerSlugs(), outcomeSlugs())
	}
	sig := signalspec.Get(w.Signal)
	if sig == nil {
		return w, fmt.Errorf("unknown signal %q (%s, %s, a trigger: %s, or an outcome: %s)", w.Signal, signalspec.RunStart, signalspec.Manual, triggerSlugs(), outcomeSlugs())
	}
	fires := sig.Trigger && w.Signal != signalspec.Manual
	if !fires && !sig.Outcome && !sig.Entry && w.Signal != signalspec.Manual {
		return w, fmt.Errorf("signal %q is something to wait for, not something that opens a phase (a %s stage waits on it; a phase opens on %s, %s, a trigger: %s, or an outcome: %s)",
			w.Signal, StageWait, signalspec.RunStart, signalspec.Manual, triggerSlugs(), outcomeSlugs())
	}
	// The policy: only a signal the engine observes has one — a trigger or
	// an outcome. For run_start and manual a person's act is the start.
	if raw.Start != "" {
		if !fires && !sig.Outcome {
			return w, fmt.Errorf("start does not apply to %q — nothing fires to decide about: a person's act is the start", w.Signal)
		}
		v, via, err := p.lookup(raw.Start, site("start", ParamString, []string{string(StartAuto), string(StartConfirm), string(StartManual)}))
		if err != nil {
			return w, fmt.Errorf("start: %v", err)
		}
		switch Start(v) {
		case StartAuto, StartConfirm, StartManual:
			w.Start = Start(v)
		default:
			return w, fmt.Errorf("start %q%s must be %s, %s or %s", v, via, StartAuto, StartConfirm, StartManual)
		}
	} else if fires || sig.Outcome {
		w.Start = StartAuto
	}
	if raw.QuietFor != "" {
		if !sig.Debounced {
			return w, fmt.Errorf("quiet_for applies only to a signal that fires on activity (%s does not)", w.Signal)
		}
		v, via, err := p.lookup(raw.QuietFor, site("quiet_for", ParamString, nil))
		if err != nil {
			return w, fmt.Errorf("quiet_for: %v", err)
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return w, fmt.Errorf("quiet_for: invalid duration %q%s", v, via)
		}
		if d < time.Minute {
			return w, fmt.Errorf("quiet_for must be at least 1m, got %s%s", v, via)
		}
		w.QuietFor = d
	}
	// Runs per subject are unbounded unless the flow caps them: a reaction
	// happens as often as its signal brings a NEW fact (an occurrence key
	// never fires twice), and the run's budget bounds what that may cost.
	if raw.Max != nil {
		if sig.Outcome {
			return w, fmt.Errorf("max does not apply to an outcome: it fires once, and the run ends")
		}
		n, via, err := p.lookupInt(raw.Max, site("max", ParamInt, nil))
		if err != nil {
			return w, fmt.Errorf("max: %v", err)
		}
		if n != UnlimitedMax && n < 1 {
			return w, fmt.Errorf("max must be >= 1, or -1 for no bound (the default), got %d%s", n, via)
		}
		w.Max = n
	}
	return w, nil
}

// triggerSlugs lists the signals that open a phase when they hold.
func triggerSlugs() string {
	var out []string
	for _, s := range signalspec.Registry() {
		if s.Trigger && s.Slug != signalspec.Manual {
			out = append(out, s.Slug)
		}
	}
	return strings.Join(out, ", ")
}

// outcomeSlugs lists the signals that end the run when a phase opens on them.
func outcomeSlugs() string {
	var out []string
	for _, s := range signalspec.Registry() {
		if s.Outcome {
			out = append(out, s.Slug)
		}
	}
	return strings.Join(out, ", ")
}

// waitableSlugs lists the signals a wait stage or a gate's auto-answer may
// take: those that open no phase.
func waitableSlugs() string {
	var out []string
	for _, s := range signalspec.Registry() {
		if !s.Trigger && !s.Entry {
			out = append(out, s.Slug)
		}
	}
	return strings.Join(out, "|")
}

// validatePhases checks what spans stages: a phase's routes stay inside it,
// and a rebase is published by a leased push.
func (p *parser) validatePhases(f *Flow, ids map[string]int) error {
	same := func(from *Stage, to string) bool {
		return routeTerminal(to) || f.Stages[ids[to]].Phase == from.Phase
	}
	for i := range f.Stages {
		s := &f.Stages[i]
		if !same(s, s.OnSuccess) {
			return p.errf(s.Line, "stage %q: on_success %q is in phase %q — a phase's routes stay inside it (end it with %q)",
				s.ID, s.OnSuccess, f.Stages[ids[s.OnSuccess]].Phase, TerminalEnd)
		}
		for kind, e := range s.Edges {
			for link := e; link != nil; link = link.Then {
				if !same(s, link.To) {
					return p.errf(s.Line, "stage %q: %s target %q is in phase %q — a phase's routes stay inside it",
						s.ID, kind, link.To, f.Stages[ids[link.To]].Phase)
				}
			}
		}
	}
	// A rebase rewrites the branch: only a leased push may publish it.
	for _, ph := range f.Phases {
		rebase, leased := 0, false
		for i := ph.First; i <= ph.Last; i++ {
			s := &f.Stages[i]
			if s.Type != StageAction {
				continue
			}
			if s.Action == actionspec.PushBranch {
				leased = true
			}
			if in := s.Input("strategy"); s.Action == actionspec.SyncTarget && in != nil && in.Value == actionspec.StrategyRebase {
				rebase = s.Line
			}
		}
		if rebase > 0 && !leased {
			return p.errf(rebase, "phase %q: strategy rebase rewrites the branch — publish it with a push_branch stage in this phase (a push leased against what the run last saw), not a plain git push", ph.ID)
		}
	}
	// An outcome ends the run: nothing in it may end only the phase.
	for pi := range f.Phases {
		ph := &f.Phases[pi]
		if !ph.IsOutcome() {
			continue
		}
		for i := ph.First; i <= ph.Last; i++ {
			s := &f.Stages[i]
			if s.OnSuccess == TerminalEnd {
				return p.errf(s.Line, "stage %q: phase %q is an outcome and ends the run — route to %q or %q, not %q", s.ID, ph.ID, TerminalDone, TerminalAbort, TerminalEnd)
			}
			for kind, e := range s.Edges {
				for link := e; link != nil; link = link.Then {
					if link.To == TerminalEnd {
						return p.errf(s.Line, "stage %q: %s: phase %q is an outcome and ends the run — route to %q or %q, not %q", s.ID, kind, ph.ID, TerminalDone, TerminalAbort, TerminalEnd)
					}
				}
			}
		}
	}
	// At least one route ends the run (FLOWLANG §3): a flow that only ever
	// ends phases says nothing about when the work is finished.
	if !f.EndsRun() {
		line := p.keyLine("phases")
		if f.shorthand {
			line = p.keyLine("stages")
		}
		return p.errf(line, "no route ends the run: route a stage to %q, or add an outcome phase (on: %s, then: %s)", TerminalDone, signalspec.MRMerged, TerminalDone)
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

// Rename rewrites a document's own `name:` so a copy can carry a new one.
//
// A flow names itself and that name is its id, so copying under a different
// name is a document edit, not a metadata change. The rewrite goes through the
// YAML node tree rather than a string replacement: comments, key order and
// formatting survive, and only the top-level name is touched — a stage or an
// agent called "name" is untouched.
func Rename(src, newName string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		return "", fmt.Errorf("flow document: %v", err)
	}
	root := &doc
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return "", fmt.Errorf("flow document: top level must be a mapping")
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "name" {
			continue
		}
		v := root.Content[i+1]
		v.Value = newName
		v.Tag = "!!str"
		v.Style = 0
		out, err := marshalDoc(&doc)
		if err != nil {
			return "", err
		}
		return out, nil
	}
	return "", fmt.Errorf("flow document has no top-level name")
}

func marshalDoc(doc *yaml.Node) (string, error) {
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}
