package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"apothecary/internal/flow"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/skills"
)

// Page is one page the assistant serves: what the agent is told, the
// presets the panel offers, what a fill looks like, how the page's state
// reaches the agent, and the engine's check of a fill before any page sees
// it.
type Page struct {
	Kind Kind
	// Instructions are the agent's standing instructions on this page.
	Instructions string
	// Presets are the panel's one-click questions.
	Presets []string
	// FillSchema is the fill tool's input: this page's fill and nothing else.
	FillSchema map[string]any
	// Repos: a session on this page reads its workshop's repositories.
	Repos bool
}

// Checks are what a page's check reads beyond the fill itself.
type Checks interface {
	// TicketTypes are the issue type ids of a workshop's ticket source, as
	// person reaches it; empty when it has none.
	TicketTypes(ctx context.Context, person, workshop string) ([]string, error)
}

// commonInstructions open every page's.
const commonInstructions = `You are the Apothecary assistant, beside a page of the Apothecary web app. Apothecary runs AI agents on repositories: a workshop is a project (its repositories, flows, tickets and settings), a flow is a YAML document that says what a run does, a ticket is a piece of work a run starts on, a skill is instructions and files an agent is given.

You can read, never change: the workshop's repositories in your working directory (your own read and search tools), their history (the git tool), and the platform (the apothecary tools: workshops, tickets, flows, skills, runs). Your one way to act is the fill tool: it puts what you propose into the person's page, for them to read, change and save. Nothing is saved until they save it; never say it is.

Call fill with the whole of what the page should hold, not a difference. If fill answers with errors, correct them and call it again. Then tell the person, briefly, what you filled and why. When the person asks a question that needs no change, answer it and do not call fill.`

var pages = map[Kind]Page{
	KindFlow: {
		Kind: KindFlow,
		Instructions: commonInstructions + `

This page is a flow's editor. A fill replaces the editor's whole YAML document. Read the flow schema (get_flow_schema) and the built-in flows (list_flow_templates) when you need the language; validate_flow checks a document without filling it. Keep what the person did not ask to change exactly as it is.`,
		Presets: []string{
			"Explain what this flow does, stage by stage.",
			"Add a review stage before the merge request is opened.",
			"Why does this flow not validate? Fix it.",
		},
		FillSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"yaml": map[string]any{"type": "string", "description": "The whole flow document, as YAML."},
			},
			"required":             []string{"yaml"},
			"additionalProperties": false,
		},
		Repos: true,
	},
	KindTicket: {
		Kind: KindTicket,
		Instructions: commonInstructions + `

This page is a ticket's form. A fill sets any of its fields: title (one line), goal (what done looks like, in Markdown: the agent working the ticket reads it), change_type, type_id (one of the workshop's issue types). Leave out a field you do not mean to change. Read the repositories to make the goal precise: name the files, the behaviour, how to check it.`,
		Presets: []string{
			"Turn my notes into a ticket: a title, a goal and its type.",
			"Make the goal precise: name the files and how to check the change.",
			"Is this already done in the repository?",
		},
		FillSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":       map[string]any{"type": "string", "description": "One line."},
				"goal":        map[string]any{"type": "string", "description": "What done looks like, in Markdown."},
				"change_type": map[string]any{"type": "string", "enum": changeTypes(), "description": "The kind of change."},
				"type_id":     map[string]any{"type": "string", "description": "One of the workshop's issue type ids."},
			},
			"additionalProperties": false,
		},
		Repos: true,
	},
	KindSkill: {
		Kind: KindSkill,
		Instructions: commonInstructions + `

This page is a skill's editor. A skill is a folder: SKILL.md, which opens with YAML frontmatter holding name and description (when an agent should use the skill), then its instructions in Markdown; and any support files. A fill sets the skill's name and its files (each a path and its whole content); the name must match SKILL.md's. A file you leave out of a fill is removed, except a file that is not text (an image, an archive), which stays. When the person attached a workshop, its repositories are in your working directory: read them to make the skill fit the code it is for.`,
		Presets: []string{
			"Write SKILL.md from my description.",
			"Make the description say precisely when to use this skill.",
			"Review this skill: what is unclear or missing?",
		},
		FillSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "The skill's name, as in SKILL.md."},
				"files": map[string]any{"type": "array", "description": "Every file of the skill, SKILL.md included.",
					"items": map[string]any{"type": "object", "properties": map[string]any{
						"path":    map[string]any{"type": "string"},
						"content": map[string]any{"type": "string"},
					}, "required": []string{"path", "content"}, "additionalProperties": false}},
			},
			"required":             []string{"name", "files"},
			"additionalProperties": false,
		},
		// An attached workshop's: a skill session reads none without one.
		Repos: true,
	},
}

// PageOf is a kind's page.
func PageOf(k Kind) (Page, bool) {
	p, ok := pages[k]
	return p, ok
}

func changeTypes() []any {
	var out []any
	for _, t := range trackerport.ChangeTypes() {
		out = append(out, string(t))
	}
	return out
}

