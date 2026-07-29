# Apothecary v5 — Flow language hardening (plan)

> Status: **draft — awaiting approval**. Response to the external review of
> `docs/flow-language.md` (`flow-language-review.md`). Every review section got
> a verdict against the actual code; the milestones below are what survived.

## 0. How the review was read

The review is good on the *language spec* and weaker on the *engine*, because
it only saw the doc. Three of its findings are already built:

- **"Event-source the engine" (long-term 2) — already done.** The engine has
  been event-sourced from day one: append-only `events(run_id, seq)`
  (`internal/pgstore/pgstore.go:134`), 28 event types including `edge_taken`,
  full replay-based crash recovery (`internal/engine/state.go:86`,
  `engine.go:1049`). Nothing to do except let the doc say so.
- **"Retry counters underspecified" (§6) — specified by the code, not the
  doc.** Counters are per `(stage, edge kind, chain depth)`, cumulative for the
  whole run, persisted in `edge_taken` events, rebuilt on replay, and cleared
  only by `run_resumed` ("try again"). The fix is documentation, plus one
  deliberate decision to confirm (see M24).
- **"`ask_human` is an undocumented hidden gate" (§9) — undocumented, not
  hidden.** The ping protocol is fully engine-owned: `ping_raised`/
  `ping_answered` events carry `stage_id`, the stage *timeout is suspended*
  while a ping is open (`runloop.go:517-551`), open pings re-arm on recovery,
  answers deliver attached files into the workspace. The doc is simply silent
  about all of it.

The exploration also surfaced **two real bugs the review missed** (M18, M23)
and confirmed its two most serious findings (stale outputs, the `create_mr`
crash window) are genuine.

## 1. Verdict table

| review item | verdict | where |
|---|---|---|
| §1 hidden workspace state / resource-effect graphs | **doc fix now, grammar rejected** (revisit with parallelism) | M25 |
| §2 stale output validation | **adopt, simpler mechanism** (delete-before-attempt, not attempt dirs) | M18 |
| §4 flow hash ≠ reproducible | **adopt half**: pin the flow bytes; keep agents/commands live on purpose | M19 |
| §5 action idempotency / reconciliation | **adopt**: intent event + branch-keyed reconciliation | M20 |
| §6 retry counter semantics | **document the existing semantics**; no backoff | M25 |
| §7 timeout ambiguity | **adopt minimally**: `timeout:` on commands + a per-type table; no renames | M23 |
| §9 `ask_human` undefined | **document the existing protocol** | M25 |
| §10 implicit ticket/context | **doc only**: a "run context" section; no `inputs:` grammar | M25 |
| §11 YAML/path safety | **adopt most**: dup-key gap, aliases, limits, symlink containment | M22 |
| §12 action edge wording | **doc fix** | M25 |
| §12 `$CHANGED` is a stat, and wrong base | **adopt + bug fix** (base-branch bug found here) | M23 |
| §12 abort projected as abandoned | **adopt minimally**; full state rework stays in TODO #17 | M24 |
| §12 failure taxonomy gaps | **adopt one**: `output_invalid`; rest documented | M18 |
| §12 `$FAILURE` leaks secrets | **adopt**: value-based redaction | M21 |
| A typed stage refs (`from: plan.outputs.plan`) | **reject syntax, adopt the check** (definite availability on paths) | M24 |
| C expanded artifact types | **reject for now** — the `OutputDecl.type` registry is already the designed seam; no consumer yet |
| D stronger preflight | **adopt selectively** | M24 |
| E policy sugar | **reject as grammar**; becomes an editor affordance ("add rescue gate" button), no language change |
| G scoped outputs replacing `$MR_URL` | **reject** — sentinels are the deliberate single value channel; `actionspec` outputs→sentinel is already the pattern |
| H terminal outcomes | **reject the registry**; the abort/abandoned naming trap is fixed in M24 |
| LT1 compile to IR | **reject** — `flow.Flow` after `Parse` *is* the IR (explicit `OnSuccess`, resolved defaults, explicit edges) |
| (follow-up idea) agents declared in the flow | **adopt fully (user decision)**: agents live in the flow, prompts included; the project agent library retires | M26 |
| (follow-up idea) per-attempt output snapshots | **adopt (user decision)**: `artifact_published` events + the existing blob store; every attempt's outputs *and full logs* auditable forever | M27 |
| LT2 event-sourcing | **already built** |
| LT4 multi-outcome routing | **reject for now** — §9 of the spec already reserves the edge-kind registry for exactly this |

