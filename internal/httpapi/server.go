// Package httpapi is the engine's HTTP surface (DESIGN.md §8): stdlib
// net/http, hand-rolled SSE. No auth except the ping endpoints, which
// require the per-run bearer token (localhost mode, §8/§15).
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/channels"
	"apothecary/internal/confstore"
	"apothecary/internal/engine"
	"apothecary/internal/event"
	"apothecary/internal/kinds"
	"apothecary/internal/run"
	"apothecary/internal/secrets"
)

type Server struct {
	Engine   *engine.Engine
	Store    event.Store
	Registry *channels.Registry
	RunsDir  string
	// Conf is the config-plane store (DESIGN2 §10); nil disables those
	// routes (bare test servers).
	Conf *confstore.Service
	// Kinds serves GET /api/kinds descriptors.
	Kinds *kinds.Registry
	// Ops runs integration tests / repo listing (the resolver); nil = 503.
	Ops IntegrationOps
	// Pings serves the durable request/response projection (PLAN4 M15);
	// nil falls back to the engine's in-memory pending registry.
	Pings PingStore
	// CPUIDir, when set, is served at / with an SPA fallback to index.html
	// (the built SvelteKit bundle; the CPUI talks only to this API).
	CPUIDir string
	// HoldTimeout is the ping long-poll hold (§8: 55 s).
	HoldTimeout time.Duration
	Log         *slog.Logger
}

