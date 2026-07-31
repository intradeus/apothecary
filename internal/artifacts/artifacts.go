// Package artifacts is the blob store for HUMAN-attached files (documents,
// images) sent with a run's goal or a ping answer. The bytes live on the
// filesystem under <RunsDir>/artifacts/<id[:2]>/<id>; the metadata rides a
// Postgres side-table (the DB port, implemented by pgstore) — durable state,
// not an event, mirroring channels / run_exposed_ports.
//
// Everything the event log REFERENCES lives here, in both directions:
// human→agent uploads (goal, ticket, ping answers), engine-captured attempt
// snapshots (stage outputs and logs), and agent→human ping attachments, which
// are snapshotted when the ping is raised. That last one used to be the
// exception — referenced by repo-relative path and read back live — which made
// the log point at state that dies with the workspace, and made a re-opened
// ping show the file's current content rather than what the human reviewed.
// A ping's refs are still the names; the bytes behind them are ours.
package artifacts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Scopes an artifact row can hold. `orphan` = uploaded but not yet attached
// to a run/ping (GC-eligible); the rest are the attachment points.
const (
	ScopeOrphan = "orphan"
	ScopeGoal   = "goal"
	// ScopeTicketGoal is a TICKET's attachment: owned by the ticket (run_id
	// stays empty), delivered into the workspace of EVERY run of that ticket.
	// Distinct from ScopeGoal, which binds an artifact to one run — the
	// artifacts table has a single run_id, so a ticket attachment could
	// otherwise only ever reach the first run.
	ScopeTicketGoal = "ticket_goal"
	ScopePingAnswer = "ping_answer"
	// ScopePingPayload is the AGENT→human side of a ping: a snapshot, taken
	// when the ping is raised, of each workspace file the agent attached.
	// Before it existed those files were referenced by repo-relative path and
	// read back live, which made the event log point at state that dies with
	// the workspace — and made a re-opened ping show the file's CURRENT
	// content rather than what the human actually reviewed. Like the stage
	// scopes below it is engine-captured and outlives the run.
	ScopePingPayload = "ping_payload"
	// ScopeStageOutput / ScopeStageLog are per-attempt snapshots (PLAN5 M27):
	// what a stage attempt actually produced, and what it printed. Unlike the
	// scopes above these are engine-captured rather than human-uploaded, and
	// they deliberately OUTLIVE the run — they are the audit trail, so the
	// archive sweep leaves them alone.
	ScopeStageOutput = "stage_output"
	ScopeStageLog    = "stage_log"
)

// IsStageScope reports whether a scope is an engine-captured attempt
// snapshot rather than a human upload.
func IsStageScope(scope string) bool {
	return scope == ScopeStageOutput || scope == ScopeStageLog
}

// IsAuditScope reports whether a scope is part of the durable record of what
// a run did and what a human was shown — engine-captured, and deliberately
// outliving the run (the archive sweep leaves these alone). Human uploads are
// the opposite: they are the run's inputs and go when the run is archived.
func IsAuditScope(scope string) bool {
	return IsStageScope(scope) || scope == ScopePingPayload
}

var (
	ErrTooLarge = errors.New("artifact exceeds the size limit")
	ErrMimeType = errors.New("artifact type is not allowed")
	ErrNotFound = errors.New("artifact not found")
)

// Meta is an artifact's metadata (no bytes).
type Meta struct {
	ID        string
	RunID     string
	PingID    string
	Scope     string
	Filename  string
	Mime      string
	Size      int64
	SHA256    string
	CreatedAt time.Time
	// StageID/Iteration/OutputName attribute a stage-scoped snapshot to the
	// attempt that produced it (PLAN5 M27). OutputName is the declared
	// output's ROLE ("plan", "verdict") — empty for a stage log.
	StageID    string
	Iteration  int
	OutputName string
}

// DB is the metadata persistence the Store needs (a port; *pgstore.Store
// implements it). Kept in Meta terms so the store is testable with a fake.
type DB interface {
	InsertArtifact(ctx context.Context, m Meta) error
	ArtifactsForStage(ctx context.Context, runID string) ([]Meta, error)
	AssociateArtifacts(ctx context.Context, ids []string, runID, pingID, scope string) error
	ArtifactsForRun(ctx context.Context, runID string) ([]Meta, error)
	ArtifactsForPing(ctx context.Context, pingID, scope string) ([]Meta, error)
	GetArtifact(ctx context.Context, id string) (Meta, error)
	DeleteArtifactsForRun(ctx context.Context, runID string) ([]string, error)
	OrphanArtifactIDsBefore(ctx context.Context, cutoff time.Time) ([]string, error)
	DeleteArtifacts(ctx context.Context, ids []string) error
}

