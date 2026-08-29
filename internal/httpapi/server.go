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

	"apothecary/internal/builtin"
	"apothecary/internal/channels"
	"apothecary/internal/confstore"
	"apothecary/internal/engine"
	"apothecary/internal/event"
	"apothecary/internal/filestore"
	"apothecary/internal/kinds"
	"apothecary/internal/portrelay"
	"apothecary/internal/ports"
	"apothecary/internal/run"
	"apothecary/internal/secrets"
	"apothecary/internal/tickets"
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
	// Sources resolves ticketsource integrations for the ticket factory (the
	// resolver again); nil = importing is off and the routes say so.
	Sources tickets.Sources
	// Pings serves the durable request/response projection (PLAN4 M15);
	// nil falls back to the engine's in-memory pending registry.
	Pings PingStore
	// Attachments/Snapshots are the file stores (DESIGN-FILES): human uploads
	// and the engine-captured record. nil disables the respective routes
	// (uploads → 503, lists → empty).
	Attachments *filestore.Attachments
	Snapshots   *filestore.Snapshots
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
	Files      []string  `json:"files,omitempty"`
	Link       string    `json:"link,omitempty"`
	PayloadRef string    `json:"payload_ref,omitempty"`
	Options    []string  `json:"options,omitempty"`
	Status     string    `json:"status"`
	RaisedAt   time.Time `json:"raised_at"`
}

func toPingJSON(p *engine.Ping) pingJSON {
	return pingJSON{
		PingID: p.ID, RunID: p.RunID, Type: p.Type, StageID: p.StageID, Question: p.Question,
		Context: p.Context, Files: p.Files, Link: p.Link, PayloadRef: p.Primary(),
		Options: p.Options, Status: "pending", RaisedAt: p.RaisedAt,
	}
}

