// Package archtest enforces the backend's import graph. The package map in
// docs/clean-architecture-plan.md says which packages may depend on which;
// this test turns that map into a build failure, so a shortcut import (an
// extracted service reaching back into the engine, an adapter imported from
// the domain) is caught at the commit that adds it rather than in review.
//
// Only imports inside the module are constrained. The standard library and
// third-party modules are allowed everywhere; test files are not checked.
package archtest

import (
	"errors"
	"go/build"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const module = "apothecary"

// rule constrains the packages whose path starts with pkg (a path relative to
// the module root; a trailing "/" means "strictly below"). Each list holds
// path prefixes in the same form.
type rule struct {
	pkg string
	// mayImport, when non-nil, is the complete allow-list of in-module imports.
	mayImport []string
	// denyImport lists in-module imports the package must never have.
	denyImport []string
	// importedOnlyBy, when non-nil, lists the only packages allowed to import it.
	importedOnlyBy []string
}

// outerLayers are the packages a domain-level package never reaches for: the
// driving adapter, the stores, the composition of adapters, the adapters.
var outerLayers = []string{
	"internal/engine",
	"internal/httpapi",
	"internal/confstore",
	"internal/kinds",
	"internal/adapters/",
}

var rules = []rule{
	{pkg: "internal/ports", mayImport: []string{}},
	// The capability ports: envport is the root every other builds on.
	{pkg: "internal/ports/envport", mayImport: []string{}},
	{pkg: "internal/ports/agentport", mayImport: []string{"internal/ports/envport"}},
	{pkg: "internal/ports/egressport", mayImport: []string{"internal/ports/envport"}},
	{pkg: "internal/ports/repoport", mayImport: []string{}},
	{pkg: "internal/ports/trackerport", mayImport: []string{"internal/ports/repoport"}},
	{pkg: "internal/ports/browserport", mayImport: []string{}},
	{pkg: "internal/ports/notifyport", mayImport: []string{}},
	{pkg: "internal/adapters/", importedOnlyBy: []string{"internal/kinds", "internal/adapters/", "cmd/"}},
	{pkg: "internal/adapters/pgstore", importedOnlyBy: []string{"cmd/"}},
	{pkg: "internal/engine", importedOnlyBy: []string{"cmd/", "internal/httpapi"}},
	{pkg: "internal/runinput", mayImport: []string{"internal/skills"}},
	{pkg: "internal/event", denyImport: outerLayers},
	{pkg: "internal/run", denyImport: outerLayers},
	// The language stands alone.
	{pkg: "internal/flow", mayImport: []string{"internal/flow/"}},
	{pkg: "internal/flow/", mayImport: []string{"internal/flow", "internal/flow/"}},
	// Building blocks with no domain knowledge.
	{pkg: "internal/platform/", mayImport: []string{"internal/platform/"}},
	{pkg: "internal/wsname", mayImport: []string{"internal/run"}},
	{pkg: "internal/bootstrap", mayImport: []string{
		"internal/flow/agentspec", "internal/event", "internal/ports/", "internal/platform/shell", "internal/skills",
	}},
	{pkg: "internal/modelcheck", mayImport: []string{"internal/flow", "internal/ports/", "internal/resolution"}},
	{pkg: "internal/redact", mayImport: []string{"internal/resolution"}},
	{pkg: "internal/mrs", mayImport: []string{"internal/event", "internal/ports/", "internal/resolution", "internal/run"}},
	{pkg: "internal/resources", mayImport: []string{"internal/ports/", "internal/resolution", "internal/run", "internal/wsname"}},
	{pkg: "internal/envstate", mayImport: []string{"internal/ports/", "internal/resolution", "internal/run"}},
	{pkg: "internal/wsinfo", mayImport: []string{
		"internal/envstate", "internal/event", "internal/ports/", "internal/resolution", "internal/run",
		"internal/platform/shell", "internal/sidecar", "internal/wsname",
	}},
	{pkg: "internal/envcheck", mayImport: []string{
		"internal/flow/agentspec", "internal/bootstrap", "internal/flow", "internal/modelcheck",
		"internal/ports/", "internal/resolution", "internal/platform/shell", "internal/wsname",
	}},
	{pkg: "internal/egress", mayImport: []string{"internal/ports/", "internal/resolution", "internal/run", "internal/wsname"}},
	{pkg: "internal/ask", mayImport: []string{
		"internal/flow/agentspec", "internal/flow", "internal/modelcheck", "internal/ports/",
		"internal/redact", "internal/resolution", "internal/run",
	}},
	{pkg: "internal/authsink", mayImport: []string{"internal/ports/", "internal/redact", "internal/resolution", "internal/run"}},
	{pkg: "internal/runports", mayImport: []string{"internal/event", "internal/portrelay", "internal/ports/", "internal/resolution", "internal/run"}},
	{pkg: "internal/browsermcp", mayImport: []string{"internal/ports/", "internal/resolution", "internal/run"}},
	{pkg: "internal/services", mayImport: []string{
		"internal/event", "internal/ports/", "internal/resolution", "internal/run",
		"internal/platform/shell", "internal/sidecar", "internal/wsinfo",
	}},
	{pkg: "internal/runcore", mayImport: []string{
		"internal/flow/actionspec", "internal/flow/agentspec", "internal/event", "internal/flow",
		"internal/flow/reviewfile", "internal/run", "internal/flow/signalspec", "internal/ports/",
	}},
	{pkg: "internal/resolution", mayImport: []string{
		"internal/flow/actionspec", "internal/flow/agentspec", "internal/event", "internal/flow",
		"internal/run", "internal/flow/signalspec", "internal/ports/",
	}},
}

// matches reports whether path is the package prefix names: "a/b" matches
// a/b only, "a/b/" matches every package strictly below a/b.
func matches(path, prefix string) bool {
	if strings.HasSuffix(prefix, "/") {
		return strings.HasPrefix(path, prefix)
	}
	return path == prefix
}

func matchesAny(path string, prefixes []string) bool {
	return slices.ContainsFunc(prefixes, func(p string) bool { return matches(path, p) })
}

// packages maps every package under internal/ and cmd/ (module-relative
// path) to its in-module, non-test imports (module-relative too).
func packages(t *testing.T) map[string][]string {
	t.Helper()
	root := moduleRoot(t)
	out := map[string][]string{}
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(dir string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return nil
			}
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			pkg, err := build.Default.ImportDir(dir, 0)
			if _, ok := errors.AsType[*build.NoGoError](err); ok {
				return nil
			}
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, dir)
			if err != nil {
				return err
			}
			var imports []string
			for _, imp := range pkg.Imports {
				if after, ok := strings.CutPrefix(imp, module+"/"); ok {
					imports = append(imports, after)
				}
			}
			out[filepath.ToSlash(rel)] = imports
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's directory")
		}
		dir = parent
	}
}

