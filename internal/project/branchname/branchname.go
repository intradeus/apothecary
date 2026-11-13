// Package branchname names a ticket's base branch from its workshop's branch
// format: free text with three placeholders, {ticket_type} (the change type),
// {ticket_number} (the tracker's issue key) and {ticket_title}. The default,
// {ticket_type}/{ticket_number}-{ticket_title}, gives
// feat/proj-123-add-footnotes. The key in the name is what links the branch
// to the issue on the tracker's side (Jira's Development panel picks up a
// branch whose name carries the key, once the repository host is connected
// to Jira).
//
// The rules are mirrored by the ticket form ($lib/core/branchname in cpui),
// which prefills the name before the ticket is created; testdata/cases.json
// is read by both test suites, so the two renderers cannot drift.
package branchname

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"apothecary/internal/workspace/checkout"
)

// The placeholders a format may use.
const (
	Type   = "ticket_type"
	Number = "ticket_number"
	Title  = "ticket_title"
)

// Default is the format of a workshop that has not set one.
const Default = "{" + Type + "}/{" + Number + "}-{" + Title + "}"

// maxFormat bounds a format's length in characters.
const maxFormat = 100

// maxName bounds a rendered name, like the engine's own branch slugs: not a
// readability cap, only a backstop under a ref's 255-byte file name, with
// room for the tail every run appends.
const maxName = 200

// A Validate failure is one of these, wrapped with the detail.
var (
	errUnknown    = errors.New("unknown placeholder")
	errUnbalanced = errors.New("unbalanced braces")
	errNoStem     = errors.New("needs {" + Number + "} or {" + Title + "}")
	errTooLong    = fmt.Errorf("longer than %d characters", maxFormat)
	errRef        = errors.New("not a valid branch name")
	errEmpty      = errors.New("renders an empty name")
)

const known = "{" + Type + "}, {" + Number + "} or {" + Title + "}"

// token is a literal run of a segment (ph == "") or a placeholder.
type token struct{ lit, ph string }

// parse splits a format into its "/"-separated segments of tokens. A brace
// that does not open a known placeholder is an error; Render, which never
// sees an invalid format once the workshop is saved, keeps it as text.
func parse(format string) ([][]token, error) {
	segs := [][]token{nil}
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			segs[len(segs)-1] = append(segs[len(segs)-1], token{lit: lit.String()})
			lit.Reset()
		}
	}
	var err error
	for i := 0; i < len(format); i++ {
		switch c := format[i]; c {
		case '/':
			flush()
			segs = append(segs, nil)
		case '{':
			end := strings.IndexAny(format[i+1:], "{}")
			if end < 0 || format[i+1+end] == '{' {
				if err == nil {
					err = fmt.Errorf("%w: placeholders are %s", errUnbalanced, known)
				}
				lit.WriteByte(c)
				continue
			}
			name := format[i+1 : i+1+end]
			if name != Type && name != Number && name != Title {
				if err == nil {
					err = fmt.Errorf("%w {%s}: use %s", errUnknown, name, known)
				}
				lit.WriteString(format[i : i+2+end])
			} else {
				flush()
				segs[len(segs)-1] = append(segs[len(segs)-1], token{ph: name})
			}
			i += 1 + end
		case '}':
			if err == nil {
				err = fmt.Errorf("%w: placeholders are %s", errUnbalanced, known)
			}
			lit.WriteByte(c)
		default:
			lit.WriteByte(c)
		}
	}
	flush()
	return segs, err
}

// Validate refuses a format that would not name every ticket a distinct,
// valid branch. "" is the default, so valid.
func Validate(format string) error {
	if format == "" {
		format = Default
	}
	if utf8.RuneCountInString(format) > maxFormat {
		return errTooLong
	}
	segs, err := parse(format)
	if err != nil {
		return err
	}
	if count(segs, Number)+count(segs, Title) == 0 {
		return fmt.Errorf("%w, or every ticket would get the same branch", errNoStem)
	}
	for _, key := range []string{"PROJ-123", ""} {
		name := Render(format, "feat", key, "Add footnotes")
		if name == "" {
			return fmt.Errorf("%w for a ticket with no number: add {%s}", errEmpty, Title)
		}
		if err := checkout.CheckBranch(name); err != nil {
			return fmt.Errorf("%q is %w: %v", name, errRef, err)
		}
	}
	return nil
}

// count is how many times a format uses a placeholder.
func count(segs [][]token, ph string) int {
	n := 0
	for _, seg := range segs {
		for _, t := range seg {
			if t.ph == ph {
				n++
			}
		}
	}
	return n
}

// Render names a ticket's base branch by format ("" is Default). Each value
// is slugged; a key carrying its repository ("lu/greeter#5", GitHub and
// GitLab issues) keeps only its number, and a ticket with neither number nor
// title is titled "ticket". A placeholder that renders empty takes one
// adjacent separator (-, _ or .) with it — the next one when it starts its
// "/" segment, else the one before — and a segment it leaves empty is
// dropped: feat/add-footnotes for a hand-written ticket. The title is cut so
// the name stays within maxName bytes.
func Render(format, changeType, key, title string) string {
	if format == "" {
		format = Default
	}
	segs, _ := parse(format)
	if i := strings.LastIndex(key, "#"); i >= 0 {
		key = key[i+1:]
	}
	vals := map[string]string{Type: slug(changeType), Number: slug(key), Title: slug(title)}
	if vals[Number] == "" && vals[Title] == "" {
		vals[Title] = "ticket"
	}
	name := render(segs, vals)
	if over, n := len(name)-maxName, count(segs, Title); over > 0 && n > 0 {
		keep := max(len(vals[Title])-(over+n-1)/n, 0)
		vals[Title] = strings.TrimRight(vals[Title][:keep], "-")
		name = render(segs, vals)
	}
	return name
}

func render(segs [][]token, vals map[string]string) string {
	out := make([]string, 0, len(segs))
	for _, seg := range segs {
		parts := make([]string, len(seg))
		for i, t := range seg {
			if t.ph == "" {
				parts[i] = t.lit
			} else {
				parts[i] = vals[t.ph]
			}
		}
		emptied := false
		for i, t := range seg {
			if t.ph == "" || parts[i] != "" {
				continue
			}
			emptied = true
			if strings.Join(parts[:i], "") == "" {
				if i+1 < len(seg) && seg[i+1].ph == "" && parts[i+1] != "" && isSep(parts[i+1][0]) {
					parts[i+1] = parts[i+1][1:]
				}
			} else if i > 0 && seg[i-1].ph == "" && parts[i-1] != "" && isSep(parts[i-1][len(parts[i-1])-1]) {
				parts[i-1] = parts[i-1][:len(parts[i-1])-1]
			}
		}
		s := strings.Join(parts, "")
		if s == "" && emptied {
			continue
		}
		out = append(out, s)
	}
	return strings.Join(out, "/")
}

func isSep(c byte) bool { return c == '-' || c == '_' || c == '.' }

// slug lowercases s to [a-z0-9] runs joined by single dashes.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	return b.String()
}
