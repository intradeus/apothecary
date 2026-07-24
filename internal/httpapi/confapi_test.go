package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
defaults: { harness: fake, model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: plan,       type: agent, agent: planner, input: [ticket], output: artifacts/plan.md }
  - { id: plan_gate,  type: human_gate, ping: plan_approval, payload: artifacts/plan.md,
      on_reject: { to: plan, max: 3, feed: feedback } }
  - { id: build,      type: agent, agent: builder, input: [ticket, artifacts/plan.md] }
  - { id: test,       type: command, run_ref: verify, on_fail: { to: build, max: 5, feed: output } }
  - { id: mr,         type: forge_mr, body: artifacts/plan.md }
  - { id: final_gate, type: human_gate, ping: final_review, payload: mr_url, auto_complete: mr_merged }
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
	if _, err := conf.SaveFlow(t.Context(), "v1", v1FlowDoc, nil); err != nil {
		t.Fatal(err)
	}
	resolver := resolve.New(conf, reg, "", 8080)
	t.Cleanup(resolver.Close)

	store := event.NewMemStore()
	chreg := channels.NewRegistry()
	e := engine.New(engine.Config{}, store, resolver, conf.FlowSource(), resolve.Settings{Conf: conf}, nil)
	t.Cleanup(e.Shutdown)

	srv := New(e, store, chreg, "")
	srv.Pings = EventPingStore{Store: store}
	srv.Conf = conf
	srv.Kinds = reg
	srv.Ops = resolver
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &confEnv{env: &env{e: e, store: store, srv: srv, ts: ts}, conf: conf, resolver: resolver}
}

// waitPingType polls the inbox for a pending ping of the given type.
func (x *confEnv) waitPingType(t *testing.T, ptype string) string {
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
			if p.Type == ptype {
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
	forgeID := mk("forge", "f", map[string]string{"token": "glpat-PLANTED-forge-token"})
	provID := mk("provisioner", "c", nil)
	// Model auth is harness-owned: the second planted secret rides in
	// auth_json, exercising the real credential path.
	harnessID := mk("harness", "h", map[string]string{
		"auth_json": `{"openrouter":{"type":"api","key":"sk-PLANTED-provider-key"}}`,
	})

	// Repo picker.
	resp, data := x.do(t, "GET", "/api/integrations/"+forgeID+"/repos", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("repos = %d: %s", resp.StatusCode, data)
	}
	var repos []kinds.RepoInfo
	_ = json.Unmarshal(data, &repos)
	if len(repos) == 0 {
		t.Fatal("no repos to pick")
	}

	resp, data = x.do(t, "POST", "/api/projects", map[string]any{
		"name": "web", "forge_id": forgeID, "provisioner_id": provID,
		"harness_ids": []string{harnessID},
		"repo": map[string]string{
			"url": repos[0].URL, "locator": repos[0].Locator, "default_branch": repos[0].DefaultBranch,
		},
		"commands":     map[string]string{"verify": "true"},
		"workspace":    map[string]any{"template": "dev"},
		"default_flow": "v1",
	}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project = %d: %s", resp.StatusCode, data)
	}
	var p struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &p)

	// Agent library (DESIGN3 §1): the v1 flow's agents must exist on the
	// project — agents live in the DB, not the target repo.
	for _, name := range []string{"planner", "builder", "reviewer"} {
		resp, data := x.do(t, "PUT", "/api/projects/"+p.ID+"/agents/"+name, map[string]any{
			"description": name + " agent", "read_only": name != "builder",
			"allow_bash": name == "builder", "prompt": "You are the " + name + ".",
		}, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("create agent %s = %d: %s", name, resp.StatusCode, data)
		}
	}
	return p.ID
}

