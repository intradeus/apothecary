package httpapi

import (
	"apothecary/internal/builtin"
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
version: 4
defaults: { harness: fake, model: anthropic/claude-sonnet-4-6 }
agents:
  - name: planner
    prompt: plan the work
    outputs:
      - { name: plan, path: artifacts/plan.md }
  - name: builder
    prompt: build the thing
    inputs:
      - { name: plan, optional: true }
stages:
  - { id: plan,       type: agent, agent: planner }
  - { id: plan_gate,  type: human_gate, question: "Approve the plan?", input: { doc: artifacts/plan.md },
      on_reject: { to: plan, max: 3 } }
  - { id: build,      type: agent, agent: builder, input: { plan: artifacts/plan.md } }
  - { id: test,       type: command, run_ref: verify, on_fail: { to: build, max: 5 } }
  - { id: mr,         type: action, action: create_mr, input: { body: artifacts/plan.md } }
  - { id: final_gate, type: human_gate, question: "Merge when ready.", input: { mr: $MR_URL }, auto_approve: mr_merged }
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
		"commands":  map[string]string{"verify": "true"},
		"workspace": map[string]any{"template": "dev"},
		// A project is created on the built-in template: flows belong to a
		// project, so its own cannot exist before it does.
		"default_flow": builtin.DefaultFlowName,
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
		"yaml": "name: bad\nversion: 4\ndefaults: { harness: fake, model: nope }\nstages: []\n",
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
	resp, data = x.do(t, "DELETE", "/api/integrations/"+forgeID, nil, nil)
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
	resp, data := x.do(t, "GET", "/api/projects/"+projectID+"/flows/default", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"builtin":true`) {
		t.Fatalf("get default = %d: %s", resp.StatusCode, data)
	}
	// The project's own listing offers the template it has not adopted; the
	// cross-project overview lists only what projects actually own.
	resp, data = x.do(t, "GET", "/api/projects/"+projectID+"/flows", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"name":"default"`) {
		t.Fatalf("project list = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", "/api/flow-templates", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"builtin":true`) {
		t.Fatalf("templates = %d: %s", resp.StatusCode, data)
	}

	// Saving under the template's name creates this project's own copy.
	override := "name: default\nversion: 4\ndefaults: { harness: fake, model: anthropic/claude-sonnet-4-6 }\nstages:\n  - { id: s, type: command, run: \"true\" }\n"
	resp, data = x.do(t, "PUT", "/api/projects/"+projectID+"/flows/default", map[string]string{"yaml": override}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("shadow put = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", "/api/projects/"+projectID+"/flows/default", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"shadows_builtin":true`) ||
		strings.Contains(string(data), `"builtin":true`) {
		t.Fatalf("shadowed get = %d: %s", resp.StatusCode, data)
	}

	// Deleting the override resets to the built-in; deleting again is a
	// clear refusal, not a silent 404.
	resp, _ = x.do(t, "DELETE", "/api/projects/"+projectID+"/flows/default", nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("override delete = %d", resp.StatusCode)
	}
	resp, data = x.do(t, "GET", "/api/projects/"+projectID+"/flows/default", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"builtin":true`) {
		t.Fatalf("post-reset get = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "DELETE", "/api/projects/"+projectID+"/flows/default", nil, nil)
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

	resp, data := x.do(t, "POST", "/api/flow-templates/default/copy", map[string]string{"to_project_id": projectID}, nil)
	if resp.StatusCode != http.StatusCreated || !strings.Contains(string(data), `"shadows_builtin":true`) ||
		!strings.Contains(string(data), `"project_id":"`+projectID+`"`) {
		t.Fatalf("adopt = %d: %s", resp.StatusCode, data)
	}
	// From here on the project owns it: the listing shows it as its own,
	// and the template has become this project's shadowed document.
	resp, data = x.do(t, "GET", "/api/projects/"+projectID+"/flows/default", nil, nil)
	if resp.StatusCode != 200 || strings.Contains(string(data), `"builtin":true`) {
		t.Fatalf("adopted get = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "POST", "/api/flow-templates/default/copy", map[string]string{"to_project_id": projectID}, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second adoption = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "POST", "/api/flow-templates/default/copy", map[string]string{"to_project_id": projectID, "name": "default-fork"}, nil)
	if resp.StatusCode != http.StatusCreated || !strings.Contains(string(data), `"name":"default-fork"`) ||
		strings.Contains(string(data), `"shadows_builtin":true`) {
		t.Fatalf("renamed copy = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "POST", "/api/flow-templates/default/copy", map[string]string{}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no target = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "POST", "/api/flow-templates/nope/copy", map[string]string{"to_project_id": projectID}, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown template = %d: %s", resp.StatusCode, data)
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
      - { name: plan }
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
	code, vr := post("name: w\nversion: 4\ndefaults: { harness: fake, model: a/b }\n" + agents +
		"stages:\n  - { id: b, type: agent, agent: builder, input: { ghost: x.md }, on_success: done }\n")
	if code != 200 || vr.OK {
		t.Fatalf("validate = %d ok=%v, want ok:false", code, vr.OK)
	}
	if !strings.Contains(strings.Join(vr.Errors, " "), "not declared by agent") {
		t.Errorf("errors = %v, want an undeclared-input error", vr.Errors)
	}

	// on_reject on an agent that declares no verdict can never fire: also an
	// error now that the contract is in the document.
	code, vr = post("name: r\nversion: 4\ndefaults: { harness: fake, model: a/b }\n" + agents +
		"stages:\n  - { id: b, type: agent, agent: builder, input: { plan: artifacts/p.md }, on_success: j }\n" +
		"  - { id: j, type: agent, agent: plain, on_success: done, on_reject: { to: b, max: 1 } }\n")
	if code != 200 || vr.OK {
		t.Fatalf("validate = %d ok=%v, want ok:false", code, vr.OK)
	}
	if !strings.Contains(strings.Join(vr.Errors, " "), "on_reject can never fire") {
		t.Errorf("errors = %v, want a dead-on_reject error", vr.Errors)
	}

	// A legal-but-suspicious document still validates, with the finding as a
	// warning: nothing produces the path the builder reads.
	code, vr = post("name: ok\nversion: 4\ndefaults: { harness: fake, model: a/b }\n" + agents +
		"stages:\n  - { id: b, type: agent, agent: builder, input: { plan: artifacts/nobody.md }, on_success: done }\n")
	if code != 200 || !vr.OK {
		t.Fatalf("validate = %d ok=%v, want ok:true", code, vr.OK)
	}
	if !strings.Contains(strings.Join(vr.Warnings, " "), "which no stage produces") {
		t.Errorf("warnings = %v, want an unproduced-path warning", vr.Warnings)
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
	if mr.Title == "" || mr.Requires != actionspec.RequiresForge || len(mr.Inputs) != 1 ||
		mr.Inputs[0].Name != "body" || !mr.Inputs[0].Required {
		t.Errorf("create_mr = %+v", mr)
	}
	if len(mr.Outputs) != 1 || mr.Outputs[0].Sentinel != "$MR_URL" {
		t.Errorf("create_mr outputs = %+v", mr.Outputs)
	}
}
