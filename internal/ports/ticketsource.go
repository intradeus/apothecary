package ports

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"apothecary/internal/ports/repoport"
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
	// ChangeType is the adapter's reading of Type as a canonical change type,
	// set when its integration maps that type (Jira's type map); empty, the
	// importer guesses from Type (GuessChangeType).
	ChangeType ChangeType
}

// ChangeType is a CANONICAL kind of change — what a ticket is: a feature, a
// fix, an urgent fix to production, a performance change, documentation,
// upkeep. A ticket carries one, and its base branch is named after it
// (feat/proj-123-…). The set is fixed, like TicketStatus, so a flow can rely
// on it whatever the tracker; each tracker's own issue types ("Bug",
// "Story", "Spike") are mapped onto it — by the integration's type map where
// it has one, else by GuessChangeType.
type ChangeType string

const (
	ChangeFeat   ChangeType = "feat"
	ChangeFix    ChangeType = "fix"
	ChangeHotfix ChangeType = "hotfix"
	ChangePerf   ChangeType = "perf"
	ChangeDocs   ChangeType = "docs"
	ChangeChore  ChangeType = "chore"
)

// ChangeTypes lists every ChangeType; the first is the default.
func ChangeTypes() []ChangeType {
	return []ChangeType{ChangeFeat, ChangeFix, ChangeHotfix, ChangePerf, ChangeDocs, ChangeChore}
}

// ParseChangeType reads a canonical change type; ok is false for anything
// else.
func ParseChangeType(s string) (ChangeType, bool) {
	for _, c := range ChangeTypes() {
		if string(c) == strings.ToLower(strings.TrimSpace(s)) {
			return c, true
		}
	}
	return "", false
}

// GuessChangeType reads a tracker's issue type with no mapping for it: an
// incident is a hotfix, a defect a fix, performance perf, documentation docs,
// upkeep a chore, anything else a feature. The first rule whose word the type
// contains wins.
func GuessChangeType(issueType string) ChangeType {
	t := strings.ToLower(strings.TrimSpace(issueType))
	for _, r := range guessRules {
		for _, w := range r.words {
			if strings.Contains(t, w) {
				return r.ct
			}
		}
	}
	return ChangeFeat
}

var guessRules = []struct {
	ct    ChangeType
	words []string
}{
	{ChangeHotfix, []string{"hotfix", "incident"}},
	{ChangeFix, []string{"bug", "defect", "problem"}},
	{ChangePerf, []string{"perf"}},
	{ChangeDocs, []string{"doc"}},
	{ChangeChore, []string{"chore", "maintenance", "debt"}},
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

// TicketStatus is a CANONICAL status: one of the few states of a piece of
// work that every tracker has some word for. A flow names these
// (ticket_transition's `to`), so the same flow moves a Jira issue to
// "In Progress" and reopens a GitHub issue without spelling either; each
// adapter maps them to its tracker's own vocabulary — Jira through the
// integration's status map, GitHub Issues through its two states — and says
// which ones it has no word for (*ErrStatusUnsupported), which the engine
// turns into a skipped stage rather than a failure. A tracker's literal
// status name still travels through Transition when a flow wants exactly
// that word.
type TicketStatus string

const (
	StatusTodo       TicketStatus = "todo"
	StatusInProgress TicketStatus = "in_progress"
	StatusInReview   TicketStatus = "in_review"
	StatusDone       TicketStatus = "done"
	StatusBlocked    TicketStatus = "blocked"
)

// CanonicalStatuses lists every TicketStatus, in lifecycle order.
func CanonicalStatuses() []TicketStatus {
	return []TicketStatus{StatusTodo, StatusInProgress, StatusInReview, StatusDone, StatusBlocked}
}

// ParseTicketStatus reads a canonical status name; ok is false for anything
// else (a tracker's own status name belongs to Transition, not here).
func ParseTicketStatus(s string) (TicketStatus, bool) {
	for _, st := range CanonicalStatuses() {
		if string(st) == strings.TrimSpace(s) {
			return st, true
		}
	}
	return "", false
}

// StatusNames renders statuses for a message ("todo, done").
func StatusNames(list []TicketStatus) string {
	names := make([]string, len(list))
	for i, st := range list {
		names[i] = string(st)
	}
	return strings.Join(names, ", ")
}

// TicketLink is one outbound link attached to a tracker issue (the run's
// merge request, typically).
type TicketLink struct {
	Title string
	URL   string
}

// TicketWriter is an OPTIONAL TicketSource capability: the write surface a
// flow's tracker actions use (FLOWLANG §2 action: ticket_transition,
// ticket_comment, ticket_set_field, ticket_link, ticket_link_branch).
// Discovered by type assertion on the source adapter, so one integration —
// one credential — serves both the import and the write-back; an adapter
// without it simply cannot be the target of a tracker action, and run
// creation says so.
//
// Everything here speaks the tracker's OWN vocabulary (a status name, a
// field's display name, markdown that the adapter renders to vendor markup)
// — except TransitionTo, which takes a canonical TicketStatus and lets the
// adapter find the tracker's word for it, so one flow fits every tracker.
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
	// TransitionTo moves the issue to the tracker's own status for a
	// canonical one. The tracker has no status for it ⇒ *ErrStatusUnsupported
	// (the engine skips the stage); it has one but the workflow does not
	// allow the move from here ⇒ *ErrStatusUnreachable, as Transition.
	TransitionTo(ctx context.Context, key string, to TicketStatus) error
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

// TicketAttacher is an OPTIONAL TicketWriter capability: the tracker hosts a
// file for a comment to show (ticket_comment's attachments). Jira attaches
// it to the issue; GitHub Issues uploads it beside the repository's own. A
// tracker without it cannot take a comment with attachments, and the stage
// fails saying so.
type TicketAttacher interface {
	// Attach uploads one file for the issue and says how a comment links it.
	Attach(ctx context.Context, key, name, contentType string, data []byte) (repoport.Attachment, error)
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

// ErrStatusUnsupported: the tracker has no status that means the canonical
// one a flow named — GitHub Issues knows nothing of "in review". Supported
// lists the canonical statuses it does have a word for, so a flow author
// can tell "this tracker cannot say it" from "the workflow forbids it"
// (ErrStatusUnreachable). Kind names the tracker for the message.
type ErrStatusUnsupported struct {
	Key       string
	Status    TicketStatus
	Kind      string
	Supported []TicketStatus
}

func (e *ErrStatusUnsupported) Error() string {
	kind := e.Kind
	if kind == "" {
		kind = "the tracker"
	}
	msg := fmt.Sprintf("%s: %s has no status for %q", e.Key, kind, string(e.Status))
	if len(e.Supported) > 0 {
		msg += "; it has: " + StatusNames(e.Supported)
	}
	return msg
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
