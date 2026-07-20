package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"apothecary/internal/confstore"
	"apothecary/internal/engine"
	"apothecary/internal/flow"
	"apothecary/internal/kinds"
	"apothecary/internal/secrets"
	"apothecary/internal/wsimage"
)

// Config-plane routes (DESIGN2 §10). Secrets are write-only: requests carry
// plaintext values, responses only masks (§3).

// IntegrationOps is the slice of the resolver these routes need — an
// interface so tests can stub it without a kinds registry.
type IntegrationOps interface {
	TestIntegration(ctx context.Context, id string) error
	ListRepos(ctx context.Context, id string) ([]kinds.RepoInfo, error)
	ListModels(ctx context.Context, id string) ([]kinds.ModelInfo, error)
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
	case errors.Is(err, confstore.ErrInvalid), errors.Is(err, engine.ErrUnresolvable):
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
	rows, err := s.Conf.ListIntegrations(r.Context())
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
	var body struct {
		Port    string            `json:"port"`
		Kind    string            `json:"kind"`
		Name    string            `json:"name"`
		Config  map[string]any    `json:"config"`
		Secrets map[string]string `json:"secrets"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	in, err := s.Conf.CreateIntegration(r.Context(), confstore.Integration{
		Port: body.Port, Kind: body.Kind, Name: body.Name,
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
	var body struct {
		Name    string            `json:"name"`
		Config  map[string]any    `json:"config"`
		Secrets map[string]string `json:"secrets"` // §3 merge: present replaces, "" deletes, absent keeps
	}
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
	if err := s.Ops.TestIntegration(r.Context(), r.PathValue("id")); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
	Name          string                    `json:"name"`
	ForgeID       string                    `json:"forge_id"`
	ProvisionerID string                    `json:"provisioner_id"`
	HarnessID     string                    `json:"harness_id"`
	Repo          confstore.RepoConfig      `json:"repo"`
	Commands      map[string]string         `json:"commands"`
	Workspace     confstore.WorkspaceConfig `json:"workspace"`
	DefaultFlow   string                    `json:"default_flow"`
}

func (b projectBody) toProject() confstore.Project {
	return confstore.Project{
		Name: b.Name, ForgeID: b.ForgeID, ProvisionerID: b.ProvisionerID,
		HarnessID: b.HarnessID, Repo: b.Repo,
		Commands: b.Commands, Workspace: b.Workspace, DefaultFlow: b.DefaultFlow,
	}
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Conf.ListProjects(r.Context())
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
	p, err := s.Conf.CreateProject(r.Context(), body.toProject())
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
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "steps": steps})
}

// startEnvCheck launches the §9 compliance gate for a project: provisions a
// throwaway workspace and verifies the derived contract. 202 + check id;
// the UI polls GET /api/envchecks/{id}.
func (s *Server) startEnvCheck(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Keep bool `json:"keep"`
	}
	if r.ContentLength > 0 && !readJSON(w, r, &body) {
		return
	}
	id, err := s.Engine.StartEnvCheck(r.Context(), r.PathValue("id"), body.Keep)
	if err != nil {
		if strings.Contains(err.Error(), "already running") {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"check_id": id})
}

func (s *Server) getEnvCheck(w http.ResponseWriter, r *http.Request) {
	snap, ok := s.Engine.EnvCheck(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "envcheck not found (only the last 20 are kept)")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) listPresets(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, wsimage.Presets())
}

// --- agents (DESIGN3 §1) ----------------------------------------------------

type agentBody struct {
	Description string `json:"description"`
	ReadOnly    bool   `json:"read_only"`
	AllowBash   bool   `json:"allow_bash"`
	Prompt      string `json:"prompt"`
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	// Project existence gives an honest 404 (an unknown project would
	// otherwise read as an empty library).
	if _, err := s.Conf.GetProject(r.Context(), r.PathValue("id")); err != nil {
		confErr(w, err)
		return
	}
	rows, err := s.Conf.ListAgents(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	if rows == nil {
		rows = []confstore.Agent{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) putAgent(w http.ResponseWriter, r *http.Request) {
	var body agentBody
	if !readJSON(w, r, &body) {
		return
	}
	a, err := s.Conf.SaveAgent(r.Context(), confstore.Agent{
		ProjectID: r.PathValue("id"), Name: r.PathValue("name"),
		Description: body.Description, ReadOnly: body.ReadOnly,
		AllowBash: body.AllowBash, Prompt: body.Prompt,
	})
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	a, err := s.Conf.GetAgent(r.Context(), r.PathValue("id"), r.PathValue("name"))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	if err := s.Conf.DeleteAgent(r.Context(), r.PathValue("id"), r.PathValue("name")); err != nil {
		confErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listAgentVersions(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Conf.AgentVersions(r.Context(), r.PathValue("id"), r.PathValue("name"))
	if err != nil {
		confErr(w, err)
		return
	}
	if rows == nil {
		rows = []confstore.AgentVersion{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) getAgentVersion(w http.ResponseWriter, r *http.Request) {
	v, err := s.Conf.AgentVersion(r.Context(), r.PathValue("hash"))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) copyAgents(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FromProjectID string   `json:"from_project_id"`
		Names         []string `json:"names"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	copied, err := s.Conf.CopyAgents(r.Context(), r.PathValue("id"), body.FromProjectID, body.Names)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"copied": copied})
}

// --- flows ------------------------------------------------------------------

func (s *Server) listFlows(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Conf.ListFlows(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []confstore.FlowRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) createFlow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string          `json:"name"`
		YAML   string          `json:"yaml"`
		Layout json.RawMessage `json:"layout"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if _, err := s.Conf.GetFlow(r.Context(), body.Name); err == nil {
		writeErr(w, http.StatusConflict, "flow "+body.Name+" already exists (PUT to update)")
		return
	} else if !errors.Is(err, confstore.ErrNotFound) {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	row, err := s.Conf.SaveFlow(r.Context(), body.Name, body.YAML, body.Layout)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (s *Server) getFlow(w http.ResponseWriter, r *http.Request) {
	row, err := s.Conf.GetFlow(r.Context(), r.PathValue("name"))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) putFlow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		YAML   string          `json:"yaml"`
		Layout json.RawMessage `json:"layout"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	row, err := s.Conf.SaveFlow(r.Context(), r.PathValue("name"), body.YAML, body.Layout)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) deleteFlow(w http.ResponseWriter, r *http.Request) {
	if err := s.Conf.DeleteFlow(r.Context(), r.PathValue("name")); err != nil {
		confErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateFlow runs the parser without saving — line-precise errors travel
// verbatim (§7).
func (s *Server) validateFlow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		YAML string `json:"yaml"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	f, err := flow.Parse("flow", []byte(body.YAML))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "errors": []string{err.Error()}})
		return
	}
	// doc is the raw schema shape the graph editor edits (DESIGN3 §2.3).
	doc, err := flow.ParseDoc("flow", []byte(body.YAML))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "errors": []string{err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "name": f.Name, "hash": f.Hash, "stages": len(f.Stages), "doc": doc,
	})
}

func (s *Server) listFlowVersions(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Conf.FlowVersions(r.Context(), r.PathValue("name"))
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
	v, err := s.Conf.FlowVersion(r.Context(), r.PathValue("hash"))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// --- settings ---------------------------------------------------------------

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	set, err := s.Conf.Settings(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	// Decode over the *stored* settings so absent fields keep their current
	// values instead of reverting to compile-time defaults.
	set, err := s.Conf.Settings(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !readJSON(w, r, &set) {
		return
	}
	if err := s.Conf.PutSettings(r.Context(), set); err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}
