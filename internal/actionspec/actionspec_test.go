package actionspec

import (
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
			case KindEnum:
				// A literal choice needs its choices, and a default among them
				// when the stage may leave it unbound.
				if len(in.Values) == 0 || (!in.Required && !slices.Contains(in.Values, in.Default)) {
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
