package httpapi

// The guard covers ids in a PATH. These are ids in a BODY: the same rule, one
// person's rows are nobody else's to reach, checked where the body is read.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"apothecary/internal/engine"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/confstore/conftest"
	"apothecary/internal/skills/skillstore"
)

// makeProject builds a project with its own integrations for whoever hdr
// signs in, over the API.
func (x *confEnv) makeProject(t *testing.T, name string, hdr map[string]string) string {
	t.Helper()
	mk := func(port string) string {
		res, data := x.do(t, "POST", "/api/integrations", map[string]any{"port": port, "kind": "fake", "name": name + "-" + port}, hdr)
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("integration %s = %d %s", port, res.StatusCode, data)
		}
		return decode[integrationJSON](t, data).ID
	}
	res, data := x.do(t, "POST", "/api/projects", projectBody{
		Name: name, RepoAccountID: mk("repository"), ProvisionerID: mk("provisioner"), HarnessIDs: []string{mk("harness")},
		Repo:        confstore.RepoConfig{URL: "https://git.local/" + name + ".git", Locator: "1", DefaultBranch: "main"},
		DefaultFlow: "builder",
	}, hdr)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("project = %d %s", res.StatusCode, data)
	}
	return decode[confstore.Project](t, data).ID
}

// A project cannot be built on someone else's integrations: those are their
// credentials.
func TestProjectCannotBorrowAnIntegration(t *testing.T) {
	x := newConfEnv(t)
	bob := x.addUser(t, "bob", false)
	project := x.makeProject(t, "mine", nil)
	p, err := x.conf.GetProject(t.Context(), project)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(port string) string {
		_, data := x.do(t, "POST", "/api/integrations", map[string]any{"port": port, "kind": "fake", "name": "b-" + port}, bob)
		return decode[integrationJSON](t, data).ID
	}
	repo, prov, harness := mk("repository"), mk("provisioner"), mk("harness")
	for name, body := range map[string]projectBody{
		"repo_account_id": {RepoAccountID: p.RepoAccountID, ProvisionerID: prov, HarnessIDs: []string{harness}},
		"provisioner_id":  {RepoAccountID: repo, ProvisionerID: p.ProvisionerID, HarnessIDs: []string{harness}},
		"harness_ids":     {RepoAccountID: repo, ProvisionerID: prov, HarnessIDs: p.HarnessIDs},
	} {
		body.Name, body.DefaultFlow = "bobs", "builder"
		body.Repo = confstore.RepoConfig{URL: "https://git.local/b.git", Locator: "1"}
		res, data := x.do(t, "POST", "/api/projects", body, bob)
		if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), name) {
			t.Errorf("project on someone's %s = %d %s, want 400", name, res.StatusCode, data)
		}
	}
	// An edit is held to the same rule as a create.
	bobs := x.makeProject(t, "bobs", bob)
	res, data := x.do(t, "PUT", "/api/projects/"+bobs, projectBody{
		Name: "bobs", RepoAccountID: p.RepoAccountID, ProvisionerID: prov, HarnessIDs: []string{harness}, DefaultFlow: "builder",
		Repo: confstore.RepoConfig{URL: "https://git.local/b.git", Locator: "1"},
	}, bob)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("edit onto someone's integration = %d %s, want 400", res.StatusCode, data)
	}
}

// Nor can a harness integration point at someone else's provider credential.
func TestIntegrationCannotBorrowACredential(t *testing.T) {
	x := newConfEnv(t)
	bob := x.addUser(t, "bob", false)
	res, data := x.do(t, "POST", "/api/credentials", map[string]any{"provider": "openrouter", "key": "sk-or-x"}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("credential = %d %s", res.StatusCode, data)
	}
	cred := decode[confstore.ProviderCredential](t, data).ID
	res, data = x.do(t, "POST", "/api/integrations", map[string]any{
		"port": "harness", "kind": "fake", "name": "h", "config": map[string]any{confstore.ConfigCredentialIDs: []string{cred}},
	}, bob)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "not found") {
		t.Errorf("harness on someone's credential = %d %s, want 400", res.StatusCode, data)
	}
	// Nor by editing one that was created clean.
	res, data = x.do(t, "POST", "/api/integrations", map[string]any{"port": "harness", "kind": "fake", "name": "h"}, bob)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("clean harness = %d %s", res.StatusCode, data)
	}
	id := decode[integrationJSON](t, data).ID
	res, data = x.do(t, "PUT", "/api/integrations/"+id, map[string]any{"name": "h", "config": map[string]any{confstore.ConfigCredentialIDs: []string{cred}}}, bob)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("edit onto someone's credential = %d %s, want 400", res.StatusCode, data)
	}
}

