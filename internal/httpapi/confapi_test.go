package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/actionspec"
	"apothecary/internal/channels"
	"apothecary/internal/confstore"
	"apothecary/internal/engine"
	"apothecary/internal/event"
	"apothecary/internal/kinds"
	"apothecary/internal/resolve"
	"apothecary/internal/secrets"
)

// v1FlowDoc is this suite's user-created flow (the product ships none):
// gated build ending in an MR — enough to drive both human gates.
const v1FlowDoc = `name: v1
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: planner
    prompt: plan the work
    outputs:
      - { name: plan, type: file }
  - name: builder
    prompt: build the thing
    inputs:
      - { name: plan, type: file, optional: true }
main:
  - { id: plan,       type: agent, harness: $harness, model: $model, agent: planner, output: { plan: { to: artifacts/plan.md, type: file } } }
  - { id: plan_gate,  type: human_gate, question: "Approve the plan?", input: { doc: { from: artifacts/plan.md, type: file } },
      on_reject: { to: plan, max: 3 } }
  - { id: build,      type: agent, harness: $harness, model: $model, agent: builder, input: { plan: { from: artifacts/plan.md, type: file } } }
  - { id: test,       type: command, run_ref: verify, on_fail: { to: build, max: 5 } }
  - { id: mr,         type: action, action: create_mr, input: { body: { from: artifacts/plan.md, type: file } } }
  - { id: final_gate, type: human_gate, question: "Merge when ready.", input: { mr: { from: run.mr.url, type: link } }, auto_approve: mr_merged }
`

// confEnv is the full config-plane stack over Mem + fake kinds: the same
// wiring serve uses, minus Postgres.
type confEnv struct {
	*env
	conf     *confstore.Service
	resolver *resolve.Resolver
}

func newConfEnv(t *testing.T) *confEnv {
	t.Helper()
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	reg := kinds.NewRegistry()
	kinds.RegisterFakes(reg)
	conf := confstore.New(confstore.NewMem(), key)
	conf.KindValid = reg.Valid
	resolver := resolve.New(conf, reg, "", 8080, "")
	t.Cleanup(resolver.Close)

	store := event.NewMemStore()
	chreg := channels.NewRegistry()
	e := engine.New(engine.Config{}, store, resolver, conf.FlowSource(),
		engine.StaticSettings{L: engine.Limits{MaxConcurrentRuns: 3, MaxEnvironments: 6}}, nil)
	e.SetFlowBindings(conf)
	t.Cleanup(e.Shutdown)

	srv := New(e, store, chreg, "")
	srv.Pings = EventPingStore{Store: store}
	srv.Conf = conf
	srv.Kinds = reg
	srv.Ops = resolver
	x := &env{e: e, store: store, srv: srv, reg: chreg}
	x.signIn(t)
	x.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(x.ts.Close)
	return &confEnv{env: x, conf: conf, resolver: resolver}
}

