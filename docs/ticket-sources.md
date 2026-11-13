# Ticket sources

A ticket source is an optional integration (System → Integrations): Jira,
GitHub Issues or GitLab Issues. It lets you create a
ticket **from** a tracker issue — search the tracker on the New Ticket
form and pick an issue. The ticket then **follows the issue**: title,
description, status, type, labels and comments mirror the tracker and
are read-only in apothecary (the base branch stays yours). A sync
re-reads them every 5 minutes, skipping tickets in a Done status; a
ticket is also refreshed when a run starts on it, after a flow's tracker
stage writes to it, and on demand (Refresh on the ticket or the
integration — done tickets included). A ticket typed in by hand lives on
your **internal tracker** (one per user, created on first use, with
To do / In progress / In review / Done / Blocked).

Each tracker has a **status list** and a **type list** on its
integration page. Per status: a **Done** checkbox (done tickets drop out
of the sync and can be filtered out of the ticket list) and an optional **flow step** (todo,
in_progress, in_review, done, blocked — each held by at most one
status), which `ticket_transition to: <step>` moves to. Per type: its
change type (feat, fix, hotfix, perf, docs, chore), which names the
branch. Remote lists come from the tracker and only those columns are
yours; the internal tracker's lists are entirely editable.

What is written back to a remote tracker: what the tracker actions
(`ticket_transition`, `ticket_comment`, `ticket_set_field`,
`ticket_link` — see `docs/flow-language.md` §2) do, each as a stage the
flow's author placed, and a status, label or comment change you make on
the ticket page. On a hand-written ticket the same actions write to the
internal tracker (any field other than `labels` skips). Only **one run
per ticket** may be active at a time. Deleting a ticket source whose
tickets are still linked asks first, then moves them onto your internal
tracker (status by name, else Done/To do), keeping their comments.

> **GitHub Issues:** a fine-grained token with *Issues: read and write*
> on the repositories (read-only is enough to import). Keys are
> `owner/repo#123`; the only statuses are `open` and `closed` and the
> only type `issue`, the only writable fields `labels`, `assignees` and
> `milestone`.

> **GitLab Issues:** an access token with the `api` scope (`read_api`
> is enough to import). Keys are GitLab's full reference,
> `group/project#123`; GitLab's newer "work items" (issues, incidents,
> tasks) all import through its issues API, and those issue types
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
> the status and type lists), `read:comment:jira` (the comments the sync
> reads), `read:project:jira` (key resolution + `project = …` filters) and
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
> needs Jira's **Browse Projects** permission on the projects you import
> from, and the token can never see more than that human can. The
> reads, import and sync, and nothing else:
> `GET /myself` (the Test button), `POST /search/jql` (the picker's
> search, falling back to `GET /search` on older sites),
> `GET /issue/{KEY}?fields=…,labels,attachment` (preview, import, and a
> single ticket's refresh), a `GET` of each attachment's content URL,
> `GET /status` + `GET /issuetype` (the status and type lists), and the
> sync: the search with JQL `key in (…)`, up to 100 issues a request,
> then `GET /issue/{KEY}/comment` per refreshed ticket. The writes, each
> only when a flow's tracker action runs or you change the ticket's
> status, labels or comments: `GET /issue/{KEY}/transitions` +
> `POST /issue/{KEY}/transitions`, `POST /issue/{KEY}/comment` (with
> `POST /issue/{KEY}/attachments` first for a comment's files),
> `GET /field` + `PUT /issue/{KEY}`, and `POST /issue/{KEY}/remotelink`.
>
> Optionally set a **JQL search filter** (`project = PROJ`); it is ANDed
> into every search so the picker only shows issues you care about — it
> narrows what you see and cannot widen it. Then **Test**.
>
> The New Ticket search box takes a **bare number** (`2624` — resolved as
> `<PROJECT>-2624` against the filter's project, or against your most
> recently active projects when no filter is set), a **key**
> (`PROJ-2624`, also `proj 2624`), free **words** (text search), or
> `jql:…` for raw JQL.
