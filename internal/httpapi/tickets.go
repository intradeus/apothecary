package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/authz"
	"apothecary/internal/engine"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/tickets"
	"apothecary/internal/run"
	"apothecary/internal/run/runinput"
	"apothecary/internal/workspace/checkout"
)

// A workshop reads its tickets live from its one ticket source (the
// project's ticket_source_id): a remote tracker, or its owner's internal
// tracker. The routes are keyed by the workshop and the tracker's key — on
// the internal tracker the ticket's id. A remote issue has a local record
// only once something needs one (a run, an automation, a base branch); a row
// says which, and every workshop on the same source sees the same record.

// ticketBody creates an internal ticket, or edits one. BaseBranch is the
// ticket's BASE branch ("" = each run names it by its workshop's branch
// format); each run derives its own branch from it. On a remote issue an
// edit takes BaseBranch only: everything else is its tracker's.
type ticketBody struct {
	Title      string `json:"title"`
	Goal       string `json:"goal"`
	BaseBranch string `json:"base_branch"`
	// ChangeType is a trackerport.ChangeType; empty is feat.
	ChangeType string `json:"change_type"`
	// StatusID / TypeID place a new ticket (the tracker's first status, and
	// the first type of its change type, when empty); an edit takes TypeID.
	StatusID      string   `json:"status_id,omitempty"`
	TypeID        string   `json:"type_id,omitempty"`
	Labels        []string `json:"labels,omitempty"`
	AttachmentIDs []string `json:"attachment_ids"`
}

// TicketRow is one issue of a workshop's ticket source as its list shows it,
// joined to its record when Apothecary has one.
type TicketRow struct {
	// Key is the tracker's key ("PROJ-123"); an internal ticket's id.
	Key string `json:"key"`
	// RemoteID is the tracker's stable id for the issue.
	RemoteID string `json:"remote_id"`
	Title    string `json:"title"`
	// Body is markdown: the issue's description, an internal ticket's goal.
	Body string `json:"body"`
	// URL is the issue's page on its tracker; "" on the internal tracker.
	URL      string `json:"url"`
	Status   string `json:"status"`
	StatusID string `json:"status_id"`
	// Done: the status is in the tracker's done category.
	Done   bool   `json:"done"`
	Type   string `json:"type"`
	TypeID string `json:"type_id"`
	// ChangeType is the issue's type read through the source's type list
	// (an internal ticket's own).
	ChangeType trackerport.ChangeType `json:"change_type"`
	Labels     []string               `json:"labels"`
	Assignee   string                 `json:"assignee"`
	UpdatedAt  *time.Time             `json:"updated_at,omitempty"`
	// TicketID is the local record's id; "" while the issue has none.
	TicketID string `json:"ticket_id"`
	// BaseBranch is the record's user-set base branch; "" = each run names it.
	BaseBranch string `json:"base_branch"`
	// ActiveRunID is the record's active run, in any workshop; "" when none.
	ActiveRunID string `json:"active_run_id"`
}

// TicketPage is one page of a workshop's ticket list.
type TicketPage struct {
	Tickets []TicketRow `json:"tickets"`
	// Next is the following page's cursor; "" on the last one.
	Next string `json:"next"`
}

// TicketSourceRef names a ticket source integration.
type TicketSourceRef struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// TicketDetail is one issue of a workshop's source: its row, the source it
// is read from, and the record's attachments and links.
type TicketDetail struct {
	TicketRow
	Source TicketSourceRef `json:"source"`
	// Internal: the source is the internal tracker, so the ticket is edited
	// here (a remote issue takes only its base branch).
	Internal bool `json:"internal"`
	// AttachmentIDs are the record's claimed attachments, for the edit
	// form's re-send semantics.
	AttachmentIDs []string `json:"attachment_ids"`
	// Links are the internal tracker's links on the ticket (ticket_link,
	// ticket_link_branch); a remote issue keeps its own.
	Links []confstore.TicketLink `json:"links"`
}

