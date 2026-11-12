package kinds

import (
	"regexp"
	"testing"

	"apothecary/workspace"
)

// The tools bundle (workspace/images/tools, published by deploy/images.sh)
// pins each harness CLI in an ARG; these kinds pin the same versions for
// what the engine writes them. The two must not drift.
func TestToolsDockerfilePinsMatchKinds(t *testing.T) {
	df := workspace.Dockerfile("tools")
	pins := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^ARG\s+([A-Z_]+)=(\S+)`).FindAllStringSubmatch(df, -1) {
		pins[m[1]] = m[2]
	}
	for arg, want := range map[string]string{
		"OPENCODE_VERSION":  OpencodeVersion,
		"CODEX_VERSION":     CodexVersion,
		"CODEX_ACP_VERSION": CodexACPVersion,
		"OMP_VERSION":       OmpVersion,
	} {
		if got := pins[arg]; got != want {
			t.Errorf("workspace/images/tools/Dockerfile ARG %s=%q, internal/project/kinds pins %q", arg, got, want)
		}
	}
	if _, ok := pins["CODE_SERVER_VERSION"]; !ok {
		t.Error("Dockerfile must pin CODE_SERVER_VERSION")
	}
}
