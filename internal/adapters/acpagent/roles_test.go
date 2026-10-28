package acpagent

import (
	"strings"
	"testing"

	"apothecary/internal/agentspec"
)

// Every role ends with the standing ask_human section — mechanics the flow's
// prompt no longer has to carry — except the Ask pane's, which has no
// ask_human to call (a read-only session gets no profile MCP server).
func TestRoleTextAppendsAskHumanRules(t *testing.T) {
	role := RoleText(&agentspec.Spec{Name: "planner", Prompt: "Plan carefully.\n", ReadOnly: true})
	if i, j := strings.Index(role, "Plan carefully."), strings.Index(role, "# ASKING THE HUMAN"); i < 0 || j < i {
		t.Fatalf("rules must follow the prompt:\n%s", role)
	}
	for _, want := range []string{"ask_human_ask_human", "type=clarification", "type=blocker", "type=permission", "a guess is worse than a wait"} {
		if !strings.Contains(role, want) {
			t.Errorf("role missing %q:\n%s", want, role)
		}
	}
	ask := RoleText(&agentspec.Spec{Name: agentspec.AskAgent, Prompt: "Answer.\n", ReadOnly: true})
	if strings.Contains(ask, "ASKING THE HUMAN") || strings.Contains(ask, "ask_human") {
		t.Errorf("the Ask pane's role must not be told to ask:\n%s", ask)
	}
}

// The renderer that knows the listed name states it; one that does not says
// the name may be prefixed.
func TestAskHumanRulesNamesTheTool(t *testing.T) {
	exact := AskHumanRules("ask_human_ask_human")
	if !strings.Contains(exact, "listed as ask_human_ask_human") || strings.Contains(exact, "may list it") {
		t.Errorf("exact name not stated:\n%s", exact)
	}
	bare := AskHumanRules("ask_human")
	if strings.Contains(bare, "listed as") || strings.Contains(bare, "may list it") {
		t.Errorf("bare name needs no hint:\n%s", bare)
	}
	unknown := AskHumanRules("")
	if !strings.Contains(unknown, "may list it under a prefixed name such as ask_human_ask_human") {
		t.Errorf("unknown name must warn about prefixing:\n%s", unknown)
	}
}
