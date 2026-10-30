package flow

import (
	"encoding/json"
	"strings"
	"testing"
)

// portsDoc is a small document whose agents read and write through ports:
// a writer of a plan, a reader of it, and a reviewer whose verdict routes.
const portsDoc = `name: t
version: 5
parameters: { harness: fake, model: fake/model }
schemas:
  - { name: report, json_schema: { type: object, required: [passed] } }
agents:
  - name: writer
    prompt: write the plan
    inputs:
      - { name: ticket, type: ticket }
      - { name: failure, type: failure, optional: true }
    outputs:
      - { name: plan, type: file }
  - name: reader
    prompt: read the plan
    inputs:
      - { name: plan, type: file }
      - { name: rejection, type: verdict, optional: true }
  - name: reviewer
    prompt: review
    inputs:
      - { name: changes, type: changeset }
    outputs:
      - { name: verdict, type: verdict }
main:
`

const (
	writeStage = `  - id: write
    type: agent
    harness: $harness
    model: $model
    agent: writer
    input:
      ticket:  { from: run.ticket, type: ticket }
      failure: { from: edge.failure, type: failure, optional: true }
    output:
      plan: { to: artifacts/plan.md, type: file }
    on_fail: { to: write, max: 1 }
`
	readStage = `  - id: read
    type: agent
    harness: $harness
    model: $model
    agent: reader
    input:
      plan: { from: artifacts/plan.md, type: file }
`
)

func parsePorts(t *testing.T, stages string) error {
	t.Helper()
	_, err := Parse("t.yaml", []byte(portsDoc+stages))
	return err
}

func TestPortsParse(t *testing.T) {
	f, err := Parse("t.yaml", []byte(portsDoc+writeStage+readStage))
	if err != nil {
		t.Fatal(err)
	}
	w := f.Stage("write")
	if in := w.Input("ticket"); in == nil || in.Value != SourceTicket || in.Type != "ticket" {
		t.Errorf("ticket port = %+v", in)
	}
	if in := w.Input("failure"); in == nil || !in.Optional || in.Value != SourceFailure {
		t.Errorf("failure port = %+v", in)
	}
	if out := w.Output("plan"); out == nil || out.Value != "artifacts/plan.md" || out.Type != "file" {
		t.Errorf("plan port = %+v", out)
	}
	if got := f.Stage("read").Input("plan"); got == nil || got.Value != "artifacts/plan.md" {
		t.Errorf("read's plan = %+v", got)
	}
	for _, w := range f.Warnings {
		if !strings.Contains(w, `schema "report"`) { // portsDoc declares one for other tests
			t.Errorf("warning: %s", w)
		}
	}
}

