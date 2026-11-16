# journal_read: the run journal's tool

Why: the ledger a `run.journal` input puts in the prompt is the list, not
the record: a pass's verdict, outputs and log, and a decision too long for
the ledger, are what an agent needs to check a claim or avoid a repeat, and
none of them belongs in every prompt. The plan (Part 2, D21) gives them to
the agent on demand.

What now: a second tool, `journal_read`, on the shim's MCP server beside
`ask_human` (`workspace/shim/journal.go`), and two read-only routes under the
run's own token (`internal/httpapi/journal.go`):
`GET /api/runs/{run}/journal` (every record) and `…/journal/{id}` (a pass, a
decision, a note). The engine renders both from the same fold as the
ledger (`Engine.JournalRead`), so every id the ledger prints resolves.

- Outputs are typed from the log: `snapshot_captured` records the port's
  type (`port_type`) as the attempt ran, and the journal reads it there
  rather than from the flow. Before, the ledger typed them with the run's
  live flow and the tool with a flow it loaded again — and a run whose
  pinned version was gone loads the project's current document, or none,
  so the tool could miss notes and verdicts the ledger listed. The flow is
  now a fallback for an event with no type, and names what a skipped stage
  did not write; one that cannot be loaded is logged, not ignored.

- A pass is one file: its record, each output — text embedded, a binary
  listed by name and size — and its full log. A decision or a note is
  inline unless past 8 KiB (the plan's I4), then a file: a note's `did`
  and `why` have no length limit, and the tool never puts a long entry in
  the agent's context. The tool's description names all three kinds of id. No id: every decision whole and
  every pass's record with its verdict, no logs.
- Files are written by the shim to `.apothecary/journal/<id>.md` in the
  primary checkout (`$HOME/repo`, the sidecar's default root): the result
  says the path, bytes and lines, so nothing large enters the agent's
  context. Each call removes and rewrites the file, a read-only copy
  included; the engine names the file and the shim refuses a name that
  would leave the directory. The engine empties the folder as each fresh
  agent attempt starts (`clearJournalFiles`, beside `clearOutputs`; not on
  re-entering one): the plan said nothing could go stale, but a file one
  stage fetched would otherwise sit there for the next agent to find by
  searching instead of calling the tool.
- A snapshot swept by retention (`ErrNotFound`, `ErrContentGone`) reads "no
  longer kept"; a store that fails reads "could not be read just now", not
  taken for a sweep. A truncated capture carries its own marker.

Deviation from the plan: it says the tool is offered to "the agents of a
stage that binds `run.journal`", by the switch that already gates
`ask_human`. That switch is not per stage — it is whether the server runs
below an agent (`underAgent`) — and per stage is not reachable: codex's and
opencode's MCP registrations are static per workspace, and one agent
process serves every stage of a run. So every agent is offered the tool, as
every agent is offered `ask_human`; an agent whose stage binds no journal
has no ledger to tell it the ids, and the tool's own description says what
they look like. The Ask pane is unchanged: it is offered what it was
offered for `ask_human` (opencode turns the server's tools off for it by
prefix, `ask_human_*`; a read-only ACP session gets none of the profile's
MCP servers).

A read-only flow agent (the planner, the reviewer, the redactor) keeps the
server, as it keeps `ask_human` (the plan's I5): opencode's read-only rule is
a file permission, not a tool switch (now asserted in its adapter test), and
codex and omp make it an instruction in the role, in the same process.
omp's separate `--tools=` process is the Ask pane's read-only session only.

Tests: `internal/engine/journal_test.go` — `TestJournalRead` (a pass whole,
its text, binary and swept outputs and its log; a running pass; every
record with verdicts and no log; a decision inline and a long one in a
file; unknown ids refused), `TestEveryLedgerIDResolves`,
`TestSnapBodyStoreDown`, `TestPassRecordWords`,
`TestJournalFilesClearedEachAttempt` (the next agent finds no earlier
file, and only the journal's folder goes),
`TestJournalTypesOutputsFromTheLog` (a run whose flow is gone still gives
its notes and verdicts; an untyped event takes the flow's type; a long
note in a file), `TestAgentNotesReachTheJournal` (a capture records its
port type);
`internal/httpapi/journal_test.go` — `TestJournalRoutes` (records, a pass, a
decision, a 404, another run's token and none refused);
`workspace/shim/journal_test.go` — the file written and rewritten over a
read-only copy, records and inline, every failure a tool error, a rotated
token retried, and the tool listed and called over MCP only below an agent;
`TestServeMCPFraming` and `TestRunAskHuman` list both tools. The plan's
"one test per harness adapter that the tool is registered" is held in two
halves, since an adapter registers the shim's server, never its tools one
by one: each adapter's own test asserts the server for every agent, a
read-only one's included (codex's config, omp's sessions, opencode's
config and a read-only agent's front matter), and the shim's tests that
the server lists `journal_read`. No adapter filters the server's tools but
for the Ask pane.
