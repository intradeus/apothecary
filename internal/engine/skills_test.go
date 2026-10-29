package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"apothecary/internal/event"
	"apothecary/internal/ports"
	"apothecary/internal/run"
	"apothecary/internal/skills"
)

// fakeSkillLib is the engine's SkillStore for scenarios: sets by id with a
// head version and member skill names, trees by skill name.
type fakeSkillLib struct {
	owner string
	sets  map[string]*fakeSet
	trees map[string]skills.Tree
	fail  error
}

type fakeSet struct {
	name   string
	head   int
	skills []string
}

func (f *fakeSkillLib) ResolveSkillsets(_ context.Context, userID string, refs []skills.Ref) ([]event.SkillsetPin, error) {
	var pins []event.SkillsetPin
	seen := map[string]bool{}
	for _, r := range refs {
		if seen[r.ID] {
			continue // the real store collapses duplicates to the first
		}
		seen[r.ID] = true
		set, ok := f.sets[r.ID]
		if !ok || userID != f.owner {
			return nil, fmt.Errorf("skillset %s: not found", r.ID)
		}
		seq := r.Seq
		if seq == 0 {
			seq = set.head
		}
		pins = append(pins, event.SkillsetPin{ID: r.ID, Name: set.name, Seq: seq, Hash: fmt.Sprintf("hash-%d", seq)})
	}
	return pins, nil
}

func (f *fakeSkillLib) SkillTrees(_ context.Context, pins []event.SkillsetPin) ([]skills.Tree, error) {
	if f.fail != nil {
		return nil, f.fail
	}
	seen := map[string]bool{}
	var out []skills.Tree
	for _, p := range pins {
		for _, n := range f.sets[p.ID].skills {
			if !seen[n] {
				seen[n] = true
				out = append(out, f.trees[n])
			}
		}
	}
	return out, nil
}

func newSkillLib() *fakeSkillLib {
	tree := func(name string) skills.Tree {
		return skills.Tree{Name: name, Files: []ports.TreeFile{
			{Path: "SKILL.md", Data: []byte("---\nname: " + name + "\ndescription: d\n---\n")},
			{Path: "scripts/run.sh", Mode: 0o755, Data: []byte("echo")},
		}}
	}
	return &fakeSkillLib{
		sets: map[string]*fakeSet{
			"set1": {name: "One", head: 2, skills: []string{"alpha"}},
			"set2": {name: "Two", head: 1, skills: []string{"alpha", "beta"}},
		},
		trees: map[string]skills.Tree{"alpha": tree("alpha"), "beta": tree("beta")},
	}
}

// installRecorder stands in for Resolution.InstallSkills.
type installRecorder struct {
	mu    sync.Mutex
	calls [][]string
}

func (r *installRecorder) fn(_ context.Context, _ ports.EnvID, names []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string(nil), names...))
	return nil
}

func (r *installRecorder) get() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.calls...)
}

func skillsResetScript() string {
	return "rm -rf '/apothecary/skills' && mkdir -p '/apothecary/skills'"
}

func (h *harness) execScripts() []string {
	var out []string
	for _, c := range h.envp.Execs() {
		out = append(out, c.Cmd.Script)
	}
	return out
}

// A run that pins skillsets gets them written once to the neutral tree,
// with a manifest, and installed into every harness; the pins ride
// run_created and the view.
func TestRunInstallsPinnedSkills(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	lib := newSkillLib()
	h.e.SetSkills(lib)
	rec := &installRecorder{}
	h.res.InstallSkills = rec.fn
	ctx := context.Background()

	id, err := h.e.CreateRunWith(ctx, Ticket{Title: "t"}, "loop", testProject,
		RunInput{Skillsets: []skills.Ref{{ID: "set1"}, {ID: "set2", Seq: 1}, {ID: "set1"}}})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	v := h.view(t, id)
	if v.State != run.Completed {
		t.Fatalf("state = %s", v.State)
	}
	want := []event.SkillsetPin{{ID: "set1", Name: "One", Seq: 2, Hash: "hash-2"}, {ID: "set2", Name: "Two", Seq: 1, Hash: "hash-1"}}
	if fmt.Sprint(v.Skillsets) != fmt.Sprint(want) {
		t.Errorf("view pins = %+v, want %+v", v.Skillsets, want)
	}
	env := ports.EnvID(v.EnvID)
	md, err := h.envp.ReadFile(ctx, env, "/apothecary/skills/alpha/SKILL.md")
	if err != nil || !strings.Contains(string(md), "name: alpha") {
		t.Errorf("alpha SKILL.md = %q, %v", md, err)
	}
	if _, err := h.envp.ReadFile(ctx, env, "/apothecary/skills/beta/scripts/run.sh"); err != nil {
		t.Errorf("beta script: %v", err)
	}
	manifest, err := h.envp.ReadFile(ctx, env, "/apothecary/skills/manifest.json")
	if err != nil || !strings.Contains(string(manifest), `"set2"`) || !strings.Contains(string(manifest), `"beta"`) {
		t.Errorf("manifest = %s, %v", manifest, err)
	}
	if n := len(h.envp.Trees); n != 2 {
		t.Errorf("trees written = %d, want 2 (alpha once, though two sets list it)", n)
	}
	if got := rec.get(); len(got) != 1 || strings.Join(got[0], ",") != "alpha,beta" {
		t.Errorf("installed = %v, want one call with alpha,beta", got)
	}
	found := false
	for _, s := range h.execScripts() {
		if s == skillsResetScript() {
			found = true
		}
	}
	if !found {
		t.Error("bootstrap must reset /apothecary/skills before writing")
	}
}

