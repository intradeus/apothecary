package kinds

import (
	"strings"

	gitlabadapter "apothecary/internal/adapters/gitlab"
	"apothecary/internal/ports/trackerport"
)

// GitLabIssues is the ticketsource/gitlab kind: tickets imported from — kept
// in sync with, and, when a flow runs tracker actions, written back to —
// GitLab issues. Same token shape as the repository host kind; a separate
// integration because the account a team tracks issues under is not always
// the one that pushes code.
type GitLabIssues struct{}

const gitlabIssuesHelp = `Imports a GitLab issue as a ticket (title, description as markdown, and the files uploaded into the description). An issue's key is GitLab's full reference, group/project#123. GitLab's "work items" (issues, incidents, tasks) all come through its issues API, so any of them imports; epics do not. The ticket then MIRRORS the issue: title, description, state, type, labels and comments are re-read from GitLab every few minutes (not once its status is marked Done), after a stage that writes to the ticket, when a run starts, and on demand — edit them on GitLab, not in apothecary.

Statuses and types: an issue is open or closed, and that is this integration's whole status list (closed is pre-filled as Done and the done flow step, open as the todo step); its types are the work item types — Issue, Incident, Task, Test case — each mapped to a change type you pick.

What the New Ticket search box accepts:
• group/project#123 — that exact issue (a bare #123 works when exactly one project is listed below).
• any other words — a text search over the listed projects and groups, with GitLab's filter words lifted out, one word each: ~bug or label:bug, is:open / is:closed, assignee:@me or assignee:name, author:name, milestone:v1, type:incident.
• nothing — open issues assigned to you.

Reads (import and sync): GET /api/v4/user (Test), GET /api/v4/projects/:id/issues, /groups/:id/issues or /issues (search), GET /api/v4/projects/:id/issues/:iid, GET /api/v4/projects/:id/issues?iids[]=… (the sync, up to 100 issues of a project a request), GET /api/v4/projects/:id/issues/:iid/notes (comments), and GET /api/v4/projects/:id/uploads/:secret/:filename for each file the description links (GitLab 17.4+; a file that cannot be downloaded is skipped with a warning and its link kept).

Writes happen ONLY when a flow runs a tracker action on a ticket imported from here (or you change its status, labels or comments in apothecary):
• ticket_transition — PUT the issue's state_event: "to" moves to the status you gave that flow step (open reopens, closed closes); a step with no status skips the stage. "status" takes opened (or open) and closed verbatim.
• ticket_comment / ticket_link — POST /api/v4/projects/:id/issues/:iid/notes (a link is a note GitLab autolinks). A comment with attachments first uploads each file to POST /api/v4/projects/:id/uploads.
• ticket_set_field — PUT the issue: the writable fields are labels, assignees (usernames, via GET /users) and milestone (by title, the project's or a parent group's); GitLab issues have no custom fields.

Token: a personal, group or project access token with the api scope, Reporter or above on the projects (read_api is enough for importing; a flow's tracker action on such a ticket then fails saying so).`

func (GitLabIssues) Descriptor() Descriptor {
	return Descriptor{
		Port: "ticketsource", Kind: "gitlab", Label: "GitLab Issues",
		Help: gitlabIssuesHelp,
		Fields: []Field{
			{Name: "base_url", Label: "Base URL", Default: "https://gitlab.com", Required: true,
				Help: "Self-managed: https://<host> (no /api/v4)."},
			{Name: "token", Label: "Access token", Secret: true, Required: true,
				Help: "api scope to write back; read_api is enough to import."},
			{Name: "projects", Label: "Projects (optional)", Placeholder: "acme/app, acme/platform/api",
				Help: "Comma-separated project paths the search box is scoped to. With exactly one, a bare #123 resolves against it."},
			{Name: "groups", Label: "Groups (optional)", Placeholder: "acme/platform",
				Help: "Comma-separated group paths whose issues (subgroups included) the search box also covers."},
		},
	}
}

func (GitLabIssues) New(cfg Config) (trackerport.TicketSource, error) {
	return gitlabadapter.NewIssues(gitlabadapter.IssuesConfig{
		BaseURL:  cfg.Str("base_url", "https://gitlab.com"),
		Token:    cfg.Secrets["token"],
		Projects: splitList(cfg.Str("projects", "")),
		Groups:   splitList(cfg.Str("groups", "")),
	}), nil
}

func splitList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var _ TicketSourceKind = GitLabIssues{}
