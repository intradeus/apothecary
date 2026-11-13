# Tickets follow their tracker (TODO 49, 2026-11-01)

Built from docs/ticket-sync-plan.md, which has the why. A ticket used to be a
copy taken once ("Ticket sources", above); now **every ticket has exactly one
tracker** — its remote issue when it has a source, else its owner's internal
tracker — and every write goes there. What shipped, and where it differs from
the plan:

- **Data model** (migration `0021_ticket_tracker.sql`): per-integration
  `tracker_statuses` (name, remote category, position, `is_done`, `flow_step`
  — a step held by at most one status) and `tracker_types` (name,
  `change_type`); tickets gain `status_id`, `type_id`, `labels`, `remote_id`,
  `remote_updated_at`, `synced_at`, `sync_state` (`ok | error | gone`),
  `sync_error` and `status_changed_{at,by}`; `ticket_comments` (origin
  `remote | user | apothecary`, unique per remote id and per run+stage) and
  `ticket_links`; a unique index makes an issue one ticket per owner and
  tracker. `change_type` stays a column, derived from the type. A mirrored
  ticket refuses edits to title, goal, change type, status and labels through
  `PUT` (`confstore.RemoteOwnedError` → 409); the base branch stays editable.
- **Status and type lists.** Remote lists come from the new optional
  `trackerport.TicketMetadata` (Jira `/status` + `/issuetype`; GitHub
  open/closed and one type `issue`; GitLab opened/closed) or grow from the
  tickets a sync returns. A new status is auto-suggested
  (`confstore.EnsureRemoteStatuses`): category done ⇒ Done and the `done`
  step, category new ⇒ the `todo` step, each step only while free; a new
  type's change type is `GuessChangeType` of its name. On a remote tracker the
  user edits only Done, flow step and change type; on the internal one, the
  whole list (add, rename, reorder, delete — a status or type in use only with
  `replace_with`).
