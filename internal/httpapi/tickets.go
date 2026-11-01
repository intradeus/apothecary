package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/engine"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/resolution"
	"apothecary/internal/project/tickets"
	"apothecary/internal/run/runinput"
)

// ticketBody is the create/update DTO. Branch is the ticket's BASE branch;
// each run derives its own actual branch (base + per-run tail).
//
// SourceID+SourceKey turn a create into an IMPORT: the tracker supplies title,
// goal and attachments, and anything sent here overrides them (the operator
// may have edited the prefilled form). They are create-only — provenance never
// changes on update.
type ticketBody struct {
	ProjectID string `json:"project_id"`
	Title     string `json:"title"`
	Goal      string `json:"goal"`
	Branch    string `json:"branch"`
	// ChangeType is a trackerport.ChangeType; empty on a create is the source
	// issue's (an import) or feat, on an update feat.
	ChangeType    string   `json:"change_type"`
	AttachmentIDs []string `json:"attachment_ids"`
	SourceID      string   `json:"source_id"`
	SourceKey     string   `json:"source_key"`
}

func (b ticketBody) toTicket() confstore.Ticket {
	return confstore.Ticket{
		ProjectID: b.ProjectID, Title: b.Title, Goal: b.Goal,
		BaseBranch: b.Branch, ChangeType: trackerport.ChangeType(b.ChangeType),
	}
}

// ticketResp is a ticket plus anything the import could not carry over — a
// skipped attachment is a fact the operator must see, and silence would read
// as "everything came across".
type ticketResp struct {
	confstore.Ticket
	// AttachmentIDs are the ticket's claimed attachments (rows, not a stored
	// column), for the edit form's re-send semantics.
	AttachmentIDs []string `json:"attachment_ids"`
	Warnings      []string `json:"warnings,omitempty"`
}

// ticketAttachmentIDs lists the ids claimed by a ticket (for responses and
// run starts). Empty slice, never nil.
func (s *Server) ticketAttachmentIDs(r *http.Request, ticketID string) []string {
	ids := []string{}
	if s.Attachments == nil {
		return ids
	}
	atts, err := s.Attachments.ForTicket(r.Context(), ticketID)
	if err != nil {
		s.Log.Warn("ticket attachments", "ticket", ticketID, "err", err)
		return ids
	}
	for _, a := range atts {
		ids = append(ids, a.ID)
	}
	return ids
}

// tickets builds the ticket factory from the server's collaborators: the one
// path a ticket is created through, hand-written or imported.
func (s *Server) tickets() *tickets.Factory {
	return &tickets.Factory{
		Conf: s.Conf, Attachments: s.Attachments, Sources: s.Sources, Log: s.Log,
	}
}

func (s *Server) listTickets(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Conf.ListTickets(r.Context(), auth.UserID(r.Context()))
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

// createTicket creates a ticket from scratch or, with {source_id, source_key},
// imports one from a tracker (title/goal/attachments copied over). Both go
// through the same factory, so the resulting ticket is the same kind of thing.
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
	// The project is in the body, so the guard could not check it: a
	// ticket lands only in one of the caller's own projects.
	if !s.ownsProject(w, r, body.ProjectID) {
		return
	}
	if body.SourceID != "" || body.SourceKey != "" {
		s.importTicket(w, r, body)
		return
	}
	t, err := s.tickets().Create(r.Context(), body.toTicket(), body.AttachmentIDs)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ticketResp{Ticket: t, AttachmentIDs: s.ticketAttachmentIDs(r, t.ID)})
}

func (s *Server) importTicket(w http.ResponseWriter, r *http.Request, body ticketBody) {
	if body.SourceID == "" || body.SourceKey == "" {
		writeErr(w, http.StatusBadRequest, "importing a ticket needs both source_id and source_key")
		return
	}
	if !s.ownsIntegration(w, r, body.SourceID) {
		return
	}
	t, warnings, err := s.tickets().CreateFromSource(r.Context(), body.SourceID, body.SourceKey, body.toTicket(), body.AttachmentIDs)
	switch {
	case errors.Is(err, tickets.ErrNoSources):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, tickets.ErrDuplicate):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, confstore.ErrNotFound), errors.Is(err, confstore.ErrInvalid),
		errors.Is(err, resolution.ErrUnresolvable):
		confErr(w, err)
	case err != nil:
		// The tracker itself failed (unreachable, bad credentials, no such
		// issue) — an upstream problem, not a bad request.
		writeErr(w, http.StatusBadGateway, err.Error())
	default:
		writeJSON(w, http.StatusCreated, ticketResp{Ticket: t,
			AttachmentIDs: s.ticketAttachmentIDs(r, t.ID), Warnings: warnings})
	}
}

