package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"apothecary/internal/actionspec"
	"apothecary/internal/agentspec"
	"apothecary/internal/auth"
	"apothecary/internal/builtin"
	"apothecary/internal/confstore"
	"apothecary/internal/flow"
	"apothecary/internal/kinds"
	"apothecary/internal/resolution"
	"apothecary/internal/secrets"
	"apothecary/internal/signalspec"
	"apothecary/internal/wsinfo"
)

// flowResp / agentResp decorate store rows with the built-in layering
// flags (internal/builtin): builtin = served from the binary, no DB row —
// saving under the same name creates the editable override; shadows_builtin
// = a DB row that overrides a built-in name — deleting it resets to the
// compiled-in default.
type flowResp struct {
	confstore.FlowRow
	Builtin        bool `json:"builtin,omitempty"`
	ShadowsBuiltin bool `json:"shadows_builtin,omitempty"`
	// OperatesOn, Parameters and Phases describe the document: what its runs
	// work on (ticket or merge_request), what a project may bind,
	// and its main chain, watchers and outcomes. Absent when the document
	// no longer parses.
	OperatesOn string         `json:"operates_on,omitempty"`
	Parameters []flow.Param   `json:"parameters,omitempty"`
	Phases     []PhaseSummary `json:"phases,omitempty"`
}

// describeFlow presents a row with what a project configures and what its
// runs are started on: the document's operates_on, parameters and phases. A
// row whose document no longer parses is served without them — the editor
// shows the parse error, and the rest of the listing stays usable.
func describeFlow(row confstore.FlowRow, isBuiltin bool) flowResp {
	out := flowResp{FlowRow: row, Builtin: isBuiltin, ShadowsBuiltin: !isBuiltin && builtin.Flow(row.Name) != nil}
	f := builtin.Flow(row.Name)
	if !isBuiltin {
		parsed, err := flow.Parse(row.Name, []byte(row.YAML))
		if err != nil {
			return out
		}
		f = parsed
	}
	if f == nil {
		return out
	}
	out.OperatesOn, out.Parameters, out.Phases = f.OperatesOn, f.Params, phaseSummaries(f)
	return out
}

// builtinFlowRow presents a compiled-in template as a row. projectID is the
// project it is being offered TO — the document has no project of its own
// until that project saves it, and Builtin on the response says which it is.
func builtinFlowRow(projectID, name string) (confstore.FlowRow, bool) {
	y, ok := builtin.FlowYAML(name)
	if !ok {
		return confstore.FlowRow{}, false
	}
	return confstore.FlowRow{ProjectID: projectID, Name: name, YAML: y, Hash: builtin.Flow(name).Hash}, true
}

// Config-plane routes (DESIGN2 §10). Secrets are write-only: requests carry
// plaintext values, responses only masks (§3).

// IntegrationOps is the slice of the resolver these routes need — an
// interface so tests can stub it without a kinds registry.
type IntegrationOps interface {
	TestIntegration(ctx context.Context, id string) (warnings []string, err error)
	ListRepos(ctx context.Context, id string) ([]kinds.RepoInfo, error)
	ListModels(ctx context.Context, id string) ([]kinds.ModelInfo, error)
	// ListBranches lists the branches of a PROJECT's repo (source-branch
	// picker) — project-scoped, so it resolves the repository host + locator
	// itself.
	ListBranches(ctx context.Context, projectID string) ([]kinds.BranchInfo, error)
}

// confErr maps confstore errors to statuses: not found 404, conflicts and
// referenced-deletes 409, validation 400, everything else (DB outages,
// decryption failures) 500.
func confErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, confstore.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, confstore.ErrConflict), errors.Is(err, confstore.ErrReferenced):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, confstore.ErrInvalid), errors.Is(err, resolution.ErrUnresolvable):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// --- kinds ------------------------------------------------------------------

func (s *Server) listKinds(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Kinds.Descriptors())
}

// --- integrations -----------------------------------------------------------

