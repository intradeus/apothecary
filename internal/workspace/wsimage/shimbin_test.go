package wsimage

import (
	"context"
	"os"
	"strings"
	"testing"
)

// shimFakeDocker scripts the EnsureShim CLI sequence; `cp` really creates
// the destination file so chmod/rename succeed.
type shimFakeDocker struct{ calls [][]string }

func (f *shimFakeDocker) Run(_ context.Context, _ string, args ...string) (string, int, error) {
	f.calls = append(f.calls, args)
	switch args[0] {
	case "image": // inspect: not present yet
		return "", 1, nil
	case "build":
		return "built", 0, nil
	case "create":
		return "cid123\n", 0, nil
	case "cp":
		if err := os.WriteFile(args[2], []byte("BIN"), 0o644); err != nil {
			return err.Error(), 1, nil
		}
		return "", 0, nil
	case "rm":
		return "", 0, nil
	}
	return "", 0, nil
}

func TestEnsureShimBuildsExtractsAndCaches(t *testing.T) {
	f := &shimFakeDocker{}
	b := NewWithRunner(f, t.TempDir())

	path, err := b.EnsureShim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "BIN" {
		t.Fatalf("extracted binary = %q, %v", data, err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", info.Mode())
	}
	var haveBuild, haveCreate, haveCp, haveRm bool
	for _, c := range f.calls {
		switch c[0] {
		case "build":
			haveBuild = strings.HasPrefix(c[2], "apothecary/shim:")
		case "create":
			haveCreate = true
		case "cp":
			haveCp = strings.HasPrefix(c[1], "cid123:")
		case "rm":
			haveRm = true
		}
	}
	if !haveBuild || !haveCreate || !haveCp || !haveRm {
		t.Errorf("sequence incomplete: build=%v create=%v cp=%v rm=%v calls=%v",
			haveBuild, haveCreate, haveCp, haveRm, f.calls)
	}

	// Second call: pure cache hit, zero docker calls.
	f.calls = nil
	path2, err := b.EnsureShim(context.Background())
	if err != nil || path2 != path {
		t.Fatalf("cache path = %q, %v", path2, err)
	}
	if len(f.calls) != 0 {
		t.Errorf("cache hit ran docker: %v", f.calls)
	}
}

// The builder pins the shim's dependencies for the docker build; the root
// module compiles the same shim source (go vet/test ./...). Both must use
// one version of each, or the tested shim and the shipped one quietly
// diverge.
func TestShimDependencyPinsMatchGoMod(t *testing.T) {
	data, err := os.ReadFile("../../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range []struct{ module, pin, get string }{
		{"github.com/itchyny/gojq", gojqVersion, "github.com/itchyny/gojq/cli@"},
		{"golang.org/x/crypto", xcryptoVersion, "golang.org/x/crypto/ssh@"},
	} {
		var modVersion string
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "require "))
			if len(f) >= 2 && f[0] == dep.module {
				modVersion = f[1]
			}
		}
		if modVersion == "" {
			t.Fatalf("go.mod does not require %s", dep.module)
		}
		if dep.pin != modVersion {
			t.Errorf("shim builder pins %s %s, go.mod requires %s", dep.module, dep.pin, modVersion)
		}
		if !strings.Contains(shimBuilderDockerfile, dep.get+modVersion+" ") {
			t.Errorf("builder Dockerfile does not get %s%s:\n%s", dep.get, modVersion, shimBuilderDockerfile)
		}
	}
}
