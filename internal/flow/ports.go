package flow

// Ports (FLOWLANG §4): every value a stage reads or writes is stated on the
// stage, typed, with where it comes from or goes — `from:` a source for an
// input, `to:` a path for an output. A source never names a stage: it is a
// path some stage writes, a file of the repository (repo:), or a value the
// engine provides (run.*, edge.*). Edges carry control, and the failure or
// decision of the transition; nothing else.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"apothecary/internal/actionspec"
	"apothecary/internal/agentspec"
)

// DocPort is one port as the document writes it.
type DocPort struct {
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
	Type        string `json:"type,omitempty"`
	Schema      string `json:"schema,omitempty"`
	Description string `json:"description,omitempty"`
	Optional    bool   `json:"optional,omitempty"`
}

// DocPortPair is one named port, its line kept for errors.
type DocPortPair struct {
	Name string
	Port DocPort
	Line int
}

// DocPorts is a stage's input or output: an ordered mapping of port name to
// port. Order is the author's — prompts and the editor's round-trip keep it.
type DocPorts struct {
	Pairs []DocPortPair `json:"-"` // MarshalJSON writes the mapping itself
}

// portKeys are the keys a port takes, in the order the editor writes them.
var portKeys = []string{"from", "to", "type", "schema", "description", "optional"}

// UnmarshalYAML reads the mapping by hand: a custom unmarshaler bypasses the
// decoder's strictness, so unknown and duplicate keys are refused here.
func (d *DocPorts) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.MappingNode:
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			return nil
		}
		return fmt.Errorf("line %d: must be a mapping of ports — name: { from: …, type: … }", n.Line)
	default:
		return fmt.Errorf("line %d: must be a mapping of ports — name: { from: …, type: … }, not a list", n.Line)
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if seen[k.Value] {
			return fmt.Errorf("line %d: port %q stated twice", k.Line, k.Value)
		}
		seen[k.Value] = true
		if v.Kind != yaml.MappingNode {
			hint := retiredHint(v.Value)
			if hint == "" && !strings.Contains(v.Value, "/") {
				hint = fmt.Sprintf(" — or, for an action's literal setting, with: { %s: %s }", k.Value, v.Value)
			}
			return fmt.Errorf("line %d: port %q: a port is a mapping — %s: { %s, type: … }%s", v.Line, k.Value, k.Value, portHint(v.Value), hint)
		}
		pr := DocPortPair{Name: k.Value, Line: k.Line}
		fields := map[string]bool{}
		for j := 0; j+1 < len(v.Content); j += 2 {
			fk, fv := v.Content[j], v.Content[j+1]
			if fields[fk.Value] {
				return fmt.Errorf("line %d: port %q: %q given twice", fk.Line, k.Value, fk.Value)
			}
			fields[fk.Value] = true
			if fv.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: port %q: %s must be a single value", fv.Line, k.Value, fk.Value)
			}
			switch fk.Value {
			case "from":
				pr.Port.From = fv.Value
			case "to":
				pr.Port.To = fv.Value
			case "type":
				pr.Port.Type = fv.Value
			case "schema":
				pr.Port.Schema = fv.Value
			case "description":
				pr.Port.Description = fv.Value
			case "optional":
				if err := fv.Decode(&pr.Port.Optional); err != nil {
					return fmt.Errorf("line %d: port %q: optional is true or false", fv.Line, k.Value)
				}
			default:
				return fmt.Errorf("line %d: port %q: unknown key %q — a port takes %s", fk.Line, k.Value, fk.Value, strings.Join(portKeys, ", "))
			}
		}
		d.Pairs = append(d.Pairs, pr)
	}
	return nil
}

// portHint is the from: a bare value most likely meant.
func portHint(v string) string {
	if src := retiredSource(v); src != "" {
		return "from: " + src
	}
	return "from: " + v
}

// retiredHint names a retired sentinel's source, or "".
func retiredHint(v string) string {
	if src := retiredSource(v); src != "" {
		return fmt.Sprintf(" (%s is now %s)", v, src)
	}
	return ""
}

// retiredSource maps a v5 sentinel to the source that replaced it.
func retiredSource(v string) string {
	switch v {
	case "$DIFF_TARGET", "$CHANGED":
		return SourceDiffTarget
	case "$DIFF_PHASE":
		return SourceDiffPhase
	case "$STATUS", "$UNCOMMITTED":
		return SourceStatus
	case "$MR_URL", "mr_url":
		return SourceMRURL
	case "$WORKSPACE", "workspace":
		return SourceWorkspace
	case "$DECISION":
		return SourceVerdict
	case "$FAILURE":
		return SourceFailure
	case "ticket":
		return SourceTicket
	}
	return ""
}

