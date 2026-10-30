package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"apothecary/internal/auth"
	"apothecary/internal/engine"
	"apothecary/internal/filestore"
	"apothecary/internal/ports"
	"apothecary/internal/skillstore"
)

// testSkills is a Mem-backed skill library: deps hands it to the engine at
// construction, serve to the API, re-serving the routes (registration is
// gated on the field).
type testSkills struct {
	lib   *skillstore.Store
	fetch skillstore.FetchFunc
}

func newTestSkills(t *testing.T, fetch skillstore.FetchFunc) *testSkills {
	t.Helper()
	return &testSkills{lib: skillstore.New(skillstore.NewMem(), filestore.NewBlobs(t.TempDir())), fetch: fetch}
}

func (k *testSkills) deps(d *engine.Deps) { d.Skills = k.lib }

func (k *testSkills) serve(t *testing.T, x *env) *skillstore.Store {
	t.Helper()
	x.srv.Skills = k.lib
	if k.fetch != nil {
		x.srv.SkillImports = &skillstore.Importer{Store: k.lib, Fetch: k.fetch}
	}
	x.ts.Close()
	x.ts = httptest.NewServer(x.srv.Handler())
	t.Cleanup(x.ts.Close)
	return k.lib
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func skillBody(name, desc string, extra ...SkillFileRequest) SkillRequest {
	files := append([]SkillFileRequest{{Path: "SKILL.md", ContentB64: b64("---\nname: " + name + "\ndescription: " + desc + "\n---\n# " + name + "\n")}}, extra...)
	return SkillRequest{Name: name, Files: files}
}

func decode[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode %T: %v: %s", v, err, data)
	}
	return v
}

