package ports

import (
	"context"
	"io"
	"time"
)

// SourceTicket is one unit of work as an EXTERNAL tracker reports it (Jira in
// v1). It is deliberately the thin intersection every tracker can serve —
// identity, title, body, attachments — plus a few display-only fields the
// picker shows so a human can tell two similar issues apart. Everything
// apothecary-specific (project, base branch, flow) is decided on import; a
// tracker never dictates them.
//
// Body is plain markdown: adapters convert their vendor's markup (Jira's ADF)
// before it crosses this boundary, so nothing downstream parses vendor JSON.
type SourceTicket struct {
	// Key is the tracker's stable human-facing identifier ("PROJ-123") — what
	// the operator searches for and what the imported ticket records as its
	// provenance.
	Key   string
	Title string
	Body  string
	// URL is the issue's browser page (the "open in Jira" link).
	URL string
	// Status/Type/Assignee/Updated are display-only search-result context.
	Status      string
	Type        string
	Assignee    string
	Updated     time.Time
	Attachments []SourceAttachment
}

// SourceAttachment is one file hanging off a source ticket. URL is the
// vendor's download endpoint, opaque to the engine and only ever handed back
// to the adapter that produced it (OpenAttachment).
type SourceAttachment struct {
	ID       string
	Filename string
	Mime     string
	Size     int64
	URL      string
}

// TicketSource is the read-only ticket-tracker boundary: the engine never
// writes back (no status transitions, no comments), it only copies work in.
// It is config-plane-shaped rather than run-shaped — nothing in a run touches
// it, so an unreachable tracker can never stall a run.
type TicketSource interface {
	// Test probes credentials (the UI's "Test" button).
	Test(ctx context.Context) error
	// Search returns tickets matching a free-text query, most recently updated
	// first. An empty query means "what should I be working on" — the adapter's
	// own sensible default. Attachments may be omitted here; Fetch is
	// authoritative for those.
	Search(ctx context.Context, query string, limit int) ([]SourceTicket, error)
	// Fetch returns one ticket by key, WITH its attachment metadata.
	Fetch(ctx context.Context, key string) (SourceTicket, error)
	// OpenAttachment streams one attachment's bytes; the caller closes.
	OpenAttachment(ctx context.Context, a SourceAttachment) (io.ReadCloser, error)
}
