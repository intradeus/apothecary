# Live tickets: one source per workshop, no sync

Supersedes `2026-11-01-tickets-follow-their-tracker.md` and the plan it was
built from, `docs/ticket-sync-plan.md` (left as written, a plan no longer
current). Current behaviour: `docs/ticket-sources.md`, and
`docs/flow-language.md` §2 for `ticket_transition`.

## Why

Every remote issue was copied into a `tickets` row and a 5-minute poll kept
the copy current. The copy brought a mirror, status lists keyed by the
tracker's ids, a mapping from statuses to flow steps, sync states and errors,
and rate-limit holds. The tracker is read live instead, and only the minimum
is kept locally.

## What now

- **One source per workshop** (`projects.ticket_source_id`, NOT NULL, a
  `ticketsource` integration): a remote tracker or the owner's internal
  tracker (the default for a new workshop). Several workshops may share one.
  The ticket list, the New ticket form and `tracker_query` automations use
  it; automations have no `source_id` of their own any more.
- **Live reads** through `tickets.Live` over `internal/platform/livecache`: one
  cache group per integration, list pages 60 s, an issue, its comments and the
  status and type lists 5 min, stale served while one background read
  replaces it, single-flight, a rate limit holding the integration for the
  wait asked (1 min when unsaid). A refresh and a run's start bypass the cache
  and fall back to the copy held. A write through the tracker writer drops
  the copies it changes. One daemon is assumed (chart `replicas: 1`).
- **The record:** a remote issue's `tickets` row is made when something needs
  it (a run, an automation firing, a base branch, a cron automation saved
  with its `ticket_key`), unique by `(source_id, remote_id)`, owned by no
  workshop, never polled. It keeps the last-seen key, URL, title, goal, type
  and labels. Stored comments (`ticket_comments`) are an internal ticket's
  only: the config store refuses them on a record (`ErrInvalid`).
- **`ticket_transition` has no built-in status names.** `to:` is required
  free text, the tracker's own status or transition name, or a string
  parameter the workshop binds (`to: $done_status`), which may wait for a
  binding as a model or a harness does. Already there is a no-op;
  unreachable fails `input_invalid` naming the reachable statuses. The step
  enum, `status:`, `trackerport.FlowStep`, `tracker_statuses.flow_step` and
  the skip when no status held a step are gone (grammar changed in place, no
  upgrader). The built-in `builder` and `hands-on` read `todo_status`,
  `in_progress_status`, `in_review_status` and `done_status`.
- **Removed:** the sync loop and its routes, mirror and legacy maps, remote
  status rows and remote comments in Postgres, the unlink-on-delete path,
  the import form. Deleting an integration a workshop uses as its source is
  refused naming the workshops; deleting an unused one leaves its records with
  a dangling `source_id` and a NULL `type_id`.

## Owner decisions

- **The ticket list is workshop-scoped:** `/tickets` is one workshop's live
  list; choosing a workshop is required.
- **The migration infers a single source:** a workshop gets the one remote
  tracker its tickets and `tracker_query` automations name, when that is
  unambiguous; else its owner's internal tracker.
- **No pre-bound status parameters:** every workshop binds the four
  `*_status` parameters itself.
- **No New ticket form on a remote source:** the button shows only on
  internal-source workshops; a remote one shows an "open in <tracker>" link.

## Deviations from the plan

- **A remote ticket stores no status:** `status_id` is NULL on every remote
  record, since no remote status rows exist any more; the status is read live.
- **Placeholder remote ids** (migration `0004_live_tickets.sql`): a remote
  ticket imported before `remote_id` was kept gets `legacy:<id>`, and the
  younger of two records of one issue (two users or workshops imported it)
  gets `dup:<id>`. No issue ever matches either, so their runs still resolve
  and the oldest record stays the issue's.
- **The ticket lock moved off the project:** a ticket's `workitem.Ref` has no
  project, so `engine.ErrTicketBusy` holds across every workshop sharing the
  source (lock keys and `ErrBusy.Item` changed with it).
- **Existing base branches count as user-set:** every ticket keeps its stored
  `base_branch`, so an existing remote ticket does not follow a second
  workshop's `repo.branch_format` until someone clears it. New records store
  none, and each run renders its own workshop's format.
