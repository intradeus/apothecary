# Review memory: poll first, never twice, curated gates (TODO 45, 2026-10-30)

The user: the AI reviewer re-posted its notes after every push, a round could
leave a thread unanswered, the review gate could only approve or reject the
whole file, and nothing told the owner why a note was worth posting. Three
decisions, all taken together: the record of what was said is the repository
host plus the event log — never a file of ids ("should we store comment ids in
a yaml?" — no: artifacts are per-attempt views, deleted before each fresh
attempt and gone with a reaped workspace, and on GitHub a note's thread id
does not exist until a later poll); the engine enforces what it can, the
prompt carries the rest; a gate curates by decisions over the document it
showed, resolved against the snapshot it pinned.

- **`internal/reviewfile`** is the leaf package for the three documents:
  `review_threads` (`{summary, notes: [{path, line, kind, body, rationale,
  evidence, example}], replies: [{thread, body, resolve, rationale}]}`),
  `review_replies` (`[{thread, did, reply, resolve, rationale, evidence}]`)
  and the new built-in `mr_threads` (`{merge_request, threads: [{id, path,
  line, resolved, ours, kind, hunk, comments: [{id, author, at, ours, new,
  did, body}]}], dropped}`). Strict decode by extension, JSON Schemas for all
  three registered as built-in type schemas (printed in the OUTPUT CONTRACT,
  checked first — the agent-stage check order is now schema, verdict,
  replies, review), Go parsers for what a schema cannot say, and the
  curation types. The posted text is a note's body (its `kind` rendered back
  as the leading `[kind]` tag — a typed field, because `body: [blocking] …`
  unquoted is a YAML sequence), a reply's text and the summary; the owner-only
  fields never leave the engine. `did` names a reply's result because
  "outcome" is a flow word. `parseVerdict` decodes by extension too
  (`flow.DecodeStructured`); the templates bind `artifacts/verdict.yaml`.
- **Poll first.** `load_review_comments` gained `scope: pending | all` and
  writes `mr_threads` to its `threads` port (the markdown and the undeclared
  JSON sidecar are gone). `all` loads every thread, ours included, picks
  nothing and never ends the phase; the reviewer sequence is now `poll → read
  → gate → post`. The event `review_comments_loaded` keeps `threads:
  [{id, path, line, ours, resolved, last}]` and `scope`, replayed into
  `rs.round`. Markers carry words — `kind=` on a note, `did=` on a reply —
  read back with `ports.MarkerAttr`; `MarkerThread` reads a word, not up to
  ` -->`, and reconciliation compares `IsReplyTo(round, thread)`. The fake
  repository host materialises posted notes as threads, which is what made
  cross-round tests possible at all.
- **Never twice.** At the reviewer's stage end (`reviewGuards`): a reply
  names a loaded thread once and resolves only ours (G3); a note on the
  `(path, line)` of an open thread of ours (G1), or saying what one says on
  the same file (G1b, normalised body from the `mr_threads` input), or equal
  to a note a person dropped at a gate (G2) fails the stage with
  `output_invalid` and the message says "continue it under replies" / "do
  not propose it again" — the retry reads it. Exact rules on purpose: a
  note on a line a push moved is not caught, and that is the prompt's job;
  what is caught is never a false positive. `post_review` posts the notes as
  a review and the replies in their threads (`ReplyThread` + `ResolveThread`,
  reconciled after a crash by the round marker), and posts no review object
  when there are only replies. `review_posted` gained `review_id`, `replied`,
  `resolved`.
- **Every thread answered.** `reviewfile.CheckReplies` requires one reply per
  loaded thread (minus the ones a person held back at the gate:
  `rs.unskippedRound`); a comment judged wrong gets `did: declined` with a
  rationale, never silence; `review_replies_posted` gained `declined`. The
  loader marks comments after our last word `new`, so a heated thread's
  newest ask is what the fixer and replier answer.
- **Curated gates.** `ping_raised.file_types` (path → port type, structured
  files only) tells the page which files curate and the engine which parser
  checks the answer. `ping_answered.curated` is, per path, decisions over the
  document AS SHOWN — `{summary?, items: [{item: notes/<i> | replies/<i>,
  keep, body?, resolve?}]}`, absent items kept as written — validated on both
  answer paths against the ping-file snapshot (`validateCuration`; the
  `SnapshotStore` port gained `ForPing`/`Bytes` for exactly this read), and
  refused when the file was not captured. `routeGate` resolves them once per
  ping into `review_curated` (the curated document, the dropped notes, the
  replies held back, the threads skipped with their last comment id) on
  approve and reject alike; replay folds it into `rs.dropped`, `rs.skipped`
  and `rs.curated`. A gate has no workspace (it is routed from the idle
  loop, and a parked gate's workspace may be reaped), so the curated file is
  written when the next stage that needs the workspace and reads the path
  starts (`materialiseCurated`), and forgotten when a stage that writes the
  path starts — live in `append`, the same rule replay applies. No gate
  `output:` port: the capability is the port type's, "every gate is the
  same machine" holds. A rejection's drops ride the verdict (`renderCuration`).
  The HTTP API decodes the shown documents server-side
  (`GET /api/pings/{pid}/curation`), so the page never parses a review;
  `pings` projection columns `file_types`, `curated` (migration 0019).
- **Templates.** reviewer: `poll` first, the gate shows `review` + `threads`,
  the prompt reads the threads first and says when to continue a thread,
  close it or stay silent, and never to propose a dropped note. builder: the
  loader and the fixer/replier ports are `mr_threads`; the replier writes
  `did`/`rationale` and reads the gate's `rejection`; a `reply_gate`
  (`$reply_gate`, on by default — a person reads what goes to human
  reviewers) sits before `post`. The dev fake writes a document that holds
  each declared output's shape (the contract now prints the type beside the
  role: `- path — role (type): …`), a reply per thread of its `mr_threads`
  input included.
- **Known limits.** A file input is inlined into the prompt tail-cut at 16 KB:
  a long threads file loses its head (hunks are written for open threads
  only). Curation memory is run-scoped — a rerun is a new run, though the
  host's markers carry across. A paraphrased dropped note reaches the gate
  again; the person drops it again.
