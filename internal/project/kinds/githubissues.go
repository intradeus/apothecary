package kinds

import (
	githubadapter "apothecary/internal/adapters/github"
	"apothecary/internal/ports/trackerport"
)

// GitHubIssues is the ticketsource/github kind: GitHub Issues as the
// tracker a workshop's tickets are read from, live — and, when a flow runs
// tracker actions, written back to. Same token shape as the repository host
// kind; a separate integration because the account a team tracks issues
// under is not always the one that pushes code.
type GitHubIssues struct{}

const githubIssuesHelp = `A workshop whose ticket source is this integration lists and opens GitHub issues live (title, body as markdown; GitHub issues carry no attachments as such — files in a body are plain links an agent can read). An issue's key is owner/repo#123. Title, body, state, labels and comments are read from GitHub when you look (briefly cached), not copied — edit them on GitHub, not in apothecary.

Statuses and types: an issue is open or closed (closed is done), and that is this integration's whole status list; it has one type, Issue, whose change type you pick.

What the ticket search box accepts:
• owner/repo#123 — that exact issue (a bare #123 works when exactly one repository is listed below).
• any other words — GitHub's issue search over the listed repositories (its own query syntax works: "label:bug").
• nothing — issues assigned to you.
The list's open/done filter adds state:open or state:closed. GitHub's search serves at most 1000 results a query.

Reads: GET /user (Test), GET /search/issues (the list, a page at a time, and tracker_query automations), GET /repos/{owner}/{repo}/issues/{n} (one issue), GET /repos/{owner}/{repo}/issues/{n}/comments.

Writes happen ONLY when a flow runs a tracker action on a ticket from here (or you change its status, labels or comments in apothecary):
• ticket_transition — PATCH the issue's state: "to" takes open (reopens) or closed (closes as completed) verbatim, or a string parameter the workshop binds; an issue already there is left alone (Projects v2 columns are not reachable through the issue itself).
• ticket_comment / ticket_link — POST /repos/{owner}/{repo}/issues/{n}/comments (a link is a comment GitHub autolinks). A comment with attachments first uploads each file to POST https://uploads.github.com/user-attachments/assets (after GET /repos/{owner}/{repo} for its id) — github.com only, and only with a classic token.
• ticket_set_field — PATCH the issue: the writable fields are labels, assignees and milestone (by title); GitHub issues have no custom fields.

Token: a fine-grained personal access token with Issues: Read and write on the repositories (Metadata: Read is implied), or a classic token with the repo scope. A read-only token is enough for reading; a flow's tracker action on such a ticket is refused at run creation with the integration named.`

func (GitHubIssues) Descriptor() Descriptor {
	return Descriptor{
		Port: "ticketsource", Kind: "github", Label: "GitHub Issues",
		Help: githubIssuesHelp,
		Fields: []Field{
			{Name: "base_url", Label: "API base URL", Default: "https://api.github.com", Required: true,
				Help: "GitHub Enterprise: https://<host>/api/v3."},
			{Name: "token", Label: "Personal access token", Secret: true, Required: true,
				Help: "Fine-grained: Issues read/write on the repositories. Classic: repo."},
			{Name: "repos", Label: "Repositories (optional)", Placeholder: "acme/app, acme/docs",
				Help: "Comma-separated owner/repo list the search box is scoped to. With exactly one, a bare #123 resolves against it."},
		},
	}
}

func (GitHubIssues) New(cfg Config) (trackerport.TicketSource, error) {
	return githubadapter.NewIssues(githubadapter.IssuesConfig{
		BaseURL: cfg.Str("base_url", "https://api.github.com"),
		Token:   cfg.Secrets["token"],
		Repos:   splitList(cfg.Str("repos", "")),
	}), nil
}

var _ TicketSourceKind = GitHubIssues{}
