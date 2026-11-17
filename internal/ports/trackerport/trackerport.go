// Package trackerport is the issue-tracker port (Jira, GitHub Issues,
// GitLab issues, and Apothecary's own internal tracker): the thin
// intersection of what every tracker serves when a workshop lists or opens
// its tickets, the change type that names a ticket's branch, the status and
// type lists a tracker offers, and writing back — status, fields, comments,
// attachments.
//
// A workshop has exactly one ticket source: a remote tracker or its owner's
// internal tracker. A remote issue is read live (paged, filtered search;
// fetch by key or stable id) and never mirrored locally — Apothecary keeps
// only a small record of the issues its runs point at — so everything here is
// shaped for reading a tracker on demand as well as for writing to it.
package trackerport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/ports/repoport"
)

// SourceTicket is one unit of work as an EXTERNAL tracker reports it (Jira,
// GitHub Issues). It is deliberately the thin intersection every tracker can
// serve — identity, title, body, state, attachments. Everything
// apothecary-specific (project, base branch, flow) is decided locally; a
// tracker never dictates them. The tracker-owned fields (title, body,
// status, type, labels) are read live and never stored.
//
// Body is plain markdown: adapters convert their vendor's markup (Jira's ADF)
// before it crosses this boundary, so nothing downstream parses vendor JSON.
type SourceTicket struct {
	// RemoteID is the tracker's STABLE internal id (Jira's numeric issue id,
	// a GitHub/GitLab issue's global id). Unlike Key it survives a Jira
	// project move or a GitHub transfer, so a read that gets a 404 on the key
	// retries by it — only on a tracker that can fetch by it
	// (TicketByRemoteID).
	RemoteID string
	// Key is the tracker's human-facing identifier ("PROJ-123") — what the
	// operator searches for and what a ticket's record keeps as its last-seen
	// key.
	Key   string
	Title string
	Body  string
	// URL is the issue's browser page (the "open in Jira" link).
	URL string
	// Status is the status's display name; StatusID the tracker's id for it
	// (Jira's status id; GitHub "open"/"closed"; GitLab "opened"/"closed"),
	// resolved against the integration's status list (RemoteStatus.ID) so a
	// renamed status still matches.
	Status   string
	StatusID string
	// Done: the tracker files the status as done (Jira's statusCategory
	// Done; a closed GitHub/GitLab issue).
	Done bool
	// Type is the issue type's display name; TypeID its id (Jira's issuetype
	// id; "issue" for GitHub/GitLab), resolved like StatusID against the
	// integration's type list (RemoteType.ID), which carries the change type.
	Type   string
	TypeID string
	// Labels are the issue's labels as the tracker spells them.
	Labels []string
	// Assignee/Updated are display context.
	Assignee    string
	Updated     time.Time
	Attachments []SourceAttachment
}

// ChangeType is a CANONICAL kind of change — what a ticket is: a feature, a
// fix, an urgent fix to production, a performance change, documentation,
// upkeep. A ticket carries one, and its base branch is named after it
// (feat/proj-123-…). The set is fixed, so a flow can rely
// on it whatever the tracker; each tracker's own issue types ("Bug",
// "Story", "Spike") are mapped onto it by the integration's type list, a
// new row of which GuessChangeType pre-fills.
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

// TicketSource is the ticket-tracker READ boundary: what a workshop's ticket
// list, ticket page, tracker_query automations and a run's start read live.
// It is config-plane-shaped rather than run-shaped — a run's start falls
// back to a cached copy or the ticket's record, so an unreachable tracker can
// never stall a run on its own.
//
// Writing back is a separate, OPTIONAL capability (TicketWriter): a flow
// opts into it stage by stage with a tracker action, and only those stages
// ever call it. Listing the tracker's statuses and types (TicketMetadata)
// is optional too.
type TicketSource interface {
	// Test probes credentials (the UI's "Test" button).
	Test(ctx context.Context) error
	// Search returns one page of tickets matching q, most recently updated
	// first. Attachments may be omitted here; Fetch is authoritative for
	// those.
	Search(ctx context.Context, q SearchQuery) (SearchPage, error)
	// Fetch returns one ticket by key, WITH its attachment metadata. An
	// issue the tracker does not have (a 404) ⇒ an error wrapping
	// ErrTicketNotFound. Fetch is never handed a RemoteID: on GitHub and
	// GitLab a bare number is an issue NUMBER, which a global id would alias.
	Fetch(ctx context.Context, key string) (SourceTicket, error)
	// OpenAttachment streams one attachment's bytes; the caller closes.
	OpenAttachment(ctx context.Context, a SourceAttachment) (io.ReadCloser, error)
}