// --- ticket sources (import picker) -----------------------------------------

// sourceTicketJSON is one tracker issue as the picker renders it. Attachments
// are counted rather than listed in search results (the tracker's list
// endpoint doesn't carry them); the single-issue route has the real list.
type sourceTicketJSON struct {
	Key         string                 `json:"key"`
	Title       string                 `json:"title"`
	Body        string                 `json:"body,omitempty"`
	URL         string                 `json:"url,omitempty"`
	Status      string                 `json:"status,omitempty"`
	Type        string                 `json:"type,omitempty"`
	Assignee    string                 `json:"assignee,omitempty"`
	Updated     string                 `json:"updated,omitempty"`
	Attachments []sourceAttachmentJSON `json:"attachments,omitempty"`
	// ChangeType and Branch are what an import makes of this issue — the
	// ticket's change type and base branch (tickets.BranchName) — for the
	// form to prefill.
	ChangeType trackerport.ChangeType `json:"change_type"`
	Branch     string                 `json:"branch"`
}

type sourceAttachmentJSON struct {
	Filename string `json:"filename"`
	Mime     string `json:"mime,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

func toSourceTicketJSON(t trackerport.SourceTicket) sourceTicketJSON {
	out := sourceTicketJSON{
		Key: t.Key, Title: t.Title, Body: t.Body, URL: t.URL,
		Status: t.Status, Type: t.Type, Assignee: t.Assignee,
		ChangeType: tickets.SourceChangeType(t),
	}
	out.Branch = tickets.BranchName(out.ChangeType, t.Key, t.Title)
	if !t.Updated.IsZero() {
		out.Updated = t.Updated.UTC().Format(time.RFC3339)
	}
	for _, a := range t.Attachments {
		out.Attachments = append(out.Attachments, sourceAttachmentJSON{
			Filename: a.Filename, Mime: a.Mime, Size: a.Size,
		})
	}
	return out
}

// searchSourceTickets serves the picker: GET /api/integrations/{id}/tickets?q=
func (s *Server) searchSourceTickets(w http.ResponseWriter, r *http.Request) {
	limit := 25
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	found, err := s.tickets().Search(r.Context(), r.PathValue("id"), r.URL.Query().Get("q"), limit)
	if err != nil {
		sourceErr(w, err)
		return
	}
	out := make([]sourceTicketJSON, 0, len(found))
	for _, t := range found {
		out = append(out, toSourceTicketJSON(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// getSourceTicket previews one issue (with its attachment list) so the ticket
// form can prefill and say what an import will copy.
func (s *Server) getSourceTicket(w http.ResponseWriter, r *http.Request) {
	t, err := s.tickets().Fetch(r.Context(), r.PathValue("id"), r.PathValue("key"))
	if err != nil {
		sourceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSourceTicketJSON(t))
}

// sourceErr separates "this deployment can't do that" and "you asked for
// something that isn't there" from "the tracker answered badly" (502) — the
// last is the tracker's fault and says so.
func sourceErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tickets.ErrNoSources):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, confstore.ErrNotFound), errors.Is(err, confstore.ErrInvalid),
		errors.Is(err, resolution.ErrUnresolvable):
		confErr(w, err)
	default:
		writeErr(w, http.StatusBadGateway, err.Error())
	}
}

// getTicketArtifacts lists a ticket's attachments (uploaded by hand or copied
// from the tracker) so both the ticket page and the edit form can show what
// is actually attached. Metadata only — bytes come from the byte route. The
// rows ARE the link now (attachments.ticket_id), so this is one query.
func (s *Server) getTicketAttachments(w http.ResponseWriter, r *http.Request) {
	t, err := s.Conf.GetTicket(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	out := []filestore.Attachment{}
	if s.Attachments != nil {
		atts, err := s.Attachments.ForTicket(r.Context(), t.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, atts...)
	}
	writeJSON(w, http.StatusOK, AttachmentList{Attachments: out})
}

func (s *Server) getTicket(w http.ResponseWriter, r *http.Request) {
	t, err := s.Conf.GetTicket(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ticketResp{Ticket: t, AttachmentIDs: s.ticketAttachmentIDs(r, t.ID)})
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
	// The project is in the body here too, and it decides the ticket's owner.
	if !s.ownsProject(w, r, body.ProjectID) {
		return
	}
	// The factory claims any newly-attached uploads; provenance (source_*) is
	// preserved by the store, so an edit never disowns an imported ticket.
	t, err := s.tickets().Update(r.Context(), r.PathValue("id"), body.toTicket(), body.AttachmentIDs)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ticketResp{Ticket: t, AttachmentIDs: s.ticketAttachmentIDs(r, t.ID)})
}

// deleteTicket routes through the factory so the ticket's attachments go
// with it — the fix for the old leak (DESIGN-FILES §12 #3).
func (s *Server) deleteTicket(w http.ResponseWriter, r *http.Request) {
	if err := s.tickets().Delete(r.Context(), r.PathValue("id")); err != nil {
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
	var body StartRunRequest
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)

	runID, err := s.Runs.CreateRunWith(r.Context(), runinput.Ticket{
		TicketID:     t.ID,
		Title:        t.Title,
		Goal:         t.Goal,
		ChangeType:   string(t.ChangeType),
		Branch:       t.BaseBranch,
		SourceBranch: body.SourceBranch,
		RunGoal:      body.Goal,
		// This attempt's own uploads bind to the run; the ticket's are shared
		// by every run and only pinned here (run_created).
		AttachmentIDs:       body.AttachmentIDs,
		TicketAttachmentIDs: s.ticketAttachmentIDs(r, t.ID),
		// Where the ticket came from, for the flow's tracker actions.
		SourceID:   t.SourceID,
		SourceKind: t.SourceKind,
		SourceKey:  t.SourceKey,
		SourceURL:  t.SourceURL,
	}, body.Flow, t.ProjectID, runinput.Input{Params: body.Params, MR: body.MR, Skillsets: body.Skillsets})
	switch {
	case errors.Is(err, engine.ErrEnvCapacity):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusCreated, RunRef{RunID: runID})
	}
}

// ticketRoutes is tickets.
func (s *Server) ticketRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/api/tickets", ID: "listTickets", Tag: tagTickets,
			Summary: "One's tickets",
			Query:   []Param{{Name: "project", Doc: "only tickets of this project"}},
			Handler: s.listTickets, Returns: []confstore.Ticket{}, Enabled: haveConf},
		{Method: "POST", Path: "/api/tickets", ID: "createTicket", Tag: tagTickets,
			Summary: "Create a ticket",
			Handler: s.createTicket, Body: ticketBody{}, Returns: ticketResp{},
			Status: http.StatusCreated, Enabled: haveConf},
		{Method: "GET", Path: "/api/tickets/{id}", ID: "getTicket", Tag: tagTickets,
			Summary: "One ticket", Handler: s.getTicket, Returns: ticketResp{}, Enabled: haveConf, Scope: ScopeTicket},
		{Method: "PUT", Path: "/api/tickets/{id}", ID: "updateTicket", Tag: tagTickets,
			Summary: "Replace a ticket",
			Handler: s.updateTicket, Body: ticketBody{}, Returns: ticketResp{}, Enabled: haveConf, Scope: ScopeTicket},
		{Method: "DELETE", Path: "/api/tickets/{id}", ID: "deleteTicket", Tag: tagTickets,
			Summary: "Delete a ticket", Handler: s.deleteTicket, Status: http.StatusNoContent, Enabled: haveConf, Scope: ScopeTicket},
		{Method: "GET", Path: "/api/tickets/{id}/attachments", ID: "getTicketAttachments", Tag: tagTickets,
			Summary: "Files attached to a ticket",
			Handler: s.getTicketAttachments, Returns: AttachmentList{}, Enabled: haveConf, Scope: ScopeTicket},
		{Method: "POST", Path: "/api/tickets/{id}/runs", ID: "startTicketRun", Tag: tagTickets,
			Summary: "Start one attempt on a ticket",
			Handler: s.startTicketRun, Body: StartRunRequest{}, Returns: RunRef{},
			Status: http.StatusCreated, Enabled: haveConf, Scope: ScopeTicket},
	}
}