func New(e *engine.Engine, store event.Store, reg *channels.Registry, runsDir string) *Server {
	return &Server{
		Engine: e, Store: store, Registry: reg, RunsDir: runsDir,
		HoldTimeout: 55 * time.Second,
		Log:         slog.Default(),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/tickets", s.postTicket)
	mux.HandleFunc("GET /api/runs", s.listRuns)
	mux.HandleFunc("GET /api/runs/{id}", s.getRun)
	mux.HandleFunc("GET /api/runs/{id}/events", s.streamEvents)
	mux.HandleFunc("GET /api/runs/{id}/logs/{stage}", s.getLog)
	mux.HandleFunc("POST /api/runs/{id}/abort", s.postAbort)
	mux.HandleFunc("POST /api/runs/{id}/archive", s.postArchive)
	mux.HandleFunc("POST /api/runs/{id}/restart", s.postRestart)
	mux.HandleFunc("POST /api/runs/{id}/pings", s.postPing)
	mux.HandleFunc("GET /api/runs/{id}/pings/{pid}", s.repollPing)
	mux.HandleFunc("POST /api/pings/{pid}/answer", s.answerPing)
	mux.HandleFunc("GET /api/pings", s.listPings)
	mux.HandleFunc("GET /api/pings/{pid}", s.getStoredPing)
	mux.HandleFunc("GET /api/pings/{pid}/artifact", s.pingArtifact)
	mux.HandleFunc("GET /api/channels", s.listChannels)
	mux.HandleFunc("POST /api/channels", s.addChannel)
	mux.HandleFunc("DELETE /api/channels/{id}", s.deleteChannel)
	if s.Kinds != nil {
		mux.HandleFunc("GET /api/kinds", s.listKinds)
	}
	if s.Conf != nil { // config plane (DESIGN2 §10)
		mux.HandleFunc("GET /api/integrations", s.listIntegrations)
		mux.HandleFunc("POST /api/integrations", s.createIntegration)
		mux.HandleFunc("GET /api/integrations/{id}", s.getIntegration)
		mux.HandleFunc("PUT /api/integrations/{id}", s.updateIntegration)
		mux.HandleFunc("DELETE /api/integrations/{id}", s.deleteIntegration)
		mux.HandleFunc("POST /api/integrations/{id}/test", s.testIntegration)
		mux.HandleFunc("GET /api/integrations/{id}/repos", s.listIntegrationRepos)
		mux.HandleFunc("GET /api/integrations/{id}/models", s.listIntegrationModels)
		mux.HandleFunc("GET /api/projects", s.listProjects)
		mux.HandleFunc("POST /api/projects", s.createProject)
		mux.HandleFunc("GET /api/projects/{id}", s.getProject)
		mux.HandleFunc("PUT /api/projects/{id}", s.updateProject)
		mux.HandleFunc("DELETE /api/projects/{id}", s.deleteProject)
		// Agent library (DESIGN3 §1): per-project, copiable, versioned.
		mux.HandleFunc("GET /api/projects/{id}/agents", s.listAgents)
		mux.HandleFunc("PUT /api/projects/{id}/agents/{name}", s.putAgent)
		mux.HandleFunc("GET /api/projects/{id}/agents/{name}", s.getAgent)
		mux.HandleFunc("DELETE /api/projects/{id}/agents/{name}", s.deleteAgent)
		mux.HandleFunc("GET /api/projects/{id}/agents/{name}/versions", s.listAgentVersions)
		mux.HandleFunc("GET /api/agent-versions/{hash}", s.getAgentVersion)
		mux.HandleFunc("POST /api/projects/{id}/agents/copy", s.copyAgents)
		mux.HandleFunc("POST /api/projects/{id}/preflight", s.preflightProject)
		mux.HandleFunc("POST /api/projects/{id}/envcheck", s.startEnvCheck)
		mux.HandleFunc("GET /api/envchecks/{id}", s.getEnvCheck)
		mux.HandleFunc("GET /api/presets", s.listPresets)
		mux.HandleFunc("GET /api/flows", s.listFlows)
		mux.HandleFunc("POST /api/flows", s.createFlow)
		mux.HandleFunc("POST /api/flows/validate", s.validateFlow)
		mux.HandleFunc("GET /api/flows/{name}", s.getFlow)
		mux.HandleFunc("PUT /api/flows/{name}", s.putFlow)
		mux.HandleFunc("DELETE /api/flows/{name}", s.deleteFlow)
		mux.HandleFunc("GET /api/flows/{name}/versions", s.listFlowVersions)
		mux.HandleFunc("GET /api/flow-versions/{hash}", s.getFlowVersion)
		mux.HandleFunc("GET /api/settings", s.getSettings)
		mux.HandleFunc("PUT /api/settings", s.putSettings)
	}
	if s.CPUIDir != "" {
		mux.HandleFunc("GET /", s.serveCPUI)
	}
	return mux
}

// serveCPUI serves the built CPUI bundle with an SPA fallback: files that
// exist are served as-is, everything else gets index.html so client-side
// routes deep-link correctly.
func (s *Server) serveCPUI(w http.ResponseWriter, r *http.Request) {
	p := filepath.Join(s.CPUIDir, filepath.Clean("/"+r.URL.Path))
	if info, err := os.Stat(p); err == nil && !info.IsDir() {
		http.ServeFile(w, r, p)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.CPUIDir, "index.html"))
}

// --- helpers ----------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// runEvents loads a run's full event history, or 404s.
func (s *Server) runEvents(w http.ResponseWriter, r *http.Request, id string) ([]event.Event, bool) {
	evs, err := s.Store.Events(r.Context(), id, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if len(evs) == 0 {
		writeErr(w, http.StatusNotFound, "run "+id+" not found")
		return nil, false
	}
	return evs, true
}

// authPing enforces the per-run bearer token on the ping endpoints (§8).
func (s *Server) authPing(w http.ResponseWriter, r *http.Request, runID string) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	want := s.Engine.RunToken(runID)
	if !ok || want == "" || subtle.ConstantTimeCompare([]byte(token), []byte(want)) != 1 {
		writeErr(w, http.StatusUnauthorized, "missing or invalid run token")
		return false
	}
	return true
}

// PingStore is the durable request/response projection surface (PLAN4
// M15): pgstore in production, EventPingStore over any event.Store in
// tests/tools.
type PingStore interface {
	ListPings(ctx context.Context, status string, limit int) ([]run.PingView, error)
	GetPing(ctx context.Context, id string) (run.PingView, error)
}

// EventPingStore derives the projection by folding events — parity with
// pgstore's SQL view via the shared run.ReducePings reducer.
type EventPingStore struct{ Store event.Store }