// No skillsets: the neutral directory is still reset (a re-bootstrap must
// not keep an older selection), nothing is written or installed.
func TestRunWithoutSkillsResetsOnly(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.e.SetSkills(newSkillLib())
	rec := &installRecorder{}
	h.res.InstallSkills = rec.fn
	id := completedWithEnv(t, h)
	if v := h.view(t, id); len(v.Skillsets) != 0 {
		t.Errorf("pins = %+v", v.Skillsets)
	}
	if len(h.envp.Trees) != 0 || len(rec.get()) != 0 {
		t.Errorf("trees=%d installs=%d, want none", len(h.envp.Trees), len(rec.get()))
	}
	found := false
	for _, s := range h.execScripts() {
		if s == skillsResetScript() {
			found = true
		}
	}
	if !found {
		t.Error("the reset exec must run even with no skills")
	}
}

// An unknown (or another user's) skillset refuses the run at preflight; a
// server without a library refuses any request that names one.
func TestRunRefusesUnknownSkillset(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()
	_, err := h.e.CreateRunWith(ctx, Ticket{Title: "t"}, "loop", testProject, RunInput{Skillsets: []skills.Ref{{ID: "set1"}}})
	if err == nil || !strings.Contains(err.Error(), "skills are not available") {
		t.Errorf("no library: err = %v", err)
	}
	h.e.SetSkills(newSkillLib())
	_, err = h.e.CreateRunWith(ctx, Ticket{Title: "t"}, "loop", testProject, RunInput{Skillsets: []skills.Ref{{ID: "nope"}}})
	if err == nil || !strings.Contains(err.Error(), "skillset nope") {
		t.Errorf("unknown: err = %v", err)
	}
}

// A rerun carries the original's skillsets re-pinned to their latest; an
// explicit empty list clears them.
func TestRerunRepinsSkillsets(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	lib := newSkillLib()
	h.e.SetSkills(lib)
	ctx := context.Background()
	id, err := h.e.CreateRunWith(ctx, Ticket{Title: "t"}, "loop", testProject, RunInput{Skillsets: []skills.Ref{{ID: "set2", Seq: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	lib.sets["set2"].head = 3

	again, err := h.e.Rerun(ctx, id, RunInput{})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(again)
	if v := h.view(t, again); len(v.Skillsets) != 1 || v.Skillsets[0].Seq != 3 {
		t.Errorf("rerun pins = %+v, want set2 at v3", v.Skillsets)
	}
	none, err := h.e.Rerun(ctx, again, RunInput{Skillsets: []skills.Ref{}})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(none)
	if v := h.view(t, none); len(v.Skillsets) != 0 {
		t.Errorf("cleared rerun pins = %+v", v.Skillsets)
	}
}

// A rebuild lays the pinned skills down again in the fresh container.
func TestRebuildReinstallsSkills(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.e.SetSkills(newSkillLib())
	rec := &installRecorder{}
	h.res.InstallSkills = rec.fn
	ctx := context.Background()
	id, err := h.e.CreateRunWith(ctx, Ticket{Title: "t"}, "loop", testProject, RunInput{Skillsets: []skills.Ref{{ID: "set1"}}})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	h.res.Envp = &rebuilderEnvp{EnvProvisioner: h.envp}
	if err := h.e.RebuildEnv(ctx, id, false); err != nil {
		t.Fatalf("RebuildEnv: %v", err)
	}
	if got := rec.get(); len(got) != 2 || strings.Join(got[1], ",") != "alpha" {
		t.Errorf("installs = %v, want a second call after rebuild", got)
	}
	if len(h.envp.Trees) != 2 {
		t.Errorf("trees = %d, want the tree written twice", len(h.envp.Trees))
	}
}

// Skill bytes that cannot be assembled fail the workspace like any other
// bootstrap failure.
func TestSkillTreeFailureFailsEnv(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	lib := newSkillLib()
	lib.fail = errors.New("content is no longer stored")
	h.e.SetSkills(lib)
	ctx := context.Background()
	id, err := h.e.CreateRunWith(ctx, Ticket{Title: "t"}, "loop", testProject, RunInput{Skillsets: []skills.Ref{{ID: "set1"}}})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	v := h.view(t, id)
	if v.State == run.Completed || !v.State.Terminal() {
		t.Errorf("state = %s, want a failed run", v.State)
	}
	if !hasEvent(h.events(t, id), event.TypeEnvFailed) {
		t.Error("want env_failed on the log")
	}
}
