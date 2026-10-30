package skills

import (
	"apothecary/internal/ports/envport"
	"strings"
	"testing"
	"unicode/utf8"
)

func md(name, desc string) []byte {
	return []byte("---\nname: " + name + "\ndescription: " + desc + "\n---\n\n# " + name + "\n")
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"a", "a-b", "pdf-processing", "x1", strings.Repeat("a", 64)} {
		if err := ValidName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "a--b", "PDF", "a_b", "a b", "é", strings.Repeat("a", 65)} {
		if err := ValidName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCleanPath(t *testing.T) {
	for _, ok := range []string{"SKILL.md", "scripts/run.sh", "references/a/b.md", "weird name.txt"} {
		if _, err := CleanPath(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "/abs", "../x", "a/../b", "a//b", "a/./b", ".", "..", "a\\b", "a\x01b", "a\x00b"} {
		if _, err := CleanPath(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseFrontmatter(t *testing.T) {
	fm, err := ParseFrontmatter([]byte("---\nname: pdf\ndescription: >\n  Extract PDF text.\n  Use when asked.\nlicense: MIT\nargument-hint: '[file]'\nmetadata:\n  version: 1.0\nallowed-tools:\n  - Bash\n---\nbody"))
	if err != nil {
		t.Fatal(err)
	}
	if fm.Name != "pdf" || fm.Description != "Extract PDF text. Use when asked." || fm.License != "MIT" {
		t.Errorf("fm = %+v", fm)
	}
	if _, err := ParseFrontmatter([]byte("---\r\nname: crlf\r\ndescription: ok\r\n---\r\n")); err != nil {
		t.Errorf("crlf: %v", err)
	}
	cases := map[string]string{
		"no opening":        "name: x\ndescription: y\n",
		"unterminated":      "---\nname: x\ndescription: y\n",
		"missing name":      "---\ndescription: y\n---\n",
		"bad name":          "---\nname: Bad Name\ndescription: y\n---\n",
		"empty description": "---\nname: x\ndescription: ''\n---\n",
		"invalid yaml":      "---\nname: [\ndescription: y\n---\n",
	}
	for name, in := range cases {
		if _, err := ParseFrontmatter([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Over-long descriptions are published in the wild (anthropics/skills):
	// accepted, and only the stored copy is cut.
	long, err := ParseFrontmatter([]byte("---\nname: x\ndescription: " + strings.Repeat("d", 1500) + "\ncompatibility: " + strings.Repeat("c", 600) + "\n---\n"))
	if err != nil || utf8.RuneCountInString(long.Description) != MaxDescriptionLen || utf8.RuneCountInString(long.Compatibility) != MaxCompatibilityLen {
		t.Errorf("long fields: %v, desc=%d compat=%d", err, utf8.RuneCountInString(long.Description), utf8.RuneCountInString(long.Compatibility))
	}
}

func TestValidateNormalises(t *testing.T) {
	files := []envport.TreeFile{
		{Path: "scripts/run.sh", Data: []byte("echo")},
		{Path: "SKILL.md", Data: md("deploy", "Deploys things.")},
		{Path: "references/x.md", Mode: 0o700, Data: []byte("x")},
	}
	fm, norm, err := Validate("deploy", files)
	if err != nil {
		t.Fatal(err)
	}
	if fm.Name != "deploy" || fm.Description != "Deploys things." {
		t.Errorf("fm = %+v", fm)
	}
	var got []string
	for _, f := range norm {
		got = append(got, f.Path+":"+f.Mode.String())
	}
	want := []string{"SKILL.md:-rw-r--r--", "references/x.md:-rwxr-xr-x", "scripts/run.sh:-rwxr-xr-x"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("normalised = %v, want %v", got, want)
	}
}

func TestValidateRejects(t *testing.T) {
	big := make([]byte, MaxFileBytes+1)
	cases := map[string][]envport.TreeFile{
		"empty":          nil,
		"no manifest":    {{Path: "README.md", Data: []byte("x")}},
		"name mismatch":  {{Path: "SKILL.md", Data: md("other", "d")}},
		"bad path":       {{Path: "SKILL.md", Data: md("s", "d")}, {Path: "../x", Data: []byte("x")}},
		"duplicate path": {{Path: "SKILL.md", Data: md("s", "d")}, {Path: "SKILL.md", Data: md("s", "d")}},
		"file too large": {{Path: "SKILL.md", Data: md("s", "d")}, {Path: "big", Data: big}},
	}
	for name, files := range cases {
		if _, _, err := Validate("s", files); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	many := []envport.TreeFile{{Path: "SKILL.md", Data: md("s", "d")}}
	for i := 0; i < MaxFiles; i++ {
		many = append(many, envport.TreeFile{Path: "f" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + strings.Repeat("y", i/26), Data: []byte("x")})
	}
	if _, _, err := Validate("s", many); err == nil {
		t.Error("too many files accepted")
	}
	if _, _, err := Validate("Bad", []envport.TreeFile{{Path: "SKILL.md", Data: md("Bad", "d")}}); err == nil {
		t.Error("bad dir name accepted")
	}
}

func TestHashes(t *testing.T) {
	a := []envport.TreeFile{{Path: "SKILL.md", Data: []byte("1")}, {Path: "scripts/x", Data: []byte("2")}}
	b := []envport.TreeFile{{Path: "scripts/x", Data: []byte("2")}, {Path: "SKILL.md", Data: []byte("1")}}
	if Hash(Manifest(a)) != Hash(Manifest(b)) {
		t.Error("hash depends on file order")
	}
	c := []envport.TreeFile{{Path: "SKILL.md", Data: []byte("1")}, {Path: "scripts/x", Mode: 0o644, Data: []byte("2")}}
	if Hash(Manifest(a)) != Hash(Manifest(c)) {
		t.Error("scripts/ files should hash as executable regardless of the given mode")
	}
	d := []envport.TreeFile{{Path: "SKILL.md", Data: []byte("1")}, {Path: "other", Data: []byte("2")}}
	e := []envport.TreeFile{{Path: "SKILL.md", Data: []byte("1")}, {Path: "other", Mode: 0o755, Data: []byte("2")}}
	if Hash(Manifest(d)) == Hash(Manifest(e)) {
		t.Error("executable bit should change the hash")
	}
	if Hash(Manifest(a)) == Hash(Manifest(d)) {
		t.Error("different paths should change the hash")
	}
	m1 := []Member{{"a", "h1"}, {"b", "h2"}}
	m2 := []Member{{"b", "h2"}, {"a", "h1"}}
	if SetHash(m1) != SetHash(m2) {
		t.Error("set hash depends on member order")
	}
	if SetHash(m1) == SetHash([]Member{{"a", "h1"}, {"b", "h3"}}) {
		t.Error("member content change should change the set hash")
	}
	if SetHash(m1) == SetHash([]Member{{"a", "h1"}}) {
		t.Error("membership change should change the set hash")
	}
}
