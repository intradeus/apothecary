// Package trackerport is the issue-tracker port (Jira, GitHub Issues,
// GitLab issues, and Apothecary's own internal tracker): the thin
// intersection of what every tracker serves when a ticket is imported and
// kept in sync, the change type that names a ticket's branch, the flow steps
// a run moves a ticket through, the status and type lists a tracker offers,
// and writing back — status, fields, comments, attachments.
//
// Every internal ticket has exactly one tracker: its remote issue when it was
// imported, the built-in internal tracker otherwise. The remote is the single
// source of truth for a sourced ticket — the internal row is a mirror that a
// sync overwrites — so everything here is shaped for reading a tracker
// repeatedly (stable ids, comment identity, batched fetch) as well as for
// writing to it.
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
// apothecary-specific (project, base branch, flow) is decided on import; a
// tracker never dictates them. The sync copies the tracker-owned fields
// (title, body, status, type, labels) onto the internal ticket on every pass.
//
// Body is plain markdown: adapters convert their vendor's markup (Jira's ADF)
// before it crosses this boundary, so nothing downstream parses vendor JSON.
type SourceTicket struct {
	// RemoteID is the tracker's STABLE internal id (Jira's numeric issue id,
	// a GitHub/GitLab issue's global id). Unlike Key it survives a Jira
	// project move or a GitHub transfer, so a sync that gets a 404 on the key
	// retries by it (Fetch accepts either where the tracker does).
	RemoteID string
	// Key is the tracker's human-facing identifier ("PROJ-123") — what the
	// operator searches for and what the imported ticket records as its
	// provenance.
	Key   string
	Title string
	Body  string
	// URL is the issue's browser page (the "open in Jira" link).
	URL string
	// Status is the status's display name; StatusID the tracker's id for it
	// (Jira's status id; GitHub "open"/"closed"; GitLab "opened"/"closed"),
	// which the sync resolves against the integration's status list
	// (RemoteStatus.ID) so a renamed status still matches.
	Status   string
	StatusID string
	// Type is the issue type's display name; TypeID its id (Jira's issuetype
	// id; "issue" for GitHub/GitLab), resolved like StatusID against the
	// integration's type list (RemoteType.ID), which carries the change type.
	Type   string
	TypeID string
	// Labels are the issue's labels as the tracker spells them.
	Labels []string
	// Assignee/Updated are display context; Updated also guards a sync
	// against applying a snapshot older than the one it already holds.
	Assignee    string
	Updated     time.Time
	Attachments []SourceAttachment
}

// ChangeType is a CANONICAL kind of change — what a ticket is: a feature, a
// fix, an urgent fix to production, a performance change, documentation,
// upkeep. A ticket carries one, and its base branch is named after it
// (feat/proj-123-…). The set is fixed, like FlowStep, so a flow can rely
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

// TicketSource is the ticket-tracker READ boundary: what the ticket factory
// imports work from and what the sync re-reads to keep an imported ticket's
// mirror current. It is config-plane-shaped rather than run-shaped — import
// and sync read it, and nothing in a run blocks on it, so an unreachable
// tracker can never stall a run on its own.
//
// Writing back is a separate, OPTIONAL capability (TicketWriter): a flow
// opts into it stage by stage with a tracker action, and only those stages
// ever call it. Listing the tracker's statuses and types (TicketMetadata)
// and batched re-reads (TicketSyncer) are optional too.
type TicketSource interface {
	// Test probes credentials (the UI's "Test" button).
	Test(ctx context.Context) error
	// Search returns tickets matching a free-text query, most recently updated
	// first. An empty query means "what should I be working on" — the adapter's
	// own sensible default. Attachments may be omitted here; Fetch is
	// authoritative for those.
	Search(ctx context.Context, query string, limit int) ([]SourceTicket, error)
	// Fetch returns one ticket by key, WITH its attachment metadata. Where
	// the tracker addresses an issue by its stable id too (Jira), Fetch takes
	// SourceTicket.RemoteID as well. An issue the tracker does not have (a
	// 404) ⇒ an error wrapping ErrTicketNotFound.
	Fetch(ctx context.Context, key string) (SourceTicket, error)
	// OpenAttachment streams one attachment's bytes; the caller closes.
	OpenAttachment(ctx context.Context, a SourceAttachment) (io.ReadCloser, error)
}

// ErrTicketNotFound: the tracker has no issue by that key (a 404). Fetch
// wraps it (errors.Is), so the sync can tell a moved or deleted issue —
// retry by RemoteID, else mark the mirror gone — from a tracker that is
// merely unreachable.
var ErrTicketNotFound = errors.New("ticket not found")