// What a port says by itself — its shape, its source, its destination — is
// checked as the stage is read, with the fix in the message.
func TestPortShapeRules(t *testing.T) {
	cmd := func(in, out string) string {
		s := "  - id: c\n    type: command\n    run: \"true\"\n"
		if in != "" {
			s += "    input:\n      " + in + "\n"
		}
		if out != "" {
			s += "    output:\n      " + out + "\n"
		}
		return s
	}
	tests := []struct {
		name, stages, want string
	}{
		{"a bare value", cmd("plan: artifacts/plan.md", ""), `a port is a mapping — plan: { from: artifacts/plan.md, type: … }`},
		{"a retired sentinel", writeStage + "  - id: r\n    type: agent\n    harness: $harness\n    model: $model\n    agent: reader\n    input: { plan: { from: artifacts/plan.md, type: file }, rejection: $DECISION }\n",
			`$DECISION is now edge.verdict`},
		{"an action literal written as a port", "  - id: m\n    type: action\n    action: ticket_transition\n    input: { to: done }\n",
			`or, for an action's literal setting, with: { to: done }`},
		{"an unknown key", cmd("plan: { from: repo:a.md, type: file, kind: x }", ""), `unknown key "kind"`},
		{"an input with to", cmd("plan: { to: a.md, type: file }", ""), `an input comes from somewhere: from:`},
		{"an output with from", cmd("", "plan: { from: a.md, type: file }"), `an output goes somewhere: to:`},
		{"an optional output", cmd("", "plan: { to: a.md, type: file, optional: true }"), `optional is an input's`},
		{"no shape", cmd("plan: { from: repo:a.md }", ""), `type or schema is required`},
		{"an unknown type", cmd("plan: { from: repo:a.md, type: pdf }", ""), `unknown type "pdf"`},
		{"file beside a schema", cmd("plan: { from: repo:a.json, type: file, schema: report }", ""), `the schema alone says what the file is`},
		{"an undeclared schema", cmd("plan: { from: repo:a.json, schema: nope }", ""), `schema "nope" is not declared`},
		{"a structured path", cmd("plan: { from: repo:a.md, schema: report }", ""), `must end in .json, .yaml or .yml`},
		{"a value type from a file", cmd("plan: { from: repo:a.md, type: ticket }", ""), `a ticket is a value the engine provides, not a file: from: run.ticket`},
		{"a value type written", cmd("", "plan: { to: a.md, type: changeset }"), `what is written is a file`},
		{"an engine source mistyped", readStage[:0] + "  - id: r\n    type: agent\n    harness: $harness\n    model: $model\n    agent: writer\n    input: { ticket: { from: run.ticket, type: file } }\n    output: { plan: { to: a.md, type: file } }\n",
			`run.ticket is a ticket — type: ticket`},
		{"an edge source not optional", "  - id: r\n    type: agent\n    harness: $harness\n    model: $model\n    agent: writer\n    input: { ticket: { from: run.ticket, type: ticket }, failure: { from: edge.failure, type: failure } }\n    output: { plan: { to: a.md, type: file } }\n    on_fail: { to: r, max: 1 }\n",
			`mark the port optional: true`},
		{"a command reading the engine", cmd("t: { from: run.ticket, type: ticket }", ""), `a command reads files; run.ticket is a value the engine provides`},
		{"a gate shown a changeset", "  - id: g\n    type: human_gate\n    input: { c: { from: run.git.status, type: changeset } }\n", `a gate shows a file or a link`},
		{"a destination in the repo namespace", cmd("", "plan: { to: repo:a.md, type: file }"), `must be a repo-relative path (no repo:`},
		{"a parameter as a source", cmd("plan: { from: $plan, type: file }", ""), `is a parameter, and this field reads none`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parsePorts(t, tt.stages)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want error containing %q, got %v", tt.want, err)
			}
		})
	}
}

// An agent stage states the agent's whole contract, in shapes that satisfy it.
func TestAgentPortContract(t *testing.T) {
	tests := []struct {
		name, stages, want string
	}{
		{"an output left out", "  - id: w\n    type: agent\n    harness: $harness\n    model: $model\n    agent: writer\n    input: { ticket: { from: run.ticket, type: ticket } }\n",
			`agent "writer" writes "plan" — state where: output: { plan: { to: …, type: file } }`},
		{"a required input left out", writeStage + "  - id: r\n    type: agent\n    harness: $harness\n    model: $model\n    agent: reader\n",
			`agent "reader" requires input "plan"`},
		{"a port the agent lacks", writeStage + "  - id: r\n    type: agent\n    harness: $harness\n    model: $model\n    agent: reader\n    input: { plan: { from: artifacts/plan.md, type: file }, extra: { from: repo:x.md, type: file } }\n",
			`input "extra" is not an input of agent "reader" (inputs: plan, rejection)`},
		{"an output of another type", "  - id: w\n    type: agent\n    harness: $harness\n    model: $model\n    agent: writer\n    input: { ticket: { from: run.ticket, type: ticket } }\n    output: { plan: { to: a.json, type: verdict } }\n",
			""},
		{"an on_reject with no verdict port", writeStage + "  - id: r\n    type: agent\n    harness: $harness\n    model: $model\n    agent: reader\n    input: { plan: { from: artifacts/plan.md, type: file } }\n    on_reject: { to: write, max: 1 }\n",
			`on_reject can never fire: agent "reader" writes no verdict`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parsePorts(t, tt.stages)
			if tt.want == "" {
				// A verdict is a file: it satisfies the agent's `file`. The
				// stage narrows, it never contradicts.
				if err != nil {
					t.Fatalf("narrowing a file output was refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want error containing %q, got %v", tt.want, err)
			}
		})
	}
}

