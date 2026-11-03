package engine

// Smaller refusals and renderings, each a contract someone reads: a form's
// answer, a verdict file, the transcript of an agent's turn, a command that
// dies in a way its script did not choose.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

func TestNormalizeFormRefusals(t *testing.T) {
	many := func(n int) []event.Field {
		out := make([]event.Field, n)
		for i := range out {
			out[i] = event.Field{ID: "f" + strings.Repeat("x", i), Type: event.FieldString}
		}
		return out
	}
	opts := make([]event.Option, maxFieldOptions+1)
	for i := range opts {
		opts[i] = event.Option{ID: strings.Repeat("o", i+1)}
	}
	cases := []struct {
		name string
		f    event.Form
		want string
	}{
		{"none", event.Form{}, "a form needs at least one field"},
		{"too many", event.Form{Fields: many(maxFormFields + 1)}, "at most 20 fields (got 21)"},
		{"a long title", event.Form{Fields: []event.Field{{ID: "a", Type: event.FieldString, Title: strings.Repeat("t", 501)}}}, "a title is at most 500 chars"},
		{"a long description", event.Form{Fields: []event.Field{{ID: "a", Type: event.FieldString, Description: strings.Repeat("d", 2001)}}}, "a description 2000"},
		{"too many options", event.Form{Fields: []event.Field{{ID: "a", Type: event.FieldString, Options: opts}}}, "at most 50 options (got 51)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := c.f
			wantErrText(t, normalizeForm(&f), c.want)
		})
	}
}

