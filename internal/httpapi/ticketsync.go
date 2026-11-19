package httpapi

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/platform/livecache"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/resolution"
	"apothecary/internal/project/tickets"
	"apothecary/internal/project/tickets/internaltracker"
)

// A ticket's status, labels and comments are its tracker's: the remote
// issue's, else the internal tracker's. The ticket page writes them through
// that tracker — a remote write drops the cached copies of the issue
// (tickets.Live.Writer), so the answer reads it anew — and an integration's
// status and type lists are read and edited here.

// TicketWriters resolves a remote ticket source's writer
// (integrations.Resolver.TicketWriter).
type TicketWriters interface {
	TicketWriter(ctx context.Context, integrationID string) (trackerport.TicketWriter, error)
}

// liveErr maps a remote tracker's read failure: a tracker holding off for
// its rate limit with nothing cached 503 with Retry-After; an issue the
// tracker does not have 404; no tracker wiring 503; the config store's own
// errors as confErr; the remote tracker failing 502.
func liveErr(w http.ResponseWriter, err error) {
	sourceErr(w, err, http.StatusBadGateway)
}

// fail is liveErr for the workshop's source: on the internal tracker,
// whose rows are the config store's, any other failure is a 500.
func (ws workshop) fail(w http.ResponseWriter, err error) {
	fallback := http.StatusBadGateway
	if ws.internal() {
		fallback = http.StatusInternalServerError
	}
	sourceErr(w, err, fallback)
}

func sourceErr(w http.ResponseWriter, err error, fallback int) {
	var held *livecache.HeldError
	var limited *trackerport.ErrRateLimited
	switch {
	case errors.As(err, &held):
		retryLater(w, time.Until(held.Until), err)
	case errors.As(err, &limited):
		wait := limited.RetryAfter
		if wait <= 0 {
			wait = tickets.RateLimitBackoff
		}
		retryLater(w, wait, err)
	case errors.Is(err, trackerport.ErrTicketNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, tickets.ErrNoSources):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, confstore.ErrNotFound), errors.Is(err, confstore.ErrInvalid), errors.Is(err, confstore.ErrConflict),
		errors.Is(err, confstore.ErrReferenced), errors.Is(err, resolution.ErrUnresolvable):
		confErr(w, err)
	default:
		writeErr(w, fallback, err.Error())
	}
}

// retryLater is the 503 of a rate-limited tracker, Retry-After in whole
// seconds (at least one).
func retryLater(w http.ResponseWriter, wait time.Duration, err error) {
	secs := max(int(math.Ceil(wait.Seconds())), 1)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeErr(w, http.StatusServiceUnavailable, err.Error())
}

// --- writes through the ticket's tracker -------------------------------------

// TicketStatusRequest moves a ticket to a status of its tracker, named as
// the tracker names it (a status, or on a remote tracker a transition).
type TicketStatusRequest struct {
	Status string `json:"status"`
}

// TicketLabelsRequest replaces a ticket's labels.
type TicketLabelsRequest struct {
	Labels []string `json:"labels"`
}

// TicketCommentRequest is a person's comment on a ticket (markdown).
type TicketCommentRequest struct {
	Body string `json:"body"`
}

// TicketComment is one comment on a ticket.
type TicketComment struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body_md"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Origin is the internal tracker's: user (a person's) or apothecary (a
	// run's); "" on a remote issue's.
	Origin string `json:"origin,omitempty"`
	// RunID is the run that posted it (internal tracker).
	RunID string `json:"run_id,omitempty"`
}

// writerOf is the workshop's tracker's writer: the internal tracker, or the
// remote source's, wrapped so a write drops the issue's cached copies.
func (s *Server) writerOf(ctx context.Context, ws workshop) (trackerport.TicketWriter, error) {
	if ws.internal() {
		return internaltracker.New(s.Conf), nil
	}
	if s.Trackers == nil {
		return nil, tickets.ErrNoSources
	}
	tw, err := s.Trackers.TicketWriter(ctx, ws.source.ID)
	if err != nil {
		return nil, err
	}
	return s.Live.Writer(ws.source.ID, tw), nil
}