Rationale for the big rejections:

- **Resource/effect graphs (§1).** The engine runs one cursor in one workspace;
  there is no write/write conflict a validator could catch, so `resources:
  workspace: write` on nearly every stage is ceremony with no checker behind
  it. The honest fix is the spec's overclaim: rule 1 says "everything a stage
  consumes or produces is declared", and the working tree falsifies that. Say
  instead: *artifact interfaces* are declared; the workspace is the ambient
  medium every agent/command stage shares; commit/test/push read it by nature.
  When parallel stages arrive (explicitly out of scope, §9), effects become
  checkable and earn their grammar.
- **Typed stage references (A).** `from: plan.outputs.plan` replaces the
  path-rendezvous model the editor's derived dataflow is built on, and turns
  every flow file over. The property the review actually wants — "this input
  is definitely produced before it is consumed" — is checkable on paths
  as-is (M24). Keep the syntax, strengthen the checker.
- **Policies (E).** Expansion machinery would have to live twice (Go parser +
  the TS canonicalizer in `cpui/src/lib/flowdoc.ts`) and hide the compiled
  graph the whole language exists to show. The repetition it targets is real
  but confined to rescue-gate boilerplate — an editor button that stamps out
  a `debug_<stage>` gate pattern gets the ergonomics without touching the
  grammar.

---

## 2. M18 — Output freshness (`FLOWLANG §4/§5`)

The review's strongest finding, confirmed: output validation is a pure
existence check (`runloop.go:734-745`), so attempt 2 can "succeed" on attempt
1's stale file. Same hole for malformed verdicts left over from a prior
attempt, and for command outputs.

Not adopting the review's attempt-directory layout
(`.apothecary/runs/<id>/attempts/...`): artifacts live in the repo on purpose
— they are committed, diffed, and read by agents through their own harness at
repo-relative paths. Publishing/copying would break that rendezvous.

The fit for this codebase is **delete-before-attempt**:

- On every *fresh* attempt (the same place `stage_started` is appended with a
  new iteration), the engine `Exec`s `rm -f` over the stage's effective
  outputs — `flow.EffectiveOutputs` for agent stages, `st.Outputs` for
  command stages — before the stage runs.
- **Not** on crash-recovery re-entry (`phIn` resume, `reenter=true`): the
  harness session resumes mid-work and its partial outputs must survive.
- The existing post-stage existence check then *is* a freshness check: the
  file can only exist because this attempt wrote it. No hashing, no mtime.
- This tightens the output contract to what the prompt already promises
  ("every file it must write") — an agent that wants to keep a prior file
  must rewrite it, which is the honest behavior on a retry anyway.

Taxonomy, while here (`event.go:57-69`): add `ReasonOutputInvalid` and use it
for a verdict file that exists but fails `parseVerdict`
(`runloop.go:466-478` currently reports `output_contract`, which the spec
defines as "did not write what it declared" — wrong claim). Spec §5 table
gains the row; budget exhaustion and cancellation stay run-level reasons and
get documented as such, not new stage-failure reasons.

Acceptance: a scenario test where an agent stage succeeds without rewriting a
prior attempt's output fails with `output_contract`; a malformed verdict
reports `output_invalid`; crash recovery mid-agent-stage keeps partial files.

Deletion loses no history: attempt N+1 already overwrote attempt N at the
same path, so nothing kept today goes away — and M27 adds the durable
per-attempt snapshot that makes attempt history *actually* inspectable.

