package filestore

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Mem is the in-memory AttachmentDB + SnapshotDB used by unit tests across
// packages (filestore, engine, httpapi, tickets) — one substrate instead of
// the three divergent fakes the old artifacts store accumulated, and it
// mirrors the pgstore semantics that matter: one-shot claims, structural
// ownership, insert-only snapshots.
type Mem struct {
	mu          sync.Mutex
	attachments map[string]Attachment
	snapshots   map[string]Snapshot
}

func NewMem() *Mem {
	return &Mem{attachments: map[string]Attachment{}, snapshots: map[string]Snapshot{}}
}

// --- AttachmentDB -----------------------------------------------------------

func (m *Mem) InsertAttachment(_ context.Context, a Attachment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attachments[a.ID] = a
	return nil
}

func (m *Mem) GetAttachment(_ context.Context, id string) (Attachment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.attachments[id]
	if !ok {
		return Attachment{}, fmt.Errorf("attachment %s: %w", id, ErrNotFound)
	}
	return a, nil
}

// ClaimAttachments mirrors the SQL: only still-unclaimed rows are touched, so
// a second claim (or a claim of someone else's attachment) is a silent no-op.
func (m *Mem) ClaimAttachments(_ context.Context, ids []string, ticketID, runID, pingID string) error {
	if (ticketID == "") == (runID == "") {
		return fmt.Errorf("claim: exactly one of ticket/run must own the attachment")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		a, ok := m.attachments[id]
		if !ok || !a.Unclaimed() {
			continue
		}
		a.TicketID, a.RunID, a.PingID = ticketID, runID, pingID
		m.attachments[id] = a
	}
	return nil
}

func (m *Mem) list(match func(Attachment) bool) []Attachment {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Attachment
	for _, a := range m.attachments {
		if match(a) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (m *Mem) AttachmentsForTicket(_ context.Context, ticketID string) ([]Attachment, error) {
	return m.list(func(a Attachment) bool { return a.TicketID == ticketID && ticketID != "" }), nil
}

func (m *Mem) AttachmentsForRun(_ context.Context, runID string) ([]Attachment, error) {
	return m.list(func(a Attachment) bool { return a.RunID == runID && runID != "" }), nil
}

func (m *Mem) AttachmentsForPing(_ context.Context, pingID string) ([]Attachment, error) {
	return m.list(func(a Attachment) bool { return a.PingID == pingID && pingID != "" }), nil
}

func (m *Mem) DeleteAttachmentsForRun(_ context.Context, runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, a := range m.attachments {
		if a.RunID == runID && runID != "" {
			delete(m.attachments, id)
		}
	}
	return nil
}

func (m *Mem) DeleteAttachmentsForTicket(_ context.Context, ticketID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, a := range m.attachments {
		if a.TicketID == ticketID && ticketID != "" {
			delete(m.attachments, id)
		}
	}
	return nil
}

func (m *Mem) UnclaimedAttachmentIDsBefore(_ context.Context, userID string, cutoff time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for id, a := range m.attachments {
		if a.Unclaimed() && a.CreatedAt.Before(cutoff) && (userID == "" || a.UserID == userID) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (m *Mem) DeleteAttachments(_ context.Context, ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		delete(m.attachments, id)
	}
	return nil
}

// --- SnapshotDB -------------------------------------------------------------

func (m *Mem) InsertSnapshot(_ context.Context, s Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshots[s.ID] = s
	return nil
}

func (m *Mem) GetSnapshot(_ context.Context, id string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.snapshots[id]
	if !ok {
		return Snapshot{}, fmt.Errorf("snapshot %s: %w", id, ErrNotFound)
	}
	return s, nil
}

func (m *Mem) SnapshotsForRun(_ context.Context, runID string) ([]Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Snapshot
	for _, s := range m.snapshots {
		if s.RunID == runID {
			out = append(out, s)
		}
	}
	// Mirror the SQL ordering: stage, iteration, then capture time.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.StageID != b.StageID {
			return a.StageID < b.StageID
		}
		if a.Iteration != b.Iteration {
			return a.Iteration < b.Iteration
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	return out, nil
}

func (m *Mem) SnapshotsForPing(_ context.Context, pingID string) ([]Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Snapshot
	for _, s := range m.snapshots {
		if s.PingID == pingID && pingID != "" {
			out = append(out, s)
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

// TruncateSnapshots empties the projection (rebuild-projections parity).
func (m *Mem) TruncateSnapshots(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshots = map[string]Snapshot{}
	return nil
}

var (
	_ AttachmentDB = (*Mem)(nil)
	_ SnapshotDB   = (*Mem)(nil)
)