func (e EventPingStore) ListPings(ctx context.Context, status string, limit int) ([]run.PingView, error) {
	ids, err := e.Store.RunIDs(ctx)
	if err != nil {
		return nil, err
	}
	var out []run.PingView
	for _, id := range ids {
		evs, err := e.Store.Events(ctx, id, 0)
		if err != nil {
			return nil, err
		}
		for _, v := range run.ReducePings(evs) {
			if status == "" || status == "all" || v.Status == status {
				out = append(out, v)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RaisedAt.After(out[j].RaisedAt) })
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (e EventPingStore) GetPing(ctx context.Context, id string) (run.PingView, error) {
	all, err := e.ListPings(ctx, "all", 500)
	if err != nil {
		return run.PingView{}, err
	}
	for _, v := range all {
		if v.PingID == id {
			return v, nil
		}
	}
	return run.PingView{}, fmt.Errorf("ping %s: %w", id, engine.ErrPingNotFound)
}

// pingJSON is the registry-fallback shape (Pings == nil); the projection
// path serves run.PingView directly.
type pingJSON struct {
	PingID     string    `json:"ping_id"`
	RunID      string    `json:"run_id"`
	Type       string    `json:"type"`
	StageID    string    `json:"stage_id,omitempty"`
	Question   string    `json:"question"`
	Context    string    `json:"context,omitempty"`
	PayloadRef string    `json:"payload_ref,omitempty"`
	Options    []string  `json:"options,omitempty"`
	Status     string    `json:"status"`
	RaisedAt   time.Time `json:"raised_at"`
}

func toPingJSON(p *engine.Ping) pingJSON {
	return pingJSON{
		PingID: p.ID, RunID: p.RunID, Type: p.Type, StageID: p.StageID, Question: p.Question,
		Context: p.Context, PayloadRef: p.PayloadRef, Options: p.Options, Status: "pending", RaisedAt: p.RaisedAt,
	}
}

// --- tickets & runs ---------------------------------------------------------

func (s *Server) postTicket(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title     string `json:"title"`
		Goal      string `json:"goal"`
		Flow      string `json:"flow"`       // "" = the project's default flow
		ProjectID string `json:"project_id"` // required (DESIGN2 §10)
	}
	if !readJSON(w, r, &body) {
		return
	}
	runID, err := s.Engine.CreateRun(r.Context(), engine.Ticket{Title: body.Title, Goal: body.Goal}, body.Flow, body.ProjectID)
	switch {
	case errors.Is(err, engine.ErrEnvCapacity):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusCreated, map[string]string{"run_id": runID})
	}
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	ids, err := s.Store.RunIDs(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	project := r.URL.Query().Get("project")
	views := make([]run.View, 0, len(ids))
	for _, id := range ids {
		evs, err := s.Store.Events(r.Context(), id, 0)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if len(evs) == 0 {
			continue
		}
		v := run.Reduce(evs)
		if project != "" && v.ProjectID != project {
			continue
		}
		views = append(views, v)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].CreatedAt.After(views[j].CreatedAt) })
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	evs, ok := s.runEvents(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	last := evs
	if len(last) > 100 {
		last = last[len(last)-100:]
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run.Reduce(evs), "events": last})
}

