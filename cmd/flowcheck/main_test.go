package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"apothecary/internal/flow"
	"apothecary/internal/flow/builtin"
)

// TestMain lets a test run the real main in a child process, where its exits
// are what an operator's shell sees.
func TestMain(m *testing.M) {
	if raw := os.Getenv("FLOWCHECK_TEST_ARGV"); raw != "" {
		var argv []string
		if err := json.Unmarshal([]byte(raw), &argv); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		os.Args = append([]string{"flowcheck"}, argv...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func child(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	argv, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0])
	// GOCOVERDIR: a binary built with -cover warns on stderr without it.
	cmd.Env = []string{"FLOWCHECK_TEST_ARGV=" + string(argv), "GOCOVERDIR=" + t.TempDir()}
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errb.String(), code
}

const head = "name: t\nversion: 5\nparameters:\n  harness: fake\n  model: a/b\n  done_status: { type: string, description: where a merged ticket goes }\n"

// twoStages is a flow of a command that retries itself, then a tracker move
// whose status waits for the workshop.
const twoStages = head + `main:
  - id: test
    type: command
    run: "true"
    on_fail: { to: test, max: 2, then: debug }
  - id: move
    type: action
    action: ticket_transition
    with: { to: $done_status }
`

// plain is the head of a flow with no parameter of its own.
const plain = "name: t\nversion: 5\nparameters: { harness: fake, model: a/b }\n"

// warns parses, and warns: the wait needs a merge request nothing opens.
const warns = plain + `main:
  - { id: test, type: command, run: "true" }
  - { id: merged, type: wait, for: mr_merged }
`

// flowDir is a directory holding one flow as docs writes it, and edited as
// what the editor wrote for it ("" for nothing).
func flowDir(t *testing.T, original, edited string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "f.yaml"), original)
	write(t, filepath.Join(dir, "f.doc.json"), "{}")
	if edited != "" {
		write(t, filepath.Join(dir, "f.editor.yaml"), edited)
	}
	return dir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func verified(t *testing.T, dir string) (out string, err error) {
	t.Helper()
	var b strings.Builder
	err = run([]string{"verify", dir}, &b)
	return b.String(), err
}

func TestTheFixturesAreWhatTheySay(t *testing.T) {
	for name, c := range map[string]struct {
		yaml     string
		warnings int
	}{"twoStages": {twoStages, 0}, "warns": {warns, 1}} {
		f, err := flow.Parse(name, []byte(c.yaml))
		if err != nil || len(f.Warnings) != c.warnings {
			t.Errorf("%s: %v, warnings %v, want %d", name, err, f.Warnings, c.warnings)
		}
	}
}

// docs writes every built-in flow and each file named: the flow as it is, and
// the document the engine serves the editor — where a parameter with no
// default says `"default": ""`.
func TestDocsWritesTheBuiltinsAndTheFilesNamed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "flows")
	mine := filepath.Join(t.TempDir(), "mine.yaml")
	write(t, mine, twoStages)
	var out strings.Builder
	if err := run([]string{"docs", dir, mine}, &out); err != nil {
		t.Fatal(err)
	}
	labels := []string{fileLabel(mine)}
	for _, name := range builtin.FlowNames() {
		labels = append(labels, "builtin-"+name)
	}
	if len(labels) < 2 {
		t.Fatal("no built-in flow to write")
	}
	for _, label := range labels {
		for _, suffix := range []string{yamlSuffix, docSuffix} {
			if st, err := os.Stat(filepath.Join(dir, label+suffix)); err != nil || st.Size() == 0 {
				t.Errorf("%s%s: %v", label, suffix, err)
			}
		}
	}
	if got, _ := os.ReadFile(filepath.Join(dir, fileLabel(mine)+yamlSuffix)); string(got) != twoStages {
		t.Errorf("the flow was not written as it is: %q", got)
	}
	var doc struct {
		Parameters map[string]struct {
			Default any `json:"default"`
		} `json:"parameters"`
	}
	data, _ := os.ReadFile(filepath.Join(dir, fileLabel(mine)+docSuffix))
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if p, ok := doc.Parameters["done_status"]; !ok || p.Default != "" {
		t.Errorf("done_status in the document = %+v, %v", p, ok)
	}
	if want := fmt.Sprintf("%d flows parse and bind with no warning", len(labels)); !strings.HasPrefix(out.String(), want) {
		t.Errorf("output = %q, want %q…", out.String(), want)
	}
}

