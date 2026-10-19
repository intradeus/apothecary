package jira

import (
	"encoding/json"
	"fmt"
	"regexp"
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

// --- markdown → ADF ---------------------------------------------------------

// FromMarkdown renders markdown as an ADF document — what Jira Cloud's API v3
// wants for a comment body or a paragraph ("textarea") custom field. The
// inverse of ToMarkdown for the subset the engine writes: paragraphs (a
// single newline is a hard break), ATX headings, bullet and ordered lists
// (one level), fenced code, blockquotes, rules; inline bold, emphasis, code,
// strikethrough and links. Anything else is kept as text rather than lost.
func FromMarkdown(md string) map[string]any {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var blocks []map[string]any
	for i := 0; i < len(lines); {
		trim := strings.TrimSpace(lines[i])
		switch {
		case trim == "":
			i++
		case strings.HasPrefix(trim, "```"):
			lang := strings.TrimSpace(strings.TrimPrefix(trim, "```"))
			var code []string
			i++
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				code = append(code, lines[i])
				i++
			}
			i++ // the closing fence (or the end)
			blocks = append(blocks, adfCodeBlock(lang, strings.Join(code, "\n")))
		case headingRe.MatchString(trim):
			m := headingRe.FindStringSubmatch(trim)
			blocks = append(blocks, map[string]any{"type": "heading",
				"attrs": map[string]any{"level": len(m[1])}, "content": inlineNodes(m[2])})
			i++
		case trim == "---" || trim == "***" || trim == "___":
			blocks = append(blocks, map[string]any{"type": "rule"})
			i++
		case bulletRe.MatchString(trim), orderedRe.MatchString(trim):
			re, kind := bulletRe, "bulletList"
			if orderedRe.MatchString(trim) {
				re, kind = orderedRe, "orderedList"
			}
			var items []map[string]any
			for i < len(lines) && re.MatchString(strings.TrimSpace(lines[i])) {
				text := re.FindStringSubmatch(strings.TrimSpace(lines[i]))[1]
				items = append(items, map[string]any{"type": "listItem", "content": []map[string]any{adfParagraph(text)}})
				i++
			}
			blocks = append(blocks, map[string]any{"type": kind, "content": items})
		case strings.HasPrefix(trim, ">"):
			var quoted []string
			for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">") {
				quoted = append(quoted, strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(lines[i]), ">"), " "))
				i++
			}
			blocks = append(blocks, map[string]any{"type": "blockquote",
				"content": []map[string]any{adfParagraph(strings.Join(quoted, "\n"))}})
		default:
			var para []string
			for i < len(lines) && isParagraphLine(lines[i]) {
				para = append(para, strings.TrimSpace(lines[i]))
				i++
			}
			blocks = append(blocks, adfParagraph(strings.Join(para, "\n")))
		}
	}
	if blocks == nil {
		blocks = []map[string]any{}
	}
	return map[string]any{"type": "doc", "version": 1, "content": blocks}
}

var (
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	bulletRe  = regexp.MustCompile(`^[-*+]\s+(.*)$`)
	orderedRe = regexp.MustCompile(`^\d+[.)]\s+(.*)$`)
)

// isParagraphLine: a non-blank line that does not open another block.
func isParagraphLine(line string) bool {
	trim := strings.TrimSpace(line)
	if trim == "" || strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, ">") ||
		trim == "---" || trim == "***" || trim == "___" {
		return false
	}
	return !headingRe.MatchString(trim) && !bulletRe.MatchString(trim) && !orderedRe.MatchString(trim)
}

func adfParagraph(text string) map[string]any {
	return map[string]any{"type": "paragraph", "content": inlineNodes(text)}
}

func adfCodeBlock(lang, code string) map[string]any {
	n := map[string]any{"type": "codeBlock", "content": []map[string]any{{"type": "text", "text": code}}}
	if lang != "" {
		n["attrs"] = map[string]any{"language": lang}
	}
	return n
}

// inlineNodes tokenizes a run of inline markdown into ADF text nodes with
// marks: **strong**, *em* / _em_, `code`, ~~strike~~, [text](href); a
// newline is a hardBreak. Marks do not nest (a link's text is plain) — the
// engine's own comments never need more, and an unmatched opener stays
// literal rather than swallowing the rest of the line.
func inlineNodes(s string) []map[string]any {
	var out []map[string]any
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			out = append(out, map[string]any{"type": "text", "text": text.String()})
			text.Reset()
		}
	}
	marked := func(body string, mark map[string]any) {
		flush()
		out = append(out, map[string]any{"type": "text", "text": body, "marks": []map[string]any{mark}})
	}
	// span finds the closing delimiter for a non-empty body starting at i+len(open).
	span := func(i int, open, close string) (body string, next int, ok bool) {
		start := i + len(open)
		end := strings.Index(s[start:], close)
		if end <= 0 {
			return "", 0, false
		}
		return s[start : start+end], start + end + len(close), true
	}
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\n':
			flush()
			out = append(out, map[string]any{"type": "hardBreak"})
			i++
		case strings.HasPrefix(s[i:], "**"):
			if body, next, ok := span(i, "**", "**"); ok {
				marked(body, map[string]any{"type": "strong"})
				i = next
				continue
			}
			text.WriteString("**")
			i += 2
		case strings.HasPrefix(s[i:], "~~"):
			if body, next, ok := span(i, "~~", "~~"); ok {
				marked(body, map[string]any{"type": "strike"})
				i = next
				continue
			}
			text.WriteString("~~")
			i += 2
		case s[i] == '`':
			if body, next, ok := span(i, "`", "`"); ok {
				marked(body, map[string]any{"type": "code"})
				i = next
				continue
			}
			text.WriteByte('`')
			i++
		case s[i] == '[':
			if label, next, ok := span(i, "[", "]("); ok && next <= len(s) {
				if end := strings.IndexByte(s[next:], ')'); end >= 0 {
					href := s[next : next+end]
					marked(label, map[string]any{"type": "link", "attrs": map[string]any{"href": href}})
					i = next + end + 1
					continue
				}
			}
			text.WriteByte('[')
			i++
		case s[i] == '*' || s[i] == '_':
			d := string(s[i])
			if body, next, ok := span(i, d, d); ok && !strings.HasPrefix(body, " ") && !strings.HasSuffix(body, " ") {
				marked(body, map[string]any{"type": "em"})
				i = next
				continue
			}
			text.WriteString(d)
			i++
		default:
			text.WriteByte(s[i])
			i++
		}
	}
	flush()
	if out == nil {
		out = []map[string]any{}
	}
	return out
}
