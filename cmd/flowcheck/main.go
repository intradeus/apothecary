// Command flowcheck holds the flows this repository ships to what the flow
// editor does with them.
//
// A flow reaches the editor as the engine's document (the `doc` of
// /api/flows/validate), the editor writes it out again (the CPUI's
// docToYaml), and the engine parses that: two languages, so no test of one
// side sees the whole trip. `task flows:check` runs it, on every built-in
// flow and on the files it names:
//
//	flowcheck docs build/flows examples/flows/v5.yaml
//	node cpui/scripts/flows-through-editor.mjs build/flows
//	flowcheck verify build/flows
//
// docs parses each flow, refuses a warning, and writes <name>.yaml and
// <name>.doc.json; the script writes <name>.editor.yaml; verify parses that
// one and holds it to the same meaning as the flow it came from. The files
// stay in the directory, to read what the editor wrote after a failure.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"apothecary/internal/flow"
	"apothecary/internal/flow/builtin"
)

const (
	yamlSuffix   = ".yaml"
	docSuffix    = ".doc.json"
	editorSuffix = ".editor.yaml"
	// maxDiffs caps what one flow reports: a prompt lost is one line, a
	// stage lost is a line per field.
	maxDiffs = 12
)

const usage = "usage: flowcheck docs <dir> [flow.yaml ...] | flowcheck verify <dir>"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "flowcheck:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) >= 2 && args[0] == "docs" {
		return writeDocs(args[1], args[2:], out)
	}
	if len(args) == 2 && args[0] == "verify" {
		return verify(args[1], out)
	}
	return errors.New(usage)
}

// source is one flow to check: label names its files in the directory.
type source struct {
	label string
	yaml  []byte
}

// sources are the built-in flows, as compiled in, then the files named.
func sources(files []string) ([]source, error) {
	var out []source
	for _, name := range builtin.FlowNames() {
		y, _ := builtin.FlowYAML(name)
		out = append(out, source{label: "builtin-" + name, yaml: []byte(y)})
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		out = append(out, source{label: fileLabel(f), yaml: data})
	}
	return out, nil
}

// fileLabel is a flow file's path as one file name: examples/flows/v5.yaml is
// examples-flows-v5.
func fileLabel(path string) string {
	p := strings.TrimSuffix(filepath.ToSlash(filepath.Clean(path)), yamlSuffix)
	return strings.ReplaceAll(strings.TrimLeft(p, "./"), "/", "-")
}

// writeDocs writes each flow and the document the engine serves the editor
// for it. A flow that does not parse, or parses with a warning, stops it:
// these are what a person copies to start from.
func writeDocs(dir string, files []string, out io.Writer) error {
	srcs, err := sources(files)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, s := range srcs {
		if seen[s.label] {
			return fmt.Errorf("two flows are named %s", s.label)
		}
		seen[s.label] = true
		f, err := flow.Parse(s.label+yamlSuffix, s.yaml)
		if err != nil {
			return err
		}
		if len(f.Warnings) > 0 {
			return fmt.Errorf("%s binds with a warning: %s", s.label, strings.Join(f.Warnings, "; "))
		}
		doc, err := flow.ParseDoc(s.label+yamlSuffix, s.yaml)
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return fmt.Errorf("%s: %w", s.label, err)
		}
		if err := os.WriteFile(filepath.Join(dir, s.label+yamlSuffix), s.yaml, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, s.label+docSuffix), data, 0o644); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "%d flows parse and bind with no warning; their documents are in %s\n", len(srcs), dir)
	return nil
}

// verify holds every flow of the directory to what the editor wrote for it:
// the engine parses it, with no warning, as the same flow.
func verify(dir string, out io.Writer) error {
	docs, err := filepath.Glob(filepath.Join(dir, "*"+docSuffix))
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		return fmt.Errorf("no flow in %s: flowcheck docs writes them", dir)
	}
	failed := 0
	for _, doc := range docs {
		label := strings.TrimSuffix(filepath.Base(doc), docSuffix)
		problems := check(dir, label)
		if len(problems) == 0 {
			fmt.Fprintf(out, "ok    %s\n", label)
			continue
		}
		failed++
		fmt.Fprintf(out, "FAIL  %s\n", label)
		for _, p := range problems {
			fmt.Fprintf(out, "      %s\n", p)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d flows do not come back from the flow editor as they went in (%s holds what it wrote)", failed, len(docs), dir)
	}
	fmt.Fprintf(out, "%d flows come back from the flow editor as they went in\n", len(docs))
	return nil
}

