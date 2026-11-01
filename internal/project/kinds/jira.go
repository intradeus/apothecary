package kinds

import (
	jiraadapter "apothecary/internal/adapters/jira"
	"apothecary/internal/ports/trackerport"
)

// Jira is the ticketsource/jira kind: the tracker tickets are imported FROM
// and kept in sync with (an imported ticket mirrors its issue) — and, when a
// flow runs tracker actions (FLOWLANG §2 ticket_*), written back to: a
// transition, a comment, a field, a remote link to the merge request.
// Nothing writes unless a flow (or the user, on the ticket page) says so.
type Jira struct{}

// jiraHelp is the whole "what do I paste here" answer, shown above the form.
// Jira comes in two deployments with different credentials, and picking the
// wrong one fails with an unhelpful 401, so both recipes are spelled out —
// including the exact token scopes, since Atlassian's scoped-token page offers
// dozens and every extra one is access apothecary provably never uses.
const jiraHelp = `Import and sync: an imported ticket MIRRORS its issue — summary, description, status, type, labels and comments are re-read from Jira every few minutes (not once the status is one you marked Done), after a stage that writes to the ticket, when a run starts, and on demand. Jira is the source of truth: edit those in Jira, not in apothecary.

Statuses and types: this integration's status and type lists are fetched from your site. For each status you tick Done (the work is over; the ticket leaves the poll) and pick which flow step it is, if any (todo, in_progress, in_review, done, blocked — each used once); for each type you pick its change type. New lists are pre-filled: a status in Jira's Done category is ticked Done and holds the done step, one in the To Do category the todo step.

Write-back happens ONLY when a flow runs a tracker action on a ticket imported from here — each one a stage the flow's author placed:
• ticket_transition — moves the issue: "to" names a flow step, which moves to the status you gave that step, "status" a status or transition as your workflow spells it. A move the workflow does not allow fails the stage naming the ones it does; a step no status holds skips the stage.
• ticket_comment — posts a markdown artifact as a comment, ending with a short apothecary trailer (run + stage).
• ticket_set_field — writes one field by its DISPLAY NAME ("Feature flags"): text, paragraph, number, select, labels, components, versions, priority. Replace or append (comma-separated items merged).
• ticket_link — attaches the run's merge request(s) as remote issue links.
• ticket_link_branch — attaches the run's pushed branch as a remote issue link. (Jira's own Development panel needs no write: it lists a branch whose name carries the issue key — feat/proj-123-… is how an imported ticket's branch is named — once your GitHub/GitLab is connected to Jira.)

Each imported issue's type becomes the ticket's change type — feat, fix, hotfix, perf, docs or chore — which its branch is named after: as the type list says. A type new to the list is pre-filled from its name (Hotfix, Incident → hotfix; Bug, Defect → fix; Performance → perf; Documentation → docs; Chore, Maintenance, Tech Debt → chore; anything else → feat).
A flow with none of these never writes. A token without write permissions still imports fine; a run of a flow WITH tracker actions on such a ticket fails at creation with the integration named.

Which Jira is this?
• Cloud — Atlassian hosts it, your site is <something>.atlassian.net. Credential: account email + API token. API version 3.
• Data Center / Server — self-hosted on your own domain. Credential: personal access token with the email left EMPTY. API version 2.

What the New Ticket search box accepts:
• 2624 — a bare number, resolved the way Jira's own quick search does: tried as <PROJECT>-2624 against the project(s) named in the search filter below, or against the account's most recently active projects when no filter is set.
• PROJ-2624 (also "proj 2624" / "PROJ_2624") — looked up as that exact issue.
• any other words — a text search over summary, description and comments.
• jql:… — the rest is passed to Jira as raw JQL, e.g. jql:status = "In Review" AND assignee = currentUser()

EXACTLY what this integration calls — the reads (import and sync):
1. GET /rest/api/3/myself — the Test button only; proves the credential authenticates.
2. POST /rest/api/3/search/jql (older sites: GET /rest/api/3/search) — the search box on the New Ticket form.
3. GET /rest/api/3/issue/{KEY}?fields=summary,description,status,issuetype,assignee,updated,labels,attachment — the preview, the import, and a single ticket's refresh.
4. GET the attachment content URL returned by (3) — copies the files.
5. GET /rest/api/3/status and GET /rest/api/3/issuetype — fill the status and type lists.
6. The sync: the search of (2) with JQL key in (…), up to 100 issues a request, then GET /issue/{KEY}/comment for each ticket it refreshes.
And the writes, each only when a flow runs the named tracker action (or you change the ticket's status, labels or comments in apothecary):
7. ticket_transition: GET /issue/{KEY}?fields=status, GET /issue/{KEY}/transitions, POST /issue/{KEY}/transitions.
8. ticket_comment: POST /issue/{KEY}/comment; with attachments, POST /issue/{KEY}/attachments for each file first.
9. ticket_set_field: GET /field (name → id, once per run), GET /issue/{KEY}?fields=… (append mode), PUT /issue/{KEY}.
10. ticket_link, ticket_link_branch: POST /issue/{KEY}/remotelink.

CLOUD — create the API token:
1. Sign in as the account whose issues you want to see, then open https://id.atlassian.com/manage-profile/security/api-tokens
2. Use "Create API token" — the plain, UNSCOPED one. Atlassian accepts it on your own site URL, which is what this integration calls.
   A token created with "Create API token with scopes" is only accepted on Atlassian's api.atlassian.com gateway; against https://<site>.atlassian.net it comes back 401 "Client must be authenticated to access this resource". If that is the 401 you are looking at, swap it for an unscoped token.
   An unscoped token is not a wider grant than the human holds: it inherits that account's permissions exactly (see the Browse Projects note below), and this integration only ever issues the reads listed above unless a flow of yours writes.
3. If you do use a scoped token (against the gateway), select READ scopes for the import, and WRITE scopes only if your flows run tracker actions —
   • Classic scopes: read:jira-user (call 1) and read:jira-work (calls 2-6) are exactly enough to import and sync; add write:jira-work for calls 7-10.
   • Granular scopes (if the picker shows those instead), one line per thing this integration actually reads:
       read:user:jira          — call 1 (/myself, the Test button) and the assignee name in search results
       read:issue-details:jira, read:issue:jira, read:issue-meta:jira — the issue itself: summary, description, updated
       read:field:jira, read:status:jira, read:issue-type:jira        — the named fields requested in calls 2-3, and the lists of call 5
       read:comment:jira       — the comments of call 6
       read:project:jira       — resolving keys and any "project = …" search filter
       read:attachment:jira    — call 4
     Atlassian also lists read:avatar:jira and read:group:jira on these endpoints because the RESPONSES carry avatar URLs and group memberships — this integration never reads either field, so start without them. If your site enforces the full documented list, the refused call names the missing scope and apothecary prints that message verbatim; add exactly what it names.
     For the writes (calls 7-10), only when your flows use tracker actions: write:issue:jira (transitions + fields), write:comment:jira (comments), write:attachment:jira (a comment's attachments), write:issue.remote-link:jira (links), plus read:field:jira for resolving a field's display name.
   Never select manage: or delete: anything, and no write: scope unless a flow of yours runs a tracker action — a scope apothecary never uses only widens the blast radius of a leaked token.
4. Label it (e.g. apothecary), set an expiry, Create, and copy it — it is shown once. Paste it below with that account's email address. Revoke it on the same page.
Scopes cap the token; they do not grant anything. The account itself still needs the Jira "Browse Projects" permission on every project you want to import from, and the token can never see more than the human can.
Atlassian's granular list per endpoint is long and moves; if a call is refused, Jira names the missing scope in its error and apothecary shows that message verbatim.

DATA CENTER / SERVER — create the personal access token:
1. Profile menu → Profile → Personal Access Tokens → "Create token".
2. Label it, set an expiry, Create, copy it.
3. Paste it into "API token" and leave "Account email" EMPTY (that is what switches the adapter to bearer auth).
Data Center PATs have NO scope picker — a PAT carries the full permissions of the account that made it. To get least privilege there, mint it from a service account that only has "Browse Projects" on the relevant projects.
(If your admin disabled PATs, a username + password in the email/token fields works too — that path is plain basic auth.)

Then press Test: it runs call 1 and reports whether the credential authenticates.`

