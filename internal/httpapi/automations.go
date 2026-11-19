package httpapi

// Automations (flow v4): the ways a run begins without a person pressing
// Start — a tracker query polled into tickets and runs, or a schedule for
// one ticket. Rules of a project, validated by confstore the way a firing
// would be; internal/project/automation does the firing from the daemon.

import (
	"errors"
	"net/http"
	"strings"

	"apothecary/internal/account/authz"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
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
		Query: body.Query, Schedule: body.Schedule, TicketID: body.TicketID, Repo: body.Repo,
		Flow: body.Flow, Params: body.Params, Enabled: true,
	}
	if body.Enabled != nil {
		a.Enabled = *body.Enabled
	}
	return a
}

// automationTicket resolves body's ticket_key — a row of the workshop's live
// list — into the ticket_id a cron automation fires on, making the remote
// issue's record when it has none. A key the workshop's source does not know
// is 400. ok=false means the response is written.
func (s *Server) automationTicket(w http.ResponseWriter, r *http.Request, body *AutomationRequest) bool {
	key := strings.TrimSpace(body.TicketKey)
	if key == "" {
		return true
	}
	if body.Kind != confstore.AutomationCron {
		writeErr(w, http.StatusBadRequest, "only a cron automation takes ticket_key")
		return false
	}
	if strings.TrimSpace(body.TicketID) != "" {
		writeErr(w, http.StatusBadRequest, "a cron automation takes ticket_key or ticket_id, not both")
		return false
	}
	ws, ok := s.workshop(w, r)
	if !ok {
		return false
	}
	_, t, err := s.recordAt(r.Context(), ws, key)
	if errors.Is(err, trackerport.ErrTicketNotFound) {
		writeErr(w, http.StatusBadRequest, "ticket_key: "+err.Error())
		return false
	}
	if err != nil {
		ws.fail(w, err)
		return false
	}
	body.TicketID = t.ID
	return true
}

// An automation starts runs with no person: writing one needs run:operate
// beside project:author, or a token granted authoring alone would start runs.
func (s *Server) automationRuns(w http.ResponseWriter, r *http.Request) bool {
	return s.projectIn(w, r, authz.RunOperate, r.PathValue("id"))
}

func (s *Server) createAutomation(w http.ResponseWriter, r *http.Request) {
	var body AutomationRequest
	if !s.automationRuns(w, r) || !readJSON(w, r, &body) || !s.automationTicket(w, r, &body) {
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
	if !s.automationRuns(w, r) || !readJSON(w, r, &body) || !s.automationTicket(w, r, &body) {
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
