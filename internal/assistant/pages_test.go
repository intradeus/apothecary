package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const goodFlow = `name: tiny
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: builder
    prompt: build the thing
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
`

func check(t *testing.T, k Kind, fill string) (string, []string) {
	t.Helper()
	out, errs := pages[k].Check(context.Background(), fakeChecks{}, "u1", "w1", json.RawMessage(fill))
	return string(out), errs
}

func TestFlowFillIsTheParsersToAccept(t *testing.T) {
	yaml, _ := json.Marshal(map[string]string{"yaml": goodFlow})
	if out, errs := check(t, KindFlow, string(yaml)); len(errs) > 0 || !strings.Contains(out, "name: tiny") {
		t.Errorf("a good flow = %s %v", out, errs)
	}
	for name, fill := range map[string]string{
		"not YAML":      `{"yaml":"name: ["}`,
		"empty":         `{"yaml":"  "}`,
		"no stages":     `{"yaml":"name: x\nversion: 5\n"}`,
		"another field": `{"yaml":"x","flow":"y"}`,
		"not an object": `"x"`,
		"too large":     `{"yaml":"` + strings.Repeat("a", maxFill) + `"}`,
	} {
		if _, errs := check(t, KindFlow, fill); len(errs) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTicketFill(t *testing.T) {
	out, errs := check(t, KindTicket, `{"title":"Fix the login page","goal":"It works on Safari.","change_type":"fix","type_id":"bug"}`)
	if len(errs) > 0 || !strings.Contains(out, `"change_type":"fix"`) {
		t.Errorf("a good ticket = %s %v", out, errs)
	}
	// Only what is set comes back: the page leaves the rest alone.
	if out, _ := check(t, KindTicket, `{"goal":"only the goal"}`); out != `{"goal":"only the goal"}` {
		t.Errorf("a goal alone = %s", out)
	}
	for name, fill := range map[string]string{
		"nothing":            `{}`,
		"two lines of title": `{"title":"a\nb"}`,
		"an empty title":     `{"title":" "}`,
		"a change type":      `{"change_type":"rewrite"}`,
		"a type elsewhere":   `{"type_id":"epic"}`,
		"a label":            `{"labels":["a"]}`,
		"an unknown field":   `{"priority":"high"}`,
	} {
		if _, errs := check(t, KindTicket, fill); len(errs) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSkillFill(t *testing.T) {
	md := "---\nname: release-notes\ndescription: Write release notes from the merged changes.\n---\n\nRead the merged changes, then write notes.\n"
	fill, _ := json.Marshal(map[string]any{"name": "release-notes", "files": []map[string]string{{"path": "SKILL.md", "content": md}}})
	if _, errs := check(t, KindSkill, string(fill)); len(errs) > 0 {
		t.Errorf("a good skill = %v", errs)
	}
	bad, _ := json.Marshal(map[string]any{"name": "other-name", "files": []map[string]string{{"path": "SKILL.md", "content": md}}})
	if _, errs := check(t, KindSkill, string(bad)); len(errs) == 0 {
		t.Error("a name that differs from SKILL.md's was accepted")
	}
	if _, errs := check(t, KindSkill, `{"name":"x","files":[]}`); len(errs) == 0 {
		t.Error("a skill with no SKILL.md was accepted")
	}
	if _, errs := check(t, KindSkill, `{"files":"x"}`); len(errs) == 0 {
		t.Error("a malformed skill was accepted")
	}
	if _, errs := (Page{Kind: "x"}).Check(context.Background(), nil, "", "", json.RawMessage(`{}`)); len(errs) == 0 {
		t.Error("a page that is not one accepted a fill")
	}
}

func TestPageContext(t *testing.T) {
	c := pages[KindFlow].Context(json.RawMessage(`{"yaml":"name: x","errors":["no stages"],"params":{"model":"acme/one","harness":"fake"}}`))
	if !strings.Contains(c, "```yaml\nname: x\n```") || !strings.Contains(c, "no stages") || !strings.Contains(c, "harness=fake model=acme/one") {
		t.Errorf("flow context = %q", c)
	}
	if c := pages[KindTicket].Context(json.RawMessage(`{"title":"t"}`)); !strings.Contains(c, `"title": "t"`) {
		t.Errorf("ticket context = %q", c)
	}
	if pages[KindSkill].Context(nil) != "" || pages[KindSkill].Context(json.RawMessage("null")) != "" {
		t.Error("no page state is not nothing")
	}
	big := pages[KindTicket].Context(json.RawMessage(`{"goal":"` + strings.Repeat("a", maxContext) + `"}`))
	if len(big) > maxContext+100 || !strings.HasSuffix(big, "cut here]\n") {
		t.Errorf("a large page is %d bytes", len(big))
	}
	for _, k := range Kinds {
		p, ok := PageOf(k)
		if !ok || len(p.Presets) == 0 || p.FillSchema["type"] != "object" || !strings.Contains(p.Instructions, "fill") {
			t.Errorf("page %s = %+v", k, p)
		}
	}
	if !Runs("claude-code") || !Runs("opencode") || Runs("omp") || Runs("codex") {
		t.Error("the harnesses the assistant runs on")
	}
}