func (s *Server) postAbort(w http.ResponseWriter, r *http.Request) {
	err := s.Engine.Abort(r.PathValue("id"))
	switch {
	case errors.Is(err, engine.ErrRunNotActive):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

func (s *Server) postArchive(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.Archive(r.Context(), r.PathValue("id")); err != nil {
		status := http.StatusConflict
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeErr(w, status, err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// postRestart starts a terminal run over as a fresh run; 201 carries the
// new run id. 409 when the run is active/non-terminal, 404 when unknown,
// 400 when the re-preflight refuses (config drifted since the original).
func (s *Server) postRestart(w http.ResponseWriter, r *http.Request) {
	newID, err := s.Engine.RestartRun(r.Context(), r.PathValue("id"))
	if err != nil {
		msg := err.Error()
		status := http.StatusBadRequest
		switch {
		case strings.Contains(msg, "not found"):
			status = http.StatusNotFound
		case strings.Contains(msg, "still active"), strings.Contains(msg, "only terminal"):
			status = http.StatusConflict
		}
		writeErr(w, status, msg)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"run_id": newID})
}

// --- SSE --------------------------------------------------------------------

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.runEvents(w, r, id); !ok {
		return
	}
	after := int64(0)
	if q := r.URL.Query().Get("after_seq"); q != "" {
		v, err := strconv.ParseInt(q, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid after_seq")
			return
		}
		after = v
	}
	// Browser EventSource reconnects carry the last received id here.
	if h := r.Header.Get("Last-Event-ID"); h != "" {
		if v, err := strconv.ParseInt(h, 10, 64); err == nil && v > after {
			after = v
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	ch, err := s.Store.Watch(r.Context(), id, after)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case ev, open := <-ch:
			if !open {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				s.Log.Error("sse marshal", "err", err)
				continue
			}
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// --- logs -------------------------------------------------------------------

func (s *Server) getLog(w http.ResponseWriter, r *http.Request) {
	id, stage := r.PathValue("id"), r.PathValue("stage")
	if s.RunsDir == "" {
		writeErr(w, http.StatusNotFound, "log storage is not configured")
		return
	}
	dir := filepath.Join(s.RunsDir, id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no logs for run "+id)
		return
	}
	// Serve the highest iteration for the stage.
	best, bestIter := "", -1
	for _, e := range entries {
		name := e.Name()
		rest, ok := strings.CutPrefix(name, stage+"-")
		if !ok {
			continue
		}
		numStr, ok := strings.CutSuffix(rest, ".log")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(numStr); err == nil && n > bestIter {
			bestIter, best = n, name
		}
	}
	if best == "" {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("no log for stage %s", stage))
		return
	}
	data, err := os.ReadFile(filepath.Join(dir, best))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	n := 64 << 10
	if q := r.URL.Query().Get("n"); q != "" {
		if v, err := strconv.Atoi(q); err == nil && v > 0 {
			n = v
		}
	}
	if len(data) > n {
		data = data[len(data)-n:]
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}

// --- pings ------------------------------------------------------------------

// postPing is the ask_human shim entry (§10): raise, then hold up to
// HoldTimeout for the answer; 200 with the answer, else 204 (the shim
// re-polls with the ping id from the X-Ping-ID header — the 204 has no body,
// so the id travels in the header on both responses).
func (s *Server) postPing(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if !s.authPing(w, r, runID) {
		return
	}
	var body struct {
		Type     string   `json:"type"`
		Question string   `json:"question"`
		Context  string   `json:"context"`
		Options  []string `json:"options"`  // PLAN4 M16: closed answer set
		Artifact string   `json:"artifact"` // PLAN4 M16: workspace file to review
	}
	if !readJSON(w, r, &body) {
		return
	}
	p, err := s.Engine.RaisePing(runID, body.Type, body.Question, body.Context, body.Options, body.Artifact)
	switch {
	case errors.Is(err, engine.ErrRunNotActive):
		writeErr(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.holdForAnswer(w, r, p)
}

func (s *Server) repollPing(w http.ResponseWriter, r *http.Request) {
	runID, pid := r.PathValue("id"), r.PathValue("pid")
	if !s.authPing(w, r, runID) {
		return
	}
	p := s.Engine.GetPing(pid)
	if p == nil || p.RunID != runID {
		writeErr(w, http.StatusNotFound, "ping not found")
		return
	}
	s.holdForAnswer(w, r, p)
}

func (s *Server) holdForAnswer(w http.ResponseWriter, r *http.Request, p *engine.Ping) {
	w.Header().Set("X-Ping-ID", p.ID)
	ctx, cancel := context.WithTimeout(r.Context(), s.HoldTimeout)
	defer cancel()
	if ans, ok := p.Await(ctx); ok {
		writeJSON(w, http.StatusOK, map[string]string{
			"ping_id": p.ID, "answer": ans.Answer, "feedback": ans.Feedback,
		})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) answerPing(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Answer   string `json:"answer"`
		Feedback string `json:"feedback"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	err := s.Engine.AnswerPing(r.PathValue("pid"), engine.PingAnswer{Answer: body.Answer, Feedback: body.Feedback})
	switch {
	case errors.Is(err, engine.ErrPingNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, engine.ErrPingAnswered):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, engine.ErrRunNotActive):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "answered"})
	}
}

func (s *Server) listPings(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	switch status {
	case "", "all", "pending", "answered":
	default:
		writeErr(w, http.StatusBadRequest, "status must be pending|answered|all")
		return
	}
	if s.Pings == nil {
		// Bare test servers: pending only, from the live registry.
		pings := s.Engine.PendingPings()
		out := make([]pingJSON, 0, len(pings))
		for _, p := range pings {
			out = append(out, toPingJSON(p))
		}
		sort.Slice(out, func(i, j int) bool { return out[i].RaisedAt.Before(out[j].RaisedAt) })
		writeJSON(w, http.StatusOK, out)
		return
	}
	if status == "" {
		status = "pending" // inbox default; history is opt-in
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.Pings.ListPings(r.Context(), status, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if status == "" || status == "pending" {
		// Pending lists read oldest-first (inbox order).
		sort.Slice(rows, func(i, j int) bool { return rows[i].RaisedAt.Before(rows[j].RaisedAt) })
	}
	if rows == nil {
		rows = []run.PingView{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// getStoredPing serves one durable request/response pair (PLAN4 M15).
func (s *Server) getStoredPing(w http.ResponseWriter, r *http.Request) {
	if s.Pings == nil {
		writeErr(w, http.StatusServiceUnavailable, "ping projection is not wired")
		return
	}
	v, err := s.Pings.GetPing(r.Context(), r.PathValue("pid"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, engine.ErrPingNotFound) || strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// pingArtifact serves a gate's payload artifact from the live workspace
// (DESIGN3 §3.1) so the plan review workbench renders it in place. 409 for
// URL payloads (the UI keeps the link-out).
func (s *Server) pingArtifact(w http.ResponseWriter, r *http.Request) {
	path, content, err := s.Engine.PingArtifact(r.Context(), r.PathValue("pid"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]string{"path": path, "content": string(content)})
	case errors.Is(err, engine.ErrPingNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, engine.ErrArtifactIsURL):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, engine.ErrNoArtifact):
		writeErr(w, http.StatusGone, err.Error())
	default:
		writeErr(w, http.StatusBadGateway, "artifact read failed: "+err.Error())
	}
}

// --- channels ---------------------------------------------------------------

// maskChannel redacts secret-shaped config values (tokens etc.) before an
// Entry leaves the server: channel storage is a documented plaintext
// residual, but responses carry only masks, like integrations (§3).
func maskChannel(e *channels.Entry) map[string]any {
	cfg := make(map[string]any, len(e.Config))
	for k, v := range e.Config {
		lk := strings.ToLower(k)
		secret := strings.Contains(lk, "token") || strings.Contains(lk, "secret") ||
			strings.Contains(lk, "password") || strings.Contains(lk, "key")
		if str, ok := v.(string); ok && secret {
			cfg[k] = secrets.Mask(str)
		} else {
			cfg[k] = v
		}
	}
	return map[string]any{"id": e.ID, "kind": e.Kind, "config": cfg}
}

func (s *Server) listChannels(w http.ResponseWriter, _ *http.Request) {
	entries := s.Registry.List()
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, maskChannel(e))
	}
	writeJSON(w, http.StatusOK, out)
}

// addChannel accepts the §8 flat shape: {"kind":"matrix", "homeserver":...,
// "access_token":..., "room_id":...} — kind plus config keys.
func (s *Server) addChannel(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !readJSON(w, r, &body) {
		return
	}
	kind, _ := body["kind"].(string)
	if kind == "" {
		writeErr(w, http.StatusBadRequest, "kind is required")
		return
	}
	delete(body, "kind")
	e, err := s.Registry.Add(kind, body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, maskChannel(e))
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	if err := s.Registry.Delete(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