// Every input agrees with what feeds it.
func TestPortSources(t *testing.T) {
	tests := []struct {
		name, stages, want string
	}{
		{"nothing writes it", readStage, `nothing in this flow writes "artifacts/plan.md" — a file of the repository is from: repo:artifacts/plan.md`},
		{"a mistyped engine source", "  - id: c\n    type: command\n    run: \"true\"\n    input: { t: { from: run.tiket, type: file } }\n",
			""},
		{"repo: on a file the flow writes", writeStage + "  - id: r\n    type: agent\n    harness: $harness\n    model: $model\n    agent: reader\n    input: { plan: { from: repo:artifacts/plan.md, type: file } }\n",
			`stage "write" writes artifacts/plan.md — read it as from: artifacts/plan.md`},
		{"a writer of another shape", "  - id: a\n    type: command\n    run: \"true\"\n    output: { r: { to: artifacts/r.json, type: file } }\n  - id: b\n    type: command\n    run: \"true\"\n    input: { r: { from: artifacts/r.json, type: verdict } }\n",
			`stage "a" writes "artifacts/r.json" as a file; the port reads a verdict`},
		{"two writers disagree", "  - id: a\n    type: command\n    run: \"true\"\n    output: { r: { to: artifacts/r.json, type: file } }\n  - id: b\n    type: command\n    run: \"true\"\n    output: { r: { to: artifacts/r.json, type: verdict } }\n  - id: c\n    type: command\n    run: \"true\"\n    input: { r: { from: artifacts/r.json, type: file } }\n",
			`both write "artifacts/r.json", as a file and as a verdict`},
		{"not written before it is read", readStage + writeStage,
			`"artifacts/plan.md" is not written on every route reaching this stage (written by "write")`},
		{"an edge source nothing fills", writeStage + "  - id: r\n    type: agent\n    harness: $harness\n    model: $model\n    agent: reader\n    input: { plan: { from: artifacts/plan.md, type: file }, rejection: { from: edge.verdict, type: verdict, optional: true } }\n",
			`no on_reject edge enters stage "r", nor the success of a gate or a verdict, so edge.verdict is never filled`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parsePorts(t, tt.stages)
			if tt.want == "" {
				if err == nil || !strings.Contains(err.Error(), "did you mean run.ticket?") {
					t.Fatalf("want a suggestion of run.ticket, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestPortSourcesAccept(t *testing.T) {
	for name, stages := range map[string]string{
		// An optional reader does without a writer that has not run yet.
		"an optional input read early": "  - id: c\n    type: command\n    run: \"true\"\n    input: { plan: { from: artifacts/plan.md, type: file, optional: true } }\n" + writeStage,
		// A file of the repository needs no writer.
		"a repository file": "  - id: c\n    type: command\n    run: \"true\"\n    input: { spec: { from: repo:docs/spec.md, type: file } }\n",
		// A reviewer's REJECT carries its decision to the stage it routes to.
		"a decision carried by on_reject": writeStage + `  - id: r
    type: agent
    harness: $harness
    model: $model
    agent: reader
    input:
      plan:      { from: artifacts/plan.md, type: file }
      rejection: { from: edge.verdict, type: verdict, optional: true }
  - id: review
    type: agent
    harness: $harness
    model: $model
    agent: reviewer
    input: { changes: { from: run.git.diff_target, type: changeset } }
    output: { verdict: { to: artifacts/verdict.json, type: verdict } }
    on_reject: { to: r, max: 2 }
`,
		// Several writers of one path, one shape: the reader gets the latest.
		"two writers agree": writeStage + "  - id: c\n    type: command\n    run: \"true\"\n    output: { plan: { to: artifacts/plan.md, type: file } }\n" + readStage,
		// A watcher reads what the main chain wrote.
		"a watcher reads the main chain": writeStage + `  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/plan.md, type: file } } }
watchers:
  - id: fix
    on: mr_conflicts
    stages:
      - id: r
        type: agent
        harness: $harness
        model: $model
        agent: reader
        input: { plan: { from: artifacts/plan.md, type: file } }
outcomes:
  - { id: merged, on: mr_merged, then: done }
`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := parsePorts(t, stages); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// An action's contract is the registry's: its ports, typed; its literals
// under with:.
func TestActionPorts(t *testing.T) {
	tests := []struct {
		name, stages, want string
	}{
		{"a setting given as a port", "  - id: m\n    type: action\n    action: ticket_transition\n    input: { to: { from: repo:a.md, type: file } }\n",
			`input "to": "to" is a setting, a literal — with: { to: … }`},
		{"a port given as a setting", "  - id: m\n    type: action\n    action: create_mr\n    with: { body: artifacts/mr.md }\n",
			`with "body": "body" is a file the action reads — a port: input: { body: { from: …, type: file } }`},
		{"an unknown setting", "  - id: m\n    type: action\n    action: sync_target\n    with: { strategi: merge }\n",
			`with "strategi" is not a setting of action "sync_target" (settings: strategy)`},
		{"a setting out of its enum", "  - id: m\n    type: action\n    action: sync_target\n    with: { strategy: squash }\n",
			`with "strategy": "squash" is not one of merge | rebase`},
		{"a required input left out", "  - id: m\n    type: action\n    action: create_mr\n",
			`action "create_mr" requires input "body"`},
		{"an output left out", "  - id: l\n    type: action\n    action: load_review_comments\n",
			`action "load_review_comments" writes "threads" — state where: output: { threads: { to: artifacts/threads.yaml, type: mr_threads } }`},
		{"a typed input of another type", writeStage + "  - id: c\n    type: action\n    action: commit\n    input: { message: { from: artifacts/plan.md, type: file } }\n",
			`input "message": action "commit" reads a commit_message, the port says a file`},
		{"with on an agent", writeStage[:len(writeStage)-1] + "\n    with: { to: done }\n",
			`field "with" does not apply to type agent`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parsePorts(t, tt.stages)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want error containing %q, got %v", tt.want, err)
			}
		})
	}
	f, err := Parse("t.yaml", []byte(portsDoc+"  - id: m\n    type: action\n    action: sync_target\n    with: { strategy: rebase }\n  - { id: p, type: action, action: push_branch }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Stage("m").Setting("strategy", "merge"); got != "rebase" {
		t.Errorf("strategy = %q", got)
	}
	if got := f.Stage("p").Setting("strategy", "merge"); got != "merge" {
		t.Errorf("an unset setting is its default, got %q", got)
	}
}

// The editor receives ports in the order written, each as the object the
// YAML says, and the canonical document parses back to the same ports.
func TestDocPortsJSON(t *testing.T) {
	doc, err := ParseDoc("t.yaml", []byte(portsDoc+writeStage))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(doc.Main[0].Input)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ticket":{"from":"run.ticket","type":"ticket"},"failure":{"from":"edge.failure","type":"failure","optional":true}}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
	var back DocPorts
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Pairs) != 2 || back.Pairs[0].Name != "ticket" || !back.Pairs[1].Port.Optional {
		t.Errorf("round trip = %+v", back.Pairs)
	}
}
