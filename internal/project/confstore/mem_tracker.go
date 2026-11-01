package confstore

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"apothecary/internal/ports/trackerport"
)

// Mem's trackers: the status and type lists, a ticket's tracker state,
// comments and links — each held to what the SQL in pgstore does.

// dropOrphans is ON DELETE CASCADE from tickets: their comments and links go.
// Callers hold m.mu.
func (m *Mem) dropOrphans() {
	maps.DeleteFunc(m.comments, func(_ string, c TicketComment) bool { _, ok := m.tickets[c.TicketID]; return !ok })
	maps.DeleteFunc(m.links, func(_ string, l TicketLink) bool { _, ok := m.tickets[l.TicketID]; return !ok })
}

func byPosition[T any](rows []T, pos func(T) int, id func(T) string) {
	sort.Slice(rows, func(i, j int) bool {
		if pos(rows[i]) != pos(rows[j]) {
			return pos(rows[i]) < pos(rows[j])
		}
		return id(rows[i]) < id(rows[j])
	})
}

func (m *Mem) ListTrackerStatuses(_ context.Context, integrationID string) ([]TrackerStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusesOf(integrationID), nil
}

func (m *Mem) statusesOf(integrationID string) []TrackerStatus {
	var out []TrackerStatus
	for _, s := range m.statuses {
		if s.IntegrationID == integrationID {
			out = append(out, s)
		}
	}
	byPosition(out, func(s TrackerStatus) int { return s.Position }, func(s TrackerStatus) string { return s.ID })
	return out
}

func (m *Mem) ListTrackerTypes(_ context.Context, integrationID string) ([]TrackerType, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.typesOf(integrationID), nil
}

func (m *Mem) typesOf(integrationID string) []TrackerType {
	var out []TrackerType
	for _, t := range m.types {
		if t.IntegrationID == integrationID {
			out = append(out, t)
		}
	}
	byPosition(out, func(t TrackerType) int { return t.Position }, func(t TrackerType) string { return t.ID })
	return out
}

func (m *Mem) ReplaceTrackerStatuses(_ context.Context, integrationID string, rows []TrackerStatus, reassign map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.integrations[integrationID]; !ok {
		return fmt.Errorf("statuses of integration %s: %w", integrationID, ErrInvalid)
	}
	keep := map[string]bool{}
	steps, remote := map[trackerport.FlowStep]bool{}, map[string]bool{}
	for i := range rows {
		r := &rows[i]
		r.IntegrationID = integrationID
		if old, ok := m.statuses[r.ID]; ok && old.IntegrationID != integrationID {
			return fmt.Errorf("status %s is another integration's: %w", r.ID, ErrConflict)
		}
		if (r.FlowStep != "" && steps[r.FlowStep]) || remote[r.RemoteID] {
			return fmt.Errorf("statuses of integration %s: a flow step or remote id is held twice: %w", integrationID, ErrConflict)
		}
		steps[r.FlowStep] = r.FlowStep != ""
		remote[r.RemoteID] = true
		keep[r.ID] = true
	}
	moves := map[string]string{}
	for _, s := range m.statusesOf(integrationID) {
		if keep[s.ID] {
			continue
		}
		for _, t := range m.tickets {
			if t.StatusID != s.ID {
				continue
			}
			if to, ok := reassign[s.ID]; !ok || !keep[to] {
				return fmt.Errorf("status %s is used by ticket %s: %w", s.Name, t.ID, ErrReferenced)
			}
		}
		moves[s.ID] = reassign[s.ID]
	}
	for id, t := range m.tickets {
		if to, ok := moves[t.StatusID]; ok {
			t.StatusID = to
			m.tickets[id] = t
		}
	}
	maps.DeleteFunc(m.statuses, func(id string, s TrackerStatus) bool { return s.IntegrationID == integrationID && !keep[id] })
	for _, r := range rows {
		m.statuses[r.ID] = r
	}
	return nil
}