// maxFill bounds a fill: a flow or a skill's files, not a dump.
const maxFill = 512 << 10

// Check checks a fill for the page: its errors for the agent to correct,
// or the fill as the page will apply it.
func (p Page) Check(ctx context.Context, c Checks, person, workshop string, raw json.RawMessage) (json.RawMessage, []string) {
	if len(raw) > maxFill {
		return nil, []string{fmt.Sprintf("the fill is %d bytes; at most %d", len(raw), maxFill)}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	switch p.Kind {
	case KindFlow:
		var f struct {
			YAML string `json:"yaml"`
		}
		if err := dec.Decode(&f); err != nil {
			return nil, []string{"the fill is not {yaml}: " + err.Error()}
		}
		if strings.TrimSpace(f.YAML) == "" {
			return nil, []string{"yaml is empty"}
		}
		if _, err := flow.Parse("flow", []byte(f.YAML)); err != nil {
			return nil, []string{err.Error()}
		}
		if _, err := flow.ParseDoc("flow", []byte(f.YAML)); err != nil {
			return nil, []string{err.Error()}
		}
		out, _ := json.Marshal(f)
		return out, nil
	case KindTicket:
		var f struct {
			Title      *string `json:"title,omitempty"`
			Goal       *string `json:"goal,omitempty"`
			ChangeType *string `json:"change_type,omitempty"`
			TypeID     *string `json:"type_id,omitempty"`
		}
		if err := dec.Decode(&f); err != nil {
			return nil, []string{"the fill is not {title, goal, change_type, type_id}: " + err.Error()}
		}
		var errs []string
		if f.Title == nil && f.Goal == nil && f.ChangeType == nil && f.TypeID == nil {
			errs = append(errs, "the fill sets nothing")
		}
		if f.Title != nil && (strings.TrimSpace(*f.Title) == "" || strings.ContainsAny(*f.Title, "\r\n")) {
			errs = append(errs, "title is one line, not empty")
		}
		if f.ChangeType != nil && !slices.Contains(changeTypes(), any(*f.ChangeType)) {
			errs = append(errs, fmt.Sprintf("change_type %q is not one of %v", *f.ChangeType, changeTypes()))
		}
		if f.TypeID != nil {
			types, err := c.TicketTypes(ctx, person, workshop)
			switch {
			case err != nil:
				errs = append(errs, "the workshop's issue types: "+err.Error())
			case !slices.Contains(types, *f.TypeID):
				errs = append(errs, fmt.Sprintf("type_id %q is not one of the workshop's: %v", *f.TypeID, types))
			}
		}
		if len(errs) > 0 {
			return nil, errs
		}
		out, _ := json.Marshal(f)
		return out, nil
	case KindSkill:
		var f struct {
			Name  string `json:"name"`
			Files []struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			} `json:"files"`
		}
		if err := dec.Decode(&f); err != nil {
			return nil, []string{"the fill is not {name, files}: " + err.Error()}
		}
		tree := make([]envport.TreeFile, 0, len(f.Files))
		for _, file := range f.Files {
			tree = append(tree, envport.TreeFile{Path: file.Path, Data: []byte(file.Content)})
		}
		if _, _, err := skills.Validate(f.Name, tree); err != nil {
			return nil, []string{err.Error()}
		}
		out, _ := json.Marshal(f)
		return out, nil
	}
	return nil, []string{"no page " + string(p.Kind)}
}

// maxContext bounds what the page's state adds to a prompt.
const maxContext = 96 << 10

// Context renders the page's state, as the panel sent it, for the prompt:
// the agent reads what the person sees, unsaved edits included.
func (p Page) Context(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var b strings.Builder
	switch p.Kind {
	case KindFlow:
		var c struct {
			YAML   string            `json:"yaml"`
			Errors []string          `json:"errors"`
			Params map[string]string `json:"params"`
		}
		_ = json.Unmarshal(raw, &c)
		fmt.Fprintf(&b, "The flow in the editor now (unsaved edits included):\n```yaml\n%s\n```\n", c.YAML)
		if len(c.Errors) > 0 {
			fmt.Fprintf(&b, "Its errors: %s\n", strings.Join(c.Errors, "; "))
		}
		if len(c.Params) > 0 {
			keys := make([]string, 0, len(c.Params))
			for k := range c.Params {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			b.WriteString("The workshop binds its parameters to:")
			for _, k := range keys {
				fmt.Fprintf(&b, " %s=%s", k, c.Params[k])
			}
			b.WriteString("\n")
		}
	default:
		// The form as it stands, as the panel sent it.
		var pretty bytes.Buffer
		if json.Indent(&pretty, raw, "", "  ") == nil {
			fmt.Fprintf(&b, "The %s's form now (unsaved edits included):\n```json\n%s\n```\n", p.Kind, pretty.String())
		}
	}
	out := b.String()
	if len(out) > maxContext {
		out = out[:maxContext] + "\n[the page's state is cut here]\n"
	}
	return out
}
