package confstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"apothecary/internal/ports/trackerport"
)

// Every ticket has exactly one tracker (docs/ticket-sync-plan.md): the
// ticketsource integration it was imported from (Ticket.SourceID), or — for a
// ticket with no source, hand-written or made for a merge request — the
// owner's built-in INTERNAL tracker, a ticketsource integration of kind
// "internal" that every user has exactly one of. Each tracker carries two
// lists the ticket's state points into:
//
//   - statuses (TrackerStatus): a ticket's status_id is one of its tracker's.
//     The user marks which are Done (the work is over: the sync skips tickets
//     in them) and which status holds each flow step (what
//     `ticket_transition to: <step>` moves to, at most one per step);
//   - types (TrackerType): a ticket's type_id is one of its tracker's, and the
//     type says the ticket's change type (what its branch is named after).
//
// A remote tracker's lists are FETCHED from it (EnsureRemoteStatuses /
// EnsureRemoteTypes, by the tracker's own ids, so a rename follows the id)
// and only their Done / flow step / change type columns are the user's. The
// internal tracker's lists are the user's to edit outright, seeded with the
// stock statuses and one type per change type.

// KindInternal is the internal tracker's integration kind (port
// ticketsource). It is built in: created by InternalTracker, never through
// CreateIntegration, and never deleted.
const KindInternal = "internal"

// internalTrackerName is what the internal tracker is called in a person's
// integration list.
const internalTrackerName = "Apothecary tickets"