func (m *Mem) ReplaceTrackerTypes(_ context.Context, integrationID string, rows []TrackerType, reassign map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.integrations[integrationID]; !ok {
		return fmt.Errorf("types of integration %s: %w", integrationID, ErrInvalid)
	}
	keep := map[string]TrackerType{}
	remote := map[string]bool{}
	for i := range rows {
		r := &rows[i]
		r.IntegrationID = integrationID
		if old, ok := m.types[r.ID]; ok && old.IntegrationID != integrationID {
			return fmt.Errorf("type %s is another integration's: %w", r.ID, ErrConflict)
		}
		if remote[r.RemoteID] {
			return fmt.Errorf("types of integration %s: a remote id is held twice: %w", integrationID, ErrConflict)
		}
		remote[r.RemoteID] = true
		keep[r.ID] = *r
	}
	moves := map[string]string{}
	for _, y := range m.typesOf(integrationID) {
		if _, ok := keep[y.ID]; ok {
			continue
		}
		for _, t := range m.tickets {
			if t.TypeID != y.ID {
				continue
			}
			if to, ok := reassign[y.ID]; !ok || keep[to].ID == "" {
				return fmt.Errorf("type %s is used by ticket %s: %w", y.Name, t.ID, ErrReferenced)
			}
		}
		moves[y.ID] = reassign[y.ID]
	}
	for id, t := range m.tickets {
		if to, ok := moves[t.TypeID]; ok {
			t.TypeID = to
		}
		if y, ok := keep[t.TypeID]; ok {
			t.ChangeType = y.ChangeType
		}
		m.tickets[id] = t
	}
	maps.DeleteFunc(m.types, func(id string, y TrackerType) bool { _, ok := keep[id]; return y.IntegrationID == integrationID && !ok })
	for _, r := range rows {
		m.types[r.ID] = r
	}
	return nil
}

func (m *Mem) EnsureTrackerStatus(_ context.Context, s TrackerStatus) (TrackerStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.integrations[s.IntegrationID]; !ok {
		return TrackerStatus{}, fmt.Errorf("status %s references a missing integration: %w", s.Name, ErrInvalid)
	}
	for id, x := range m.statuses {
		if x.IntegrationID == s.IntegrationID && x.RemoteID == s.RemoteID {
			x.Name = s.Name
			if s.RemoteCategory != "" {
				x.RemoteCategory = s.RemoteCategory
			}
			m.statuses[id] = x
			return x, nil
		}
	}
	if s.FlowStep != "" {
		for _, x := range m.statuses {
			if x.IntegrationID == s.IntegrationID && x.FlowStep == s.FlowStep {
				s.FlowStep = ""
				break
			}
		}
	}
	if _, ok := m.statuses[s.ID]; ok {
		return TrackerStatus{}, fmt.Errorf("status %s: %w", s.ID, ErrConflict)
	}
	m.statuses[s.ID] = s
	return s, nil
}

func (m *Mem) EnsureTrackerType(_ context.Context, t TrackerType) (TrackerType, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.integrations[t.IntegrationID]; !ok {
		return TrackerType{}, fmt.Errorf("type %s references a missing integration: %w", t.Name, ErrInvalid)
	}
	for id, x := range m.types {
		if x.IntegrationID == t.IntegrationID && x.RemoteID == t.RemoteID {
			x.Name = t.Name
			m.types[id] = x
			return x, nil
		}
	}
	if _, ok := m.types[t.ID]; ok {
		return TrackerType{}, fmt.Errorf("type %s: %w", t.ID, ErrConflict)
	}
	m.types[t.ID] = t
	return t, nil
}

// oldestFirst orders tickets by creation, then id.
func oldestFirst(out []Ticket) {
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
}

func (m *Mem) GetTicketBySource(_ context.Context, userID, sourceID, key string) (Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tickets {
		if sourceID != "" && t.UserID == userID && t.SourceID == sourceID && strings.EqualFold(t.SourceKey, key) {
			return storedTicket(t), nil
		}
	}
	return Ticket{}, notFound("ticket", key)
}