// TicketByRemoteID is the OPTIONAL capability of a tracker that addresses an
// issue by its stable id as well as by its key (Jira: /issue/{idOrKey}). A
// live read whose stored key is gone (a project move) retries through it; a
// tracker without it (GitHub, GitLab: a bare number is an issue number there,
// not a global id) gets no retry, so a missing key cannot alias an unrelated
// issue.
type TicketByRemoteID interface {
	// FetchByRemoteID is Fetch by SourceTicket.RemoteID; the answer carries
	// the issue's CURRENT key. None ⇒ an error wrapping ErrTicketNotFound.
	FetchByRemoteID(ctx context.Context, remoteID string) (SourceTicket, error)
}

// DoneFilter narrows a search by the tracker's own notion of done (Jira
// statusCategory Done; closed on GitHub/GitLab).
type DoneFilter string

const (
	DoneAny  DoneFilter = ""     // both
	DoneOpen DoneFilter = "open" // not done
	DoneOnly DoneFilter = "done" // done only
)

// SearchQuery is one page request of TicketSource.Search.
type SearchQuery struct {
	// Text is a free-text query; "" means "what should I be working on" —
	// the adapter's own default (assigned to me), with no done clause of its
	// own: Done carries that.
	Text string
	Done DoneFilter
	// Cursor is opaque, from SearchPage.Next; "" is the first page.
	Cursor string
	// Limit is the page size; <= 0 is the adapter's default.
	Limit int
}

// SearchPage is one page of TicketSource.Search.
type SearchPage struct {
	Tickets []SourceTicket
	// Next is the cursor of the following page; "" on the last one.
	Next string
}

// ErrTicketNotFound: the tracker has no issue by that key (a 404). Fetch
// wraps it (errors.Is), so a live read can tell a moved or deleted issue —
// retry by RemoteID where the tracker can (TicketByRemoteID) — from a
// tracker that is merely unreachable.
var ErrTicketNotFound = errors.New("ticket not found")

// ErrRateLimited: the tracker refused the call for its rate limit (an HTTP
// 429, or GitHub's 403 with no requests remaining). RetryAfter is how long
// it asked to be left alone — 0 when it did not say. The live reads hold the
// integration for that long, serving what they have cached (errors.As).
type ErrRateLimited struct {
	// Tracker names who refused ("jira GET /rest/api/3/search"), Detail what
	// it said, for the message.
	Tracker, Detail string
	RetryAfter      time.Duration
}

func (e *ErrRateLimited) Error() string {
	msg := e.Tracker + ": rate limited"
	if e.Detail != "" {
		msg = e.Tracker + ": " + e.Detail
	}
	if e.RetryAfter > 0 {
		msg += fmt.Sprintf("; retry after %s", e.RetryAfter.Round(time.Second))
	}
	return msg
}

// RetryAfter reads a Retry-After header — delay seconds or an HTTP date —
// as a duration from now; 0 when it is absent, unreadable or past.
func RetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if secs, err := strconv.Atoi(value); err == nil {
		return max(time.Duration(secs)*time.Second, 0)
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(at.Sub(now), 0)
	}
	return 0
}

// ResetAfter reads a unix-seconds reset header (GitHub's and GitLab's
// RateLimit-Reset) as a duration from now; 0 when absent, unreadable or past.
func ResetAfter(value string, now time.Time) time.Duration {
	secs, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0
	}
	return max(time.Unix(secs, 0).Sub(now), 0)
}

