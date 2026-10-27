package engine

import (
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"apothecary/internal/event"
)

// AnswerField is the one field of a question asked without any: a free-text
// answer, as an open question always was.
const AnswerField = "answer"

// Bounds on a form an agent (or its harness) asks. Generous — a harness's
// own question tool is not ours to reshape — but finite: it is rendered.
const (
	maxFormFields   = 20
	maxFieldOptions = 50
)

// normalizeForm checks an asked form and tidies it in place: ids and labels
// trimmed, a missing label defaulted to the id. What cannot be enforced here
// rather than refused — a pattern Go's regexp does not speak, a format or
// default of the wrong shape — is dropped: the human still reads the
// field's description, and a harness's question is not failed over a hint.
func normalizeForm(f *event.Form) error {
	if len(f.Fields) == 0 {
		return fmt.Errorf("a form needs at least one field")
	}
	if len(f.Fields) > maxFormFields {
		return fmt.Errorf("at most %d fields (got %d)", maxFormFields, len(f.Fields))
	}
	seen := map[string]bool{}
	for i := range f.Fields {
		fd := &f.Fields[i]
		fd.ID, fd.Title, fd.Description = strings.TrimSpace(fd.ID), strings.TrimSpace(fd.Title), strings.TrimSpace(fd.Description)
		if fd.ID == "" || len(fd.ID) > 64 || strings.ContainsAny(fd.ID, "\n\r") {
			return fmt.Errorf("field %d: an id is one line of at most 64 chars", i+1)
		}
		if seen[fd.ID] {
			return fmt.Errorf("field %q is declared twice", fd.ID)
		}
		seen[fd.ID] = true
		if len(fd.Title) > 500 || len(fd.Description) > 2000 {
			return fmt.Errorf("field %q: a title is at most 500 chars, a description 2000", fd.ID)
		}
		switch fd.Type {
		case event.FieldString, event.FieldBoolean, event.FieldNumber, event.FieldInteger:
		case event.FieldArray:
			if len(fd.Options) == 0 {
				return fmt.Errorf("field %q: an array field picks from options, and has none", fd.ID)
			}
		default:
			return fmt.Errorf("field %q: type must be string|number|integer|boolean|array, not %q", fd.ID, fd.Type)
		}
		if len(fd.Options) > 0 && fd.Type != event.FieldString && fd.Type != event.FieldArray {
			return fmt.Errorf("field %q: only a string or an array field has options", fd.ID)
		}
		opts, err := normalizeOptions(fd.Options)
		if err != nil {
			return fmt.Errorf("field %q: %w", fd.ID, err)
		}
		fd.Options = opts
		if fd.Pattern != "" {
			if _, err := regexp.Compile(fd.Pattern); err != nil {
				fd.Pattern = ""
			}
		}
		switch fd.Format {
		case "", "email", "uri", "date", "date-time":
		default:
			fd.Format = ""
		}
		if fd.Default != nil {
			if _, err := checkValue(*fd, fd.Default); err != nil {
				fd.Default = nil
			}
		}
	}
	return nil
}

func normalizeOptions(in []event.Option) ([]event.Option, error) {
	if len(in) > maxFieldOptions {
		return nil, fmt.Errorf("at most %d options (got %d)", maxFieldOptions, len(in))
	}
	var out []event.Option
	seen := map[string]bool{}
	for _, o := range in {
		o.ID, o.Label, o.Detail = strings.TrimSpace(o.ID), strings.TrimSpace(o.Label), strings.TrimSpace(o.Detail)
		if o.Label == "" {
			o.Label = o.ID
		}
		if o.ID == "" || len(o.ID) > 200 || len(o.Label) > 200 || len(o.Detail) > 500 || strings.ContainsAny(o.ID, "\n\r") {
			return nil, fmt.Errorf("options need a non-empty id (one line, at most 200 chars), a label of at most 200 and a detail of at most 500")
		}
		if !seen[o.ID] {
			seen[o.ID] = true
			out = append(out, o)
		}
	}
	return out, nil
}

// validateForm checks a form's answer and tidies its content in place
// (strings trimmed, unanswered optional fields dropped). Accept answers
// every required field within its constraints; decline carries the human's
// reason in the note — the agent is owed one.
func validateForm(form *event.Form, ans *PingAnswer) error {
	switch ans.Answer {
	case event.FormDecline:
		if strings.TrimSpace(ans.Feedback) == "" {
			return fmt.Errorf("declining needs a note saying why — it is what the agent reads")
		}
		ans.Content = nil
		return nil
	case event.FormAccept:
	default:
		return fmt.Errorf("a form is answered accept (with its fields) or decline (with a note)")
	}
	if form == nil {
		return fmt.Errorf("this question has no form to accept")
	}
	out := map[string]any{}
	for _, fd := range form.Fields {
		v, ok := ans.Content[fd.ID]
		if ok {
			nv, err := checkValue(fd, v)
			if err != nil {
				return fmt.Errorf("%s: %w", fd.Label(), err)
			}
			if nv == nil {
				ok = false
			} else {
				out[fd.ID] = nv
			}
		}
		if !ok && fd.Required {
			return fmt.Errorf("%s: an answer is required", fd.Label())
		}
	}
	for id := range ans.Content {
		if !slices.ContainsFunc(form.Fields, func(f event.Field) bool { return f.ID == id }) {
			return fmt.Errorf("%q is not one of the form's fields", id)
		}
	}
	ans.Content = out
	return nil
}