// ErrRateLimited: the tracker refused the call for its rate limit (an HTTP
// 429, or GitHub's 403 with no requests remaining). RetryAfter is how long
// it asked to be left alone — 0 when it did not say. The sync skips the
// integration for that long instead of recording the tickets in error
// (errors.As).
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
// "done". It only pre-fills an integration's status list (Done checked,
// the todo/done flow steps); the user's columns decide afterwards.
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
// its statuses and issue types, which fill the integration's status and
// type lists (where the user marks Done, picks each flow step's status and
// each type's change type). Jira reads them from the instance; GitHub and
// GitLab issues have a fixed open/closed pair and a single "issue" type.
type TicketMetadata interface {
	Statuses(ctx context.Context) ([]RemoteStatus, error)
	Types(ctx context.Context) ([]RemoteType, error)
}

// TicketSyncer is an OPTIONAL TicketSource capability: re-read many tickets
// in few calls, for the periodic sync. Each ticket is what Fetch returns,
// attachment metadata INCLUDED — unlike Search — since the sync copies the
// files an issue gained from it (Jira asks for the attachment field, GitLab
// lists the description's uploads, GitHub issues have none). The result is
// in no particular order; a key absent from it was not found (the caller
// falls back to Fetch to tell a moved issue from a deleted one). A tracker
// without it is synced with one Fetch per ticket.
type TicketSyncer interface {
	FetchMany(ctx context.Context, keys []string) ([]SourceTicket, error)
}

// FlowStep is a CANONICAL step of a ticket's lifecycle: one of the few
// points a flow moves work through, whatever the tracker calls them. A flow
// names these (ticket_transition's `to`), so the same flow moves a Jira
// issue to "In Progress" and closes a GitHub issue without spelling either.
// Each tracker integration's status list says which of its statuses holds
// each step (at most one per step); the caller — not the adapter — resolves
// the step to that status's name and moves there with Transition. A step no
// status holds is *ErrStatusUnsupported, which the engine turns into a
// skipped stage rather than a failure. A tracker's literal status name
// still travels through Transition when a flow wants exactly that word.
type FlowStep string

const (
	StepTodo       FlowStep = "todo"
	StepInProgress FlowStep = "in_progress"
	StepInReview   FlowStep = "in_review"
	StepDone       FlowStep = "done"
	StepBlocked    FlowStep = "blocked"
)

// FlowSteps lists every FlowStep, in lifecycle order.
func FlowSteps() []FlowStep {
	return []FlowStep{StepTodo, StepInProgress, StepInReview, StepDone, StepBlocked}
}

// ParseFlowStep reads a flow step name; ok is false for anything else (a
// tracker's own status name belongs to Transition, not here).
func ParseFlowStep(s string) (FlowStep, bool) {
	for _, st := range FlowSteps() {
		if string(st) == strings.TrimSpace(s) {
			return st, true
		}
	}
	return "", false
}

// StepNames renders steps for a message ("todo, done").
func StepNames(list []FlowStep) string {
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

// CommentLister reads an issue's comments, oldest first: the sync's
// comment mirror and the reconciliation read for a Comment whose completion
// was not recorded.
type CommentLister interface {
	ListComments(ctx context.Context, key string) ([]Comment, error)
}

// TicketWriter is the write surface a flow's tracker actions use (FLOWLANG
// §2 action: ticket_transition, ticket_comment, ticket_set_field,
// ticket_link, ticket_link_branch). Every ticket has one: a remote source
// adapter offers it as an OPTIONAL capability (discovered by type assertion,
// so one integration — one credential — serves import, sync and
// write-back), and a ticket without a remote source writes to the internal
// tracker, which implements it over the ticket row.
//
// Everything here speaks the tracker's OWN vocabulary (a status name, a
// field's display name, markdown that the adapter renders to vendor markup);
// a flow step is resolved to a status name by the caller, from the
// integration's status list. No vendor types cross the boundary; what the
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

// ErrStatusUnsupported: no status of the ticket's tracker holds the flow
// step a flow named — GitHub Issues has nothing for "in review" unless the
// user gives that step a status. Supported lists the steps that do have a
// status, so a flow author can tell "this tracker cannot say it" from "the
// workflow forbids it" (ErrStatusUnreachable). Raised by the caller that
// resolves the step against the integration's status list, not by an
// adapter.
type ErrStatusUnsupported struct {
	Key       string
	Step      FlowStep
	Supported []FlowStep
}

func (e *ErrStatusUnsupported) Error() string {
	msg := fmt.Sprintf("%s: the tracker has no status for step %q", e.Key, string(e.Step))
	if len(e.Supported) > 0 {
		msg += "; steps with a status: " + StepNames(e.Supported)
	}
	return msg
}

// ErrNotApplicable: the write has no meaning on this ticket's tracker — a
// field the internal tracker does not have, a ticket deleted mid-run. Not a
// failure: the engine completes the stage as skipped with Reason, as for
// *ErrStatusUnsupported.
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
