package flow

// Flow schemas (FLOWLANG §4): the structures a flow's author defines for the
// files its own stages pass around. A built-in type (agentspec.FileTypes)
// exists only for a structure something built in reads — the router, an
// action; everything else is the author's, declared once under `schemas:`
// and named by the outputs that write it and the inputs that read it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"gopkg.in/yaml.v3"

	"apothecary/internal/flow/agentspec"
	"apothecary/internal/flow/reviewfile"
)

// DocSchema is one entry of `schemas:`: a name the agents' inputs and
// outputs refer to, and the JSON Schema the file must satisfy, written in
// YAML like the rest of the document.
type DocSchema struct {
	Name        string  `yaml:"name" json:"name"`
	Description string  `yaml:"description" json:"description,omitempty"`
	JSONSchema  DocJSON `yaml:"json_schema" json:"json_schema"`
}

// DocJSON is a JSON value written in YAML. It keeps the YAML node rather
// than a decoded map, so the editor receives the keys in the order the
// author wrote them — a schema reads type, then properties, then required —
// and re-emits them in that order.
type DocJSON struct{ node yaml.Node }

func (d *DocJSON) UnmarshalYAML(n *yaml.Node) error {
	d.node = *n
	return nil
}

// MarshalJSON emits the value as JSON, mapping keys in document order.
func (d DocJSON) MarshalJSON() ([]byte, error) {
	if d.node.Kind == 0 {
		return []byte("null"), nil
	}
	var b bytes.Buffer
	if err := nodeJSON(&b, &d.node); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// UnmarshalJSON reads a JSON value back into a node. Defensive, like DocIO's:
// the server only ever emits the document. Order is not kept on this path.
func (d *DocJSON) UnmarshalJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	return d.node.Encode(v)
}

// nodeJSON writes a YAML node as JSON. Only what JSON can say is accepted:
// string keys, finite numbers — a schema is a JSON document written in YAML,
// not a YAML one.
func nodeJSON(b *bytes.Buffer, n *yaml.Node) error {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			b.WriteString("null")
			return nil
		}
		return nodeJSON(b, n.Content[0])
	case yaml.MappingNode:
		b.WriteByte('{')
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || (k.Tag != "!!str" && k.Tag != "") {
				return fmt.Errorf("line %d: a key must be a string, got %q", k.Line, k.Value)
			}
			if i > 0 {
				b.WriteByte(',')
			}
			key, _ := json.Marshal(k.Value)
			b.Write(key)
			b.WriteByte(':')
			if err := nodeJSON(b, n.Content[i+1]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case yaml.SequenceNode:
		b.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := nodeJSON(b, c); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case yaml.ScalarNode:
		var v any
		if err := n.Decode(&v); err != nil {
			return fmt.Errorf("line %d: %v", n.Line, err)
		}
		if f, ok := v.(float64); ok && (math.IsInf(f, 0) || math.IsNaN(f)) {
			return fmt.Errorf("line %d: %s is not a JSON number", n.Line, n.Value)
		}
		out, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("line %d: %v", n.Line, err)
		}
		b.Write(out)
	default:
		return fmt.Errorf("line %d: an alias is not JSON", n.Line)
	}
	return nil
}

// DataSchema is one compiled structure: a flow schema, or the shape of a
// built-in type that has one (TypeSchema).
type DataSchema struct {
	Name        string
	Description string
	// Source is the schema as JSON, in document order: what an agent's
	// OUTPUT CONTRACT shows it.
	Source   json.RawMessage
	compiled *jsonschema.Schema
}

// Schema returns the flow schema named name, or nil.
func (f *Flow) Schema(name string) *DataSchema {
	for i := range f.Schemas {
		if f.Schemas[i].Name == name {
			return &f.Schemas[i]
		}
	}
	return nil
}