// waitPingType polls the inbox for a pending ping of the given type, skipping
// any ids already handled.
func (x *confEnv) waitPingType(t *testing.T, ptype string, except ...string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, data := x.do(t, "GET", "/api/pings?status=pending", nil, nil)
		var pings []struct {
			PingID string `json:"ping_id"`
			Type   string `json:"type"`
		}
		_ = json.Unmarshal(data, &pings)
		for _, p := range pings {
			if p.Type == ptype && !slices.Contains(except, p.PingID) {
				return p.PingID
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no pending %s ping appeared", ptype)
	return ""
}

// wizard drives the whole UI configuration path over HTTP and returns the
// project id: integrations -> repo pick -> project.
func (x *confEnv) wizard(t *testing.T) string {
	t.Helper()
	mk := func(port, name string, secretVals map[string]string) string {
		resp, data := x.do(t, "POST", "/api/integrations", map[string]any{
			"port": port, "kind": "fake", "name": name, "secrets": secretVals,
		}, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %s integration = %d: %s", port, resp.StatusCode, data)
		}
		var out struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(data, &out)
		return out.ID
	}
	repoAccountID := mk("repository", "f", map[string]string{"token": "glpat-PLANTED-repo-token"})
	provID := mk("provisioner", "c", nil)
	// Model auth is a provider credential the harness references: the second
	// planted secret is one, created through its own API, exercising the
	// real credential path (and its listing, which must never echo it).
	resp, data := x.do(t, "POST", "/api/credentials", map[string]any{"provider": "openrouter", "key": "sk-PLANTED-provider-key"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create credential = %d: %s", resp.StatusCode, data)
	}
	harnessID := mk("harness", "h", nil)

	// Repo picker.
	resp, data = x.do(t, "GET", "/api/integrations/"+repoAccountID+"/repos", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("repos = %d: %s", resp.StatusCode, data)
	}
	var repos []kinds.RepoInfo
	_ = json.Unmarshal(data, &repos)
	if len(repos) == 0 {
		t.Fatal("no repos to pick")
	}

	resp, data = x.do(t, "POST", "/api/projects", map[string]any{
		"name": "web", "repo_account_id": repoAccountID, "provisioner_id": provID,
		"harness_ids": []string{harnessID},
		"repo": map[string]string{
			"url": repos[0].URL, "locator": repos[0].Locator, "default_branch": repos[0].DefaultBranch,
		},
		"commands":  map[string]string{"verify": "true"},
		"workspace": map[string]any{"template": "dev"},
		// A project is created on the built-in template: flows belong to a
		// project, so its own cannot exist before it does.
		"default_flow": "builder",
	}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project = %d: %s", resp.StatusCode, data)
	}
	var p struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &p)

	// Now the project can own a flow. No agents to seed: since v3 they live
	// in the flow itself (FLOWLANG §1).
	if _, err := x.conf.SaveFlow(t.Context(), p.ID, "v1", v1FlowDoc, nil); err != nil {
		t.Fatal(err)
	}
	resp, data = x.do(t, "GET", "/api/projects/"+p.ID, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get project = %d: %s", resp.StatusCode, data)
	}
	var full map[string]any
	_ = json.Unmarshal(data, &full)
	full["default_flow"] = "v1"
	resp, data = x.do(t, "PUT", "/api/projects/"+p.ID, full, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("point project at its flow = %d: %s", resp.StatusCode, data)
	}
	return p.ID
}

func TestConfigPlaneCRUDAndMasking(t *testing.T) {
	x := newConfEnv(t)
	projectID := x.wizard(t)

	// GET /api/kinds serves descriptors for the generic forms.
	resp, data := x.do(t, "GET", "/api/kinds", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"port":"repository"`) {
		t.Fatalf("kinds = %d: %s", resp.StatusCode, data)
	}

	// Masking: list + get show masks, never values.
	resp, data = x.do(t, "GET", "/api/integrations", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	if strings.Contains(string(data), "PLANTED") {
		t.Fatalf("integration list leaks secrets: %s", data)
	}
	if !strings.Contains(string(data), "••••") {
		t.Fatalf("no mask in list: %s", data)
	}

	// Update with absent secret keeps it (§3): rename only, then verify the
	// stored secret still decrypts.
	var list []integrationJSON
	_ = json.Unmarshal(data, &list)
	var repoAccountID string
	for _, in := range list {
		if in.Port == "repository" {
			repoAccountID = in.ID
		}
	}
	resp, data = x.do(t, "PUT", "/api/integrations/"+repoAccountID, map[string]any{"name": "renamed"}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("update = %d: %s", resp.StatusCode, data)
	}
	in, err := x.conf.GetIntegration(t.Context(), repoAccountID)
	if err != nil || in.Secrets["token"] != "glpat-PLANTED-repo-token" {
		t.Fatalf("secret lost on update: %v %v", in.Secrets, err)
	}

	// Preflight passes against the fake repository host.
	resp, data = x.do(t, "POST", "/api/projects/"+projectID+"/preflight", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"ok":true`) {
		t.Fatalf("preflight = %d: %s", resp.StatusCode, data)
	}

	// Settings roundtrip + validation.
	resp, _ = x.do(t, "PUT", "/api/settings", map[string]any{
		"destroy_completed_after": "24h", "destroy_abandoned": "never",
	}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("settings put = %d", resp.StatusCode)
	}
	resp, data = x.do(t, "GET", "/api/settings", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"destroy_completed_after":"24h"`) {
		t.Fatalf("settings get = %d: %s", resp.StatusCode, data)
	}
	// The capacity caps are the deployment's, not a setting.
	resp, data = x.do(t, "GET", "/api/limits", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"max_concurrent_runs"`) {
		t.Fatalf("limits get = %d: %s", resp.StatusCode, data)
	}
	resp, _ = x.do(t, "PUT", "/api/settings", map[string]any{
		"destroy_completed_after": "24h", "destroy_abandoned": "sometimes",
	}, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("invalid settings = %d, want 400", resp.StatusCode)
	}

	// Flow validate: line-precise errors verbatim.
	resp, data = x.do(t, "POST", "/api/flows/validate", map[string]string{
		"yaml": "name: bad\nversion: 5\nparameters: { harness: fake, model: nope }\nmain: []\n",
	}, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), "provider/model") {
		t.Fatalf("validate = %d: %s", resp.StatusCode, data)
	}

	// Flow versions accumulate content-addressed history.
	var v1row confstore.FlowRow
	_, data = x.do(t, "GET", "/api/projects/"+projectID+"/flows/v1", nil, nil)
	_ = json.Unmarshal(data, &v1row)
	resp, _ = x.do(t, "PUT", "/api/projects/"+projectID+"/flows/v1", map[string]string{"yaml": v1row.YAML + "# tweak\n"}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("flow put = %d", resp.StatusCode)
	}
	_, data = x.do(t, "GET", "/api/projects/"+projectID+"/flows/v1/versions", nil, nil)
	var versions []confstore.FlowVersion
	_ = json.Unmarshal(data, &versions)
	if len(versions) != 2 {
		t.Fatalf("versions = %d, want 2", len(versions))
	}
	resp, data = x.do(t, "GET", "/api/projects/"+projectID+"/flow-versions/"+v1row.Hash, nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), v1row.Hash) {
		t.Fatalf("flow-version = %d: %s", resp.StatusCode, data)
	}

	// Delete rules surface as 409s naming the referrer.
	resp, data = x.do(t, "DELETE", "/api/integrations/"+repoAccountID, nil, nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(data), "web") {
		t.Fatalf("referenced delete = %d: %s", resp.StatusCode, data)
	}
	resp, _ = x.do(t, "DELETE", "/api/projects/"+projectID+"/flows/v1", nil, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("default_flow delete = %d, want 409", resp.StatusCode)
	}
}