## 3. M19 — Pin the flow, audit the rest (`FLOWLANG §1`)

Confirmed worse than the review knew: the run records `flow_hash` but the loop
reloads the flow *by name* on every start and only **warns** on hash drift
(`runloop.go:102-105`) — editing a flow mid-run silently changes running runs.

The infrastructure already exists: `flow_versions` is content-addressed by
hash (`migrations/0002_config_plane.sql`), and the parse cache is keyed by
hash (`confstore/flowsource.go`). So:

- The run loop loads the flow **by the recorded `flow_hash`** from
  `flow_versions`, not by name. Drift stops being possible instead of being
  warned about. The stored raw bytes are the run's flow, forever.
- A missing version row (pre-existing runs, pruned rows) falls back to
  today's by-name load with the existing warning.

With M26, agents live *inside* the flow — so pinning the flow bytes pins the
prompts too, and the review's "execution lock" is answered for agents
entirely. Commands (`run_ref`) and model strings stay live-resolved on
purpose, audited per attempt; the spec states the split explicitly: **the
flow — agents included — is pinned; project-side dependencies are resolved
live and audited per-attempt.**

The prompt-repair workflow moves to one deliberate seam: **`run_resumed`
re-pins the run to the flow's current version.** "Try again" is the
human-initiated repair moment — edit the flow (a prompt, a binding), hit
resume, and the run picks it up, with the existing
recorded-stage-must-exist check guarding the swap. Crash recovery and
pause/play never re-pin — mid-run behavior cannot drift silently.

## 4. M20 — Action intent + reconciliation (`FLOWLANG §2`)

Confirmed crash window (`runloop.go:1024-1037`): `Forge.CreateDraftMR`
returns, the process dies before `mr_created` is appended, recovery re-enters
the action executor with `rs.mr == nil` → duplicate MR. The
`if rs.mr != nil` guard only covers crashes *after* the event.

Event-sourced engines fix this with an intent event, and ours is one:

- New event `action_started{stage_id, action}` appended **before** the
  executor's side-effecting call. (Extends the §6 catalog like `run_archived`
  did; note in IMPLEMENTATION_NOTES.)
- `actionspec.Action` gains a `Reconcile` hook next to its executor. On
  re-entry, if replay shows `action_started` without the action's completion
  event, the engine calls `Reconcile` instead of the executor blindly.
- For `create_mr`, reconciliation is natural and needs no stored idempotency
  key: **the run's branch is unique** (`factory/<run_id>-…`), so "find open
  MR with source branch X" answers it. Found → append `mr_created` with the
  found MR and continue; not found → execute normally.
- No generic retry-classification field yet — one action exists; the hook is
  the seam a second action will use.

Spec fix bundled here: §2 currently says "Actions carry no edges" while every
example gives them `on_success`. Reword to "actions carry no *failure* edges
in this version: `on_success` routes normally, a failure is terminal."

## 5. M21 — Redact secrets from the failure channel (`FLOWLANG §4`)

Confirmed: zero redaction anywhere. Raw command output — which can contain
the git token, run token, or provider keys — flows verbatim into
`stage_failed.Error`, `edge_taken.Feed`, agent prompts (`$FAILURE`), gate
displays, and plaintext stage logs.

The engine *knows every secret it injected* (git credentials, provider keys
from the resolution, per-run bearer tokens), so value-based redaction is
precise and cheap — no regex guessing:

- `engine.redactor`: built per-run from the resolved secret values (plus
  both run tokens across a rotation), replaces each occurrence with
  `[redacted:<kind>]`.
- Applied at the capture points, once, before anything persists or renders:
  the `stage_failed.Error` / `edge_taken.Feed` tails (`runloop.go:297`),
  `$FAILURE`/`$DECISION` prompt rendering (`prompt.go:30-40`), and the stage
  log writer (`runloop.go:1416`).
- Pattern-based scrubbing (Bearer …, AKIA…, PEM blocks) is a possible second
  layer, deliberately out of scope here — value-based covers what *we*
  injected, which is the threat the review named.