func TestValidateFormChecksEachFieldsConstraints(t *testing.T) {
	five, two := 5, 2
	lo, hi := 1.0, 10.0
	cases := []struct {
		name string
		fd   event.Field
		v    any
		want string // "" = accepted
	}{
		{"too long", event.Field{Type: event.FieldString, MaxLength: &two}, "abc", "at most 2 characters"},
		{"too short", event.Field{Type: event.FieldString, MinLength: &five}, "abc", "at least 5 characters"},
		{"not text", event.Field{Type: event.FieldString}, 7.0, "expected text"},
		{"off the pattern", event.Field{Type: event.FieldString, Pattern: "^a"}, "b", "does not match ^a"},
		{"not an email", event.Field{Type: event.FieldString, Format: "email"}, "nope", "not a valid email"},
		{"an email", event.Field{Type: event.FieldString, Format: "email"}, "a@b.test", ""},
		{"no scheme", event.Field{Type: event.FieldString, Format: "uri"}, "example.test/x", "not a valid uri"},
		{"a uri", event.Field{Type: event.FieldString, Format: "uri"}, "https://example.test/x", ""},
		{"not a date", event.Field{Type: event.FieldString, Format: "date"}, "01/02/2026", "not a valid date"},
		{"a date", event.Field{Type: event.FieldString, Format: "date"}, "2026-02-01", ""},
		{"not a date-time", event.Field{Type: event.FieldString, Format: "date-time"}, "2026-02-01", "not a valid date-time"},
		{"a date-time", event.Field{Type: event.FieldString, Format: "date-time"}, "2026-02-01T10:00:00Z", ""},
		{"not a number", event.Field{Type: event.FieldNumber}, "7", "expected a number"},
		{"below the minimum", event.Field{Type: event.FieldNumber, Minimum: &lo}, 0.5, "at least 1"},
		{"above the maximum", event.Field{Type: event.FieldNumber, Maximum: &hi}, 11.0, "at most 10"},
		{"not whole", event.Field{Type: event.FieldInteger}, 1.5, "expected a whole number"},
		{"not a yes or no", event.Field{Type: event.FieldBoolean}, "yes", "expected yes or no"},
		{"not a list", event.Field{Type: event.FieldArray, Options: []event.Option{{ID: "a"}}}, "a", "expected a list of options"},
		{"too few picks", event.Field{Type: event.FieldArray, Options: []event.Option{{ID: "a"}, {ID: "b"}}, MinItems: &two}, []any{"a"}, "pick at least 2"},
		{"too many picks", event.Field{Type: event.FieldArray, Options: []event.Option{{ID: "a"}, {ID: "b"}}, MaxItems: new(int)}, []any{"a"}, "pick at most 0"},
		{"a repeat is one pick", event.Field{Type: event.FieldArray, Options: []event.Option{{ID: "a"}, {ID: "b"}}, MinItems: &two}, []any{"a", "a"}, "pick at least 2"},
		{"an unknown type", event.Field{Type: "color"}, "red", `unknown field type "color"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.fd.ID = "f"
			ans := PingAnswer{Answer: event.FormAccept, Content: map[string]any{"f": c.v}}
			err := validateForm(&event.Form{Fields: []event.Field{c.fd}}, &ans)
			if c.want == "" {
				if err != nil {
					t.Errorf("err = %v", err)
				}
				return
			}
			wantErrText(t, err, c.want)
		})
	}
}

func TestValidateFormAcceptAndDecline(t *testing.T) {
	form := &event.Form{Fields: []event.Field{{ID: "a", Type: event.FieldString, Required: true}, {ID: "b", Type: event.FieldString}}}
	accept := func(c map[string]any) *PingAnswer { return &PingAnswer{Answer: event.FormAccept, Content: c} }

	wantErrText(t, validateForm(nil, accept(nil)), "this question has no form to accept")
	wantErrText(t, validateForm(form, &PingAnswer{Answer: "maybe"}), "answered accept")
	wantErrText(t, validateForm(form, accept(map[string]any{})), "a: an answer is required")
	// Blank and null are not answers.
	wantErrText(t, validateForm(form, accept(map[string]any{"a": "   "})), "a: an answer is required")
	wantErrText(t, validateForm(form, accept(map[string]any{"a": nil})), "a: an answer is required")
	wantErrText(t, validateForm(form, accept(map[string]any{"a": "x", "z": "y"})), `"z" is not one of the form's fields`)

	ans := accept(map[string]any{"a": "  x  ", "b": ""})
	if err := validateForm(form, ans); err != nil {
		t.Fatal(err)
	}
	if ans.Content["a"] != "x" {
		t.Errorf("not tidied: %v", ans.Content)
	}
	if _, ok := ans.Content["b"]; ok {
		t.Errorf("a blank field kept: %v", ans.Content)
	}
}

func TestComposeFormTextOfEachValue(t *testing.T) {
	one := &event.Form{Fields: []event.Field{{ID: "a", Type: event.FieldBoolean}}}
	if got := composeFormText(one, event.FormAccept, "", map[string]any{"a": true}); got != "yes" {
		t.Errorf("yes = %q", got)
	}
	if got := composeFormText(one, event.FormAccept, "", map[string]any{"a": false}); got != "no" {
		t.Errorf("no = %q", got)
	}
	if got := composeFormText(one, event.FormAccept, "", map[string]any{"a": 7}); got != "7" {
		t.Errorf("default = %q", got)
	}
	if got := composeFormText(one, event.FormAccept, "why not", map[string]any{"a": true}); got != "yes\n(note: why not)" {
		t.Errorf("noted = %q", got)
	}
	if got := composeFormText(one, event.FormDecline, "no idea", nil); !strings.Contains(got, "and said: no idea") {
		t.Errorf("decline = %q", got)
	}
	two := &event.Form{Fields: []event.Field{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}}
	got := composeFormText(two, event.FormAccept, "", map[string]any{"a": 1.5, "c": []any{}, "d": []any{"x", "y"}})
	want := "a: 1.5\nb: (no answer)\nc: none selected\nd: selected: x, y"
	if got != want {
		t.Errorf("many = %q, want %q", got, want)
	}
}

func TestParseVerdictRefusals(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"not json", `{`, "not valid JSON"},
		{"wrong shape", `[1]`, "is not a verdict"},
		{"an unknown word", `{"verdict":"MAYBE"}`, `verdict must be APPROVE or REJECT, got "MAYBE"`},
		{"an empty remark", `{"verdict":"REJECT","comments":["  "]}`, "comment 1 is empty"},
		{"a comment of neither shape", `{"verdict":"REJECT","comments":[7]}`, "comment 1 is neither a remark nor {path, line, body}"},
		{"an empty body", `{"verdict":"REJECT","comments":[{"path":"a.go","line":1,"body":" "}]}`, "comment 1 is empty"},
		{"outside the repository", `{"verdict":"REJECT","comments":[{"path":"../a.go","line":1,"body":"x"}]}`, `path "../a.go" must be a file of the repository`},
		{"a range backwards", `{"verdict":"REJECT","comments":[{"path":"a.go","line":5,"end_line":2,"body":"x"}]}`, "lines 5–2 are not a range"},
		{"an end with no start", `{"verdict":"REJECT","comments":[{"path":"a.go","end_line":2,"body":"x"}]}`, "are not a range"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseVerdict("artifacts/v.json", []byte(c.doc))
			wantErrText(t, err, c.want)
		})
	}
	v, err := parseVerdict("artifacts/v.yaml", []byte("verdict: APPROVE\nsummary: '  fine  '\ncomments:\n  - looks good\n  - { path: a.go, line: 3, body: nit }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !v.Approve || v.Summary != "fine" || len(v.Comments) != 2 || v.Comments[1].Path != "a.go" {
		t.Errorf("verdict = %+v", v)
	}
}

