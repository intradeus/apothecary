package ports

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// SourceTicket is one unit of work as an EXTERNAL tracker reports it (Jira,
// GitHub Issues). It is deliberately the thin intersection every tracker can
// serve — identity, title, body, attachments — plus a few display-only fields
// the picker shows so a human can tell two similar issues apart. Everything
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

// TicketSource is the ticket-tracker READ boundary: what the ticket factory
// copies work in from. It is config-plane-shaped rather than run-shaped —
// the import path reads it, and nothing in a run touches it, so an
// unreachable tracker can never stall a run on its own.
//
// Writing back is a separate, OPTIONAL capability (TicketWriter): a flow
// opts into it stage by stage with a tracker action, and only those stages
// ever call it.
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

// TicketLink is one outbound link attached to a tracker issue (the run's
// merge request, typically).
type TicketLink struct {
	Title string
	URL   string
}

// TicketWriter is an OPTIONAL TicketSource capability: the write surface a
// flow's tracker actions use (FLOWLANG §2 action: ticket_transition,
// ticket_comment, ticket_set_field, ticket_link). Discovered by type
// assertion on the source adapter, so one integration — one credential —
// serves both the import and the write-back; an adapter without it simply
// cannot be the target of a tracker action, and run creation says so.
//
// Everything here speaks the tracker's OWN vocabulary (a status name, a
// field's display name, markdown that the adapter renders to vendor markup).
// No vendor types cross the boundary; what the engine needs to explain a
// refusal travels as the typed errors below, so a flow's $FAILURE can say
// "not reachable from X; reachable: A, B" without parsing vendor text.
//
// Idempotency is split: Transition and SetFields are read-compare-write and
// AddLink upserts on URL, so a re-entry after a crash is harmless; Comment
// is not — the engine records an intent first and reconciles through
// Comments by looking for the trailer it appended (the ReplyMarker pattern).
type TicketWriter interface {
	// Transition moves the issue to the named status (case-insensitive match
	// on the tracker's status or transition names). Already there ⇒ no-op.
	// Not reachable from the current status ⇒ *ErrStatusUnreachable.
	Transition(ctx context.Context, key, status string) error
	// Comment posts markdown as a new comment.
	Comment(ctx context.Context, key, body string) error
	// Comments lists the issue's comment bodies, oldest first, rendered to
	// markdown — the reconciliation read for a Comment whose completion was
	// not recorded.
	Comments(ctx context.Context, key string) ([]string, error)
	// SetFields writes fields addressed by display name (or raw id). The
	// adapter resolves names, coerces each value to the field's schema and
	// refuses what it cannot express (*ErrFieldUnknown, *ErrFieldUnsupported).
	SetFields(ctx context.Context, key string, fields map[string]string) error
	// Fields reads the current values of the named fields, each rendered as
	// the same comma-separated text SetFields accepts (append mode's read).
	// A field with no value maps to "".
	Fields(ctx context.Context, key string, names []string) (map[string]string, error)
	// AddLink attaches a link to the issue; idempotent on URL (an upsert).
	AddLink(ctx context.Context, key string, link TicketLink) error
}

// ErrStatusUnreachable: the status a flow names is not one the issue can
// move to from where it is. Reachable lists what is, so the message names
// the fix.
type ErrStatusUnreachable struct {
	Key, From, To string
	Reachable     []string
}

func (e *ErrStatusUnreachable) Error() string {
	if len(e.Reachable) == 0 {
		return fmt.Sprintf("%s: status %q is not reachable from %q (no transition is available)", e.Key, e.To, e.From)
	}
	return fmt.Sprintf("%s: status %q is not reachable from %q; reachable: %s", e.Key, e.To, e.From, strings.Join(e.Reachable, ", "))
}

// ErrFieldUnknown: no field of the tracker matches the name a flow wrote.
// Candidates carries near-misses (an ambiguous display name, say).
type ErrFieldUnknown struct {
	Key, Field string
	Candidates []string
}

func (e *ErrFieldUnknown) Error() string {
	if len(e.Candidates) == 0 {
		return fmt.Sprintf("%s: no field named %q", e.Key, e.Field)
	}
	return fmt.Sprintf("%s: field %q is ambiguous or unknown; candidates: %s", e.Key, e.Field, strings.Join(e.Candidates, ", "))
}

// ErrFieldUnsupported: the field exists but its type is one the adapter
// cannot write from a text value (a user picker, a date).
type ErrFieldUnsupported struct {
	Key, Field, Type string
	// Supported lists what the adapter can write, for trackers with a fixed
	// field set (GitHub: labels, assignees, milestone).
	Supported []string
}

func (e *ErrFieldUnsupported) Error() string {
	msg := fmt.Sprintf("%s: field %q cannot be set from text", e.Key, e.Field)
	if e.Type != "" {
		msg += fmt.Sprintf(" (its type is %s)", e.Type)
	}
	if len(e.Supported) > 0 {
		msg += "; supported: " + strings.Join(e.Supported, ", ")
	}
	return msg
}
