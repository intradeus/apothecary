package confstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"apothecary/internal/ports/trackerport"
	"apothecary/internal/run/event"
)

// Ticket is a first-class unit of work that owns many runs (each run is one
// attempt). It is the DB-backed successor of the transient run-creation input:
// runs now reference a ticket by id and derive their own branch from the
// ticket's BaseBranch. Flow is deliberately NOT here — it's a per-run choice
// made when a run is started.
//
// Every ticket has one tracker (tracker.go): the remote issue it was imported
// from, or the owner's internal tracker. A ticket with a source is a MIRROR of
// its issue — title, goal (the body), status, type and labels are the
// tracker's, overwritten by every sync (MirrorTicket) and refused to an edit
// here (ErrRemoteOwned); writes go to the tracker. A ticket without one is
// written in place.
type Ticket struct {
	ID string `json:"id"`
	// UserID is the owner: the project's, copied onto the row so the
	// per-user list needs no join. Set on every write from the project.
	UserID     string `json:"user_id"`
	ProjectID  string `json:"project_id"`
	Title      string `json:"title"`
	Goal       string `json:"goal"`
	BaseBranch string `json:"base_branch"` // "" = generated per run from the title
	// ChangeType is what the ticket is (trackerport.ChangeType: feat, fix, …; ""
	// on a write means feat). Derived from the ticket's type when it has one;
	// the base branch is named after it at creation, and changing it later
	// renames nothing.
	ChangeType trackerport.ChangeType `json:"change_type"`
	// StatusID and TypeID point into the ticket's tracker's lists
	// (TrackerStatus, TrackerType): the internal tracker's when SourceID is
	// empty. An imported ticket has neither until its first sync.
	StatusID string `json:"status_id,omitempty"`
	TypeID   string `json:"type_id,omitempty"`
	// Labels are the tracker's labels, as it spells them.
	Labels []string `json:"labels,omitempty"`
	// Source* say which remote issue the ticket mirrors (all empty for one
	// typed in by hand; SourceKind/Key/URL without SourceID for a merge
	// request's ticket, which has no remote tracker). SourceKind is stored
	// next to the id so the provenance reads without the integration row.
	SourceID   string `json:"source_id,omitempty"`   // ticketsource integration
	SourceKind string `json:"source_kind,omitempty"` // "jira"
	SourceKey  string `json:"source_key,omitempty"`  // "PROJ-123"
	SourceURL  string `json:"source_url,omitempty"`
	// SourceType is the tracker's own word for the issue's type ("Bug").
	SourceType string `json:"source_type,omitempty"`
	// RemoteID is the issue's stable id on its tracker (a Jira key changes
	// when the issue moves project; this does not).
	RemoteID string `json:"remote_id,omitempty"`
	// RemoteUpdatedAt is the tracker's last-updated time of the snapshot the
	// mirror holds: a sync drops an older one. SyncedAt is when the mirror
	// was last written; SyncState is "" (never synced), ok, error (SyncError
	// says why) or gone (the issue no longer exists, so polling stops).
	RemoteUpdatedAt *time.Time `json:"remote_updated_at,omitempty"`
	SyncedAt        *time.Time `json:"synced_at,omitempty"`
	SyncState       string     `json:"sync_state,omitempty"`
	SyncError       string     `json:"sync_error,omitempty"`
	// StatusChangedAt/By say when and by whom the status last changed: sync,
	// run:<run id>/<stage>, or user.
	StatusChangedAt *time.Time `json:"status_changed_at,omitempty"`
	StatusChangedBy string     `json:"status_changed_by,omitempty"`
	// AutomationID names the automation that imported the ticket, when one
	// did: provenance that lets a later poll tell its own import still
	// waiting for a run from a person's ticket.
	AutomationID string    `json:"automation_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Sync states.
const (
	SyncOK    = "ok"
	SyncError = "error"
	SyncGone  = "gone"
)

// Who changed a ticket's status (status_changed_by), besides a run's
// "run:<id>/<stage>".
const (
	ChangedBySync = "sync"
	ChangedByUser = "user"
)

// TrackerKey is the ticket's key on its tracker: the issue's key for an
// imported ticket, the ticket's own id on the internal tracker.
func (t Ticket) TrackerKey() string {
	if t.SourceID != "" {
		return t.SourceKey
	}
	return t.ID
}

// ErrRemoteOwned refuses an edit, here, of what a remote tracker owns on a
// mirrored ticket (title, goal, status, type, labels): the write goes to the
// tracker, and the sync brings it back. The API maps it to 409.
var ErrRemoteOwned = errors.New("owned by the ticket's remote tracker")

// RemoteOwnedError names the fields an edit tried to change.
type RemoteOwnedError struct {
	TicketID string
	Fields   []string
}

func (e *RemoteOwnedError) Error() string {
	return fmt.Sprintf("ticket %s mirrors a remote issue: %s %s owned by its tracker — change it there", e.TicketID,
		strings.Join(e.Fields, ", "), map[bool]string{true: "is", false: "are"}[len(e.Fields) == 1])
}

func (e *RemoteOwnedError) Is(target error) bool { return target == ErrRemoteOwned }

// CreateTicket validates and inserts a ticket. Branch-format validation is the
// caller's job (the httpapi layer runs checkout.CheckBranch, which confstore
// cannot import) — this only enforces the required fields, that the project
// exists, and that the status and type are its tracker's. A ticket with no
// source lands on its owner's internal tracker, in the status holding the
// todo step and the type of its change type unless told otherwise.
func (s *Service) CreateTicket(ctx context.Context, t Ticket) (Ticket, error) {
	t.ID = s.newID()
	if err := s.validateTicket(ctx, &t); err != nil {
		return Ticket{}, err
	}
	t.CreatedAt = s.now().UTC()
	t.UpdatedAt = t.CreatedAt
	if t.StatusID != "" && t.StatusChangedAt == nil {
		at := t.CreatedAt
		t.StatusChangedAt = &at
	}
	if err := s.db.UpsertTicket(ctx, t); err != nil {
		return Ticket{}, err
	}
	return t, nil
}

// UpdateTicket preserves id/created_at, the provenance and the sync
// bookkeeping, and re-validates. A status or type left empty, and labels left
// nil, keep the stored ones (an edit form may not know them). On a ticket
// with no type, or one whose change type changed while its type did not, the
// type follows the change type. On a mirrored ticket a change to anything its
// tracker owns is refused (*RemoteOwnedError).
func (s *Service) UpdateTicket(ctx context.Context, id string, upd Ticket) (Ticket, error) {
	cur, err := s.db.GetTicket(ctx, id)
	if err != nil {
		return Ticket{}, err
	}
	upd.ID = cur.ID
	upd.CreatedAt = cur.CreatedAt
	// Provenance is immutable: an edit form that doesn't know about the source
	// fields must not silently erase where the ticket came from.
	upd.SourceID, upd.SourceKind = cur.SourceID, cur.SourceKind
	upd.SourceKey, upd.SourceURL = cur.SourceKey, cur.SourceURL
	upd.SourceType, upd.AutomationID = cur.SourceType, cur.AutomationID
	upd.RemoteID, upd.RemoteUpdatedAt, upd.SyncedAt = cur.RemoteID, cur.RemoteUpdatedAt, cur.SyncedAt
	upd.SyncState, upd.SyncError = cur.SyncState, cur.SyncError
	upd.StatusChangedAt, upd.StatusChangedBy = cur.StatusChangedAt, cur.StatusChangedBy
	if upd.StatusID == "" {
		upd.StatusID = cur.StatusID
	}
	if upd.Labels == nil {
		upd.Labels = cur.Labels
	}
	if cur.SourceID != "" {
		// The tracker owns the type and so the change type; a form that
		// leaves them out changes nothing.
		if upd.TypeID == "" {
			upd.TypeID = cur.TypeID
		}
		if upd.ChangeType == "" {
			upd.ChangeType = cur.ChangeType
		}
		if err := remoteOwned(cur, upd); err != nil {
			return Ticket{}, err
		}
	} else if upd.TypeID == cur.TypeID && normalChange(upd.ChangeType) != cur.ChangeType {
		upd.TypeID = "" // the change type was edited: the type follows it
	}
	if err := s.validateTicket(ctx, &upd); err != nil {
		return Ticket{}, err
	}
	upd.UpdatedAt = s.now().UTC()
	if upd.StatusID != cur.StatusID {
		at := upd.UpdatedAt
		upd.StatusChangedAt, upd.StatusChangedBy = &at, ChangedByUser
	}
	if err := s.db.UpsertTicket(ctx, upd); err != nil {
		return Ticket{}, err
	}
	return upd, nil
}

// remoteOwned lists what an edit of a mirrored ticket changes that its
// tracker owns.
func remoteOwned(cur, upd Ticket) error {
	var fields []string
	if strings.TrimSpace(upd.Title) != cur.Title {
		fields = append(fields, "title")
	}
	if upd.Goal != cur.Goal {
		fields = append(fields, "goal")
	}
	if upd.StatusID != cur.StatusID {
		fields = append(fields, "status")
	}
	if upd.TypeID != "" && upd.TypeID != cur.TypeID {
		fields = append(fields, "type")
	}
	if cur.TypeID != "" && normalChange(upd.ChangeType) != cur.ChangeType {
		fields = append(fields, "change type")
	}
	if !slices.Equal(cleanLabels(upd.Labels), cur.Labels) {
		fields = append(fields, "labels")
	}
	if len(fields) > 0 {
		return &RemoteOwnedError{TicketID: cur.ID, Fields: fields}
	}
	return nil
}

func (s *Service) GetTicket(ctx context.Context, id string) (Ticket, error) {
	return s.db.GetTicket(ctx, id)
}

// ListTickets is one user's tickets ("" = everyone's, for the daemon's own
// passes).
func (s *Service) ListTickets(ctx context.Context, userID string) ([]Ticket, error) {
	return s.db.ListTickets(ctx, userID)
}

// TicketBySource is the user's ticket mirroring the issue key (compared
// without case) of a ticketsource integration — at most one, by a unique
// index; found=false when there is none.
func (s *Service) TicketBySource(ctx context.Context, userID, sourceID, key string) (Ticket, bool, error) {
	return found(s.db.GetTicketBySource(ctx, userID, sourceID, key))
}

// TicketByProjectKey is a project's ticket whose source key is key (compared
// without case): how a merge request's ticket, keyed by its URL with no
// source integration, is found again. The oldest when several share it.
func (s *Service) TicketByProjectKey(ctx context.Context, projectID, key string) (Ticket, bool, error) {
	return found(s.db.GetTicketByProjectKey(ctx, projectID, key))
}

func found(t Ticket, err error) (Ticket, bool, error) {
	if errors.Is(err, ErrNotFound) {
		return Ticket{}, false, nil
	}
	return t, err == nil, err
}

// ListTicketsBySource is every ticket mirroring an issue of the integration,
// whatever its status: a manual refresh of the integration, an unlink.
func (s *Service) ListTicketsBySource(ctx context.Context, sourceID string) ([]Ticket, error) {
	return s.db.ListTicketsBySource(ctx, sourceID, false)
}

// ListSyncable is what the periodic sync polls of an integration: tickets
// mirroring its issues that are not in a Done status and not gone (an issue
// not synced yet has no status, and is polled).
func (s *Service) ListSyncable(ctx context.Context, sourceID string) ([]Ticket, error) {
	return s.db.ListTicketsBySource(ctx, sourceID, true)
}

// DeleteTicket removes the ticket (its comments and links go with it). Runs
// that referenced it keep their own run_created snapshot (self-contained), so
// this never orphans history.
func (s *Service) DeleteTicket(ctx context.Context, id string) error {
	return s.db.DeleteTicket(ctx, id)
}

func (s *Service) validateTicket(ctx context.Context, t *Ticket) error {
	// A person's text is refused at the API; a tracker's isn't theirs to fix,
	// and one issue with a NUL would fail its automation on every pass.
	event.CleanValue(t)
	if strings.TrimSpace(t.Title) == "" {
		return invalidf("ticket: title is required")
	}
	if t.ProjectID == "" {
		return invalidf("ticket: project_id is required")
	}
	p, err := s.db.GetProject(ctx, t.ProjectID)
	if err != nil {
		return invalidf("ticket: project %s not found", t.ProjectID)
	}
	// A ticket belongs to whoever owns its project — whatever the caller
	// put on the row.
	t.UserID = p.UserID
	t.Title = strings.TrimSpace(t.Title)
	t.BaseBranch = strings.TrimSpace(t.BaseBranch)
	t.Labels = cleanLabels(t.Labels)
	if t.ChangeType == "" {
		t.ChangeType = trackerport.ChangeFeat
	} else if ct, ok := trackerport.ParseChangeType(string(t.ChangeType)); ok {
		t.ChangeType = ct
	} else {
		return invalidf("ticket: change type %q is not one of feat, fix, hotfix, perf, docs, chore", t.ChangeType)
	}
	return s.placeOnTracker(ctx, t)
}

// placeOnTracker checks that the ticket's status and type are its tracker's,
// derives the change type from the type, and puts a ticket with no source on
// the internal tracker's defaults: the todo step's status (else the first),
// the first type of its change type.
func (s *Service) placeOnTracker(ctx context.Context, t *Ticket) error {
	if t.SourceID != "" && t.StatusID == "" && t.TypeID == "" {
		return nil // not synced yet
	}
	trackerID, err := s.TicketTrackerID(ctx, *t)
	if err != nil {
		return err
	}
	statuses, err := s.db.ListTrackerStatuses(ctx, trackerID)
	if err != nil {
		return err
	}
	switch {
	case t.StatusID != "":
		if !slices.ContainsFunc(statuses, func(st TrackerStatus) bool { return st.ID == t.StatusID }) {
			return invalidf("ticket: status %s is not one of its tracker's", t.StatusID)
		}
	case t.SourceID == "" && len(statuses) > 0:
		t.StatusID = statuses[0].ID
		if i := slices.IndexFunc(statuses, func(st TrackerStatus) bool { return st.FlowStep == trackerport.StepTodo }); i >= 0 {
			t.StatusID = statuses[i].ID
		}
	}
	types, err := s.db.ListTrackerTypes(ctx, trackerID)
	if err != nil {
		return err
	}
	switch {
	case t.TypeID != "":
		i := slices.IndexFunc(types, func(ty TrackerType) bool { return ty.ID == t.TypeID })
		if i < 0 {
			return invalidf("ticket: type %s is not one of its tracker's", t.TypeID)
		}
		t.ChangeType = types[i].ChangeType
	case t.SourceID == "":
		if i := slices.IndexFunc(types, func(ty TrackerType) bool { return ty.ChangeType == t.ChangeType }); i >= 0 {
			t.TypeID = types[i].ID
		}
	}
	return nil
}

// normalChange is a change type as validateTicket stores it: "" is feat,
// any case is accepted; one that is no change type is left for it to refuse.
func normalChange(ct trackerport.ChangeType) trackerport.ChangeType {
	if ct == "" {
		return trackerport.ChangeFeat
	}
	if c, ok := trackerport.ParseChangeType(string(ct)); ok {
		return c
	}
	return ct
}

// cleanLabels trims labels and drops empty and repeated ones (case kept: a
// tracker's labels are case-sensitive); nil when none are left.
func cleanLabels(in []string) []string {
	var out []string
	for _, l := range in {
		if l = strings.TrimSpace(l); l != "" && !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out
}

// --- the internal tracker's writes ------------------------------------------

// internalTicket is a ticket the internal tracker may write: one with no
// remote source.
func (s *Service) internalTicket(ctx context.Context, id string) (Ticket, error) {
	t, err := s.db.GetTicket(ctx, id)
	if err != nil {
		return Ticket{}, err
	}
	if t.SourceID != "" {
		return Ticket{}, &RemoteOwnedError{TicketID: id, Fields: []string{"its tracker state"}}
	}
	return t, nil
}

// SetTicketStatus moves a ticket of the internal tracker to one of that
// tracker's statuses, stamping when and by whom (by: ChangedByUser, or
// "run:<id>/<stage>") — one SQL statement, so a run and a person moving it at
// once is last-write-wins, never a torn row. Already there is a no-op. A
// mirrored ticket's status is its tracker's (*RemoteOwnedError).
func (s *Service) SetTicketStatus(ctx context.Context, ticketID, statusID, by string) error {
	t, err := s.internalTicket(ctx, ticketID)
	if err != nil {
		return err
	}
	trackerID, err := s.TicketTrackerID(ctx, t)
	if err != nil {
		return err
	}
	statuses, err := s.db.ListTrackerStatuses(ctx, trackerID)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(statuses, func(st TrackerStatus) bool { return st.ID == statusID }) {
		return invalidf("ticket: status %s is not one of its tracker's", statusID)
	}
	return s.db.SetTicketStatus(ctx, ticketID, statusID, by, s.now().UTC())
}

// SetTicketLabels replaces a ticket's labels, or adds to them (union, in
// order of first appearance), in one SQL statement, and returns the result.
// Internal tracker only, like SetTicketStatus.
func (s *Service) SetTicketLabels(ctx context.Context, ticketID string, labels []string, union bool) ([]string, error) {
	if _, err := s.internalTicket(ctx, ticketID); err != nil {
		return nil, err
	}
	return s.db.SetTicketLabels(ctx, ticketID, cleanLabels(labels), union, s.now().UTC())
}

// TicketComments is the ticket's comments, oldest first.
func (s *Service) TicketComments(ctx context.Context, ticketID string) ([]TicketComment, error) {
	return s.db.ListTicketComments(ctx, ticketID)
}

// AddTicketComment records a comment typed in Apothecary (origin user) or
// posted by a run (origin apothecary, RunID and StageID set). A run's comment
// is keyed by them: posting it again is a no-op that reports posted=false and
// returns the comment already there. A missing ticket is ErrNotFound.
func (s *Service) AddTicketComment(ctx context.Context, c TicketComment) (TicketComment, bool, error) {
	event.CleanValue(&c)
	if strings.TrimSpace(c.Body) == "" {
		return TicketComment{}, false, invalidf("comment: a body is required")
	}
	switch c.Origin {
	case CommentUser:
		c.RunID, c.StageID = "", ""
	case CommentApothecary:
		if c.RunID == "" || c.StageID == "" {
			return TicketComment{}, false, invalidf("comment: a run's comment names its run and stage")
		}
	default:
		return TicketComment{}, false, invalidf("comment: origin %q is not user or apothecary", c.Origin)
	}
	c.ID, c.RemoteID = s.newID(), ""
	c.CreatedAt = s.now().UTC()
	c.UpdatedAt = c.CreatedAt
	posted, err := s.db.InsertTicketComment(ctx, c)
	if err != nil || posted {
		return c, posted, err
	}
	list, err := s.db.ListTicketComments(ctx, c.TicketID)
	if err != nil {
		return TicketComment{}, false, err
	}
	for _, x := range list {
		if x.RunID == c.RunID && x.StageID == c.StageID {
			return x, false, nil
		}
	}
	return TicketComment{}, false, fmt.Errorf("comment of %s/%s: neither posted nor found", c.RunID, c.StageID)
}

// ReconcileRemoteComments makes a mirrored ticket's remote comments the
// tracker's list: new ones inserted, edited ones updated, ones the tracker no
// longer has deleted — by the tracker's comment id. Comments of other origins
// are left alone.
func (s *Service) ReconcileRemoteComments(ctx context.Context, ticketID string, remote []trackerport.Comment) error {
	rows := make([]TicketComment, 0, len(remote))
	for _, c := range remote {
		if c.ID == "" {
			continue
		}
		tc := TicketComment{ID: s.newID(), TicketID: ticketID, RemoteID: c.ID, Author: c.Author, Body: c.Body,
			CreatedAt: c.Created.UTC(), UpdatedAt: c.Updated.UTC(), Origin: CommentRemote}
		if tc.UpdatedAt.IsZero() {
			tc.UpdatedAt = tc.CreatedAt
		}
		event.CleanValue(&tc)
		rows = append(rows, tc)
	}
	return s.db.ReconcileRemoteComments(ctx, ticketID, rows)
}

// AddTicketLink upserts a link on a ticket of the internal tracker, on URL.
func (s *Service) AddTicketLink(ctx context.Context, ticketID string, l trackerport.TicketLink) error {
	if _, err := s.internalTicket(ctx, ticketID); err != nil {
		return err
	}
	if strings.TrimSpace(l.URL) == "" {
		return invalidf("link: a URL is required")
	}
	return s.db.UpsertTicketLink(ctx, TicketLink{TicketID: ticketID, URL: strings.TrimSpace(l.URL), Title: l.Title})
}

// TicketLinks is the ticket's links, by URL.
func (s *Service) TicketLinks(ctx context.Context, ticketID string) ([]TicketLink, error) {
	return s.db.ListTicketLinks(ctx, ticketID)
}

// --- the mirror --------------------------------------------------------------

// TicketMirror is what a sync writes onto a mirrored ticket: the tracker's
// snapshot of the issue, already resolved against the integration's lists.
type TicketMirror struct {
	Title, Goal          string
	SourceKey, SourceURL string
	SourceType           string
	RemoteID             string
	StatusID, TypeID     string // "" keeps the mirror's (the tracker's is not listed)
	Labels               []string
	// RemoteUpdated is the snapshot's last-updated time (zero = unknown).
	RemoteUpdated time.Time
}

// MirrorTicket overwrites a mirrored ticket's tracker-owned fields with a
// snapshot, in one statement, and marks it synced. A snapshot older than the
// one held is dropped (applied=false): a lagging search index must not undo a
// fresher read. A status change is stamped "sync". The status and type must
// be the integration's; the change type follows the type.
func (s *Service) MirrorTicket(ctx context.Context, ticketID string, m TicketMirror) (applied bool, err error) {
	cur, err := s.db.GetTicket(ctx, ticketID)
	if err != nil {
		return false, err
	}
	if cur.SourceID == "" {
		return false, invalidf("ticket %s has no remote tracker to mirror", ticketID)
	}
	event.CleanValue(&m)
	m.Title = strings.TrimSpace(m.Title)
	if m.Title == "" {
		m.Title = cur.Title
	}
	if m.SourceKey == "" {
		m.SourceKey = cur.SourceKey
	}
	m.Labels = cleanLabels(m.Labels)
	t := cur
	if m.StatusID != "" {
		t.StatusID = m.StatusID
	}
	if m.TypeID != "" {
		t.TypeID = m.TypeID
	}
	if err := s.placeOnTracker(ctx, &t); err != nil {
		return false, err
	}
	return s.db.MirrorTicket(ctx, ticketID, m, t.ChangeType, s.now().UTC())
}

// SetTicketSyncState records a sync that could not mirror the ticket: error
// (msg says why; the next pass retries) or gone (the issue no longer exists,
// and the poll stops asking).
func (s *Service) SetTicketSyncState(ctx context.Context, ticketID, state, msg string) error {
	if state != SyncError && state != SyncGone && state != SyncOK {
		return invalidf("sync state %q is not ok, error or gone", state)
	}
	return s.db.SetTicketSyncState(ctx, ticketID, state, msg)
}