func TestFormatMessages(t *testing.T) {
	cases := []struct {
		msgs []agentport.UserMessage
		want string
	}{
		{nil, ""},
		{[]agentport.UserMessage{{Text: "one"}}, "Message from the human operator:\n\none"},
		{[]agentport.UserMessage{{Text: "one"}, {Text: "two"}}, "Messages from the human operator:\n\n1. one\n\n2. two\n"},
		{[]agentport.UserMessage{{Text: "composed", Verbatim: true}}, "composed"},
		{[]agentport.UserMessage{{Text: "composed", Verbatim: true}, {Text: "one"}}, "Message from the human operator:\n\none\n\ncomposed"},
	}
	for _, c := range cases {
		if got := formatMessages(c.msgs); got != c.want {
			t.Errorf("formatMessages = %q, want %q", got, c.want)
		}
	}
}

// What an agent's turn leaves on the record: its notices and the tools it
// ran, secrets out of all of them.
func TestTranscriptRecordsWhatTheAgentDid(t *testing.T) {
	const token = "sk-test" // the harness's provider key
	h := newHarness(t, mustFlow(t, askFlowDoc))
	big := strings.Repeat("x", maxMessageText+1)
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		emit := call.In.Events
		emit(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s1"})
		emit(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Text: "reading " + token})
		emit(agentport.AgentEvent{Kind: agentport.AgentEvPermission, Text: "granted"})
		emit(agentport.AgentEvent{Kind: agentport.AgentEvPermission, Text: "denied", Tool: &agentport.AgentTool{Title: "rm -rf /"}})
		emit(agentport.AgentEvent{Kind: agentport.AgentEvTool, Tool: &agentport.AgentTool{ID: "t1", Title: "Edit a.go", Kind: "edit", Status: "in_progress",
			Input: `{"key":"` + token + `"}`}})
		emit(agentport.AgentEvent{Kind: agentport.AgentEvTool, Tool: &agentport.AgentTool{ID: "t1", Status: "completed", Input: `{"second":"` + token + `"}`,
			Locations: []string{"a.go:3"},
			Diffs:     []agentport.AgentDiff{{Path: "a.go", OldText: "old " + token, NewText: "new " + token}}}})
		emit(agentport.AgentEvent{Kind: agentport.AgentEvNotice, Text: "the agent restarted with " + token})
		emit(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Text: big})
		emit(agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: "t", Error: "rate limited"})
		emit(agentport.AgentEvent{Kind: agentport.AgentEvUserMessage, Messages: []agentport.UserMessage{{Text: "engine text", Verbatim: true}}})
		return agentport.AgentResult{TurnsUsed: 1, Session: "s1"}, nil
	}
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	evs := h.events(t, id)
	var notices []string
	for _, ev := range evs {
		if n, ok := ev.Payload.(*event.AgentNotice); ok {
			notices = append(notices, n.Text)
		}
	}
	joined := strings.Join(notices, "\n")
	for _, want := range []string{"Permission requested for a tool call: granted", "Permission requested for rm -rf /: denied",
		"the agent restarted with", "The turn ended with an error: rate limited"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notices = %q, want %q", joined, want)
		}
	}
	tool, ok := findPayload[*event.AgentTool](evs)
	if !ok {
		t.Fatalf("no tool: %v", eventTypes(evs))
	}
	if tool.Status != "completed" || len(tool.Locations) != 1 || len(tool.Diffs) != 1 || tool.Diffs[0].Path != "a.go" {
		t.Errorf("tool = %+v", tool)
	}
	blob := joined + tool.Input + tool.Diffs[0].OldText + tool.Diffs[0].NewText
	cut := false
	for _, ev := range evs {
		if m, ok := ev.Payload.(*event.AgentMessage); ok {
			blob += m.Text
			cut = cut || strings.HasSuffix(m.Text, "… [truncated]")
		}
	}
	if !cut {
		t.Error("a message past the limit was not cut")
	}
	if strings.Contains(blob, token) {
		t.Errorf("a secret reached the log: %q", blob)
	}
	if countType(evs, event.TypeMessageDelivered) != 0 {
		t.Error("engine-composed text recorded as a delivered message")
	}
}

