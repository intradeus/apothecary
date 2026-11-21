# Tickets and dashboards keep their history

Why: assistants now write and rewrite tickets and analytics dashboards for
a person, and a save overwrote what was there. Someone who disliked what an
assistant (or a teammate) saved could not go back. Flows and skillsets
already kept versions; tickets, dashboards and scorecards did not.

What now:

- **The skillset pattern, not the flow one.** `0017_versions.sql` adds
  `ticket_versions`, `analytics_dashboard_versions` and
  `analytics_scorecard_versions`: a per-object `seq`, immutable rows,
  `ON DELETE CASCADE` to the parent, as `skillset_versions` does. The
  content-addressed `flow_versions` key, `(project_id, hash)`, would make
  "a restore appends a version equal to an old one" impossible. Every row
  that existed when the migration ran got a seq-1 `backfill` version.
- **The row stays the object.** Nothing references a version: runs,
  automations and run start keep reading the `tickets` row (and the
  dashboard and scorecard rows) as before; no column or foreign key names a
  version. History is for people to read and restore from. A version's
  `type_id` is plain text with no FK, so an old version never holds a
  tracker type in place (`ReplaceTrackerTypes` still deletes it).
- **What is versioned.** A ticket's title, goal, base branch, change type,
  type and labels — not its status, comments, links or attachments. A
  dashboard's name, filters and panels; a scorecard's name and terms.
- **Deduplicated against the latest version, in the write.** The store
  appends `INSERT … SELECT` from the stored row, in the same transaction as
  the row write, only when it differs from the latest version (a row-wise
  `IS DISTINCT FROM`; jsonb equality for the jsonb columns, so key order
  is no change). A version is always what was stored, a save with the same
  content appends nothing, and A→B→A is three versions. No hash column is
  needed. The row write holds the parent's row lock, so two writers take
  their seqs one after the other; the primary key is the last line of
  defence and its violation is `ErrConflict` (409). `SetTicketLabels`,
  one statement before, is now a transaction like the others.
- **Who made it.** `author_kind` is `user` (a person; `author_id` their
  user id), `assistant` (an assistant's fill the person saved — the save
  body's `assisted` — or an act they confirmed from an assistant's
  request; `author_id` the person), `run` (a flow's tracker action;
  `author_id` the run), `sync` (what was seen of a remote issue;
  `author_id` the ticket-source integration) or `backfill`.
  `restored_from` is the seq a restore copied. A confirmed assistant act
  is marked on its context (`confstore.WithAssistedChange`, set by
  `decideAssistantRequest` before it runs the tool's route), so a ticket
  or dashboard write it makes — labels through the internal tracker
  included — is authored `assistant`. A person's label change on a remote
  issue goes to its tracker and comes back through sync, so it is
  authored `sync`.
- **Remote-tracker tickets: refused, not written back.** A record's title,
  goal, type and labels are its tracker's (`UpdateTicket` already kept only
  the base branch), and `trackerport.TicketWriter` has no write for a title
  or a description. Writing back would mean extending every adapter, so a
  restore on a record is `ErrInvalid` (400), naming the tracker and saying
  to change the issue there. A refused restore changes nothing, so the next
  sync has nothing to undo. Records still get a history (`RecordTicket`
  and `SeeTicket` append `sync` versions when what was seen changed; a
  base-branch edit a `user` one), readable but not restorable. History of a
  remote issue starts when its record is made.
- **Restore writes directly, after a comparison and a confirmation.** The
  History panels show the old version next to the current content, and
  "Restore this version" asks to confirm, then `POST
  …/versions/{seq}/restore` saves it. We did not load the old content into
  the edit form for the person to save, as the assistant's fill does: the
  person who confirms has just seen both side by side; the restore is one
  atomic, permission-checked operation, the same for the UI, the API and
  the MCP/assistant `request` path, and recorded as a restore
  (`restored_from`); it is undone by restoring the version before it; and
  no half-applied form state is left behind. The panel is offered only
  outside edit mode, so no unsaved edit is lost. A restore goes through the
  normal update path: a ticket is re-validated and placed on its tracker
  (a type no longer there gives way to its change type's; a base branch git
  refuses is 400), a dashboard runs `checkDashboard` (a panel that no
  longer compiles is 400, nothing appended), a built-in is 409. Restoring
  content equal to the current one appends nothing.
