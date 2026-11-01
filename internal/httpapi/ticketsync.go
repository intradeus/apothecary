package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/resolution"
	"apothecary/internal/project/tickets"
)

// Tickets that follow their tracker (docs/ticket-sync-plan.md): a ticket's
// status, type, labels and comments are its tracker's — the remote issue it
// mirrors, else Apothecary's internal tracker. The ticket page writes them
// through that tracker (a remote write is followed by a refresh of the
// mirror), refreshes the mirror on demand, and an integration's status and
// type lists are edited here.

// TicketTrackers resolves a ticket's tracker as its row stands now
// (integrations.Resolver.TicketTracker): the remote issue's writer and its
// key, else the internal tracker and the ticket's id.
type TicketTrackers interface {
	TicketTracker(ctx context.Context, ticketID string) (trackerport.TicketWriter, string, error)
}

// TicketStatusRef is a ticket's status, resolved from its tracker's list.
type TicketStatusRef struct {
	ID       string               `json:"id"`
	Name     string               `json:"name"`
	IsDone   bool                 `json:"is_done"`
	FlowStep trackerport.FlowStep `json:"flow_step,omitempty"`
}

// TicketTypeRef is a ticket's type, resolved from its tracker's list.
type TicketTypeRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// TicketView is a ticket with its status and type resolved (absent on an
// imported ticket not synced yet).
type TicketView struct {
	confstore.Ticket
	Status *TicketStatusRef `json:"status,omitempty"`
	Type   *TicketTypeRef   `json:"type,omitempty"`
}

// ticketViews resolves the tickets' statuses and types, reading each
// tracker's lists once.
func (s *Server) ticketViews(ctx context.Context, ts []confstore.Ticket) ([]TicketView, error) {
	statuses := map[string]confstore.TrackerStatus{}
	types := map[string]confstore.TrackerType{}
	loaded := map[string]bool{}
	for _, t := range ts {
		_, haveStatus := statuses[t.StatusID]
		_, haveType := types[t.TypeID]
		if (t.StatusID == "" || haveStatus) && (t.TypeID == "" || haveType) {
			continue
		}
		trackerID, err := s.Conf.TicketTrackerID(ctx, t)
		if err != nil {
			return nil, err
		}
		if loaded[trackerID] {
			continue
		}
		loaded[trackerID] = true
		sl, err := s.Conf.TrackerStatuses(ctx, trackerID)
		if err != nil {
			return nil, err
		}
		for _, st := range sl {
			statuses[st.ID] = st
		}
		tl, err := s.Conf.TrackerTypes(ctx, trackerID)
		if err != nil {
			return nil, err
		}
		for _, ty := range tl {
			types[ty.ID] = ty
		}
	}
	out := make([]TicketView, len(ts))
	for i, t := range ts {
		out[i].Ticket = t
		if st, ok := statuses[t.StatusID]; ok {
			out[i].Status = &TicketStatusRef{ID: st.ID, Name: st.Name, IsDone: st.IsDone, FlowStep: st.FlowStep}
		}
		if ty, ok := types[t.TypeID]; ok {
			out[i].Type = &TicketTypeRef{ID: ty.ID, Name: ty.Name}
		}
	}
	return out, nil
}

// ticketResp builds the one-ticket response: the view, its attachments and
// its links.
func (s *Server) ticketResp(r *http.Request, t confstore.Ticket, warnings []string) (ticketResp, error) {
	views, err := s.ticketViews(r.Context(), []confstore.Ticket{t})
	if err != nil {
		return ticketResp{}, err
	}
	links, err := s.Conf.TicketLinks(r.Context(), t.ID)
	if err != nil {
		return ticketResp{}, err
	}
	if links == nil {
		links = []confstore.TicketLink{}
	}
	return ticketResp{TicketView: views[0], AttachmentIDs: s.ticketAttachmentIDs(r, t.ID), Links: links, Warnings: warnings}, nil
}

// writeTicket answers with the ticket as it is stored now.
func (s *Server) writeTicket(w http.ResponseWriter, r *http.Request, status int, t confstore.Ticket, warnings []string) {
	resp, err := s.ticketResp(r, t, warnings)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, status, resp)
}

// --- refresh -----------------------------------------------------------------