Spec: `$FAILURE`'s row in §4 gains its defined behavior — last 8 KB, UTF-8
lossy, secrets redacted by value.

## 6. M22 — Parser and path hardening (`FLOWLANG §6`)

The review's checklist, filtered against what yaml.v3 already gives us
(duplicate keys and billion-laughs are library-rejected; `KnownFields` is
already on):

- **`DocEdge.UnmarshalYAML` duplicate-key gap** (`parse.go:171-200`): the
  custom unmarshaler bypasses yaml.v3's check, so `{to: a, to: b}` silently
  last-wins. Reject by hand, as `DocIO` already does (`parse.go:260-275`).
- **Reject anchors/aliases**: walk the `yaml.Node` tree (we already decode
  one for line numbers) and refuse `AliasNode`. They break the "canonical
  form round-trips" property and buy flow authors nothing.
- **Reject trailing documents**: a second `Decode` must return `io.EOF`;
  today everything after `---` is silently ignored.
- **Structural caps**: `then` chain depth (8), stages per flow (128),
  document size on the file `Load` path (1 MB, matching the HTTP cap that
  already exists).
- **Symlink containment at runtime** (the one with teeth): `validArtifactPath`
  is lexical-only, and `devcontainer.ReadFile` follows symlinks — an
  agent-authored `artifacts/plan.md → /apothecary/run.env` symlink would leak
  the run token into a prompt or a gate display. Fix in the adapter, not the
  grammar: resolve with `realpath` and require the result to stay under the
  repo root before reading/writing artifact paths.
- Spec §6 gains a short normative block: YAML 1.2 via yaml.v3 strict mode,
  duplicate keys rejected, unknown fields rejected, no anchors/aliases/tags,
  the caps above, POSIX paths, no symlink traversal for artifact I/O.

## 7. M23 — Timeouts and `$CHANGED` (`FLOWLANG §2/§4`)

Timeouts, confirmed asymmetry: commands may **not** declare `timeout:`
(parse-rejected) yet are always bound by the flow default; `wait` ignores the
default (0 = forever); gates and actions have no timer at all.

- Allow `timeout:` on command stages (one row in the field-applicability
  table, `parse.go:605`; use it in `runloop.go:686`). The default flow's
  `test` stage — the review's own example — can then own its budget.
- No renames (`execution_timeout`/`wait_deadline`) and no per-type `defaults`
  blocks: the grammar is closed and per-type validated, so one `timeout`
  field with per-type documented meaning is unambiguous at parse time.
  Instead, §2 gets the table the review asked for: agent → default 30m from
  `defaults.timeout`; command → same; wait → own `timeout` only, absent =
  forever; gate/action → none, by design (a gate is bounded by human
  attention, an action by its client).

`$CHANGED`, two fixes:

- **Bug (found in this review, not the external one)**: the diff base is
  `origin/<project default branch>` (`prompt.go:41-52`) — a run forked from a
  non-default `source_branch` diffs against the wrong base. Use the run's
  recorded `source_branch`, falling back to the default branch.
- Switch `--stat` to `--name-status`: the spec calls it "the changed-file
  list" and the prompt tells the agent to read files itself, so the stat's
  churn counts are noise. The sentinel name stays `$CHANGED`; the spec row
  is corrected.

## 8. M24 — Validator strengthening + terminal naming (`FLOWLANG §3/§6`)

Adopted from short-term A/D, on the existing path-based model:

- **Definite availability**: for every required file input, verify some stage
  produces that path on *every* control path reaching the consumer (walk the
  explicit graph; it is small and acyclic on success edges). Today's check
  (`bindings.go:56-64`) only asks "does any producer exist at all". After
  M26 this lives in the parser (contracts are in-document) as a warning.
- **Signal ordering**: `wait: mr_merged` / `auto_approve: mr_merged` reachable
  before any `create_mr` stage → warning naming the `needs` prerequisite,
  instead of a guaranteed runtime `input_missing`.
