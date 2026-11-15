package confstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"apothecary/internal/ports/trackerport"
)

// Every ticket has exactly one tracker, its source (Ticket.SourceID): a
// remote ticketsource integration whose issue it records, or the owner's
// built-in INTERNAL tracker, a ticketsource integration of kind "internal"
// that every user has exactly one of. A workshop reads its tickets from one
// of them (Project.TicketSourceID).
//
// A remote tracker's issues, statuses and comments are read live from it;
// what is stored of one of its issues is a record (what Apothecary last saw
// of it, plus its base branch), and its types are listed here
// (EnsureRemoteTypes, by the tracker's own ids, so a rename follows the id)
// only so the user can say which change type each one is. The internal
// tracker's lists are the user's to edit outright, seeded with the stock
// statuses and one type per change type:
//
//   - statuses (TrackerStatus): an internal ticket's status_id is one of
//     them; the user marks which are Done (the work is over). A flow's
//     `ticket_transition to:` names one, as the tracker spells it;
//   - types (TrackerType): a ticket's type_id is one of its tracker's, and the
//     type says the ticket's change type (what its branch is named after).

// KindInternal is the internal tracker's integration kind (port
// ticketsource). It is built in: created by InternalTracker, never through
// CreateIntegration, and never deleted.
const KindInternal = "internal"

// internalTrackerName is what the internal tracker is called in a person's
// integration list.
const internalTrackerName = "Apothecary tickets"

// TrackerStatus is one status of the internal tracker (the
// tracker_statuses table). A remote tracker has no rows: its statuses are
// read from it.
type TrackerStatus struct {
	ID            string `json:"id"`
	IntegrationID string `json:"integration_id"`
	// RemoteID is the row's own id (the internal tracker is its own remote).
	RemoteID string `json:"remote_id"`
	// Name is what Transition takes and the UI shows.
	Name     string `json:"name"`
	Position int    `json:"position"`
	// IsDone: the work is over. Any number of statuses may be.
	IsDone bool `json:"is_done"`
}

// TrackerType is one issue type of a ticketsource integration (the
// tracker_types table), mapped onto the change type its tickets are.
type TrackerType struct {
	ID            string                 `json:"id"`
	IntegrationID string                 `json:"integration_id"`
	RemoteID      string                 `json:"remote_id"` // Jira's issuetype id; "issue"; the row's id on the internal tracker
	Name          string                 `json:"name"`
	ChangeType    trackerport.ChangeType `json:"change_type"`
	Position      int                    `json:"position"`
}

// Comment origins: typed by a person in Apothecary, posted by a run's
// ticket_comment stage.
const (
	CommentUser       = "user"
	CommentApothecary = "apothecary"
)