// Store persists uploaded blobs on disk and their metadata via DB.
type Store struct {
	db    DB
	dir   string // <RunsDir>/artifacts
	max   int64  // per-file byte cap
	allow func(string) bool
	now   func() time.Time // injectable for tests
}

// New builds a Store. dir is derived from RunsDir; maxBytes caps each file;
// allow is a MIME allowlist (exact types or "text/*" wildcards).
func New(db DB, runsDir string, maxBytes int64, allow []string) *Store {
	return &Store{
		db:    db,
		dir:   filepath.Join(runsDir, "artifacts"),
		max:   maxBytes,
		allow: mimeAllowFunc(allow),
		now:   time.Now,
	}
}

func (s *Store) blobPath(id string) string { return filepath.Join(s.dir, id[:2], id) }

// Save streams r to disk (capped, hashed), sniffs+allowlists the MIME type,
// and records an orphan metadata row. On any failure the partial blob is
// removed. The caller later Associates the returned id with a run/ping.
func (s *Store) Save(ctx context.Context, filename, declaredMime string, r io.Reader) (Meta, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return Meta{}, err
	}
	tmp, err := os.CreateTemp(s.dir, "upload-*")
	if err != nil {
		return Meta{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed away

	h := sha256.New()
	// Read at most max+1 bytes: max+1 means the source had more than the cap.
	n, err := io.Copy(tmp, io.LimitReader(io.TeeReader(r, h), s.max+1))
	tmp.Close()
	if err != nil {
		return Meta{}, err
	}
	if n > s.max {
		return Meta{}, ErrTooLarge
	}

	mimeType := detectMime(tmpName, filename, declaredMime)
	if !s.allow(mimeType) {
		return Meta{}, ErrMimeType
	}

	id := newID()
	if err := os.MkdirAll(filepath.Join(s.dir, id[:2]), 0o755); err != nil {
		return Meta{}, err
	}
	final := s.blobPath(id)
	if err := os.Rename(tmpName, final); err != nil {
		return Meta{}, err
	}
	m := Meta{
		ID: id, Scope: ScopeOrphan, Filename: SafeName(filename),
		Mime: mimeType, Size: n, SHA256: hex.EncodeToString(h.Sum(nil)),
	}
	if err := s.db.InsertArtifact(ctx, m); err != nil {
		os.Remove(final)
		return Meta{}, err
	}
	return m, nil
}

// Bytes returns an artifact's bytes and metadata (for delivery + serving).
func (s *Store) Bytes(ctx context.Context, id string) ([]byte, Meta, error) {
	m, err := s.db.GetArtifact(ctx, id)
	if err != nil {
		return nil, Meta{}, err
	}
	data, err := os.ReadFile(s.blobPath(id))
	if err != nil {
		return nil, Meta{}, err
	}
	return data, m, nil
}

func (s *Store) Associate(ctx context.Context, ids []string, runID, pingID, scope string) error {
	return s.db.AssociateArtifacts(ctx, ids, runID, pingID, scope)
}

// Capture stores an engine-produced attempt snapshot: a declared output's
// bytes or a stage's log (PLAN5 M27). Unlike Save it takes the bytes
// directly, skips the MIME allowlist (the engine, not a browser upload,
// decides what is captured) and lands attached rather than orphaned.
//
// Oversized content is TAIL-truncated with a marker rather than dropped: for
// an audit trail a truncated artifact beats a missing one.
func (s *Store) Capture(ctx context.Context, m Meta, data []byte) (Meta, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return Meta{}, err
	}
	if int64(len(data)) > s.max {
		const marker = "\n[... truncated: this artifact exceeded the capture limit ...]\n"
		keep := s.max - int64(len(marker))
		if keep < 0 {
			keep = 0
		}
		data = append([]byte(marker), data[int64(len(data))-keep:]...)
	}
	id := newID()
	if err := os.MkdirAll(filepath.Join(s.dir, id[:2]), 0o755); err != nil {
		return Meta{}, err
	}
	sum := sha256.Sum256(data)
	m.ID = id
	m.Size = int64(len(data))
	m.SHA256 = hex.EncodeToString(sum[:])
	if m.Filename == "" {
		m.Filename = "file"
	}
	m.Filename = SafeName(m.Filename)
	if m.Mime == "" {
		m.Mime = detectMimeBytes(data, m.Filename)
	}
	if err := os.WriteFile(s.blobPath(id), data, 0o600); err != nil {
		return Meta{}, err
	}
	if err := s.db.InsertArtifact(ctx, m); err != nil {
		os.Remove(s.blobPath(id))
		return Meta{}, err
	}
	return m, nil
}