// PortSchemas are the structures a port's file must hold: its type's shape
// (every structured built-in type has one), then its flow schema. Empty for
// a file the engine only checks exists. What a schema cannot say about a
// type — a path inside the repository, a thread the round loaded — the
// engine checks after it.
func (f *Flow) PortSchemas(p Port) []*DataSchema {
	var out []*DataSchema
	if s := TypeSchema(p.Type); s != nil {
		out = append(out, s)
	}
	if p.Schema != "" {
		if s := f.Schema(p.Schema); s != nil {
			out = append(out, s)
		}
	}
	return out
}

// unusedSchemas names the flow schemas no agent input or output, nor any
// command binding, refers to.
func unusedSchemas(f *Flow) []string {
	used := map[string]bool{}
	for _, a := range f.Agents {
		for _, in := range a.Inputs {
			used[in.Schema] = true
		}
		for _, out := range a.Outputs {
			used[out.Schema] = true
		}
	}
	for i := range f.Stages {
		for _, b := range append(slices.Clone(f.Stages[i].Inputs), f.Stages[i].Outputs...) {
			used[b.Schema] = true
		}
	}
	var out []string
	for _, s := range f.Schemas {
		if !used[s.Name] {
			out = append(out, s.Name)
		}
	}
	return out
}

// compileSchema compiles one schema document. Nothing is ever loaded: a
// $ref resolves inside the document or not at all, so a flow cannot make
// the engine read a file or fetch a URL.
func compileSchema(name string, src []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(jsonschema.SchemeURLLoader{})
	url := "apothecary:schemas/" + name
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	s, err := c.Compile(url)
	if err != nil {
		var load *jsonschema.LoadURLError
		if errors.As(err, &load) {
			return nil, fmt.Errorf("$ref %q: a $ref resolves inside the schema only — nothing is loaded", load.URL)
		}
		return nil, fmt.Errorf("%s", strings.ReplaceAll(err.Error(), url, "the schema"))
	}
	return s, nil
}

// buildSchemas compiles the document's `schemas:`, refusing a duplicate or
// malformed name and a schema JSON Schema itself refuses.
func (p *parser) buildSchemas(raw []DocSchema) ([]DataSchema, error) {
	out := make([]DataSchema, 0, len(raw))
	seen := map[string]int{}
	for i := range raw {
		ds := &raw[i]
		at := p.seqLine("schemas", i)
		if at == 0 {
			at = p.keyLine("schemas")
		}
		if !agentspec.ValidName(ds.Name) {
			return nil, p.errf(at, "schema #%d: name must match [a-z0-9][a-z0-9._-]* (lowercase slug), got %q", i+1, ds.Name)
		}
		if prev, dup := seen[ds.Name]; dup {
			return nil, p.errf(at, "schema %q: declared twice (first at line %d)", ds.Name, prev)
		}
		seen[ds.Name] = at
		if err := notAParam(ds.Description); err != nil {
			return nil, p.errf(at, "schema %q: description: %v", ds.Name, err)
		}
		if ds.JSONSchema.node.Kind == 0 {
			return nil, p.errf(at, "schema %q: json_schema is required — the JSON Schema the file must satisfy, written in YAML", ds.Name)
		}
		src, err := ds.JSONSchema.MarshalJSON()
		if err != nil {
			return nil, p.errf(at, "schema %q: json_schema: %v", ds.Name, err)
		}
		compiled, err := compileSchema(ds.Name, src)
		if err != nil {
			return nil, p.errf(at, "schema %q: json_schema is not a valid JSON Schema: %v", ds.Name, err)
		}
		out = append(out, DataSchema{Name: ds.Name, Description: ds.Description, Source: src, compiled: compiled})
	}
	return out, nil
}

// Check decodes a file by its extension — .json as JSON, .yaml or .yml as
// YAML — and validates it. The error names every violation, each at its
// place in the document, for an agent to fix on its next attempt.
func (s *DataSchema) Check(path string, data []byte) error {
	v, err := DecodeStructured(path, data)
	if err != nil {
		return err
	}
	if err := s.compiled.Validate(v); err != nil {
		return fmt.Errorf("%s does not match schema %q:\n%s", path, s.Name, describeViolations(err))
	}
	return nil
}