// checkValue checks one field's value against its type and constraints and
// returns it tidied; nil means "not answered" (an empty string or a null).
func checkValue(fd event.Field, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch fd.Type {
	case event.FieldString:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected text")
		}
		if s = strings.TrimSpace(s); s == "" {
			return nil, nil
		}
		if len(fd.Options) > 0 {
			if !slices.Contains(event.OptionIDs(fd.Options), s) {
				return nil, fmt.Errorf("%q is not one of the options", s)
			}
			return s, nil
		}
		n := utf8.RuneCountInString(s)
		if fd.MinLength != nil && n < *fd.MinLength {
			return nil, fmt.Errorf("at least %d characters", *fd.MinLength)
		}
		if fd.MaxLength != nil && n > *fd.MaxLength {
			return nil, fmt.Errorf("at most %d characters", *fd.MaxLength)
		}
		if fd.Pattern != "" {
			if re, err := regexp.Compile(fd.Pattern); err == nil && !re.MatchString(s) {
				return nil, fmt.Errorf("does not match %s", fd.Pattern)
			}
		}
		if err := checkFormat(fd.Format, s); err != nil {
			return nil, err
		}
		return s, nil
	case event.FieldNumber, event.FieldInteger:
		n, ok := v.(float64)
		if !ok {
			return nil, fmt.Errorf("expected a number")
		}
		if fd.Type == event.FieldInteger && n != math.Trunc(n) {
			return nil, fmt.Errorf("expected a whole number")
		}
		if fd.Minimum != nil && n < *fd.Minimum {
			return nil, fmt.Errorf("at least %s", fmtNumber(*fd.Minimum))
		}
		if fd.Maximum != nil && n > *fd.Maximum {
			return nil, fmt.Errorf("at most %s", fmtNumber(*fd.Maximum))
		}
		return n, nil
	case event.FieldBoolean:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("expected yes or no")
		}
		return b, nil
	case event.FieldArray:
		list, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("expected a list of options")
		}
		ids := event.OptionIDs(fd.Options)
		picked := []any{}
		for _, x := range list {
			s, ok := x.(string)
			if !ok || !slices.Contains(ids, s) {
				return nil, fmt.Errorf("%v is not one of the options", x)
			}
			if !slices.Contains(picked, any(s)) {
				picked = append(picked, s)
			}
		}
		if fd.MinItems != nil && len(picked) < *fd.MinItems {
			return nil, fmt.Errorf("pick at least %d", *fd.MinItems)
		}
		if fd.MaxItems != nil && len(picked) > *fd.MaxItems {
			return nil, fmt.Errorf("pick at most %d", *fd.MaxItems)
		}
		return picked, nil
	}
	return nil, fmt.Errorf("unknown field type %q", fd.Type)
}

func checkFormat(format, s string) error {
	var err error
	switch format {
	case "email":
		_, err = mail.ParseAddress(s)
	case "uri":
		var u *url.URL
		if u, err = url.Parse(s); err == nil && u.Scheme == "" {
			err = fmt.Errorf("no scheme")
		}
	case "date":
		_, err = time.Parse(time.DateOnly, s)
	case "date-time":
		_, err = time.Parse(time.RFC3339, s)
	}
	if err != nil {
		return fmt.Errorf("not a valid %s", format)
	}
	return nil
}

func fmtNumber(n float64) string { return strconv.FormatFloat(n, 'f', -1, 64) }

// composeFormText renders a form's answer for the agent: a lone field's
// value as-is, several as "id: value" lines, a decline as the human's
// reason. One renderer for the tool return and a resumed stage's message.
func composeFormText(form *event.Form, answer, feedback string, content map[string]any) string {
	if answer == event.FormDecline {
		return "declined — the human did not answer the question as asked, and said: " + feedback
	}
	var text string
	if form != nil && len(form.Fields) == 1 {
		text = formValueText(form.Fields[0], content)
	} else if form != nil {
		lines := make([]string, len(form.Fields))
		for i, fd := range form.Fields {
			lines[i] = fd.ID + ": " + formValueText(fd, content)
		}
		text = strings.Join(lines, "\n")
	}
	if feedback != "" {
		text += "\n(note: " + feedback + ")"
	}
	return text
}

func formValueText(fd event.Field, content map[string]any) string {
	switch v := content[fd.ID].(type) {
	case nil:
		return "(no answer)"
	case string:
		return v
	case bool:
		if v {
			return "yes"
		}
		return "no"
	case float64:
		return fmtNumber(v)
	case []any:
		if len(v) == 0 {
			return "none selected"
		}
		ids := make([]string, len(v))
		for i, x := range v {
			ids[i] = fmt.Sprint(x)
		}
		return "selected: " + strings.Join(ids, ", ")
	default:
		return fmt.Sprint(v)
	}
}

// formPicks reads an array field's answer as option ids.
func formPicks(content map[string]any, id string) []string {
	var out []string
	if list, ok := content[id].([]any); ok {
		for _, x := range list {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