- **The testdata v5 flow** (`internal/flow/testdata/v5.yaml`) gives its status
  parameters defaults (`In Progress`, `Done`), so the canonical fixture
  resolves without bindings.
- **A known issue starts while its tracker is down:** `POST
  …/tickets/{key}/runs` falls back to the record last seen under that key
  (`GetTicketBySourceKey`, index `tickets_source_seen_key_idx`) when the live
  read fails; only an issue with no record needs the tracker.
- **A pinned cron ticket follows the workshop's source:** the watcher refuses
  (and `last_error` says why) a pinned ticket of another owner or another
  source than the workshop's, and the migration disables cron automations
  pinned to such a ticket.
- **GitLab pages over several scopes with an opaque cursor:** the projects'
  and groups' lists are merged into pages of at most the limit, the cursor
  holding each scope's position and the issues listed at the page's last
  update time, so an issue a project and its group both list is listed once
  even when its two copies fall on different pages of the two lists.
- **A moved issue is found by its stable id on Jira only:** the retry by
  `remote_id` goes through an optional `trackerport.TicketByRemoteID`
  (Jira, the fake), never through `Fetch`: GitHub's and GitLab's `Fetch`
  read a bare number as an issue number, so a global id would alias an
  unrelated issue. A moved GitHub or GitLab issue keeps its record's
  last-seen values until it is found again by key.
- **A tracker query reads every page:** issues already handled stay ahead in
  the newest-first list, so the watcher follows the cursor to the source's
  last page each pass; its limit caps the runs a pass starts (the rest fire
  on the next pass) and is the page size, not a cap on what it looks at.

## Risks

- Saved flows with `with: { status: … }` stop parsing; the error names the
  `to:` to write.
- Saved flows with `to: in_progress` (or any old step name) still parse, but
  fail `input_invalid` at runtime unless the tracker has a status of that
  literal name.
- With nothing pre-bound, every existing workshop's runs of `builder` and
  `hands-on` are refused (`"done_status" has no value …`), and their
  automation firings record it as `last_error`, until the four status
  parameters are bound.
- The migration is lossy and one-way: it drops remote comments, the sync
  columns, remote status rows and remote tickets' `status_id`. A
  `tracker_query` automation that names a source other than its workshop's
  inferred one is disabled, saying why.
- More tracker traffic: lists and run starts hit the tracker, bounded by the
  cache, single-flight and the rate-limit hold. Analytics' unfiltered ticket
  counts count a ticket once per workshop that ran it.

## Tests that hold it

- `internal/platform/livecache/livecache_test.go`: `TestFreshHitDoesNotLoad`,
  `TestStaleServedWhileOneRefreshRuns`, `TestMissLoadsOnce`,
  `TestBypassLoadsAndFallsBack`, `TestInvalidate`, `TestHold`,
  `TestHeldByAMiss`, `TestLongerHoldStays`, `TestIdleEviction`,
  `TestLoadOutlivesItsCaller`.
- `internal/project/tickets/live_test.go`:
  `TestWorkshopsOnOneSourceShareItsCopies`,
  `TestAWriteDropsTheIssueAndTheListPages`,
  `TestARateLimitServesTheCopyUntilTheWaitEnds`,
  `TestARateLimitWithNoWaitBacksOff`,
  `TestTheInternalTrackerIsReadFromTheStore`,
  `TestTypesAddTheTrackersNewTypes`,
  `TestStatusesAndCommentsOfARemoteTracker`,
  `TestAMissingKeyNeverAliasesAnIssueNumberedLikeItsID`.
- `internal/project/tickets/runticket_test.go`: `TestTicketNowOfARemoteIssue`,
  `TestRunStartReadsTheIssueAndRecordsIt`, `TestRunStartSurvivesTheTracker`,
  `TestRunStartCopiesTheFilesOnce`, `TestEachWorkshopNamesTheBaseBranch`,
  `TestAnInternalRunTicketHasNoSource`.
- `internal/engine/ticketbusy_test.go`: `TestTicketBusySpansWorkshops`,
  `TestTicketBusyRule`, `TestConcurrentStartsOnOneTicketCreateOneRun`.
