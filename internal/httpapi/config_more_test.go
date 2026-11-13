package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"apothecary/internal/account/logins"
	"apothecary/internal/account/providers"
	"apothecary/internal/engine"
	"apothecary/internal/flow/builtin"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/confstore/conftest"
	"apothecary/internal/project/kinds"
	"apothecary/internal/project/resolution"
	"apothecary/internal/project/tickets"
	"apothecary/internal/skills/skillstore"
)

// stubOps answers the resolver-backed routes.
type stubOps struct {
	warnings []string
	err      error
	empty    bool
	asked    []string
}

func (s *stubOps) TestIntegration(_ context.Context, id string) ([]string, error) {
	s.asked = append(s.asked, "test:"+id)
	return s.warnings, s.err
}
func (s *stubOps) ListRepos(_ context.Context, id string) ([]kinds.RepoInfo, error) {
	s.asked = append(s.asked, "repos:"+id)
	if s.empty {
		return nil, s.err
	}
	return []kinds.RepoInfo{{Locator: "1"}}, s.err
}
func (s *stubOps) ListModels(_ context.Context, id string) ([]kinds.ModelInfo, error) {
	s.asked = append(s.asked, "models:"+id)
	if s.empty {
		return nil, s.err
	}
	return []kinds.ModelInfo{{ID: "fake/model"}}, s.err
}
func (s *stubOps) ListBranches(_ context.Context, id string) ([]kinds.BranchInfo, error) {
	s.asked = append(s.asked, "branches:"+id)
	if s.empty {
		return nil, s.err
	}
	return []kinds.BranchInfo{{Name: "main"}}, s.err
}

func TestIntegrationOps(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedIntegration(t, "int-1", "repository")
	paths := []struct{ method, path string }{
		{"POST", "/api/integrations/int-1/test"},
		{"GET", "/api/integrations/int-1/repos"},
		{"GET", "/api/integrations/int-1/models"},
		{"GET", "/api/projects/" + testProject + "/branches"},
	}
	// Not wired: said so.
	for _, c := range paths {
		if res, body := x.do(t, c.method, c.path, nil, nil); res.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s unwired = %d %s", c.path, res.StatusCode, body)
		}
	}
	ops := &stubOps{warnings: []string{"token expires soon"}}
	x.srv.Ops = ops
	res, body := x.do(t, "POST", paths[0].path, nil, nil)
	if got := decode[TestResult](t, body); res.StatusCode != http.StatusOK || !got.OK || len(got.Warnings) != 1 {
		t.Errorf("test = %d %s", res.StatusCode, body)
	}
	for _, c := range paths[1:] {
		if res, body := x.do(t, c.method, c.path, nil, nil); res.StatusCode != http.StatusOK || strings.Contains(string(body), "null") {
			t.Errorf("%s = %d %s", c.path, res.StatusCode, body)
		}
	}
	// A provider with nothing to list answers a list.
	ops.empty = true
	for _, c := range paths[1:] {
		if res, body := x.do(t, c.method, c.path, nil, nil); res.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
			t.Errorf("%s empty = %d %s", c.path, res.StatusCode, body)
		}
	}
	// A failed check is a result to show; a failed listing is an error status.
	ops.err = confstore.ErrInvalid
	res, body = x.do(t, "POST", paths[0].path, nil, nil)
	if got := decode[TestResult](t, body); res.StatusCode != http.StatusOK || got.OK || got.Error == "" {
		t.Errorf("failed test = %d %s", res.StatusCode, body)
	}
	for _, c := range paths[1:] {
		if res, body := x.do(t, c.method, c.path, nil, nil); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s failing = %d %s", c.path, res.StatusCode, body)
		}
	}
}

func TestConfErrMapping(t *testing.T) {
	for err, want := range map[error]int{
		confstore.ErrNotFound:       http.StatusNotFound,
		confstore.ErrConflict:       http.StatusConflict,
		confstore.ErrReferenced:     http.StatusConflict,
		confstore.ErrInvalid:        http.StatusBadRequest,
		resolution.ErrUnresolvable:  http.StatusBadRequest,
		errors.New("database down"): http.StatusInternalServerError,
	} {
		w := httptest.NewRecorder()
		confErr(w, err)
		if w.Code != want {
			t.Errorf("%v = %d, want %d", err, w.Code, want)
		}
	}
}

func malformed(t *testing.T, x *env, method, path string) {
	t.Helper()
	res, body := x.bare(t, method, path, "{", map[string]string{"Authorization": "Bearer " + x.token})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "invalid JSON") {
		t.Errorf("%s %s with a malformed body = %d %s", method, path, res.StatusCode, body)
	}
}

func TestProjectCRUDEdges(t *testing.T) {
	x := newConfEnv(t)
	pid := x.makeProject(t, "web", nil)
	path := "/api/projects/" + pid

	malformed(t, x.env, "POST", "/api/projects")
	malformed(t, x.env, "PUT", path)
	if res, body := x.do(t, "POST", "/api/projects", projectBody{Name: "x"}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("incomplete project = %d %s", res.StatusCode, body)
	}
	res, body := x.do(t, "GET", path, nil, nil)
	p := decode[confstore.Project](t, body)
	if res.StatusCode != http.StatusOK || p.UserID != testUser {
		t.Fatalf("get = %d %s", res.StatusCode, body)
	}
	// An edit cannot give a project away: the body carries no owner, and one
	// smuggled in is ignored.
	res, body = x.do(t, "PUT", path, map[string]any{
		"user_id": "someone-else", "name": "renamed", "repo_account_id": p.RepoAccountID, "provisioner_id": p.ProvisionerID,
		"harness_ids": p.HarnessIDs, "repo": p.Repo, "default_flow": p.DefaultFlow,
	}, nil)
	got := decode[confstore.Project](t, body)
	if res.StatusCode != http.StatusOK || got.Name != "renamed" || got.UserID != testUser {
		t.Fatalf("update = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "PUT", path, projectBody{Name: ""}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad update = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "DELETE", path, nil, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d %s", res.StatusCode, body)
	}
	if res, _ := x.do(t, "GET", path, nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("after delete = %d", res.StatusCode)
	}
	for name, h := range map[string]http.HandlerFunc{
		"get": x.srv.getProject, "delete": x.srv.deleteProject, "browser": x.srv.getProjectBrowser,
	} {
		if w := call(h, "GET", "/", "", testUser, map[string]string{"id": "gone"}); w.Code != http.StatusNotFound {
			t.Errorf("%s of nothing = %d %s", name, w.Code, w.Body)
		}
	}
}

