package httpapi

// Automations (flow v4): the ways a run begins without a person pressing
// Start — a tracker query polled into tickets and runs, or a schedule for
// one ticket. Rules of a project, validated by confstore the way a firing
// would be; internal/automation does the firing from the daemon.

import (
	"errors"
	"net/http"

	"apothecary/internal/confstore"
)

func (s *Server) listAutomations(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Conf.ListAutomations(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []confstore.Automation{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func automationFrom(projectID string, body AutomationRequest) confstore.Automation {
	a := confstore.Automation{
		ProjectID: projectID, Name: body.Name, Kind: body.Kind,
		SourceID: body.SourceID, Query: body.Query, Schedule: body.Schedule, TicketID: body.TicketID,
		Flow: body.Flow, Phase: body.Phase, Params: body.Params, Enabled: true,
	}
	if body.Enabled != nil {
		a.Enabled = *body.Enabled
	}
	return a
}

func (s *Server) createAutomation(w http.ResponseWriter, r *http.Request) {
	var body AutomationRequest
	if !readJSON(w, r, &body) {
		return
	}
	a, err := s.Conf.CreateAutomation(r.Context(), automationFrom(r.PathValue("id"), body))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

// automationOf loads an automation under its project: one of another
// project is not found here.
func (s *Server) automationOf(r *http.Request) (confstore.Automation, error) {
	a, err := s.Conf.GetAutomation(r.Context(), r.PathValue("aid"))
	if err != nil {
		return a, err
	}
	if a.ProjectID != r.PathValue("id") {
		return a, errors.Join(confstore.ErrNotFound, errors.New("automation "+a.ID+" belongs to another project"))
	}
	return a, nil
}

func (s *Server) getAutomation(w http.ResponseWriter, r *http.Request) {
	a, err := s.automationOf(r)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) updateAutomation(w http.ResponseWriter, r *http.Request) {
	if _, err := s.automationOf(r); err != nil {
		confErr(w, err)
		return
	}
	var body AutomationRequest
	if !readJSON(w, r, &body) {
		return
	}
	a, err := s.Conf.UpdateAutomation(r.Context(), r.PathValue("aid"), automationFrom(r.PathValue("id"), body))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) deleteAutomation(w http.ResponseWriter, r *http.Request) {
	if _, err := s.automationOf(r); err != nil {
		confErr(w, err)
		return
	}
	if err := s.Conf.DeleteAutomation(r.Context(), r.PathValue("aid")); err != nil {
		confErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