// syncTicket re-mirrors one ticket from its remote tracker (done or not) and
// answers with it. A tracker that cannot be read is recorded on the ticket
// (sync_state error, sync_error), which the answer carries; a ticket with no
// source is answered as it is.
func (s *Server) syncTicket(w http.ResponseWriter, r *http.Request) {
	if s.Syncer == nil {
		writeErr(w, http.StatusServiceUnavailable, "ticket sync is not wired")
		return
	}
	id := r.PathValue("id")
	if err := s.Syncer.Refresh(r.Context(), id); errors.Is(err, confstore.ErrNotFound) {
		confErr(w, err)
		return
	} else if err != nil {
		s.Log.Warn("ticket refresh", "ticket", id, "err", err)
	}
	t, err := s.Conf.GetTicket(r.Context(), id)
	if err != nil {
		confErr(w, err)
		return
	}
	s.writeTicket(w, r, http.StatusOK, t, nil)
}

// remoteTicketSource is the integration as a remote ticketsource, or the
// refusal: another port, or the internal tracker (nothing to fetch).
func (s *Server) remoteTicketSource(w http.ResponseWriter, r *http.Request) (confstore.Integration, bool) {
	in, err := s.Conf.GetIntegration(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return in, false
	}
	if in.Port != confstore.PortTicketSource {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("integration %s is a %s integration, not a ticket source", in.Name, in.Port))
		return in, false
	}
	return in, true
}

// syncIntegration re-mirrors every ticket of a remote ticketsource
// integration, done ones included, after refreshing its lists.
func (s *Server) syncIntegration(w http.ResponseWriter, r *http.Request) {
	if s.Syncer == nil {
		writeErr(w, http.StatusServiceUnavailable, "ticket sync is not wired")
		return
	}
	in, ok := s.remoteTicketSource(w, r)
	if !ok {
		return
	}
	if in.Kind == confstore.KindInternal {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("integration %s is the internal tracker: its tickets are written in place, there is nothing to sync", in.Name))
		return
	}
	counts, err := s.Syncer.RefreshIntegration(r.Context(), in.ID)
	if err != nil {
		sourceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, counts)
}

// --- writes through the ticket's tracker -------------------------------------

// TicketStatusRequest moves a ticket to one of its tracker's statuses.
type TicketStatusRequest struct {
	StatusID string `json:"status_id"`
}

// TicketLabelsRequest replaces a ticket's labels.
type TicketLabelsRequest struct {
	Labels []string `json:"labels"`
}

// TicketCommentRequest is a person's comment on a ticket (markdown).
type TicketCommentRequest struct {
	Body string `json:"body"`
}

// throughTracker runs one write on the ticket's tracker — the remote issue,
// then a refresh of the mirror so the answer shows it; else the internal
// tracker, which writes the row — and reads the ticket back. done=false means
// the response is written (a refusal).
func (s *Server) throughTracker(w http.ResponseWriter, r *http.Request, write func(ctx context.Context, tw trackerport.TicketWriter, key string, t confstore.Ticket) error) (confstore.Ticket, bool) {
	if s.Trackers == nil {
		writeErr(w, http.StatusServiceUnavailable, "ticket trackers are not wired")
		return confstore.Ticket{}, false
	}
	ctx := r.Context()
	t, err := s.Conf.GetTicket(ctx, r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return t, false
	}
	tw, key, err := s.Trackers.TicketTracker(ctx, t.ID)
	if err != nil {
		trackerErr(w, err)
		return t, false
	}
	if err := write(ctx, tw, key, t); err != nil {
		trackerErr(w, err)
		return t, false
	}
	if t.SourceID != "" && s.Syncer != nil {
		if err := s.Syncer.Refresh(ctx, t.ID); err != nil {
			s.Log.Warn("ticket refresh after a write", "ticket", t.ID, "err", err)
		}
	}
	if t, err = s.Conf.GetTicket(ctx, t.ID); err != nil {
		confErr(w, err)
		return t, false
	}
	return t, true
}

// trackerErr maps a tracker write's failure: the config store's own errors as
// confErr; a status the issue cannot move to from where it is 409; a field the
// tracker does not have, or a write with no meaning on it, 400; the remote
// tracker failing 502.
func trackerErr(w http.ResponseWriter, err error) {
	var unreachable *trackerport.ErrStatusUnreachable
	var unknown *trackerport.ErrFieldUnknown
	var unsupported *trackerport.ErrFieldUnsupported
	var na *trackerport.ErrNotApplicable
	switch {
	case errors.Is(err, confstore.ErrNotFound), errors.Is(err, confstore.ErrInvalid), errors.Is(err, confstore.ErrConflict),
		errors.Is(err, confstore.ErrRemoteOwned), errors.Is(err, resolution.ErrUnresolvable):
		confErr(w, err)
	case errors.As(err, &unreachable):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.As(err, &unknown), errors.As(err, &unsupported), errors.As(err, &na):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeErr(w, http.StatusBadGateway, err.Error())
	}
}