// The workshop's branch format rides in repo: saved as sent, refused with
// the field named when it is not one.
func TestProjectBranchFormat(t *testing.T) {
	x := newConfEnv(t)
	path := "/api/projects/" + x.makeProject(t, "web", nil)
	_, body := x.do(t, "GET", path, nil, nil)
	p := decode[confstore.Project](t, body)
	put := func(format string) (*http.Response, []byte) {
		repo := p.Repo
		repo.BranchFormat = format
		return x.do(t, "PUT", path, map[string]any{
			"name": p.Name, "repo_account_id": p.RepoAccountID, "provisioner_id": p.ProvisionerID,
			"harness_ids": p.HarnessIDs, "repo": repo, "default_flow": p.DefaultFlow,
		}, nil)
	}
	res, body := put("{ticket_number}/{ticket_title}")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update = %d %s", res.StatusCode, body)
	}
	_, body = x.do(t, "GET", path, nil, nil)
	if got := decode[confstore.Project](t, body).Repo.BranchFormat; got != "{ticket_number}/{ticket_title}" {
		t.Errorf("read back %q", got)
	}
	if res, body := put("{type}/{title}"); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "repo.branch_format") {
		t.Errorf("invalid format = %d %s", res.StatusCode, body)
	}
}

func TestIntegrationCRUDEdges(t *testing.T) {
	x := newConfEnv(t)
	malformed(t, x.env, "POST", "/api/integrations")
	if res, body := x.do(t, "POST", "/api/integrations", map[string]any{"port": "nonsense", "kind": "fake", "name": "x"}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad port = %d %s", res.StatusCode, body)
	}
	res, body := x.do(t, "POST", "/api/integrations", map[string]any{"port": "repository", "kind": "fake", "name": "r", "secrets": map[string]string{"token": "glpat-SECRET"}}, nil)
	if res.StatusCode != http.StatusCreated || strings.Contains(string(body), "glpat-SECRET") {
		t.Fatalf("create = %d %s", res.StatusCode, body)
	}
	id := decode[integrationJSON](t, body).ID
	path := "/api/integrations/" + id

	res, body = x.do(t, "GET", path, nil, nil)
	if res.StatusCode != http.StatusOK || strings.Contains(string(body), "glpat-SECRET") {
		t.Errorf("get = %d %s", res.StatusCode, body)
	}
	malformed(t, x.env, "PUT", path)
	res, body = x.do(t, "PUT", path, map[string]any{"name": "renamed"}, nil)
	if res.StatusCode != http.StatusOK || decode[integrationJSON](t, body).Name != "renamed" {
		t.Errorf("rename = %d %s", res.StatusCode, body)
	}
	// Absent keeps the secret, as the §3 merge says.
	in, err := x.conf.GetIntegration(t.Context(), id)
	if err != nil || in.Secrets["token"] != "glpat-SECRET" {
		t.Errorf("secret after a rename = %+v, %v", in.Secrets, err)
	}
	if w := call(x.srv.updateIntegration, "PUT", "/", `{"name":"x"}`, testUser, map[string]string{"id": "gone"}); w.Code != http.StatusNotFound {
		t.Errorf("update of nothing = %d %s", w.Code, w.Body)
	}
	if w := call(x.srv.getIntegration, "GET", "/", "", testUser, map[string]string{"id": "gone"}); w.Code != http.StatusNotFound {
		t.Errorf("get of nothing = %d %s", w.Code, w.Body)
	}
	if w := call(x.srv.deleteIntegration, "DELETE", "/", "", testUser, map[string]string{"id": "gone"}); w.Code != http.StatusNotFound {
		t.Errorf("delete of nothing = %d %s", w.Code, w.Body)
	}
	if res, body := x.do(t, "DELETE", path, nil, nil); res.StatusCode != http.StatusNoContent {
		t.Errorf("delete = %d %s", res.StatusCode, body)
	}
}