type integrationJSON struct {
	ID        string            `json:"id"`
	Port      string            `json:"port"`
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	Config    map[string]any    `json:"config"`
	Secrets   map[string]string `json:"secrets"` // masked, never values
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

func toIntegrationJSON(in confstore.Integration) integrationJSON {
	return integrationJSON{
		ID: in.ID, Port: in.Port, Kind: in.Kind, Name: in.Name,
		Config: in.Config, Secrets: secrets.MaskAll(in.Secrets),
		CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt,
	}
}

func (s *Server) listIntegrations(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Conf.ListIntegrations(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]integrationJSON, 0, len(rows))
	for _, in := range rows {
		out = append(out, toIntegrationJSON(in))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createIntegration(w http.ResponseWriter, r *http.Request) {
	var body IntegrationRequest
	if !readJSON(w, r, &body) {
		return
	}
	in, err := s.Conf.CreateIntegration(r.Context(), confstore.Integration{
		UserID: auth.UserID(r.Context()),
		Port:   body.Port, Kind: body.Kind, Name: body.Name,
		Config: body.Config, Secrets: body.Secrets,
	})
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toIntegrationJSON(in))
}

func (s *Server) getIntegration(w http.ResponseWriter, r *http.Request) {
	in, err := s.Conf.GetIntegration(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toIntegrationJSON(in))
}

func (s *Server) updateIntegration(w http.ResponseWriter, r *http.Request) {
	// §3 secret merge: a present key replaces, "" deletes, an absent key keeps.
	var body IntegrationUpdate
	if !readJSON(w, r, &body) {
		return
	}
	in, err := s.Conf.UpdateIntegration(r.Context(), r.PathValue("id"), body.Name, body.Config, body.Secrets)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toIntegrationJSON(in))
}

