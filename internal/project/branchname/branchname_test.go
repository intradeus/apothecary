package branchname

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// cases is testdata/cases.json, which cpui's branchname.test.ts reads too.
type cases struct {
	Render []struct {
		Format     string `json:"format"`
		Type       string `json:"type"`
		Key        string `json:"key"`
		Title      string `json:"title"`
		Repeat     int    `json:"repeat"`
		Want       string `json:"want"`
		WantPrefix string `json:"want_prefix"`
		WantSuffix string `json:"want_suffix"`
		WantLen    int    `json:"want_len"`
	} `json:"render"`
	Valid   []string `json:"valid"`
	Invalid []struct {
		Format string `json:"format"`
		Error  string `json:"error"`
	} `json:"invalid"`
}

func load(t *testing.T) cases {
	t.Helper()
	b, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var c cases
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRender(t *testing.T) {
	for _, c := range load(t).Render {
		title := c.Title
		if c.Repeat > 0 {
			title = strings.Repeat(c.Title, c.Repeat)
		}
		got := Render(c.Format, c.Type, c.Key, title)
		if c.WantLen == 0 {
			if got != c.Want {
				t.Errorf("Render(%q, %q, %q, %q) = %q, want %q", c.Format, c.Type, c.Key, title, got, c.Want)
			}
			continue
		}
		if len(got) != c.WantLen || !strings.HasPrefix(got, c.WantPrefix) || !strings.HasSuffix(got, c.WantSuffix) {
			t.Errorf("Render(%q, %q, %q, %d× %q) = %q (%d bytes), want %d bytes, %q…%q",
				c.Format, c.Type, c.Key, c.Repeat, c.Title, got, len(got), c.WantLen, c.WantPrefix, c.WantSuffix)
		}
	}
}

func TestValidate(t *testing.T) {
	c := load(t)
	for _, f := range c.Valid {
		if err := Validate(f); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", f, err)
		}
	}
	codes := map[string]error{
		"unknown": errUnknown, "unbalanced": errUnbalanced, "no_stem": errNoStem,
		"too_long": errTooLong, "ref": errRef, "empty": errEmpty,
	}
	for _, in := range c.Invalid {
		want, ok := codes[in.Error]
		if !ok {
			t.Fatalf("case %q: unknown code %q", in.Format, in.Error)
		}
		if err := Validate(in.Format); !errors.Is(err, want) {
			t.Errorf("Validate(%q) = %v, want %v", in.Format, err, want)
		}
	}
}

// The refusals say how to fix the format: the placeholders a typo meant, the
// rendered name git refuses.
func TestValidateMessages(t *testing.T) {
	for _, c := range []struct{ format, want string }{
		{"{type}/{ticket_title}", "unknown placeholder {type}: use {ticket_type}, {ticket_number} or {ticket_title}"},
		{"{ticket_title", "unbalanced braces: placeholders are {ticket_type}, {ticket_number} or {ticket_title}"},
		{"{ticket_type} {ticket_title}", `"feat add-footnotes" is not a valid branch name: character ' ' is not allowed`},
		{"{ticket_type}/main", "needs {ticket_number} or {ticket_title}, or every ticket would get the same branch"},
		{"{ticket_number}", "renders an empty name for a ticket with no number: add {ticket_title}"},
	} {
		if err := Validate(c.format); err == nil || err.Error() != c.want {
			t.Errorf("Validate(%q) = %v, want %q", c.format, err, c.want)
		}
	}
}
