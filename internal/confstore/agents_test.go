package confstore

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// seedAgentEnv builds a service over Mem with two projects and a default
// flow referencing agents planner+scribe on project A (scribe is NOT a
// built-in name, so the delete guard has something to guard).
func seedAgentEnv(t *testing.T) (*Service, Project, Project) {
	t.Helper()
	svc, _ := newSvc(t)
	flowYAML := `name: f
defaults: { harness: fake, model: fake/fast-1 }
stages:
  - { id: plan,  type: agent, agent: planner, output: artifacts/plan.md }
  - { id: build, type: agent, agent: scribe }
`
	if _, err := svc.SaveFlow(context.Background(), "f", flowYAML, nil); err != nil {
		t.Fatal(err)
	}
	mkInt := func(port, name string) string {
		in, err := svc.CreateIntegration(context.Background(), Integration{Port: port, Kind: "fake", Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	f, c, h := mkInt(PortForge, "f"), mkInt(PortProvisioner, "c"), mkInt(PortHarness, "h")
	mkProj := func(name string) Project {
		p, err := svc.CreateProject(context.Background(), Project{
			Name: name, ForgeID: f, ProvisionerID: c, HarnessIDs: []string{h},
			Repo:        RepoConfig{URL: "https://forge.local/x.git"},
			Workspace:   WorkspaceConfig{Template: "t"},
			DefaultFlow: "f",
		})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	return svc, mkProj("a"), mkProj("b")
}

func TestAgentSaveHashAndVersions(t *testing.T) {
	svc, pa, _ := seedAgentEnv(t)
	ctx := context.Background()

	a, err := svc.SaveAgent(ctx, Agent{ProjectID: pa.ID, Name: "planner", Prompt: "Plan v1."})
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash == "" || len(a.Hash) != 16 {
		t.Fatalf("hash = %q", a.Hash)
	}
	// Same content = same hash, no new version.
	a2, _ := svc.SaveAgent(ctx, Agent{ProjectID: pa.ID, Name: "planner", Prompt: "Plan v1."})
	if a2.Hash != a.Hash {
		t.Errorf("hash changed on identical save: %s vs %s", a.Hash, a2.Hash)
	}
	// Changed prompt = new hash + new version.
	a3, _ := svc.SaveAgent(ctx, Agent{ProjectID: pa.ID, Name: "planner", Prompt: "Plan v2."})
	if a3.Hash == a.Hash {
		t.Error("hash must change with the prompt")
	}
	vs, err := svc.AgentVersions(ctx, pa.ID, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("versions = %d, want 2", len(vs))
	}
	v, err := svc.AgentVersion(ctx, a.Hash)
	if err != nil || !strings.Contains(string(v.Spec), "Plan v1.") {
		t.Errorf("version %s = %s, %v", a.Hash, v.Spec, err)
	}

	// Validation: bad slug, empty prompt, mask-shaped nothing (agents have
	// no secrets), missing project.
	if _, err := svc.SaveAgent(ctx, Agent{ProjectID: pa.ID, Name: "Bad Name", Prompt: "x"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad name err = %v", err)
	}
	if _, err := svc.SaveAgent(ctx, Agent{ProjectID: pa.ID, Name: "ok", Prompt: "  "}); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty prompt err = %v", err)
	}
	if _, err := svc.SaveAgent(ctx, Agent{ProjectID: "nope", Name: "ok", Prompt: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing project err = %v", err)
	}
}

func TestAgentDeleteGuardedByDefaultFlow(t *testing.T) {
	svc, pa, _ := seedAgentEnv(t)
	ctx := context.Background()
	if _, err := svc.SaveAgent(ctx, Agent{ProjectID: pa.ID, Name: "planner", Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveAgent(ctx, Agent{ProjectID: pa.ID, Name: "scribe", Prompt: "w"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveAgent(ctx, Agent{ProjectID: pa.ID, Name: "spare", Prompt: "s"}); err != nil {
		t.Fatal(err)
	}
	// scribe is referenced by the default flow and has NO built-in fallback:
	// deletion would break the flow, so it is refused.
	err := svc.DeleteAgent(ctx, pa.ID, "scribe")
	if !errors.Is(err, ErrReferenced) || !strings.Contains(err.Error(), "default flow f") {
		t.Errorf("guarded delete err = %v", err)
	}
	// planner is referenced too, but shadows a built-in: deleting the
	// override falls back to the compiled-in default — always allowed.
	if err := svc.DeleteAgent(ctx, pa.ID, "planner"); err != nil {
		t.Errorf("builtin-shadow delete err = %v", err)
	}
	if err := svc.DeleteAgent(ctx, pa.ID, "spare"); err != nil {
		t.Errorf("unreferenced delete err = %v", err)
	}
	// Versions survive deletion (immutable history).
	spareHash := AgentHash(Agent{Name: "spare", Prompt: "s"})
	if _, err := svc.AgentVersion(ctx, spareHash); err != nil {
		t.Errorf("version gone after delete: %v", err)
	}
}

func TestAgentCopyAcrossProjects(t *testing.T) {
	svc, pa, pb := seedAgentEnv(t)
	ctx := context.Background()
	for _, n := range []string{"planner", "builder"} {
		if _, err := svc.SaveAgent(ctx, Agent{ProjectID: pa.ID, Name: n, Prompt: "prompt " + n}); err != nil {
			t.Fatal(err)
		}
	}
	copied, err := svc.CopyAgents(ctx, pb.ID, pa.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(copied) != 2 {
		t.Fatalf("copied = %d, want 2", len(copied))
	}
	got, _ := svc.GetAgent(ctx, pb.ID, "planner")
	src, _ := svc.GetAgent(ctx, pa.ID, "planner")
	if got.Prompt != src.Prompt || got.Hash != src.Hash {
		t.Errorf("copy diverged: %+v vs %+v", got, src)
	}
	// Identical content shares the content-addressed version row.
	if vs, _ := svc.AgentVersions(ctx, pb.ID, "planner"); len(vs) != 1 {
		t.Errorf("versions after copy = %d, want 1 (shared)", len(vs))
	}
	// Collision refuses the whole copy.
	if _, err := svc.CopyAgents(ctx, pb.ID, pa.ID, []string{"planner"}); !errors.Is(err, ErrConflict) {
		t.Errorf("collision err = %v", err)
	}
	// Unknown source agent.
	if _, err := svc.CopyAgents(ctx, pb.ID, pa.ID, []string{"ghost"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown agent err = %v", err)
	}
	// Same project refused.
	if _, err := svc.CopyAgents(ctx, pa.ID, pa.ID, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("self copy err = %v", err)
	}
}