- **Retention: the newest 100 per object.** Pruned in the transaction that
  inserts a version. Sync can append a version for every change seen on a
  busy remote issue, and a dashboard version holds up to 30 panels, so
  "keep everything" was not bounded. Seqs are never reused ("v37" stays
  v37); the oldest versions, the creation or backfill one included, age
  out. Lowering the bound later is safe; raising it is not retroactive.
- **Permissions are the object's.** The ticket routes are `ScopeProject`
  like the other ticket routes: `ticket:read` lists and reads, `ticket:write`
  restores. The dashboard and scorecard routes are `ScopeDashboard` /
  `ScopeScorecard` (the owner's): `analytics:read` reads,
  `analytics:write` restores. A built-in has no versions. The MCP face gets
  `list_/get_/restore_ticket_version`, `…_dashboard_version` and
  `…_scorecard_version`; the restores are among the `request` tool's write
  tools.
- **Order is content.** The grid shows panels, and the filter bar filters,
  in array order, and a reorder is a version (jsonb arrays compare in
  order). So the list's summary names `filters` for a filter reorder and
  `panels` with `reordered` for a panel one (`panelChanges`: the panels
  both versions hold, in another order), and the History comparison gives
  the version's order when the items both hold stand differently; it never
  says "same" before a restore that would move things.
- **A canceled fill is not an assistant save.** `assisted` comes from the
  page's fill guard; Cancel settles it, so a later manual save in the same
  visit is the person's. A version picked in History is shown only when
  its answer is the last one asked for, so Restore posts what is shown.

Tests that hold it:

- `conftest` "ticket versions" (memory and Postgres): a changed write
  appends, an identical one and a status move do not; labels and seen
  writes carry their author and equal the row; newest first, by seq;
  missing is `ErrNotFound`; versions go with their ticket; 101 changes keep
  100 and never reuse a seq; a version holds no type in place; a restore
  appends v1's content by the restorer with `restored_from`, and restoring
  the current content appends nothing; a removed type falls back to the
  change type's; a record's history is `sync` and `user`, its restore is
  `ErrInvalid` and changes nothing.
- `analyticstest` "dashboard versions" / "scorecard versions" (Postgres)
  and `internal/analytics/service_test.go`: the same for dashboards and
  scorecards, jsonb key order, panel added/removed/changed counts, a
  panel and a filter reorder, built-ins, content that no longer checks.
- `pgstore/faults_test.go`: the new reads in the dead-database table; a
  sabotaged `ticket_versions` fails the ticket write as a whole; malformed
  version rows are errors. `migrate_test.go` `TestVersionsMigration`: one
  backfill version per existing row, equal to it, gone with its parent.
- `internaltracker_test.go`: a labels write is a version by the run, the
  person, or the assistant on a confirmed act.
- `httpapi` `ticket_versions_test.go`: list, get, restore; a viewer reads
  but may not restore (403), a stranger gets 404; a remote issue lists
  none without a record, refuses a restore (400) and keeps its history;
  `assisted` and a confirmed `update_ticket` author `assistant`.
  `analytics_test.go` `TestAnalyticsVersions`: the dashboard and scorecard
  routes, another person's history refused, built-ins.
- CPUI: `TicketHistory.test.ts` and `DashboardHistory.test.ts` (loading,
  empty, error and content; each author kind; the comparison, order
  included; restore confirmed, declined and failing; no Restore on the
  newest version; a remote record's and a reader's Restore disabled with
  the reason; a slower earlier pick never replaces the last one);
  `TicketPage.test.ts` and `Dashboard.test.ts` (the panel in the read view
  only, hidden on a built-in, refreshed after a restore); `TicketForm` and
  `Dashboard` send `assisted` after a fill only, not after a canceled one;
  the comparison helpers in `domain/ticket.test.ts` and
  `domain/analytics.test.ts`.