// RemoteAttachment is a file on a remote issue, metadata only: a run's start
// copies it onto the record.
type RemoteAttachment struct {
	Filename string `json:"filename"`
	Mime     string `json:"mime,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

// TicketAttachments is what a ticket page lists: the record's files
// (uploaded, or copied from the tracker at a run's start) and the files the
// remote issue has now.
type TicketAttachments struct {
	Attachments []filestore.Attachment `json:"attachments"`
	Remote      []RemoteAttachment     `json:"remote"`
}

// WorkshopRef names a workshop.
type WorkshopRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// TicketRecord is a ticket record by id — what a run links to — with the
// key it is opened by, its source (absent once the integration is gone)
// and the owner's workshops that read that source.
type TicketRecord struct {
	Ticket    confstore.Ticket `json:"ticket"`
	Key       string           `json:"key"`
	Source    *TicketSourceRef `json:"source,omitempty"`
	Workshops []WorkshopRef    `json:"workshops"`
}

// tickets builds the ticket factory from the server's collaborators.
func (s *Server) tickets() *tickets.Factory {
	return &tickets.Factory{
		Conf: s.Conf, Attachments: s.Attachments, Sources: s.Sources, Live: s.Live, Log: s.Log,
	}
}

// workshop is a route's workshop and the ticket source it reads.
type workshop struct {
	project confstore.Project
	source  confstore.Integration
}

func (ws workshop) internal() bool { return ws.source.Kind == confstore.KindInternal }

func (ws workshop) sourceRef() TicketSourceRef {
	return TicketSourceRef{ID: ws.source.ID, Kind: ws.source.Kind, Name: ws.source.Name}
}

// workshop loads the route's workshop (the guard made sure it is the
// caller's) and its ticket source.
func (s *Server) workshop(w http.ResponseWriter, r *http.Request) (workshop, bool) {
	if s.Live == nil {
		writeErr(w, http.StatusServiceUnavailable, "ticket sources are not wired")
		return workshop{}, false
	}
	p, err := s.Conf.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return workshop{}, false
	}
	in, err := s.Conf.GetIntegration(r.Context(), p.TicketSourceID)
	if err != nil {
		confErr(w, err)
		return workshop{}, false
	}
	return workshop{project: p, source: in}, true
}

// issueAt reads the source's issue at key, and its record when it has one.
// An internal ticket of another tracker is not found.
func (s *Server) issueAt(ctx context.Context, ws workshop, key string, bypass bool) (trackerport.SourceTicket, *confstore.Ticket, error) {
	issue, err := s.Live.Issue(ctx, ws.source.ID, key, bypass)
	if err != nil {
		return issue, nil, err
	}
	if ws.internal() {
		t, err := s.Conf.GetTicket(ctx, issue.Key)
		if err != nil {
			return issue, nil, err
		}
		return issue, &t, nil
	}
	if issue.RemoteID == "" {
		return issue, nil, nil
	}
	t, found, err := s.Conf.TicketByRemote(ctx, ws.source.ID, issue.RemoteID)
	if err != nil || !found {
		return issue, nil, err
	}
	return issue, &t, nil
}

// recordAt is issueAt with the record made now when the issue has none.
func (s *Server) recordAt(ctx context.Context, ws workshop, key string) (trackerport.SourceTicket, confstore.Ticket, error) {
	issue, rec, err := s.issueAt(ctx, ws, key, false)
	if err != nil {
		return issue, confstore.Ticket{}, err
	}
	if ws.internal() {
		return issue, *rec, nil
	}
	t, _, err := s.tickets().Record(ctx, ws.source.ID, issue, "")
	return issue, t, err
}

// runRecord is the record a run starts on: recordAt, or — when the tracker
// cannot be read (down, rate-limited, nothing cached) — the record already
// last seen under key, so a tracker problem never stops a run on an issue
// Apothecary already knows; the engine starts it from the record as last
// seen. Only an issue with no record needs the tracker.
func (s *Server) runRecord(ctx context.Context, ws workshop, key string) (confstore.Ticket, error) {
	_, t, err := s.recordAt(ctx, ws, key)
	if err == nil || ws.internal() {
		return t, err
	}
	rec, found, ferr := s.Conf.TicketBySourceKey(ctx, ws.source.ID, key)
	if ferr != nil || !found {
		return t, err
	}
	s.Log.Warn("ticket run: tracker unread, starting from the record", "integration", ws.source.ID, "key", key, "ticket", rec.ID, "err", err)
	return rec, nil
}

// ticketRows renders a page's rows: change types through the source's type
// list, and each record's active run.
func (s *Server) ticketRows(ctx context.Context, ws workshop, rows []tickets.LiveRow) ([]TicketRow, error) {
	changeTypes := map[string]trackerport.ChangeType{}
	if !ws.internal() && len(rows) > 0 {
		// A type list that cannot be read now leaves each row to its record
		// or a guess from its type's name: the list still shows.
		types, err := s.Live.Types(ctx, ws.source.ID, false)
		if err != nil {
			s.Log.Warn("ticket source types", "integration", ws.source.ID, "err", err)
		}
		for _, ty := range types {
			changeTypes[ty.RemoteID] = ty.ChangeType
		}
	}
	var ids []string
	for _, row := range rows {
		if row.Record != nil {
			ids = append(ids, row.Record.ID)
		}
	}
	active, err := s.activeTicketRuns(ctx, ws.project.UserID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]TicketRow, len(rows))
	for i, row := range rows {
		is := row.Issue
		out[i] = TicketRow{
			Key: is.Key, RemoteID: is.RemoteID, Title: is.Title, Body: is.Body, URL: is.URL,
			Status: is.Status, StatusID: is.StatusID, Done: is.Done, Type: is.Type, TypeID: is.TypeID,
			Labels: is.Labels, Assignee: is.Assignee,
		}
		if out[i].Labels == nil {
			out[i].Labels = []string{}
		}
		if !is.Updated.IsZero() {
			at := is.Updated.UTC()
			out[i].UpdatedAt = &at
		}
		typeID := strings.TrimSpace(is.TypeID)
		if typeID == "" {
			typeID = strings.TrimSpace(is.Type)
		}
		ct, listed := changeTypes[typeID]
		switch {
		case listed && !ws.internal():
			out[i].ChangeType = ct
		case row.Record != nil:
			out[i].ChangeType = row.Record.ChangeType
		default:
			out[i].ChangeType = tickets.SourceChangeType(is)
		}
		if rec := row.Record; rec != nil {
			out[i].TicketID, out[i].BaseBranch, out[i].ActiveRunID = rec.ID, rec.BaseBranch, active[rec.ID]
		}
	}
	return out, nil
}

// activeTicketRuns is the active run of each of the records, by record id:
// one read of the owner's runs. A record's runs are its owner's, in whichever
// workshop on its source they started.
func (s *Server) activeTicketRuns(ctx context.Context, owner string, ticketIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ticketIDs) == 0 {
		return out, nil
	}
	want := make(map[string]bool, len(ticketIDs))
	for _, id := range ticketIDs {
		want[id] = true
	}
	views, err := (&run.Reader{Store: s.Store}).List(ctx, run.ListFilter{Seen: run.Seen{UserID: owner}})
	if err != nil {
		return nil, err
	}
	for _, v := range views {
		if want[v.TicketID] && !v.State.Terminal() && out[v.TicketID] == "" {
			out[v.TicketID] = v.RunID
		}
	}
	return out, nil
}

// ticketAttachmentIDs lists the ids claimed by a ticket (for responses and
// run starts). Empty slice, never nil.
func (s *Server) ticketAttachmentIDs(ctx context.Context, ticketID string) []string {
	ids := []string{}
	if s.Attachments == nil {
		return ids
	}
	atts, err := s.Attachments.ForTicket(ctx, ticketID)
	if err != nil {
		s.Log.Warn("ticket attachments", "ticket", ticketID, "err", err)
		return ids
	}
	for _, a := range atts {
		ids = append(ids, a.ID)
	}
	return ids
}

// ticketDetail is the one-ticket response.
func (s *Server) ticketDetail(ctx context.Context, ws workshop, issue trackerport.SourceTicket, rec *confstore.Ticket) (TicketDetail, error) {
	rows, err := s.ticketRows(ctx, ws, []tickets.LiveRow{{Issue: issue, Record: rec}})
	if err != nil {
		return TicketDetail{}, err
	}
	out := TicketDetail{TicketRow: rows[0], Source: ws.sourceRef(), Internal: ws.internal(),
		AttachmentIDs: []string{}, Links: []confstore.TicketLink{}}
	if rec == nil {
		return out, nil
	}
	out.AttachmentIDs = s.ticketAttachmentIDs(ctx, rec.ID)
	if ws.internal() {
		links, err := s.Conf.TicketLinks(ctx, rec.ID)
		if err != nil {
			return TicketDetail{}, err
		}
		if links != nil {
			out.Links = links
		}
	}
	return out, nil
}

func (s *Server) writeDetail(w http.ResponseWriter, r *http.Request, status int, ws workshop, issue trackerport.SourceTicket, rec *confstore.Ticket) {
	d, err := s.ticketDetail(r.Context(), ws, issue, rec)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, status, d)
}

// writeTicketAt answers with the issue at key as it reads now.
func (s *Server) writeTicketAt(w http.ResponseWriter, r *http.Request, status int, ws workshop, key string) {
	issue, rec, err := s.issueAt(r.Context(), ws, key, false)
	if err != nil {
		ws.fail(w, err)
		return
	}
	s.writeDetail(w, r, status, ws, issue, s.seen(r.Context(), ws, issue, rec))
}

// seen writes what the issue shows now to its record (its last-seen
// columns), when it has one: a read, or the reread after a write, keeps the
// record current. A failure is logged and the record answered as it was.
func (s *Server) seen(ctx context.Context, ws workshop, issue trackerport.SourceTicket, rec *confstore.Ticket) *confstore.Ticket {
	if rec == nil || ws.internal() {
		return rec
	}
	t, err := s.tickets().Seen(ctx, rec.ID, issue)
	if err != nil {
		s.Log.Warn("ticket record: last seen", "ticket", rec.ID, "err", err)
		return rec
	}
	return &t
}

// listWorkshopTickets is one page of the workshop's source, live.
func (s *Server) listWorkshopTickets(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workshop(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	sq := trackerport.SearchQuery{Text: q.Get("q"), Done: trackerport.DoneOpen, Cursor: q.Get("cursor")}
	switch q.Get("done") {
	case "", "open":
	case "done":
		sq.Done = trackerport.DoneOnly
	case "any":
		sq.Done = trackerport.DoneAny
	default:
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("done %q: open, done or any", q.Get("done")))
		return
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("limit %q: a positive number", v))
			return
		}
		sq.Limit = n
	}
	page, err := s.Live.List(r.Context(), ws.source.ID, sq, q.Get("refresh") == "1")
	if err != nil {
		ws.fail(w, err)
		return
	}
	rows, err := s.ticketRows(r.Context(), ws, page.Rows)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, TicketPage{Tickets: rows, Next: page.Next})
}

// checkBaseBranch refuses a base branch git would refuse.
func checkBaseBranch(w http.ResponseWriter, branch string) bool {
	if branch == "" {
		return true
	}
	if err := checkout.CheckBranch(branch); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("base branch %q: %v", branch, err))
		return false
	}
	return true
}

// notInternal refuses a write only the internal tracker takes.
func notInternal(w http.ResponseWriter, ws workshop, what string) {
	writeErr(w, http.StatusBadRequest, fmt.Sprintf("workshop %s reads its tickets from %s (%s): %s there",
		ws.project.Name, ws.source.Name, ws.source.Kind, what))
}

// createWorkshopTicket writes a ticket on the workshop's internal tracker —
// the workshop owner's, whoever writes it — and claims the caller's uploads
// for the ticket.
func (s *Server) createWorkshopTicket(w http.ResponseWriter, r *http.Request) {
	var body ticketBody
	if !readJSON(w, r, &body) {
		return
	}
	ws, ok := s.workshop(w, r)
	if !ok || !checkBaseBranch(w, body.BaseBranch) {
		return
	}
	if !ws.internal() {
		notInternal(w, ws, "a ticket is created")
		return
	}
	t, err := s.tickets().Create(r.Context(), confstore.Ticket{
		UserID: ws.project.UserID, Title: body.Title, Goal: body.Goal, BaseBranch: body.BaseBranch,
		ChangeType: trackerport.ChangeType(body.ChangeType), StatusID: body.StatusID, TypeID: body.TypeID, Labels: body.Labels,
	}, auth.UserID(r.Context()), body.AttachmentIDs)
	if err != nil {
		confErr(w, err)
		return
	}
	s.writeTicketAt(w, r, http.StatusCreated, ws, t.ID)
}

// getWorkshopTicket is one issue, live, with its record; a record takes
// what the issue shows now (its last-seen columns).
func (s *Server) getWorkshopTicket(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workshop(w, r)
	if !ok {
		return
	}
	issue, rec, err := s.issueAt(r.Context(), ws, r.PathValue("key"), r.URL.Query().Get("refresh") == "1")
	if err != nil {
		ws.fail(w, err)
		return
	}
	s.writeDetail(w, r, http.StatusOK, ws, issue, s.seen(r.Context(), ws, issue, rec))
}

// updateWorkshopTicket edits an internal ticket, or sets a remote issue's
// base branch — making its record when it has none.
func (s *Server) updateWorkshopTicket(w http.ResponseWriter, r *http.Request) {
	var body ticketBody
	if !readJSON(w, r, &body) {
		return
	}
	ws, ok := s.workshop(w, r)
	if !ok || !checkBaseBranch(w, body.BaseBranch) {
		return
	}
	ctx := r.Context()
	if !ws.internal() {
		_, rec, err := s.recordAt(ctx, ws, r.PathValue("key"))
		if err != nil {
			ws.fail(w, err)
			return
		}
		if _, err := s.tickets().Update(ctx, rec.ID, confstore.Ticket{BaseBranch: body.BaseBranch}, "", nil); err != nil {
			confErr(w, err)
			return
		}
		s.writeTicketAt(w, r, http.StatusOK, ws, r.PathValue("key"))
		return
	}
	_, rec, err := s.issueAt(ctx, ws, r.PathValue("key"), false)
	if err != nil {
		ws.fail(w, err)
		return
	}
	// The factory claims any newly-attached uploads: the caller's, who need
	// not own the ticket.
	if _, err := s.tickets().Update(ctx, rec.ID, confstore.Ticket{
		Title: body.Title, Goal: body.Goal, BaseBranch: body.BaseBranch,
		ChangeType: trackerport.ChangeType(body.ChangeType), TypeID: body.TypeID,
	}, auth.UserID(ctx), body.AttachmentIDs); err != nil {
		confErr(w, err)
		return
	}
	s.writeTicketAt(w, r, http.StatusOK, ws, rec.ID)
}

// deleteWorkshopTicket deletes an internal ticket and its attachment rows.
func (s *Server) deleteWorkshopTicket(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workshop(w, r)
	if !ok {
		return
	}
	if !ws.internal() {
		notInternal(w, ws, "a ticket is deleted")
		return
	}
	_, rec, err := s.issueAt(r.Context(), ws, r.PathValue("key"), false)
	if err != nil {
		ws.fail(w, err)
		return
	}
	if err := s.tickets().Delete(r.Context(), rec.ID); err != nil {
		confErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// startWorkshopTicketRun starts a new run (attempt) on an issue of the
// workshop's source, in this workshop, making its record first when it has
// none. Everything in the body is per-attempt and optional: {flow} overrides
// the workshop's default, while {goal, attachment_ids} add direction and
// files for THIS try on top of the ticket's own. The engine reads the
// ticket now at the start and names the base branch by this workshop's
// format unless the record has one. A ticket has one active run at a time,
// across workshops: 409 with run_id naming it (runStartErr).
func (s *Server) startWorkshopTicketRun(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workshop(w, r)
	if !ok {
		return
	}
	// Body is optional (empty ⇒ workshop default flow, no direction).
	var body StartRunRequest
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)

	ctx := r.Context()
	t, err := s.runRecord(ctx, ws, r.PathValue("key"))
	if err != nil {
		ws.fail(w, err)
		return
	}
	in := runinput.Ticket{
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
		TicketAttachmentIDs: s.ticketAttachmentIDs(ctx, t.ID),
	}
	if !t.Internal() {
		// Where the issue lives, for the flow's tracker actions.
		in.SourceID, in.SourceKind, in.SourceKey, in.SourceURL = t.SourceID, t.SourceKind, t.SourceKey, t.SourceURL
	}
	runID, err := s.Runs.CreateRunWith(ctx, in, body.Flow, ws.project.ID,
		runinput.Input{Params: body.Params, MR: body.MR, Skillsets: body.Skillsets, Starter: auth.UserID(ctx)})
	if err != nil {
		runStartErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, RunRef{RunID: runID})
}

// getWorkshopTicketAttachments lists the record's files (none for a remote
// issue without one) and the remote issue's own. Metadata only — bytes come
// from the byte route.
func (s *Server) getWorkshopTicketAttachments(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workshop(w, r)
	if !ok {
		return
	}
	issue, rec, err := s.issueAt(r.Context(), ws, r.PathValue("key"), false)
	if err != nil {
		ws.fail(w, err)
		return
	}
	out := TicketAttachments{Attachments: []filestore.Attachment{}, Remote: []RemoteAttachment{}}
	if rec != nil && s.Attachments != nil {
		atts, err := s.Attachments.ForTicket(r.Context(), rec.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out.Attachments = append(out.Attachments, atts...)
	}
	if !ws.internal() {
		for _, a := range issue.Attachments {
			out.Remote = append(out.Remote, RemoteAttachment{Filename: a.Filename, Mime: a.Mime, Size: a.Size})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// getTicketRecord is a record by id, for links from runs: the UI opens it in
// a workshop on its source.
func (s *Server) getTicketRecord(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t, err := s.Conf.GetTicket(ctx, r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	out := TicketRecord{Ticket: t, Key: t.TrackerKey(), Workshops: []WorkshopRef{}}
	in, err := s.Conf.GetIntegration(ctx, t.SourceID)
	switch {
	case errors.Is(err, confstore.ErrNotFound):
	case err != nil:
		confErr(w, err)
		return
	default:
		out.Source = &TicketSourceRef{ID: in.ID, Kind: in.Kind, Name: in.Name}
	}
	projects, err := s.Conf.ListProjects(ctx, t.UserID)
	if err != nil {
		confErr(w, err)
		return
	}
	// The owner's workshops on the source, as far as the caller sees them:
	// a member is not told the names of the owner's other workshops.
	v, err := s.visible(ctx, authz.KindProject)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, p := range projects {
		if p.TicketSourceID == t.SourceID && v.Sees(p.UserID, p.ID) {
			out.Workshops = append(out.Workshops, WorkshopRef{ID: p.ID, Name: p.Name})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// runBusy is a start refused for a run in the way: a work item's active
// run (workitem.ErrBusy).
type runBusy interface {
	error
	ActiveRunID() string
}

// runStartErr answers a refused run start: 409 for a ticket or a merge
// request that already has an active run — the body's run_id names it, so the client can open or
// abort it — and for the environment cap, 400 for every other refusal
// (preflight, a bad branch, a missing flow).
func runStartErr(w http.ResponseWriter, err error) {
	var busy runBusy
	switch {
	case errors.As(err, &busy):
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: err.Error(), RunID: busy.ActiveRunID()})
	case errors.Is(err, engine.ErrEnvCapacity):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}

// ticketRoutes is a workshop's tickets, and a ticket record by id.
func (s *Server) ticketRoutes() []Route {
	const one = "/api/projects/{id}/tickets/{key}"
	refresh := Param{Name: "refresh", Doc: "1 = read the tracker now rather than the cached copy", Enum: []string{"1"}}
	return []Route{
		{Method: "GET", Path: "/api/projects/{id}/tickets", ID: "listWorkshopTickets", Tag: tagTickets,
			Summary: "One page of a workshop's tickets, read live from its ticket source",
			Doc: "Each row carries its local record's id, base branch and active run (in any workshop) when the issue has a record. " +
				"A tracker holding off for its rate limit with nothing cached is 503 with Retry-After.",
			Query: []Param{
				{Name: "q", Doc: "free-text query"},
				{Name: "done", Doc: "open (default), done or any", Enum: []string{"open", "done", "any"}},
				{Name: "cursor", Doc: "the previous page's next"},
				{Name: "limit", Doc: "page size (default: the tracker's)", Type: "integer"},
				refresh,
			},
			Handler: s.listWorkshopTickets, Returns: TicketPage{}, Enabled: haveConf, Scope: ScopeProject, Action: authz.TicketRead,
			Tool: &Tool{Name: "list_tickets", Doc: "One page of a workshop's tickets, read from its ticket source (the built-in tracker or a remote one). Open tickets unless done says otherwise; q searches the text. Each row has the ticket's key, title, status and the run working on it, if any. Pass next back as cursor for the following page.", Args: map[string]string{"id": argWorkshop}}},
		{Method: "POST", Path: "/api/projects/{id}/tickets", ID: "createWorkshopTicket", Tag: tagTickets,
			Summary: "Create a ticket on a workshop's internal tracker",
			Doc:     "A workshop reading a remote tracker creates its tickets there (400).",
			Handler: s.createWorkshopTicket, Body: ticketBody{}, Returns: TicketDetail{},
			Status: http.StatusCreated, Enabled: haveConf, Scope: ScopeProject, Action: authz.TicketWrite,
			Tool: &Tool{Name: "create_ticket", Doc: "Create a ticket on a workshop's built-in tracker: title (one line), goal (what done looks like, in Markdown: the agent working the ticket reads it), change_type, and optionally type_id (list_ticket_types) and base_branch. A workshop reading a remote tracker refuses it: its tickets are made there.", Args: map[string]string{"id": argWorkshop}}},
		{Method: "GET", Path: "/api/projects/{id}/ticket-statuses", ID: "listWorkshopTicketStatuses", Tag: tagTickets,
			Summary: "The statuses a workshop's tickets move through, in order",
			Doc: "Its ticket source's list, as listTrackerStatuses gives it, read by anyone who reads the workshop's tickets: the source is its owner's. " +
				"A tracker holding off for its rate limit with nothing cached is 503 with Retry-After.",
			Query:   []Param{refresh},
			Handler: s.listWorkshopTicketStatuses, Returns: TrackerStatusList{}, Enabled: haveConf, Scope: ScopeProject, Action: authz.TicketRead,
			Tool: &Tool{Name: "list_ticket_statuses", Doc: "The statuses a workshop's tickets move through, in order.", Args: map[string]string{"id": argWorkshop}}},
		{Method: "GET", Path: "/api/projects/{id}/ticket-types", ID: "listWorkshopTicketTypes", Tag: tagTickets,
			Summary: "The issue types of a workshop's tickets, in order",
			Doc:     "Its ticket source's list, as listTrackerTypes gives it, read by anyone who reads the workshop's tickets: the source is its owner's.",
			Query:   []Param{refresh},
			Handler: s.listWorkshopTicketTypes, Returns: TrackerTypeList{}, Enabled: haveConf, Scope: ScopeProject, Action: authz.TicketRead,
			Tool: &Tool{Name: "list_ticket_types", Doc: "The issue types a workshop's tickets can have.", Args: map[string]string{"id": argWorkshop}}},
		{Method: "GET", Path: one, ID: "getWorkshopTicket", Tag: tagTickets,
			Summary: "One ticket of a workshop's source, by its key",
			Doc:     "Read live; a record takes what the issue shows now. An issue the tracker does not have is 404.",
			Query:   []Param{refresh},
			Handler: s.getWorkshopTicket, Returns: TicketDetail{}, Enabled: haveConf, Scope: ScopeProject, Action: authz.TicketRead,
			Tool: &Tool{Name: "get_ticket", Doc: "One ticket of a workshop: its title, goal, status, type, labels, base branch and its runs.", Args: map[string]string{"id": argWorkshop, "key": argTicket}}},
		{Method: "PUT", Path: one, ID: "updateWorkshopTicket", Tag: tagTickets,
			Summary: "Edit a ticket",
			Doc: "An internal ticket takes title, goal, base_branch, change_type, type_id and attachment_ids. " +
				"A remote issue takes base_branch only (the rest is its tracker's), its record made when it has none.",
			Handler: s.updateWorkshopTicket, Body: ticketBody{}, Returns: TicketDetail{}, Enabled: haveConf, Scope: ScopeProject, Action: authz.TicketWrite,
			Tool: &Tool{Name: "update_ticket", Doc: "Edit a ticket. One of the built-in tracker takes title, goal, change_type, type_id and base_branch; a remote tracker's issue takes base_branch only.", Args: map[string]string{"id": argWorkshop, "key": argTicket}}},
		{Method: "DELETE", Path: one, ID: "deleteWorkshopTicket", Tag: tagTickets,
			Summary: "Delete an internal ticket",
			Doc:     "A remote issue is its tracker's (400).",
			Handler: s.deleteWorkshopTicket, Status: http.StatusNoContent, Enabled: haveConf, Scope: ScopeProject, Action: authz.TicketWrite},
		{Method: "GET", Path: one + "/attachments", ID: "getWorkshopTicketAttachments", Tag: tagTickets,
			Summary: "Files of a ticket: its record's, and the remote issue's",
			Handler: s.getWorkshopTicketAttachments, Returns: TicketAttachments{}, Enabled: haveConf, Scope: ScopeProject, Action: authz.TicketRead},
		{Method: "POST", Path: one + "/runs", ID: "startWorkshopTicketRun", Tag: tagTickets,
			Summary: "Start one attempt on a ticket, in this workshop",
			Doc: "The issue's record is made first when it has none; an issue that has one starts from it even while its tracker cannot be read. " +
				"A ticket has at most one active (non-terminal) run across workshops: while one is going the start is refused with 409, the error's run_id naming it.",
			Handler: s.startWorkshopTicketRun, Body: StartRunRequest{}, Returns: RunRef{},
			Status: http.StatusCreated, Enabled: haveConf, Scope: ScopeProject, Action: authz.RunOperate,
			Tool: &Tool{Name: "start_run", Doc: "Start a run on a ticket: the workshop's default flow unless flow names another of its flows. Answers the run's id; follow it with wait_run. A ticket with a run still going is refused (409, naming that run).", Args: map[string]string{"id": argWorkshop, "key": argTicket}}},
		{Method: "POST", Path: one + "/status", ID: "setWorkshopTicketStatus", Tag: tagTickets,
			Summary: "Move a ticket to a status of its tracker, by name",
			Doc:     "A status the issue cannot reach is 400, naming those it can; the remote tracker failing 502.",
			Handler: s.setTicketStatus, Body: TicketStatusRequest{}, Returns: TicketDetail{}, Enabled: haveConf, Scope: ScopeProject, Action: authz.TicketWrite},
		{Method: "PUT", Path: one + "/labels", ID: "setWorkshopTicketLabels", Tag: tagTickets,
			Summary: "Replace a ticket's labels on its tracker",
			Handler: s.setTicketLabels, Body: TicketLabelsRequest{}, Returns: TicketDetail{}, Enabled: haveConf, Scope: ScopeProject, Action: authz.TicketWrite},
		{Method: "GET", Path: one + "/comments", ID: "listWorkshopTicketComments", Tag: tagTickets,
			Summary: "A ticket's comments, oldest first",
			Doc:     "A remote issue's are read live.",
			Query:   []Param{refresh},
			Handler: s.listTicketComments, Returns: []TicketComment{}, Enabled: haveConf, Scope: ScopeProject, Action: authz.TicketRead,
			Tool: &Tool{Name: "list_ticket_comments", Doc: "A ticket's comments, oldest first.", Args: map[string]string{"id": argWorkshop, "key": argTicket}}},
		{Method: "POST", Path: one + "/comments", ID: "addWorkshopTicketComment", Tag: tagTickets,
			Summary: "Comment on a ticket, on its tracker",
			Doc:     "Answers with the ticket's comments.",
			Handler: s.addTicketComment, Body: TicketCommentRequest{}, Returns: []TicketComment{},
			Status: http.StatusCreated, Enabled: haveConf, Scope: ScopeProject, Action: authz.TicketWrite},
		{Method: "GET", Path: "/api/tickets/{id}", ID: "getTicketRecord", Tag: tagTickets,
			Summary: "A ticket record by id, for links from runs",
			Doc:     "With the key it is opened by, its source (absent once the integration is deleted) and the workshops that read that source.",
			Handler: s.getTicketRecord, Returns: TicketRecord{}, Enabled: haveConf, Scope: ScopeTicket, Action: authz.TicketRead},
	}
}