// TrackerStatus is one status of a ticketsource integration (the
// tracker_statuses table).
type TrackerStatus struct {
	ID            string `json:"id"`
	IntegrationID string `json:"integration_id"`
	// RemoteID is the tracker's id for the status (Jira's status id; GitHub
	// "open"/"closed"; GitLab "opened"/"closed"), what a fetched ticket's
	// StatusID is matched on. On the internal tracker it is the row's own id.
	RemoteID string `json:"remote_id"`
	// Name is the tracker's word for it: what Transition takes and the UI
	// shows.
	Name string `json:"name"`
	// RemoteCategory is the tracker's own classification (Jira's
	// statusCategory), what auto-suggest reads; "" when it never said.
	RemoteCategory trackerport.StatusCategory `json:"remote_category"`
	Position       int                        `json:"position"`
	// IsDone: the work is over. Any number of statuses may be; the sync's
	// poll skips their tickets.
	IsDone bool `json:"is_done"`
	// FlowStep is the step this status holds ("" = none), unique per
	// integration.
	FlowStep trackerport.FlowStep `json:"flow_step,omitempty"`
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

// Comment origins: mirrored from the remote tracker, typed by a person in
// Apothecary, posted by a run's ticket_comment stage.
const (
	CommentRemote     = "remote"
	CommentUser       = "user"
	CommentApothecary = "apothecary"
)

// TicketComment is one comment on a ticket (ticket_comments). A remote
// comment is keyed by the tracker's id (RemoteID); a run's by the run and the
// stage occurrence that posted it (RunID, StageID) — the internal tracker's
// idempotency key, which makes a re-posted comment a no-op.
type TicketComment struct {
	ID        string    `json:"id"`
	TicketID  string    `json:"ticket_id"`
	RemoteID  string    `json:"remote_id,omitempty"`
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
// the same as migration 0021 gives the trackers it creates.
var internalStatusSeed = []TrackerStatus{
	{Name: "To do", RemoteCategory: trackerport.CategoryNew, FlowStep: trackerport.StepTodo},
	{Name: "In progress", RemoteCategory: trackerport.CategoryIndeterminate, FlowStep: trackerport.StepInProgress},
	{Name: "In review", RemoteCategory: trackerport.CategoryIndeterminate, FlowStep: trackerport.StepInReview},
	{Name: "Done", RemoteCategory: trackerport.CategoryDone, FlowStep: trackerport.StepDone, IsDone: true},
	{Name: "Blocked", RemoteCategory: trackerport.CategoryIndeterminate, FlowStep: trackerport.StepBlocked},
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
		for i, st := range internalStatusSeed {
			st.ID = integrationID + "-" + string(st.FlowStep)
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
// come from: its source, else its owner's internal tracker.
func (s *Service) TicketTrackerID(ctx context.Context, t Ticket) (string, error) {
	if t.SourceID != "" {
		return t.SourceID, nil
	}
	in, err := s.InternalTracker(ctx, t.UserID)
	if err != nil {
		return "", err
	}
	return in.ID, nil
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

// PutTrackerStatuses saves an integration's status list as edited (the
// Statuses table) and says what is worth a warning without refusing it: the
// done step held by a status not marked Done.
//
// On a remote tracker the rows are the fetched ones — the list must name
// exactly the stored ids, and only IsDone and FlowStep are taken. On the
// internal tracker the list is the user's: a row with no id is new, a stored
// row missing from it is deleted, names are unique (case aside), and the
// order given is the order kept. Each flow step is held at most once.
// Deleting a status tickets still use takes replaceWith[id], another status
// kept in the list, which they move to; without one it is refused
// (ErrReferenced).
func (s *Service) PutTrackerStatuses(ctx context.Context, integrationID string, rows []TrackerStatus, replaceWith map[string]string) ([]TrackerStatus, []string, error) {
	in, err := s.ticketSourceIntegration(ctx, integrationID)
	if err != nil {
		return nil, nil, err
	}
	cur, err := s.db.ListTrackerStatuses(ctx, integrationID)
	if err != nil {
		return nil, nil, err
	}
	steps := map[trackerport.FlowStep]string{}
	for i := range rows {
		r := &rows[i]
		if r.FlowStep != "" {
			step, ok := trackerport.ParseFlowStep(string(r.FlowStep))
			if !ok {
				return nil, nil, invalidf("status %q: flow step %q is not one of %s", r.Name, r.FlowStep, trackerport.StepNames(trackerport.FlowSteps()))
			}
			if other, taken := steps[step]; taken {
				return nil, nil, invalidf("flow step %s is held by both %q and %q; each step may be held by one status", step, other, r.Name)
			}
			r.FlowStep = step
			steps[step] = r.Name
		}
	}
	var out []TrackerStatus
	if in.Kind == KindInternal {
		out, err = s.internalStatuses(integrationID, cur, rows, replaceWith)
	} else {
		out, err = remoteStatuses(cur, rows, replaceWith)
	}
	if err != nil {
		return nil, nil, err
	}
	if err := s.db.ReplaceTrackerStatuses(ctx, integrationID, out, replaceWith); err != nil {
		return nil, nil, err
	}
	var warnings []string
	for _, r := range out {
		if r.FlowStep == trackerport.StepDone && !r.IsDone {
			warnings = append(warnings, fmt.Sprintf("status %q holds the done step but is not marked Done: the sync keeps polling its tickets", r.Name))
		}
	}
	saved, err := s.db.ListTrackerStatuses(ctx, integrationID)
	return saved, warnings, err
}

// remoteStatuses applies the user's columns to the fetched rows.
func remoteStatuses(cur, rows []TrackerStatus, replaceWith map[string]string) ([]TrackerStatus, error) {
	if len(replaceWith) > 0 {
		return nil, invalidf("a remote tracker's statuses come from the tracker: none can be deleted here")
	}
	byID := map[string]TrackerStatus{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if len(byID) != len(rows) || len(rows) != len(cur) {
		return nil, invalidf("a remote tracker's statuses come from the tracker: send every stored status once, and no other")
	}
	out := make([]TrackerStatus, 0, len(cur))
	for _, c := range cur {
		r, ok := byID[c.ID]
		if !ok {
			return nil, invalidf("a remote tracker's statuses come from the tracker: status %s is missing", c.ID)
		}
		c.IsDone, c.FlowStep = r.IsDone, r.FlowStep
		out = append(out, c)
	}
	return out, nil
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
		} else if old, ok := stored[r.ID]; !ok {
			return nil, invalidf("status %s is not one of this tracker's", r.ID)
		} else {
			r.RemoteCategory = old.RemoteCategory
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

// TrackerListNotices hears statuses and types a tracker added to an integration's existing lists (notices.Service); nil = silent.
type TrackerListNotices interface {
	TrackerListsGrew(ctx context.Context, userID, integrationID, name string, added []string)
}

// EnsureRemoteStatuses records statuses a remote tracker reported — from its
// metadata, or the one a fetched ticket is in — and returns the integration's
// whole list. A status already listed (by remote id) takes the tracker's
// current name and category, so a rename follows the id, and keeps the
// user's columns. A new one is auto-suggested (plan Decision 4): category
// done ⇒ Done, and the done step; category new ⇒ the todo step — each step
// only while no other status holds it.
func (s *Service) EnsureRemoteStatuses(ctx context.Context, integrationID string, remote []trackerport.RemoteStatus) ([]TrackerStatus, error) {
	cur, err := s.db.ListTrackerStatuses(ctx, integrationID)
	if err != nil {
		return nil, err
	}
	next := len(cur)
	var added []string
	for _, r := range remote {
		if strings.TrimSpace(r.ID) == "" {
			continue
		}
		st := TrackerStatus{ID: s.newID(), IntegrationID: integrationID, RemoteID: r.ID, Name: r.Name,
			RemoteCategory: r.Category, Position: next}
		switch r.Category {
		case trackerport.CategoryDone:
			st.IsDone, st.FlowStep = true, trackerport.StepDone
		case trackerport.CategoryNew:
			st.FlowStep = trackerport.StepTodo
		}
		got, err := s.db.EnsureTrackerStatus(ctx, st)
		if err != nil {
			return nil, err
		}
		if got.ID == st.ID { // the store keeps a listed row's id: this one is new
			line := fmt.Sprintf("status %q: no flow step", got.Name)
			if got.FlowStep != "" {
				line = fmt.Sprintf("status %q: flow step %s", got.Name, got.FlowStep)
			}
			if got.IsDone {
				line += ", Done"
			}
			added = append(added, line)
		}
		next++
	}
	s.listsGrew(ctx, integrationID, added)
	return s.db.ListTrackerStatuses(ctx, integrationID)
}

// EnsureRemoteTypes is EnsureRemoteStatuses for types: a new one's change
// type is GuessChangeType's reading of its name, a listed one is renamed.
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

// StepStatus is the status of the ticket's tracker that holds a flow step —
// what `ticket_transition to: <step>` moves the ticket to — or
// *trackerport.ErrStatusUnsupported naming the steps that do have one (the
// engine skips the stage). Key in the error is the ticket's key on its
// tracker.
func (s *Service) StepStatus(ctx context.Context, ticketID string, step trackerport.FlowStep) (TrackerStatus, error) {
	t, err := s.db.GetTicket(ctx, ticketID)
	if err != nil {
		return TrackerStatus{}, err
	}
	trackerID, err := s.TicketTrackerID(ctx, t)
	if err != nil {
		return TrackerStatus{}, err
	}
	list, err := s.db.ListTrackerStatuses(ctx, trackerID)
	if err != nil {
		return TrackerStatus{}, err
	}
	var supported []trackerport.FlowStep
	for _, st := range list {
		if st.FlowStep == step {
			return st, nil
		}
		if st.FlowStep != "" {
			supported = append(supported, st.FlowStep)
		}
	}
	slices.SortFunc(supported, func(a, b trackerport.FlowStep) int {
		return slices.Index(trackerport.FlowSteps(), a) - slices.Index(trackerport.FlowSteps(), b)
	})
	return TrackerStatus{}, &trackerport.ErrStatusUnsupported{Key: t.TrackerKey(), Step: step, Supported: supported}
}
