//go:build conformance

package kinds

// A stage's omp harness options against the real pinned omp: the overlay
// RenderOptions writes is the config omp reads back, and the command line it
// adds is one `omp acp` starts with. How these tests run: conformance_test.go.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ompadapter "apothecary/internal/adapters/omp"
	"apothecary/internal/flow/harnessspec"
)

func TestOmpOptionsConformance(t *testing.T) {
	bin := toolsBin(t, "omp")
	wantVersion(t, bin, OmpVersion)
	overlay, flags, err := ompadapter.RenderOptions(map[string]string{
		harnessspec.OmpAdvisor: "acme/a", harnessspec.OmpAdvisorReview: "agent-end",
		harnessspec.OmpSmol: "acme/s", harnessspec.OmpSlow: "acme/l",
	})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "omp-options.yml")
	if err := os.WriteFile(file, overlay, 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	// Every option reads back as the setting it renders to.
	env := harnessEnv(home, "OMP_SKIP_SETUP=1", "PI_CONFIG_FILES="+file)
	out, err := runBin(t, env, "", bin, "config", "get", "modelRoles")
	if err != nil {
		t.Fatalf("omp config get modelRoles: %v\n%s", err, out)
	}
	for _, want := range []string{`"advisor":"acme/a"`, `"smol":"acme/s"`, `"slow":"acme/l"`} {
		if !strings.Contains(out, want) {
			t.Errorf("modelRoles = %q, want %s", out, want)
		}
	}
	if out, err := runBin(t, env, "", bin, "config", "get", "advisor.reviewMode"); err != nil || strings.TrimSpace(out) != "agent-end" {
		t.Errorf("advisor.reviewMode = %q, %v", out, err)
	}

	// omp exits with a usage error on a flag it does not take: an agent
	// that initializes took the command line.
	command := ompadapter.New(nil, ompadapter.Config{}).Profile().Command + " --config=" + file + " " + strings.Join(flags, " ")
	cl := startACP(t, command, harnessEnv(home), gitRepo(t))
	cl.initialize()
}
