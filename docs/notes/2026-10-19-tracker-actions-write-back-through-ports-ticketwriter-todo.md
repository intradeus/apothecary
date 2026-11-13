# Tracker actions: write-back through `ports.TicketWriter` (TODO 27)

A flow can now move, annotate and link the issue its ticket was imported
from: four actions (`ticket_transition`, `ticket_comment`, `ticket_set_field`,
`ticket_link`, FLOWLANG §2) behind one write port, with Jira and GitHub Issues
adapters. Decisions worth recording:

- **An optional capability on the source adapter, not a second port.**
  `ports.TicketWriter` (`Transition/Comment/ListComments/SetFields/Fields/AddLink`
  — `Comments` became `ListComments` in TODO 49)
  is discovered by type assertion on the `ports.TicketSource` adapter, so one
  integration — one credential — serves both the import and the write-back,
  and an adapter that never learned to write is simply not a valid target
  (`resolve.Resolver.TicketWriter` refuses it by name). The read port's "the
  engine never writes back" promise narrows to "nothing writes unless a flow
  says so, stage by stage" (TODO 49 adds: or the user changes the ticket's
  status, labels or comments in apothecary).
- **Resolved per ticket, by integration id.** The tracker is the *ticket's*
  (`tickets.source_id`, snapshotted onto `run_created` as
  `ticket_source_{id,kind,key,url}` and carried on `run.View`), never the
  project's: the "not a project reference" decision stands, no project column
  was added. `engine.Resolution.TicketWriter` is a func field like `Models`
  and `Prepare` (nil in fakes that don't wire it). Preflight's per-project
  half (`actionAvailable`, `Requires: tracker`) checks only that the build can
  write back; the per-ticket half (`preflightTicket`, in `CreateRun` where
  the ticket is known) resolves the writer once — only when the ticket *is*
  linked.
- **Unlinked ticket ⇒ skip, visibly.** *(Superseded by TODO 49, below: a
  ticket with no source lives on its owner's internal tracker, so tracker
  stages act on it; a skip now means "no ticket", a ticket deleted mid-run, a
  step no status holds, or a non-label field on the internal tracker.)* The
  skip appends `stage_completed{skipped: true, note}` and follows
  `on_success`. Two new fields on an existing event rather than a new type:
  replay, the projection, telemetry and the UI's "attempt ended" lists all
  tolerate a zero-cost completion, and the timeline renders the note.
- **Failures are outcome failures.** `actionFail` → `on_fail` (else the run is
  abandoned), never `infraFail`: a status the workflow cannot reach, a field
  the tracker cannot write and a tracker that is down are all fixed outside
  the run. A new reason, `input_invalid`, names the first two (the flow's
  literal does not fit the tracker's schema); transport errors keep `infra`.
  "Best effort" is written as `on_fail: { to: <next>, max: 1 }`.
- **Remote actions need no workspace.** `actionspec.Action.Remote` +
  `engine.needsWorkspace`: a tracker action with no file input bound runs
  without provisioning, so "In Progress" lands seconds after the run starts
  and a "Done" after the final gate never re-provisions a reaped workspace.
  Three things followed: the model check's "no stage ran yet" guard became
  "no *workspace* stage ran yet" (it also fixes a gate-first flow silently
  skipping the check); `run.Apply` moves `pending → running` on
  `stage_started` (a run working before any workspace used to read as
  queued, and `Recover` would have re-queued it); and `ensureEnv` announces
  the first provisioning from the `env_requested` record rather than from
  the pending state.
- **Idempotency, per action.** Transition is read-compare-write; `append`
  mode is a union of comma-separated items; Jira links upsert on `globalId`
  and GitHub links carry an HTML-comment marker. Only a comment needs the
  intent pattern (`action_started` + reconcile through `Comments`), and its
  trailer — `— apothecary · run <id> · stage <id>` — is visible text because
  Jira renders comments as ADF and strips an HTML comment; it names the run,
  the stage and (in a phased flow) the phase run, never the attempt, since
  `actionPending` survives a failed attempt and the retry must recognise the
  comment it already posted.
- **Fields by display name.** A flow says `"Feature flags"`, not
  `customfield_10042`: the name is what the issue shows and what survives a
  Jira migration. The adapter resolves it through `GET /field` (cached per
  client), refuses an ambiguous name with the candidate ids, and coerces the
  text to the schema — a paragraph field takes ADF on v3 (`FromMarkdown`, the
  inverse of `ToMarkdown` for the subset the engine writes), labels split on
  commas, a select takes `{value}`. GitHub has `labels`, `assignees` and
  `milestone` and nothing else; `ErrFieldUnsupported` says so.
- **A free-text input kind.** `actionspec.KindText` is the author's literal
  in the tracker's vocabulary: the parser checks only blankness, the
  validator keeps it out of the dataflow, the editor draws it as a value
  (like an enum) rather than a connectable dot, and `ticket_set_field`'s
  `value`/`text` pair is "exactly one", the way a command takes `run` or
  `run_ref`.
- **No run-end hook.** "Done once the merge request merges" is a stage after
  the final gate (`auto_approve: mr_merged` → `ticket_transition`), which the
  language already expresses. (Flow v4, below, made the same true of a phased
  flow: the delivery phase parks on a `wait` for the merge and the move
  follows it.)

Not done, deliberately: a `Test`-button probe of write permissions (the
button's `error`-only result has nowhere to put "reads ok, writes denied");
Jira Cloud's development panel ("connect branch"), which needs an OAuth app
rather than an API token — the branch name already carries the issue key, so
the GitHub/GitLab-for-Jira app links it on its own, and `ticket_link` covers
the rest with a remote link.
