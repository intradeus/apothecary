package kinds

import (
	"strings"

	githubadapter "apothecary/internal/adapters/github"
	"apothecary/internal/ports"
)

// GitHubIssues is the ticketsource/github kind: tickets imported from — and,
// when a flow runs tracker actions, written back to — GitHub Issues. Same
// token shape as the repository host kind; a separate integration because the
// account a team tracks issues under is not always the one that pushes code.
type GitHubIssues struct{}

const githubIssuesHelp = `Imports a GitHub issue as a ticket (title, body as markdown; GitHub issues carry no attachments as such — files in a body are plain links an agent can read). An issue's key is owner/repo#123.

What the New Ticket search box accepts:
• owner/repo#123 — that exact issue (a bare #123 works when exactly one repository is listed below).
• any other words — GitHub's issue search over the listed repositories (its own query syntax works: "label:bug is:open").
• nothing — open issues assigned to you.

Reads (import): GET /user (Test), GET /search/issues, GET /repos/{owner}/{repo}/issues/{n}.

Writes happen ONLY when a flow runs a tracker action on a ticket imported from here:
• ticket_transition — PATCH the issue's state: "to: todo" reopens, "to: done" closes (as completed); the other canonical statuses have no GitHub equivalent and skip the stage. "status" takes open or closed verbatim (Projects v2 columns are not reachable through the issue itself).
• ticket_comment / ticket_link — POST /repos/{owner}/{repo}/issues/{n}/comments (a link is a comment GitHub autolinks).
• ticket_set_field — PATCH the issue: the writable fields are labels, assignees and milestone (by title); GitHub issues have no custom fields.

Token: a fine-grained personal access token with Issues: Read and write on the repositories (Metadata: Read is implied), or a classic token with the repo scope. A read-only token is enough for importing; a flow's tracker action on such a ticket is refused at run creation with the integration named.`

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

func (GitHubIssues) New(cfg Config) (ports.TicketSource, error) {
	var repos []string
	for part := range strings.SplitSeq(cfg.Str("repos", ""), ",") {
		if p := strings.TrimSpace(part); p != "" {
			repos = append(repos, p)
		}
	}
	return githubadapter.NewIssues(githubadapter.IssuesConfig{
		BaseURL: cfg.Str("base_url", "https://api.github.com"),
		Token:   cfg.Secrets["token"],
		Repos:   repos,
	}), nil
}

var _ TicketSourceKind = GitHubIssues{}
