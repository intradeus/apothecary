// Package notes holds docs/notes to the shape that keeps it free of merge
// conflicts: one file per decision, named by its date and a slug, and no
// shared file (the old single IMPLEMENTATION_NOTES.md, an index) that every
// branch would edit (AGENTS.md, "Implementation notes").
package notes

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const root = "../../.."

var noteName = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-[a-z0-9]+(?:-[a-z0-9]+)*\.md$`)

// problems lists every way dir strays from one well-named note per file.
func problems(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{err.Error()}
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			out = append(out, name+": a directory; notes are flat files")
			continue
		}
		m := noteName.FindStringSubmatch(name)
		if m == nil {
			out = append(out, name+": not <YYYY-MM-DD>-<slug>.md (lowercase letters, digits, hyphens)")
			continue
		}
		if _, err := time.Parse("2006-01-02", m[1]); err != nil {
			out = append(out, name+": "+m[1]+" is no date")
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			out = append(out, name+": "+err.Error())
			continue
		}
		first, _, _ := strings.Cut(string(b), "\n")
		if !strings.HasPrefix(first, "# ") || strings.TrimSpace(first[2:]) == "" {
			out = append(out, name+": the first line is not `# <title>`")
		}
	}
	return out
}

func TestNotesAreOneFilePerDecision(t *testing.T) {
	for _, p := range problems(filepath.Join(root, "docs", "notes")) {
		t.Error(p)
	}
}

// The single file every branch appended to is what made the conflicts.
func TestNoSharedNotesFile(t *testing.T) {
	if _, err := os.Stat(filepath.Join(root, "docs", "IMPLEMENTATION_NOTES.md")); err == nil {
		t.Error("docs/IMPLEMENTATION_NOTES.md is back: write a new file in docs/notes instead (AGENTS.md, \"Implementation notes\")")
	}
}

func TestProblemsNamesEachStray(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("2026-11-13-a-good-note.md", "# A good note\n\nbody\n")
	write("README.md", "# Index\n")
	write("2026-13-01-bad-month.md", "# Bad month\n")
	write("2026-11-13-Upper.md", "# Upper\n")
	write("2026-11-13-no-title.md", "body first\n")
	write("2026-11-13-empty-title.md", "# \n")
	if err := os.Mkdir(filepath.Join(dir, "2026-11-13-sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(problems(dir), "\n")
	for _, want := range []string{
		"README.md: not",
		"2026-13-01-bad-month.md: 2026-13-01 is no date",
		"2026-11-13-Upper.md: not",
		"2026-11-13-no-title.md: the first line",
		"2026-11-13-empty-title.md: the first line",
		"2026-11-13-sub: a directory",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("problems lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "a-good-note") {
		t.Errorf("a well-formed note was flagged:\n%s", got)
	}
	if p := problems(filepath.Join(dir, "missing")); len(p) != 1 {
		t.Errorf("a missing directory = %v, want one problem", p)
	}
}
