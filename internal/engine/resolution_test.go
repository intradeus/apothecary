package engine

import (
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports"
)

// TestResolutionRunnerSelection covers the per-step-harness selection logic:
// a stage's pinned kind picks its runner, unpinned falls back to the default,
// and a single-harness Resolution (nil Runners) always uses Runner.
func TestResolutionRunnerSelection(t *testing.T) {
	def := &fake.AgentRunner{}
	alt := &fake.AgentRunner{}
	res := &Resolution{
		HarnessKind:  "opencode",
		HarnessKinds: []string{"codex", "opencode"},
		Runner:       def,
		Runners:      map[string]ports.AgentRunner{"opencode": def, "codex": alt},
	}
	if res.runnerFor("") != ports.AgentRunner(def) {
		t.Error("unpinned stage should use the default runner")
	}
	if res.runnerFor("codex") != ports.AgentRunner(alt) {
		t.Error("codex stage should use the codex runner")
	}
	if res.runnerFor("opencode") != ports.AgentRunner(def) {
		t.Error("opencode stage should use the default runner")
	}
	if !res.harnessInSet("codex") || !res.harnessInSet("opencode") {
		t.Error("set members must report in-set")
	}
	if res.harnessInSet("claude") {
		t.Error("a kind outside the set must report not-in-set")
	}

	// Single-harness setup (fakes): nil Runners ⇒ always the default; only the
	// one kind is in the set.
	solo := &Resolution{HarnessKind: "opencode", Runner: def}
	if solo.runnerFor("opencode") != ports.AgentRunner(def) {
		t.Error("solo resolution should fall back to Runner")
	}
	if !solo.harnessInSet("opencode") || solo.harnessInSet("codex") {
		t.Error("solo set membership wrong")
	}
}