// MarshalJSON emits the ports in order, each a {from|to, type, …} object.
func (d DocPorts) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, pr := range d.Pairs {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(pr.Name)
		if err != nil {
			return nil, err
		}
		val, err := json.Marshal(pr.Port)
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

// UnmarshalJSON reads ports back in order. Defensive, like DocIO's: the
// server only ever emits the document.
func (d *DocPorts) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if tok, err := dec.Token(); err != nil {
		return err
	} else if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("expected a JSON object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		name, ok := tok.(string)
		if !ok {
			return fmt.Errorf("expected a string key")
		}
		var p DocPort
		if err := dec.Decode(&p); err != nil {
			return err
		}
		d.Pairs = append(d.Pairs, DocPortPair{Name: name, Port: p})
	}
	return nil
}

// portDir is which side of a stage a port is on.
type portDir string

const (
	portIn  portDir = "input"
	portOut portDir = "output"
)

// buildPorts checks one stage's input or output ports for everything a port
// says by itself: a slug name, a shape, a source or destination of the right
// kind for its type, and the kinds of source its stage type can read. What
// the rest of the flow must agree with — the agent's or action's contract,
// who writes a path — is validateContracts'.
func (p *parser) buildPorts(raw *DocPorts, dir portDir, st StageType, stage string) ([]Port, error) {
	if raw == nil {
		return nil, nil
	}
	out := make([]Port, 0, len(raw.Pairs))
	for _, pr := range raw.Pairs {
		port, err := p.buildPort(pr, dir, st, stage)
		if err != nil {
			return nil, fmt.Errorf("%s %q: %w", dir, pr.Name, err)
		}
		// One path holds one file: a stage cannot write it twice.
		for _, prev := range out {
			if dir == portOut && prev.Value == port.Value {
				return nil, fmt.Errorf("output %q: %q is written by output %q too — one path holds one file", pr.Name, port.Value, prev.Name)
			}
		}
		out = append(out, port)
	}
	return out, nil
}

func (p *parser) buildPort(pr DocPortPair, dir portDir, st StageType, stage string) (Port, error) {
	d := pr.Port
	port := Port{Name: pr.Name, Type: d.Type, Schema: d.Schema, Description: d.Description, Optional: d.Optional}
	if !agentspec.ValidName(pr.Name) {
		return port, fmt.Errorf("name must match [a-z0-9][a-z0-9._-]* (lowercase slug)")
	}
	if err := notAParam(d.Description); err != nil {
		return port, fmt.Errorf("description: %v", err)
	}
	if err := agentspec.CheckShape(d.Type, d.Schema, dir == portOut); err != nil {
		return port, err
	}
	if d.Schema != "" && !p.schemas[d.Schema] {
		return port, fmt.Errorf("schema %q is not declared in this flow (add it to the %q block)", d.Schema, "schemas")
	}
	if dir == portOut {
		switch {
		case d.From != "":
			return port, fmt.Errorf("an output goes somewhere: to: a path, not from:")
		case d.Optional:
			return port, fmt.Errorf("optional is an input's: an output is always written")
		case d.To == "":
			return port, fmt.Errorf("to is required: the path it is written to")
		}
		if err := notAParam(d.To); err != nil {
			return port, err
		}
		if strings.HasPrefix(d.To, RepoPrefix) || IsEngineSource(d.To) || !validPortPath(d.To) {
			return port, fmt.Errorf("to: %q must be a repo-relative path (no repo:, no leading /, no . or .. segments)", d.To)
		}
		port.Value = d.To
		return port, structuredPathRule(port, d.To)
	}

	switch {
	case d.To != "":
		return port, fmt.Errorf("an input comes from somewhere: from: a source, not to:")
	case d.From == "":
		return port, fmt.Errorf("from is required: a path some stage writes, repo:<path>, or one of %s", strings.Join(EngineSources, ", "))
	}
	if src := retiredSource(d.From); src != "" && d.From[0] == '$' {
		return port, fmt.Errorf("%s is now %s", d.From, src)
	}
	if retiredSentinelRe.MatchString(d.From) {
		return port, fmt.Errorf("%s is not a source — a value the engine provides is one of %s", d.From, strings.Join(EngineSources, ", "))
	}
	if err := notAParam(d.From); err != nil {
		return port, err
	}
	from, err := p.resolveServiceSource(d.From, stage, pr.Name)
	if err != nil {
		return port, fmt.Errorf("from: %v", err)
	}
	port.Value = from
	if typ := SourceType(from); typ != "" {
		if d.Type != typ || d.Schema != "" {
			return port, fmt.Errorf("%s is a %s — type: %s", d.From, typ, typ)
		}
		switch st {
		case StageCommand, StageAction:
			return port, fmt.Errorf("%s reads files; %s is a value the engine provides to an agent or a gate", map[StageType]string{StageCommand: "a command", StageAction: "an action"}[st], d.From)
		case StageHumanGate:
			if typ != agentspec.TypeLink {
				return port, fmt.Errorf("a gate shows a file or a link; %s is a %s", d.From, typ)
			}
		}
		if IsEdgeSource(d.From) && !d.Optional {
			return port, fmt.Errorf("%s is empty unless an edge carrying it entered the stage — mark the port optional: true", d.From)
		}
		return port, nil
	}
	path := FilePath(d.From)
	if !validPortPath(path) {
		return port, fmt.Errorf("from: %q must be a path some stage writes, repo:<path>, or one of %s", d.From, strings.Join(EngineSources, ", "))
	}
	if agentspec.IsValueType(d.Type) {
		return port, fmt.Errorf("a %s is a value the engine provides, not a file: from: %s", d.Type, sourceOfType(d.Type))
	}
	return port, structuredPathRule(port, path)
}