// DecodeStructured reads a JSON or YAML file into the value JSON Schema
// validates (numbers as json.Number).
func DecodeStructured(path string, data []byte) (any, error) {
	if !strings.HasSuffix(path, ".json") {
		var y any
		if err := yaml.Unmarshal(data, &y); err != nil {
			return nil, fmt.Errorf("%s is not valid YAML: %v", path, err)
		}
		b, err := json.Marshal(y)
		if err != nil {
			return nil, fmt.Errorf("%s does not hold JSON-compatible data: %v", path, err)
		}
		data = b
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %v", path, err)
	}
	return v, nil
}

// describeViolations flattens a validation error to its leaves, one line
// each: where in the file, and what is wrong there.
func describeViolations(err error) string {
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return "- " + err.Error()
	}
	en := message.NewPrinter(language.English)
	var lines []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			at := "/" + strings.Join(e.InstanceLocation, "/")
			lines = append(lines, fmt.Sprintf("- at %s: %s", at, e.ErrorKind.LocalizedString(en)))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// builtinTypeSchemas are the shapes of the structured built-in types, each
// shown verbatim in the agent's OUTPUT CONTRACT and checked like a flow
// schema: the verdict the router reads, the commit message the commit
// action reads, and the three review documents (reviewfile), which the
// review actions post and load.
var builtinTypeSchemas = map[string]*DataSchema{}

func init() {
	for typ, src := range map[string]string{
		agentspec.TypeReviewThreads: reviewfile.SchemaReviewThreads,
		agentspec.TypeReviewReplies: reviewfile.SchemaReviewReplies,
		agentspec.TypeMRThreads:     reviewfile.SchemaMRThreads,
		agentspec.TypeVerdict: `{
  "type": "object",
  "properties": {
    "verdict": {"enum": ["APPROVE", "REJECT"]},
    "summary": {"type": "string", "description": "optional: the verdict in a sentence or two"},
    "comments": {
      "type": "array",
      "items": {"oneOf": [
        {"type": "string", "minLength": 1, "description": "a remark on the change as a whole"},
        {"type": "object",
         "properties": {
           "path": {"type": "string", "minLength": 1, "description": "the file, relative to the repository root"},
           "line": {"type": "integer", "minimum": 1, "description": "optional: the line (without it, the whole file)"},
           "end_line": {"type": "integer", "minimum": 1, "description": "optional: the last line of a range"},
           "quote": {"type": "string", "description": "optional: the text the comment is about"},
           "body": {"type": "string", "minLength": 1}
         },
         "required": ["path", "body"],
         "additionalProperties": false}
      ]}
    }
  },
  "required": ["verdict"],
  "additionalProperties": false
}`,
		agentspec.TypeCommitMessage: `{
  "type": "object",
  "properties": {
    "subject": {"type": "string", "minLength": 1, "maxLength": 100, "pattern": "^[^\\n]*\\S[^\\n]*$",
                "description": "the commit's subject line: one line, the project's commit style"},
    "body": {"type": "string", "description": "optional: the commit's body, plain text after a blank line"}
  },
  "required": ["subject"],
  "additionalProperties": false
}`,
	} {
		compiled, err := compileSchema(typ, []byte(src))
		if err != nil {
			panic(fmt.Sprintf("built-in schema %s: %v", typ, err))
		}
		var compact bytes.Buffer
		_ = json.Compact(&compact, []byte(src))
		builtinTypeSchemas[typ] = &DataSchema{Name: typ, Source: compact.Bytes(), compiled: compiled}
	}
}

// TypeSchema returns the shape of a built-in output type the engine checks
// with a schema, or nil.
func TypeSchema(typ string) *DataSchema { return builtinTypeSchemas[typ] }
