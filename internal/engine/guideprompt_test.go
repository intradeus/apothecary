package engine

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run/runinput"
)

const guideFlowDoc = `name: guide
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: writer
    prompt: write the notes
    outputs:
      - { name: notes, type: file }
main:
  - { id: write, type: agent, harness: $harness, model: $model, agent: writer,
      output: { notes: { to: artifacts/notes.md, type: file } } }
`

// The agent guide is named in the prompt only when the project sets one and
// the run's checkout holds it; otherwise the prompt says nothing about it.
func TestAgentGuideSection(t *testing.T) {
	for _, c := range []struct {
		name, path, seeded string
		want               bool
	}{
		{"set and present", "docs/AI.md", "docs/AI.md", true},
		{"set but absent", "docs/AI.md", "", false},
		// A file keyed "" would answer a read of an empty path: none is made.
		{"unset", "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, mustFlow(t, guideFlowDoc))
			h.res.AgentsMDPath = c.path
			h.envp.ExecFunc = func(e *fake.Env, _ envport.Command) (envport.ExecResult, error) {
				e.Files[c.seeded] = []byte("# Conventions")
				return envport.ExecResult{}, nil
			}
			h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
				"writer": func(rc fake.RunCall) (agentport.AgentResult, error) {
					h.writeFile(t, rc.Env, "artifacts/notes.md", "notes")
					return agentport.AgentResult{}, nil
				},
			})
			id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T", Goal: "g"}, "guide", testProject)
			if err != nil {
				t.Fatal(err)
			}
			h.e.Wait(id)
			prompt := lastPrompt(h)
			at, contract := strings.Index(prompt, "# REPOSITORY GUIDE"), strings.Index(prompt, "# OUTPUT CONTRACT")
			if (at >= 0) != c.want || contract < 0 {
				t.Fatalf("guide section shown = %v, want %v:\n%s", at >= 0, c.want, prompt)
			}
			if c.want && (at > contract || !strings.Contains(prompt[at:contract], "agent guide is docs/AI.md: read it first")) {
				t.Errorf("the guide section names the file, before the contract:\n%s", prompt)
			}
		})
	}
}
