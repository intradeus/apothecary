package filestore

import (
	"context"
	"fmt"
	"os"
	"time"
)

// SnapshotKind is the ONE home of kind predicates: SQL never lists kinds and
// no other layer re-derives family membership (DESIGN-FILES §5.1).
type SnapshotKind string

const (
	// KindOutput is a stage attempt's declared output at attempt end; Name
	// carries the contract role ("plan"), Path the workspace path it lived at.
	KindOutput SnapshotKind = "output"
	// KindLog is what an attempt printed (including the _provision
	// pseudo-stage); no role, no path.
	KindLog SnapshotKind = "log"
	// KindPingFile is one workspace file the agent attached to a ping,
	// pinned at raise time — what the human was shown, as shown. PingID names
	// the ping; Path is the ref verbatim.
	KindPingFile SnapshotKind = "ping_file"
)

// Snapshot is one engine-captured, redacted, immutable record of workspace
// content. It arrives owned (RunID + a coherent attribution) — there is no
// unclaimed state and no delivery path back into a workspace.
type Snapshot struct {
	ID        string       `json:"id"`
	RunID     string       `json:"run_id"`
	Kind      SnapshotKind `json:"kind"`
	StageID   string       `json:"stage_id"`
	Iteration int          `json:"iteration,omitempty"`
	PingID    string       `json:"ping_id,omitempty"` // KindPingFile only
	Name      string       `json:"name,omitempty"`    // output role; "" otherwise
	Path      string       `json:"path,omitempty"`    // workspace path; "" for logs
	Filename  string       `json:"filename"`
	Mime      string       `json:"mime"`
	Size      int64        `json:"size"`
	SHA256    string       `json:"sha256"`
	Truncated bool         `json:"truncated,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
}

// validate mirrors the DB CHECKs so an incoherent attribution fails loudly at
// the constructor, not at the insert.
func (s Snapshot) validate() error {
	if s.RunID == "" || s.StageID == "" {
		return fmt.Errorf("snapshot: run_id and stage_id are required")
	}
	switch s.Kind {
	case KindOutput:
		if s.Name == "" || s.Path == "" || s.PingID != "" {
			return fmt.Errorf("snapshot: an output carries name+path and no ping")
		}
	case KindLog:
		if s.Name != "" || s.Path != "" || s.PingID != "" {
			return fmt.Errorf("snapshot: a log carries no name/path/ping")
		}
	case KindPingFile:
		if s.PingID == "" || s.Path == "" || s.Name != "" {
			return fmt.Errorf("snapshot: a ping file carries ping_id+path and no role name")
		}
	default:
		return fmt.Errorf("snapshot: unknown kind %q", s.Kind)
	}
	return nil
}

// SnapshotDB is the metadata persistence port. Rows are a PROJECTION of
// snapshot_captured events: insert-only here; rebuild-projections truncates
// and re-folds them.
type SnapshotDB interface {
	InsertSnapshot(ctx context.Context, s Snapshot) error
	GetSnapshot(ctx context.Context, id string) (Snapshot, error)
	SnapshotsForRun(ctx context.Context, runID string) ([]Snapshot, error)
	SnapshotsForPing(ctx context.Context, pingID string) ([]Snapshot, error)
}

// truncationMarker prefixes tail-truncated capture content; the Truncated
// flag carries the same fact in metadata.
const truncationMarker = "\n[... truncated: this snapshot exceeded the capture limit ...]\n"

// Snapshots is the capture-policy store: no allowlist (the engine, not a
// browser, decides what is recorded), oversize content tail-truncated rather
// than dropped — for an audit trail a truncated record beats a missing one.
type Snapshots struct {
	db    SnapshotDB
	blobs *Blobs
	max   int64
	now   func() time.Time // injectable for tests
}

func NewSnapshots(db SnapshotDB, blobs *Blobs, maxBytes int64) *Snapshots {
	return &Snapshots{db: db, blobs: blobs, max: maxBytes, now: time.Now}
}

// Capture stores one snapshot. scrub is REQUIRED — a snapshot is by
// definition scrubbed content, because it outlives the workspace security
// boundary (secrets injected there must not surface in the durable record).
// The zero-secret case passes an identity function, not nil.
//
// The caller appends the matching snapshot_captured event; Capture itself
// never touches the event log (single-writer rule).
func (s *Snapshots) Capture(ctx context.Context, snap Snapshot, scrub func(string) string, data []byte) (Snapshot, error) {
	if scrub == nil {
		return Snapshot{}, fmt.Errorf("snapshot: a scrub function is required (pass identity for zero secrets)")
	}
	if err := snap.validate(); err != nil {
		return Snapshot{}, err
	}
	data = []byte(scrub(string(data)))
	if int64(len(data)) > s.max {
		keep := max(s.max-int64(len(truncationMarker)), 0)
		data = append([]byte(truncationMarker), data[int64(len(data))-keep:]...)
		snap.Truncated = true
	}
	sha, err := s.blobs.Put(data)
	if err != nil {
		return Snapshot{}, err
	}
	snap.ID = newID()
	snap.SHA256 = sha
	snap.Size = int64(len(data))
	if snap.Filename == "" {
		snap.Filename = "file"
	}
	snap.Filename = SafeName(snap.Filename)
	if snap.Mime == "" {
		snap.Mime = detectMimeBytes(data, snap.Filename)
	}
	snap.CreatedAt = s.now().UTC()
	if err := s.db.InsertSnapshot(ctx, snap); err != nil {
		// The blob stays: another reference may share the content, and the
		// sweep owns deletion (never-unlink rule, §3.1).
		return Snapshot{}, err
	}
	return snap, nil
}

func (s *Snapshots) Get(ctx context.Context, id string) (Snapshot, error) {
	return s.db.GetSnapshot(ctx, id)
}

// Open returns a verified, seekable handle for streaming; ErrContentGone
// when retention swept the bytes (the metadata still stands).
func (s *Snapshots) Open(ctx context.Context, id string) (*os.File, Snapshot, error) {
	snap, err := s.db.GetSnapshot(ctx, id)
	if err != nil {
		return nil, Snapshot{}, err
	}
	f, err := s.blobs.Open(snap.SHA256)
	if err != nil {
		return nil, Snapshot{}, err
	}
	return f, snap, nil
}

// Bytes is Open in-hand (small reads: verdicts, plan rendering).
func (s *Snapshots) Bytes(ctx context.Context, id string) ([]byte, Snapshot, error) {
	snap, err := s.db.GetSnapshot(ctx, id)
	if err != nil {
		return nil, Snapshot{}, err
	}
	data, err := s.blobs.Bytes(snap.SHA256)
	if err != nil {
		return nil, Snapshot{}, err
	}
	return data, snap, nil
}

func (s *Snapshots) ForRun(ctx context.Context, runID string) ([]Snapshot, error) {
	return s.db.SnapshotsForRun(ctx, runID)
}

func (s *Snapshots) ForPing(ctx context.Context, pingID string) ([]Snapshot, error) {
	return s.db.SnapshotsForPing(ctx, pingID)
}