// ForStage lists a run's per-attempt snapshots, oldest first.
func (s *Store) ForStage(ctx context.Context, runID string) ([]Meta, error) {
	return s.db.ArtifactsForStage(ctx, runID)
}

// Meta returns one artifact's metadata by id (no bytes). Used to resolve a
// ticket's attachments, which are owned by the ticket rather than any run and
// so can't be found through ForRun.
func (s *Store) Meta(ctx context.Context, id string) (Meta, error) {
	return s.db.GetArtifact(ctx, id)
}

func (s *Store) ForRun(ctx context.Context, runID string) ([]Meta, error) {
	return s.db.ArtifactsForRun(ctx, runID)
}

func (s *Store) ForPing(ctx context.Context, pingID, scope string) ([]Meta, error) {
	return s.db.ArtifactsForPing(ctx, pingID, scope)
}

// detectMimeBytes classifies captured content without a temp file.
func detectMimeBytes(data []byte, filename string) string {
	if ext := filepath.Ext(filename); ext != "" {
		if ct := mime.TypeByExtension(ext); ct != "" {
			return strings.TrimSpace(strings.Split(ct, ";")[0])
		}
	}
	n := min(len(data), 512)
	return strings.TrimSpace(strings.Split(http.DetectContentType(data[:n]), ";")[0])
}

// DeleteForRun removes a run's artifact rows and their blobs (run archive).
func (s *Store) DeleteForRun(ctx context.Context, runID string) error {
	ids, err := s.db.DeleteArtifactsForRun(ctx, runID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		os.Remove(s.blobPath(id))
	}
	return nil
}

// GCOrphans removes never-attached uploads older than olderThan. Blobs are
// unlinked before the rows so a crash mid-sweep self-heals (the rows are
// re-found next pass; os.Remove of a gone blob is harmless).
func (s *Store) GCOrphans(ctx context.Context, olderThan time.Duration) (int, error) {
	ids, err := s.db.OrphanArtifactIDsBefore(ctx, s.now().Add(-olderThan))
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	for _, id := range ids {
		os.Remove(s.blobPath(id))
	}
	if err := s.db.DeleteArtifacts(ctx, ids); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// SafeName reduces an uploaded filename to a single safe path component,
// preserving the extension where possible. Used for both the stored metadata
// and the in-workspace delivery path.
func SafeName(name string) string {
	name = filepath.Base(filepath.FromSlash(name))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, name)
	name = strings.Trim(strings.TrimSpace(name), ".")
	if name == "" {
		return "file"
	}
	if len(name) > 128 {
		name = name[len(name)-128:] // keep the extension end
	}
	return name
}

// detectMime resolves the best content type: a specific declared type wins,
// else content sniffing, else the extension, else octet-stream. The result
// is allowlist-checked by the caller.
func detectMime(path, filename, declared string) string {
	if d := strings.TrimSpace(strings.Split(declared, ";")[0]); d != "" && d != "application/octet-stream" {
		return d
	}
	if f, err := os.Open(path); err == nil {
		buf := make([]byte, 512)
		n, _ := f.Read(buf)
		f.Close()
		if ct := http.DetectContentType(buf[:n]); ct != "application/octet-stream" {
			return strings.TrimSpace(strings.Split(ct, ";")[0])
		}
	}
	if ext := filepath.Ext(filename); ext != "" {
		if ct := mime.TypeByExtension(ext); ct != "" {
			return strings.TrimSpace(strings.Split(ct, ";")[0])
		}
	}
	return "application/octet-stream"
}

func mimeAllowFunc(allow []string) func(string) bool {
	exact := map[string]bool{}
	var prefixes []string
	for _, a := range allow {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if strings.HasSuffix(a, "/*") {
			prefixes = append(prefixes, strings.TrimSuffix(a, "*"))
		} else {
			exact[a] = true
		}
	}
	return func(m string) bool {
		m = strings.ToLower(strings.TrimSpace(strings.Split(m, ";")[0]))
		if exact[m] {
			return true
		}
		for _, p := range prefixes {
			if strings.HasPrefix(m, p) {
				return true
			}
		}
		return false
	}
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; fall back to a
		// time-seeded value so an id is still unique enough to not collide.
		t := time.Now().UnixNano()
		for i := range 8 {
			b[i] = byte(t >> (8 * i))
		}
	}
	return hex.EncodeToString(b[:])
}
