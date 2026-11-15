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
// attempt): runs reference a ticket by id and derive their own branch from
// it. Flow is deliberately NOT here — it's a per-run choice made when a run
// is started.
//
// Every ticket has one tracker, its source (SourceID, tracker.go). On the
// owner's internal tracker the ticket is the issue itself, written in place.
// On a remote tracker it is a RECORD of the issue: what Apothecary last saw
// of it (title, goal, key, type, labels — read live and written back by
// SeeTicket/RecordTicket, never edited here) plus what is Apothecary's own,
// the base branch. A remote issue's status and comments are read live from
// its tracker and never stored.
type Ticket struct {
	ID string `json:"id"`
	// UserID is the owner: the workshop owner's for an internal ticket, the
	// integration owner's for a record.
	UserID     string `json:"user_id"`
	Title      string `json:"title"`
	Goal       string `json:"goal"`
	BaseBranch string `json:"base_branch"` // "" = each run renders its workshop's repo.branch_format
	// ChangeType is what the ticket is (trackerport.ChangeType: feat, fix, …; ""
	// on a write means feat). Derived from the ticket's type when it has one;
	// the base branch is named after it, and changing it later renames
	// nothing.
	ChangeType trackerport.ChangeType `json:"change_type"`
	// StatusID points into the internal tracker's statuses (internal tickets
	// only: a record has none, its status is read live). TypeID points into
	// the ticket's tracker's types.
	StatusID string `json:"status_id,omitempty"`
	TypeID   string `json:"type_id,omitempty"`
	// Labels are the tracker's labels, as it spells them.
	Labels []string `json:"labels,omitempty"`
	// SourceID is the ticketsource integration the ticket is from — always
	// set, the internal tracker included. SourceKind is its kind ("internal",
	// "jira", …), stored next to the id so the provenance reads without the
	// integration row.
	SourceID   string `json:"source_id"`
	SourceKind string `json:"source_kind"`
	// SourceKey, SourceURL and SourceType are a record's last-seen key
	// ("PROJ-123"), URL and the tracker's word for its type ("Bug"); "" on an
	// internal ticket.
	SourceKey  string `json:"source_key,omitempty"`
	SourceURL  string `json:"source_url,omitempty"`
	SourceType string `json:"source_type,omitempty"`
	// RemoteID is the issue's stable id on its tracker (a Jira key changes
	// when the issue moves project; this does not); an internal ticket's is
	// its own id. (SourceID, RemoteID) is unique.
	RemoteID string `json:"remote_id"`
	// StatusChangedAt/By say when and by whom an internal ticket's status
	// last changed: run:<run id>/<stage>, or user.
	StatusChangedAt *time.Time `json:"status_changed_at,omitempty"`
	StatusChangedBy string     `json:"status_changed_by,omitempty"`
	// AutomationID names the automation that recorded the ticket, when one
	// did: provenance that lets a later poll tell its own record still
	// waiting for a run from a person's ticket.
	AutomationID string    `json:"automation_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ChangedByUser is status_changed_by for a person's move (a run's is
// "run:<id>/<stage>").
const ChangedByUser = "user"

// Internal reports whether the ticket is its owner's internal tracker's.
func (t Ticket) Internal() bool { return t.SourceKind == KindInternal }

// TrackerKey is the ticket's key on its tracker: the issue's last-seen key
// for a record, the ticket's own id on the internal tracker.
func (t Ticket) TrackerKey() string {
	if t.Internal() {
		return t.ID
	}
	return t.SourceKey
}

// TicketSeen is what Apothecary last saw of a remote issue.
type TicketSeen struct {
	RemoteID, Key, URL, Title, Goal string
	// Type is the issue type's name, TypeRemoteID the tracker's id for it.
	Type, TypeRemoteID string
	Labels             []string
}

// TicketCursor is a keyset position in an internal tracker's list (newest
// first).
type TicketCursor struct {
	CreatedAt time.Time
	ID        string
}

// InternalTicketQuery narrows an internal tracker's list.
type InternalTicketQuery struct {
	// Text is a case-insensitive substring of the title, goal or id ("" =
	// all).
	Text string
	// Done: DoneOpen is a ticket with no status or one not Done, DoneOnly one
	// in a Done status, DoneAny both.
	Done trackerport.DoneFilter
	// After keeps the rows strictly after it in (created_at DESC, id DESC)
	// order.
	After *TicketCursor
	Limit int // <= 0 = 50
}

// CreateTicket validates and inserts a ticket of the owner's (t.UserID; ""
// is the ownerless scope, as for a project) internal tracker. Branch-format
// validation is the caller's job (the httpapi layer runs
// checkout.CheckBranch, which confstore cannot import). The ticket lands in
// the first status unless told otherwise, and the first type of its change
// type unless told otherwise.
func (s *Service) CreateTicket(ctx context.Context, t Ticket) (Ticket, error) {
	in, err := s.InternalTracker(ctx, t.UserID)
	if err != nil {
		return Ticket{}, err
	}
	t.ID = s.newID()
	t.SourceID, t.SourceKind, t.RemoteID = in.ID, KindInternal, t.ID
	t.SourceKey, t.SourceURL, t.SourceType = "", "", ""
	if err := s.validateTicket(ctx, &t); err != nil {
		return Ticket{}, err
	}
	t.CreatedAt = s.now().UTC()
	t.UpdatedAt = t.CreatedAt
	t.StatusChangedAt, t.StatusChangedBy = nil, ""
	if t.StatusID != "" {
		at := t.CreatedAt
		t.StatusChangedAt = &at
	}
	if err := s.db.UpsertTicket(ctx, t); err != nil {
		return Ticket{}, err
	}
	return t, nil
}

// UpdateTicket edits a ticket. An internal ticket keeps its id, owner,
// created_at and provenance, and is re-validated: a status or type left
// empty, and labels left nil, keep the stored ones (an edit form may not know
// them); on a ticket with no type, or one whose change type changed while its
// type did not, the type follows the change type. A remote record changes
// only its BaseBranch: everything else is its tracker's.
func (s *Service) UpdateTicket(ctx context.Context, id string, upd Ticket) (Ticket, error) {
	cur, err := s.db.GetTicket(ctx, id)
	if err != nil {
		return Ticket{}, err
	}
	if !cur.Internal() {
		cur.BaseBranch = strings.TrimSpace(upd.BaseBranch)
		event.CleanValue(&cur.BaseBranch)
		cur.UpdatedAt = s.now().UTC()
		if err := s.db.UpsertTicket(ctx, cur); err != nil {
			return Ticket{}, err
		}
		return cur, nil
	}
	upd.ID, upd.UserID, upd.CreatedAt = cur.ID, cur.UserID, cur.CreatedAt
	// Provenance is immutable: an edit form that doesn't know about the source
	// fields must not silently erase where the ticket came from.
	upd.SourceID, upd.SourceKind = cur.SourceID, cur.SourceKind
	upd.SourceKey, upd.SourceURL, upd.SourceType = cur.SourceKey, cur.SourceURL, cur.SourceType
	upd.RemoteID, upd.AutomationID = cur.RemoteID, cur.AutomationID
	upd.StatusChangedAt, upd.StatusChangedBy = cur.StatusChangedAt, cur.StatusChangedBy
	if upd.StatusID == "" {
		upd.StatusID = cur.StatusID
	}
	if upd.Labels == nil {
		upd.Labels = cur.Labels
	}
	if upd.TypeID == cur.TypeID && normalChange(upd.ChangeType) != cur.ChangeType {
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

func (s *Service) GetTicket(ctx context.Context, id string) (Ticket, error) {
	return s.db.GetTicket(ctx, id)
}

// TicketByRemote is the record of a source's issue, by the tracker's stable
// id; found=false when there is none.
func (s *Service) TicketByRemote(ctx context.Context, sourceID, remoteID string) (Ticket, bool, error) {
	t, err := s.db.GetTicketByRemote(ctx, sourceID, remoteID)
	if errors.Is(err, ErrNotFound) {
		return Ticket{}, false, nil
	}
	return t, err == nil, err
}

// TicketBySourceKey is the record of a source's issue by the key it was
// last seen under — what a run start falls back to when the tracker cannot
// say which issue a key is now; found=false when there is none.
func (s *Service) TicketBySourceKey(ctx context.Context, sourceID, key string) (Ticket, bool, error) {
	t, err := s.db.GetTicketBySourceKey(ctx, sourceID, key)
	if errors.Is(err, ErrNotFound) {
		return Ticket{}, false, nil
	}
	return t, err == nil, err
}

// TicketsByRemote is the records of those of a source's issues that have
// one, keyed by RemoteID.
func (s *Service) TicketsByRemote(ctx context.Context, sourceID string, remoteIDs []string) (map[string]Ticket, error) {
	list, err := s.db.ListTicketsByRemote(ctx, sourceID, remoteIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Ticket, len(list))
	for _, t := range list {
		out[t.RemoteID] = t
	}
	return out, nil
}

// InternalTickets is a page of an internal tracker's tickets, newest first.
func (s *Service) InternalTickets(ctx context.Context, sourceID string, q InternalTicketQuery) ([]Ticket, error) {
	q.Text = strings.TrimSpace(q.Text)
	return s.db.ListInternalTickets(ctx, sourceID, q)
}

// DeleteTicket removes the ticket (its comments and links go with it). Runs
// that referenced it keep their own run_created snapshot (self-contained), so
// this never orphans history.
func (s *Service) DeleteTicket(ctx context.Context, id string) error {
	return s.db.DeleteTicket(ctx, id)
}

// RecordTicket makes sure a remote issue has a record: an existing one (by
// sourceID and seen.RemoteID) takes what was seen and is returned
// (created=false; its automation stays); otherwise one is inserted, owned by
// the integration's owner and naming automationID (created=true). sourceID
// must be a remote ticketsource integration.
func (s *Service) RecordTicket(ctx context.Context, sourceID string, seen TicketSeen, automationID string) (Ticket, bool, error) {
	in, err := s.ticketSourceIntegration(ctx, sourceID)
	if err != nil {
		return Ticket{}, false, err
	}
	if in.Kind == KindInternal {
		return Ticket{}, false, invalidf("integration %s: the internal tracker's tickets are created, not recorded", in.Name)
	}
	cleanSeen(&seen)
	if seen.RemoteID == "" {
		return Ticket{}, false, invalidf("ticket: the issue's remote id is required")
	}
	if cur, err := s.db.GetTicketByRemote(ctx, sourceID, seen.RemoteID); err == nil {
		t, err := s.see(ctx, cur, seen)
		return t, false, err
	} else if !errors.Is(err, ErrNotFound) {
		return Ticket{}, false, err
	}
	typeID, ct, err := s.seenType(ctx, sourceID, seen)
	if err != nil {
		return Ticket{}, false, err
	}
	now := s.now().UTC()
	t := Ticket{ID: s.newID(), UserID: in.UserID, Title: seenTitle(seen), Goal: seen.Goal, ChangeType: ct,
		TypeID: typeID, Labels: seen.Labels, SourceID: sourceID, SourceKind: in.Kind, SourceKey: seen.Key,
		SourceURL: seen.URL, SourceType: seen.Type, RemoteID: seen.RemoteID, AutomationID: automationID,
		CreatedAt: now, UpdatedAt: now}
	err = s.db.UpsertTicket(ctx, t)
	if errors.Is(err, ErrConflict) {
		// Recorded meanwhile: that record is the one.
		cur, gerr := s.db.GetTicketByRemote(ctx, sourceID, seen.RemoteID)
		if gerr != nil {
			return Ticket{}, false, err
		}
		t, err := s.see(ctx, cur, seen)
		return t, false, err
	}
	if err != nil {
		return Ticket{}, false, err
	}
	stored, err := s.db.GetTicket(ctx, t.ID)
	return stored, err == nil, err
}

// SeeTicket writes what was last seen of a record's issue onto it (a moved
// key follows) and returns the updated record. An internal ticket is
// ErrInvalid.
func (s *Service) SeeTicket(ctx context.Context, ticketID string, seen TicketSeen) (Ticket, error) {
	cur, err := s.db.GetTicket(ctx, ticketID)
	if err != nil {
		return Ticket{}, err
	}
	if cur.Internal() {
		return Ticket{}, invalidf("ticket %s is the internal tracker's: it is written in place", ticketID)
	}
	cleanSeen(&seen)
	return s.see(ctx, cur, seen)
}

func (s *Service) see(ctx context.Context, cur Ticket, seen TicketSeen) (Ticket, error) {
	typeID, ct, err := s.seenType(ctx, cur.SourceID, seen)
	if err != nil {
		return Ticket{}, err
	}
	seen.Title = seenTitle(seen)
	if err := s.db.SetTicketSeen(ctx, cur.ID, seen, typeID, ct, s.now().UTC()); err != nil {
		return Ticket{}, err
	}
	return s.db.GetTicket(ctx, cur.ID)
}

// seenType is the record's type: the integration's type of the tracker's id,
// listed now when it is new (its change type guessed from its name); none
// (feat) when the issue has no type.
func (s *Service) seenType(ctx context.Context, sourceID string, seen TicketSeen) (string, trackerport.ChangeType, error) {
	if seen.TypeRemoteID == "" {
		return "", trackerport.ChangeFeat, nil
	}
	types, err := s.db.ListTrackerTypes(ctx, sourceID)
	if err != nil {
		return "", "", err
	}
	if i := slices.IndexFunc(types, func(ty TrackerType) bool { return ty.RemoteID == seen.TypeRemoteID }); i >= 0 {
		return types[i].ID, types[i].ChangeType, nil
	}
	name := seen.Type
	if name == "" {
		name = seen.TypeRemoteID
	}
	want := TrackerType{ID: s.newID(), IntegrationID: sourceID, RemoteID: seen.TypeRemoteID,
		Name: name, ChangeType: trackerport.GuessChangeType(name), Position: len(types)}
	ty, err := s.db.EnsureTrackerType(ctx, want)
	if err != nil {
		return "", "", err
	}
	if ty.ID == want.ID { // the store keeps a listed row's id: this one is new
		s.listsGrew(ctx, sourceID, []string{fmt.Sprintf("type %q: change type %s (guessed)", ty.Name, ty.ChangeType)})
	}
	return ty.ID, ty.ChangeType, nil
}

// cleanSeen makes a tracker's text storable: a NUL in one issue must not fail
// every read of it.
func cleanSeen(seen *TicketSeen) {
	event.CleanValue(seen)
	seen.RemoteID = strings.TrimSpace(seen.RemoteID)
	seen.Key = strings.TrimSpace(seen.Key)
	seen.Title = strings.TrimSpace(seen.Title)
	seen.TypeRemoteID = strings.TrimSpace(seen.TypeRemoteID)
	seen.Labels = cleanLabels(seen.Labels)
}

// seenTitle is a record's title: the issue's, else its key (a tracker may
// have none to give).
func seenTitle(seen TicketSeen) string {
	if seen.Title != "" {
		return seen.Title
	}
	if seen.Key != "" {
		return seen.Key
	}
	return seen.RemoteID
}

// validateTicket checks an internal ticket's fields and places it on its
// tracker.
func (s *Service) validateTicket(ctx context.Context, t *Ticket) error {
	// A person's text is refused at the API; a run's isn't theirs to fix.
	event.CleanValue(t)
	if strings.TrimSpace(t.Title) == "" {
		return invalidf("ticket: title is required")
	}
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

// placeOnTracker checks that an internal ticket's status and type are its
// tracker's, derives the change type from the type, and fills in the
// defaults: the first status by position, the first type of its change type.
func (s *Service) placeOnTracker(ctx context.Context, t *Ticket) error {
	statuses, err := s.db.ListTrackerStatuses(ctx, t.SourceID)
	if err != nil {
		return err
	}
	switch {
	case t.StatusID != "":
		if !slices.ContainsFunc(statuses, func(st TrackerStatus) bool { return st.ID == t.StatusID }) {
			return invalidf("ticket: status %s is not one of its tracker's", t.StatusID)
		}
	case len(statuses) > 0:
		t.StatusID = statuses[0].ID
	}
	types, err := s.db.ListTrackerTypes(ctx, t.SourceID)
	if err != nil {
		return err
	}
	if t.TypeID != "" {
		i := slices.IndexFunc(types, func(ty TrackerType) bool { return ty.ID == t.TypeID })
		if i < 0 {
			return invalidf("ticket: type %s is not one of its tracker's", t.TypeID)
		}
		t.ChangeType = types[i].ChangeType
	} else if i := slices.IndexFunc(types, func(ty TrackerType) bool { return ty.ChangeType == t.ChangeType }); i >= 0 {
		t.TypeID = types[i].ID
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

// internalTicket is a ticket the internal tracker may write: one of its own.
// A remote record's state is its tracker's (ErrInvalid).
func (s *Service) internalTicket(ctx context.Context, id string) (Ticket, error) {
	t, err := s.db.GetTicket(ctx, id)
	if err != nil {
		return Ticket{}, err
	}
	if !t.Internal() {
		return Ticket{}, invalidf("ticket %s is a %s issue: change it on its tracker", id, t.SourceKind)
	}
	return t, nil
}

// SetTicketStatus moves an internal ticket to one of its tracker's statuses,
// stamping when and by whom (by: ChangedByUser, or "run:<id>/<stage>") — one
// SQL statement, so a run and a person moving it at once is last-write-wins,
// never a torn row. Already there is a no-op.
func (s *Service) SetTicketStatus(ctx context.Context, ticketID, statusID, by string) error {
	t, err := s.internalTicket(ctx, ticketID)
	if err != nil {
		return err
	}
	statuses, err := s.db.ListTrackerStatuses(ctx, t.SourceID)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(statuses, func(st TrackerStatus) bool { return st.ID == statusID }) {
		return invalidf("ticket: status %s is not one of its tracker's", statusID)
	}
	return s.db.SetTicketStatus(ctx, ticketID, statusID, by, s.now().UTC())
}

// SetTicketLabels replaces an internal ticket's labels, or adds to them
// (union, in order of first appearance), in one SQL statement, and returns
// the result.
func (s *Service) SetTicketLabels(ctx context.Context, ticketID string, labels []string, union bool) ([]string, error) {
	if _, err := s.internalTicket(ctx, ticketID); err != nil {
		return nil, err
	}
	return s.db.SetTicketLabels(ctx, ticketID, cleanLabels(labels), union, s.now().UTC())
}

// TicketComments is an internal ticket's stored comments, oldest first. A
// remote record has none here: its issue's comments are its tracker's
// (ErrInvalid).
func (s *Service) TicketComments(ctx context.Context, ticketID string) ([]TicketComment, error) {
	if _, err := s.internalTicket(ctx, ticketID); err != nil {
		return nil, err
	}
	return s.db.ListTicketComments(ctx, ticketID)
}

// AddTicketComment records a comment on an internal ticket, typed in
// Apothecary (origin user) or posted by a run (origin apothecary, RunID and
// StageID set). A run's comment is keyed by them: posting it again is a no-op
// that reports posted=false and returns the comment already there. A missing
// ticket is ErrNotFound; a remote record ErrInvalid (comment on its tracker).
func (s *Service) AddTicketComment(ctx context.Context, c TicketComment) (TicketComment, bool, error) {
	event.CleanValue(&c)
	if strings.TrimSpace(c.Body) == "" {
		return TicketComment{}, false, invalidf("comment: a body is required")
	}
	if _, err := s.internalTicket(ctx, c.TicketID); err != nil {
		return TicketComment{}, false, err
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
	c.ID = s.newID()
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

// AddTicketLink upserts a link on an internal ticket, on URL.
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