// A project's integrations are in use; deleting one is a conflict, not a
// dangling reference.
func TestReferencedIntegrationStays(t *testing.T) {
	x := newConfEnv(t)
	pid := x.makeProject(t, "web", nil)
	p, err := x.conf.GetProject(t.Context(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if res, body := x.do(t, "DELETE", "/api/integrations/"+p.RepoAccountID, nil, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("delete in use = %d %s", res.StatusCode, body)
	}
}

// The throwaway check, driven over HTTP: started, polled, its latest found by
// project, cancelled by id.
func TestEnvCheckRoutes(t *testing.T) {
	x := newEnv(t, engine.Config{})
	project := "/api/projects/" + testProject + "/envcheck"
	if res, body := x.do(t, "GET", project, nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("latest of none = %d %s", res.StatusCode, body)
	}
	malformed(t, x, "POST", project)
	res, body := x.do(t, "POST", project, EnvCheckRequest{Keep: true}, nil)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("start = %d %s", res.StatusCode, body)
	}
	id := decode[EnvCheckRef](t, body).CheckID
	waitFor(t, "the check to finish", func() bool {
		_, b := x.do(t, "GET", "/api/envchecks/"+id, nil, nil)
		return !strings.Contains(string(b), `"status":"running"`)
	})
	if res, body := x.do(t, "GET", project, nil, nil); res.StatusCode != http.StatusOK || !strings.Contains(string(body), id) {
		t.Errorf("latest = %d %s", res.StatusCode, body)
	}
	// A body is optional.
	if res, body := x.do(t, "POST", project, nil, nil); res.StatusCode != http.StatusAccepted {
		t.Errorf("start with no body = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "DELETE", "/api/envchecks/"+id, nil, nil); res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusNotFound {
		t.Errorf("cancel = %d %s", res.StatusCode, body)
	}
	gone := map[string]string{"id": "gone"}
	if w := call(x.srv.getEnvCheck, "GET", "/", "", testUser, gone); w.Code != http.StatusNotFound {
		t.Errorf("get of nothing = %d", w.Code)
	}
	if w := call(x.srv.cancelEnvCheck, "DELETE", "/", "", testUser, gone); w.Code != http.StatusNotFound {
		t.Errorf("cancel of nothing = %d", w.Code)
	}
}

// A project that does not resolve cannot be checked.
func TestEnvCheckOfAnUnresolvableProject(t *testing.T) {
	x := newConfEnv(t)
	if w := call(x.srv.startEnvCheck, "POST", "/", "", testUser, map[string]string{"id": "gone"}); w.Code != http.StatusNotFound && w.Code != http.StatusBadRequest {
		t.Errorf("start for nothing = %d %s", w.Code, w.Body)
	}
}

func TestFlowRoutes(t *testing.T) {
	x := newConfEnv(t)
	bob := x.addUser(t, "bob", false)
	mine, second := x.makeProject(t, "mine", nil), x.makeProject(t, "second", nil)
	theirs := x.makeProject(t, "theirs", bob)
	flows := "/api/projects/" + mine + "/flows"

	res, body := x.do(t, "GET", "/api/flows/schema", nil, nil)
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/schema+json" || !strings.Contains(string(body), "$schema") {
		t.Errorf("schema = %d %s", res.StatusCode, body[:min(len(body), 80)])
	}

	// Create, and again.
	malformed(t, x.env, "POST", flows)
	if res, body := x.do(t, "POST", flows, CreateFlowRequest{Name: "v1", YAML: "not: a flow"}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid doc = %d %s", res.StatusCode, body)
	}
	res, body = x.do(t, "POST", flows, CreateFlowRequest{Name: "v1", YAML: v1FlowDoc}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %s", res.StatusCode, body)
	}
	created := decode[flowResp](t, body)
	if res, body := x.do(t, "POST", flows, CreateFlowRequest{Name: "v1", YAML: v1FlowDoc}, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("duplicate = %d %s", res.StatusCode, body)
	}

	// A built-in's name resolves to the template until the project saves one.
	name := builtin.FlowNames()[0]
	res, body = x.do(t, "GET", flows+"/"+name, nil, nil)
	if got := decode[flowResp](t, body); res.StatusCode != http.StatusOK || !got.Builtin || got.ProjectID != mine {
		t.Errorf("template = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "GET", flows+"/nope", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("no such flow = %d %s", res.StatusCode, body)
	}
	// Nothing of this project's to delete under a template's name.
	if res, body := x.do(t, "DELETE", flows+"/"+name, nil, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("delete a template = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "DELETE", flows+"/nope", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("delete nothing = %d %s", res.StatusCode, body)
	}

	// Versions.
	res, body = x.do(t, "PUT", flows+"/v1", UpdateFlowRequest{YAML: strings.Replace(v1FlowDoc, "plan the work", "plan it well", 1)}, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update = %d %s", res.StatusCode, body)
	}
	malformed(t, x.env, "PUT", flows+"/v1")
	res, body = x.do(t, "GET", flows+"/v1/versions", nil, nil)
	if vs := decode[[]confstore.FlowVersion](t, body); res.StatusCode != http.StatusOK || len(vs) != 2 {
		t.Errorf("versions = %d %s", res.StatusCode, body)
	}
	res, body = x.do(t, "GET", flows+"/nope/versions", nil, nil)
	if res.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("versions of nothing = %d %s", res.StatusCode, body)
	}
	res, body = x.do(t, "GET", "/api/projects/"+mine+"/flow-versions/"+created.Hash, nil, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "plan the work") {
		t.Errorf("version = %d %s", res.StatusCode, body)
	}
	// A hash from this project is not an id for another's history.
	if res, body := x.do(t, "GET", "/api/projects/"+second+"/flow-versions/"+created.Hash, nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("version from another project = %d %s", res.StatusCode, body)
	}

	// Copy: into one's own project, under a new name; never into someone's.
	copyPath := flows + "/v1/copy"
	malformed(t, x.env, "POST", copyPath)
	res, body = x.do(t, "POST", copyPath, CopyFlowRequest{ToProjectID: second, Name: "v1"}, nil)
	if res.StatusCode != http.StatusCreated || decode[flowResp](t, body).ProjectID != second {
		t.Errorf("copy = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", copyPath, CopyFlowRequest{ToProjectID: theirs}, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("copy into someone's project = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", copyPath, CopyFlowRequest{ToProjectID: mine}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("copy onto itself = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", copyPath, CopyFlowRequest{}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("copy with no target = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", flows+"/nope/copy", CopyFlowRequest{ToProjectID: second}, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("copy of nothing = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", "/api/flow-templates/"+name+"/copy", CopyFlowRequest{ToProjectID: theirs}, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("template into someone's project = %d %s", res.StatusCode, body)
	}
	malformed(t, x.env, "POST", "/api/flow-templates/"+name+"/copy")
	if res, body := x.do(t, "POST", "/api/flow-templates/nope/copy", CopyFlowRequest{ToProjectID: mine}, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("no such template = %d %s", res.StatusCode, body)
	}

	// The overview is the caller's own projects', and only theirs.
	res, body = x.do(t, "GET", "/api/flows", nil, nil)
	all := decode[[]flowResp](t, body)
	if res.StatusCode != http.StatusOK || len(all) != 2 {
		t.Fatalf("overview = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "GET", "/api/flows", nil, bob); res.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("bob's overview = %d %s", res.StatusCode, body)
	}
	// The project list adds the templates it has not adopted.
	_, body = x.do(t, "GET", flows, nil, nil)
	if got := decode[[]flowResp](t, body); len(got) != 1+len(builtin.FlowNames()) {
		t.Errorf("project flows = %d rows", len(got))
	}
	res, body = x.do(t, "GET", "/api/flow-templates", nil, nil)
	if got := decode[[]flowResp](t, body); res.StatusCode != http.StatusOK || len(got) != len(builtin.FlowNames()) || !got[0].Builtin {
		t.Errorf("templates = %d %s", res.StatusCode, body)
	}

	if res, body := x.do(t, "DELETE", flows+"/v1", nil, nil); res.StatusCode != http.StatusNoContent {
		t.Errorf("delete = %d %s", res.StatusCode, body)
	}
}

// A row whose document no longer parses is listed without its description, so
// one bad flow does not take the page down.
func TestFlowRowThatNoLongerParses(t *testing.T) {
	got := describeFlow(confstore.FlowRow{Name: "broken", YAML: "not a flow"}, false)
	if got.Name != "broken" || got.OperatesOn != "" || got.Phases != nil {
		t.Errorf("described = %+v", got)
	}
	if _, ok := builtinFlowRow("p", "no-such-template"); ok {
		t.Error("a template that does not exist was offered")
	}
	name := builtin.FlowNames()[0]
	if shadow := describeFlow(confstore.FlowRow{Name: name, YAML: "not a flow"}, false); !shadow.ShadowsBuiltin {
		t.Errorf("a row with a template's name does not shadow it: %+v", shadow)
	}
}

func TestBindingAndSettingsEdges(t *testing.T) {
	x := newEnv(t, engine.Config{})
	base := "/api/projects/" + testProject
	malformed(t, x, "PUT", base+"/flows/builder/binding")
	if res, body := x.do(t, "DELETE", base+"/flows/builder/binding", nil, nil); res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusNotFound {
		t.Errorf("delete of no binding = %d %s", res.StatusCode, body)
	}
	malformed(t, x, "PUT", "/api/settings")
	res, body := x.do(t, "GET", "/api/settings", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("settings = %d %s", res.StatusCode, body)
	}
	// An absent field keeps its stored value.
	set := decode[confstore.Settings](t, body)
	res, body = x.do(t, "PUT", "/api/settings", map[string]any{}, nil)
	if res.StatusCode != http.StatusOK || decode[confstore.Settings](t, body) != set {
		t.Errorf("empty put = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "PUT", "/api/settings", map[string]any{"reaper": map[string]any{"completed_after": "soon"}}, nil); res.StatusCode != http.StatusOK && res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad settings = %d %s", res.StatusCode, body)
	}
}

// keeperStore is a credential the keeper sees; refused says it is spent.
func TestCredentialRoutes(t *testing.T) {
	x := newConfEnv(t)
	ctx := context.Background()
	res, data := x.do(t, "GET", "/api/credentials", nil, nil)
	if res.StatusCode != http.StatusOK || strings.TrimSpace(string(data)) != "[]" {
		t.Errorf("empty list = %d %s", res.StatusCode, data)
	}
	malformed(t, x.env, "POST", "/api/credentials")
	malformed(t, x.env, "POST", "/api/credentials/import")
	res, data = x.do(t, "POST", "/api/credentials", map[string]any{"provider": "openrouter", "key": "  sk-or-x  "}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %s", res.StatusCode, data)
	}
	pc := decode[confstore.ProviderCredential](t, data)
	opened, _, err := x.conf.OpenCredential(ctx, pc.ID)
	if err != nil || opened.Key != "sk-or-x" {
		t.Errorf("stored key = %q, %v", opened.Key, err)
	}
	path := "/api/credentials/" + pc.ID

	res, data = x.do(t, "GET", path, nil, nil)
	if res.StatusCode != http.StatusOK || strings.Contains(string(data), "sk-or-x") {
		t.Errorf("get = %d %s", res.StatusCode, data)
	}
	malformed(t, x.env, "PUT", path)
	res, data = x.do(t, "PUT", path, CredentialRename{Name: "work"}, nil)
	if res.StatusCode != http.StatusOK || decode[confstore.ProviderCredential](t, data).Name != "work" {
		t.Errorf("rename = %d %s", res.StatusCode, data)
	}
	// Past the guard, a row gone.
	gone := map[string]string{"id": "gone"}
	if w := call(x.srv.getCredential, "GET", "/", "", testUser, gone); w.Code != http.StatusNotFound {
		t.Errorf("get of nothing = %d", w.Code)
	}
	if w := call(x.srv.renameCredential, "PUT", "/", `{"name":"x"}`, testUser, gone); w.Code != http.StatusNotFound {
		t.Errorf("rename of nothing = %d", w.Code)
	}
	if w := call(x.srv.testCredential, "POST", "/", "", testUser, gone); w.Code != http.StatusNotFound {
		t.Errorf("test of nothing = %d", w.Code)
	}
	if w := call(x.srv.deleteCredential, "DELETE", "/", "", testUser, gone); w.Code != http.StatusNotFound {
		t.Errorf("delete of nothing = %d", w.Code)
	}

	if res, data := x.do(t, "DELETE", path, nil, nil); res.StatusCode != http.StatusNoContent {
		t.Errorf("delete = %d %s", res.StatusCode, data)
	}
	if res, _ := x.do(t, "GET", path, nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("after delete = %d", res.StatusCode)
	}

	// Import: nothing usable is a 200 with the reasons, junk a 400.
	res, data = x.do(t, "POST", "/api/credentials/import", CredentialImportRequest{Text: "NOT_A_KEY=x"}, nil)
	got := decode[CredentialImportResult](t, data)
	if res.StatusCode != http.StatusOK || len(got.Created) != 0 || len(got.Rejected) != 1 {
		t.Errorf("nothing kept = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "POST", "/api/credentials/import", CredentialImportRequest{Text: "{"}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("junk = %d %s", res.StatusCode, data)
	}
	// A list with only kept entries still has an empty refused list.
	res, data = x.do(t, "POST", "/api/credentials/import", CredentialImportRequest{Text: "OPENROUTER_API_KEY=sk-new"}, nil)
	if res.StatusCode != http.StatusCreated || !strings.Contains(string(data), `"rejected":[]`) || strings.Contains(string(data), "sk-new") {
		t.Errorf("all kept = %d %s", res.StatusCode, data)
	}
	// One the table refuses at creation lands in the refusals, not a 500.
	res, data = x.do(t, "POST", "/api/credentials/import", CredentialImportRequest{Text: "OPENROUTER_API_KEY=sk-new\n"}, nil)
	if res.StatusCode >= 500 {
		t.Errorf("repeat = %d %s", res.StatusCode, data)
	}
}

func TestProviderList(t *testing.T) {
	x := newConfEnv(t)
	res, data := x.do(t, "GET", "/api/providers", nil, nil)
	all := decode[[]ProviderInfo](t, data)
	if res.StatusCode != http.StatusOK || len(all) != len(providers.All()) {
		t.Fatalf("all = %d, %d rows", res.StatusCode, len(all))
	}
	byID := map[string]ProviderInfo{}
	for _, p := range all {
		byID[p.ID] = p
	}
	if p := byID["openai-chatgpt"]; !p.Login || !p.SignIn || p.Key {
		t.Errorf("chatgpt = %+v", p)
	}
	if p := byID["openrouter"]; p.Login || p.SignIn || !p.Key {
		t.Errorf("openrouter = %+v", p)
	}
	if p := byID["google-antigravity"]; !p.Login || p.SignIn {
		t.Errorf("antigravity (a login one imports) = %+v", p)
	}
	_, data = x.do(t, "GET", "/api/providers?harness="+providers.Codex, nil, nil)
	some := decode[[]ProviderInfo](t, data)
	if len(some) == 0 || len(some) >= len(all) {
		t.Errorf("narrowed = %d rows of %d", len(some), len(all))
	}
	for _, p := range some {
		found := false
		for _, h := range p.Harnesses {
			found = found || h == providers.Codex
		}
		if !found {
			t.Errorf("%s listed for a harness it does not serve: %v", p.ID, p.Harnesses)
		}
	}
}

// Testing a credential opens it through the keeper when there is one and says
// what came of it, without failing the request.
func TestCredentialTest(t *testing.T) {
	x := newConfEnv(t)
	ctx := context.Background()
	login, err := x.conf.CreateCredential(ctx, testUser, providers.Credential{
		Provider: "github-copilot", Kind: providers.KindOAuth, Access: "gho", Refresh: "gho",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := x.conf.CreateCredential(ctx, testUser, providers.Credential{
		Provider: "anthropic", Kind: providers.KindOAuth, Access: "a", Refresh: "r", Expires: time.Now().Add(time.Hour),
	}, "gone")
	if err != nil {
		t.Fatal(err)
	}
	if err := x.conf.DisableCredential(ctx, disabled.ID, "spent"); err != nil {
		t.Fatal(err)
	}
	x.srv.Keeper = logins.New(x.conf, nil)

	res, data := x.do(t, "POST", "/api/credentials/"+disabled.ID+"/test", nil, nil)
	got := decode[TestResult](t, data)
	if res.StatusCode != http.StatusOK || got.OK || !strings.Contains(got.Error, "spent") {
		t.Errorf("disabled = %d %s", res.StatusCode, data)
	}
	_ = login
}

func TestSignInRoutes(t *testing.T) {
	x := newConfEnv(t)
	bob := x.addUser(t, "bob", false)
	if res, data := x.do(t, "POST", "/api/credentials/signin", SignInRequest{Provider: "anthropic"}, nil); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("not wired = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "POST", "/api/credentials/signin/x/complete", SignInComplete{}, nil); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("not wired = %d %s", res.StatusCode, data)
	}
	x.srv.SignIns = logins.NewSignIns()
	malformed(t, x.env, "POST", "/api/credentials/signin")
	malformed(t, x.env, "POST", "/api/credentials/signin/x/complete")
	if res, data := x.do(t, "POST", "/api/credentials/signin", SignInRequest{Provider: "openrouter"}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("no sign-in for it = %d %s", res.StatusCode, data)
	}
	// Anthropic's start is local: the URL is built here, the exchange later.
	res, data := x.do(t, "POST", "/api/credentials/signin", SignInRequest{Provider: "anthropic"}, nil)
	p := decode[logins.Pending](t, data)
	if res.StatusCode != http.StatusOK || !p.Paste || p.ID == "" || p.URL == "" {
		t.Fatalf("start = %d %s", res.StatusCode, data)
	}
	// Someone else cannot finish it.
	if res, data := x.do(t, "POST", "/api/credentials/signin/"+p.ID+"/complete", SignInComplete{Pasted: "code"}, bob); res.StatusCode != http.StatusBadRequest {
		t.Errorf("someone else's sign-in = %d %s", res.StatusCode, data)
	}
	// Nothing pasted stops before any exchange.
	if res, data := x.do(t, "POST", "/api/credentials/signin/"+p.ID+"/complete", SignInComplete{}, nil); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "paste") {
		t.Errorf("empty paste = %d %s", res.StatusCode, data)
	}
}

func TestAuthBrokerFailure(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	// A run with no broker behind it, past the token check.
	w := call(x.srv.authBroker, "GET", "/api/runs/r1/auth-broker/snapshot", "", testUser, map[string]string{"id": "r1"})
	if w.Code != http.StatusNotFound {
		t.Errorf("no broker = %d %s", w.Code, w.Body)
	}
}

// listFails makes the store's lists fail, for the routes that report it.
type listFails struct {
	confstore.DB
	err error
}

func (l listFails) ListProjects(context.Context, string) ([]confstore.Project, error) {
	return nil, l.err
}
func (l listFails) ListIntegrations(context.Context, string) ([]confstore.StoredIntegration, error) {
	return nil, l.err
}
func (l listFails) ListTickets(context.Context, string) ([]confstore.Ticket, error) {
	return nil, l.err
}
func (l listFails) ListFlows(context.Context, string) ([]confstore.FlowRow, error) {
	return nil, l.err
}
func (l listFails) ListFlowBindings(context.Context, string) ([]confstore.FlowBinding, error) {
	return nil, l.err
}
func (l listFails) ListAutomations(context.Context, string) ([]confstore.Automation, error) {
	return nil, l.err
}
func (l listFails) ListCredentials(context.Context, string) ([]confstore.ProviderCredential, error) {
	return nil, l.err
}
func (l listFails) GetSettings(context.Context, string) (map[string]string, error) {
	return nil, l.err
}
func (l listFails) GetFlow(context.Context, string, string) (confstore.FlowRow, error) {
	return confstore.FlowRow{}, l.err
}
func (l listFails) ListProjectSecrets(context.Context, string) ([]confstore.StoredProjectSecret, error) {
	return nil, l.err
}

// A database outage is a 500 with the reason; it never reads as an empty list.
func TestConfigStoreOutage(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.srv.Conf = confstore.New(listFails{x.srv.Conf.DB(), errors.New("db down")}, secretsKey(t))
	for _, path := range []string{
		"/api/projects", "/api/integrations", "/api/tickets", "/api/flows", "/api/credentials", "/api/settings",
		"/api/projects/" + testProject + "/flow-bindings", "/api/projects/" + testProject + "/automations",
		"/api/projects/" + testProject + "/flows", "/api/projects/" + testProject + "/secrets",
	} {
		res, body := x.do(t, "GET", path, nil, nil)
		if res.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "db down") {
			t.Errorf("%s = %d %s", path, res.StatusCode, body)
		}
	}
	if res, body := x.do(t, "PUT", "/api/settings", map[string]any{}, nil); res.StatusCode != http.StatusInternalServerError {
		t.Errorf("settings write = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", "/api/projects/"+testProject+"/flows", CreateFlowRequest{Name: "x", YAML: loopFlowDoc}, nil); res.StatusCode != http.StatusInternalServerError {
		t.Errorf("create flow = %d %s", res.StatusCode, body)
	}
}

// flowOverviewFails fails only the second list the overview makes.
type projectsFail struct {
	confstore.DB
	err error
}

func (p projectsFail) ListProjects(context.Context, string) ([]confstore.Project, error) {
	return nil, p.err
}

func TestFlowOverviewCannotListProjects(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.srv.Conf = confstore.New(projectsFail{x.srv.Conf.DB(), errors.New("db down")}, secretsKey(t))
	if res, body := x.do(t, "GET", "/api/flows", nil, nil); res.StatusCode != http.StatusInternalServerError {
		t.Errorf("overview = %d %s", res.StatusCode, body)
	}
}

func TestAutomationEdges(t *testing.T) {
	x := newConfEnv(t)
	a, b := x.makeProject(t, "a", nil), x.makeProject(t, "b", nil)
	res, data := x.do(t, "POST", "/api/tickets", map[string]any{"project_id": a, "title": "Nightly"}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("ticket = %d %s", res.StatusCode, data)
	}
	ticket := decode[confstore.Ticket](t, data).ID
	base := "/api/projects/" + a + "/automations"
	malformed(t, x.env, "POST", base)
	res, data = x.do(t, "POST", base, AutomationRequest{Name: "n", Kind: "cron", Schedule: "0 3 * * *", TicketID: ticket, Flow: "builder"}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %s", res.StatusCode, data)
	}
	id := decode[confstore.Automation](t, data).ID
	item := base + "/" + id

	// Through the other project's path it does not exist, for every verb.
	other := "/api/projects/" + b + "/automations/" + id
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		if res, data := x.do(t, method, other, AutomationRequest{Name: "x"}, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s through another project = %d %s", method, res.StatusCode, data)
		}
	}
	malformed(t, x.env, "PUT", item)
	if res, data := x.do(t, "PUT", item, AutomationRequest{Name: "", Kind: "cron"}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad update = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "DELETE", base+"/nope", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("delete of nothing = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "PUT", base+"/nope", AutomationRequest{Name: "x"}, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("update of nothing = %d %s", res.StatusCode, data)
	}
	if res, _ := x.do(t, "GET", item, nil, nil); res.StatusCode != http.StatusOK {
		t.Errorf("the refused writes touched it: %d", res.StatusCode)
	}
}

// fetchBoom is a skill source that fails.
func TestSkillRoutes(t *testing.T) {
	skl := newTestSkills(t, nil)
	x := newEnvWith(t, engine.Config{}, skl.deps)
	skl.serve(t, x)

	if res, data := x.do(t, "GET", "/api/skillsets", nil, nil); res.StatusCode != http.StatusOK || strings.TrimSpace(string(data)) != "[]" {
		t.Errorf("empty sets = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "POST", "/api/skills", SkillRequest{Name: "x", Files: []SkillFileRequest{{Path: "SKILL.md", ContentB64: "!!!"}}}, nil); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "SKILL.md") {
		t.Errorf("bad base64 = %d %s", res.StatusCode, data)
	}
	malformed(t, x, "POST", "/api/skills")
	_, data := x.do(t, "POST", "/api/skills", skillBody("pdf", "Reads PDFs.", SkillFileRequest{Path: "ref.md", ContentB64: b64("notes")}), nil)
	sk := decode[skillstore.Skill](t, data)
	path := "/api/skills/" + sk.ID

	res, data := x.do(t, "GET", path, nil, nil)
	detail := decode[SkillDetail](t, data)
	if res.StatusCode != http.StatusOK || len(detail.Contents) != 2 {
		t.Fatalf("detail = %d %s", res.StatusCode, data)
	}
	malformed(t, x, "PUT", path)
	if res, data := x.do(t, "PUT", path, SkillRequest{Name: "pdf", Files: []SkillFileRequest{{Path: "a", ContentB64: "!!!"}}}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("update, bad base64 = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "PUT", path, SkillRequest{Name: "pdf", Files: []SkillFileRequest{{Path: "other.md", ContentB64: b64("x")}}}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("update without SKILL.md = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "PUT", path, skillBody("pdf", "Reads more PDFs."), nil); res.StatusCode != http.StatusOK {
		t.Errorf("update = %d %s", res.StatusCode, data)
	}

	// Skillsets.
	malformed(t, x, "POST", "/api/skillsets")
	if res, data := x.do(t, "POST", "/api/skillsets", SkillsetRequest{Name: "Docs", SkillIDs: []string{"nope"}}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("set of nothing = %d %s", res.StatusCode, data)
	}
	res, data = x.do(t, "POST", "/api/skillsets", SkillsetRequest{Name: "Docs", Description: "d", SkillIDs: []string{sk.ID}}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("set = %d %s", res.StatusCode, data)
	}
	set := decode[skillstore.Skillset](t, data)
	setPath := "/api/skillsets/" + set.ID
	res, data = x.do(t, "GET", setPath, nil, nil)
	if res.StatusCode != http.StatusOK || decode[skillstore.Skillset](t, data).Name != "Docs" {
		t.Errorf("get = %d %s", res.StatusCode, data)
	}
	res, data = x.do(t, "GET", "/api/skillsets", nil, nil)
	if res.StatusCode != http.StatusOK || len(decode[[]skillstore.Skillset](t, data)) != 1 {
		t.Errorf("list = %d %s", res.StatusCode, data)
	}
	malformed(t, x, "PUT", setPath)
	res, data = x.do(t, "PUT", setPath, SkillsetRequest{Name: "Renamed", SkillIDs: []string{sk.ID}}, nil)
	if res.StatusCode != http.StatusOK || decode[skillstore.Skillset](t, data).Name != "Renamed" {
		t.Errorf("update = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "PUT", setPath, SkillsetRequest{Name: "Renamed", SkillIDs: []string{"nope"}}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("update to nothing = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "GET", setPath+"/versions/not-a-number", nil, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad seq = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "GET", setPath+"/versions/-1", nil, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("negative seq = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "GET", setPath+"/versions/999", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("no such version = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "GET", setPath+"/versions/0", nil, nil); res.StatusCode != http.StatusOK {
		t.Errorf("latest = %d %s", res.StatusCode, data)
	}

	// No importer wired: said so, on all three routes.
	for _, p := range []string{"/api/skills/import", path + "/refresh", setPath + "/refresh"} {
		if res, data := x.do(t, "POST", p, SkillImportRequest{URL: "https://x"}, nil); res.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s = %d %s", p, res.StatusCode, data)
		}
	}
	// A listed skill cannot go; the set first, then the skill.
	if res, data := x.do(t, "DELETE", path, nil, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("delete of a listed skill = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "DELETE", setPath, nil, nil); res.StatusCode != http.StatusNoContent {
		t.Errorf("delete set = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "DELETE", path, nil, nil); res.StatusCode != http.StatusNoContent {
		t.Errorf("delete skill = %d %s", res.StatusCode, data)
	}

	// What goes missing past the guard.
	gone := map[string]string{"id": "gone"}
	s := x.srv
	for name, c := range map[string]struct {
		h    http.HandlerFunc
		body string
	}{
		"get skill":    {s.getSkill, ""},
		"delete skill": {s.deleteSkill, ""},
		"update skill": {s.updateSkill, `{"name":"x"}`},
		"get set":      {s.getSkillset, ""},
		"delete set":   {s.deleteSkillset, ""},
		"update set":   {s.updateSkillset, `{"name":"x"}`},
	} {
		if w := call(c.h, "POST", "/", c.body, testUser, gone); w.Code != http.StatusNotFound {
			t.Errorf("%s of nothing = %d %s", name, w.Code, w.Body)
		}
	}
}

func TestSkillImportFailures(t *testing.T) {
	fetchErr := errors.New("archive unreachable")
	skl := newTestSkills(t, func(context.Context, skillstore.Source) (skillstore.Fetched, error) {
		return skillstore.Fetched{}, fetchErr
	})
	x := newEnvWith(t, engine.Config{}, skl.deps)
	skl.serve(t, x)
	malformed(t, x, "POST", "/api/skills/import")
	if res, data := x.do(t, "POST", "/api/skills/import", SkillImportRequest{URL: "https://github.com/acme/skills"}, nil); res.StatusCode < 400 {
		t.Errorf("failed import = %d %s", res.StatusCode, data)
	}
	gone := map[string]string{"id": "gone"}
	if w := call(x.srv.refreshSkill, "POST", "/", "", testUser, gone); w.Code != http.StatusNotFound {
		t.Errorf("refresh of nothing = %d %s", w.Code, w.Body)
	}
	if w := call(x.srv.refreshSkillset, "POST", "/", "", testUser, gone); w.Code != http.StatusNotFound {
		t.Errorf("refresh of nothing = %d %s", w.Code, w.Body)
	}
}

func TestChannelRoutes(t *testing.T) {
	x := newEnv(t, engine.Config{})
	malformed(t, x, "POST", "/api/channels")
	if res, body := x.do(t, "POST", "/api/channels", map[string]any{"room_id": "x"}, nil); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "kind") {
		t.Errorf("no kind = %d %s", res.StatusCode, body)
	}
	// Secrets by any of their names come back masked; the rest as given.
	res, body := x.do(t, "POST", "/api/channels", map[string]any{
		"kind": "test", "access_token": "tok-SECRET", "webhook_url": "https://hooks/SECRET", "room_id": "!ops", "retries": 3,
	}, nil)
	if res.StatusCode != http.StatusCreated || strings.Contains(string(body), "SECRET") || !strings.Contains(string(body), "!ops") || !strings.Contains(string(body), `"retries":3`) {
		t.Errorf("masked = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "GET", "/api/channels", nil, nil); res.StatusCode != http.StatusOK || strings.Contains(string(body), "SECRET") {
		t.Errorf("list = %d %s", res.StatusCode, body)
	}
	if w := call(x.srv.deleteChannel, "DELETE", "/", "", testUser, map[string]string{"id": "gone"}); w.Code != http.StatusNotFound {
		t.Errorf("delete of nothing = %d", w.Code)
	}
}

func TestProjectSecretEdges(t *testing.T) {
	x := newEnv(t, engine.Config{})
	base := "/api/projects/" + testProject + "/secrets/"
	malformed(t, x, "PUT", base+"token")
	if res, body := x.do(t, "PUT", base+"BAD-NAME", ProjectSecretRequest{Value: "x"}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad name = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "DELETE", base+"nope", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("delete of nothing = %d %s", res.StatusCode, body)
	}
}

func TestMergeRequestStartEdges(t *testing.T) {
	x := newEnv(t, engine.Config{})
	base := "/api/projects/" + testProject + "/mrs/"
	if res, body := x.do(t, "POST", base+"abc/runs", nil, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("non-numeric iid = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", base+"0/runs", nil, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("zero iid = %d %s", res.StatusCode, body)
	}
	if res, body := x.bare(t, "POST", base+"1/runs", "{", map[string]string{"Authorization": "Bearer " + x.token}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", base+"999/runs", MRRunRequest{}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("no such merge request = %d %s", res.StatusCode, body)
	}
}

// failingSource is a tracker that is down.
type failingSource struct{ err error }

func (f failingSource) Test(context.Context) error { return f.err }
func (f failingSource) Search(context.Context, string, int) ([]trackerport.SourceTicket, error) {
	return nil, f.err
}
func (f failingSource) Fetch(context.Context, string) (trackerport.SourceTicket, error) {
	return trackerport.SourceTicket{}, f.err
}
func (f failingSource) OpenAttachment(context.Context, trackerport.SourceAttachment) (io.ReadCloser, error) {
	return nil, f.err
}

func TestTicketRoutes(t *testing.T) {
	x := newEnv(t, engine.Config{})
	id := x.newTicket(t, "first", "")
	conftest.SeedProject(t, x.srv.Conf, confstore.Project{ID: "prj-2", UserID: testUser, Name: "second"})
	res, data := x.do(t, "POST", "/api/tickets", map[string]any{"title": "second", "project_id": "prj-2"}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("second = %d %s", res.StatusCode, data)
	}

	// The filter.
	res, data = x.do(t, "GET", "/api/tickets?project="+testProject, nil, nil)
	rows := decode[[]confstore.Ticket](t, data)
	if res.StatusCode != http.StatusOK || len(rows) != 1 || rows[0].ID != id {
		t.Errorf("by project = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "GET", "/api/tickets?project=none", nil, nil); res.StatusCode != http.StatusOK || strings.TrimSpace(string(data)) != "[]" {
		t.Errorf("by no project = %d %s", res.StatusCode, data)
	}
	// With nothing at all it is still a list.
	if res, data := x.do(t, "GET", "/api/tickets", nil, x.addUser(t, "fresh", false)); res.StatusCode != http.StatusOK || strings.TrimSpace(string(data)) != "[]" {
		t.Errorf("empty = %d %s", res.StatusCode, data)
	}

	// Edit.
	path := "/api/tickets/" + id
	malformed(t, x, "PUT", path)
	malformed(t, x, "POST", "/api/tickets")
	if res, data := x.do(t, "PUT", path, map[string]any{"title": "t", "project_id": testProject, "branch": "a..b"}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad branch = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "PUT", path, map[string]any{"title": " ", "project_id": testProject}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("blank title = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "PUT", path, map[string]any{"title": "t", "project_id": testProject, "change_type": "nonsense"}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad change type = %d %s", res.StatusCode, data)
	}
	res, data = x.do(t, "PUT", path, map[string]any{"title": "renamed", "goal": "g2", "project_id": testProject, "branch": "fix/x", "change_type": "fix"}, nil)
	got := decode[ticketResp](t, data)
	if res.StatusCode != http.StatusOK || got.Title != "renamed" || got.BaseBranch != "fix/x" || got.ChangeType != "fix" {
		t.Errorf("edit = %d %s", res.StatusCode, data)
	}

	// A body a run start cannot read is an empty one, and a flow it cannot
	// resolve is refused.
	if res, data := x.do(t, "POST", path+"/runs", map[string]any{"flow": "no-such-flow"}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown flow = %d %s", res.StatusCode, data)
	}

	// Gone past the guard.
	gone := map[string]string{"id": "gone"}
	s := x.srv
	for name, c := range map[string]struct {
		h    http.HandlerFunc
		body string
	}{
		"get":         {s.getTicket, ""},
		"attachments": {s.getTicketAttachments, ""},
		"delete":      {s.deleteTicket, ""},
		"update":      {s.updateTicket, `{"title":"t","project_id":"` + testProject + `"}`},
		"start":       {s.startTicketRun, ""},
	} {
		if w := call(c.h, "POST", "/", c.body, testUser, gone); w.Code != http.StatusNotFound {
			t.Errorf("%s of nothing = %d %s", name, w.Code, w.Body)
		}
	}
	if res, data := x.do(t, "DELETE", path, nil, nil); res.StatusCode != http.StatusNoContent {
		t.Errorf("delete = %d %s", res.StatusCode, data)
	}
	if res, _ := x.do(t, "GET", path, nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("after delete = %d", res.StatusCode)
	}
}

func TestTicketImportFailures(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedIntegration(t, "src", "ticketsource")
	body := map[string]any{"project_id": testProject, "source_id": "src", "source_key": "K-1"}

	// A half-named import, in either direction.
	for _, half := range []map[string]any{
		{"project_id": testProject, "source_id": "src"},
		{"project_id": testProject, "source_key": "K-1"},
	} {
		if res, data := x.do(t, "POST", "/api/tickets", half, nil); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "both") {
			t.Errorf("%v = %d %s", half, res.StatusCode, data)
		}
	}
	// No trackers wired.
	if res, data := x.do(t, "POST", "/api/tickets", body, nil); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("no sources = %d %s", res.StatusCode, data)
	}

	// The tracker failing is the tracker's fault, wherever it shows.
	x.srv.Sources = stubSources{src: failingSource{errors.New("tracker down")}}
	if res, data := x.do(t, "POST", "/api/tickets", body, nil); res.StatusCode != http.StatusBadGateway {
		t.Errorf("import = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "GET", "/api/integrations/src/tickets?q=x", nil, nil); res.StatusCode != http.StatusBadGateway {
		t.Errorf("search = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "GET", "/api/integrations/src/tickets/K-1", nil, nil); res.StatusCode != http.StatusBadGateway {
		t.Errorf("preview = %d %s", res.StatusCode, data)
	}
	// What the tracker cannot resolve reads as config.
	x.srv.Sources = stubSources{src: failingSource{confstore.ErrNotFound}}
	if res, data := x.do(t, "GET", "/api/integrations/src/tickets/K-1", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("no such issue = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "POST", "/api/tickets", body, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("import of no such issue = %d %s", res.StatusCode, data)
	}
}

func TestSourceErrMapping(t *testing.T) {
	for err, want := range map[error]int{
		tickets.ErrNoSources:       http.StatusServiceUnavailable,
		confstore.ErrNotFound:      http.StatusNotFound,
		confstore.ErrInvalid:       http.StatusBadRequest,
		resolution.ErrUnresolvable: http.StatusBadRequest,
		errors.New("502"):          http.StatusBadGateway,
	} {
		w := httptest.NewRecorder()
		sourceErr(w, err)
		if w.Code != want {
			t.Errorf("%v = %d, want %d", err, w.Code, want)
		}
	}
}