// StatusCategory is the coarse bucket a tracker files a status under —
// Jira's statusCategory key; GitHub/GitLab's open state is "new", closed
// "done". A remote ticket's Done is read from it; the live status list
// carries it for display.
type StatusCategory string

const (
	CategoryNew           StatusCategory = "new"
	CategoryIndeterminate StatusCategory = "indeterminate"
	CategoryDone          StatusCategory = "done"
	// CategoryUnknown: the tracker did not say.
	CategoryUnknown StatusCategory = ""
)

// RemoteStatus is one status a tracker offers. ID is what SourceTicket's
// StatusID carries, Name what Transition takes.
type RemoteStatus struct {
	ID       string
	Name     string
	Category StatusCategory
}

// RemoteType is one issue type a tracker offers. ID is what SourceTicket's
// TypeID carries.
type RemoteType struct {
	ID   string
	Name string
}

// TicketMetadata is an OPTIONAL TicketSource capability: the tracker lists
// its statuses (read live for the ticket page and the status bindings) and
// issue types (which fill the integration's type list, where the user maps
// each to its change type). Jira reads them from the instance; GitHub and
// GitLab issues have a fixed open/closed pair and a single "issue" type.
type TicketMetadata interface {
	Statuses(ctx context.Context) ([]RemoteStatus, error)
	Types(ctx context.Context) ([]RemoteType, error)
}

// TicketLink is one outbound link attached to a tracker issue (the run's
// merge request, typically).
type TicketLink struct {
	Title string
	URL   string
}

// Comment is one comment on an issue. ID is the tracker's id for it (stable
// across edits), Body markdown (adapters render vendor markup, as for
// SourceTicket.Body). Updated equals Created for a comment never edited.
type Comment struct {
	ID      string
	Author  string
	Body    string
	Created time.Time
	Updated time.Time
}

// CommentLister reads an issue's comments, oldest first: what a prompt and
// the ticket page show, and the reconciliation read for a Comment whose
// completion was not recorded.
type CommentLister interface {
	ListComments(ctx context.Context, key string) ([]Comment, error)
}

// TicketWriter is the write surface a flow's tracker actions use (FLOWLANG
// §2 action: ticket_transition, ticket_comment, ticket_set_field,
// ticket_link, ticket_link_branch). Every ticket has one: a remote source
// adapter offers it as an OPTIONAL capability (discovered by type assertion,
// so one integration — one credential — serves reads and write-back), and
// the internal tracker implements it over the ticket row.
//
// Everything here speaks the tracker's OWN vocabulary (a status name, a
// field's display name, markdown that the adapter renders to vendor markup);
// a flow names a status literally (or through a string parameter its
// workshop binds). No vendor types cross the boundary; what the
// engine needs to explain a refusal travels as the typed errors below, so a
// flow's $FAILURE can say "not reachable from X; reachable: A, B" without
// parsing vendor text.
//
// Idempotency is split: Transition and SetFields are read-compare-write and
// AddLink upserts on URL, so a re-entry after a crash is harmless; Comment
// is not — the engine records an intent first and reconciles through
// ListComments by looking for the trailer it appended (the ReplyMarker
// pattern).
type TicketWriter interface {
	// Transition moves the issue to the named status (case-insensitive match
	// on the tracker's status or transition names). Already there ⇒ no-op.
	// Not reachable from the current status ⇒ *ErrStatusUnreachable.
	Transition(ctx context.Context, key, status string) error
	// Comment posts markdown as a new comment.
	Comment(ctx context.Context, key, body string) error
	CommentLister
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

// ErrNotApplicable: the write has no meaning on this ticket's tracker — a
// field the internal tracker does not have, a ticket deleted mid-run. Not a
// failure: the engine completes the stage as skipped with Reason.
type ErrNotApplicable struct{ Key, Reason string }

func (e *ErrNotApplicable) Error() string { return e.Key + ": " + e.Reason }

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
