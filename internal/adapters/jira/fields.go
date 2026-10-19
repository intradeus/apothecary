package jira

// Fields by DISPLAY NAME. A flow says "Feature flags", not customfield_10042:
// the name is what a human sees on the issue and what survives a Jira
// migration, and the id is an accident of the site. The field list is
// fetched once per client and the value is coerced to what the field's
// schema wants — a text field takes the string, a labels field a list, a
// select an {value}, a paragraph field an ADF document on v3.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"apothecary/internal/ports"
)

type fieldJSON struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Custom bool   `json:"custom"`
	Schema struct {
		Type   string `json:"type"`
		Items  string `json:"items"`
		Custom string `json:"custom"`
	} `json:"schema"`
}

// fields lists the site's fields (GET /field), cached for the client's life:
// the engine constructs a client per resolution, so a renamed field is seen
// by the next run.
func (c *Client) fields(ctx context.Context) ([]fieldJSON, error) {
	c.fieldMu.Lock()
	defer c.fieldMu.Unlock()
	if c.fieldList != nil {
		return c.fieldList, nil
	}
	var list []fieldJSON
	if _, err := c.api(ctx, http.MethodGet, c.apiBase()+"/field", nil, &list); err != nil {
		return nil, err
	}
	c.fieldList = list
	return list, nil
}

// resolveField finds a field by raw id or by display name (case-insensitive).
// Two custom fields may share a display name on a Jira site; that is an
// ambiguity the flow must settle by id, and the error says which ids.
func (c *Client) resolveField(ctx context.Context, key, name string) (fieldJSON, error) {
	name = strings.TrimSpace(name)
	list, err := c.fields(ctx)
	if err != nil {
		return fieldJSON{}, err
	}
	for _, f := range list {
		if f.ID == name {
			return f, nil
		}
	}
	var matches []fieldJSON
	for _, f := range list {
		if strings.EqualFold(f.Name, name) {
			matches = append(matches, f)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		var near []string
		for _, f := range list {
			if strings.Contains(strings.ToLower(f.Name), strings.ToLower(name)) && len(near) < 5 {
				near = append(near, f.Name)
			}
		}
		return fieldJSON{}, &ports.ErrFieldUnknown{Key: key, Field: name, Candidates: near}
	default:
		ids := make([]string, 0, len(matches))
		for _, f := range matches {
			ids = append(ids, f.Name+" ("+f.ID+")")
		}
		return fieldJSON{}, &ports.ErrFieldUnknown{Key: key, Field: name, Candidates: ids}
	}
}

// splitItems is the list form of a text value: comma-separated, trimmed,
// empties dropped.
func splitItems(value string) []string {
	var out []string
	for part := range strings.SplitSeq(value, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// coerce turns a text value into the JSON value the field's schema wants.
func (c *Client) coerce(key string, f fieldJSON, value string) (any, error) {
	unsupported := func(typ string) error {
		return &ports.ErrFieldUnsupported{Key: key, Field: f.Name, Type: typ}
	}
	switch f.Schema.Type {
	case "string":
		// A paragraph ("textarea") custom field is rich text: ADF on v3.
		if strings.HasSuffix(f.Schema.Custom, ":textarea") {
			return c.richText(value), nil
		}
		return value, nil
	case "number":
		if value == "" {
			return nil, nil
		}
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not a number", unsupported("number"), value)
		}
		return n, nil
	case "array":
		items := splitItems(value)
		switch f.Schema.Items {
		case "string":
			if items == nil {
				items = []string{}
			}
			return items, nil
		case "option":
			out := make([]map[string]any, 0, len(items))
			for _, it := range items {
				out = append(out, map[string]any{"value": it})
			}
			return out, nil
		case "component", "version":
			out := make([]map[string]any, 0, len(items))
			for _, it := range items {
				out = append(out, map[string]any{"name": it})
			}
			return out, nil
		}
		return nil, unsupported("array of " + f.Schema.Items)
	case "option":
		if value == "" {
			return nil, nil
		}
		return map[string]any{"value": value}, nil
	case "priority":
		if value == "" {
			return nil, nil
		}
		return map[string]any{"name": value}, nil
	}
	return nil, unsupported(f.Schema.Type)
}

// SetFields writes the named fields in one PUT.
func (c *Client) SetFields(ctx context.Context, key string, fields map[string]string) error {
	key = strings.TrimSpace(key)
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	payload := map[string]any{}
	for _, name := range names {
		f, err := c.resolveField(ctx, key, name)
		if err != nil {
			return err
		}
		v, err := c.coerce(key, f, fields[name])
		if err != nil {
			return err
		}
		payload[f.ID] = v
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := c.api(ctx, http.MethodPut, c.issuePath(key), map[string]any{"fields": payload}, nil)
	return err
}

// Fields reads the named fields' current values, rendered as the text
// SetFields accepts (lists comma-joined), so append mode can merge.
func (c *Client) Fields(ctx context.Context, key string, names []string) (map[string]string, error) {
	key = strings.TrimSpace(key)
	resolved := map[string]fieldJSON{}
	ids := make([]string, 0, len(names))
	for _, name := range names {
		f, err := c.resolveField(ctx, key, name)
		if err != nil {
			return nil, err
		}
		resolved[name] = f
		ids = append(ids, f.ID)
	}
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	var issue struct {
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if _, err := c.api(ctx, http.MethodGet, c.issuePath(key)+"?fields="+strings.Join(ids, ","), nil, &issue); err != nil {
		return nil, err
	}
	for _, name := range names {
		out[name] = renderField(issue.Fields[resolved[name].ID])
	}
	return out, nil
}

// renderField renders a field value as text: a string as is, an ADF document
// as markdown, a number as itself, an object by its value/name, a list of
// any of those comma-joined. null is "".
func renderField(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return renderValue(v)
}

func renderValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			if s := renderValue(e); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		if t, ok := x["type"].(string); ok && t == "doc" {
			raw, _ := json.Marshal(x)
			return ToMarkdown(raw)
		}
		for _, k := range []string{"value", "name", "displayName", "key"} {
			if s, ok := x[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
