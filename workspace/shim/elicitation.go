package main

// A harness's own question, over ACP. When a harness asks the human
// something itself — codex's request_user_input, an MCP server's
// elicitation it forwards — it arrives as elicitation/create: a form
// (MCP elicitation's restricted JSON Schema) the client is to fill in. The
// sidecar puts it to the human exactly as ask_human does — the same ping,
// the same wait, the same parking — and answers the request with the human's
// form. ask_human stays the tool agents are told to use; this is what keeps a
// harness that asks its own way from hanging, or from being answered with
// nothing.

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"strings"
)

// secretNotice stands in for a secret a harness asked the human to type: a
// ping's answer is kept in the run's log, so a secret never goes through one.
const secretNotice = "(not asked: secrets are never collected through a question — " +
	"ask for it to be configured as a project secret instead)"

// proceedField is the one field of a message-only form: go ahead, or not.
const proceedField = "proceed"

// elicitField is one property of the requested schema, as MCP/ACP write it.
type elicitField struct {
	Type        string          `json:"type"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	Enum        []string        `json:"enum,omitempty"`
	EnumNames   []string        `json:"enumNames,omitempty"` // legacy titles for enum
	OneOf       []elicitChoice  `json:"oneOf,omitempty"`
	Items       *elicitItems    `json:"items,omitempty"`
	Format      string          `json:"format,omitempty"`
	Pattern     string          `json:"pattern,omitempty"`
	MinLength   *int            `json:"minLength,omitempty"`
	MaxLength   *int            `json:"maxLength,omitempty"`
	Minimum     *float64        `json:"minimum,omitempty"`
	Maximum     *float64        `json:"maximum,omitempty"`
	MinItems    *int            `json:"minItems,omitempty"`
	MaxItems    *int            `json:"maxItems,omitempty"`
	Default     any             `json:"default,omitempty"`
	Meta        json.RawMessage `json:"_meta,omitempty"`
}

type elicitChoice struct {
	Const       string `json:"const"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

type elicitItems struct {
	Enum  []string       `json:"enum,omitempty"`
	AnyOf []elicitChoice `json:"anyOf,omitempty"`
	OneOf []elicitChoice `json:"oneOf,omitempty"`
}

// pingOption and pingField are the engine's event.Option and event.Field,
// as JSON.
type pingOption struct {
	ID     string `json:"id"`
	Label  string `json:"label,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type pingField struct {
	ID          string       `json:"id"`
	Type        string       `json:"type"`
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description,omitempty"`
	Required    bool         `json:"required,omitempty"`
	Options     []pingOption `json:"options,omitempty"`
	Format      string       `json:"format,omitempty"`
	Pattern     string       `json:"pattern,omitempty"`
	MinLength   *int         `json:"min_length,omitempty"`
	MaxLength   *int         `json:"max_length,omitempty"`
	Minimum     *float64     `json:"minimum,omitempty"`
	Maximum     *float64     `json:"maximum,omitempty"`
	MinItems    *int         `json:"min_items,omitempty"`
	MaxItems    *int         `json:"max_items,omitempty"`
	Default     any          `json:"default,omitempty"`
}

// onElicitation answers elicitation/create with the human's form: accept
// with its content, decline, or cancel when the question could not be put
// (or the run was parked before an answer came — the session then hears the
// answer when it resumes).
func (h *agentHost) onElicitation(params json.RawMessage, done <-chan struct{}) (any, *rpcError) {
	var r struct {
		Mode            string `json:"mode"`
		Message         string `json:"message"`
		RequestedSchema struct {
			Properties json.RawMessage `json:"properties"`
			Required   []string        `json:"required"`
		} `json:"requestedSchema"`
	}
	if err := json.Unmarshal(params, &r); err != nil {
		return nil, &rpcError{Code: -32602, Message: "elicitation/create: " + err.Error()}
	}
	if r.Mode != "form" {
		return elicitResult("decline", nil), nil
	}
	fields, secrets, err := elicitFields(r.RequestedSchema.Properties, r.RequestedSchema.Required)
	if err != nil {
		return nil, &rpcError{Code: -32602, Message: "elicitation/create: " + err.Error()}
	}
	content := map[string]any{}
	for _, id := range secrets {
		content[id] = secretNotice
	}
	messageOnly := len(fields) == 0 && len(secrets) == 0
	switch {
	case messageOnly:
		fields = []pingField{{ID: proceedField, Type: "boolean", Title: "Go ahead?", Required: true}}
	case len(fields) == 0:
		return elicitResult("accept", content), nil // nothing the human may be asked
	}
	question := strings.TrimSpace(r.Message)
	if question == "" {
		question = "The agent needs your input to continue."
	}
	raw := make([]json.RawMessage, len(fields))
	for i, f := range fields {
		raw[i], _ = json.Marshal(f)
	}
	ans, err := askHuman(askArgs{Type: "clarification", Question: question, Fields: raw}, done, nil)
	switch {
	case errors.Is(err, errCancelled):
		return elicitResult("cancel", nil), nil
	case err != nil:
		log.Printf("sidecar: elicitation: %v", err)
		return elicitResult("cancel", nil), nil
	case ans.Parked:
		return elicitResult("cancel", nil), nil
	case ans.Answer != "accept":
		return elicitResult("decline", nil), nil
	case messageOnly:
		if ans.Content[proceedField] == true {
			return elicitResult("accept", map[string]any{}), nil
		}
		return elicitResult("decline", nil), nil
	}
	for k, v := range ans.Content {
		content[k] = v
	}
	return elicitResult("accept", content), nil
}

func elicitResult(action string, content map[string]any) map[string]any {
	out := map[string]any{"action": action}
	if action == "accept" {
		out["content"] = content
	}
	return out
}

// elicitFields maps the requested schema's properties, in their order, onto
// ping fields. Secret ones (codex marks them in _meta) are not asked: their
// ids come back separately.
func elicitFields(props json.RawMessage, required []string) (fields []pingField, secrets []string, err error) {
	keys, vals, err := orderedObject(props)
	if err != nil {
		return nil, nil, err
	}
	for _, id := range keys {
		var p elicitField
		if err := json.Unmarshal(vals[id], &p); err != nil {
			return nil, nil, err
		}
		if isSecret(p.Meta) {
			secrets = append(secrets, id)
			continue
		}
		f := pingField{
			ID: id, Type: p.Type, Title: p.Title, Description: p.Description,
			Format: p.Format, Pattern: p.Pattern, MinLength: p.MinLength, MaxLength: p.MaxLength,
			Minimum: p.Minimum, Maximum: p.Maximum, MinItems: p.MinItems, MaxItems: p.MaxItems,
			Default: p.Default,
		}
		for _, r := range required {
			f.Required = f.Required || r == id
		}
		switch p.Type {
		case "string":
			f.Options = choices(p.OneOf, p.Enum, p.EnumNames)
		case "array":
			if p.Items != nil {
				f.Options = choices(append(p.Items.AnyOf, p.Items.OneOf...), p.Items.Enum, nil)
			}
		}
		fields = append(fields, f)
	}
	return fields, secrets, nil
}

// choices reads a property's options: titled oneOf/anyOf entries, else a
// bare enum (with the legacy enumNames as labels).
func choices(titled []elicitChoice, enum, names []string) []pingOption {
	var out []pingOption
	for _, c := range titled {
		out = append(out, pingOption{ID: c.Const, Label: c.Title, Detail: c.Description})
	}
	for i, v := range enum {
		o := pingOption{ID: v}
		if i < len(names) {
			o.Label = names[i]
		}
		out = append(out, o)
	}
	return out
}

func isSecret(meta json.RawMessage) bool {
	var m struct {
		Codex struct {
			IsSecret bool `json:"isSecret"`
		} `json:"codex"`
	}
	return len(meta) > 0 && json.Unmarshal(meta, &m) == nil && m.Codex.IsSecret
}

// orderedObject reads a JSON object's keys in the order written — the order
// a form's fields are read in, which a Go map would lose.
func orderedObject(raw json.RawMessage) ([]string, map[string]json.RawMessage, error) {
	vals := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, vals, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, nil, errors.New("properties is not an object")
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, err
		}
		if _, dup := vals[key]; !dup {
			keys = append(keys, key)
		}
		vals[key] = v
	}
	return keys, vals, nil
}