// check is one flow's verdict: what is wrong with the editor's writing of
// it, nothing when it is the same flow.
func check(dir, label string) []string {
	original, err := os.ReadFile(filepath.Join(dir, label+yamlSuffix))
	if err != nil {
		return []string{err.Error()}
	}
	written, err := os.ReadFile(filepath.Join(dir, label+editorSuffix))
	if err != nil {
		return []string{fmt.Sprintf("the editor wrote nothing: %v", err)}
	}
	before, err := flow.Parse(label+yamlSuffix, original)
	if err != nil {
		return []string{err.Error()}
	}
	after, err := flow.Parse(label+editorSuffix, written)
	if err != nil {
		return []string{fmt.Sprintf("the engine refuses what the editor wrote: %v", err)}
	}
	var problems []string
	for _, w := range after.Warnings {
		problems = append(problems, "what the editor wrote binds with a warning: "+w)
	}
	diffs := diff("flow", meaning(before), meaning(after), nil)
	if len(diffs) > maxDiffs {
		diffs = append(diffs[:maxDiffs], fmt.Sprintf("… and %d more", len(diffs)-maxDiffs))
	}
	for _, d := range diffs {
		problems = append(problems, "the editor changed "+d)
	}
	return problems
}

// meaning is what a parsed flow says, as plain data, less what a rewrite may
// change without changing the flow: where things are written (the lines, the
// document's hash), and three orders — of the stages, which the editor
// writes along each chain's success path (every route is explicit once
// parsed; a chain still starts at the same stage, kept as Entries), of the
// parameters (the engine's six first), and of where they are read.
func meaning(f *flow.Flow) map[string]any {
	// A parsed flow is plain data: marshalling it cannot fail.
	data, _ := json.Marshal(f)
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	delete(m, "Hash")
	m["Stages"] = keyed(m["Stages"], "ID")
	m["Params"] = keyed(m["Params"], "name")
	entries := map[string]any{}
	for _, ph := range f.Phases {
		if ph.First <= ph.Last {
			entries[ph.ID] = f.Stages[ph.First].ID
		}
	}
	m["Entries"] = entries
	refs := make([]any, 0, len(f.Refs))
	for _, r := range f.Refs {
		refs = append(refs, strings.Join([]string{r.Param, r.Phase, r.Stage, r.Field, r.Input}, " "))
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].(string) < refs[j].(string) })
	m["Refs"] = refs
	dropLines(m)
	return m
}

// keyed turns a list of objects into an object by one of their fields, so two
// lists compare whatever their order and a difference names what it is in.
func keyed(list any, key string) map[string]any {
	out := map[string]any{}
	items, _ := list.([]any)
	for _, it := range items {
		if obj, ok := it.(map[string]any); ok {
			out[fmt.Sprint(obj[key])] = obj
		}
	}
	return out
}

// dropLines removes every source line, at any depth.
func dropLines(v any) {
	switch t := v.(type) {
	case map[string]any:
		delete(t, "Line")
		for _, child := range t {
			dropLines(child)
		}
	case []any:
		for _, child := range t {
			dropLines(child)
		}
	}
}

// diff lists where two values differ, each as "path: before → after".
func diff(path string, a, b any, out []string) []string {
	switch av := a.(type) {
	case map[string]any:
		if bv, ok := b.(map[string]any); ok {
			keys := make([]string, 0, len(av)+len(bv))
			for k := range av {
				keys = append(keys, k)
			}
			for k := range bv {
				if _, both := av[k]; !both {
					keys = append(keys, k)
				}
			}
			slices.Sort(keys)
			for _, k := range keys {
				out = diff(path+"."+k, av[k], bv[k], out)
			}
			return out
		}
	case []any:
		if bv, ok := b.([]any); ok && len(av) == len(bv) {
			for i := range av {
				out = diff(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i], out)
			}
			return out
		}
	}
	if !reflect.DeepEqual(a, b) {
		out = append(out, fmt.Sprintf("%s: %s → %s", path, show(a), show(b)))
	}
	return out
}

// show is one value in a difference: short, on one line.
func show(v any) string {
	if v == nil {
		return "nothing"
	}
	data, _ := json.Marshal(v)
	if s := string(data); len(s) <= 80 {
		return s
	}
	return string(data[:77]) + "…"
}