// --- tickets & runs ---------------------------------------------------------

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	ids, err := s.Store.RunIDs(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	project := r.URL.Query().Get("project")
	ticket := r.URL.Query().Get("ticket")
	// One snapshot of the reconciler's live-status map (overlaid with any
	// in-flight op) for the whole list — the workspace badge reads env_status
	// to tell a live env from a stopped one, and to show rebuilding/destroying/
	// error transitions (env_id alone can't, see run.View.EnvStatus).
	envStatus := s.Engine.EnvBadges()
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
		if ticket != "" && v.TicketID != ticket {
			continue
		}
		// Overlay may carry a provisioning/error op before env_id exists, so
		// don't gate on EnvID here.
		if st, ok := envStatus[v.RunID]; ok {
			v.EnvStatus = st
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
	v := run.Reduce(evs)
	if st, ok := s.Engine.EnvBadges()[v.RunID]; ok {
		v.EnvStatus = st
	}
	writeJSON(w, http.StatusOK, RunDetail{Run: v, Events: last})
}

func (s *Server) postAbort(w http.ResponseWriter, r *http.Request) {
	err := s.Engine.Abort(r.PathValue("id"))
	switch {
	case err != nil && strings.Contains(err.Error(), "not found"):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, engine.ErrRunNotActive):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// postPause parks an active run (202); 409 when it has no live goroutine.
func (s *Server) postPause(w http.ResponseWriter, r *http.Request) {
	err := s.Engine.Pause(r.PathValue("id"))
	switch {
	case errors.Is(err, engine.ErrRunNotActive):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// postPlay restarts a paused run (202, same id); 409 when active or not
// paused, 404 when unknown.
func (s *Server) postPlay(w http.ResponseWriter, r *http.Request) {
	err := s.Engine.Play(r.Context(), r.PathValue("id"))
	if err != nil {
		msg := err.Error()
		status := http.StatusBadRequest
		switch {
		case strings.Contains(msg, "not found"):
			status = http.StatusNotFound
		case strings.Contains(msg, "still active"), strings.Contains(msg, "can be played"):
			status = http.StatusConflict
		}
		writeErr(w, status, msg)
		return
	}
	w.WriteHeader(http.StatusAccepted)
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
// The optional body {flow, goal, attachment_ids} steers THIS attempt —
// restart-after-failure is when a human most wants to give direction.
func (s *Server) postRestart(w http.ResponseWriter, r *http.Request) {
	var body RestartRunRequest
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)

	newID, err := s.Engine.RestartRun(r.Context(), r.PathValue("id"), engine.RunInput{
		Flow: body.Flow, Goal: body.Goal, SourceBranch: body.SourceBranch, AttachmentIDs: body.AttachmentIDs,
	})
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
	writeJSON(w, http.StatusCreated, RunRef{RunID: newID})
}

// postResume re-enters the failed stage of an abandoned/failed run in its
// existing workspace ("try again") — 202, the run continues under the same
// id. 409 when active/not-resumable, 404 when unknown.
func (s *Server) postResume(w http.ResponseWriter, r *http.Request) {
	err := s.Engine.ResumeRun(r.Context(), r.PathValue("id"))
	if err != nil {
		msg := err.Error()
		status := http.StatusBadRequest
		switch {
		case strings.Contains(msg, "not found"):
			status = http.StatusNotFound
		case strings.Contains(msg, "still active"), strings.Contains(msg, "can be resumed"):
			status = http.StatusConflict
		}
		writeErr(w, status, msg)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// postRebuild recreates the run's workspace container in place (keeps the run
// + its commits, re-runs bootstrap). Blocks until done — the run page shows
// progress via the workspace-log tail and the rebuilding badge.
func (s *Server) postRebuild(w http.ResponseWriter, r *http.Request) {
	err := s.Engine.RebuildEnv(r.Context(), r.PathValue("id"))
	if err != nil {
		msg := err.Error()
		status := http.StatusBadRequest
		switch {
		case strings.Contains(msg, "not found"):
			status = http.StatusNotFound
		case errors.Is(err, engine.ErrEnvOpInFlight), errors.Is(err, engine.ErrRunActive),
			strings.Contains(msg, "does not support"), strings.Contains(msg, "no workspace"),
			strings.Contains(msg, "no project"):
			status = http.StatusConflict
		}
		writeErr(w, status, msg)
		return
	}
	w.WriteHeader(http.StatusAccepted)
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

// getWorkspaceLog serves the LIVE tail of the run's workspace exec output
// (optional provisioner capability) — feedback while a stage is working.
func (s *Server) getWorkspaceLog(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	data, err := s.Engine.WorkspaceLogTail(r.Context(), r.PathValue("id"), n)
	if err != nil {
		status := http.StatusBadGateway
		switch {
		case strings.Contains(err.Error(), "not found"):
			status = http.StatusNotFound
		case errors.Is(err, engine.ErrNoLiveLog), os.IsNotExist(err):
			status = http.StatusNotFound
		}
		writeErr(w, status, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}

// getEgressDefaults serves the compiled-in "sensible defaults" allowlist the
// project form offers via its "Add sensible defaults" button. Static, no auth —
// it's the same list baked into the binary. The form copies what the operator
// keeps into the project's extra_domains (they are no longer force-seeded).
func (s *Server) getEgressDefaults(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, EgressDefaults{Domains: builtin.EgressDefaults()})
}

// getEgressDenials lists hosts the per-project gateway blocked for this run:
// the visible diagnosis for "the firewall ate my model provider". Empty list
// when the run has no gateway or nothing was blocked.
func (s *Server) getEgressDenials(w http.ResponseWriter, r *http.Request) {
	dens := s.Engine.EgressDenials(r.Context(), r.PathValue("id"))
	if dens == nil {
		dens = []ports.EgressDenial{}
	}
	writeJSON(w, http.StatusOK, dens)
}

// getRunEgressPolicy serves the egress policy the gateway enforces for a run
// right now — the "current rule" side of the firewall UI. The audit rows only
// carry historical verdicts, so the rule toggles render from this instead.
func (s *Server) getRunEgressPolicy(w http.ResponseWriter, r *http.Request) {
	pol, err := s.Engine.RunEgressPolicy(r.Context(), r.PathValue("id"))
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, engine.ErrEgressOff) {
			status = http.StatusServiceUnavailable
		}
		writeErr(w, status, err.Error())
		return
	}
	if pol.Domains == nil {
		pol.Domains = []string{}
	}
	if pol.BlockedDomains == nil {
		pol.BlockedDomains = []string{}
	}
	writeJSON(w, http.StatusOK, EgressPolicy{
		Mode: pol.Mode, Domains: pol.Domains, BlockedDomains: pol.BlockedDomains,
	})
}

// getRunEgressAudit serves the durable egress audit trail for a run — every
// external host it reached or was blocked from, with counts, byte totals and
// timestamps. Filterable via ?verdict=allow|deny, ?host=<substring>, ?limit=N.
func (s *Server) getRunEgressAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.Engine.RunEgressAudit(r.Context(), r.PathValue("id"),
		r.URL.Query().Get("verdict"), r.URL.Query().Get("host"), limit)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if rows == nil {
		rows = []ports.EgressAuditRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// getProjectEgressAudit serves the durable egress audit trail across all of a
// project's runs. Same filters as the run-level endpoint.
func (s *Server) getProjectEgressAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.Engine.ProjectEgressAudit(r.Context(), r.PathValue("id"),
		r.URL.Query().Get("verdict"), r.URL.Query().Get("host"), limit)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if rows == nil {
		rows = []ports.EgressAuditRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// validEgressHost accepts a hostname or `*.suffix` wildcard — no scheme,
// port or path (the proxy matches host only).
func validEgressHost(h string) bool {
	if h == "" || len(h) > 253 || strings.ContainsAny(h, " /:@") {
		return false
	}
	body := strings.TrimPrefix(h, "*.")
	if body == "" || !strings.Contains(body, ".") {
		return false
	}
	for _, r := range body {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// postEgressAllow adds a host to a run's live proxy allowlist AND persists
// it to the project's extra_domains (M18 "add to egress" button): the hung
// agent proceeds instantly, and future runs never hit the wall.
func (s *Server) postEgressAllow(w http.ResponseWriter, r *http.Request) {
	var body EgressHostRequest
	if !readJSON(w, r, &body) {
		return
	}
	host := strings.ToLower(strings.TrimSpace(body.Host))
	if !validEgressHost(host) {
		writeErr(w, http.StatusBadRequest, "host must be a bare hostname or *.wildcard (no scheme/port/path)")
		return
	}
	runID := r.PathValue("id")

	// Instant: unblock the live run.
	if err := s.Engine.AllowEgress(r.Context(), runID, host); err != nil {
		status := http.StatusConflict
		if errors.Is(err, engine.ErrEgressOff) {
			status = http.StatusServiceUnavailable
		}
		writeErr(w, status, err.Error())
		return
	}

	// Persist to the project so future runs inherit it (best-effort — the
	// live unblock already succeeded).
	persisted := false
	if s.Conf != nil {
		if evs, err := s.Store.Events(r.Context(), runID, 0); err == nil && len(evs) > 0 {
			if pid := run.Reduce(evs).ProjectID; pid != "" {
				if p, err := s.Conf.GetProject(r.Context(), pid); err == nil {
					if !containsStr(p.Workspace.ExtraDomains, host) {
						p.Workspace.ExtraDomains = append(p.Workspace.ExtraDomains, host)
						if _, err := s.Conf.UpdateProject(r.Context(), pid, p); err == nil {
							persisted = true
						}
					} else {
						persisted = true
					}
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, EgressAllowResult{Allowed: host, Persisted: persisted})
}

// postEgressRule live-toggles a host's egress rule for a run (allow or force-
// block) via the gateway, and persists the change to the project (allow →
// extra_domains, block → blocked_domains) so future runs inherit it.
func (s *Server) postEgressRule(w http.ResponseWriter, r *http.Request) {
	var body EgressRuleRequest
	if !readJSON(w, r, &body) {
		return
	}
	host := strings.ToLower(strings.TrimSpace(body.Host))
	if !validEgressHost(host) {
		writeErr(w, http.StatusBadRequest, "host must be a bare hostname or *.wildcard (no scheme/port/path)")
		return
	}
	runID := r.PathValue("id")

	if err := s.Engine.SetEgressRule(r.Context(), runID, host, body.Allow); err != nil {
		status := http.StatusConflict
		if errors.Is(err, engine.ErrEgressOff) {
			status = http.StatusServiceUnavailable
		}
		writeErr(w, status, err.Error())
		return
	}

	// Persist to the project: keep the two lists mutually exclusive so future
	// runs inherit the same effective rule (best-effort — the live change won).
	persisted := false
	if s.Conf != nil {
		if evs, err := s.Store.Events(r.Context(), runID, 0); err == nil && len(evs) > 0 {
			if pid := run.Reduce(evs).ProjectID; pid != "" {
				if p, err := s.Conf.GetProject(r.Context(), pid); err == nil {
					if body.Allow {
						p.Workspace.BlockedDomains = removeStr(p.Workspace.BlockedDomains, host)
						if !containsStr(p.Workspace.ExtraDomains, host) {
							p.Workspace.ExtraDomains = append(p.Workspace.ExtraDomains, host)
						}
					} else {
						p.Workspace.ExtraDomains = removeStr(p.Workspace.ExtraDomains, host)
						if !containsStr(p.Workspace.BlockedDomains, host) {
							p.Workspace.BlockedDomains = append(p.Workspace.BlockedDomains, host)
						}
					}
					if _, err := s.Conf.UpdateProject(r.Context(), pid, p); err == nil {
						persisted = true
					}
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, EgressRuleResult{Host: host, Allow: body.Allow, Persisted: persisted})
}

// postExposePort publishes a workspace port and returns its browser URL
// (the run-page "open the app" path). Idempotent per (run, port).
func (s *Server) postExposePort(w http.ResponseWriter, r *http.Request) {
	var body ExposePortRequest
	if !readJSON(w, r, &body) {
		return
	}
	if body.Port < 1 || body.Port > 65535 {
		writeErr(w, http.StatusBadRequest, "port must be 1-65535")
		return
	}
	url, err := s.Engine.ExposeRunPort(r.Context(), r.PathValue("id"), body.Port)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, engine.ErrPortsOff) {
			status = http.StatusServiceUnavailable
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ExposedPort{Port: body.Port, URL: url})
}

// postOpenEditor lazily starts code-server in the run's workspace and returns
// its browser URL (the run-page "Open editor" path). Blocks while code-server
// boots on first open; idempotent thereafter.
func (s *Server) postOpenEditor(w http.ResponseWriter, r *http.Request) {
	url, err := s.Engine.OpenEditor(r.Context(), r.PathValue("id"))
	if err != nil {
		msg := err.Error()
		status := http.StatusConflict
		switch {
		case errors.Is(err, engine.ErrPortsOff), strings.Contains(msg, "does not support"):
			status = http.StatusServiceUnavailable
		case strings.Contains(msg, "not found"):
			status = http.StatusNotFound
		}
		writeErr(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, EditorURL{URL: url})
}

// postObservedPorts receives the in-workspace shim scanner's listener
// snapshot (run-token auth, like the ping endpoints — which also restricts
// it to live runs). 204 on accept.
func (s *Server) postObservedPorts(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if !s.authPing(w, r, runID) {
		return
	}
	var body ObservedPortsRequest
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.Ports) > 128 {
		writeErr(w, http.StatusBadRequest, "too many ports (max 128)")
		return
	}
	clean := make([]ports.ObservedPort, 0, len(body.Ports))
	for _, p := range body.Ports {
		if p.Port < 1 || p.Port > 65535 {
			continue
		}
		p.Process, p.Service = truncate(p.Process, 64), truncate(p.Service, 64)
		clean = append(clean, p)
	}
	s.Engine.ReportObservedPorts(runID, clean)
	w.WriteHeader(http.StatusNoContent)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// getRunPorts lists the run's LIVE exposures plus not-yet-exposed discovered
// listeners — the registry, not events, is authoritative (exposures are
// engine-process-scoped).
func (s *Server) getRunPorts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	exposed := s.Engine.RunPorts(id)
	if exposed == nil {
		exposed = []portrelay.Exposure{}
	}
	live := map[int]bool{}
	for _, e := range exposed {
		live[e.Port] = true
	}
	observed := []ports.ObservedPort{}
	for _, p := range s.Engine.ObservedPorts(id) {
		if !live[p.Port] { // an exposed port graduates out of the discovered list
			observed = append(observed, p)
		}
	}
	writeJSON(w, http.StatusOK, RunPorts{Ports: exposed, Observed: observed})
}

// getRunResources serves the run's rolling window of resource samples
// (cpu/mem/net) for the run-page graphs — engine memory, like ports:
// the sampler, not events, is authoritative. Empty when nothing was sampled
// yet (no env, a provisioner without the capability, or an engine restart).
func (s *Server) getRunResources(w http.ResponseWriter, r *http.Request) {
	samples := s.Engine.RunResources(r.PathValue("id"))
	if samples == nil {
		samples = []ports.ResourceSample{}
	}
	writeJSON(w, http.StatusOK, RunResources{Samples: samples})
}

func containsStr(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

func removeStr(ss []string, v string) []string {
	out := ss[:0:0]
	for _, s := range ss {
		if s != v {
			out = append(out, s)
		}
	}
	return out
}

func (s *Server) getLog(w http.ResponseWriter, r *http.Request) {
	// The log SNAPSHOT is the log (DESIGN-FILES §5.4): the runs-dir per-stage
	// files are retired; this serves the latest attempt's captured log. The
	// live tail during a running stage stays on the workspace-log route.
	id, stage := r.PathValue("id"), r.PathValue("stage")
	if s.Snapshots == nil {
		writeErr(w, http.StatusNotFound, "log storage is not configured")
		return
	}
	snaps, err := s.Snapshots.ForRun(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var best *filestore.Snapshot
	for i := range snaps {
		sn := &snaps[i]
		if sn.Kind != filestore.KindLog || sn.StageID != stage {
			continue
		}
		if best == nil || sn.Iteration > best.Iteration {
			best = sn
		}
	}
	if best == nil {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("no log for stage %s", stage))
		return
	}
	data, _, err := s.Snapshots.Bytes(r.Context(), best.ID)
	if err != nil {
		writeErr(w, http.StatusGone, "this log's content is no longer stored")
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
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// --- pings ------------------------------------------------------------------

// postPing is the ask_human shim entry (§10): raise, then hold for the answer;
// 200 with the answer, else 204 (the ping id travels in the X-Ping-ID header,
// since the 204 has no body).
//
// The hold is a GRACE PERIOD, not the wait: hold_ms lets the shim ask for a
// window short enough to stay well inside any harness's tool-call timeout, so
// an answer that is already waiting costs the agent nothing, and a slower one
// turns the tool call into "stop and wait" — the engine then holds the real
// wait, untimed, and resumes the stage (see engine.stageAgent).
func (s *Server) postPing(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if !s.authPing(w, r, runID) {
		return
	}
	var body RaisePingRequest
	if !readJSON(w, r, &body) {
		return
	}
	refs := body.Artifacts
	if len(refs) == 0 && body.Artifact != "" {
		refs = []string{body.Artifact}
	}
	p, err := s.Engine.RaisePing(runID, body.Type, body.Question, body.Context, body.Options, refs)
	switch {
	case errors.Is(err, engine.ErrRunNotActive):
		writeErr(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.holdForAnswer(w, r, p, s.holdFor(body.HoldMS))
}

// holdFor clamps a requested grace hold to the server's own limit. A missing
// or nonsensical value keeps the historical behaviour (hold the maximum).
func (s *Server) holdFor(ms int) time.Duration {
	if ms <= 0 {
		return s.HoldTimeout
	}
	return min(time.Duration(ms)*time.Millisecond, s.HoldTimeout)
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
	s.holdForAnswer(w, r, p, s.holdFor(0))
}

func (s *Server) holdForAnswer(w http.ResponseWriter, r *http.Request, p *engine.Ping, hold time.Duration) {
	w.Header().Set("X-Ping-ID", p.ID)
	ctx, cancel := context.WithTimeout(r.Context(), hold)
	defer cancel()
	if ans, ok := p.Await(ctx); ok {
		// The agent gets the answer in-band: as far as it is concerned the
		// question is closed, so the stage need not pause on it.
		p.HandOff(true)
		// Text is the composed answer, rendered by the engine so the tool
		// return and a resumed stage's message say the same thing; Answer and
		// Feedback stay as the raw fields behind it.
		writeJSON(w, http.StatusOK, PingHold{
			PingID: p.ID, Text: engine.ComposeAnswer(p),
			Answer: ans.Answer, Feedback: ans.Feedback,
			Artifacts: p.DeliveredArtifacts(), // workspace paths of the human's answer files
		})
		return
	}
	// The turn goes back to the agent without an answer. From here on, any
	// answer — however fast — is one the agent has NOT seen, so its stage must
	// pause on the question and deliver it on resume rather than let the
	// invocation close as if it had been answered.
	p.HandOff(false)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) answerPing(w http.ResponseWriter, r *http.Request) {
	var body AnswerPingRequest
	if !readJSON(w, r, &body) {
		return
	}
	pid := r.PathValue("pid")
	// Claim the uploaded files for this ping BEFORE AnswerPing records the
	// answer, so delivery (which queries by ping_id) finds them. The run id
	// comes from the live registry when a loop is holding the ping, and from
	// the durable projection when the run is parked on it — a parked run has
	// no registry entry, and its answers are the ones most likely to carry
	// attachments (the human had time to gather them). The ids also ride on
	// the answer, so ping_answered records the association as a replayable
	// fact.
	if s.Attachments != nil && len(body.AttachmentIDs) > 0 {
		runID := ""
		if p := s.Engine.GetPing(pid); p != nil {
			runID = p.RunID
		} else if s.Pings != nil {
			if v, err := s.Pings.GetPing(r.Context(), pid); err == nil {
				runID = v.RunID
			}
		}
		if runID != "" {
			if err := s.Attachments.ClaimForRun(r.Context(), body.AttachmentIDs, runID, pid); err != nil {
				s.Log.Warn("claim answer attachments", "ping", pid, "err", err)
			}
		}
	}
	err := s.Engine.AnswerPing(pid, engine.PingAnswer{Answer: body.Answer, Feedback: body.Feedback,
		AttachmentIDs: body.AttachmentIDs})
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
		writeJSON(w, http.StatusOK, AnswerAccepted{Status: "answered"})
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

// --- channels ---------------------------------------------------------------

// maskChannel redacts secret-shaped config values (tokens etc.) before an
// Entry leaves the server: channel storage is a documented plaintext
// residual, but responses carry only masks, like integrations (§3).
func maskChannel(e *channels.Entry) map[string]any {
	cfg := make(map[string]any, len(e.Config))
	for k, v := range e.Config {
		lk := strings.ToLower(k)
		secret := strings.Contains(lk, "token") || strings.Contains(lk, "secret") ||
			strings.Contains(lk, "password") || strings.Contains(lk, "key") ||
			strings.Contains(lk, "webhook") // e.g. Slack webhook_url embeds its secret
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