// validPortPath is a repo-relative path, or a directory's (a files port's,
// ending in "/").
func validPortPath(p string) bool { return validArtifactPath(strings.TrimSuffix(p, "/")) }

// structuredPathRule: a file the engine decodes to check its shape is JSON or
// YAML, and its extension says which; a directory of files is written as one
// (a trailing "/"), and nothing else is.
func structuredPathRule(port Port, path string) error {
	isDir := strings.HasSuffix(path, "/")
	switch {
	case port.Type == agentspec.TypeFiles && !isDir:
		return fmt.Errorf("files is a directory — its path %q must end in \"/\"", path)
	case port.Type != agentspec.TypeFiles && isDir:
		return fmt.Errorf("%q is a directory, and one is type: files", path)
	}
	if (port.Schema != "" || agentspec.IsStructuredType(port.Type)) && !agentspec.StructuredPath(path) {
		what := "a " + port.Type
		if port.Type == "" {
			what = "a file with a schema"
		}
		return fmt.Errorf("%s is JSON or YAML — its path %q must end in .json, .yaml or .yml", what, path)
	}
	return nil
}

// sourceOfType lists the engine sources of a value type.
func sourceOfType(typ string) string {
	var out []string
	for _, src := range EngineSources {
		if engineSources[src] == typ {
			out = append(out, src)
		}
	}
	return strings.Join(out, " or ")
}

// shape is the structure a port states: a type, a flow schema, or both (the
// type narrowed by the schema).
type shape struct{ typ, schema string }

func shapeOf(p Port) shape { return shape{p.Type, p.Schema} }

func (s shape) String() string {
	switch {
	case s.typ != "" && s.schema != "":
		return fmt.Sprintf("a %s with schema %q", s.typ, s.schema)
	case s.schema != "":
		return fmt.Sprintf("schema %q", s.schema)
	}
	return "a " + s.typ
}

// isFile reports whether the shape is a file's: a file type, or a schema.
func (s shape) isFile() bool {
	return (agentspec.IsFileType(s.typ) && s.typ != agentspec.TypeFiles) || (s.typ == "" && s.schema != "")
}

// satisfies reports whether a value of shape s is what need asks for: the
// type it names (any file satisfies `file`), and the schema it names. A
// writer may promise more than a reader asks.
func (s shape) satisfies(need shape) bool {
	switch {
	case need.typ == "":
	case need.typ == agentspec.TypeFile:
		if !s.isFile() {
			return false
		}
	case s.typ != need.typ:
		return false
	}
	return need.schema == "" || s.schema == need.schema
}

// actionPorts holds an action stage's ports to the action's signature: every
// port is one the action has, a required input is stated, every output is
// stated (it writes them all; the stage says where), and each port's shape
// is what the action reads or writes.
func actionPorts(act *actionspec.Action, s *Stage) error {
	check := func(dir portDir, ports []Port, sigs []actionspec.PortSig) error {
		stated := map[string]bool{}
		for _, pt := range ports {
			var sig *actionspec.PortSig
			for i := range sigs {
				if sigs[i].Name == pt.Name {
					sig = &sigs[i]
				}
			}
			if sig == nil {
				if dir == portIn && act.Setting(pt.Name) != nil {
					return fmt.Errorf("input %q: %q is a setting, a literal — with: { %s: … }", pt.Name, pt.Name, pt.Name)
				}
				return fmt.Errorf("%s %q is not an %s of action %q (%ss: %s)", dir, pt.Name, dir, act.Slug, dir, actionspec.Names(sigs))
			}
			stated[pt.Name] = true
			need := shape{typ: sig.Type}
			if dir == portIn && !shapeOf(pt).satisfies(need) {
				return fmt.Errorf("input %q: action %q reads %s, the port says %s", pt.Name, act.Slug, need, shapeOf(pt))
			}
			if dir == portOut && !shapeOf(pt).satisfies(need) {
				return fmt.Errorf("output %q: action %q writes %s, the port says %s", pt.Name, act.Slug, need, shapeOf(pt))
			}
		}
		for _, sig := range sigs {
			if stated[sig.Name] {
				continue
			}
			if dir == portOut {
				return fmt.Errorf("action %q writes %q — state where: output: { %s: { to: %s, type: %s } }", act.Slug, sig.Name, sig.Name, sig.DefaultPath, sig.Type)
			}
			if sig.Required {
				return fmt.Errorf("action %q requires input %q (%s): input: { %s: { from: …, type: %s } }", act.Slug, sig.Name, sig.Description, sig.Name, sig.Type)
			}
		}
		return nil
	}
	if err := check(portIn, s.Inputs, act.Inputs); err != nil {
		return err
	}
	return check(portOut, s.Outputs, act.Outputs)
}
