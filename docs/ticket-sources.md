# Ticket sources

Every workshop has exactly **one ticket source**, chosen on its settings page:
a remote tracker integration (System → Integrations: Jira, GitHub Issues or
GitLab Issues) or your **internal tracker** (one per user, created on first
use, with To do / In progress / In review / Done / Blocked). A new workshop
starts on the internal tracker. Several workshops may share one source: they
see the same tickets.

**A remote source is read live, never copied.** The workshop's ticket list is
the tracker's list (by default what the tracker shows as assigned to you; a
done filter, open by default, and a search box narrow it), and a ticket page is
the issue as it is now: title, description, status, type, labels, comments and
files come from the tracker. Title, description and type are edited there;
a status, label or comment changed here is written to the tracker. Reads go through one
in-memory cache per source, shared by every workshop on it: a list page is
served for 60 seconds, an issue, its comments and the status and type lists
for 5 minutes, and an older copy is served while one background read replaces
it. **Refresh** on the list or the ticket reads the tracker again at once; so
does a run's start (and an agent's prompt, for the comments), falling back to
the copy held when the tracker fails. A write from Apothecary drops the
copies it changes. When the tracker answers with its rate limit, the source
is held for the wait it asked (a minute when it did not say): copies are
served at any age, and a page with nothing cached says when to retry. One
daemon is assumed; a restart empties the cache.

**The record.** Runs, the one-active-run lock, attachments and a base branch
need a local row, so Apothecary keeps a small **record** of a remote issue,
made the first time something needs it — a run starts, an automation fires,
someone sets a base branch — and never polled. It is unique by source and the
tracker's stable id (not the key, so a moved Jira issue keeps its record) and
belongs to no workshop: every workshop on that source shares it, and a ticket
has at most one active run across all of them. It keeps what was last seen
(key, URL, title, description, type, labels) for pages that cannot reach the
tracker, and no status: the status is always read live.

**The internal tracker** is the one source whose tickets are Apothecary's own.
Only a workshop on it shows **New ticket**; a workshop on a remote source shows
a link to open the tracker instead, and its tickets are made there. Its
tickets' title, goal, status, type, labels and comments are edited here.

**Base branch.** A ticket has no stored branch unless you set one: each run
names its branch by **its own workshop's** `repo.branch_format`, so two
workshops sharing a source name the same ticket's branches each their way.
A base branch set on the ticket (on a remote one, the only field editable
here; setting it makes the record) is used by every workshop instead. Tickets
from before live sources keep the branch they stored, which now counts as
set by you: clear it to follow the workshop's format.

**Statuses and types.** Each source has a status list and a type list on its
integration page. A remote tracker's statuses are read from it and are not
editable; the internal tracker's are (name, order, **Done**). Per type: its
change type (feat, fix, hotfix, perf, docs, chore), which names the branch
(`{ticket_type}`), feeds `run_if: run.ticket.change_type` and analytics;
remote types are read from the tracker, their change types stay yours.

**Status parameters to bind.** `ticket_transition` moves the ticket to a
status named in the flow or by a string parameter the workshop binds
(`docs/flow-language.md` §2). The built-in `builder` and `hands-on` flows read
four, with no default: `todo_status`, `in_progress_status`,
`in_review_status` and `done_status`. Bind each to your source's own status
name (Workshop → Flows → Configure): `In Progress` / `In Review` / `Done` on
Jira, or `Ready for prod` for a team whose Done is a deploy away; `open` and
`closed` on GitHub Issues, `opened` and `closed` on GitLab Issues; the
internal tracker's `To do`, `In progress`, `In review`, `Done`. Until all four
are bound, runs of those flows (and their automations' firings) are refused,
naming the parameters. A status the tracker cannot reach from where the issue
is fails the stage naming the ones it can; one it is already in does nothing.

What is written back to a remote tracker: what the tracker actions
(`ticket_transition`, `ticket_comment`, `ticket_set_field`, `ticket_link`,
`ticket_link_branch`) do, each as a stage the flow's author placed, and a
status, label or comment change you make on the ticket page. On an internal
ticket the same actions write to the internal tracker (any field other than
`labels` skips).

**Automations.** A `tracker_query` automation polls its workshop's ticket
source, every couple of minutes; each new issue gets a record and one run.
It names no source of its own: switch the workshop's source and it polls the
new one.

**Switching a workshop's source** hides the old source's tickets from that
workshop's list. Their records and runs are kept, and a run's link to its
ticket still opens it. **Deleting a source** that a workshop uses is refused,
naming the workshops; switch them first. A source no workshop uses deletes;
its records stay, showing what was last seen.

> **GitHub Issues:** a fine-grained token with *Issues: read and write*
> on the repositories (read-only is enough to read the list and start runs). Keys are
> `owner/repo#123`; the only statuses are `open` and `closed` and the
> only type `issue`, the only writable fields `labels`, `assignees` and
> `milestone`.

> **GitLab Issues:** an access token with the `api` scope (`read_api`
> is enough to read). Keys are GitLab's full reference,
> `group/project#123`; GitLab's newer "work items" (issues, incidents,
> tasks) all come through its issues API, and those issue types
> (issue, incident, task, test case) are its type list. The only
> statuses are `opened` and `closed`, the only writable fields
> `labels`, `assignees` and `milestone`; files uploaded into the
> description come along.

