package wsimage

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"apothecary/internal/project/kinds"
	"apothecary/workspace"
)

// toolsFakeDocker scripts the EnsureTools CLI sequence; `cp` really creates
// the destination dir with the hash marker so the rename and the completeness
// check succeed.
type toolsFakeDocker struct{ calls [][]string }

func (f *toolsFakeDocker) Run(_ context.Context, _ string, args ...string) (string, int, error) {
	f.calls = append(f.calls, args)
	switch args[0] {
	case "image": // inspect: not present yet
		return "", 1, nil
	case "build":
		return "built", 0, nil
	case "create":
		return "cid123\n", 0, nil
	case "cp":
		if _, err := os.Stat(args[2]); err == nil {
			return "destination exists", 1, nil // what docker would nest into
		}
		if err := os.MkdirAll(filepath.Join(args[2], "gnu", "bin"), 0o755); err != nil {
			return err.Error(), 1, nil
		}
		if err := os.WriteFile(filepath.Join(args[2], toolsHashFile), []byte(workspace.ToolsHash()+"\n"), 0o644); err != nil {
			return err.Error(), 1, nil
		}
		return "", 0, nil
	case "rm":
		return "", 0, nil
	}
	return "", 0, nil
}

func TestEnsureToolsBuildsExtractsAndCaches(t *testing.T) {
	f := &toolsFakeDocker{}
	runs := t.TempDir()
	b := NewWithRunner(f, runs)
	// A stale .tmp from an interrupted extraction must not survive (docker cp
	// would nest /tools inside it).
	stale := filepath.Join(runs, "tools-cache", workspace.ToolsHash()+".tmp")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}

	dir, err := b.EnsureTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(runs, "tools-cache", workspace.ToolsHash()); dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}
	if _, err := os.Stat(filepath.Join(dir, toolsHashFile)); err != nil {
		t.Errorf("extracted bundle lacks the hash marker: %v", err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("stale .tmp dir survived")
	}
	var haveBuild, haveArg, haveCreate, haveCp, haveRm bool
	for _, c := range f.calls {
		switch c[0] {
		case "build":
			haveBuild = strings.HasPrefix(c[2], "apothecary/tools:")
			haveArg = strings.Contains(strings.Join(c, " "), "--build-arg TOOLS_HASH="+workspace.ToolsHash())
		case "create":
			haveCreate = true
		case "cp":
			haveCp = c[1] == "cid123:/tools"
		case "rm":
			haveRm = true
		}
	}
	if !haveBuild || !haveArg || !haveCreate || !haveCp || !haveRm {
		t.Errorf("sequence incomplete: build=%v arg=%v create=%v cp=%v rm=%v calls=%v",
			haveBuild, haveArg, haveCreate, haveCp, haveRm, f.calls)
	}

	// Second call: pure cache hit, zero docker calls.
	f.calls = nil
	dir2, err := b.EnsureTools(context.Background())
	if err != nil || dir2 != dir {
		t.Fatalf("cache dir = %q, %v", dir2, err)
	}
	if len(f.calls) != 0 {
		t.Errorf("cache hit ran docker: %v", f.calls)
	}
}

// The bundle's Dockerfile pins each tool's version in its ARG defaults (CI
// builds it with plain `docker build`, so the pins must live there); the Go
// constants are what the rest of the engine knows. One source of truth is
// impossible across the two, so the test holds them together.
func TestToolsDockerfilePinsMatchKinds(t *testing.T) {
	df := workspace.Dockerfile("tools")
	pins := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^ARG\s+([A-Z_]+)=(\S+)`).FindAllStringSubmatch(df, -1) {
		pins[m[1]] = m[2]
	}
	for arg, want := range map[string]string{
		"OPENCODE_VERSION":  kinds.OpencodeVersion,
		"CODEX_VERSION":     kinds.CodexVersion,
		"CODEX_ACP_VERSION": kinds.CodexACPVersion,
		"OMP_VERSION":       kinds.OmpVersion,
	} {
		if got := pins[arg]; got != want {
			t.Errorf("workspace/images/tools/Dockerfile ARG %s=%q, internal/project/kinds pins %q", arg, got, want)
		}
	}
	if _, ok := pins["CODE_SERVER_VERSION"]; !ok {
		t.Error("Dockerfile must pin CODE_SERVER_VERSION")
	}
}
