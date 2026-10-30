package engine

import (
	"context"
	"strings"
	"sync"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
)

// structuredDoc: a writer whose report has a schema, a reader that demands
// it, and a committer whose message the commit action reads.
const structuredDoc = `name: structured
version: 5
parameters: { harness: fake, model: fake/model }
schemas:
  - name: report
    json_schema:
      type: object
      properties: { passed: { type: integer, minimum: 0 } }
      required: [passed]
agents:
  - name: writer
    prompt: write the report
    inputs:
      - { name: failure, type: failure, optional: true }
    outputs:
      - { name: report, schema: report }
  - name: reader
    prompt: read the report
    inputs:
      - { name: report, schema: report }
  - name: committer
    prompt: write the message
    inputs:
      - { name: failure, type: failure, optional: true }
    outputs:
      - { name: commit, type: commit_message }
`

func stageFailures(evs []event.Event) []*event.StageFailed {
	var out []*event.StageFailed
	for _, e := range evs {
		if p, ok := e.Payload.(*event.StageFailed); ok {
			out = append(out, p)
		}
	}
	return out
}

// A file that does not hold its schema fails the stage that wrote it with
// output_invalid, and the next attempt is told exactly what is wrong — and
// was told the schema in the first place.
func TestSchemaOutputInvalidIsRetriedWithTheViolations(t *testing.T) {
	doc := structuredDoc + `main:
  - { id: write, type: agent, harness: $harness, model: $model, agent: writer, input: { failure: { from: edge.failure, type: failure, optional: true } },
      output: { report: { to: artifacts/report.json, schema: report } }, on_fail: { to: write, max: 1 } }
`
	h := newHarness(t, mustFlow(t, doc))
	var mu sync.Mutex
	var prompts []string
	h.agentScript(map[string]func(fake.RunCall) (ports.AgentResult, error){
		"writer": func(c fake.RunCall) (ports.AgentResult, error) {
			mu.Lock()
			prompts = append(prompts, c.In.Prompt)
			n := len(prompts)
			mu.Unlock()
			if n == 1 {
				h.writeFile(t, c.Env, "artifacts/report.json", `{"passed": -2}`)
			} else {
				h.writeFile(t, c.Env, "artifacts/report.json", `{"passed": 2}`)
			}
			return ports.AgentResult{TurnsUsed: 1}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "structured", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events %v", v.State, eventTypes(h.events(t, runID)))
	}
	fails := stageFailures(h.events(t, runID))
	if len(fails) != 1 || fails[0].Reason != event.ReasonOutputInvalid || !strings.Contains(fails[0].Error, "at /passed: minimum: got -2, want 0") {
		t.Fatalf("failures = %+v", fails)
	}
	if len(prompts) != 2 {
		t.Fatalf("%d attempts, want 2", len(prompts))
	}
	if !strings.Contains(prompts[0], `artifacts/report.json must be JSON satisfying this JSON Schema`) ||
		!strings.Contains(prompts[0], `{"type":"object","properties":{"passed":{"type":"integer","minimum":0}},"required":["passed"]}`) {
		t.Errorf("the contract does not show the schema:\n%s", prompts[0])
	}
	if !strings.Contains(prompts[1], "at /passed: minimum: got -2, want 0") {
		t.Errorf("the retry was not told why:\n%s", prompts[1])
	}
}

// A file an agent reads with a schema is checked before it starts: it is a
// file of the repository, so nothing checked it on the way in.
func TestSchemaInputInvalidFailsBeforeTheAgentRuns(t *testing.T) {
	doc := structuredDoc + `main:
  - { id: make, type: command, run: "make-report" }
  - { id: read, type: agent, harness: $harness, model: $model, agent: reader, input: { report: { from: repo:artifacts/report.json, schema: report } }, on_fail: abort }
`
	h := newHarness(t, mustFlow(t, doc))
	h.envp.ExecFunc = func(e *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if cmd.Script == "make-report" {
			e.Files["artifacts/report.json"] = []byte(`{"passed": "all"}`)
		}
		return envport.ExecResult{}, nil
	}
	ran := false
	h.agentScript(map[string]func(fake.RunCall) (ports.AgentResult, error){
		"reader": func(fake.RunCall) (ports.AgentResult, error) { ran = true; return ports.AgentResult{}, nil },
	})
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "structured", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if ran {
		t.Error("the reader ran on a malformed input")
	}
	fails := stageFailures(h.events(t, runID))
	if len(fails) != 1 || fails[0].StageID != "read" || fails[0].Reason != event.ReasonInputInvalid || !strings.Contains(fails[0].Error, "at /passed: got string, want integer") {
		t.Fatalf("failures = %+v", fails)
	}
}

// The commit action commits with the committer's message — subject, blank
// line, body — read from stdin, and leaves a clean tree alone.
func TestCommitActionCommitsTheMessage(t *testing.T) {
	doc := structuredDoc + `main:
  - { id: msg,    type: agent, harness: $harness, model: $model, agent: committer, output: { commit: { to: artifacts/commit.json, type: commit_message } } }
  - { id: commit, type: action, action: commit, input: { message: { from: artifacts/commit.json, type: commit_message } } }
`
	h := newHarness(t, mustFlow(t, doc))
	h.agentScript(map[string]func(fake.RunCall) (ports.AgentResult, error){
		"committer": func(c fake.RunCall) (ports.AgentResult, error) {
			if !strings.Contains(c.In.Prompt, `"subject"`) {
				t.Errorf("the contract does not show the commit_message shape:\n%s", c.In.Prompt)
			}
			h.writeFile(t, c.Env, "artifacts/commit.json", `{"subject": "feat: it's done", "body": "Why it was needed."}`)
			return ports.AgentResult{TurnsUsed: 1}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "structured", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events %v", v.State, eventTypes(h.events(t, runID)))
	}
	var commits []string
	for _, c := range h.envp.Execs() {
		if strings.Contains(c.Cmd.Script, "git commit") {
			commits = append(commits, c.Cmd.Script)
		}
	}
	if len(commits) != 1 {
		t.Fatalf("commit scripts = %q", commits)
	}
	want := `printf '%s' 'feat: it'\''s done` + "\n\nWhy it was needed.\n" + `' | git commit -F -`
	if !strings.Contains(commits[0], want) || !strings.HasPrefix(commits[0], "git add -A && if git diff --cached --quiet;") {
		t.Errorf("commit script = %q, want it to contain %q", commits[0], want)
	}
}

// A message that is not a commit_message never reaches git — here a file of
// the repository, which nothing checked on the way in.
func TestCommitActionRefusesAMalformedMessage(t *testing.T) {
	doc := structuredDoc + `main:
  - { id: make,   type: command, run: "make-message" }
  - { id: commit, type: action, action: commit, input: { message: { from: repo:artifacts/commit.json, type: commit_message } }, on_fail: abort }
`
	h := newHarness(t, mustFlow(t, doc))
	h.envp.ExecFunc = func(e *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if cmd.Script == "make-message" {
			e.Files["artifacts/commit.json"] = []byte(`{"subject": "two\nlines"}`)
		}
		return envport.ExecResult{}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "structured", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	for _, c := range h.envp.Execs() {
		if strings.Contains(c.Cmd.Script, "git commit") {
			t.Errorf("git ran: %q", c.Cmd.Script)
		}
	}
	fails := stageFailures(h.events(t, runID))
	if len(fails) != 1 || fails[0].Reason != event.ReasonInputInvalid || !strings.Contains(fails[0].Error, "at /subject") {
		t.Fatalf("failures = %+v", fails)
	}
}

// A refused commit (a hook said no) fails the stage with git's words.
func TestCommitActionRefusedByGit(t *testing.T) {
	doc := structuredDoc + `main:
  - { id: msg,    type: agent, harness: $harness, model: $model, agent: committer, output: { commit: { to: artifacts/commit.json, type: commit_message } } }
  - { id: commit, type: action, action: commit, input: { message: { from: artifacts/commit.json, type: commit_message } }, on_fail: abort }
`
	h := newHarness(t, mustFlow(t, doc))
	h.agentScript(map[string]func(fake.RunCall) (ports.AgentResult, error){
		"committer": func(c fake.RunCall) (ports.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/commit.json", `{"subject": "fix: x"}`)
			return ports.AgentResult{}, nil
		},
	})
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "git commit") {
			return envport.ExecResult{ExitCode: 1, Output: "pre-commit hook: lint failed"}, nil
		}
		return envport.ExecResult{}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "structured", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	fails := stageFailures(h.events(t, runID))
	if len(fails) != 1 || fails[0].Reason != event.ReasonExitNonzero || !strings.Contains(fails[0].Error, "pre-commit hook: lint failed") {
		t.Fatalf("failures = %+v", fails)
	}
}

// A command's ports carry their structure too: what it writes is
// checked when it exits 0, what it reads before it runs.
func TestCommandSchemaOutputInvalid(t *testing.T) {
	doc := structuredDoc + `main:
  - { id: make, type: command, run: "make-report", output: { report: { to: artifacts/report.json, schema: report } }, on_fail: abort }
`
	h := newHarness(t, mustFlow(t, doc))
	h.envp.ExecFunc = func(e *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if cmd.Script == "make-report" {
			e.Files["artifacts/report.json"] = []byte(`{"passed": -1}`)
		}
		return envport.ExecResult{}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "structured", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	fails := stageFailures(h.events(t, runID))
	if len(fails) != 1 || fails[0].StageID != "make" || fails[0].Reason != event.ReasonOutputInvalid || !strings.Contains(fails[0].Error, "at /passed: minimum: got -1, want 0") {
		t.Fatalf("failures = %+v", fails)
	}
}

func TestCommandSchemaInputInvalidFailsBeforeTheScript(t *testing.T) {
	// The file is the repository's — a person, or anything outside the
	// flow, wrote it — so only the reader's check stands.
	doc := structuredDoc + `main:
  - { id: seed, type: command, run: "seed" }
  - { id: use,  type: command, run: "use-report", input: { report: { from: repo:artifacts/report.json, schema: report } }, on_fail: abort }
`
	h := newHarness(t, mustFlow(t, doc))
	ran := false
	h.envp.ExecFunc = func(e *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		switch cmd.Script {
		case "seed":
			e.Files["artifacts/report.json"] = []byte(`{"passed": "many"}`)
		case "use-report":
			ran = true
		}
		return envport.ExecResult{}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "structured", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if ran {
		t.Error("the script ran on a malformed input")
	}
	fails := stageFailures(h.events(t, runID))
	if len(fails) != 1 || fails[0].StageID != "use" || fails[0].Reason != event.ReasonInputInvalid || !strings.Contains(fails[0].Error, "at /passed: got string, want integer") {
		t.Fatalf("failures = %+v", fails)
	}
}

// A schema beside a built-in type narrows it: the file holds both, and the
// agent is shown both.
func TestTypedOutputNarrowedBySchema(t *testing.T) {
	doc := `name: structured
version: 5
parameters: { harness: fake, model: fake/model }
schemas:
  - name: conventional
    json_schema:
      properties: { subject: { pattern: "^(feat|fix|chore)(\\(.+\\))?: " } }
agents:
  - name: committer
    prompt: write the message
    outputs:
      - { name: commit, type: commit_message, schema: conventional }
main:
  - { id: msg,    type: agent, harness: $harness, model: $model, agent: committer, output: { commit: { to: artifacts/commit.json, type: commit_message, schema: conventional } }, on_fail: abort }
  - { id: commit, type: action, action: commit, input: { message: { from: artifacts/commit.json, type: commit_message } } }
`
	h := newHarness(t, mustFlow(t, doc))
	h.agentScript(map[string]func(fake.RunCall) (ports.AgentResult, error){
		"committer": func(c fake.RunCall) (ports.AgentResult, error) {
			if n := strings.Count(c.In.Prompt, "must be JSON satisfying this JSON Schema"); n != 2 {
				t.Errorf("the contract shows %d shapes, want the type's and the flow's", n)
			}
			h.writeFile(t, c.Env, "artifacts/commit.json", `{"subject": "Add widgets"}`)
			return ports.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "structured", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	fails := stageFailures(h.events(t, runID))
	if len(fails) != 1 || fails[0].Reason != event.ReasonOutputInvalid || !strings.Contains(fails[0].Error, `schema "conventional"`) {
		t.Fatalf("failures = %+v", fails)
	}
}

// An agent that finishes without writing a declared output is retried, and
// the retry is told which file it left out.
func TestMissingOutputIsFedToTheRetry(t *testing.T) {
	doc := structuredDoc + `main:
  - id: write
    type: agent
    harness: $harness
    model: $model
    agent: writer
    input:
      failure: { from: edge.failure, type: failure, optional: true }
    output:
      report: { to: artifacts/report.json, schema: report }
    on_fail: { to: write, max: 1 }
`
	h := newHarness(t, mustFlow(t, doc))
	var mu sync.Mutex
	var prompts []string
	h.agentScript(map[string]func(fake.RunCall) (ports.AgentResult, error){
		"writer": func(c fake.RunCall) (ports.AgentResult, error) {
			mu.Lock()
			prompts = append(prompts, c.In.Prompt)
			n := len(prompts)
			mu.Unlock()
			if n > 1 {
				h.writeFile(t, c.Env, "artifacts/report.json", `{"passed": 1}`)
			}
			return ports.AgentResult{TurnsUsed: 1}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "structured", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events %v", v.State, eventTypes(h.events(t, runID)))
	}
	if len(prompts) != 2 || !strings.Contains(prompts[1], "declared output(s) not readable: artifacts/report.json") {
		t.Fatalf("the retry was not told what it left out:\n%v", prompts)
	}
}