// setTicketStatus moves the ticket to one of its tracker's statuses (by id;
// the tracker is asked by the status's name).
func (s *Server) setTicketStatus(w http.ResponseWriter, r *http.Request) {
	var body TicketStatusRequest
	if !readJSON(w, r, &body) {
		return
	}
	t, ok := s.throughTracker(w, r, func(ctx context.Context, tw trackerport.TicketWriter, key string, t confstore.Ticket) error {
		trackerID, err := s.Conf.TicketTrackerID(ctx, t)
		if err != nil {
			return err
		}
		list, err := s.Conf.TrackerStatuses(ctx, trackerID)
		if err != nil {
			return err
		}
		for _, st := range list {
			if st.ID == body.StatusID {
				return tw.Transition(ctx, key, st.Name)
			}
		}
		return fmt.Errorf("%w: status %q is not one of ticket %s's tracker's", confstore.ErrInvalid, body.StatusID, t.ID)
	})
	if ok {
		s.writeTicket(w, r, http.StatusOK, t, nil)
	}
}

// setTicketLabels replaces the ticket's labels on its tracker.
func (s *Server) setTicketLabels(w http.ResponseWriter, r *http.Request) {
	var body TicketLabelsRequest
	if !readJSON(w, r, &body) {
		return
	}
	labels := make([]string, 0, len(body.Labels))
	for _, l := range body.Labels {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		if strings.Contains(l, ",") {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("label %q: a label cannot contain a comma", l))
			return
		}
		labels = append(labels, l)
	}
	t, ok := s.throughTracker(w, r, func(ctx context.Context, tw trackerport.TicketWriter, key string, _ confstore.Ticket) error {
		return tw.SetFields(ctx, key, map[string]string{"labels": strings.Join(labels, ", ")})
	})
	if ok {
		s.writeTicket(w, r, http.StatusOK, t, nil)
	}
}

// listTicketComments is the ticket's comments, oldest first: the remote
// issue's mirrored ones, a person's and the runs'.
func (s *Server) listTicketComments(w http.ResponseWriter, r *http.Request) {
	t, err := s.Conf.GetTicket(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	s.writeComments(w, r, http.StatusOK, t.ID)
}

func (s *Server) writeComments(w http.ResponseWriter, r *http.Request, status int, ticketID string) {
	list, err := s.Conf.TicketComments(r.Context(), ticketID)
	if err != nil {
		confErr(w, err)
		return
	}
	if list == nil {
		list = []confstore.TicketComment{}
	}
	writeJSON(w, status, list)
}

// addTicketComment posts a person's comment on the ticket's tracker and
// answers with the comments as they stand after it.
func (s *Server) addTicketComment(w http.ResponseWriter, r *http.Request) {
	var body TicketCommentRequest
	if !readJSON(w, r, &body) {
		return
	}
	text := strings.TrimSpace(body.Body)
	if text == "" {
		writeErr(w, http.StatusBadRequest, "comment: a body is required")
		return
	}
	t, ok := s.throughTracker(w, r, func(ctx context.Context, tw trackerport.TicketWriter, key string, _ confstore.Ticket) error {
		return tw.Comment(ctx, key, text)
	})
	if ok {
		s.writeComments(w, r, http.StatusCreated, t.ID)
	}
}

// --- an integration's status and type lists ----------------------------------

// TrackerStatusList is a ticketsource integration's statuses, in order, and
// what a save is worth a warning about without refusing it.
type TrackerStatusList struct {
	Statuses []confstore.TrackerStatus `json:"statuses"`
	Warnings []string                  `json:"warnings"`
}

// TrackerStatusesUpdate saves the Statuses table. On a remote tracker the rows
// are the fetched ones and only is_done and flow_step are taken; on the
// internal tracker a row with no id is new, a missing one is deleted, and the
// order given is kept. ReplaceWith maps a deleted status that tickets still
// use to the kept one they move to.
type TrackerStatusesUpdate struct {
	Statuses    []confstore.TrackerStatus `json:"statuses"`
	ReplaceWith map[string]string         `json:"replace_with,omitempty"`
}

// TrackerTypeList is a ticketsource integration's types, in order.
type TrackerTypeList struct {
	Types []confstore.TrackerType `json:"types"`
}

// TrackerTypesUpdate saves the Types table, like TrackerStatusesUpdate: a
// remote tracker's rows only take change_type.
type TrackerTypesUpdate struct {
	Types       []confstore.TrackerType `json:"types"`
	ReplaceWith map[string]string       `json:"replace_with,omitempty"`
}

// refreshLists fetches a remote integration's lists when ?refresh=1 asks;
// the warnings are what the refresh could not apply (a legacy Jira status or
// type map naming nothing on the tracker).
func (s *Server) refreshLists(w http.ResponseWriter, r *http.Request, in confstore.Integration) ([]string, bool) {
	if r.URL.Query().Get("refresh") != "1" || in.Kind == confstore.KindInternal {
		return nil, true
	}
	if s.Sources == nil {
		sourceErr(w, tickets.ErrNoSources)
		return nil, false
	}
	src, err := s.Sources.TicketSource(r.Context(), in.ID)
	var warnings []string
	if err == nil {
		warnings, err = tickets.SyncLists(r.Context(), s.Conf, in.ID, src)
	}
	if err != nil {
		sourceErr(w, err)
		return nil, false
	}
	return warnings, true
}

func (s *Server) listTrackerStatuses(w http.ResponseWriter, r *http.Request) {
	in, ok := s.remoteTicketSource(w, r)
	if !ok {
		return
	}
	warnings, ok := s.refreshLists(w, r, in)
	if !ok {
		return
	}
	list, err := s.Conf.TrackerStatuses(r.Context(), in.ID)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, statusList(list, warnings))
}