- `internal/engine/ticket_action_test.go`:
  `TestTicketTransitionAlreadyThereIsANoop`,
  `TestTicketTransitionUnreachableRoutesOnFail`,
  `TestLiteralStatusMovesOrFails`, `TestBoundStatusParameterMovesTheTicket`,
  `TestTicketTransitionMovesAHandWrittenTicket`; `internal/engine/unbound_test.go`:
  `TestUnboundTicketStatusRefusesTheRunUntilBound`;
  `internal/engine/ticketrefresh_test.go`:
  `TestRunStartReadsTheTicketForItsWorkshop`,
  `TestRunStartFallsBackToTheCallersTicket`.
- `internal/adapters/pgstore/migrate_test.go`: `TestLiveTicketsMigration`
  (every migration branch over seeded data); `scoping_test.go`:
  `TestListInternalTickets`.
- The `confstore` contract suite (`internal/project/confstore/conftest`), run
  by both the Postgres store and `Mem`: integrations "delete is refused while
  a project uses it, naming them", "a ticket source no project uses deletes,
  its tickets stay"; tickets "an issue is one record per tracker, across
  owners", "listed by remote id, of one tracker", "an internal tracker's list
  pages newest first, by done and text", "what was seen is written, the
  remote id kept"; trackers "a listed status takes the name only", "only the
  internal tracker's statuses are edited", "an internal ticket is its
  tracker's own issue", "a remote issue's record is made once and follows the
  issue", "a record changes only its base branch here", "a project's ticket
  source defaults to the internal tracker".
- `internal/project/confstore/failures_test.go`:
  `TestRecordBelongsToTheIntegrationOwner`.
- `internal/project/automation/watcher_test.go`:
  `TestTrackerQueryRecordsAndStartsOnce`, `TestSomeoneElsesRecordIsLeftAlone`,
  `TestARecordWithRunsIsNotFiredAgain`, `TestTrackerQueryReadsPastTheFirstPage`,
  `TestTrackerQueryOnABusyTicket`;
  `failures_test.go`: `TestAFailedRecordDoesNotStopTheOthers`,
  `TestRefiringNeedsToFindTheRecord`; `watcher_test.go`:
  `TestCronRefusesATicketTheWorkshopNoLongerShows`.
- `internal/flow/parse_test.go`: `TestTrackerActionSettings` (`to` via an
  unbound `$done_status`, the retired `status:`), `TestCanonicalV5`;
  `internal/flow/actionspec/actionspec_test.go`: `TestTrackerActions`;
  `internal/flow/builtin/builtin_test.go`:
  `TestBuiltinFlowsLeaveTicketStatusesToTheWorkshop`.
- `internal/analytics/compile_test.go`: `TestTicketsGrainIsPerWorkshop`.
- `internal/httpapi/ticketsync_test.go`: `TestLiveListJoinsRecordsAndPages`,
  `TestLiveListDoneFilter`, `TestRefreshBypassesTheCache`,
  `TestRemoteBaseBranchEditCreatesTheRecord`,
  `TestRunOnARemoteIssueCreatesTheRecord`,
  `TestRemoteSourceRefusesCreateAndDelete`,
  `TestRateLimitWithNothingCachedIs503`, `TestRemoteWritesGoThroughTheTracker`,
  `TestInternalTicketsAndWrites`, `TestRemovedTicketRoutes`,
  `TestTrackerStatusesAndTypes`, `TestProjectTicketSource` (a source in use
  refuses deletion, naming the workshop); `crossuser_test.go`:
  `TestWorkshopCannotBorrowATracker`, `TestTicketOfSomeoneElsesTracker`;
  `TestRunStartsFromTheRecordWhileTheTrackerIsDown`,
  `TestRemoteWriteRefreshesTheRecord` (`ticketsync_test.go`);
  `automations_test.go`: `TestCronAutomationByTicketKey`.
- `internal/adapters/gitlab/issues_test.go`:
  `TestGitLabIssuesSearchMergesOverlappingScopes`; `TestGitLabIssuesSearchAndFetch`
  and GitHub's `TestIssuesSearchAndFetch` (no fetch by stable id);
  `internal/adapters/jira/failures_test.go`: `TestFetchByRemoteID`.
- `cpui/src/routes/w/[workshop]/tickets/[key]/layout.test.ts`: a fresh ticket
  page for each workshop and each key; `cpui/src/lib/workshop/TicketPage.test.ts`:
  live load and refresh, internal versus remote actions, run start and its
  refusals; `cpui/src/routes/tickets/[ticketId]/page.test.ts`: the record
  route's hinted and first-workshop redirects and its orphan page.
