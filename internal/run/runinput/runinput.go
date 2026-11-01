// Package runinput is what starts a run: the ticket it works on and the
// per-attempt input around it. Whoever creates runs speaks it — the HTTP API,
// the automations, the engine — so it stands apart from all of them, and an
// automation needs nothing of the engine to say what it wants started.
package runinput

import (
	"time"

	"apothecary/internal/skills"
)

// Ticket is the work a run does.
type Ticket struct {
	// TicketID is the parent ticket this run belongs to. When set, Branch is
	// treated as the ticket's BASE branch and the run gets its own unique
	// actual branch = base + "-" + <run-id tail>. Empty (direct/test path) =
	// legacy behavior: Branch is used verbatim / generated with the run id.
	TicketID string
	Title    string
	Goal     string
	// ChangeType is the ticket's kind of change (trackerport.ChangeType), which `run_if: run.ticket.change_type == fix` reads; "" is feat.
	ChangeType string
	// Branch is the base branch when TicketID is set, else the run's branch
	// override ("" = the default apothecary/<run-id>-<title-slug>). Names git
	// check-ref-format would refuse are rejected.
	Branch string
	// SourceBranch is the branch this run forks off from (the base of
	// `git switch -c <branch> origin/<SourceBranch>`). Per-run; "" defaults to
	// the project's default branch. Names git check-ref-format would refuse are
	// rejected.
	SourceBranch string
	// RunGoal is extra direction for THIS attempt, additive to Goal (which
	// stays the enduring spec). Rendered as its own prompt section.
	RunGoal string
	// AttachmentIDs are uploaded files attached to THIS RUN: claimed for the
	// run and delivered into its workspace.
	AttachmentIDs []string
	// TicketAttachmentIDs are the parent ticket's attachments — owned by the
	// ticket, delivered to every run of it. Snapshotted onto run_created;
	// NOT associated to this run.
	TicketAttachmentIDs []string
	// Source* snapshot the tracker issue the ticket was imported from — the
	// ticketsource integration id, its vendor, the issue key and its page —
	// so a flow's tracker actions (FLOWLANG §2) know what to write to. All
	// empty for a ticket written by hand: those stages then skip.
	SourceID   string
	SourceKind string
	SourceKey  string
	SourceURL  string
}

// TicketNow is a ticket's tracker-owned facts as an agent's prompt shows
// them, read when the prompt is built rather than when the run started: its
// key on the tracker ("" for a ticket written here), the name of its status
// ("" before its first sync), its labels, and its comments, oldest first.
type TicketNow struct {
	Key      string
	Status   string
	Labels   []string
	Comments []TicketComment
}

// TicketComment is one comment on a ticket. Own is one apothecary posted to
// the internal tracker (origin apothecary); one it posted to a remote
// tracker comes back from a sync as anybody's, and is known by its trailer.
type TicketComment struct {
	Author string
	Body   string
	At     time.Time
	Own    bool
}

// Input is the optional per-attempt input when starting a run from a
// ticket or restarting one: a flow override plus this attempt's direction and
// attachments.
type Input struct {
	Flow string // "" = the ticket/original run's flow
	Goal string // extra direction for this attempt
	// SourceBranch overrides the branch the restarted run forks off from;
	// "" reuses the original run's source branch.
	SourceBranch  string
	AttachmentIDs []string
	// Params override the project's parameter bindings for this attempt;
	// By records who created the run on run_created ("" = a person,
	// "automation" for an automation).
	Params map[string]string
	By     string
	// MR starts the run on a merge request someone opened, for a flow with
	// operates_on: merge_request: Repo is the checkout it belongs to ("" =
	// the primary), IID its number.
	MR *MR
	// Skillsets are the skillsets to install into the workspace, each at a
	// version (0 = latest). On a rerun, nil means the original run's,
	// re-pinned to their latest; an empty non-nil list means none.
	Skillsets []skills.Ref
}

// MR names a merge request to create a run on.
type MR struct {
	Repo string `json:"repo,omitempty"`
	IID  int    `json:"iid"`
}