func (Jira) Descriptor() Descriptor {
	return Descriptor{
		Port: "ticketsource", Kind: "jira", Label: "Jira",
		Help: jiraHelp,
		Fields: []Field{
			{
				Name: "base_url", Label: "Site URL", Required: true,
				Placeholder: "https://acme.atlassian.net",
				Help:        "The site root, not an API path — no /rest/api and no trailing /browse/….",
			},
			{
				Name: "email", Label: "Account email", Placeholder: "you@acme.com",
				Help: "Jira Cloud only. Leave empty for a Data Center personal access token.",
			},
			{
				Name: "api_token", Label: "API token", Secret: true, Required: true,
				Help: "Cloud: https://id.atlassian.com/manage-profile/security/api-tokens — scopes read:jira-user + read:jira-work to import, plus write:jira-work only if your flows run tracker actions. Data Center: Profile → Personal Access Tokens (no scopes; it inherits the account's permissions).",
			},
			{
				// Named "search filter" rather than "scope": on Atlassian, scope
				// means the token's permissions, and one word for two things
				// sends people to the wrong page.
				Name: "jql", Label: "Search filter (JQL, optional)", Placeholder: "project = PROJ",
				Help: "ANDed into every search, so the picker only ever shows issues you care about. Not a permission — it narrows what you see, it cannot widen it. Naming a project here also makes a bare-number search (\"2624\") resolve in one request.",
			},
			{
				Name: "api_version", Label: "API version", Default: "3",
				Help: "3 for Cloud, 2 for Data Center / Server.",
			},
		},
	}
}

func (Jira) New(cfg Config) (trackerport.TicketSource, error) {
	return jiraadapter.New(jiraadapter.Config{
		BaseURL:    cfg.Str("base_url", ""),
		Email:      cfg.Str("email", ""),
		Token:      cfg.Secrets["api_token"],
		JQL:        cfg.Str("jql", ""),
		APIVersion: cfg.Str("api_version", "3"),
	}), nil
}

var _ TicketSourceKind = Jira{}