- **Jira's legacy `status_map` / `type_map`** stay in the integration's config
  through the migration (their rows need the tracker's ids). The first list
  refresh (`tickets.SyncLists`, `legacymaps.go`) applies them by name, case
  aside, after the fetched lists are stored: `status_map` (laid over the old
  defaults To Do / In Progress / In Review / Done; `step=` clears a step)
  gives each named status its flow step, taking it from the auto-suggested
  holder; `type_map` sets change types. Then the keys are dropped
  (`confstore.DropIntegrationConfig`, secrets untouched). An entry naming
  nothing on the tracker, or not parsing, is a warning — logged, and returned
  by `GET …/statuses?refresh=1` — and dropped all the same.
- **Flow steps replace canonical statuses.** `trackerport.TicketStatus` is
  `FlowStep`; `ticket_transition to: <step>` moves to whichever status holds
  the step, and a step no status holds skips the stage with a note naming the
  steps that have one. Adapters lost `TransitionTo` and their status maps.
- **The internal tracker** (`internal/project/tickets/internaltracker`) is a
  `TicketWriter` over the ticket row: one integration of kind `internal` per
  user (`confstore.KindInternal`), created on first use, seeded To do / In
  progress / In review / Done (done) / Blocked plus one type per change type,
  never created or deleted through the API. Transition by name
  (case-insensitive) or step; comments keyed by run+stage instead of trailer
  scanning; `labels` replace or append; any other field skips with a note
  (`ErrNotApplicable`); links upsert on URL. A ticket without a source is
  therefore no longer skipped by tracker stages — this narrows "Unlinked
  ticket ⇒ skip" from TODO 27.
- **Sync** (`tickets.Syncer`, single-flight per ticket). Triggers: the
  daemon's poll (`cmd/apothecaryd/background.go`, at boot then every 5 min)
  over `ListSyncable` — sourced, **not in a Done status**, not gone; a manual
  refresh of a ticket or of an integration (done ones included); after a
  ticket stage wrote to a remote tracker; before an agent stage that reads
  `run.ticket.comments`; and at run start. Done is a read-side filter: a done
  ticket reopened remotely is noticed only by a manual refresh or a run start.
  A snapshot older than `remote_updated_at` is dropped; a not-found key is
  retried by `remote_id` (a Jira move, a GitHub transfer) and otherwise the
  ticket is `gone` and polling stops. Engine refreshes are best-effort, 20 s
  each, and never fail a stage or refuse a run. A tracker refusing for its
  rate limit (`trackerport.ErrRateLimited`: a 429 on Jira, GitHub or GitLab,
  or GitHub's 403 with no requests remaining; the wait from `Retry-After` —
  seconds or an HTTP date — else the reset header) records nothing on the
  tickets: the pass stops reading, counts them `rate_limited`, and the poll
  skips the integration for that wait (the error backoff when none was
  given). A manual refresh that meets one sets the same hold.
- **Batched reads** (`trackerport.TicketSyncer.FetchMany`): Jira JQL
  `key in (…)` by 100, falling back to one `Fetch` per key when Jira rejects a
  batch naming a deleted key; GitHub one GraphQL query per repository with an
  aliased `issue(number:)` per key, by 100 (`/api/graphql` on Enterprise),
  mapped as the REST `Fetch` maps (the REST id is GraphQL's `databaseId`) —
  an unresolved number (deleted, transferred, a pull request) is left out and
  the sync fetches it alone; GitLab `iids[]=` by 100 per project.
- **Attachments follow too** (G14). An imported file keeps the tracker's id
  for it (`attachments.remote_id`, unique per ticket where non-empty; `''`
  for an upload), from the import on (`filestore.Attachments.SaveImported`).
  Every sync — a refresh by name and the poll's pass, which skips done
  tickets as for everything else — copies the files whose remote id the
  ticket does not hold yet, through the same code and so the same size cap
  and MIME allowlist as the import (`tickets.copyAttachments`), and claims
  them for the ticket; a held id is skipped, so a repeat sync copies nothing.
  A file the issue no longer lists is **kept**: runs pin attachment ids in
  `run_created`, and deleting the row would break them. A file with no
  remote id can't be matched, so only the import copies it. A ticket
  imported before remote ids were kept holds its files with `remote_id ''`:
  before downloading, the sync **adopts** one of them instead
  (`filestore.Attachments.AdoptRemote`, which sets the id under the unique
  index) — the oldest of the same stored name (case-sensitive) and the
  tracker's size, or, when the tracker gives no size, the only one of that
  name while only one new remote file has it; anything ambiguous is copied.
  A failed download, store or claim sets `sync_state=error` with the files
  named, but the mirror and the comments are written all the same; the sync
  is a `tickets.PartialSyncError`, which a pass counts `failed` (not
  `synced`), `Refresh` returns, and `POST /api/tickets/{id}/sync` answers
  with 200 and the ticket showing the error. The next pass retries the file,
  its id still not held (the poll does not back off for it); a file the
  upload policy refuses is only logged (the import already warned) and
  retried each pass too, so a raised limit takes it. No extra read: every
  `FetchMany` carries attachment metadata as `Fetch` does (the
  `TicketSyncer` contract — Jira asks for the `attachment` field, GitLab's
  list endpoint carries the description its uploads are parsed from, GitHub
  issues have no attachments).
- **One active run per ticket** (`engine.ErrTicketBusy`, checked in
  `CreateRunWith` before preflight under a per-ticket lock held until the run
  is queued). HTTP answers 409 with the busy run's `run_id` (ticket runs,
  merge-request runs, rerun); a cron automation slot on a busy ticket is
  marked fired with `last_error`, no catch-up. Runs without a ticket are
  exempt. Rerun starts from the ticket as it is now (`engine.Deps.Tickets`),
  falling back to the old run's copy when the ticket was deleted.
- **Deleting a ticket source** with linked tickets is 409 (`tickets`,
  `active_runs`) unless `?unlink_tickets=1`: then, in one transaction
  (`confstore.DeleteIntegrationUnlinking`), each ticket's source and remote
  fields are cleared and it moves to the internal tracker — the status of the
  same name (case aside), else the done step's if it was Done, else the todo
  step's; the type by change type; comments kept.
- **Prompts**: a new input `run.ticket.comments` (type `ticket_comments`,
  agents only; `limit` 1–200, default 20; `exclude_own`, default true, drops
  origin `apothecary` and remote comments carrying the engine's trailer).
  Rendered as `# TICKET DISCUSSION`, oldest first, every line quoted with
  `> `, marked untrusted, capped at 32 KiB (`flow.CommentsBudget`) by
  dropping the oldest. `# TASK` gains the ticket's key, status and labels.
- **API**: `POST /api/tickets/{id}/sync`, `/status`, `PUT …/labels`,
  `GET`/`POST …/comments`, `GET /api/tickets?done=`; `POST
  /api/integrations/{id}/sync`, `GET`/`PUT …/statuses` and `…/types`
  (`?refresh=1`).

### Where the build differs from the plan

- **GitLab types are the `issue_type` list** (issue, incident, task,
  test_case), not one `issue` type: collapsing them would lose
  incident → hotfix.
- **The refresh after a ticket stage runs only for a remote tracker**
  (`trackerTarget.remote`); the internal tracker wrote the row itself.
- **Backoff**: the poll leaves an integration alone for 15 minutes
  (`tickets.DefaultErrorBackoff`) after a failed pass, and **every** failed
  read — not only 401/403 — sets `sync_state=error` (a rate limit excepted,
  above). A manual refresh is never held back.
- **The run-start refresh is inside the engine** (`CreateRunWith` →
  `freshRunTicket`), taking title, goal, change type and the issue's key/URL
  from the refreshed row.
- **GitHub batches through GraphQL**, not `issues?since=`: listing by date
  pages through every issue updated since, while aliases read exactly the
  numbers asked for.