func (s *Server) putTrackerStatuses(w http.ResponseWriter, r *http.Request) {
	var body TrackerStatusesUpdate
	if !readJSON(w, r, &body) {
		return
	}
	list, warnings, err := s.Conf.PutTrackerStatuses(r.Context(), r.PathValue("id"), body.Statuses, body.ReplaceWith)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, statusList(list, warnings))
}

func statusList(list []confstore.TrackerStatus, warnings []string) TrackerStatusList {
	if list == nil {
		list = []confstore.TrackerStatus{}
	}
	if warnings == nil {
		warnings = []string{}
	}
	return TrackerStatusList{Statuses: list, Warnings: warnings}
}

func (s *Server) listTrackerTypes(w http.ResponseWriter, r *http.Request) {
	in, ok := s.remoteTicketSource(w, r)
	if !ok {
		return
	}
	if _, ok := s.refreshLists(w, r, in); !ok {
		return
	}
	list, err := s.Conf.TrackerTypes(r.Context(), in.ID)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, typeList(list))
}

func (s *Server) putTrackerTypes(w http.ResponseWriter, r *http.Request) {
	var body TrackerTypesUpdate
	if !readJSON(w, r, &body) {
		return
	}
	list, err := s.Conf.PutTrackerTypes(r.Context(), r.PathValue("id"), body.Types, body.ReplaceWith)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, typeList(list))
}

func typeList(list []confstore.TrackerType) TrackerTypeList {
	if list == nil {
		list = []confstore.TrackerType{}
	}
	return TrackerTypeList{Types: list}
}

// --- deleting a ticketsource integration -------------------------------------

// deleteTicketSource deletes a ticketsource integration whose tickets still
// mirror its issues: refused (409, counting the tickets and their active runs)
// unless ?unlink_tickets=1 confirms, which unlinks them onto the internal
// tracker in the same transaction as the delete. handled=false: the
// integration has no such tickets, and the plain delete applies.
func (s *Server) deleteTicketSource(w http.ResponseWriter, r *http.Request, in confstore.Integration) (handled bool) {
	ctx := r.Context()
	linked, err := s.Conf.ListTicketsBySource(ctx, in.ID)
	if err != nil {
		confErr(w, err)
		return true
	}
	if len(linked) == 0 {
		return false
	}
	if r.URL.Query().Get("unlink_tickets") != "1" {
		ids := make([]string, len(linked))
		for i, t := range linked {
			ids[i] = t.ID
		}
		active := 0
		if s.Runs != nil {
			if active, err = s.Runs.ActiveRunsOfTickets(ctx, ids); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return true
			}
		}
		n := len(linked)
		writeJSON(w, http.StatusConflict, ErrorResponse{
			Error: fmt.Sprintf("integration %s: %d ticket(s) mirror its issues; deleting it with ?unlink_tickets=1 unlinks them, and they become Apothecary tickets",
				in.Name, n),
			Tickets: &n, ActiveRuns: &active,
		})
		return true
	}
	if _, err := s.Conf.DeleteIntegrationUnlinking(ctx, in.ID); err != nil {
		confErr(w, err)
		return true
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}