// TicketComment is one comment stored on a ticket (ticket_comments). A run's
// is keyed by the run and the stage occurrence that posted it (RunID,
// StageID) — the idempotency key that makes a re-posted comment a no-op.
type TicketComment struct {
	ID        string    `json:"id"`
	TicketID  string    `json:"ticket_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body_md"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Origin    string    `json:"origin"`
	RunID     string    `json:"run_id,omitempty"`
	// StageID is the stage occurrence: "<phase>-<n>/<stage id>", so a stage a
	// watcher phase runs again posts again while a retried attempt does not.
	StageID string `json:"stage_id,omitempty"`
}

// TicketLink is one link on a ticket of the internal tracker (ticket_link /
// ticket_link_branch), upserted on URL.
type TicketLink struct {
	TicketID string `json:"ticket_id"`
	URL      string `json:"url"`
	Title    string `json:"title"`
}

// internalStatusSeed / internalTypeSeed are a new internal tracker's lists —
// the same as the migrations give the trackers they create. A seeded
// status's id is the tracker's id + "-" + its suffix, so two seeders write
// the same rows.
var internalStatusSeed = []struct {
	suffix string
	TrackerStatus
}{
	{"todo", TrackerStatus{Name: "To do"}},
	{"in_progress", TrackerStatus{Name: "In progress"}},
	{"in_review", TrackerStatus{Name: "In review"}},
	{"done", TrackerStatus{Name: "Done", IsDone: true}},
	{"blocked", TrackerStatus{Name: "Blocked"}},
}

var internalTypeSeed = []TrackerType{
	{Name: "Feature", ChangeType: trackerport.ChangeFeat},
	{Name: "Bug fix", ChangeType: trackerport.ChangeFix},
	{Name: "Hotfix", ChangeType: trackerport.ChangeHotfix},
	{Name: "Performance", ChangeType: trackerport.ChangePerf},
	{Name: "Docs", ChangeType: trackerport.ChangeDocs},
	{Name: "Chore", ChangeType: trackerport.ChangeChore},
}

// --- the internal tracker ----------------------------------------------------

// InternalTracker is the user's internal tracker, created (and seeded) the
// first time it is asked for: the migration made one for every user that
// existed, and this makes one for every user since. Safe to race — one
// tracker per user is a unique index, and a loser reads the winner's.
func (s *Service) InternalTracker(ctx context.Context, userID string) (Integration, error) {
	in, found, err := s.findInternal(ctx, userID)
	if err != nil {
		return Integration{}, err
	}
	if !found {
		if in, err = s.createInternal(ctx, userID); err != nil {
			return Integration{}, err
		}
	}
	if err := s.seedInternal(ctx, in.ID); err != nil {
		return Integration{}, err
	}
	return in, nil
}

func (s *Service) findInternal(ctx context.Context, userID string) (Integration, bool, error) {
	rows, err := s.db.ListIntegrations(ctx, userID)
	if err != nil {
		return Integration{}, false, err
	}
	for _, r := range rows {
		// ListIntegrations("") is everyone's: the owner must match exactly.
		if r.Kind == KindInternal && r.Port == PortTicketSource && r.UserID == userID {
			in, err := s.open(r)
			return in, err == nil, err
		}
	}
	return Integration{}, false, nil
}

func (s *Service) createInternal(ctx context.Context, userID string) (Integration, error) {
	now := s.now().UTC()
	// The second name is for an owner who already called another tracker
	// "Apothecary tickets".
	for _, name := range []string{internalTrackerName, internalTrackerName + " (built in)"} {
		in := Integration{ID: s.newID(), UserID: userID, Port: PortTicketSource, Kind: KindInternal, Name: name,
			Config: map[string]any{}, Secrets: map[string]string{}, CreatedAt: now, UpdatedAt: now}
		err := s.db.UpsertIntegration(ctx, StoredIntegration{ID: in.ID, UserID: userID, Port: in.Port, Kind: in.Kind,
			Name: in.Name, Config: in.Config, CreatedAt: now, UpdatedAt: now})
		if err == nil {
			return in, nil
		}
		if !errors.Is(err, ErrConflict) {
			return Integration{}, err
		}
		// Lost a race, or the name is taken: a tracker made meanwhile wins.
		if got, found, ferr := s.findInternal(ctx, userID); ferr != nil || found {
			return got, ferr
		}
	}
	return Integration{}, fmt.Errorf("user %q: the internal tracker cannot be created: %w", userID, ErrConflict)
}

// seedInternal gives an internal tracker with no lists the stock ones. Ids are
// derived from the tracker's, so two seeders write the same rows.
func (s *Service) seedInternal(ctx context.Context, integrationID string) error {
	sts, err := s.db.ListTrackerStatuses(ctx, integrationID)
	if err != nil {
		return err
	}
	if len(sts) == 0 {
		for i, seed := range internalStatusSeed {
			st := seed.TrackerStatus
			st.ID = integrationID + "-" + seed.suffix
			st.IntegrationID, st.RemoteID, st.Position = integrationID, st.ID, i
			if _, err := s.db.EnsureTrackerStatus(ctx, st); err != nil {
				return err
			}
		}
	}
	tys, err := s.db.ListTrackerTypes(ctx, integrationID)
	if err != nil {
		return err
	}
	if len(tys) == 0 {
		for i, ty := range internalTypeSeed {
			ty.ID = integrationID + "-" + string(ty.ChangeType)
			ty.IntegrationID, ty.RemoteID, ty.Position = integrationID, ty.ID, i
			if _, err := s.db.EnsureTrackerType(ctx, ty); err != nil {
				return err
			}
		}
	}
	return nil
}

// TicketTrackerID is the integration whose lists the ticket's status and type
// come from: its source.
func (s *Service) TicketTrackerID(_ context.Context, t Ticket) (string, error) {
	return t.SourceID, nil
}

// --- the lists ---------------------------------------------------------------

// TrackerStatuses is an integration's status list, in position order.
func (s *Service) TrackerStatuses(ctx context.Context, integrationID string) ([]TrackerStatus, error) {
	return s.db.ListTrackerStatuses(ctx, integrationID)
}

// TrackerTypes is an integration's type list, in position order.
func (s *Service) TrackerTypes(ctx context.Context, integrationID string) ([]TrackerType, error) {
	return s.db.ListTrackerTypes(ctx, integrationID)
}

// ticketSourceIntegration is a ticketsource integration, or a refusal saying
// what it is instead.
func (s *Service) ticketSourceIntegration(ctx context.Context, id string) (StoredIntegration, error) {
	in, err := s.db.GetIntegration(ctx, id)
	if err != nil {
		return StoredIntegration{}, err
	}
	if in.Port != PortTicketSource {
		return StoredIntegration{}, invalidf("integration %s is a %s integration; only a ticket tracker has statuses and types", in.Name, in.Port)
	}
	return in, nil
}

// PutTrackerStatuses saves the internal tracker's status list as edited (the
// Statuses table): a row with no id is new, a stored row missing from it is
// deleted, names are unique (case aside), and the order given is the order
// kept. Deleting a status tickets still use takes replaceWith[id], another
// status kept in the list, which they move to; without one it is refused
// (ErrReferenced). A remote tracker's statuses are read from it, never
// stored (ErrInvalid).
func (s *Service) PutTrackerStatuses(ctx context.Context, integrationID string, rows []TrackerStatus, replaceWith map[string]string) ([]TrackerStatus, error) {
	in, err := s.ticketSourceIntegration(ctx, integrationID)
	if err != nil {
		return nil, err
	}
	if in.Kind != KindInternal {
		return nil, invalidf("integration %s: statuses of a remote tracker are read from it", in.Name)
	}
	cur, err := s.db.ListTrackerStatuses(ctx, integrationID)
	if err != nil {
		return nil, err
	}
	out, err := s.internalStatuses(integrationID, cur, rows, replaceWith)
	if err != nil {
		return nil, err
	}
	if err := s.db.ReplaceTrackerStatuses(ctx, integrationID, out, replaceWith); err != nil {
		return nil, err
	}
	return s.db.ListTrackerStatuses(ctx, integrationID)
}

// internalStatuses validates the user's own list and stamps new rows.
func (s *Service) internalStatuses(integrationID string, cur, rows []TrackerStatus, replaceWith map[string]string) ([]TrackerStatus, error) {
	if len(rows) == 0 {
		return nil, invalidf("the internal tracker needs at least one status")
	}
	stored := map[string]TrackerStatus{}
	for _, c := range cur {
		stored[c.ID] = c
	}
	names := map[string]bool{}
	kept := map[string]bool{}
	out := make([]TrackerStatus, 0, len(rows))
	for i, r := range rows {
		r.Name = strings.TrimSpace(r.Name)
		if r.Name == "" {
			return nil, invalidf("status %d: a name is required", i+1)
		}
		if names[strings.ToLower(r.Name)] {
			return nil, invalidf("status %q is named twice", r.Name)
		}
		names[strings.ToLower(r.Name)] = true
		if r.ID == "" {
			r.ID = s.newID()
		} else if _, ok := stored[r.ID]; !ok {
			return nil, invalidf("status %s is not one of this tracker's", r.ID)
		}
		if kept[r.ID] {
			return nil, invalidf("status %s is listed twice", r.ID)
		}
		kept[r.ID] = true
		r.IntegrationID, r.RemoteID, r.Position = integrationID, r.ID, i
		out = append(out, r)
	}
	for old, repl := range replaceWith {
		if _, ok := stored[old]; !ok || kept[old] {
			return nil, invalidf("replace_with: status %s is not one being deleted", old)
		}
		if !kept[repl] {
			return nil, invalidf("replace_with: status %s must be a stored status kept in the list", repl)
		}
	}
	return out, nil
}

// PutTrackerTypes saves an integration's type list, like PutTrackerStatuses:
// a remote tracker's rows are the fetched ones and only their ChangeType is
// taken; the internal tracker's are the user's. Every type maps to a change
// type, and a ticket's change type follows its type's on save.
func (s *Service) PutTrackerTypes(ctx context.Context, integrationID string, rows []TrackerType, replaceWith map[string]string) ([]TrackerType, error) {
	in, err := s.ticketSourceIntegration(ctx, integrationID)
	if err != nil {
		return nil, err
	}
	cur, err := s.db.ListTrackerTypes(ctx, integrationID)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		ct, ok := trackerport.ParseChangeType(string(rows[i].ChangeType))
		if !ok {
			return nil, invalidf("type %q: change type %q is not one of feat, fix, hotfix, perf, docs, chore", rows[i].Name, rows[i].ChangeType)
		}
		rows[i].ChangeType = ct
	}
	var out []TrackerType
	if in.Kind == KindInternal {
		out, err = s.internalTypes(integrationID, cur, rows, replaceWith)
	} else {
		out, err = remoteTypes(cur, rows, replaceWith)
	}
	if err != nil {
		return nil, err
	}
	if err := s.db.ReplaceTrackerTypes(ctx, integrationID, out, replaceWith); err != nil {
		return nil, err
	}
	return s.db.ListTrackerTypes(ctx, integrationID)
}

func remoteTypes(cur, rows []TrackerType, replaceWith map[string]string) ([]TrackerType, error) {
	if len(replaceWith) > 0 {
		return nil, invalidf("a remote tracker's types come from the tracker: none can be deleted here")
	}
	byID := map[string]TrackerType{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if len(byID) != len(rows) || len(rows) != len(cur) {
		return nil, invalidf("a remote tracker's types come from the tracker: send every stored type once, and no other")
	}
	out := make([]TrackerType, 0, len(cur))
	for _, c := range cur {
		r, ok := byID[c.ID]
		if !ok {
			return nil, invalidf("a remote tracker's types come from the tracker: type %s is missing", c.ID)
		}
		c.ChangeType = r.ChangeType
		out = append(out, c)
	}
	return out, nil
}

func (s *Service) internalTypes(integrationID string, cur, rows []TrackerType, replaceWith map[string]string) ([]TrackerType, error) {
	stored := map[string]bool{}
	for _, c := range cur {
		stored[c.ID] = true
	}
	names := map[string]bool{}
	kept := map[string]bool{}
	out := make([]TrackerType, 0, len(rows))
	for i, r := range rows {
		r.Name = strings.TrimSpace(r.Name)
		if r.Name == "" {
			return nil, invalidf("type %d: a name is required", i+1)
		}
		if names[strings.ToLower(r.Name)] {
			return nil, invalidf("type %q is named twice", r.Name)
		}
		names[strings.ToLower(r.Name)] = true
		if r.ID == "" {
			r.ID = s.newID()
		} else if !stored[r.ID] {
			return nil, invalidf("type %s is not one of this tracker's", r.ID)
		}
		if kept[r.ID] {
			return nil, invalidf("type %s is listed twice", r.ID)
		}
		kept[r.ID] = true
		r.IntegrationID, r.RemoteID, r.Position = integrationID, r.ID, i
		out = append(out, r)
	}
	for old, repl := range replaceWith {
		if !stored[old] || kept[old] {
			return nil, invalidf("replace_with: type %s is not one being deleted", old)
		}
		if !kept[repl] {
			return nil, invalidf("replace_with: type %s must be a stored type kept in the list", repl)
		}
	}
	return out, nil
}

// TrackerListNotices hears types a tracker added to an integration's existing list (notices.Service); nil = silent.
type TrackerListNotices interface {
	TrackerListsGrew(ctx context.Context, userID, integrationID, name string, added []string)
}

// EnsureRemoteTypes records types a remote tracker reported and returns the
// integration's whole list. A type already listed (by remote id) takes the
// tracker's current name, so a rename follows the id; a new one's change
// type is GuessChangeType's reading of its name.
func (s *Service) EnsureRemoteTypes(ctx context.Context, integrationID string, remote []trackerport.RemoteType) ([]TrackerType, error) {
	cur, err := s.db.ListTrackerTypes(ctx, integrationID)
	if err != nil {
		return nil, err
	}
	next := len(cur)
	var added []string
	for _, r := range remote {
		if strings.TrimSpace(r.ID) == "" {
			continue
		}
		ty := TrackerType{ID: s.newID(), IntegrationID: integrationID, RemoteID: r.ID, Name: r.Name,
			ChangeType: trackerport.GuessChangeType(r.Name), Position: next}
		got, err := s.db.EnsureTrackerType(ctx, ty)
		if err != nil {
			return nil, err
		}
		if got.ID == ty.ID {
			added = append(added, fmt.Sprintf("type %q: change type %s (guessed)", got.Name, got.ChangeType))
		}
		next++
	}
	s.listsGrew(ctx, integrationID, added)
	return s.db.ListTrackerTypes(ctx, integrationID)
}

// listsGrew tells Notices the rows a tracker added to an integration's lists.
// The lists are already written, so an integration that cannot be read now
// only loses the notice.
func (s *Service) listsGrew(ctx context.Context, integrationID string, added []string) {
	if len(added) == 0 || s.Notices == nil {
		return
	}
	in, err := s.db.GetIntegration(ctx, integrationID)
	if err != nil {
		return
	}
	s.Notices.TrackerListsGrew(ctx, in.UserID, integrationID, in.Name, added)
}