> **Which Jira?** *Cloud* is the Atlassian-hosted one, at
> `<site>.atlassian.net` — credential is an **account email + API
> token**, API version **3**. *Data Center* (and the retired *Server*)
> is self-hosted on your own domain — credential is a **personal access
> token** with the email field left **empty**, API version **2**. Both
> are the same form; the email field is what selects between basic and
> bearer auth.
>
> *Cloud token:* sign in as the account whose issues you want to see →
> <https://id.atlassian.com/manage-profile/security/api-tokens> →
> **Create API token** — the plain, **unscoped** one. (A token made with
> *Create API token with scopes* is only accepted on Atlassian's
> `api.atlassian.com` gateway; against your `<site>.atlassian.net` URL it
> returns `401 Client must be authenticated to access this resource`.)
> An unscoped token is no wider than the human: it inherits that
> account's permissions, and this integration only ever issues the
> reads listed below unless a flow of yours writes. If you do scope one (for the gateway), the read
> scopes are `read:jira-user` and `read:jira-work` — those two are
> exactly enough. If your site's picker shows granular scopes instead, the set
> that matches what this integration reads is `read:user:jira`
> (`/myself` + the assignee name), `read:issue-details:jira`,
> `read:issue:jira`, `read:issue-meta:jira` (the issue), `read:field:jira`,
> `read:status:jira`, `read:issue-type:jira` (the requested fields and
> the status and type lists), `read:comment:jira` (the comments a ticket
> page and a prompt read), `read:project:jira` (key resolution + `project = …` filters) and
> `read:attachment:jira` (the files). Atlassian also documents
> `read:avatar:jira` and `read:group:jira` on those endpoints because the
> *responses* carry avatar URLs and group memberships — nothing here
> reads either, so start without them and add only what a refused call
> names (Jira's message is surfaced verbatim). Grant `write:jira-work`
> (granular: `write:issue:jira`, `write:comment:jira`,
> `write:attachment:jira`, `write:issue.remote-link:jira`, plus
> `read:field:jira`) **only** if your flows run tracker actions or you
> change tickets from apothecary; never `manage:` or `delete:`. Label +
> expiry → copy it (shown once) and paste it with that account's email.
> Revoke on the same page.
>
> *Data Center token:* profile menu → **Profile → Personal Access
> Tokens** → **Create token** → label + expiry → copy. Paste into *API
> token*, leave *Account email* empty. DC PATs have **no scope picker** —
> the token carries the full permissions of the account that minted it,
> so use a service account for least privilege. (If your admin disabled
> PATs, a username + password in the email/token fields works too —
> that path is plain basic auth.)
>
> Scopes only cap a token; they grant nothing. Either way the account
> needs Jira's **Browse Projects** permission on the projects you work
> from, and the token can never see more than that human can. The
> reads, and nothing else:
> `GET /myself` (the Test button), `POST /search/jql` (a workshop's ticket
> list and its search, falling back to `GET /search` on older sites),
> `GET /issue/{KEY}?fields=…,labels,attachment` (a ticket page and a
> run's start), a `GET` of each attachment's content URL,
> `GET /status` + `GET /issuetype` (the status and type lists), and
> `GET /issue/{KEY}/comment` (a ticket page's and a prompt's comments),
> each answered from the cache within its lifetime unless a refresh or a run's start reads again. The writes, each
> only when a flow's tracker action runs or you change the ticket's
> status, labels or comments: `GET /issue/{KEY}/transitions` +
> `POST /issue/{KEY}/transitions`, `POST /issue/{KEY}/comment` (with
> `POST /issue/{KEY}/attachments` first for a comment's files),
> `GET /field` + `PUT /issue/{KEY}`, and `POST /issue/{KEY}/remotelink`.
>
> Optionally set a **JQL search filter** (`project = PROJ`); it is ANDed
> into every search so the ticket list only shows issues you care about — it
> narrows what you see and cannot widen it. Then **Test**.
>
> The ticket list's search box takes a **bare number** (`2624` — resolved as
> `<PROJECT>-2624` against the filter's project, or against your most
> recently active projects when no filter is set), a **key**
> (`PROJ-2624`, also `proj 2624`), free **words** (text search), or
> `jql:…` for raw JQL.