// A stage's run_ref deleted from the project: running "" would exit 0 and
// silently pass whatever it guards.
func TestACommandDeletedFromTheProjectFailsTheRun(t *testing.T) {
	h := newHarness(t, mustFlow(t, `name: loop
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: first, type: command, run: "./first.sh" }
  - { id: test, type: command, run_ref: verify, on_success: done }
`))
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "./first.sh") {
			delete(h.res.Commands, "verify")
		}
		return envport.ExecResult{}, nil
	}
	id, sf := runOut(t, h, runinput.Ticket{}, "loop", runinput.Input{})
	wantFailure(t, sf, event.ReasonInfra, `run_ref "verify" no longer exists`)
	if s := h.view(t, id).State; s != run.Failed {
		t.Errorf("state = %s", s)
	}
}

// A command the workspace could not run is the infrastructure's failure; one
// that ran out of time is the stage's, routed by on_timeout.
func TestCommandsThatDieOutsideTheirScript(t *testing.T) {
	t.Run("infrastructure", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, `name: loop
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: c, type: command, run: "./c.sh", on_fail: { to: c, max: 3 } }
`))
		h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
			if strings.Contains(cmd.Script, "./c.sh") {
				return envport.ExecResult{}, errors.New("container gone")
			}
			return envport.ExecResult{}, nil
		}
		id, sf := runOut(t, h, runinput.Ticket{}, "loop", runinput.Input{})
		wantFailure(t, sf, event.ReasonInfra, "container gone")
		if s := h.view(t, id).State; s != run.Failed {
			t.Errorf("state = %s: an infrastructure failure is not retried", s)
		}
	})
	t.Run("a timeout", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, `name: loop
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: c, type: command, run: "./c.sh", timeout: 1ms, on_timeout: end }
`))
		h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
			if strings.Contains(cmd.Script, "./c.sh") {
				return envport.ExecResult{}, context.DeadlineExceeded
			}
			return envport.ExecResult{}, nil
		}
		id, sf := runOut(t, h, runinput.Ticket{}, "loop", runinput.Input{})
		wantFailure(t, sf, event.ReasonTimeout, "")
		if edge, ok := findPayload[*event.EdgeTaken](h.events(t, id)); !ok || edge.Edge != "on_timeout" {
			t.Errorf("edge = %+v", edge)
		}
	})
	t.Run("an abort mid-command", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, `name: loop
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: c, type: command, run: "./c.sh" }
`))
		entered, aborted := make(chan struct{}), make(chan struct{})
		h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
			if strings.Contains(cmd.Script, "./c.sh") {
				close(entered)
				<-aborted // the command dies of the abort, never ahead of it
				return envport.ExecResult{}, errors.New("signal: killed")
			}
			return envport.ExecResult{}, nil
		}
		id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
		if err != nil {
			t.Fatal(err)
		}
		<-entered
		if err := h.e.Abort(id); err != nil {
			t.Fatal(err)
		}
		close(aborted)
		h.e.Wait(id)
		evs := h.events(t, id)
		if s := run.Reduce(evs).State; s != run.Abandoned {
			t.Errorf("state = %s", s)
		}
		if sf, ok := findPayload[*event.StageFailed](evs); ok {
			t.Errorf("an abort read as the command's failure: %+v", sf)
		}
	})
}