// throughTracker runs one write on the workshop's tracker, on the route's
// issue (which must be the source's), and returns the workshop and the
// issue's key for the answer. ok=false means the response is written.
func (s *Server) throughTracker(w http.ResponseWriter, r *http.Request, write func(ctx context.Context, tw trackerport.TicketWriter, key string) error) (workshop, string, bool) {
	ws, ok := s.workshop(w, r)
	if !ok {
		return ws, "", false
	}
	ctx := r.Context()
	issue, _, err := s.issueAt(ctx, ws, r.PathValue("key"), false)
	if err != nil {
		ws.fail(w, err)
		return ws, "", false
	}
	tw, err := s.writerOf(ctx, ws)
	if err != nil {
		ws.writeFail(w, err)
		return ws, "", false
	}
	if err := write(ctx, tw, issue.Key); err != nil {
		ws.writeFail(w, err)
		return ws, "", false
	}
	return ws, issue.Key, true
}

// writeFail maps a tracker write's failure: a status the issue cannot move
// to (naming those it can), a field the tracker does not have, or a write
// with no meaning on it 400; the rest as a read's (fail).
func (ws workshop) writeFail(w http.ResponseWriter, err error) {
	var unreachable *trackerport.ErrStatusUnreachable
	var unknown *trackerport.ErrFieldUnknown
	var unsupported *trackerport.ErrFieldUnsupported
	var na *trackerport.ErrNotApplicable
	switch {
	case errors.As(err, &unreachable), errors.As(err, &unknown), errors.As(err, &unsupported), errors.As(err, &na):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		ws.fail(w, err)
	}
}