func TestImportGraph(t *testing.T) {
	pkgs := packages(t)
	for _, r := range rules {
		found := false
		for path, imports := range pkgs {
			if !matches(path, r.pkg) {
				continue
			}
			found = true
			for _, imp := range imports {
				if r.mayImport != nil && !matchesAny(imp, r.mayImport) {
					t.Errorf("%s imports %s; it may import only %v", path, imp, r.mayImport)
				}
				if matchesAny(imp, r.denyImport) {
					t.Errorf("%s imports %s, which it must never import", path, imp)
				}
			}
		}
		if !found {
			t.Errorf("rule for %s matches no package; fix or remove the rule", r.pkg)
		}
		if r.importedOnlyBy == nil {
			continue
		}
		for path, imports := range pkgs {
			if matches(path, r.pkg) || matchesAny(path, r.importedOnlyBy) {
				continue
			}
			for _, imp := range imports {
				if matches(imp, r.pkg) {
					t.Errorf("%s imports %s; only %v may", path, imp, r.importedOnlyBy)
				}
			}
		}
	}
}

// TestRunCoreReadsNoClock: the interpreter's core is a pure function of the
// run's events — a time it needs comes from an event or a parameter, so
// replay and the live loop see the same one.
func TestRunCoreReadsNoClock(t *testing.T) {
	dir := filepath.Join(moduleRoot(t), "internal", "runcore")
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range []string{"time.Now(", "time.Since(", "time.Until("} {
			if strings.Contains(string(src), call) {
				t.Errorf("%s calls %s; runcore takes its times from events or parameters", filepath.Base(f), call)
			}
		}
	}
}