func TestSkillsCRUDAndVersions(t *testing.T) {
	skl := newTestSkills(t, nil)
	x := newEnvWith(t, engine.Config{}, skl.deps)
	skl.serve(t, x)

	resp, data := x.do(t, "POST", "/api/skills", skillBody("pdf", "Reads PDFs."), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d: %s", resp.StatusCode, data)
	}
	sk := decode[skillstore.Skill](t, data)
	if sk.Name != "pdf" || sk.Description != "Reads PDFs." || sk.UserID != testUser || len(sk.Files) != 1 {
		t.Errorf("skill = %+v", sk)
	}
	resp, data = x.do(t, "POST", "/api/skills", SkillRequest{Name: "bad", Files: []SkillFileRequest{{Path: "SKILL.md", ContentB64: b64("no frontmatter")}}}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad frontmatter = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "POST", "/api/skills", SkillRequest{Name: "x", Files: []SkillFileRequest{{Path: "SKILL.md", ContentB64: "***"}}}, nil)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "base64") {
		t.Errorf("bad base64 = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", "/api/skills/"+sk.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get = %d: %s", resp.StatusCode, data)
	}
	detail := decode[SkillDetail](t, data)
	if detail.Skill.ID != sk.ID || len(detail.Contents) != 1 || detail.Contents[0].Path != "SKILL.md" || detail.Contents[0].ContentB64 == "" {
		t.Errorf("detail = %+v", detail)
	}
	resp, data = x.do(t, "GET", "/api/skills", nil, nil)
	if list := decode[[]skillstore.Skill](t, data); resp.StatusCode != http.StatusOK || len(list) != 1 {
		t.Errorf("list = %d %s", resp.StatusCode, data)
	}

	resp, data = x.do(t, "POST", "/api/skillsets", SkillsetRequest{Name: "Docs", SkillIDs: []string{sk.ID}}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create set = %d: %s", resp.StatusCode, data)
	}
	set := decode[skillstore.Skillset](t, data)
	if set.HeadSeq != 1 || len(set.SkillIDs) != 1 {
		t.Errorf("set = %+v", set)
	}
	resp, data = x.do(t, "POST", "/api/skillsets", SkillsetRequest{Name: "Nope", SkillIDs: []string{"missing"}}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing member = %d: %s", resp.StatusCode, data)
	}
	// Editing the member cuts v2; the versions routes show both.
	resp, data = x.do(t, "PUT", "/api/skills/"+sk.ID, skillBody("pdf", "Reads PDFs.", SkillFileRequest{Path: "scripts/go.sh", ContentB64: b64("echo")}), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", "/api/skillsets/"+set.ID+"/versions", nil, nil)
	vs := decode[[]skillstore.Version](t, data)
	if resp.StatusCode != http.StatusOK || len(vs) != 2 || vs[0].Seq != 2 || len(vs[0].Members[0].Files) != 2 {
		t.Errorf("versions = %d %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", "/api/skillsets/"+set.ID+"/versions/0", nil, nil)
	if v := decode[skillstore.Version](t, data); resp.StatusCode != http.StatusOK || v.Seq != 2 {
		t.Errorf("head = %d %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", "/api/skillsets/"+set.ID+"/versions/x", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad seq = %d: %s", resp.StatusCode, data)
	}
	// Guards: a listed skill is not deletable; after the set goes, it is.
	resp, data = x.do(t, "DELETE", "/api/skills/"+sk.ID, nil, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("delete member = %d: %s", resp.StatusCode, data)
	}
	if resp, data = x.do(t, "DELETE", "/api/skillsets/"+set.ID, nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete set = %d: %s", resp.StatusCode, data)
	}
	if resp, data = x.do(t, "DELETE", "/api/skills/"+sk.ID, nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete skill = %d: %s", resp.StatusCode, data)
	}
	if resp, _ = x.do(t, "GET", "/api/skills/"+sk.ID, nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("after delete = %d", resp.StatusCode)
	}
}

// Another account sees none of it: lists are theirs alone and ids read as
// not found.
func TestSkillsAreTheOwners(t *testing.T) {
	skl := newTestSkills(t, nil)
	x := newEnvWith(t, engine.Config{}, skl.deps)
	skl.serve(t, x)
	_, data := x.do(t, "POST", "/api/skills", skillBody("pdf", "Reads PDFs."), nil)
	sk := decode[skillstore.Skill](t, data)
	_, data = x.do(t, "POST", "/api/skillsets", SkillsetRequest{Name: "Docs", SkillIDs: []string{sk.ID}}, nil)
	set := decode[skillstore.Skillset](t, data)

	now := time.Now().UTC()
	if err := x.users.CreateUser(context.Background(), auth.User{ID: "usr-other", Handle: "other", Email: "other@example.test", Name: "Other", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	other, _, err := x.srv.Auth.NewToken(context.Background(), "usr-other", "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	hdr := map[string]string{"Authorization": "Bearer " + other}
	for _, p := range []string{"/api/skills/" + sk.ID, "/api/skillsets/" + set.ID, "/api/skillsets/" + set.ID + "/versions"} {
		if resp, _ := x.do(t, "GET", p, nil, hdr); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s as other = %d, want 404", p, resp.StatusCode)
		}
	}
	if resp, data := x.do(t, "GET", "/api/skills", nil, hdr); resp.StatusCode != http.StatusOK || strings.TrimSpace(string(data)) != "[]" {
		t.Errorf("other's list = %d %s", resp.StatusCode, data)
	}
	// Their own skillset cannot list my skill.
	if resp, data := x.do(t, "POST", "/api/skillsets", SkillsetRequest{Name: "Theirs", SkillIDs: []string{sk.ID}}, hdr); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("other's set with my skill = %d %s", resp.StatusCode, data)
	}
}

// A run started with skillsets carries the pins; the fake harness records
// the install.
func TestStartRunWithSkillsets(t *testing.T) {
	skl := newTestSkills(t, nil)
	x := newEnvWith(t, engine.Config{}, skl.deps)
	skl.serve(t, x)
	_, data := x.do(t, "POST", "/api/skills", skillBody("pdf", "Reads PDFs."), nil)
	sk := decode[skillstore.Skill](t, data)
	_, data = x.do(t, "POST", "/api/skillsets", SkillsetRequest{Name: "Docs", SkillIDs: []string{sk.ID}}, nil)
	set := decode[skillstore.Skillset](t, data)

	ticket := x.newTicket(t, "T", "")
	resp, data := x.do(t, "POST", "/api/tickets/"+ticket+"/runs", map[string]any{"skillsets": []map[string]any{{"id": set.ID}}}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start = %d: %s", resp.StatusCode, data)
	}
	runID := decode[RunRef](t, data).RunID
	x.e.Wait(runID)
	resp, data = x.do(t, "GET", "/api/runs/"+runID, nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), `"skillsets":[{"id":"`+set.ID+`","name":"Docs","seq":1,`) {
		t.Errorf("run detail = %d %s", resp.StatusCode, data)
	}
	// The neutral tree reached the workspace.
	envID := decode[struct {
		Run struct {
			EnvID string `json:"env_id"`
		} `json:"run"`
	}](t, data).Run.EnvID
	if md, err := x.envp.ReadFile(context.Background(), ports.EnvID(envID), "/apothecary/skills/pdf/SKILL.md"); err != nil || !strings.Contains(string(md), "name: pdf") {
		t.Errorf("tree in workspace: %q %v", md, err)
	}
	// An unknown skillset refuses the run.
	resp, data = x.do(t, "POST", "/api/tickets/"+ticket+"/runs", map[string]any{"skillsets": []map[string]any{{"id": "nope"}}}, nil)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "skillset nope") {
		t.Errorf("unknown skillset = %d: %s", resp.StatusCode, data)
	}
}

func TestSkillImportAndRefresh(t *testing.T) {
	version := "1"
	fetch := func(_ context.Context, src skillstore.Source) (skillstore.Fetched, error) {
		md := func(n string) ports.TreeFile {
			return ports.TreeFile{Path: n + "/SKILL.md", Data: []byte("---\nname: " + n + "\ndescription: v" + version + "\n---\n")}
		}
		return skillstore.Fetched{Commit: "abc" + version, Files: []ports.TreeFile{md("alpha"), md("beta"), {Path: "README.md", Data: []byte("x")}}}, nil
	}
	skl := newTestSkills(t, fetch)
	x := newEnvWith(t, engine.Config{}, skl.deps)
	skl.serve(t, x)

	resp, data := x.do(t, "POST", "/api/skills/import", SkillImportRequest{URL: "https://github.com/o/r/tree/main/skills"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("import = %d: %s", resp.StatusCode, data)
	}
	res := decode[skillstore.ImportResult](t, data)
	if res.Skillset == nil || res.Skillset.Name != "skills" || len(res.Skills) != 2 || res.Skillset.Source == nil || res.Skillset.Source.Commit != "abc1" {
		t.Fatalf("result = %+v", res)
	}
	if resp, data = x.do(t, "POST", "/api/skills/import", SkillImportRequest{URL: "github.com/o/r/tree/main/skills"}, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("second import = %d: %s", resp.StatusCode, data)
	}
	if resp, data = x.do(t, "POST", "/api/skills/import", SkillImportRequest{URL: "ftp://x"}, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad url = %d: %s", resp.StatusCode, data)
	}
	// Unchanged refresh keeps v1; a changed folder cuts v2.
	resp, data = x.do(t, "POST", "/api/skillsets/"+res.Skillset.ID+"/refresh", nil, nil)
	if out := decode[SkillsetRefreshResult](t, data); resp.StatusCode != http.StatusOK || out.Skillset.HeadSeq != 1 {
		t.Errorf("unchanged refresh = %d %s", resp.StatusCode, data)
	}
	version = "2"
	resp, data = x.do(t, "POST", "/api/skillsets/"+res.Skillset.ID+"/refresh", nil, nil)
	if out := decode[SkillsetRefreshResult](t, data); resp.StatusCode != http.StatusOK || out.Skillset.HeadSeq != 2 || out.Skillset.Source.Commit != "abc2" {
		t.Errorf("changed refresh = %d %s", resp.StatusCode, data)
	}
	// A local skill has nothing to refresh.
	_, data = x.do(t, "POST", "/api/skills", skillBody("local", "Local."), nil)
	local := decode[skillstore.Skill](t, data)
	if resp, data = x.do(t, "POST", "/api/skills/"+local.ID+"/refresh", nil, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("refresh local = %d: %s", resp.StatusCode, data)
	}
}