- **Fix the validate endpoint downgrade (bug)**: `POST /api/flows/validate`
  currently folds binding *errors* into `warnings` with `"error: "` prefixed
  and returns `ok: true` (`confapi.go:646-654`). M26 removes the binding
  level entirely, so this shrinks to: return
  `{ok, errors, warnings}` honestly; saving stays non-blocking (that is
  level-2's contract) but the editor stops rendering errors as advice.

Terminal naming (review §12), minimal version: today operator Abort →
`run_failed{"aborted"}` → state **failed**, while the flow's `abort` terminal
→ `run_abandoned{"aborted by flow"}` → state **abandoned** — the same verb
lands in opposite states, and a deliberate human "no" reads as "engine gave
up". Unify: both append `run_abandoned` with distinct reasons
(`aborted by user` / `aborted by flow`); `failed` is reserved for the engine's
own faults (env, infra, unparseable flow). Capability is unchanged
(`ResumeRun` already accepts both). The full state-machine rename
(abandoned/cancelled/exhausted) stays folded into TODO #17 where it belongs.

## 9. M25 — The spec catches up (`docs/flow-language.md`)

Last, because it documents the implemented reality of M18–M24 and M26. All
doc, no code:

- **§1/§4 rewritten for v3**: the `agents:` block (M26) — agents are part of
  the document, contracts checked at parse time, validation is two levels
  (parse, preflight), and "agent contracts" (§4) describes the in-flow form.

- **Rule 1 rewritten**: *artifact interfaces* are declared; the workspace is
  the shared ambient medium; name which stage types touch it implicitly
  (agent, command) and that `commit`/`test`/`push`-style stages read the tree
  by nature. Drop the overclaim the review §1 correctly attacked.
- **New: run context section** (review §10): what every run provides without
  declaration — ticket (the task), repository, branch and source branch,
  workspace, run identity — stated once, with the §7 note ("the ticket is
  always the task") pointing at it.
- **New: agent-side interactions section** (review §9): the ping protocol as
  built — types (`clarification`/`blocker`/`permission`), options, artifact
  refs, *timeout suspension while a ping is open*, `ping_raised`/
  `ping_answered` in the event stream, recovery re-arm. Also reconcile the §1
  comment ("suspended while a ping waits") with the §7 claim that "ping" is
  retired — the retired thing is the gate's `ping:` *field*; the interaction
  is alive and now specified.
- **§3 retry semantics** (review §6), documenting the code: per-link
  counters keyed by chain depth, cumulative for the run, no reset on source
  success, persisted in `edge_taken`, cleared only by "try again"
  (`run_resumed`), untouched by pause/play. One decision to confirm during
  review of this plan: cumulative-for-the-run is arguably the *right*
  semantics (it bounds total spend, which is what `max` is for) — if so it
  becomes normative text, not an accident.
- **§5/§6**: the M18 freshness rule ("a declared output must be written by
  the successful attempt"), `output_invalid`, the M23 timeout table, the M20
  action wording, the M22 YAML/path rules, the M19 pinning contract, the M21
  `$FAILURE` definition, and `abort` → "aborted (by user / by flow)"
  projection language.

## 10. M26 — Agents move into the flow (`FLOWLANG §1/§4/§6`) — grammar v3

**Decision: the flow is the single source for agents, prompts included.**
The per-project agent library retires. One file states the whole factory
behavior — which agents exist, what they read and write, what they are told,
and how the run moves between them — under one hash. (An earlier draft of
this milestone proposed interface-only declarations with the library kept as
a provider layer; dropped for MVP simplicity.)

### Grammar

A required top-level `agents:` block; every agent stage's `agent:` must name
a key in it. Each entry is the full `agentspec` schema minus the redundant
`name` field (the key is the name, same rule as the old filename-stem check):

```yaml
name: default
version: 3
agents:
  planner:
    description: "explore the repo and write the implementation plan"
    prompt: |
      Explore the repository and write an implementation plan...
    inputs:
      rejection: { optional: true, description: "why the plan was turned down" }
      failure:   { optional: true, description: "what the previous attempt printed" }
    outputs:
      plan: { path: artifacts/plan.md, description: "the implementation plan" }
  reviewer:
    prompt: |
      Review the change set against the plan...
    read_only: true
    inputs:
      plan:    { description: "the approved plan" }
      changes: { description: "the set of changed files" }
    outputs:
      verdict: { type: verdict, path: artifacts/verdict.json,
                 description: "APPROVE or REJECT with actionable comments" }
stages: [...]
```

This is a **grammar break — `CurrentVersion` bumps to 3**, exactly what the
version field exists for: a v2 document (agents in a library the flow cannot
see) is refused with a clear message, and the §7 retired-spellings table
grows the v2→v3 rows ("`agent: builder` with no `agents:` entry → define the
agent in the flow's `agents:` block").

### What collapses (the simplification payoff)

- **Level 2 validation disappears.** Every check in `bindings.go` — input
  names against the contract, non-optional inputs bound to feed sentinels,
  `on_reject` without a verdict output, output rebinds, producer/consumer
  path matching — becomes a **parse error**, because the contracts are in
  the document. `ValidateBindings`, the project-overlay machinery in
  `POST /api/flows/validate` (`flowBindingSpecs`), and the level-2 section
  of the spec all go away. Two levels remain: parse, and preflight
  (harness, integrations, `run_ref`s, signals — genuinely per-project facts).
- **Resolution simplifies.** `Resolution.AgentSpecs`/`AgentHashes` and the
  shadow layering (`resolve.go:207-220`) go; the engine reads agents off the
  parsed `flow.Flow`. `stage_started.agent_hash` stays — computed with the
  same canonical-JSON hash over the inline definition, so the
  prompt-version-to-outcome analytics join survives unchanged.
- **The library retires.** `internal/builtin/agents/*.yaml` fold into
  `builtin/flows/default.yaml`; the `agents` + `agent_versions` tables, their
  confstore/API surface, and the library UI screens are removed (MVP — no
  dormant code paths). Agent versioning is subsumed by flow versioning:
  `flow_versions` is already content-addressed, and every agent edit is a
  flow edit.
- **The editor is one editor.** Agent dots and their I/O come from the
  document; editing a prompt is a panel inside the flow editor; the
  canonicalizer emits `agents:` (sorted by name) before `stages:`. No
  project needs to be loaded to draw or author anything.

### Consequences accepted (eyes open)

- **No reuse across flows**: a second flow wanting the same builder
  copy-pastes it. Fine at MVP scale; if it ever hurts, an *additive* include
  or library layer can return without touching v3 documents.
- **No per-project agent shadowing**: a project can no longer override
  `builder` for a global flow. The per-project seam that remains is
  `commands` (`run_ref`), integrations, and the repo's own `AGENTS.md` —
  which is where repo-specific instruction belonged anyway.
- **Flow hashes churn with prompt tweaks** — that is now a feature: the hash
  *is* the behavior, and M19's resume-re-pin is the sanctioned way to move a
  live run onto an edited prompt.
- TODO #14 (harness-provided agents) is unaffected — that is about `agent:`
  resolving to a harness built-in, which the `agents:` block can grow a
  field for later.

### Migration

One-time and small: rewrite `builtin/flows/default.yaml` self-contained
(fold the five built-in agent files in), bump `CurrentVersion`, port the TS
canonicalizer, and export any DB-authored agents into their flows by hand
(single-user MVP — no automated migration needed). `agentspec` stays as the
package defining the agent schema and hash; only its storage moves.

Acceptance: a fresh checkout runs the default flow with the DB agent tables
gone; `flow-language.md` §4/§6 describe two validation levels; the graph
editor renders the full default flow with no project selected.

## 11. M27 — Per-attempt output snapshots (`FLOWLANG §4/§5`)

Today an attempt's outputs are ephemeral three times over: attempt N+1
overwrites attempt N at the same path, the workspace dies with the run, and
the event store keeps only 8 KB tails (`stage_failed.Error`,
`edge_taken.Feed`). So "show me the plan the human rejected at attempt 1" is
already unanswerable — M18's delete-before-attempt changes nothing there, it
only closes the stale-pass hole. This milestone makes the history real:
**every attempt's declared outputs and its full log are captured durably and
linked from the event stream**, so a finished run's timeline can render
"plan (attempt 1) → [plan.md] · [log]" forever.

Build on what exists rather than adding an S3 dependency:

- **The blob store is already there** (`internal/artifacts`): filesystem
  blobs under `<RunsDir>/artifacts/`, sha256-hashed, Postgres metadata rows,
  scopes, GC, serving. It gains one scope, `stage_output`, and three
  metadata fields (`stage_id`, `iteration`, `output name`). It already sits
  behind ports (`artifacts.DB` + the filesystem), so pointing blobs at S3
  later is an adapter swap, not a redesign — deliberately out of scope now
  (the factory's only stateful dependencies stay Postgres + `RunsDir`).
- **Capture is nearly free.** The post-stage output check already reads
  every declared output's bytes (`missingPaths` → `Envp.ReadFile`,
  `runloop.go:734-745`); instead of discarding them after the existence
  check, hand them to the store. Capture on *both* outcomes — a failed
  attempt's partial outputs are exactly what you want when debugging why it
  failed. With M18, anything present at capture time is attempt-authored by
  construction, so snapshots are never polluted by stale files.
- **New event `artifact_published{stage_id, iteration, name, path,
  artifact_id, sha256, size}`** — one per captured output, appended with the
  attempt's completion. The event carries the *reference*, never the bytes:
  events stay small, blobs stay in the store, and the run timeline joins
  them by `artifact_id`. (This is the `artifact_published` event the review
  proposed in its event-sourcing section — adopted here in its cheap form.)
  The sha256 column enables dedup across attempts: an output rewritten with
  identical bytes can share its blob.
- **Redaction before storage** (M21's redactor): snapshots outlive the
  workspace and its security boundary, so the same value-based scrub applies
  on the way in. Oversized outputs are tail-truncated with a marker rather
  than skipped — for audit, a truncated blob beats a missing one.
- **Retention**: `stage_output` artifacts are excluded from
  `DeleteForRun`'s archive sweep — the point is that they outlive the run.
  They are content-capped per file and GC-able only by explicit run
  deletion, not by the orphan sweep.
- **Stage logs publish through the same mechanism.** The full log of every
  attempt already lands on disk (`<RunsDir>/<run>/<stage>-<iter>.log`,
  `runloop.go:1416`) but is unlinked and unserved. At attempt end it is
  saved to the store under a `stage_log` scope (same metadata, same
  redaction — logs are the *most* likely bytes to hold secrets, same
  retention) and referenced by its own `artifact_published`. The 8 KB tail
  in `stage_failed.Error` / `edge_taken.Feed` keeps its job — a replay-cheap
  inline preview and the prompt feed — with the complete log now one link
  away. Published on success too, not just failure: a passing attempt's log
  is part of the audit.
- **UI**: the run timeline renders each attempt's outputs and log as links
  (served through the existing artifact endpoints); gate feedback, ping
  attachments and edge feeds are already durable, so this closes the last
  ephemeral class — the event store becomes fully auditable end to end.

## 12. Explicitly not doing (and why it's safe to wait)

- **Resource-effect grammar** — earns its keep only with parallel cursors;
  §9 already reserves that as a real rework, not a patch.
- **Backoff on edges** — every agent/command loop must carry a finite `max`
  or a human gate by grammar; there is no unbounded tight loop to protect
  against. Revisit if a cheap-failing command at `max: 5` ever hurts.
- **`json`-typed outputs with schemas** — `OutputDecl.type` is the designed
  seam (§9); add it the day an engine behavior consumes it.
- **Terminal-outcome registry, multi-outcome routing, flow-level
  `inputs:`/`outputs:`** — each is additive later; none unblocks anything
  above.
