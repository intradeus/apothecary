// Package livetests holds the live suites (`//go:build live`) to one way of
// not running: livetest.Skip, which CI's sandbox-test turns into a failure
// (APOTHECARY_LIVE_STRICT). A bare t.Skip there would let a suite CI depends
// on go green without running, which is how a live test that cannot pass
// where it is meant to run goes unnoticed (docs/notes,
// 2026-11-20-live-suites-skip-loudly-and-never-in-ci.md).
package livetests

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const root = "../../.."

// live reports whether a file's build constraint needs the live tag.
func live(src []byte) bool {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			return false
		}
		if !constraint.IsGoBuild(line) {
			continue
		}
		expr, err := constraint.Parse(line)
		if err != nil {
			return false
		}
		with := expr.Eval(func(string) bool { return true })
		without := expr.Eval(func(tag string) bool { return tag != "live" })
		return with && !without
	}
	return false
}

// bareSkips lists every Skip, Skipf or SkipNow in a live file that does not
// go through livetest.
func bareSkips(name string, src []byte) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Skip", "Skipf", "SkipNow":
		default:
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == "livetest" {
			return true
		}
		out = append(out, fset.Position(call.Pos()).String()+": "+sel.Sel.Name+" bypasses livetest.Skip")
		return true
	})
	return out, nil
}

// problems walks dir for live test files and lists their bare skips; n is
// how many live files it read.
func problems(dir string) (out []string, n int) {
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "build":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !live(src) {
			return nil
		}
		n++
		rel, _ := filepath.Rel(dir, path)
		skips, err := bareSkips(rel, src)
		if err != nil {
			out = append(out, err.Error())
		}
		out = append(out, skips...)
		return nil
	})
	if err != nil {
		out = append(out, err.Error())
	}
	return out, n
}

func TestLiveSuitesSkipThroughLivetest(t *testing.T) {
	out, n := problems(root)
	for _, p := range out {
		t.Error(p)
	}
	// The walk found the suites: an empty walk would pass anything.
	if n < 4 {
		t.Errorf("found %d live test files; the repository has at least 4", n)
	}
}

func TestTheCheckCatchesABareSkip(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("bad_test.go", "//go:build live && linux\n\npackage x\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {\n\tt.Skip(\"no\")\n\tt.Skipf(\"%s\", \"no\")\n\tt.SkipNow()\n}\n")
	write("good_test.go", "//go:build live\n\npackage x\n\nimport (\n\t\"testing\"\n\n\t\"apothecary/internal/platform/livetest\"\n)\n\nfunc TestY(t *testing.T) { livetest.Skip(t, \"no\") }\n")
	// Not live: its own skips are its business.
	write("unit_test.go", "package x\n\nimport \"testing\"\n\nfunc TestZ(t *testing.T) { t.Skip(\"no\") }\n")
	write("notlive_test.go", "//go:build !live\n\npackage x\n\nimport \"testing\"\n\nfunc TestW(t *testing.T) { t.Skip(\"no\") }\n")
	write("broken_test.go", "//go:build live\n\npackage x\n\nfunc {\n")
	out, n := problems(dir)
	if n != 3 {
		t.Errorf("live files = %d, want bad, good and broken", n)
	}
	joined := strings.Join(out, "\n")
	for _, want := range []string{"bad_test.go:8:2: Skip bypasses", "bad_test.go:9:2: Skipf bypasses", "bad_test.go:10:2: SkipNow bypasses", "broken_test.go"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "good_test.go") || strings.Contains(joined, "unit_test.go") || strings.Contains(joined, "notlive_test.go") {
		t.Errorf("flagged a file that is fine:\n%s", joined)
	}
	if out, _ := problems(filepath.Join(dir, "absent")); len(out) == 0 {
		t.Error("an unreadable tree reported nothing")
	}
	if live([]byte("//go:build live &&\npackage x\n")) {
		t.Error("an unparsable constraint read as live")
	}
}