// A run installs its owner's skillsets, never one named by id from someone
// else's library.
func TestRunCannotInstallSomeoneElsesSkillset(t *testing.T) {
	skl := newTestSkills(t, nil)
	x := newConfEnvWith(t, skl.deps)
	x.srv.Skills = skl.lib
	x.reserve(t)
	bob := x.addUser(t, "bob", false)
	res, data := x.do(t, "POST", "/api/skills", skillBody("pdf", "Reads PDFs."), nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("skill = %d %s", res.StatusCode, data)
	}
	res, data = x.do(t, "POST", "/api/skillsets", SkillsetRequest{Name: "Mine", SkillIDs: []string{decode[skillstore.Skill](t, data).ID}}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("set = %d %s", res.StatusCode, data)
	}
	set := decode[skillstore.Skillset](t, data)
	project := x.makeProject(t, "bobs", bob)
	res, data = x.do(t, "POST", "/api/tickets", map[string]any{"title": "t", "project_id": project}, bob)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("ticket = %d %s", res.StatusCode, data)
	}
	ticket := decode[confstore.Ticket](t, data).ID
	res, data = x.do(t, "POST", "/api/tickets/"+ticket+"/runs", map[string]any{"skillsets": []map[string]any{{"id": set.ID}}}, bob)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("run with someone's skillset = %d %s, want 400", res.StatusCode, data)
	}
}

// A tracker integration is a person's credentials too: an import names it in
// the body, past the guard, and must still be the importer's own.
func TestImportCannotBorrowATracker(t *testing.T) {
	x := newConfEnv(t)
	x.srv.Sources = x.resolver
	bob := x.addUser(t, "bob", false)
	project := x.makeProject(t, "bobs", bob)
	res, data := x.do(t, "POST", "/api/integrations", map[string]any{"port": "ticketsource", "kind": "fake", "name": "mine"}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("tracker = %d %s", res.StatusCode, data)
	}
	tracker := decode[integrationJSON](t, data).ID

	// DEV-1 is an issue the fake tracker really holds.
	res, data = x.do(t, "POST", "/api/tickets", map[string]any{"project_id": project, "source_id": tracker, "source_key": "DEV-1"}, bob)
	if res.StatusCode != http.StatusNotFound && res.StatusCode != http.StatusBadRequest {
		t.Errorf("import through someone's tracker = %d %s, want it refused", res.StatusCode, data)
	}
	if strings.Contains(string(data), "Farewell") {
		t.Errorf("the tracker's issue reached another user: %s", data)
	}
}

// An automation polls its source with its owner's credentials on a schedule,
// so it must be the project owner's to name.
func TestAutomationCannotBorrowATracker(t *testing.T) {
	x := newConfEnv(t)
	bob := x.addUser(t, "bob", false)
	project := x.makeProject(t, "bobs", bob)
	res, data := x.do(t, "POST", "/api/integrations", map[string]any{"port": "ticketsource", "kind": "fake", "name": "mine"}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("tracker = %d %s", res.StatusCode, data)
	}
	tracker := decode[integrationJSON](t, data).ID

	res, data = x.do(t, "POST", "/api/projects/"+project+"/automations", map[string]any{
		"name": "steal", "kind": "tracker_query", "source_id": tracker, "query": "project = SECRET", "flow": "builder",
	}, bob)
	if res.StatusCode != http.StatusBadRequest && res.StatusCode != http.StatusNotFound {
		t.Errorf("automation on someone's tracker = %d %s, want it refused", res.StatusCode, data)
	}
}

