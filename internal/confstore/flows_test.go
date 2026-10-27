package confstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"apothecary/internal/builtin"
)

// Two projects, one name, two documents. This is the whole point of the move:
// a stage that runs `make verify` is true in one repository and false in the
// next, so "the flow named X" cannot be one thing.
func TestFlowsAreOwnedPerProject(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	a := seedProject(t, svc)
	b := seedNamedProject(t, svc, "p2")

	const shape = `name: verify
version: 5
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
main:
  - { id: check, type: command, run: %q }
`
	if _, err := svc.SaveFlow(ctx, a.ID, "verify", fmt.Sprintf(shape, "make verify"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveFlow(ctx, b.ID, "verify", fmt.Sprintf(shape, "npm test"), nil); err != nil {
		t.Fatal(err)
	}

	rowA, err := svc.GetFlow(ctx, a.ID, "verify")
	if err != nil || !strings.Contains(rowA.YAML, "make verify") {
		t.Fatalf("project A's verify = %q, %v", rowA.YAML, err)
	}
	rowB, err := svc.GetFlow(ctx, b.ID, "verify")
	if err != nil || !strings.Contains(rowB.YAML, "npm test") {
		t.Fatalf("project B's verify = %q, %v", rowB.YAML, err)
	}
	if rowA.Hash == rowB.Hash {
		t.Error("two different documents must not share a hash")
	}

	// Each project's listing is its own.
	listA, err := svc.ListFlows(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range listA {
		if r.ProjectID != a.ID {
			t.Errorf("project A's listing contains %s/%s", r.ProjectID, r.Name)
		}
	}
	// ...and the overview is everyone's.
	all, err := svc.ListFlows(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < len(listA)+1 {
		t.Errorf("overview has %d flows, want at least %d", len(all), len(listA)+1)
	}

	// Deleting one project's flow leaves the other's alone.
	if err := svc.DeleteFlow(ctx, a.ID, "verify"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetFlow(ctx, b.ID, "verify"); err != nil {
		t.Errorf("deleting A's flow took B's: %v", err)
	}
}

// Copying is how a good flow spreads now that it cannot be shared by
// reference — and the copy is genuinely a copy: editing it never reaches back.
func TestCopyFlowBetweenProjects(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	a := seedProject(t, svc)
	b := seedNamedProject(t, svc, "p2")

	if _, err := svc.CopyFlow(ctx, a.ID, "v1", b.ID, ""); err != nil {
		t.Fatal(err)
	}
	src, _ := svc.GetFlow(ctx, a.ID, "v1")
	dst, err := svc.GetFlow(ctx, b.ID, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if dst.ProjectID != b.ID || dst.YAML != src.YAML || dst.Hash != src.Hash {
		t.Fatalf("copy = %+v, want the same document under project B", dst)
	}

	// Copying onto an existing name refuses rather than silently replacing
	// someone else's flow.
	if _, err := svc.CopyFlow(ctx, a.ID, "v1", b.ID, ""); !errors.Is(err, ErrConflict) {
		t.Errorf("copy onto an existing name = %v, want ErrConflict", err)
	}

	// Editing the copy leaves the original alone.
	edited := strings.Replace(dst.YAML, "build the thing", "build it differently", 1)
	if _, err := svc.SaveFlow(ctx, b.ID, "v1", edited, nil); err != nil {
		t.Fatal(err)
	}
	again, _ := svc.GetFlow(ctx, a.ID, "v1")
	if again.Hash != src.Hash {
		t.Error("editing the copy changed the original")
	}

	// A rename rewrites the document's own name, because the name IS the id.
	renamed, err := svc.CopyFlow(ctx, a.ID, "v1", b.ID, "v1-fork")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "v1-fork" || !strings.Contains(renamed.YAML, "name: v1-fork") {
		t.Fatalf("renamed copy = %q", renamed.YAML)
	}
	// ...and it round-trips: the parser accepts what the rename produced.
	if _, err := svc.GetFlow(ctx, b.ID, "v1-fork"); err != nil {
		t.Fatal(err)
	}
}

// A built-in is a template, not a shared flow: any project resolves it, and
// adopting it (or copying it) gives that project its own editable document.
func TestBuiltinTemplateAdoptedPerProject(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	a := seedProject(t, svc)
	b := seedNamedProject(t, svc, "p2")

	// Nobody owns it yet.
	if _, err := svc.GetFlow(ctx, a.ID, "builder"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unadopted template = %v, want ErrNotFound (the engine layers it in)", err)
	}

	// Copying the template into a project adopts it.
	row, err := svc.CopyFlow(ctx, a.ID, "builder", a.ID, "ours")
	if err != nil {
		t.Fatal(err)
	}
	if row.ProjectID != a.ID || !strings.Contains(row.YAML, "name: ours") {
		t.Fatalf("adopted template = %+v", row)
	}
	// The other project is untouched by that adoption.
	if _, err := svc.GetFlow(ctx, b.ID, "ours"); !errors.Is(err, ErrNotFound) {
		t.Errorf("project B got A's adoption: %v", err)
	}
}

// A template has no source project, so it copies through CopyTemplate: under
// its own name (adoption) or a new one, into any project, and never "onto
// itself" — there is no self to copy onto.
func TestCopyTemplateIntoProject(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	a := seedProject(t, svc)
	b := seedNamedProject(t, svc, "p2")

	// Adopting under the template's own name is the common case, and the one
	// CopyFlow would refuse as a copy onto itself.
	row, err := svc.CopyTemplate(ctx, "builder", a.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, _ := builtin.FlowYAML("builder")
	if row.ProjectID != a.ID || row.Name != "builder" || row.YAML != tmpl {
		t.Fatalf("adopted template = %+v, want the template's document under project A", row)
	}
	// Adopting twice refuses like any copy onto an existing name.
	if _, err := svc.CopyTemplate(ctx, "builder", a.ID, ""); !errors.Is(err, ErrConflict) {
		t.Errorf("second adoption = %v, want ErrConflict", err)
	}
	// A rename rewrites the document's own name, into another project.
	renamed, err := svc.CopyTemplate(ctx, "builder", b.ID, "builder-fork")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ProjectID != b.ID || renamed.Name != "builder-fork" || !strings.Contains(renamed.YAML, "name: builder-fork") {
		t.Fatalf("renamed template copy = %+v", renamed)
	}
	// Project A's adoption is untouched by B's copy.
	if _, err := svc.GetFlow(ctx, b.ID, "builder"); !errors.Is(err, ErrNotFound) {
		t.Errorf("project B got A's adoption: %v", err)
	}
	// Not a template: a clear not-found, not a silent empty document.
	if _, err := svc.CopyTemplate(ctx, "nope", a.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown template = %v, want ErrNotFound", err)
	}
}

// seedNamedProject is seedProject for a second project: the same integrations
// would collide on (port, name), so it makes its own.
func seedNamedProject(t *testing.T, svc *Service, name string) Project {
	t.Helper()
	ctx := context.Background()
	mk := func(port, kind, iname string, sec map[string]string) Integration {
		in, err := svc.CreateIntegration(ctx, Integration{Port: port, Kind: kind, Name: name + "-" + iname, Secrets: sec})
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	repoHost := mk(PortRepository, "gitlab", "gl", map[string]string{"token": "glpat-supersecret99"})
	prov := mk(PortProvisioner, "devcontainer", "dc", map[string]string{"token": "prov-tok"})
	harness := mk(PortHarness, "opencode", "oc", map[string]string{"auth_json": `{"openrouter":{"type":"api","key":"sk-or-abc"}}`})
	p, err := svc.CreateProject(ctx, Project{
		Name: name, RepoAccountID: repoHost.ID, ProvisionerID: prov.ID, HarnessIDs: []string{harness.ID},
		Repo:        RepoConfig{URL: "https://gitlab.com/g/" + name + ".git", Locator: "43"},
		DefaultFlow: "builder",
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
