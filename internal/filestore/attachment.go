package filestore

import (
	"context"
	"io"
	"os"
	"time"
)

// Attachment is a file a human gave the factory — an upload or a tracker
// import. Ownership is structural: exactly one of TicketID / RunID is set
// after the claim (PingID rides RunID for answer files); all empty =
// unclaimed, GC-eligible. There are deliberately no stage fields here — an
// attachment cannot be confused with the engine's record (DESIGN-FILES §2).
type Attachment struct {
	ID string `json:"id"`
	// UserID is whoever uploaded it: the owner from the moment it lands,
	// before anything claims it.
	UserID    string    `json:"user_id,omitempty"`
	TicketID  string    `json:"ticket_id,omitempty"`
	RunID     string    `json:"run_id,omitempty"`
	PingID    string    `json:"ping_id,omitempty"`
	Filename  string    `json:"filename"`
	Mime      string    `json:"mime"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
}

// Unclaimed reports a fresh upload no owner has claimed yet.
func (a Attachment) Unclaimed() bool { return a.TicketID == "" && a.RunID == "" }

// AttachmentDB is the metadata persistence port (implemented by pgstore and
// the in-package Mem). Claims are one-shot: implementations update ONLY rows
// that are still unclaimed, so an id can never be re-parented.
type AttachmentDB interface {
	InsertAttachment(ctx context.Context, a Attachment) error
	GetAttachment(ctx context.Context, id string) (Attachment, error)
	// ClaimAttachments sets the owner columns on still-unclaimed rows.
	// Exactly one of ticketID / runID must be non-empty (pingID rides runID).
	ClaimAttachments(ctx context.Context, ids []string, ticketID, runID, pingID string) error
	AttachmentsForTicket(ctx context.Context, ticketID string) ([]Attachment, error)
	AttachmentsForRun(ctx context.Context, runID string) ([]Attachment, error)
	AttachmentsForPing(ctx context.Context, pingID string) ([]Attachment, error)
	DeleteAttachmentsForRun(ctx context.Context, runID string) error
	DeleteAttachmentsForTicket(ctx context.Context, ticketID string) error
	// UnclaimedAttachmentIDsBefore lists one user's never-claimed uploads
	// older than cutoff (the orphan GC; "" = everyone's).
	UnclaimedAttachmentIDsBefore(ctx context.Context, userID string, cutoff time.Time) ([]string, error)
	DeleteAttachments(ctx context.Context, ids []string) error
}

// UploadLimits is the client-visible upload policy (GET /api/attachments/limits),
// so the browser can reject a bad pick inline instead of after a failed
// round trip.
type UploadLimits struct {
	MaxBytes  int64    `json:"max_bytes"`
	MaxFiles  int      `json:"max_files"`
	MimeAllow []string `json:"mime_allow"`
}

// MaxUploadFiles caps one multipart request (and one picker selection).
const MaxUploadFiles = 8

// Attachments is the upload-policy store: MIME allowlist + hard size reject.
// Blob deletion is never done here — rows are deleted and the blob sweep
// reclaims unreferenced content (§3.1).
type Attachments struct {
	db        AttachmentDB
	blobs     *Blobs
	max       int64
	allowList []string
	allow     func(string) bool
	now       func() time.Time // injectable for tests
}

// NewAttachments builds the store. maxBytes caps each file; allow is the
// MIME allowlist (exact types or "text/*" wildcards).
func NewAttachments(db AttachmentDB, blobs *Blobs, maxBytes int64, allow []string) *Attachments {
	return &Attachments{
		db: db, blobs: blobs, max: maxBytes,
		allowList: allow, allow: mimeAllowFunc(allow),
		now: time.Now,
	}
}

// Limits reports the upload policy for client-side mirroring.
func (s *Attachments) Limits() UploadLimits {
	return UploadLimits{MaxBytes: s.max, MaxFiles: MaxUploadFiles, MimeAllow: s.allowList}
}

// Save streams one upload into the blob store (capped, hashed, deduped),
// sniffs + allowlists its MIME type, and records an unclaimed row. A
// rejected upload may leave an unreferenced blob behind — that is the
// sweep's problem, not a correctness one (never-unlink rule).
func (s *Attachments) Save(ctx context.Context, userID, filename, declaredMime string, r io.Reader) (Attachment, error) {
	sha, size, err := s.blobs.PutStream(r, s.max)
	if err != nil {
		return Attachment{}, err
	}
	mimeType := detectMime(s.blobs.path(sha), filename, declaredMime)
	if !s.allow(mimeType) {
		return Attachment{}, ErrMimeType
	}
	a := Attachment{
		ID: newID(), UserID: userID, Filename: SafeName(filename), Mime: mimeType,
		Size: size, SHA256: sha, CreatedAt: s.now().UTC(),
	}
	if err := s.db.InsertAttachment(ctx, a); err != nil {
		return Attachment{}, err
	}
	return a, nil
}

// ClaimForTicket binds still-unclaimed uploads to a ticket. Already-claimed
// ids are a no-op, so re-sending a full list on update is safe.
func (s *Attachments) ClaimForTicket(ctx context.Context, ids []string, ticketID string) error {
	if len(ids) == 0 {
		return nil
	}
	return s.db.ClaimAttachments(ctx, ids, ticketID, "", "")
}

// ClaimForRun binds still-unclaimed uploads to a run (goal files) or, with a
// pingID, to an answer within that run.
func (s *Attachments) ClaimForRun(ctx context.Context, ids []string, runID, pingID string) error {
	if len(ids) == 0 {
		return nil
	}
	return s.db.ClaimAttachments(ctx, ids, "", runID, pingID)
}

func (s *Attachments) Get(ctx context.Context, id string) (Attachment, error) {
	return s.db.GetAttachment(ctx, id)
}

// Bytes returns an attachment's content in hand (workspace delivery).
func (s *Attachments) Bytes(ctx context.Context, id string) ([]byte, Attachment, error) {
	a, err := s.db.GetAttachment(ctx, id)
	if err != nil {
		return nil, Attachment{}, err
	}
	data, err := s.blobs.Bytes(a.SHA256)
	if err != nil {
		return nil, Attachment{}, err
	}
	return data, a, nil
}

// Open returns a verified, seekable handle for streaming (the API byte
// route); the caller closes it.
func (s *Attachments) Open(ctx context.Context, id string) (*os.File, Attachment, error) {
	a, err := s.db.GetAttachment(ctx, id)
	if err != nil {
		return nil, Attachment{}, err
	}
	f, err := s.blobs.Open(a.SHA256)
	if err != nil {
		return nil, Attachment{}, err
	}
	return f, a, nil
}

func (s *Attachments) ForTicket(ctx context.Context, ticketID string) ([]Attachment, error) {
	return s.db.AttachmentsForTicket(ctx, ticketID)
}

func (s *Attachments) ForRun(ctx context.Context, runID string) ([]Attachment, error) {
	return s.db.AttachmentsForRun(ctx, runID)
}

func (s *Attachments) ForPing(ctx context.Context, pingID string) ([]Attachment, error) {
	return s.db.AttachmentsForPing(ctx, pingID)
}

// DeleteForRun removes a run's attachment rows (archive). Blobs are the
// sweep's job.
func (s *Attachments) DeleteForRun(ctx context.Context, runID string) error {
	return s.db.DeleteAttachmentsForRun(ctx, runID)
}

// DeleteForTicket removes a ticket's attachment rows — the fix for the old
// ticket_goal leak, called by the ticket factory's Delete.
func (s *Attachments) DeleteForTicket(ctx context.Context, ticketID string) error {
	return s.db.DeleteAttachmentsForTicket(ctx, ticketID)
}

// GCOrphans removes one user's never-claimed uploads older than olderThan
// ("" = everyone's; rows only, the blob sweep reclaims the bytes).
func (s *Attachments) GCOrphans(ctx context.Context, userID string, olderThan time.Duration) (int, error) {
	ids, err := s.db.UnclaimedAttachmentIDsBefore(ctx, userID, s.now().Add(-olderThan))
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	if err := s.db.DeleteAttachments(ctx, ids); err != nil {
		return 0, err
	}
	return len(ids), nil
}