// A ticket stays in a project its owner owns: an edit is a body too.
func TestTicketCannotMoveIntoSomeoneElsesProject(t *testing.T) {
	x := newConfEnv(t)
	bob := x.addUser(t, "bob", false)
	mine := x.makeProject(t, "mine", nil)
	theirs := x.makeProject(t, "theirs", bob)
	res, data := x.do(t, "POST", "/api/tickets", map[string]any{"title": "t", "project_id": theirs}, bob)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("ticket = %d %s", res.StatusCode, data)
	}
	ticket := decode[confstore.Ticket](t, data).ID

	res, data = x.do(t, "PUT", "/api/tickets/"+ticket, map[string]any{"title": "t", "project_id": mine}, bob)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("moving a ticket into someone's project = %d %s, want 404", res.StatusCode, data)
	}
	if got, err := x.conf.GetTicket(t.Context(), ticket); err != nil || got.ProjectID != theirs || got.UserID != "bob" {
		t.Errorf("ticket after the attempt = %+v, %v", got, err)
	}
	if _, data := x.do(t, "GET", "/api/tickets", nil, nil); strings.Contains(string(data), ticket) {
		t.Errorf("the ticket landed in my list: %s", data)
	}
}

// Moving a ticket between one's own projects is fine, and an edit never
// changes who owns it.
func TestTicketEditKeepsItsOwner(t *testing.T) {
	x := newConfEnv(t)
	first, second := x.makeProject(t, "first", nil), x.makeProject(t, "second", nil)
	res, data := x.do(t, "POST", "/api/tickets", map[string]any{"title": "t", "project_id": first}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("ticket = %d %s", res.StatusCode, data)
	}
	ticket := decode[confstore.Ticket](t, data).ID
	res, data = x.do(t, "PUT", "/api/tickets/"+ticket, map[string]any{"title": "renamed", "project_id": second}, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("edit = %d %s", res.StatusCode, data)
	}
	got := decode[ticketResp](t, data)
	if got.ProjectID != second || got.UserID != testUser || got.Title != "renamed" {
		t.Errorf("edited = %+v", got.Ticket)
	}
}

// An upload is its uploader's until they attach it. "A claim of someone
// else's attachment is a silent no-op" says the store; the owner of the claim
// is what must make it so, or a known id copies a stranger's file into one's
// own workspace.
func TestAttachmentCannotBeClaimedByAnotherUser(t *testing.T) {
	files := newTestFiles(t, 1<<20, []string{"text/*"})
	x := newEnvWith(t, engine.Config{}, files.deps)
	files.serve(x)
	bob := x.addUser(t, "bob", false)
	conftest.SeedProject(t, x.srv.Conf, confstore.Project{ID: "prj-bob", UserID: "bob", Name: "bobs"})
	mine, err := files.att.Save(context.Background(), testUser, "secret.txt", "text/plain", strings.NewReader("my secret"))
	if err != nil {
		t.Fatal(err)
	}

	res, data := x.do(t, "POST", "/api/tickets", map[string]any{"title": "t", "project_id": "prj-bob", "attachment_ids": []string{mine.ID}}, bob)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("ticket = %d %s", res.StatusCode, data)
	}
	if got := decode[ticketResp](t, data); len(got.AttachmentIDs) != 0 {
		t.Errorf("bob's ticket claimed my upload: %+v", got.AttachmentIDs)
	}
	if a, err := files.att.Get(context.Background(), mine.ID); err != nil || !a.Unclaimed() {
		t.Errorf("my upload after the attempt = %+v, %v", a, err)
	}
}

// A body's list of ids, whatever else it is, is JSON of the right shape.
func TestBodyIDsMustBeStrings(t *testing.T) {
	x := newEnv(t, engine.Config{})
	res, body := x.bare(t, "POST", "/api/tickets", `{"title":"t","project_id":"`+testProject+`","attachment_ids":[1]}`,
		map[string]string{"Authorization": "Bearer " + x.token})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "invalid JSON") {
		t.Errorf("non-string ids = %d %s", res.StatusCode, body)
	}
	var e map[string]string
	if err := json.Unmarshal([]byte(body), &e); err != nil || e["error"] == "" {
		t.Errorf("error body = %s", body)
	}
}
