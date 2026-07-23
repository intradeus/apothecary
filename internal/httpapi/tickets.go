package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"apothecary/internal/artifacts"
	"apothecary/internal/confstore"
	"apothecary/internal/engine"
)

// ticketBody is the create/update DTO. Branch is the ticket's BASE branch;
// each run derives its own actual branch (base + per-run tail).
type ticketBody struct {
	ProjectID   string   `json:"project_id"`
	Title       string   `json:"title"`
	Goal        string   `json:"goal"`
	Branch      string   `json:"branch"`
	ArtifactIDs []string `json:"artifact_ids"`
}

func (b ticketBody) toTicket() confstore.Ticket {
	return confstore.Ticket{
		ProjectID: b.ProjectID, Title: b.Title, Goal: b.Goal,
		BaseBranch: b.Branch, ArtifactIDs: b.ArtifactIDs,
	}
}

func (s *Server) listTickets(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Conf.ListTickets(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Optional ?project= filter, mirroring the runs list.
	if pid := r.URL.Query().Get("project"); pid != "" {
		filtered := rows[:0]
		for _, t := range rows {
			if t.ProjectID == pid {
				filtered = append(filtered, t)
			}
		}
		rows = filtered
	}
	if rows == nil {
		rows = []confstore.Ticket{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) createTicket(w http.ResponseWriter, r *http.Request) {
	var body ticketBody
	if !readJSON(w, r, &body) {
		return
	}
	if body.Branch != "" {
		if err := engine.CheckBranch(body.Branch); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("branch %q: %v", body.Branch, err))
			return
		}
	}
	t, err := s.Conf.CreateTicket(r.Context(), body.toTicket())
	if err != nil {
		confErr(w, err)
		return
	}
	s.claimTicketArtifacts(r, t)
	writeJSON(w, http.StatusCreated, t)
}

// claimTicketArtifacts binds the ticket's uploads to the TICKET (not a run):
// it moves them out of `orphan` so the artifact GC can't sweep them, and
// leaves run_id empty so every run of the ticket can be handed the same
// files. Already-claimed ids are a no-op (Associate only touches orphans),
// so it is safe to re-send the full list on update.
func (s *Server) claimTicketArtifacts(r *http.Request, t confstore.Ticket) {
	if s.Artifacts == nil || len(t.ArtifactIDs) == 0 {
		return
	}
	if err := s.Artifacts.Associate(r.Context(), t.ArtifactIDs, "", "", artifacts.ScopeTicketGoal); err != nil {
		s.Log.Warn("associate ticket artifacts", "ticket", t.ID, "err", err)
	}
}

func (s *Server) getTicket(w http.ResponseWriter, r *http.Request) {
	t, err := s.Conf.GetTicket(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) updateTicket(w http.ResponseWriter, r *http.Request) {
	var body ticketBody
	if !readJSON(w, r, &body) {
		return
	}
	if body.Branch != "" {
		if err := engine.CheckBranch(body.Branch); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("branch %q: %v", body.Branch, err))
			return
		}
	}
	t, err := s.Conf.UpdateTicket(r.Context(), r.PathValue("id"), body.toTicket())
	if err != nil {
		confErr(w, err)
		return
	}
	s.claimTicketArtifacts(r, t) // claims any newly-attached uploads
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) deleteTicket(w http.ResponseWriter, r *http.Request) {
	if err := s.Conf.DeleteTicket(r.Context(), r.PathValue("id")); err != nil {
		confErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// startTicketRun starts a new run (attempt) under a ticket. Everything in the
// body is per-attempt and optional: {flow} overrides the project default,
// while {goal, artifact_ids} add direction and files for THIS try on top of
// the ticket's own (they never modify the ticket). The run derives its own
// unique branch from the ticket's base branch.
func (s *Server) startTicketRun(w http.ResponseWriter, r *http.Request) {
	t, err := s.Conf.GetTicket(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	// Body is optional (empty ⇒ project default flow, no direction).
	var body struct {
		Flow        string   `json:"flow"`
		Goal        string   `json:"goal"`
		ArtifactIDs []string `json:"artifact_ids"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)

	runID, err := s.Engine.CreateRun(r.Context(), engine.Ticket{
		TicketID: t.ID,
		Title:    t.Title,
		Goal:     t.Goal,
		Branch:   t.BaseBranch,
		RunGoal:  body.Goal,
		// This attempt's own uploads bind to the run; the ticket's are shared
		// by every run and only snapshotted here.
		ArtifactIDs:       body.ArtifactIDs,
		TicketArtifactIDs: t.ArtifactIDs,
	}, body.Flow, t.ProjectID)
	switch {
	case errors.Is(err, engine.ErrEnvCapacity):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusCreated, map[string]string{"run_id": runID})
	}
}