func (s *Server) deleteIntegration(w http.ResponseWriter, r *http.Request) {
	if err := s.Conf.DeleteIntegration(r.Context(), r.PathValue("id")); err != nil {
		confErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testIntegration(w http.ResponseWriter, r *http.Request) {
	if s.Ops == nil {
		writeErr(w, http.StatusServiceUnavailable, "integration testing is not wired")
		return
	}
	warnings, err := s.Ops.TestIntegration(r.Context(), r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusOK, TestResult{Error: err.Error(), Warnings: warnings})
		return
	}
	writeJSON(w, http.StatusOK, TestResult{OK: true, Warnings: warnings})
}

func (s *Server) listIntegrationRepos(w http.ResponseWriter, r *http.Request) {
	if s.Ops == nil {
		writeErr(w, http.StatusServiceUnavailable, "repo listing is not wired")
		return
	}
	repos, err := s.Ops.ListRepos(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	if repos == nil {
		repos = []kinds.RepoInfo{}
	}
	writeJSON(w, http.StatusOK, repos)
}

func (s *Server) listProjectBranches(w http.ResponseWriter, r *http.Request) {
	if s.Ops == nil {
		writeErr(w, http.StatusServiceUnavailable, "branch listing is not wired")
		return
	}
	branches, err := s.Ops.ListBranches(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	if branches == nil {
		branches = []kinds.BranchInfo{}
	}
	writeJSON(w, http.StatusOK, branches)
}

func (s *Server) listIntegrationModels(w http.ResponseWriter, r *http.Request) {
	if s.Ops == nil {
		writeErr(w, http.StatusServiceUnavailable, "model listing is not wired")
		return
	}
	models, err := s.Ops.ListModels(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	if models == nil {
		models = []kinds.ModelInfo{}
	}
	writeJSON(w, http.StatusOK, models)
}

// --- projects ---------------------------------------------------------------

type projectBody struct {
	Name          string `json:"name"`
	RepoAccountID string `json:"repo_account_id"`
	ProvisionerID string `json:"provisioner_id"`
	// HarnessIDs is the project's set of available harness integrations (no
	// default — the flow names the harness kind).
	HarnessIDs []string             `json:"harness_ids"`
	Repo       confstore.RepoConfig `json:"repo"`
	// Repos are the additional repositories of a multi-repo project.
	Repos       []confstore.ExtraRepo     `json:"repos,omitempty"`
	Commands    map[string]string         `json:"commands"`
	Workspace   confstore.WorkspaceConfig `json:"workspace"`
	DefaultFlow string                    `json:"default_flow"`
}

func (b projectBody) toProject() confstore.Project {
	return confstore.Project{
		Name: b.Name, RepoAccountID: b.RepoAccountID, ProvisionerID: b.ProvisionerID,
		HarnessIDs: b.HarnessIDs, Repo: b.Repo, Repos: b.Repos,
		Commands: b.Commands, Workspace: b.Workspace, DefaultFlow: b.DefaultFlow,
	}
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Conf.ListProjects(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []confstore.Project{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var body projectBody
	if !readJSON(w, r, &body) {
		return
	}
	p := body.toProject()
	p.UserID = auth.UserID(r.Context())
	p, err := s.Conf.CreateProject(r.Context(), p)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.Conf.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	var body projectBody
	if !readJSON(w, r, &body) {
		return
	}
	p, err := s.Conf.UpdateProject(r.Context(), r.PathValue("id"), body.toProject())
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	if err := s.Conf.DeleteProject(r.Context(), r.PathValue("id")); err != nil {
		confErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) preflightProject(w http.ResponseWriter, r *http.Request) {
	steps := s.Engine.PreflightProject(r.Context(), r.PathValue("id"))
	ok := true
	for _, st := range steps {
		ok = ok && st.OK
	}
	writeJSON(w, http.StatusOK, PreflightResult{OK: ok, Steps: steps})
}

// startEnvCheck launches the §9 compliance gate for a project: provisions a
// throwaway workspace and verifies the derived contract. 202 + check id;
// the UI polls GET /api/envchecks/{id}.
func (s *Server) startEnvCheck(w http.ResponseWriter, r *http.Request) {
	var body EnvCheckRequest
	if r.ContentLength > 0 && !readJSON(w, r, &body) {
		return
	}
	id, err := s.EnvCheck.Start(r.Context(), r.PathValue("id"), body.Keep)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, EnvCheckRef{CheckID: id})
}

func (s *Server) getEnvCheck(w http.ResponseWriter, r *http.Request) {
	snap, ok := s.EnvCheck.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "envcheck not found (only the last 20 are kept)")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) getLatestEnvCheck(w http.ResponseWriter, r *http.Request) {
	snap, ok := s.EnvCheck.Latest(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "no envcheck has run for this project since the engine started")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) cancelEnvCheck(w http.ResponseWriter, r *http.Request) {
	if !s.EnvCheck.Cancel(r.PathValue("id")) {
		writeErr(w, http.StatusNotFound, "envcheck not found (only the last 20 are kept)")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- agents (DESIGN3 §1) ----------------------------------------------------

type agentBody struct {
	Description string                 `json:"description"`
	ReadOnly    bool                   `json:"read_only"`
	AllowBash   bool                   `json:"allow_bash"`
	Prompt      string                 `json:"prompt"`
	Inputs      []agentspec.InputDecl  `json:"inputs"`
	Outputs     []agentspec.OutputDecl `json:"outputs"`
}

// listActions serves the compiled-in action registry: the slugs an `action`
// stage may name, with the signature the editor renders binding rows from.
func (s *Server) listActions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, actionspec.Registry())
}

// listSignals serves the compiled-in signal registry: what a wait stage may
// block on and what a gate may be auto-answered by.
func (s *Server) listSignals(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, signalspec.Registry())
}

// --- flows ------------------------------------------------------------------

// getFlowSchema serves the flow document's JSON Schema (flow.Schema).
func (s *Server) getFlowSchema(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/schema+json")
	_, _ = w.Write(flow.Schema())
}

// listFlows serves one project's flows, or every one of the caller's
// projects' when no project is addressed (the overview). Built-in templates
// ride along on a project listing as the names that project could adopt but
// has not.
func (s *Server) listFlows(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	rows, err := s.Conf.ListFlows(r.Context(), projectID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if projectID == "" {
		// The overview: every flow of the caller's projects, no one else's.
		mine, err := s.Conf.ListProjects(r.Context(), auth.UserID(r.Context()))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		owned := map[string]bool{}
		for _, p := range mine {
			owned[p.ID] = true
		}
		kept := rows[:0]
		for _, row := range rows {
			if owned[row.ProjectID] {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	out := make([]flowResp, 0, len(rows)+1)
	have := map[string]bool{}
	for _, row := range rows {
		have[row.Name] = true
		out = append(out, describeFlow(row, false))
	}
	if projectID != "" {
		// A built-in is a template until this project saves it, at which
		// point it becomes the project's own row above.
		for _, name := range builtin.FlowNames() {
			if have[name] {
				continue
			}
			row, _ := builtinFlowRow(projectID, name)
			out = append(out, describeFlow(row, true))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectID != out[j].ProjectID {
			return out[i].ProjectID < out[j].ProjectID
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, http.StatusOK, out)
}

// listBuiltinFlows serves the compiled-in templates on their own: the library
// a project copies from, with no project of their own.
func (s *Server) listBuiltinFlows(w http.ResponseWriter, r *http.Request) {
	out := make([]flowResp, 0, len(builtin.FlowNames()))
	for _, name := range builtin.FlowNames() {
		row, _ := builtinFlowRow("", name)
		out = append(out, describeFlow(row, true))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createFlow(w http.ResponseWriter, r *http.Request) {
	var body CreateFlowRequest
	if !readJSON(w, r, &body) {
		return
	}
	projectID := r.PathValue("id")
	if _, err := s.Conf.GetFlow(r.Context(), projectID, body.Name); err == nil {
		writeErr(w, http.StatusConflict, "flow "+body.Name+" already exists in this project (PUT to update)")
		return
	} else if !errors.Is(err, confstore.ErrNotFound) {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	row, err := s.Conf.SaveFlow(r.Context(), projectID, body.Name, body.YAML, body.Layout)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, describeFlow(row, false))
}

// copyFlow clones a flow into another project — how a good flow spreads now
// that flows are owned rather than shared. The source may be one of this
// project's flows or a built-in template it has not adopted yet.
func (s *Server) copyFlow(w http.ResponseWriter, r *http.Request) {
	var body CopyFlowRequest
	if !readJSON(w, r, &body) {
		return
	}
	// The target is named in the body: the guard covered the source only.
	if !s.ownsProject(w, r, body.ToProjectID) {
		return
	}
	row, err := s.Conf.CopyFlow(r.Context(), r.PathValue("id"), r.PathValue("name"),
		body.ToProjectID, body.Name)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, describeFlow(row, false))
}

// copyFlowTemplate adopts a compiled-in template into a project. Templates
// belong to no project, so they get their own route: addressing one through
// /api/projects/{id}/flows/{name}/copy would need an empty id, and a path
// cannot carry one (the mux collapses the empty segment and the request lands
// on the UI's catch-all as a 405).
func (s *Server) copyFlowTemplate(w http.ResponseWriter, r *http.Request) {
	var body CopyFlowRequest
	if !readJSON(w, r, &body) {
		return
	}
	if !s.ownsProject(w, r, body.ToProjectID) {
		return
	}
	row, err := s.Conf.CopyTemplate(r.Context(), r.PathValue("name"), body.ToProjectID, body.Name)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, describeFlow(row, false))
}

func (s *Server) getFlow(w http.ResponseWriter, r *http.Request) {
	projectID, name := r.PathValue("id"), r.PathValue("name")
	row, err := s.Conf.GetFlow(r.Context(), projectID, name)
	if errors.Is(err, confstore.ErrNotFound) {
		// Not saved here yet: serve the built-in template this project would
		// run, marked as such so the editor offers "save to this project".
		if brow, ok := builtinFlowRow(projectID, name); ok {
			writeJSON(w, http.StatusOK, describeFlow(brow, true))
			return
		}
	}
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, describeFlow(row, false))
}

func (s *Server) putFlow(w http.ResponseWriter, r *http.Request) {
	var body UpdateFlowRequest
	if !readJSON(w, r, &body) {
		return
	}
	row, err := s.Conf.SaveFlow(r.Context(), r.PathValue("id"), r.PathValue("name"), body.YAML, body.Layout)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, describeFlow(row, false))
}

func (s *Server) deleteFlow(w http.ResponseWriter, r *http.Request) {
	projectID, name := r.PathValue("id"), r.PathValue("name")
	if err := s.Conf.DeleteFlow(r.Context(), projectID, name); err != nil {
		if errors.Is(err, confstore.ErrNotFound) && builtin.Flow(name) != nil {
			// Deleting this project's copy IS "reset to the template"; with no
			// copy there is nothing to delete — built-ins live in the binary.
			writeErr(w, http.StatusConflict, "flow "+name+" is the built-in template; this project has no copy to delete")
			return
		}
		confErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- flow bindings ----------------------------------------------------------
//
// A project configures a flow without touching it: its values for the
// document's parameters live beside the flow (confstore.FlowBinding), laid
// over the defaults at run creation. A template is configurable without
// being copied.

func (s *Server) listFlowBindings(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Conf.ListFlowBindings(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []confstore.FlowBinding{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) getFlowBinding(w http.ResponseWriter, r *http.Request) {
	b, err := s.Conf.GetFlowBinding(r.Context(), r.PathValue("id"), r.PathValue("name"))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) putFlowBinding(w http.ResponseWriter, r *http.Request) {
	var body FlowBindingRequest
	if !readJSON(w, r, &body) {
		return
	}
	b, err := s.Conf.PutFlowBinding(r.Context(), r.PathValue("id"), r.PathValue("name"), body.Params)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) deleteFlowBinding(w http.ResponseWriter, r *http.Request) {
	if err := s.Conf.DeleteFlowBinding(r.Context(), r.PathValue("id"), r.PathValue("name")); err != nil {
		confErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateFlow answers the editor with the document's real verdict. Since v3
// a flow carries its agents, so every contract violation is a parse error and
// there is no second, project-aware pass to fold in: `errors` refuse the
// document, `warnings` are advisory only (PLAN5 M24 — this endpoint used to
// report binding errors as warnings with ok:true, which read as advice).
func (s *Server) validateFlow(w http.ResponseWriter, r *http.Request) {
	var body ValidateFlowRequest
	if !readJSON(w, r, &body) {
		return
	}
	f, err := flow.Parse("flow", []byte(body.YAML))
	if err != nil {
		writeJSON(w, http.StatusOK, FlowValidation{Errors: []string{err.Error()}})
		return
	}
	// doc is the raw schema shape the graph editor edits (DESIGN3 §2.3).
	doc, err := flow.ParseDoc("flow", []byte(body.YAML))
	if err != nil {
		writeJSON(w, http.StatusOK, FlowValidation{Errors: []string{err.Error()}})
		return
	}
	out := FlowValidation{
		OK: true, Name: f.Name, Hash: f.Hash, OperatesOn: f.OperatesOn,
		Stages: len(f.Stages), Agents: len(f.Agents), Doc: doc,
		Warnings: f.Warnings, Parameters: f.Params, Refs: f.Refs, Phases: phaseSummaries(f),
	}
	// Bound values (a workshop's) are validated by the same rules as the
	// author's defaults; a value that breaks one is an error of the document
	// as that workshop would run it.
	if len(body.Params) > 0 {
		if bound, err := f.Resolve(body.Params); err != nil {
			out.OK, out.Errors = false, []string{err.Error()}
		} else {
			out.Warnings, out.Phases = bound.Warnings, phaseSummaries(bound)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listFlowVersions(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Conf.FlowVersions(r.Context(), r.PathValue("id"), r.PathValue("name"))
	if err != nil {
		confErr(w, err)
		return
	}
	if rows == nil {
		rows = []confstore.FlowVersion{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) getFlowVersion(w http.ResponseWriter, r *http.Request) {
	v, err := s.Conf.FlowVersion(r.Context(), r.PathValue("id"), r.PathValue("hash"))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// --- settings ---------------------------------------------------------------

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	set, err := s.Conf.Settings(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	// Decode over the *stored* settings so absent fields keep their current
	// values instead of reverting to compile-time defaults.
	me := auth.UserID(r.Context())
	set, err := s.Conf.Settings(r.Context(), me)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !readJSON(w, r, &set) {
		return
	}
	if err := s.Conf.PutSettings(r.Context(), me, set); err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

// --- build cache -------------------------------------------------------------

// getBuildCache reports the project's devcontainer build cache. A provisioner
// without one answers 503: the UI then says so rather than showing an empty
// cache that will never fill.
func (s *Server) getBuildCache(w http.ResponseWriter, r *http.Request) {
	info, err := s.Workspaces.BuildCache(r.Context(), r.PathValue("id"))
	if errors.Is(err, wsinfo.ErrBuildCacheOff) {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, BuildCacheInfo{Available: info.Available, Bytes: info.Bytes})
}

// deleteBuildCache empties it. The next build repopulates it, so this is a
// safe button: the worst case is one slow build.
func (s *Server) deleteBuildCache(w http.ResponseWriter, r *http.Request) {
	err := s.Workspaces.ClearBuildCache(r.Context(), r.PathValue("id"))
	if errors.Is(err, wsinfo.ErrBuildCacheOff) {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err != nil {
		confErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
