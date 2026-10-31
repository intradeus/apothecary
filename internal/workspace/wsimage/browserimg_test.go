package wsimage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// browserFakeDocker answers image inspects from a set and records builds,
// checking the context they were handed.
type browserFakeDocker struct {
	have   map[string]bool
	builds []string
	ctx    []string
}

func (f *browserFakeDocker) Run(_ context.Context, dir string, args ...string) (string, int, error) {
	switch args[0] {
	case "image":
		if f.have[args[len(args)-1]] {
			return "[]", 0, nil
		}
		return "", 1, nil
	case "build":
		f.builds = append(f.builds, args[2])
		for _, p := range []string{"Dockerfile", "shim/main.go", "shim/browser.go"} {
			if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
				f.ctx = append(f.ctx, p)
			}
		}
		f.have[args[2]] = true
	}
	return "", 0, nil
}

// The browser image is built once per content: its tag is known without a
// build, the build context carries the shim it compiles, and a second call
// is a cache hit.
func TestEnsureBrowserImageBuildsOnceFromTheShimSource(t *testing.T) {
	f := &browserFakeDocker{have: map[string]bool{}}
	b := NewWithRunner(f, t.TempDir())
	tag, err := b.EnsureBrowserImage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tag != BrowserImageTag() || !strings.HasPrefix(tag, "apothecary/browser:") {
		t.Errorf("tag = %q, BrowserImageTag = %q", tag, BrowserImageTag())
	}
	if len(f.builds) != 1 || strings.Join(f.ctx, " ") != "Dockerfile shim/main.go shim/browser.go" {
		t.Fatalf("builds = %v, context = %v", f.builds, f.ctx)
	}
	if _, err := b.EnsureBrowserImage(context.Background()); err != nil || len(f.builds) != 1 {
		t.Errorf("second call built again: %v %v", f.builds, err)
	}
	for _, pin := range []string{"@playwright/mcp@" + playwrightMCPVersion, "gojq/cli@" + gojqVersion} {
		if !strings.Contains(browserDockerfile, pin) {
			t.Errorf("Dockerfile does not pin %s", pin)
		}
	}
}