// What a prompt says about a multi-repository run, and about a repository it
// could not read.
func TestPromptSectionsOfCheckouts(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	st := replayFor("env-1")
	st.Flow = h.flows["loop"]
	st.Checkouts = []event.Checkout{{Name: "repo", Dir: "repo", Source: "main", Primary: true}, {Name: "web", Dir: "repos/web", Source: "dev"}}
	x := &exec{e: h.e, ctx: context.Background(), res: h.res, rs: st}
	env, err := h.envp.Provision(context.Background(), envport.EnvSpec{})
	if err != nil {
		t.Fatal(err)
	}
	st.EnvID = env
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		switch {
		case strings.Contains(cmd.Script, "git diff") && cmd.Dir == "repos/web":
			return envport.ExecResult{ExitCode: 128}, nil
		case strings.Contains(cmd.Script, "git diff"):
			return envport.ExecResult{Output: "M\ta.go\n"}, nil
		case strings.Contains(cmd.Script, "git status") && cmd.Dir == "repos/web":
			return envport.ExecResult{}, errors.New("gone")
		}
		return envport.ExecResult{Output: " \n"}, nil
	}
	want := "## repo (~/repo)\nM\ta.go\n## web (~/repos/web)\n(no diff available)\n"
	if got := x.diffTarget(); got != want {
		t.Errorf("diffTarget = %q, want %q", got, want)
	}
	if got := x.diffPhase(); got != want {
		t.Errorf("diffPhase = %q", got)
	}
	want = "## repo (~/repo)\n(nothing uncommitted)\n## web (~/repos/web)\n(no status available)\n"
	if got := x.statusFiles(); got != want {
		t.Errorf("statusFiles = %q, want %q", got, want)
	}
}

func TestFilesListing(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	env, err := h.envp.Provision(context.Background(), envport.EnvSpec{})
	if err != nil {
		t.Fatal(err)
	}
	st := replayFor(env)
	x := &exec{e: h.e, ctx: context.Background(), res: h.res, rs: st}
	if got := x.filesListing("artifacts/shots/"); got != "(no files)\n" {
		t.Errorf("empty = %q", got)
	}
	for i := range promptFilesListed + 2 {
		h.writeFile(t, env, "artifacts/shots/"+strings.Repeat("a", i+1)+".txt", "x")
	}
	got := x.filesListing("artifacts/shots/")
	if !strings.HasSuffix(got, "… and 2 more\n") || strings.Count(got, "\n") != promptFilesListed+1 {
		t.Errorf("listing = %q", got)
	}
	for n, want := range map[int64]string{12: "12 B", 2048: "2.0 KB", 3 << 20: "3.0 MB"} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q", n, got)
		}
	}
}

// An agent prints what it prints. A NUL or broken UTF-8 in its output is refused
// by Postgres, and a refused append leaves the run stuck in `running`: the text
// has to reach the store clean, and the run has to end.
func TestBinaryAgentOutputDoesNotStrandTheRun(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		emit := call.In.Events
		emit(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s1"})
		emit(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Text: "said\x00this"})
		emit(agentport.AgentEvent{Kind: agentport.AgentEvTool, Tool: &agentport.AgentTool{ID: "t1", Title: "cat a\xff.bin", Status: "completed",
			Input: `{"path":"a\u0000"}`, Output: "binary\x00output", Locations: []string{"a\x00.bin"}}})
		return agentport.AgentResult{TurnsUsed: 1, Session: "s1"}, nil
	}
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	if v := h.view(t, id); !v.State.Terminal() {
		t.Fatalf("the run was left %s", v.State)
	}
	tool, ok := findPayload[*event.AgentTool](h.events(t, id))
	if !ok || tool.Output != "binary�output" || tool.Title != "cat a�.bin" {
		t.Errorf("tool = %+v", tool)
	}
}

// A ticket's text comes from outside too, by the engine's other way to the log.
func TestBinaryTicketTextDoesNotRefuseTheRun(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T\x00", Goal: "g\xff"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	created, ok := findPayload[*event.RunCreated](h.events(t, id))
	if !ok || created.TicketTitle != "T\uFFFD" || created.TicketGoal != "g\uFFFD" {
		t.Errorf("created = %+v", created)
	}
}
