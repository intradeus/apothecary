package jira

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToMarkdown renders a Jira description into markdown. Jira serves two
// completely different things under that field: API v3 returns ADF (Atlassian
// Document Format, a JSON node tree) and API v2 returns a wiki-markup string.
// Both land here so nothing past this package ever sees vendor markup — the
// imported goal is plain markdown an agent can read.
//
// Unknown node types degrade to their children's text rather than
// disappearing: a description that renders imperfectly still beats one that
// silently loses a paragraph.
func ToMarkdown(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// v2: a plain (wiki-markup) string.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var n adfNode
	if err := json.Unmarshal(raw, &n); err != nil {
		return ""
	}
	return strings.TrimSpace(renderBlocks(n.Content, ""))
}

type adfNode struct {
	Type    string         `json:"type"`
	Text    string         `json:"text"`
	Attrs   map[string]any `json:"attrs"`
	Marks   []adfMark      `json:"marks"`
	Content []adfNode      `json:"content"`
}

type adfMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs"`
}

func (n adfNode) attrStr(key string) string {
	if v, ok := n.Attrs[key].(string); ok {
		return v
	}
	return ""
}

func (n adfNode) attrInt(key string) int {
	if v, ok := n.Attrs[key].(float64); ok {
		return int(v)
	}
	return 0
}

// renderBlocks renders a sequence of block nodes, separated by a blank line.
// indent is the prefix every produced line carries (list nesting).
func renderBlocks(nodes []adfNode, indent string) string {
	var blocks []string
	for _, n := range nodes {
		if b := renderBlock(n, indent); strings.TrimSpace(b) != "" {
			blocks = append(blocks, b)
		}
	}
	return strings.Join(blocks, "\n\n")
}

func renderBlock(n adfNode, indent string) string {
	switch n.Type {
	case "paragraph":
		return prefixLines(renderInline(n.Content), indent)
	case "heading":
		level := n.attrInt("level")
		if level < 1 || level > 6 {
			level = 1
		}
		return prefixLines(strings.Repeat("#", level)+" "+renderInline(n.Content), indent)
	case "bulletList", "orderedList":
		return renderList(n, indent)
	case "codeBlock":
		lang := n.attrStr("language")
		return prefixLines("```"+lang+"\n"+plainText(n.Content)+"\n```", indent)
	case "blockquote":
		return prefixLines(prefixLines(renderBlocks(n.Content, ""), "> "), indent)
	case "panel":
		// Panels (info/warning/error) are Jira's callouts; a blockquote keeps
		// the emphasis without inventing syntax.
		kind := n.attrStr("panelType")
		body := renderBlocks(n.Content, "")
		if kind != "" {
			body = strings.ToUpper(kind) + ": " + strings.TrimLeft(body, " ")
		}
		return prefixLines(prefixLines(body, "> "), indent)
	case "rule":
		return indent + "---"
	case "table":
		return prefixLines(renderTable(n), indent)
	case "taskList":
		var lines []string
		for _, item := range n.Content {
			box := "[ ]"
			if item.attrStr("state") == "DONE" {
				box = "[x]"
			}
			lines = append(lines, indent+"- "+box+" "+renderInline(item.Content))
		}
		return strings.Join(lines, "\n")
	case "expand", "nestedExpand":
		title := n.attrStr("title")
		body := renderBlocks(n.Content, indent)
		if title == "" {
			return body
		}
		return prefixLines("**"+title+"**", indent) + "\n\n" + body
	case "mediaSingle", "mediaGroup":
		return prefixLines(renderInline(n.Content), indent)
	case "text", "media", "hardBreak", "mention", "emoji", "inlineCard", "status", "date":
		// An inline node used where a block was expected.
		return prefixLines(renderInline([]adfNode{n}), indent)
	default:
		if len(n.Content) == 0 {
			return prefixLines(n.Text, indent)
		}
		return renderBlocks(n.Content, indent)
	}
}

