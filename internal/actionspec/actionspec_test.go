package actionspec

import (
	"apothecary/internal/agentspec"
	"apothecary/internal/ports"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// TestRegistryIntegrity: the registry is compiled-in data the flow parser
// validates against, so a malformed entry is a build-time bug, not a runtime
// surprise. Every action must be nameable, bindable and describable.
func TestRegistryIntegrity(t *testing.T) {
	reg := Registry()
	if len(reg) == 0 {
		t.Fatal("empty action registry")
	}
	seen := map[string]bool{}
	for _, a := range reg {
		if a.Slug == "" || strings.ContainsAny(a.Slug, " /$") {
			t.Errorf("action slug %q is not a plain identifier", a.Slug)
		}
		if seen[a.Slug] {
			t.Errorf("duplicate action slug %q", a.Slug)
		}
		seen[a.Slug] = true
		if a.Title == "" {
			t.Errorf("action %q has no title (the editor's picker shows it)", a.Slug)
		}
		// Ports and settings share the stage's namespace (exactly_one pairs
		// one of each), so a name is unique across all three lists.
		names := map[string]bool{}
		for _, list := range [][]PortSig{a.Inputs, a.Outputs} {
			for _, p := range list {
				if p.Name == "" || names[p.Name] {
					t.Errorf("action %q: bad or duplicate port name %q", a.Slug, p.Name)
				}
				names[p.Name] = true
				if !agentspec.IsFileType(p.Type) {
					t.Errorf("action %q port %q: type %q is not a file type (%v)", a.Slug, p.Name, p.Type, agentspec.FileTypes)
				}
			}
		}
		for _, out := range a.Outputs {
			if out.DefaultPath == "" {
				t.Errorf("action %q output %q has no default path for the editor to prefill", a.Slug, out.Name)
			}
		}
		for _, s := range a.Settings {
			if s.Name == "" || names[s.Name] {
				t.Errorf("action %q: bad or duplicate setting name %q", a.Slug, s.Name)
			}
			names[s.Name] = true
			switch s.Kind {
			case KindText, KindList:
				// A free literal (or a list of them) carries no choices.
				if len(s.Values) != 0 || s.Default != "" {
					t.Errorf("action %q text setting %q carries enum fields", a.Slug, s.Name)
				}
			case KindEnum:
				// A literal choice needs its choices, and a default among them
				// when the stage may leave it out — unless leaving it out is
				// choosing its exactly-one sibling.
				optional := !s.Required && !slices.Contains(a.ExactlyOne, s.Name)
				if len(s.Values) == 0 || (optional && !slices.Contains(s.Values, s.Default)) {
					t.Errorf("action %q enum setting %q: values %v, default %q", a.Slug, s.Name, s.Values, s.Default)
				}
				if s.Default != "" && !slices.Contains(s.Values, s.Default) {
					t.Errorf("action %q enum setting %q: default %q is not among %v", a.Slug, s.Name, s.Default, s.Values)
				}
			default:
				t.Errorf("action %q setting %q: unknown kind %q", a.Slug, s.Name, s.Kind)
			}
		}
		for _, n := range a.ExactlyOne {
			if !names[n] {
				t.Errorf("action %q: exactly_one names %q, which is neither a port nor a setting", a.Slug, n)
			}
		}
	}
	// Registry returns a copy: a caller sorting it must not corrupt the source.
	reg[0].Slug = "mutated"
	if Get("mutated") != nil {
		t.Error("Registry() leaked the backing array")
	}
}

// Every port's Type is a file type the agent side knows — whatever writes an
// action's input declares the same type, so the two vocabularies are one.
func TestInputTypesAreOutputTypes(t *testing.T) {
	for _, a := range Registry() {
		for _, p := range append(slices.Clone(a.Inputs), a.Outputs...) {
			if !slices.Contains(agentspec.FileTypes, p.Type) {
				t.Errorf("action %q port %q: type %q (file types: %v)", a.Slug, p.Name, p.Type, agentspec.FileTypes)
			}
		}
	}
	if got := Get(Commit).Input("message"); got == nil || got.Type != agentspec.TypeCommitMessage {
		t.Errorf("commit's message = %+v", got)
	}
	if got := Get(ReplyReviewComments).Input("replies"); got == nil || got.Type != agentspec.TypeReviewReplies {
		t.Errorf("reply_review_comments' replies = %+v", got)
	}
	if got := Get(PostReview).Input("review"); got == nil || got.Type != agentspec.TypeReviewThreads {
		t.Errorf("post_review's review = %+v", got)
	}
}

func TestCreateMR(t *testing.T) {
	a := Get(CreateMR)
	if a == nil {
		t.Fatalf("%s is not registered", CreateMR)
	}
	if a.Requires != RequiresRepository {
		t.Errorf("requires = %q, want %q", a.Requires, RequiresRepository)
	}
	if !a.Single {
		t.Error("create_mr must be single: a run has one merge request")
	}
	body := a.Input("body")
	if body == nil || !body.Required || body.Type != agentspec.TypeFile || body.DefaultPath == "" {
		t.Errorf("body input = %+v, want a required file", body)
	}
	if a.Input("ghost") != nil {
		t.Error("Input returned a signature for an unknown name")
	}
	// The merge request is the run's (run.mr.url), not a port of the stage.
	if len(a.Outputs) != 0 {
		t.Errorf("create_mr outputs = %+v, want none", a.Outputs)
	}
	if Get("nope") != nil {
		t.Error("Get returned an unregistered action")
	}
	if !strings.Contains(Slugs(), CreateMR) || !strings.Contains(Names(a.Inputs), "body") || Names(a.Outputs) != "none" {
		t.Errorf("error-message helpers: slugs=%q inputs=%q outputs=%q", Slugs(), Names(a.Inputs), Names(a.Outputs))
	}
}

// TestJSONShape: the registry is served to the flow editor, which renders
// port rows and setting controls from these field names.
func TestJSONShape(t *testing.T) {
	b, err := json.Marshal(Get(CreateMR))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"slug":"create_mr"`, `"requires":"repository"`, `"single":true`,
		`"name":"body"`, `"type":"file"`, `"required":true`, `"default_path":"artifacts/mr.md"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("json missing %s:\n%s", want, b)
		}
	}
	b, err = json.Marshal(Get(LoadReviewComments))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"outputs":[{"name":"threads"`, `"type":"mr_threads"`, `"settings":[{"name":"scope"`, `"values":["pending","all"]`, `{"name":"select"`, `"kind":"enum"`, `"values":["human","all"]`, `"default":"human"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("json missing %s:\n%s", want, b)
		}
	}
}

// TestTrackerActions: the ticket_* family writes back to the ticket's
// tracker — remote (no workspace), per-ticket prerequisite, and
// ticket_set_field's "value or text" pair that the parser enforces.
func TestTrackerActions(t *testing.T) {
	for _, slug := range []string{TicketTransition, TicketComment, TicketSetField, TicketLink, TicketLinkBranch} {
		a := Get(slug)
		if a == nil {
			t.Fatalf("%s is not registered", slug)
		}
		if a.Requires != RequiresTracker {
			t.Errorf("%s requires = %q, want %q", slug, a.Requires, RequiresTracker)
		}
		if !a.Remote {
			t.Errorf("%s must be remote: it acts on the tracker, not the workspace", slug)
		}
		if a.Single || a.Subject {
			t.Errorf("%s must be usable anywhere in a flow (single=%v subject=%v)", slug, a.Single, a.Subject)
		}
	}
	tr := Get(TicketTransition)
	if to := tr.Setting("to"); to == nil || to.Kind != KindEnum || to.Required || !slices.Equal(to.Values, Statuses) || to.Default != "" {
		t.Errorf("ticket_transition.to = %+v, want an optional enum over the canonical statuses with no default", to)
	}
	if st := tr.Setting("status"); st == nil || st.Kind != KindText || st.Required {
		t.Errorf("ticket_transition.status = %+v, want an optional text setting", st)
	}
	if !slices.Equal(tr.ExactlyOne, []string{"to", "status"}) {
		t.Errorf("ticket_transition.exactly_one = %v, want to / status", tr.ExactlyOne)
	}
	// The registry's statuses ARE the port's: a flow's `to` is what every
	// adapter's TransitionTo understands, in the same order.
	canonical := ports.CanonicalStatuses()
	if len(canonical) != len(Statuses) {
		t.Fatalf("statuses = %v, ports = %v", Statuses, canonical)
	}
	for i, st := range canonical {
		if string(st) != Statuses[i] {
			t.Errorf("status %d = %q, ports %q", i, Statuses[i], st)
		}
	}
	if body := Get(TicketComment).Input("body"); body == nil || body.Type != agentspec.TypeFile || !body.Required || body.DefaultPath == "" {
		t.Errorf("ticket_comment.body = %+v, want a required file input with a conventional path", body)
	}
	sf := Get(TicketSetField)
	if f := sf.Setting("field"); f == nil || f.Kind != KindText || !f.Required {
		t.Errorf("ticket_set_field.field = %+v", f)
	}
	if v, x := sf.Input("value"), sf.Setting("text"); v == nil || x == nil || v.Type != agentspec.TypeFile || x.Kind != KindText || v.Required || x.Required {
		t.Errorf("ticket_set_field value/text = %+v / %+v: want an optional file and an optional text (exactly one is bound, checked by the parser)", v, x)
	}
	if m := sf.Setting("mode"); m == nil || m.Kind != KindEnum || m.Default != ModeReplace || !slices.Contains(m.Values, ModeAppend) {
		t.Errorf("ticket_set_field.mode = %+v", m)
	}
	if l := Get(TicketLink); l.Needs != NeedsMR || len(l.Inputs) != 0 || len(l.Settings) != 0 {
		t.Errorf("ticket_link = %+v: wants needs=%s and no ports or settings", l, NeedsMR)
	}
	// The branch link needs no merge request: it is what a ticket shows
	// before one exists.
	if l := Get(TicketLinkBranch); l.Needs != "" || len(l.Inputs) != 0 || len(l.Settings) != 0 {
		t.Errorf("ticket_link_branch = %+v: wants no needs, ports or settings", l)
	}
	b, err := json.Marshal(Get(TicketSetField))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"requires":"tracker"`, `"remote":true`, `"kind":"text"`, `"default":"replace"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("json missing %s:\n%s", want, b)
		}
	}
	if b, _ := json.Marshal(Get(TicketLink)); !strings.Contains(string(b), `"needs":"merge_request"`) {
		t.Errorf("ticket_link json lacks needs:\n%s", b)
	}
}

// mr_merge's methods are the port's: the registry stays free of ports, and
// this holds the two equal.
func TestMergeMethodsMirrorThePort(t *testing.T) {
	act := Get(MRMerge)
	if act == nil || !act.Subject || !act.Remote || act.Requires != RequiresRepository {
		t.Fatalf("mr_merge = %+v", act)
	}
	want := []string{ports.MergeMethodMerge, ports.MergeMethodSquash, ports.MergeMethodRebase}
	if got := act.Setting("method").Values; !slices.Equal(got, want) {
		t.Errorf("methods = %v, want %v", got, want)
	}
	if pr := Get(PostReview); pr == nil || !pr.Subject || pr.Input("review") == nil || !pr.Input("review").Required {
		t.Errorf("post_review = %+v", pr)
	}
}