// The M7 acceptance leak test: plant secret values, configure everything
// through the API, drive a full fake run, then grep every API response and
// every event payload for the planted values — none may appear.
func TestNoSecretLeaksAnywhere(t *testing.T) {
	x := newConfEnv(t)
	projectID := x.wizard(t)

	runID := x.runOnProject(t, projectID, "Add search")

	// The seeded v1 flow parks at its two human gates — answer them over
	// the API like the CPUI would. Gates are one kind now, so they are told
	// apart by the ping already answered, not by type.
	var answered []string
	for range 2 {
		p := x.waitPingType(t, engine.PingGate, answered...)
		answered = append(answered, p)
		resp, data := x.do(t, "POST", "/api/pings/"+p+"/answer", map[string]string{"answer": "approve"}, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("answer %s = %d: %s", p, resp.StatusCode, data)
		}
	}
	x.e.Wait(runID)

	planted := []string{"glpat-PLANTED-repo-token", "sk-PLANTED-provider-key"}
	scan := func(where string, blob []byte) {
		for _, s := range planted {
			if strings.Contains(string(blob), s) {
				t.Errorf("SECRET LEAK in %s: %q visible", where, s)
			}
		}
	}

	// Every read endpoint.
	for _, path := range []string{
		"/api/kinds", "/api/integrations", "/api/credentials", "/api/providers", "/api/projects", "/api/projects/" + projectID,
		"/api/flows", "/api/projects/" + projectID + "/flows/v1", "/api/settings", "/api/runs",
		"/api/runs/" + runID, "/api/pings?status=pending",
	} {
		resp, data := x.do(t, "GET", path, nil, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s = %d", path, resp.StatusCode)
		}
		scan("GET "+path, data)
	}

	// Every event payload of the completed run.
	evs, err := x.store.Events(t.Context(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 {
		t.Fatal("no events recorded")
	}
	for _, ev := range evs {
		blob, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		scan(fmt.Sprintf("event %s seq %d", ev.Type, ev.Seq), blob)
	}
}

// TestBuiltinDefaultsAPI covers the compiled-in defaults layering over
// the flows API: served when unshadowed, shadowed by a save (copy-on-
// write), reset by deleting the override, undeletable themselves.
func TestBuiltinDefaultsAPI(t *testing.T) {
	x := newConfEnv(t)
	projectID := x.wizard(t)

	// Unadopted: the template serves with the flag, for THIS project.
	resp, data := x.do(t, "GET", "/api/projects/"+projectID+"/flows/builder", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"builtin":true`) {
		t.Fatalf("get default = %d: %s", resp.StatusCode, data)
	}
	// The project's own listing offers the template it has not adopted; the
	// cross-project overview lists only what projects actually own.
	resp, data = x.do(t, "GET", "/api/projects/"+projectID+"/flows", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"name":"builder"`) {
		t.Fatalf("project list = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", "/api/flow-templates", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"builtin":true`) {
		t.Fatalf("templates = %d: %s", resp.StatusCode, data)
	}

	// Saving under the template's name creates this project's own copy.
	override := "name: builder\nversion: 5\nparameters: { harness: fake, model: fake/model }\nmain:\n  - { id: s, type: command, run: \"true\" }\n"
	resp, data = x.do(t, "PUT", "/api/projects/"+projectID+"/flows/builder", map[string]string{"yaml": override}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("shadow put = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", "/api/projects/"+projectID+"/flows/builder", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"shadows_builtin":true`) ||
		strings.Contains(string(data), `"builtin":true`) {
		t.Fatalf("shadowed get = %d: %s", resp.StatusCode, data)
	}

	// Deleting the override resets to the built-in; deleting again is a
	// clear refusal, not a silent 404.
	resp, _ = x.do(t, "DELETE", "/api/projects/"+projectID+"/flows/builder", nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("override delete = %d", resp.StatusCode)
	}
	resp, data = x.do(t, "GET", "/api/projects/"+projectID+"/flows/builder", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"builtin":true`) {
		t.Fatalf("post-reset get = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "DELETE", "/api/projects/"+projectID+"/flows/builder", nil, nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(data), "built-in") {
		t.Fatalf("builtin delete = %d: %s", resp.StatusCode, data)
	}
}

// TestCopyFlowTemplateAPI covers the template's own copy route: adoption
// under its own name, a renamed copy, and the refusals (missing target,
// unknown template, name already taken).
func TestCopyFlowTemplateAPI(t *testing.T) {
	x := newConfEnv(t)
	projectID := x.wizard(t)

	resp, data := x.do(t, "POST", "/api/flow-templates/builder/copy", map[string]string{"to_project_id": projectID}, nil)
	if resp.StatusCode != http.StatusCreated || !strings.Contains(string(data), `"shadows_builtin":true`) ||
		!strings.Contains(string(data), `"project_id":"`+projectID+`"`) {
		t.Fatalf("adopt = %d: %s", resp.StatusCode, data)
	}
	// From here on the project owns it: the listing shows it as its own,
	// and the template has become this project's shadowed document.
	resp, data = x.do(t, "GET", "/api/projects/"+projectID+"/flows/builder", nil, nil)
	if resp.StatusCode != 200 || strings.Contains(string(data), `"builtin":true`) {
		t.Fatalf("adopted get = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "POST", "/api/flow-templates/builder/copy", map[string]string{"to_project_id": projectID}, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second adoption = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "POST", "/api/flow-templates/builder/copy", map[string]string{"to_project_id": projectID, "name": "builder-fork"}, nil)
	if resp.StatusCode != http.StatusCreated || !strings.Contains(string(data), `"name":"builder-fork"`) ||
		strings.Contains(string(data), `"shadows_builtin":true`) {
		t.Fatalf("renamed copy = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "POST", "/api/flow-templates/builder/copy", map[string]string{}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no target = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "POST", "/api/flow-templates/nope/copy", map[string]string{"to_project_id": projectID}, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown template = %d: %s", resp.StatusCode, data)
	}
}

// A project configures a flow without touching it: the binding has its own
// endpoints, a template is configurable without being copied, and a bad
// binding is refused naming the parameter.
func TestFlowBindingAPI(t *testing.T) {
	x := newConfEnv(t)
	projectID := x.wizard(t)
	base := "/api/projects/" + projectID
	resp, data := x.do(t, "GET", base+"/flow-bindings", nil, nil)
	if resp.StatusCode != 200 || strings.TrimSpace(string(data)) != "[]" {
		t.Fatalf("bindings before any = %d: %s", resp.StatusCode, data)
	}
	if resp, _ = x.do(t, "GET", base+"/flows/builder/binding", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unbound get = %d", resp.StatusCode)
	}
	var b struct {
		ProjectID string            `json:"project_id"`
		FlowName  string            `json:"flow_name"`
		Params    map[string]string `json:"params"`
		UpdatedAt string            `json:"updated_at"`
	}
	resp, data = x.do(t, "PUT", base+"/flows/builder/binding",
		map[string]any{"params": map[string]string{"plan_gate": "false", "conflicts_start": "manual", "verify": ""}}, nil)
	if err := json.Unmarshal(data, &b); err != nil || resp.StatusCode != 200 || b.ProjectID != projectID || b.FlowName != "builder" ||
		len(b.Params) != 2 || b.Params["plan_gate"] != "false" || b.Params["conflicts_start"] != "manual" || b.UpdatedAt == "" {
		t.Fatalf("put = %d %v: %s", resp.StatusCode, err, data)
	}
	resp, data = x.do(t, "GET", base+"/flows/builder/binding", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"conflicts_start":"manual"`) {
		t.Fatalf("get = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", base+"/flow-bindings", nil, nil)
	if resp.StatusCode != 200 || strings.Count(string(data), `"flow_name"`) != 1 {
		t.Fatalf("list = %d: %s", resp.StatusCode, data)
	}
	// The template is still the template: untouched, and describing what
	// may be bound and what its runs are started on.
	var fr struct {
		Builtin    bool   `json:"builtin"`
		OperatesOn string `json:"operates_on"`
		Parameters []struct {
			Name  string `json:"name"`
			Known bool   `json:"known"`
		} `json:"parameters"`
		Phases []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"phases"`
	}
	resp, data = x.do(t, "GET", base+"/flows/builder", nil, nil)
	if err := json.Unmarshal(data, &fr); err != nil || resp.StatusCode != 200 || !fr.Builtin || len(fr.Parameters) < 7 ||
		fr.Parameters[0].Name != "harness" || !fr.Parameters[0].Known || len(fr.Phases) < 2 || fr.Phases[0].ID != "main" || fr.Phases[0].Kind != "main" || fr.OperatesOn != "ticket" {
		t.Fatalf("flow = %d %v: %s", resp.StatusCode, err, data)
	}
	// Refusals name the parameter, and leave the binding as it was.
	for _, bad := range []map[string]string{{"nope": "x"}, {"conflicts_start": "sometimes"}, {"plan_gate": "maybe"}, {"timeout": "soon"}} {
		resp, data = x.do(t, "PUT", base+"/flows/builder/binding", map[string]any{"params": bad}, nil)
		var name string
		for k := range bad {
			name = k
		}
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), name) {
			t.Errorf("put %v = %d: %s", bad, resp.StatusCode, data)
		}
	}
	if resp, data = x.do(t, "GET", base+"/flows/builder/binding", nil, nil); resp.StatusCode != 200 || !strings.Contains(string(data), `"plan_gate":"false"`) {
		t.Errorf("binding after refusals = %d: %s", resp.StatusCode, data)
	}
	if resp, _ = x.do(t, "PUT", "/api/projects/nope/flows/builder/binding", map[string]any{"params": map[string]string{"plan_gate": "false"}}, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown project = %d", resp.StatusCode)
	}
	if resp, _ = x.do(t, "PUT", base+"/flows/nope/binding", map[string]any{"params": map[string]string{"plan_gate": "false"}}, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown flow = %d", resp.StatusCode)
	}
	// Clearing every value removes the binding; so does a delete.
	resp, data = x.do(t, "PUT", base+"/flows/builder/binding", map[string]any{"params": map[string]string{"plan_gate": " "}}, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"params":{}`) {
		t.Fatalf("clearing put = %d: %s", resp.StatusCode, data)
	}
	if resp, _ = x.do(t, "GET", base+"/flows/builder/binding", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("after clearing = %d", resp.StatusCode)
	}
	if resp, _ = x.do(t, "PUT", base+"/flows/builder/binding", map[string]any{"params": map[string]string{"plan_gate": "false"}}, nil); resp.StatusCode != 200 {
		t.Fatal("re-bind refused")
	}
	if resp, _ = x.do(t, "DELETE", base+"/flows/builder/binding", nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete = %d", resp.StatusCode)
	}
	if resp, _ = x.do(t, "DELETE", base+"/flows/builder/binding", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("second delete = %d", resp.StatusCode)
	}
}

// Every flow response — a created one, a listing's rows and templates —
// carries what its runs are started on and the document's parameters and
// phases, so the workshop can offer what to bind and where to start it
// without parsing YAML.
func TestFlowResponsesDescribeParametersAndPhases(t *testing.T) {
	x := newConfEnv(t)
	projectID := x.wizard(t)
	base := "/api/projects/" + projectID
	doc := "name: knobs\nversion: 5\nparameters: { harness: fake, model: a/b, mode: { type: string, default: confirm, enum: [auto, confirm, manual] } }\nagents: []\n" +
		"main:\n  - { id: s, type: command, run: x, on_success: done }\n" +
		"watchers:\n  - id: conflicts\n    on: { signal: mr_conflicts, start: $mode }\n    stages:\n      - { id: sync, type: action, action: sync_target }\n"
	type described struct {
		Name       string `json:"name"`
		Builtin    bool   `json:"builtin"`
		Parameters []struct {
			Name string `json:"name"`
		} `json:"parameters"`
		OperatesOn string `json:"operates_on"`
		Phases     []struct {
			ID     string `json:"id"`
			Kind   string `json:"kind"`
			Signal string `json:"signal"`
			Start  string `json:"start"`
		} `json:"phases"`
	}
	var fr described
	resp, data := x.do(t, "POST", base+"/flows", map[string]any{"name": "knobs", "yaml": doc}, nil)
	// Parameters come in document order, the omitted engine-known names
	// synthesized after the declared ones.
	if err := json.Unmarshal(data, &fr); err != nil || resp.StatusCode != http.StatusCreated || len(fr.Parameters) != 7 || fr.Parameters[2].Name != "mode" ||
		fr.OperatesOn != "ticket" || len(fr.Phases) != 2 || fr.Phases[0].Kind != "main" || fr.Phases[1].Kind != "watcher" ||
		fr.Phases[1].Signal != "mr_conflicts" || fr.Phases[1].Start != "confirm" {
		t.Fatalf("create = %d %v: %s", resp.StatusCode, err, data)
	}
	var list []described
	resp, data = x.do(t, "GET", base+"/flows", nil, nil)
	if err := json.Unmarshal(data, &list); err != nil || resp.StatusCode != 200 {
		t.Fatalf("list = %d %v: %s", resp.StatusCode, err, data)
	}
	seen := map[string]bool{}
	for _, f := range list {
		seen[f.Name] = true
		switch f.Name {
		case "knobs":
			if f.Builtin || len(f.Parameters) != 7 || len(f.Phases) != 2 {
				t.Errorf("knobs in listing = %+v", f)
			}
		case "builder":
			if !f.Builtin || len(f.Parameters) < 7 || len(f.Phases) < 2 || f.Phases[0].ID != "main" {
				t.Errorf("builder template in listing = %+v", f)
			}
		case "reviewer":
			if !f.Builtin || f.OperatesOn != "merge_request" {
				t.Errorf("reviewer template in listing = %+v", f)
			}
		}
	}
	if !seen["knobs"] || !seen["builder"] || !seen["reviewer"] {
		t.Errorf("listing = %+v", list)
	}
}

// TestFlowValidateReportsErrorsHonestly: since v3 a flow carries its agents,
// so a binding that violates a contract is a parse ERROR — ok:false with the
// reason in `errors`. Advisory findings stay in `warnings` with ok:true.
// (PLAN5 M24: this endpoint used to report contract errors as warnings with
// ok:true, which read to the editor as advice rather than breakage.)
func TestFlowValidateReportsErrorsHonestly(t *testing.T) {
	x := newConfEnv(t)

	const agents = `agents:
  - name: builder
    prompt: build it
    inputs:
      - { name: plan, type: file }
  - name: plain
    prompt: plain
`
	post := func(yaml string) (int, struct {
		OK       bool     `json:"ok"`
		Errors   []string `json:"errors"`
		Warnings []string `json:"warnings"`
	}) {
		var vr struct {
			OK       bool     `json:"ok"`
			Errors   []string `json:"errors"`
			Warnings []string `json:"warnings"`
		}
		resp, data := x.do(t, "POST", "/api/flows/validate", map[string]string{"yaml": yaml}, nil)
		_ = json.Unmarshal(data, &vr)
		return resp.StatusCode, vr
	}

	// Binding an input the agent does not declare: refused, not advised.
	code, vr := post("name: w\nversion: 5\nparameters: { harness: fake, model: a/b }\n" + agents +
		"main:\n  - { id: b, type: agent, harness: $harness, model: $model, agent: builder, input: { ghost: { from: repo:x.md, type: file } }, on_success: done }\n")
	if code != 200 || vr.OK {
		t.Fatalf("validate = %d ok=%v, want ok:false", code, vr.OK)
	}
	if !strings.Contains(strings.Join(vr.Errors, " "), "is not an input of agent") {
		t.Errorf("errors = %v, want an undeclared-input error", vr.Errors)
	}

	// on_reject on an agent that declares no verdict can never fire: also an
	// error now that the contract is in the document.
	code, vr = post("name: r\nversion: 5\nparameters: { harness: fake, model: a/b }\n" + agents +
		"main:\n  - { id: b, type: agent, harness: $harness, model: $model, agent: builder, input: { plan: { from: repo:docs/plan.md, type: file } }, on_success: j }\n" +
		"  - { id: j, type: agent, harness: $harness, model: $model, agent: plain, on_success: done, on_reject: { to: b, max: 1 } }\n")
	if code != 200 || vr.OK {
		t.Fatalf("validate = %d ok=%v, want ok:false", code, vr.OK)
	}
	if !strings.Contains(strings.Join(vr.Errors, " "), "on_reject can never fire") {
		t.Errorf("errors = %v, want a dead-on_reject error", vr.Errors)
	}

	// Reading a path nothing in the flow writes is refused too: a file of
	// the repository says so with repo:.
	code, vr = post("name: n\nversion: 5\nparameters: { harness: fake, model: a/b }\n" + agents +
		"main:\n  - { id: b, type: agent, harness: $harness, model: $model, agent: builder, input: { plan: { from: artifacts/nobody.md, type: file } }, on_success: done }\n")
	if code != 200 || vr.OK {
		t.Fatalf("validate = %d ok=%v, want ok:false", code, vr.OK)
	}
	if !strings.Contains(strings.Join(vr.Errors, " "), "nothing in this flow writes") {
		t.Errorf("errors = %v, want an unwritten-path error", vr.Errors)
	}

	// A legal-but-suspicious document still validates, with the finding as a
	// warning: the gate shows a merge request no route opens.
	code, vr = post("name: ok\nversion: 5\nparameters: { harness: fake, model: a/b }\n" + agents +
		"main:\n  - { id: b, type: agent, harness: $harness, model: $model, agent: plain }\n" +
		"  - { id: g, type: human_gate, question: \"Merge?\", input: { mr: { from: run.mr.url, type: link } }, on_success: done }\n")
	if code != 200 || !vr.OK {
		t.Fatalf("validate = %d ok=%v errors=%v, want ok:true", code, vr.OK, vr.Errors)
	}
	if !strings.Contains(strings.Join(vr.Warnings, " "), "a merge request is not opened on every path") {
		t.Errorf("warnings = %v, want an unopened-merge-request warning", vr.Warnings)
	}
}

// TestActionRegistryRoute: the flow editor's action picker is served from the
// compiled-in registry, with the signature it renders binding rows from.
func TestActionRegistryRoute(t *testing.T) {
	x := newConfEnv(t)
	resp, data := x.do(t, "GET", "/api/actions", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("actions = %d: %s", resp.StatusCode, data)
	}
	var acts []actionspec.Action
	if err := json.Unmarshal(data, &acts); err != nil {
		t.Fatal(err)
	}
	var mr *actionspec.Action
	for i := range acts {
		if acts[i].Slug == actionspec.CreateMR {
			mr = &acts[i]
		}
	}
	if mr == nil {
		t.Fatalf("no %s in %+v", actionspec.CreateMR, acts)
	}
	if mr.Title == "" || mr.Requires != actionspec.RequiresRepository || len(mr.Inputs) != 2 ||
		mr.Inputs[0].Name != "body" || !mr.Inputs[0].Required ||
		mr.Inputs[1].Name != "attachments" || mr.Inputs[1].Type != "files" || mr.Inputs[1].Required {
		t.Errorf("create_mr = %+v", mr)
	}
	if mr.Inputs[0].Type != "file" || mr.Inputs[0].DefaultPath == "" {
		t.Errorf("create_mr body = %+v, want a typed file port with a path to prefill", mr.Inputs[0])
	}
	if len(mr.Outputs) != 0 {
		t.Errorf("create_mr outputs = %+v, want none: its link is run.mr.url", mr.Outputs)
	}
}

// Validation carries the document's parameters and phases with the defaults
// applied, and validates a workshop's bound values when given.
func TestValidateFlowReportsParametersAndPhases(t *testing.T) {
	x := newConfEnv(t)
	doc := "name: p\nversion: 5\nparameters:\n  harness: fake\n  model: a/b\n  mode: { type: string, default: confirm, enum: [auto, confirm, manual] }\n  turns: { type: int, default: 3 }\n" +
		"agents:\n  - { name: b, prompt: build, outputs: [ { name: mr, type: file } ] }\n" +
		"main:\n  - { id: build, type: agent, harness: $harness, model: $model, agent: b, max_turns: $turns, output: { mr: { to: artifacts/mr.md, type: file } } }\n  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } } }\n" +
		"watchers:\n  - id: conflicts\n    on: { signal: mr_conflicts, start: $mode }\n    stages:\n      - { id: sync, type: action, action: sync_target }\n" +
		"outcomes:\n  - { id: shipped, on: mr_merged, then: done }\n"
	var vr struct {
		OK         bool     `json:"ok"`
		Errors     []string `json:"errors"`
		Parameters []struct {
			Name  string `json:"name"`
			Type  string `json:"type"`
			Known bool   `json:"known"`
		} `json:"parameters"`
		Refs []struct {
			Param, Stage, Field string
		} `json:"refs"`
		Phases []struct {
			ID      string `json:"id"`
			Kind    string `json:"kind"`
			Signal  string `json:"signal"`
			Start   string `json:"start"`
			Max     int    `json:"max"`
			Then    string `json:"then"`
			Subject bool   `json:"subject"`
			Stages  int    `json:"stages"`
		} `json:"phases"`
	}
	resp, data := x.do(t, "POST", "/api/flows/validate", map[string]any{"yaml": doc}, nil)
	if err := json.Unmarshal(data, &vr); err != nil || resp.StatusCode != 200 || !vr.OK {
		t.Fatalf("validate = %d %v ok=%v errors=%v", resp.StatusCode, err, vr.OK, vr.Errors)
	}
	if len(vr.Parameters) != 8 || vr.Parameters[0].Name != "harness" || !vr.Parameters[0].Known || vr.Parameters[2].Name != "mode" || vr.Parameters[3].Type != "int" {
		t.Errorf("parameters = %+v", vr.Parameters)
	}
	if len(vr.Refs) != 4 || len(vr.Phases) != 3 || vr.Phases[0].Kind != "main" || vr.Phases[0].Signal != "" || vr.Phases[0].Start != "manual" || vr.Phases[0].Max != -1 || vr.Phases[0].Stages != 2 ||
		vr.Phases[1].Start != "confirm" || !vr.Phases[1].Subject || vr.Phases[1].Max != -1 ||
		vr.Phases[2].Kind != "outcome" || vr.Phases[2].Then != "done" || vr.Phases[2].Stages != 0 || vr.Phases[2].Start != "auto" || vr.Phases[2].Subject {
		t.Errorf("refs = %+v phases = %+v", vr.Refs, vr.Phases)
	}
	// Bound values: a legal one changes the summary, an illegal one is an error.
	resp, data = x.do(t, "POST", "/api/flows/validate", map[string]any{"yaml": doc, "params": map[string]string{"mode": "manual"}}, nil)
	_ = json.Unmarshal(data, &vr)
	if resp.StatusCode != 200 || !vr.OK || vr.Phases[1].Start != "manual" {
		t.Errorf("bound validate = %d ok=%v phases=%+v", resp.StatusCode, vr.OK, vr.Phases)
	}
	resp, data = x.do(t, "POST", "/api/flows/validate", map[string]any{"yaml": doc, "params": map[string]string{"turns": "0"}}, nil)
	_ = json.Unmarshal(data, &vr)
	if resp.StatusCode != 200 || vr.OK || !strings.Contains(strings.Join(vr.Errors, " "), "max_turns must be > 0") {
		t.Errorf("bad binding validate = %d ok=%v errors=%v", resp.StatusCode, vr.OK, vr.Errors)
	}
}