// setTicketStatus moves the ticket to the status its tracker names so.
func (s *Server) setTicketStatus(w http.ResponseWriter, r *http.Request) {
	var body TicketStatusRequest
	if !readJSON(w, r, &body) {
		return
	}
	status := strings.TrimSpace(body.Status)
	if status == "" {
		writeErr(w, http.StatusBadRequest, "status: a status name is required")
		return
	}
	ws, key, ok := s.throughTracker(w, r, func(ctx context.Context, tw trackerport.TicketWriter, key string) error {
		return tw.Transition(ctx, key, status)
	})
	if ok {
		s.writeTicketAt(w, r, http.StatusOK, ws, key)
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
	ws, key, ok := s.throughTracker(w, r, func(ctx context.Context, tw trackerport.TicketWriter, key string) error {
		return tw.SetFields(ctx, key, map[string]string{"labels": strings.Join(labels, ", ")})
	})
	if ok {
		s.writeTicketAt(w, r, http.StatusOK, ws, key)
	}
}

// listTicketComments is the ticket's comments, oldest first: a remote
// issue's read live, an internal ticket's rows.
func (s *Server) listTicketComments(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workshop(w, r)
	if !ok {
		return
	}
	issue, _, err := s.issueAt(r.Context(), ws, r.PathValue("key"), false)
	if err != nil {
		ws.fail(w, err)
		return
	}
	s.writeComments(w, r, http.StatusOK, ws, issue.Key, r.URL.Query().Get("refresh") == "1")
}

func (s *Server) writeComments(w http.ResponseWriter, r *http.Request, status int, ws workshop, key string, bypass bool) {
	out := []TicketComment{}
	if ws.internal() {
		list, err := s.Conf.TicketComments(r.Context(), key)
		if err != nil {
			confErr(w, err)
			return
		}
		for _, c := range list {
			out = append(out, TicketComment{ID: c.ID, Author: c.Author, Body: c.Body,
				CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Origin: c.Origin, RunID: c.RunID})
		}
		writeJSON(w, status, out)
		return
	}
	list, err := s.Live.Comments(r.Context(), ws.source.ID, key, bypass)
	if err != nil {
		liveErr(w, err)
		return
	}
	for _, c := range list {
		out = append(out, TicketComment{ID: c.ID, Author: c.Author, Body: c.Body, CreatedAt: c.Created, UpdatedAt: c.Updated})
	}
	writeJSON(w, status, out)
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
	ws, key, ok := s.throughTracker(w, r, func(ctx context.Context, tw trackerport.TicketWriter, key string) error {
		return tw.Comment(ctx, key, text)
	})
	if ok {
		s.writeComments(w, r, http.StatusCreated, ws, key, false)
	}
}

// --- an integration's status and type lists ----------------------------------

// TrackerStatusList is a ticketsource integration's statuses, in order.
// ReadOnly: a remote tracker's, read live from it; the internal tracker's
// are edited here.
type TrackerStatusList struct {
	Statuses []confstore.TrackerStatus `json:"statuses"`
	ReadOnly bool                      `json:"read_only"`
}

// TrackerStatusesUpdate saves the internal tracker's Statuses table: a row
// with no id is new, a missing one is deleted, and the order given is kept.
// ReplaceWith maps a deleted status that tickets still use to the kept one
// they move to.
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

// ticketSourceOf is the route's integration as a ticket source, or the
// refusal: another port.
func (s *Server) ticketSourceOf(w http.ResponseWriter, r *http.Request) (confstore.Integration, bool) {
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

// listTrackerStatuses is the integration's statuses (trackerStatuses).
func (s *Server) listTrackerStatuses(w http.ResponseWriter, r *http.Request) {
	in, ok := s.ticketSourceOf(w, r)
	if !ok {
		return
	}
	list, err := s.trackerStatuses(r.Context(), in, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		workshop{source: in}.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// listWorkshopTicketStatuses is the statuses of the workshop's ticket
// source: what its tickets move through, read with ticket:read on the
// workshop rather than integration:read on the source, which is its owner's.
func (s *Server) listWorkshopTicketStatuses(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workshop(w, r)
	if !ok {
		return
	}
	list, err := s.trackerStatuses(r.Context(), ws.source, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		ws.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// trackerStatuses is the internal tracker's rows, or a remote tracker's
// list read live (refresh: now rather than the cached copy), read-only:
// Done is its done category.
func (s *Server) trackerStatuses(ctx context.Context, in confstore.Integration, refresh bool) (TrackerStatusList, error) {
	if in.Kind == confstore.KindInternal {
		list, err := s.Conf.TrackerStatuses(ctx, in.ID)
		return statusList(list, false), err
	}
	if s.Live == nil {
		return TrackerStatusList{}, tickets.ErrNoSources
	}
	remote, err := s.Live.Statuses(ctx, in.ID, refresh)
	if err != nil {
		return TrackerStatusList{}, err
	}
	list := make([]confstore.TrackerStatus, len(remote))
	for i, st := range remote {
		list[i] = confstore.TrackerStatus{ID: st.ID, IntegrationID: in.ID, RemoteID: st.ID, Name: st.Name,
			Position: i, IsDone: st.Category == trackerport.CategoryDone}
	}
	return statusList(list, true), nil
}

func (s *Server) putTrackerStatuses(w http.ResponseWriter, r *http.Request) {
	var body TrackerStatusesUpdate
	if !readJSON(w, r, &body) {
		return
	}
	list, err := s.Conf.PutTrackerStatuses(r.Context(), r.PathValue("id"), body.Statuses, body.ReplaceWith)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, statusList(list, false))
}

func statusList(list []confstore.TrackerStatus, readOnly bool) TrackerStatusList {
	if list == nil {
		list = []confstore.TrackerStatus{}
	}
	return TrackerStatusList{Statuses: list, ReadOnly: readOnly}
}

// listTrackerTypes is the integration's types (trackerTypes).
func (s *Server) listTrackerTypes(w http.ResponseWriter, r *http.Request) {
	in, ok := s.ticketSourceOf(w, r)
	if !ok {
		return
	}
	list, err := s.trackerTypes(r.Context(), in, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		liveErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// listWorkshopTicketTypes is the types of the workshop's ticket source, read
// with ticket:read on the workshop like listWorkshopTicketStatuses.
func (s *Server) listWorkshopTicketTypes(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workshop(w, r)
	if !ok {
		return
	}
	list, err := s.trackerTypes(r.Context(), ws.source, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		ws.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// trackerTypes is the source's type list as stored, a remote tracker's read
// first (refresh: now rather than the cached copy) so a new type gets its row.
func (s *Server) trackerTypes(ctx context.Context, in confstore.Integration, refresh bool) (TrackerTypeList, error) {
	var list []confstore.TrackerType
	var err error
	switch {
	case in.Kind == confstore.KindInternal:
		list, err = s.Conf.TrackerTypes(ctx, in.ID)
	case s.Live == nil:
		err = tickets.ErrNoSources
	default:
		list, err = s.Live.Types(ctx, in.ID, refresh)
	}
	return typeList(list), err
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
