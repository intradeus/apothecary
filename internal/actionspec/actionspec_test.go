package actionspec

import (
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
		names := map[string]bool{}
		for _, in := range a.Inputs {
			if in.Name == "" || names[in.Name] {
				t.Errorf("action %q: bad or duplicate input name %q", a.Slug, in.Name)
			}
			names[in.Name] = true
			switch in.Kind {
			case KindFile, KindValue:
			case KindText:
				// A free literal carries neither a path nor choices.
				if in.DefaultPath != "" || len(in.Values) != 0 {
					t.Errorf("action %q text input %q carries file/enum fields", a.Slug, in.Name)
				}
			case KindEnum:
				// A literal choice needs its choices, and a default among them
				// when the stage may leave it unbound — unless leaving it
				// unbound is choosing its exactly-one sibling.
				optional := !in.Required && !slices.Contains(a.ExactlyOne, in.Name)
				if len(in.Values) == 0 || (optional && !slices.Contains(in.Values, in.Default)) {
					t.Errorf("action %q enum input %q: values %v, default %q", a.Slug, in.Name, in.Values, in.Default)
				}
			default:
				t.Errorf("action %q input %q: unknown kind %q", a.Slug, in.Name, in.Kind)
			}
		}
		outs := map[string]bool{}
		for _, out := range a.Outputs {
			if out.Name == "" || outs[out.Name] {
				t.Errorf("action %q: bad or duplicate output name %q", a.Slug, out.Name)
			}
			outs[out.Name] = true
			if out.Kind == KindFile && out.Path == "" {
				t.Errorf("action %q file output %q has no path", a.Slug, out.Name)
			}
			// A value output is only consumable if it names its sentinel.
			if out.Kind == KindValue && !strings.HasPrefix(out.Sentinel, "$") {
				t.Errorf("action %q output %q: value outputs need a $SENTINEL, got %q", a.Slug, out.Name, out.Sentinel)
			}
		}
	}
	// Registry returns a copy: a caller sorting it must not corrupt the source.
	reg[0].Slug = "mutated"
	if Get("mutated") != nil {
		t.Error("Registry() leaked the backing array")
	}
}

func TestCreateMR(t *testing.T) {
	a := Get(CreateMR)
	if a == nil {
		t.Fatalf("%s is not registered", CreateMR)
	}
	if a.Requires != RequiresForge {
		t.Errorf("requires = %q, want %q", a.Requires, RequiresForge)
	}
	if !a.Single {
		t.Error("create_mr must be single: a run has one merge request")
	}
	body := a.Input("body")
	if body == nil || !body.Required || body.Kind != KindFile {
		t.Errorf("body input = %+v, want a required file", body)
	}
	if a.Input("ghost") != nil {
		t.Error("Input returned a signature for an unknown name")
	}
	out := a.Output("mr_url")
	if out == nil || out.Kind != KindValue || out.Sentinel != "$MR_URL" {
		t.Errorf("mr_url output = %+v", out)
	}
	if Get("nope") != nil {
		t.Error("Get returned an unregistered action")
	}
	if !strings.Contains(Slugs(), CreateMR) || !strings.Contains(a.InputNames(), "body") {
		t.Errorf("error-message helpers: slugs=%q inputs=%q", Slugs(), a.InputNames())
	}
}

// TestJSONShape: the registry is served to the flow editor, which renders
// binding rows from these field names.
func TestJSONShape(t *testing.T) {
	b, err := json.Marshal(Get(CreateMR))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"slug":"create_mr"`, `"requires":"forge"`, `"single":true`,
		`"name":"body"`, `"kind":"file"`, `"required":true`, `"sentinel":"$MR_URL"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("json missing %s:\n%s", want, b)
		}
	}
}

// TestTrackerActions: the ticket_* family writes back to the ticket's
// tracker — remote (no workspace), per-ticket prerequisite, and
// ticket_set_field's "value or text" pair that the parser enforces.
func TestTrackerActions(t *testing.T) {
	for _, slug := range []string{TicketTransition, TicketComment, TicketSetField, TicketLink} {
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
	if to := tr.Input("to"); to == nil || to.Kind != KindEnum || to.Required || !slices.Equal(to.Values, Statuses) || to.Default != "" {
		t.Errorf("ticket_transition.to = %+v, want an optional enum over the canonical statuses with no default", to)
	}
	if st := tr.Input("status"); st == nil || st.Kind != KindText || st.Required {
		t.Errorf("ticket_transition.status = %+v, want an optional text input", st)
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
	if body := Get(TicketComment).Input("body"); body == nil || body.Kind != KindFile || !body.Required || body.DefaultPath == "" {
		t.Errorf("ticket_comment.body = %+v, want a required file input with a conventional path", body)
	}
	sf := Get(TicketSetField)
	if f := sf.Input("field"); f == nil || f.Kind != KindText || !f.Required {
		t.Errorf("ticket_set_field.field = %+v", f)
	}
	if v, x := sf.Input("value"), sf.Input("text"); v == nil || x == nil || v.Kind != KindFile || x.Kind != KindText || v.Required || x.Required {
		t.Errorf("ticket_set_field value/text = %+v / %+v: want an optional file and an optional text (exactly one is bound, checked by the parser)", v, x)
	}
	if m := sf.Input("mode"); m == nil || m.Kind != KindEnum || m.Default != ModeReplace || !slices.Contains(m.Values, ModeAppend) {
		t.Errorf("ticket_set_field.mode = %+v", m)
	}
	if l := Get(TicketLink); l.Needs != NeedsMR || len(l.Inputs) != 0 {
		t.Errorf("ticket_link = %+v: wants needs=%s and no inputs", l, NeedsMR)
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
