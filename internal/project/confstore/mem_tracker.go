package confstore

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"time"
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
	remote := map[string]bool{}
	for i := range rows {
		r := &rows[i]
		r.IntegrationID = integrationID
		if old, ok := m.statuses[r.ID]; ok && old.IntegrationID != integrationID {
			return fmt.Errorf("status %s is another integration's: %w", r.ID, ErrConflict)
		}
		if remote[r.RemoteID] {
			return fmt.Errorf("statuses of integration %s: a remote id is held twice: %w", integrationID, ErrConflict)
		}
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
			m.statuses[id] = x
			return x, nil
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

func (m *Mem) SetTicketStatus(_ context.Context, id, statusID, by string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[id]
	if !ok {
		return notFound("ticket", id)
	}
	if _, ok := m.statuses[statusID]; !ok {
		return fmt.Errorf("ticket %s references a missing status or type: %w", id, ErrInvalid)
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

var commentOrigins = []string{CommentUser, CommentApothecary}

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
		if (c.RunID != "" && x.RunID == c.RunID && x.StageID == c.StageID) || x.ID == c.ID {
			return false, nil
		}
	}
	m.comments[c.ID] = c
	return true, nil
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