func (m *Mem) GetTicketByProjectKey(_ context.Context, projectID, key string) (Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Ticket
	for _, t := range m.tickets {
		if key != "" && t.ProjectID == projectID && strings.EqualFold(t.SourceKey, key) {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return Ticket{}, notFound("ticket", key)
	}
	oldestFirst(out)
	return storedTicket(out[0]), nil
}

func (m *Mem) ListTicketsBySource(_ context.Context, sourceID string, syncable bool) ([]Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Ticket
	for _, t := range m.tickets {
		if sourceID == "" || t.SourceID != sourceID {
			continue
		}
		if syncable && (t.SyncState == SyncGone || m.statuses[t.StatusID].IsDone) {
			continue
		}
		out = append(out, storedTicket(t))
	}
	oldestFirst(out)
	return out, nil
}

func (m *Mem) SetTicketStatus(_ context.Context, id, statusID, by string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[id]
	if !ok {
		return notFound("ticket", id)
	}
	if _, ok := m.statuses[statusID]; !ok {
		return fmt.Errorf("ticket %s references a missing project, status or type: %w", id, ErrInvalid)
	}
	if t.StatusID == statusID {
		return nil
	}
	at = pgTime(at)
	t.StatusID, t.StatusChangedAt, t.StatusChangedBy, t.UpdatedAt = statusID, &at, by, at
	m.tickets[id] = t
	return nil
}

func (m *Mem) SetTicketLabels(_ context.Context, id string, labels []string, union bool, at time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[id]
	if !ok {
		return nil, notFound("ticket", id)
	}
	next := slices.Clone(labels)
	if union {
		next = nil
		for _, l := range append(slices.Clone(t.Labels), labels...) {
			if !slices.Contains(next, l) {
				next = append(next, l)
			}
		}
	}
	t.Labels, t.UpdatedAt = next, pgTime(at)
	t = storedTicket(t)
	m.tickets[id] = t
	return slices.Clone(t.Labels), nil
}

func (m *Mem) MirrorTicket(_ context.Context, id string, mi TicketMirror, changeType trackerport.ChangeType, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[id]
	if !ok {
		return false, notFound("ticket", id)
	}
	if _, ok := m.statuses[mi.StatusID]; mi.StatusID != "" && !ok {
		return false, fmt.Errorf("ticket %s references a missing project, status or type: %w", id, ErrInvalid)
	}
	if _, ok := m.types[mi.TypeID]; mi.TypeID != "" && !ok {
		return false, fmt.Errorf("ticket %s references a missing project, status or type: %w", id, ErrInvalid)
	}
	upd := pgTime(mi.RemoteUpdated)
	if !mi.RemoteUpdated.IsZero() && t.RemoteUpdatedAt != nil && upd.Before(*t.RemoteUpdatedAt) {
		return false, nil
	}
	for _, x := range m.tickets {
		if x.ID != id && x.UserID == t.UserID && x.SourceID == t.SourceID && strings.EqualFold(x.SourceKey, mi.SourceKey) {
			return false, fmt.Errorf("ticket %s: %s is already ticket %s: %w", id, mi.SourceKey, x.ID, ErrConflict)
		}
	}
	at = pgTime(at)
	if mi.StatusID != "" && mi.StatusID != t.StatusID {
		t.StatusID, t.StatusChangedAt, t.StatusChangedBy = mi.StatusID, &at, ChangedBySync
	}
	if mi.TypeID != "" {
		t.TypeID = mi.TypeID
	}
	t.Title, t.Goal, t.SourceKey, t.SourceURL, t.SourceType = mi.Title, mi.Goal, mi.SourceKey, mi.SourceURL, mi.SourceType
	t.RemoteID, t.Labels, t.ChangeType = mi.RemoteID, slices.Clone(mi.Labels), changeType
	if !mi.RemoteUpdated.IsZero() {
		t.RemoteUpdatedAt = &upd
	}
	t.SyncedAt, t.SyncState, t.SyncError, t.UpdatedAt = &at, SyncOK, "", at
	m.tickets[id] = storedTicket(t)
	return true, nil
}

func (m *Mem) SetTicketSyncState(_ context.Context, id, state, msg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[id]
	if !ok {
		return notFound("ticket", id)
	}
	t.SyncState, t.SyncError = state, msg
	m.tickets[id] = t
	return nil
}

func (m *Mem) DeleteTicketSource(_ context.Context, integrationID string, moves []TicketUnlink, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	byID := make(map[string]TicketUnlink, len(moves))
	for _, mv := range moves {
		byID[mv.TicketID] = mv
	}
	for _, t := range m.tickets {
		if _, ok := byID[t.ID]; t.SourceID == integrationID && !ok {
			return fmt.Errorf("integration %s: ticket %s was linked meanwhile: %w", integrationID, t.ID, ErrConflict)
		}
	}
	// Parity with the transaction: check everything before writing anything.
	for _, mv := range moves {
		if _, ok := m.statuses[mv.StatusID]; mv.StatusID != "" && !ok {
			return fmt.Errorf("ticket %s references a missing project, status or type: %w", mv.TicketID, ErrInvalid)
		}
		if _, ok := m.types[mv.TypeID]; mv.TypeID != "" && !ok {
			return fmt.Errorf("ticket %s references a missing project, status or type: %w", mv.TicketID, ErrInvalid)
		}
	}
	saved := maps.Clone(m.tickets)
	at = pgTime(at)
	for _, mv := range moves {
		t, ok := m.tickets[mv.TicketID]
		if !ok || t.SourceID != integrationID {
			continue
		}
		if t.StatusID != mv.StatusID {
			t.StatusChangedAt, t.StatusChangedBy = &at, ChangedByUser
		}
		t.StatusID, t.TypeID, t.ChangeType = mv.StatusID, mv.TypeID, mv.ChangeType
		t.SourceID, t.SourceKind, t.SourceKey, t.SourceURL, t.SourceType = "", "", "", "", ""
		t.RemoteID, t.RemoteUpdatedAt, t.SyncedAt, t.SyncState, t.SyncError = "", nil, nil, "", ""
		t.UpdatedAt = at
		m.tickets[t.ID] = storedTicket(t)
	}
	if err := m.deleteIntegration(integrationID); err != nil {
		m.tickets = saved
		return err
	}
	return nil
}

func storedComment(c TicketComment) TicketComment {
	c.CreatedAt, c.UpdatedAt = pgTime(c.CreatedAt), pgTime(c.UpdatedAt)
	return c
}

func (m *Mem) ListTicketComments(_ context.Context, ticketID string) ([]TicketComment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []TicketComment
	for _, c := range m.comments {
		if c.TicketID == ticketID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

var commentOrigins = []string{CommentRemote, CommentUser, CommentApothecary}

func (m *Mem) InsertTicketComment(_ context.Context, c TicketComment) (bool, error) {
	// Parity with ticket_comments_origin_check.
	if !slices.Contains(commentOrigins, c.Origin) {
		return false, fmt.Errorf("comment %s: origin %q is not one the table knows", c.ID, c.Origin)
	}
	c = storedComment(c)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tickets[c.TicketID]; !ok {
		return false, notFound("ticket", c.TicketID)
	}
	for _, x := range m.comments {
		if x.TicketID != c.TicketID {
			continue
		}
		if (c.RunID != "" && x.RunID == c.RunID && x.StageID == c.StageID) || (c.RemoteID != "" && x.RemoteID == c.RemoteID) || x.ID == c.ID {
			return false, nil
		}
	}
	m.comments[c.ID] = c
	return true, nil
}

func (m *Mem) ReconcileRemoteComments(_ context.Context, ticketID string, rows []TicketComment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tickets[ticketID]; !ok {
		return notFound("ticket", ticketID)
	}
	keep := map[string]bool{}
	for _, r := range rows {
		r = storedComment(r)
		r.TicketID, r.Origin = ticketID, CommentRemote
		keep[r.RemoteID] = true
		updated := false
		for id, x := range m.comments {
			if x.TicketID == ticketID && x.RemoteID == r.RemoteID {
				x.Author, x.Body, x.UpdatedAt = r.Author, r.Body, r.UpdatedAt
				m.comments[id] = x
				updated = true
				break
			}
		}
		if !updated {
			m.comments[r.ID] = r
		}
	}
	maps.DeleteFunc(m.comments, func(_ string, c TicketComment) bool {
		return c.TicketID == ticketID && c.Origin == CommentRemote && !keep[c.RemoteID]
	})
	return nil
}

func (m *Mem) UpsertTicketLink(_ context.Context, l TicketLink) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tickets[l.TicketID]; !ok {
		return notFound("ticket", l.TicketID)
	}
	m.links[l.TicketID+"\x00"+l.URL] = l
	return nil
}

func (m *Mem) ListTicketLinks(_ context.Context, ticketID string) ([]TicketLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []TicketLink
	for _, l := range m.links {
		if l.TicketID == ticketID {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out, nil
}
