package engine

import (
	"strings"
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

const phaseSignalDoc = `name: sig
version: 5
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
main:
  - { id: build, type: command, run: "true" }
watchers:
  - id: polish
    on: manual
    stages:
      - { id: tidy, type: command, run: "true" }
  - id: conflicts
    on: { signal: mr_conflicts, start: manual }
    stages:
      - { id: fix, type: command, run: "true" }
outcomes:
  - id: shipped
    on: mr_merged
    then: done
`

// A phase that opens on a repository host signal — a watcher's, an
// outcome's — needs the repository host, like a wait stage does, and so
// does a main chain started on a merge request; a main chain started on a
// ticket and a watcher started by hand need nothing. Checked on the flow's
// refs, which PreflightProject and run creation share.
func TestFlowRefsCheckPhaseSignals(t *testing.T) {
	fl := mustFlow(t, phaseSignalDoc)
	h := newHarness(t, fl)
	if err := h.e.preflightFlowRefs(h.res, fl); err != nil {
		t.Fatalf("with a repository host: %v", err)
	}
	h.res.RepoHost = nil
	err := h.e.preflightFlowRefs(h.res, fl)
	if err == nil || !strings.Contains(err.Error(), `watcher "conflicts"`) || !strings.Contains(err.Error(), "repository integration") {
		t.Fatalf("without a repository host: err = %v", err)
	}
	onlyOutcome := mustFlow(t, strings.Replace(phaseSignalDoc, "on: { signal: mr_conflicts, start: manual }", "on: manual", 1))
	if err := h.e.preflightFlowRefs(h.res, onlyOutcome); err == nil || !strings.Contains(err.Error(), `outcome "shipped"`) {
		t.Errorf("an outcome without a repository host: err = %v", err)
	}
	noFire := mustFlow(t, strings.Replace(strings.Replace(phaseSignalDoc, "on: { signal: mr_conflicts, start: manual }", "on: manual", 1),
		"outcomes:\n  - id: shipped\n    on: mr_merged\n    then: done\n",
		"  - id: finish\n    on: manual\n    stages:\n      - { id: wrap, type: command, run: \"true\", on_success: done }\n", 1))
	if err := h.e.preflightFlowRefs(h.res, noFire); err != nil {
		t.Errorf("manual phases without a repository host: %v", err)
	}
	onMR := mustFlow(t, "name: rev\nversion: 5\noperates_on: merge_request\nparameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }\n"+
		"main:\n  - { id: look, type: command, run: \"true\", on_success: done }\n")
	if err := h.e.preflightFlowRefs(h.res, onMR); err == nil || !strings.Contains(err.Error(), "the main chain") || !strings.Contains(err.Error(), "operates_on: merge_request") {
		t.Errorf("a flow started on merge requests, without a repository host: err = %v", err)
	}
}
