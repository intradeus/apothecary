package filestore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Mem is the in-memory AttachmentDB + SnapshotDB used by unit tests across
// packages (filestore, engine, httpapi, tickets) — one substrate instead of
// the three divergent fakes the old artifacts store accumulated. It mirrors
// pgstore, held to it by filetest: one-shot claims, structural ownership,
// insert-only snapshots, the schema's CHECKs. The one thing it cannot do is
// the ticket foreign key: it has no tickets.
type Mem struct {
	mu          sync.Mutex
	attachments map[string]Attachment
	snapshots   map[string]Snapshot
}

func NewMem() *Mem {
	return &Mem{attachments: map[string]Attachment{}, snapshots: map[string]Snapshot{}}
}

// instant is a time as a timestamptz column hands it back: whole microseconds,
// truncated, in the process's zone.
func instant(t time.Time) time.Time { return t.Truncate(time.Microsecond).Local() }

// storable refuses what a text column cannot hold.
func storable(fields ...string) error {
	for _, f := range fields {
		if !utf8.ValidString(f) || strings.ContainsRune(f, 0) {
			return fmt.Errorf("invalid byte sequence in %q", f)
		}
	}
	return nil
}

// --- AttachmentDB -----------------------------------------------------------

// ownerChecks are the attachments table's CHECKs.
func ownerChecks(ticketID, runID, pingID string) error {
	if ticketID != "" && runID != "" {
		return fmt.Errorf("attachment: a ticket or a run, not both")
	}
	if pingID != "" && runID == "" {
		return fmt.Errorf("attachment: an answer file names its run")
	}
	return nil
}

func (m *Mem) InsertAttachment(_ context.Context, a Attachment) error {
	if err := storable(a.ID, a.UserID, a.TicketID, a.RunID, a.PingID, a.Filename, a.Mime, a.SHA256, a.RemoteID); err != nil {
		return err
	}
	if err := ownerChecks(a.TicketID, a.RunID, a.PingID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.attachments[a.ID]; dup {
		return fmt.Errorf("attachment %s: duplicate key", a.ID)
	}
	if m.remoteTaken(a.TicketID, a.RemoteID, nil) {
		return fmt.Errorf("attachment %s: ticket %s already holds remote file %s", a.ID, a.TicketID, a.RemoteID)
	}
	a.CreatedAt = instant(a.CreatedAt)
	m.attachments[a.ID] = a
	return nil
}

// remoteTaken is attachments_remote_idx: one row per (ticket, remote id)
// among claimed, remote rows, the ids in except aside. Caller holds mu.
func (m *Mem) remoteTaken(ticketID, remoteID string, except map[string]bool) bool {
	if ticketID == "" || remoteID == "" {
		return false
	}
	for id, x := range m.attachments {
		if !except[id] && x.TicketID == ticketID && x.RemoteID == remoteID {
			return true
		}
	}
	return false
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

// ClaimAttachments mirrors the SQL: only the user's still-unclaimed rows are
// touched, so a second claim (or a claim of someone else's attachment) is a
// silent no-op. It is one UPDATE, so a CHECK fails all of it, and only if some
// row matched.
func (m *Mem) ClaimAttachments(_ context.Context, userID string, ids []string, ticketID, runID, pingID string) error {
	if (ticketID == "") == (runID == "") {
		return fmt.Errorf("claim: exactly one of ticket/run must own the attachment")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var matched []string
	for _, id := range ids {
		if a, ok := m.attachments[id]; ok && a.Unclaimed() && a.UserID == userID {
			matched = append(matched, id)
		}
	}
	if len(matched) == 0 {
		return nil
	}
	if err := ownerChecks(ticketID, runID, pingID); err != nil {
		return err
	}
	// The unique index holds after the UPDATE or none of it applies.
	if ticketID != "" {
		moving := make(map[string]bool, len(matched))
		for _, id := range matched {
			moving[id] = true
		}
		seen := map[string]bool{}
		for id := range moving {
			r := m.attachments[id].RemoteID
			if r == "" {
				continue
			}
			if seen[r] || m.remoteTaken(ticketID, r, moving) {
				return fmt.Errorf("claim: ticket %s already holds remote file %s", ticketID, r)
			}
			seen[r] = true
		}
	}
	for _, id := range matched {
		a := m.attachments[id]
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
		if a.Unclaimed() && a.CreatedAt.Before(instant(cutoff)) && (userID == "" || a.UserID == userID) {
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

// snapshotChecks are the snapshots table's CHECKs, stated as the schema does.
// Not validate: a run_id or stage_id of "" is NOT NULL as far as it can see.
func snapshotChecks(s Snapshot) error {
	switch {
	case s.Kind != KindOutput && s.Kind != KindLog && s.Kind != KindPingFile:
		return fmt.Errorf("snapshot: unknown kind %q", s.Kind)
	case (s.Kind == KindPingFile) != (s.PingID != ""):
		return fmt.Errorf("snapshot: a ping_id on a %s", s.Kind)
	case (s.Kind == KindOutput) != (s.Name != ""):
		return fmt.Errorf("snapshot: a role name on a %s", s.Kind)
	case (s.Kind != KindLog) != (s.Path != ""):
		return fmt.Errorf("snapshot: a path on a %s", s.Kind)
	}
	return nil
}

func (m *Mem) InsertSnapshot(_ context.Context, s Snapshot) error {
	if err := storable(s.ID, s.RunID, string(s.Kind), s.StageID, s.PingID, s.Name, s.Path, s.Filename, s.Mime, s.SHA256); err != nil {
		return err
	}
	if err := snapshotChecks(s); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.snapshots[s.ID]; dup {
		return fmt.Errorf("snapshot %s: duplicate key", s.ID)
	}
	s.CreatedAt = instant(s.CreatedAt)
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
	// Mirror the SQL ordering: stage, iteration, then capture time. The
	// stage compares as en_US.utf8 does (Postgres's default collation here):
	// punctuation does not weigh, so _provision sorts among the p's.
	unpunctuated := strings.NewReplacer("-", "", "_", "")
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if x, y := unpunctuated.Replace(a.StageID), unpunctuated.Replace(b.StageID); x != y {
			return x < y
		}
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