func TestDocsRefuses(t *testing.T) {
	tmp := t.TempDir()
	file := func(name, content string) string {
		p := filepath.Join(tmp, name)
		write(t, p, content)
		return p
	}
	good := file("good.yaml", twoStages)
	blocked := filepath.Join(tmp, "blocked")
	for _, suffix := range []string{yamlSuffix, docSuffix} {
		if err := os.MkdirAll(filepath.Join(blocked, fileLabel(good)+suffix), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name  string
		dir   string
		files []string
		want  string
	}{
		{"a file that is not there", tmp, []string{filepath.Join(tmp, "nope.yaml")}, "no such file"},
		{"a flow that does not parse", tmp, []string{file("bad.yaml", "name: bad\nversion: 5\nmain: nope\n")}, "bad.yaml:"},
		{"a flow that warns", tmp, []string{file("warns.yaml", warns)}, "binds with a warning"},
		{"one flow named twice", tmp, []string{good, good}, "two flows are named"},
		{"a directory it cannot make", good, nil, "not a directory"},
		{"a file it cannot write", blocked, []string{good}, "is a directory"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out strings.Builder
			err := run(append([]string{"docs", c.dir}, c.files...), &out)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// A flow written out differently — its stages in another order, its sugar
// spelled out, its lines elsewhere — is the same flow.
func TestVerifyPassesTheSameFlowWrittenDifferently(t *testing.T) {
	rewritten := `# the editor writes no comment, and every route
name: t
version: 5
parameters:
  model: a/b
  harness: fake
  done_status:
    type: string
    description: where a merged ticket goes
main:
  - id: test
    type: command
    run: "true"
    on_success: move
    on_fail:
      to: test
      max: 2
      then: debug
  - id: move
    type: action
    action: ticket_transition
    with:
      to: $done_status
    on_success: done
`
	out, err := verified(t, flowDir(t, twoStages, rewritten))
	if err != nil || !strings.Contains(out, "ok    f\n") || !strings.Contains(out, "1 flows come back") {
		t.Errorf("verify = %q, %v", out, err)
	}
}

func TestVerifyFails(t *testing.T) {
	edit := func(old, new string) string {
		if !strings.Contains(twoStages, old) {
			t.Fatalf("the fixture has no %q", old)
		}
		return strings.Replace(twoStages, old, new, 1)
	}
	cases := []struct {
		name     string
		original string
		edited   string
		want     []string
	}{
		{"when the editor wrote nothing", twoStages, "", []string{"the editor wrote nothing"}},
		// What this check was written for: the document says `default: ""`
		// for a parameter with none, and the editor wrote it back.
		{"when the engine refuses what the editor wrote", twoStages,
			edit("done_status: { type: string,", `done_status: { type: string, default: "",`),
			[]string{"the engine refuses what the editor wrote: f.editor.yaml:", `a value is required (parameter "done_status")`}},
		{"when what the editor wrote warns", twoStages, warns,
			[]string{"what the editor wrote binds with a warning", "the editor changed flow.Stages.merged: nothing → "}},
		{"when a value changed", twoStages, edit("max: 2", "max: 3"),
			[]string{"the editor changed flow.Stages.test.Edges.on_fail.Max: 2 → 3"}},
		{"when a stage is gone", twoStages, edit(`    on_fail: { to: test, max: 2, then: debug }
  - id: move
    type: action
    action: ticket_transition
    with: { to: $done_status }
`, ""), []string{"the editor changed flow.Stages.move: {", "→ nothing"}},
		{"when a chain starts elsewhere", plain + `main:
  - { id: test, type: command, run: "true", on_success: done, on_fail: { to: fix, max: 1, then: debug } }
  - { id: fix, type: command, run: "true", on_success: test }
`, plain + `main:
  - { id: fix, type: command, run: "true", on_success: test }
  - { id: test, type: command, run: "true", on_success: done, on_fail: { to: fix, max: 1, then: debug } }
`, []string{`the editor changed flow.Entries.main: "test" → "fix"`}},
		{"when the flow itself is gone", "", twoStages, []string{"f.yaml:"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := verified(t, flowDir(t, c.original, c.edited))
			if err == nil || !strings.Contains(err.Error(), "1 of 1 flows do not come back") {
				t.Errorf("err = %v", err)
			}
			if !strings.Contains(out, "FAIL  f\n") {
				t.Errorf("output = %q", out)
			}
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("output = %q, want %q", out, w)
				}
			}
		})
	}
}

func TestVerifyNeedsItsFiles(t *testing.T) {
	if _, err := verified(t, t.TempDir()); err == nil || !strings.Contains(err.Error(), "flowcheck docs writes them") {
		t.Errorf("an empty directory: %v", err)
	}
	if _, err := verified(t, "[bad"); err == nil {
		t.Error("a directory that is no pattern: no error")
	}
	dir := flowDir(t, twoStages, twoStages)
	if err := os.Remove(filepath.Join(dir, "f.yaml")); err != nil {
		t.Fatal(err)
	}
	if out, err := verified(t, dir); err == nil || !strings.Contains(out, "no such file") {
		t.Errorf("the flow missing: %q, %v", out, err)
	}
}

// A flow that lost everything says so in maxDiffs lines, not a page.
func TestVerifyCapsWhatOneFlowReports(t *testing.T) {
	var many strings.Builder
	many.WriteString(plain + "main:\n")
	for i := range maxDiffs + 3 {
		fmt.Fprintf(&many, "  - { id: s%d, type: command, run: \"true\" }\n", i)
	}
	one := plain + "main:\n  - { id: only, type: command, run: \"true\" }\n"
	out, err := verified(t, flowDir(t, many.String(), one))
	if err == nil || strings.Count(out, "the editor changed") != maxDiffs+1 || !strings.Contains(out, "… and ") {
		t.Errorf("verify = %q, %v", out, err)
	}
}

func TestDiff(t *testing.T) {
	a := map[string]any{"same": 1.0, "list": []any{"x", "y"}, "gone": true, "kind": map[string]any{"k": "v"}}
	b := map[string]any{"same": 1.0, "list": []any{"x", "z"}, "new": "n", "kind": "text"}
	got := diff("f", a, b, nil)
	want := []string{`f.gone: true → nothing`, `f.kind: {"k":"v"} → "text"`, `f.list[1]: "y" → "z"`, `f.new: nothing → "n"`}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("diff = %q, want %q", got, want)
	}
	if got := diff("f", []any{1.0}, []any{1.0, 2.0}, nil); len(got) != 1 || got[0] != "f: [1] → [1,2]" {
		t.Errorf("lists of two lengths: %q", got)
	}
	if long := show(strings.Repeat("x", 200)); len([]rune(long)) != 78 || !strings.HasSuffix(long, "…") {
		t.Errorf("a long value shows as %d runes: %q", len([]rune(long)), long)
	}
}

// What is compared says nothing of where the flow is written, and names the
// stage each chain starts at — an outcome with no chain has none.
func TestMeaningOfTheBuiltins(t *testing.T) {
	for _, name := range builtin.FlowNames() {
		f := builtin.Flow(name)
		m := meaning(f)
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		for _, gone := range []string{`"Line"`, `"Hash"`} {
			if strings.Contains(string(data), gone) {
				t.Errorf("%s: the meaning still holds %s", name, gone)
			}
		}
		chains := 0
		for _, ph := range f.Phases {
			if ph.First <= ph.Last {
				chains++
			}
		}
		if entries := m["Entries"].(map[string]any); len(entries) != chains || entries[flow.MainPhase] != f.Stages[0].ID {
			t.Errorf("%s: entries = %v, want %d, main at %s", name, entries, chains, f.Stages[0].ID)
		}
		if len(m["Stages"].(map[string]any)) != len(f.Stages) || len(m["Refs"].([]any)) != len(f.Refs) {
			t.Errorf("%s: %d stages and %d refs kept, of %d and %d", name, len(m["Stages"].(map[string]any)), len(m["Refs"].([]any)), len(f.Stages), len(f.Refs))
		}
	}
	// reviewer's outcomes end the run with no chain of their own.
	if entries := meaning(builtin.Flow("reviewer"))["Entries"].(map[string]any); entries["merged"] != nil {
		t.Errorf("an outcome with no chain has an entry: %v", entries)
	}
}

func TestKeyedAndDropLines(t *testing.T) {
	list := []any{map[string]any{"ID": "a", "Line": 3.0, "In": []any{map[string]any{"Line": 4.0, "Name": "n"}}}, "stray"}
	got := keyed(list, "ID")
	dropLines(got)
	want := map[string]any{"a": map[string]any{"ID": "a", "In": []any{map[string]any{"Name": "n"}}}}
	if len(diff("v", want, any(got), nil)) != 0 {
		t.Errorf("keyed, without lines = %v", got)
	}
	if len(keyed(nil, "ID")) != 0 {
		t.Error("nothing keyed is not empty")
	}
}

func TestFileLabel(t *testing.T) {
	for path, want := range map[string]string{
		"examples/flows/v5.yaml":   "examples-flows-v5",
		"./examples/flows/v5.yaml": "examples-flows-v5",
		"v5.yaml":                  "v5",
	} {
		if got := fileLabel(path); got != want {
			t.Errorf("fileLabel(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"docs"}, {"verify"}, {"verify", "a", "b"}, {"nope", "dir"}} {
		var out strings.Builder
		if err := run(args, &out); err == nil || err.Error() != usage {
			t.Errorf("run(%q) = %v", args, err)
		}
	}
}

// The real main: what a shell sees.
func TestMainExits(t *testing.T) {
	dir := flowDir(t, twoStages, twoStages)
	// Here too, for the path that returns.
	oldArgs, oldOut := os.Args, os.Stdout
	defer func() { os.Args, os.Stdout = oldArgs, oldOut }()
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	os.Args, os.Stdout = []string{"flowcheck", "verify", dir}, null
	main()
	os.Args, os.Stdout = oldArgs, oldOut
	if out, stderr, code := child(t, "verify", dir); code != 0 || !strings.Contains(out, "ok    f") || stderr != "" {
		t.Errorf("verify: exit %d, %q, %q", code, out, stderr)
	}
	if _, stderr, code := child(t); code != 1 || stderr != "flowcheck: "+usage+"\n" {
		t.Errorf("no arguments: exit %d, %q", code, stderr)
	}
}