// renderList renders a bullet/ordered list, recursing for nested lists (which
// ADF nests inside the listItem, so the indent grows two spaces per level).
func renderList(n adfNode, indent string) string {
	var lines []string
	for i, item := range n.Content {
		marker := "- "
		if n.Type == "orderedList" {
			marker = fmt.Sprintf("%d. ", i+1+max(0, n.attrInt("order")-1))
		}
		var first []string
		var rest []string
		for _, child := range item.Content {
			switch child.Type {
			case "bulletList", "orderedList":
				rest = append(rest, renderList(child, indent+strings.Repeat(" ", len(marker))))
			default:
				b := renderBlock(child, "")
				if b == "" {
					continue
				}
				if len(first) == 0 {
					first = append(first, indent+marker+b)
				} else {
					rest = append(rest, prefixLines(b, indent+strings.Repeat(" ", len(marker))))
				}
			}
		}
		if len(first) == 0 {
			first = append(first, indent+marker)
		}
		lines = append(lines, first[0])
		lines = append(lines, rest...)
	}
	return strings.Join(lines, "\n")
}

// renderTable renders an ADF table as a markdown pipe table. The first row is
// treated as the header (Jira's tables usually declare tableHeader cells; when
// they don't, the first row still reads as one).
func renderTable(n adfNode) string {
	var rows [][]string
	for _, row := range n.Content {
		if row.Type != "tableRow" {
			continue
		}
		var cells []string
		for _, cell := range row.Content {
			text := strings.ReplaceAll(renderBlocks(cell.Content, ""), "\n", " ")
			cells = append(cells, strings.TrimSpace(strings.ReplaceAll(text, "|", `\|`)))
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return ""
	}
	width := 0
	for _, r := range rows {
		width = max(width, len(r))
	}
	var b strings.Builder
	for i, r := range rows {
		for len(r) < width {
			r = append(r, "")
		}
		b.WriteString("| " + strings.Join(r, " | ") + " |\n")
		if i == 0 {
			b.WriteString("|" + strings.Repeat(" --- |", width) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderInline renders a run of inline nodes with their marks applied.
func renderInline(nodes []adfNode) string {
	var b strings.Builder
	for _, n := range nodes {
		switch n.Type {
		case "text":
			b.WriteString(applyMarks(n.Text, n.Marks))
		case "hardBreak":
			b.WriteString("\n")
		case "mention":
			name := strings.TrimPrefix(n.attrStr("text"), "@")
			if name == "" {
				name = n.attrStr("id")
			}
			b.WriteString("@" + name)
		case "emoji":
			if t := n.attrStr("text"); t != "" {
				b.WriteString(t)
			} else {
				b.WriteString(n.attrStr("shortName"))
			}
		case "inlineCard", "blockCard":
			b.WriteString(n.attrStr("url"))
		case "status":
			b.WriteString("[" + n.attrStr("text") + "]")
		case "date":
			b.WriteString(n.attrStr("timestamp"))
		case "media":
			// The bytes themselves are imported as artifacts; the description
			// keeps a readable placeholder naming the file.
			name := n.attrStr("alt")
			if name == "" {
				name = n.attrStr("id")
			}
			b.WriteString("[attachment: " + name + "]")
		default:
			if len(n.Content) > 0 {
				b.WriteString(renderInline(n.Content))
			} else {
				b.WriteString(n.Text)
			}
		}
	}
	return b.String()
}

// applyMarks wraps text in the markdown for its ADF marks. Order matters for
// links (the wrapper must sit outside the emphasis), so links are applied last.
func applyMarks(text string, marks []adfMark) string {
	if text == "" {
		return ""
	}
	var href string
	for _, m := range marks {
		switch m.Type {
		case "strong":
			text = "**" + text + "**"
		case "em":
			text = "*" + text + "*"
		case "code":
			text = "`" + text + "`"
		case "strike":
			text = "~~" + text + "~~"
		case "link":
			if v, ok := m.Attrs["href"].(string); ok {
				href = v
			}
		}
	}
	if href != "" {
		text = "[" + text + "](" + href + ")"
	}
	return text
}

// plainText is the un-marked text of a node run (code blocks).
func plainText(nodes []adfNode) string {
	var b strings.Builder
	for _, n := range nodes {
		if n.Type == "hardBreak" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(n.Text)
		if len(n.Content) > 0 {
			b.WriteString(plainText(n.Content))
		}
	}
	return b.String()
}

// prefixLines puts prefix in front of every line of s (empty prefix = no-op).
func prefixLines(s, prefix string) string {
	if prefix == "" || s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