func TestConfigPlaneCRUDAndMasking(t *testing.T) {
	x := newConfEnv(t)
	projectID := x.wizard(t)

	// GET /api/kinds serves descriptors for the generic forms.
	resp, data := x.do(t, "GET", "/api/kinds", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"port":"forge"`) {
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
	var forgeID string
	for _, in := range list {
		if in.Port == "forge" {
			forgeID = in.ID
		}
	}
	resp, data = x.do(t, "PUT", "/api/integrations/"+forgeID, map[string]any{"name": "renamed"}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("update = %d: %s", resp.StatusCode, data)
	}
	in, err := x.conf.GetIntegration(t.Context(), forgeID)
	if err != nil || in.Secrets["token"] != "glpat-PLANTED-forge-token" {
		t.Fatalf("secret lost on update: %v %v", in.Secrets, err)
	}

	// Preflight passes against the fake forge.
	resp, data = x.do(t, "POST", "/api/projects/"+projectID+"/preflight", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"ok":true`) {
		t.Fatalf("preflight = %d: %s", resp.StatusCode, data)
	}

	// Settings roundtrip + validation.
	resp, _ = x.do(t, "PUT", "/api/settings", map[string]any{
		"max_concurrent_runs": 2, "max_environments": 4,
		"destroy_completed_after": "24h", "destroy_abandoned": "never",
	}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("settings put = %d", resp.StatusCode)
	}
	resp, data = x.do(t, "GET", "/api/settings", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"max_concurrent_runs":2`) {
		t.Fatalf("settings get = %d: %s", resp.StatusCode, data)
	}
	resp, _ = x.do(t, "PUT", "/api/settings", map[string]any{
		"max_concurrent_runs": 2, "max_environments": 4,
		"destroy_completed_after": "24h", "destroy_abandoned": "sometimes",
	}, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("invalid settings = %d, want 400", resp.StatusCode)
	}

	// Flow validate: line-precise errors verbatim.
	resp, data = x.do(t, "POST", "/api/flows/validate", map[string]string{
		"yaml": "name: bad\ndefaults: { harness: fake, model: nope }\nstages: []\n",
	}, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), "provider/model") {
		t.Fatalf("validate = %d: %s", resp.StatusCode, data)
	}

	// Flow versions accumulate content-addressed history.
	var v1row confstore.FlowRow
	_, data = x.do(t, "GET", "/api/flows/v1", nil, nil)
	_ = json.Unmarshal(data, &v1row)
	resp, _ = x.do(t, "PUT", "/api/flows/v1", map[string]string{"yaml": v1row.YAML + "# tweak\n"}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("flow put = %d", resp.StatusCode)
	}
	_, data = x.do(t, "GET", "/api/flows/v1/versions", nil, nil)
	var versions []confstore.FlowVersion
	_ = json.Unmarshal(data, &versions)
	if len(versions) != 2 {
		t.Fatalf("versions = %d, want 2", len(versions))
	}
	resp, data = x.do(t, "GET", "/api/flow-versions/"+v1row.Hash, nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), v1row.Hash) {
		t.Fatalf("flow-version = %d: %s", resp.StatusCode, data)
	}

	// Delete rules surface as 409s naming the referrer.
	resp, data = x.do(t, "DELETE", "/api/integrations/"+forgeID, nil, nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(data), "web") {
		t.Fatalf("referenced delete = %d: %s", resp.StatusCode, data)
	}
	resp, _ = x.do(t, "DELETE", "/api/flows/v1", nil, nil)
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
	// the API like the CPUI would.
	for _, gate := range []string{"plan_approval", "final_review"} {
		p := x.waitPingType(t, gate)
		resp, data := x.do(t, "POST", "/api/pings/"+p+"/answer", map[string]string{"answer": "approve"}, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("answer %s = %d: %s", gate, resp.StatusCode, data)
		}
	}
	x.e.Wait(runID)

	planted := []string{"glpat-PLANTED-forge-token", "sk-PLANTED-provider-key"}
	scan := func(where string, blob []byte) {
		for _, s := range planted {
			if strings.Contains(string(blob), s) {
				t.Errorf("SECRET LEAK in %s: %q visible", where, s)
			}
		}
	}

	// Every read endpoint.
	for _, path := range []string{
		"/api/kinds", "/api/integrations", "/api/projects", "/api/projects/" + projectID,
		"/api/flows", "/api/flows/v1", "/api/settings", "/api/runs",
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

	// Unshadowed: the built-in serves with the flag.
	resp, data := x.do(t, "GET", "/api/flows/default", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"builtin":true`) {
		t.Fatalf("get default = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", "/api/flows", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"name":"default"`) {
		t.Fatalf("list = %d: %s", resp.StatusCode, data)
	}

	// Saving under the built-in name creates the editable override.
	override := "name: default\ndefaults: { harness: fake, model: anthropic/claude-sonnet-4-6 }\nstages:\n  - { id: s, type: command, run: \"true\" }\n"
	resp, data = x.do(t, "PUT", "/api/flows/default", map[string]string{"yaml": override}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("shadow put = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", "/api/flows/default", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"shadows_builtin":true`) ||
		strings.Contains(string(data), `"builtin":true`) {
		t.Fatalf("shadowed get = %d: %s", resp.StatusCode, data)
	}

	// Deleting the override resets to the built-in; deleting again is a
	// clear refusal, not a silent 404.
	resp, _ = x.do(t, "DELETE", "/api/flows/default", nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("override delete = %d", resp.StatusCode)
	}
	resp, data = x.do(t, "GET", "/api/flows/default", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"builtin":true`) {
		t.Fatalf("post-reset get = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "DELETE", "/api/flows/default", nil, nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(data), "built-in") {
		t.Fatalf("builtin delete = %d: %s", resp.StatusCode, data)
	}
}

// TestAgentLibraryRoutes covers the DESIGN3 §1 API surface: CRUD +
// versions + delete guard + cross-project copy + repo import.
func TestAgentLibraryRoutes(t *testing.T) {
	x := newConfEnv(t)
	projectID := x.wizard(t)

	// List: the wizard seeded planner/builder/reviewer; the compiled-in
	// defaults fill the rest of the library (committer, redactor) flagged
	// builtin — authored names shadow their built-in counterparts.
	resp, data := x.do(t, "GET", "/api/projects/"+projectID+"/agents", nil, nil)
	var agents []struct {
		confstore.Agent
		Builtin bool `json:"builtin"`
	}
	_ = json.Unmarshal(data, &agents)
	authored, builtins := 0, 0
	for _, a := range agents {
		if a.Builtin {
			builtins++
		} else {
			authored++
		}
	}
	if resp.StatusCode != 200 || authored != 3 || builtins != 2 {
		t.Fatalf("list = %d (authored %d, builtin %d): %s", resp.StatusCode, authored, builtins, data)
	}

	// Update bumps the hash and accumulates a version.
	var before confstore.Agent
	_, data = x.do(t, "GET", "/api/projects/"+projectID+"/agents/planner", nil, nil)
	_ = json.Unmarshal(data, &before)
	resp, data = x.do(t, "PUT", "/api/projects/"+projectID+"/agents/planner", map[string]any{
		"description": "plans harder", "read_only": true, "prompt": "You are the planner, v2.",
	}, nil)
	var after confstore.Agent
	_ = json.Unmarshal(data, &after)
	if resp.StatusCode != 200 || after.Hash == before.Hash {
		t.Fatalf("update = %d, hash %s -> %s", resp.StatusCode, before.Hash, after.Hash)
	}
	_, data = x.do(t, "GET", "/api/projects/"+projectID+"/agents/planner/versions", nil, nil)
	var versions []confstore.AgentVersion
	_ = json.Unmarshal(data, &versions)
	if len(versions) != 2 {
		t.Fatalf("versions = %d, want 2: %s", len(versions), data)
	}
	resp, data = x.do(t, "GET", "/api/agent-versions/"+before.Hash, nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), before.Hash) {
		t.Fatalf("agent-version = %d: %s", resp.StatusCode, data)
	}

	// Delete: planner is referenced by the default flow but shadows a
	// built-in, so deleting the override is allowed (reset to default —
	// the flow keeps resolving through the fallback). The referenced-and-
	// unshadowed refusal is covered in confstore's delete-guard test.
	resp, data = x.do(t, "DELETE", "/api/projects/"+projectID+"/agents/planner", nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("builtin-shadow delete = %d: %s", resp.StatusCode, data)
	}
	// Restore the authored planner (the copy section below copies it).
	x.do(t, "PUT", "/api/projects/"+projectID+"/agents/planner", map[string]any{
		"description": "planner agent", "read_only": true, "prompt": "You are the planner, v2.",
	}, nil)
	x.do(t, "PUT", "/api/projects/"+projectID+"/agents/spare", map[string]any{"prompt": "spare"}, nil)
	resp, _ = x.do(t, "DELETE", "/api/projects/"+projectID+"/agents/spare", nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("spare delete = %d", resp.StatusCode)
	}

	// Copy into a second project; collision refuses.
	var p2 struct {
		ID string `json:"id"`
	}
	var intgs []confstore.Integration
	_, data = x.do(t, "GET", "/api/integrations", nil, nil)
	_ = json.Unmarshal(data, &intgs)
	ids := map[string]string{}
	for _, in := range intgs {
		ids[in.Port] = in.ID
	}
	resp, data = x.do(t, "POST", "/api/projects", map[string]any{
		"name": "web2", "forge_id": ids["forge"], "provisioner_id": ids["provisioner"],
		"harness_ids": []any{ids["harness"]},
		"repo":        map[string]string{"url": "https://forge.local/dev/fake-repo.git", "locator": "1", "default_branch": "main"},
		"workspace":   map[string]any{"template": "dev"}, "default_flow": "v1",
	}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("web2 = %d: %s", resp.StatusCode, data)
	}
	_ = json.Unmarshal(data, &p2)
	resp, data = x.do(t, "POST", "/api/projects/"+p2.ID+"/agents/copy", map[string]any{
		"from_project_id": projectID, "names": []string{"planner", "builder"},
	}, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), "planner") {
		t.Fatalf("copy = %d: %s", resp.StatusCode, data)
	}
	resp, _ = x.do(t, "POST", "/api/projects/"+p2.ID+"/agents/copy", map[string]any{
		"from_project_id": projectID, "names": []string{"planner"},
	}, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("copy collision = %d", resp.StatusCode)
	}

}

// TestAgentDeclsAndFlowWarnings covers the I/O contract surfaces (DESIGN4 §1):
// PUT/GET pass decls through, built-in rows carry their decls, GET /api/agents
// serves the project-independent library, and /api/flows/validate reports
// binding problems as advisory warnings (never blocking the save).
func TestAgentDeclsAndFlowWarnings(t *testing.T) {
	x := newConfEnv(t)
	projectID := x.wizard(t)

	// PUT an authored agent carrying decls; GET it back and confirm round-trip.
	resp, data := x.do(t, "PUT", "/api/projects/"+projectID+"/agents/custom", map[string]any{
		"prompt": "do it",
		"inputs": []map[string]any{
			{"name": "plan", "description": "the plan"},
			{"name": "feedback", "optional": true},
		},
		"outputs": []map[string]any{{"name": "out", "path": "artifacts/out.md"}},
	}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("put custom = %d: %s", resp.StatusCode, data)
	}
	var got confstore.Agent
	_, data = x.do(t, "GET", "/api/projects/"+projectID+"/agents/custom", nil, nil)
	_ = json.Unmarshal(data, &got)
	if len(got.Inputs) != 2 || got.Inputs[0].Name != "plan" || !got.Inputs[1].Optional ||
		len(got.Outputs) != 1 || got.Outputs[0].Path != "artifacts/out.md" {
		t.Fatalf("decls did not round-trip: %+v", got)
	}

	// Invalid decls are refused (feedback must be optional).
	resp, _ = x.do(t, "PUT", "/api/projects/"+projectID+"/agents/bad", map[string]any{
		"prompt": "p", "inputs": []map[string]any{{"name": "feedback"}},
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid decl PUT = %d, want 400", resp.StatusCode)
	}

	// GET /api/agents (project-independent) serves built-ins with their decls.
	resp, data = x.do(t, "GET", "/api/agents", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list agents = %d: %s", resp.StatusCode, data)
	}
	var lib []confstore.Agent
	_ = json.Unmarshal(data, &lib)
	var builder *confstore.Agent
	for i := range lib {
		if lib[i].Name == "builder" {
			builder = &lib[i]
		}
	}
	if builder == nil || len(builder.Inputs) == 0 || builder.Inputs[0].Name != "plan" {
		t.Fatalf("built-in builder decls missing: %+v", builder)
	}

	// validate a flow that binds an undeclared input to a built-in agent:
	// ok stays true (save is never blocked), the problem surfaces as a warning.
	badFlow := `name: w
defaults: { harness: fake, model: a/b }
stages:
  - { id: b, type: agent, agent: builder, input: { ghost: x.md }, on_success: done }
`
	resp, data = x.do(t, "POST", "/api/flows/validate", map[string]string{"yaml": badFlow}, nil)
	var vr struct {
		OK       bool     `json:"ok"`
		Warnings []string `json:"warnings"`
	}
	_ = json.Unmarshal(data, &vr)
	if resp.StatusCode != 200 || !vr.OK {
		t.Fatalf("validate = %d ok=%v: %s", resp.StatusCode, vr.OK, data)
	}
	if !strings.Contains(strings.Join(vr.Warnings, " "), "not declared by agent") {
		t.Errorf("warnings = %v, want an undeclared-input warning", vr.Warnings)
	}
}
