# The apothecary flow language

A **flow** is a YAML document describing how a ticket becomes a merge request:
which agents run, in what order, what each one reads and writes, where a human
decides, and what happens when something fails.

This document is the normative reference. Code cites it as `FLOWLANG §n`.

Three rules shape everything below:

1. **Every artifact interface is declared, in one form.** No stage quietly
   reads a file another stage happened to leave behind, and no *artifact*
   arrives invisibly — not even the feedback from a failed attempt. Every
   binding is `name: value`, whatever the stage type, which is why the graph
   editor can draw the dataflow.

   What this rule does *not* claim: that a stage touches nothing else. Agent
   and command stages share one workspace — a checkout with a working tree, an
   index and a history — and that medium is ambient, not declared. A `commit`
   stage reads the whole tree by nature; `test` reads the repository; `push`
   writes a remote. Declaring `input: {message: artifacts/commit.md}` says what
   the *artifact interface* is, and is honest about being only that. The
   workspace is shared because the engine advances one stage at a time; when
   parallel stages arrive (§10) the effects become worth declaring, and they
   will be.
2. **Values are strict, types are extensible.** Every field takes a closed set
   of values, so a typo is a parse error rather than a surprise at 3am. New
   stage types, actions, signals, sentinels and edge kinds are added by
   extending a registry, not by loosening the grammar.
3. **Each fact is stated once, where it belongs.** An edge says where the run
   goes; the stage it lands on says what to do with what arrived. An agent
   declares what it needs; the stage that runs it says where to find it.

---

## §1 Document

```yaml
name: default              # required, slug: the flow's permanent id
version: 3                 # required int: the grammar this document is written in
description: "..."         # optional
defaults:                  # per-stage defaults; agent stages may override each
  timeout: 30m             #   wall clock per stage (suspended while a ping waits)
  harness: opencode        #   required: the harness kind agents run on
  model: provider/model    #   required
  max_turns: 50
budget:                    # run-wide ceilings; exceeding either abandons the run
  max_cost_usd: 10.0
  max_total_turns: 300
agents: [...]              # the agents this flow runs — definitions, not references
stages: [...]              # required, at least one
```

`version` is required and must equal the version this build speaks (currently
**3**). A document from a future grammar is refused with a clear message instead
of being half-understood. That is the entire purpose of the field: it is the
seam a breaking change hangs off.

### Agents live in the flow

A flow **defines** every agent it runs. There is no separate library to
resolve against:

```yaml
agents:
  - name: reviewer                      # slug, unique in the document
    description: adversarial code reviewer
    read_only: true                     # deny the harness's edit/write tools
    allow_bash: true
    inputs:
      - name: plan
        description: the approved plan to check the change set against
      - name: failure
        description: what the previous attempt printed when it failed
        optional: true                  # so a stage may bind $FAILURE to it
    outputs:
      - name: verdict
        type: verdict                   # "" (a plain file) | verdict
        path: artifacts/verdict.json    # default; a stage may rebind it
        description: APPROVE or REJECT with actionable comments
    prompt: |
      You are the adversarial reviewer of a software factory...
```

Three things follow, and they are the point:

- **The contracts are parse-time facts.** Binding an input the agent does not
  declare, leaving a required one unbound, or putting `on_reject` on an agent
  that renders no verdict are all **errors that refuse the document** — not
  advice from a second pass that needed a project to run. A flow is
  reviewable, and wrong flows are unsavable, on its own.
- **A flow is self-contained.** One file states the whole behaviour: what runs,
  what each agent is told, what it reads and writes, and how the run moves
  between them. Copy the file, get the factory.
- **Prompts are pinned with everything else.** A run executes the document it
  started with (below), so editing a prompt never changes a run already in
  flight.

The cost, accepted deliberately: two flows wanting the same agent copy it, and
a project can no longer override an agent for a flow it runs. What stays
per-project is the genuinely per-project: `commands` (`run_ref`), integrations,
and the repo's own `AGENTS.md`.

**Determinism and pinning.** A flow's identity is the SHA-256 of its raw bytes.
A run records that hash at creation and **executes that exact version for its
whole life** — recovery after a restart re-reads the same pinned document, so
a flow edited mid-run (including a prompt inside it) cannot change a run
already going. The one deliberate exception is **"try again"** (`run_resumed`):
resume is the human-initiated repair moment, so it re-pins the run to the
flow's current version — fix a prompt or a binding, then resume. Pause/play and
crash recovery never re-pin.

Project-side dependencies are resolved live and audited per attempt instead:
`run_ref`s and integrations are looked up when used, and every attempt records
`stage_started.agent_hash`, the content address of the exact agent definition
that produced it.

The graph editor re-emits a canonical form — agents before stages, stages in
success order, an explicit `on_success` on every stage, no comments — and
`docToYaml ∘ parse` is a fixed point, so opening and saving an already-canonical
flow produces no change.

**Where flows live.** Flows are global and project-independent: the same flow
runs against any project. Harness kinds, `run_ref`s and action integrations are
*free text* at parse time and validated against a specific project when a run
is created (§6).

### Run context

A run supplies these without any declaration; they are the ambient facts every
flow is written against, and no binding names them:

| context | what it is |
|---|---|
| the ticket | title + goal — always the prompt's `# TASK` section |
| the run goal | extra direction for *this* attempt, its own prompt section |
| the repository | the project's repo, cloned into the workspace |
| the branch | `factory/<run-id>-<slug>`, forked from the run's source branch |
| the workspace | one checkout shared by every agent and command stage |
| run identity | run id and its bearer token, for the ask_human channel |

A flow declares none of it because none of it varies per stage. `$CHANGED`,
`$WORKSPACE` and `$MR_URL` (§4) are how a stage reaches into this context
explicitly, and those *are* declared.

---

## §2 Stages

```yaml
- id: build                # required, slug, unique; `done`/`abort`/`start` reserved
  type: agent              # agent | command | human_gate | action | wait
  description: "..."       # optional note; documentation only, reaches no prompt
```

| type | what it does | key fields |
|---|---|---|
| `agent` | runs an agent this flow declares (§1) | `agent`, `input`, `output`, `model`, `harness`, `max_turns`, `timeout` |
| `command` | runs a shell script in the workspace | `run` **or** `run_ref`, `input`, `output`, `timeout` |
| `human_gate` | parks the run on a human decision | `question`, `input`, `auto_approve`, `auto_reject` |
| `action` | invokes a built-in apothecary integration | `action`, `input` |
| `wait` | blocks until an external signal fires | `for`, `timeout` |

A field that does not apply to a stage's type is a parse error (`model` on a
command, `question` on an agent, and so on).

### agent

```yaml
- id: build
  type: agent
  agent: builder                        # a name in this flow's agents block
  input:
    plan: artifacts/plan.md             # bind the agent's declared inputs
    rejection: $DECISION                # ...including what an edge brings back
  output: { plan: docs/plan.md }         # optional: rebind a declared output's path
  model: openai/gpt-5                   # optional overrides of the flow defaults
  harness: codex
  max_turns: 80
  timeout: 45m
```

The agent's definition in this document's `agents:` block — its **contract** —
declares what it reads and writes (§1, §4). The stage binds that contract; it
cannot invent inputs or outputs the agent does not declare, and the parser
refuses the document if it tries.

### command

```yaml
- id: commit
  type: command
  run: "git add -A && git commit -F artifacts/commit.md"   # ...or run_ref: verify
  input: { message: artifacts/commit.md }   # read before the script runs; missing ⇒ input_missing
  output: { report: artifacts/report.txt }  # must exist when the script exits 0
  timeout: 20m                              # optional; absent = defaults.timeout
```

Exactly one of `run` (literal bash) or `run_ref` (a key in the project's
`commands`) is required. A command has no contract to check its binding names
against, so they are free labels — and that is the point: they say what each
file is *for*, and they title the file's dot in the graph. Declaring them is
what makes `git commit -F artifacts/commit.md` visible as dataflow rather than
an opaque string.

### human_gate

```yaml
- id: plan_gate
  type: human_gate
  question: "Approve this implementation plan?"   # what the human is asked
  input: { plan: artifacts/plan.md }             # what they are shown
  on_reject: { to: plan, max: -1 }
```

Every gate is the **same machine**: approve follows `on_success`, reject follows
`on_reject` carrying the human's feedback. There is no gate kind — what a
particular gate means ("approve the plan", "the workspace is parked, fix it and
approve to retry", "merge when satisfied") is said in its `question`, and what
it hands over is its `input` (one binding today). Both are optional; a gate with
no question is labelled by its stage id.

**Answered automatically.** A gate is a decision point whose *default* answerer
is a human. `auto_approve` and `auto_reject` name signals (§2 wait) that may
supply the same two answers:

```yaml
- id: final_gate
  type: human_gate
  question: "Review the merge request and merge when satisfied."
  input: { mr: $MR_URL }
  auto_approve: mr_merged     # they merged it instead of clicking approve
  auto_reject: mr_closed      # they closed it unmerged, which is a no
  on_success: done
  on_reject: abort
```

The gate keeps accepting a manual answer the whole time: **whichever lands
first wins.** That race is why these are fields on the gate rather than separate
`wait` stages — the engine runs one stage at a time, so `gate → wait` would mean
"approve, *then* wait for merge", never "either one". A signal-supplied answer is
recorded exactly like a human's, with `auto: <signal>` as its feedback (so a
`$DECISION` reader still gets a reason). The same signal cannot be given to both
fields.

Why two named fields rather than one `auto_answer`, or a boolean `any_of`
combinator: the field has to say *which* answer it supplies, because approve and
reject route to entirely different places. And a symmetric-looking
`any_of: [mr_merged, human_approve]` would hide a real asymmetry — a signal is
one bit, while only a human can reject *with reasons*. Naming the two outcomes
keeps that visible. See §10 for why general AND/OR gates are not the answer.

### action

```yaml
- id: mr
  type: action
  action: create_mr                     # a slug from the compiled-in registry
  input: { body: artifacts/mr.md }      # bind the action's signature
```

Actions are apothecary's built-in integrations. Unlike agents they are compiled
in, so an unknown `action:` is a **parse** error and its input names are checked
immediately. Registry (`GET /api/actions`):

| action | requires | inputs | outputs | notes |
|---|---|---|---|---|
| `create_mr` | `forge` | `body` (file, required) | `mr_url` (value → `$MR_URL`) | at most one per flow; the body's first `# ` heading becomes the MR title |

Actions carry no *failure* edges in this version: `on_success` routes normally,
and a failure is terminal. Adding an action is one registry entry, one
executor, and (if it needs an integration) a `requires` string that run
creation asserts.

**Side effects and interruption.** An action's effect lands outside the run, so
the engine records an `action_started` intent event *before* the call. If the
process dies between the effect landing and its completion event, recovery sees
that intent alone and **reconciles** rather than re-running: `create_mr` asks
the forge what the run's branch already has (the branch is unique per run, so
it is the idempotency key) and adopts an existing merge request instead of
opening a second one. A reconciliation that cannot reach the forge fails the
stage rather than guessing — a duplicate MR is worse than a retry.

### wait

```yaml
- id: merged
  type: wait
  for: mr_merged        # a slug from the signal registry
  timeout: 24h          # optional; absent waits indefinitely
  on_timeout: { to: nudge, max: 1 }
```

A signal is something that happens *to* the run from outside it. Like a gate, a
`wait` parks the run on something beyond its control and needs no workspace to
do it — so with no `timeout` it waits indefinitely, and a bounded one routes
`on_timeout` (else `on_fail`). Registry (`GET /api/signals`):

| signal | requires | needs | fires when |
|---|---|---|---|
| `mr_merged` | `forge` | a merge request | the run's MR is merged on the forge |

`needs` is a prerequisite *within the run*: waiting for `mr_merged` in a flow
that never opens an MR fails with `input_missing` and says so, rather than
hanging forever on something that cannot happen.

| signal | requires | needs | fires when |
|---|---|---|---|
| `mr_closed` | `forge` | a merge request | the run's MR is closed *without* being merged |

Three related concepts, kept distinct: an **action** does something now, a
**wait** blocks until something happens, and a *trigger* — starting a run from
an outside event — is deliberately not in this version (§10).

### Timeouts

One field, `timeout`, whose meaning follows the stage type:

| type | what `timeout` bounds | absent means |
|---|---|---|
| `agent` | execution wall clock, suspended while an ask_human ping is open (§9) | `defaults.timeout` (30m) |
| `command` | execution wall clock | `defaults.timeout` |
| `wait` | how long to block on the signal | wait indefinitely |
| `human_gate` | — (the field does not apply) | bounded by human attention |
| `action` | — (the field does not apply) | bounded by its client |

A gate and an action take no timeout on purpose: a gate parks on a person, who
is the bound, and an action is a single call that either returns or fails.

**Signals are state predicates, not events.** Each is re-checked by polling
current state (`is the MR merged?`), never consumed as a one-shot notification.
Two consequences worth relying on: nothing is missed because the engine was down
when it happened, and **sequential waits compose as AND regardless of order** —

```yaml
- { id: green,  type: wait, for: ci_green,  on_success: merged }
- { id: merged, type: wait, for: mr_merged, on_success: deploy }
```

If the MR merges before CI goes green, the second wait still passes. So "wait for
both" needs no grammar of its own.

---

## §3 Control flow

```yaml
on_success: build            # a stage id, or `done`
on_fail:    { to: build, max: 5, then: { to: debug, max: -1 } }
on_reject:  { to: build, max: 3 }
on_timeout: { to: test,  max: 1 }
on_reject:  abort            # scalar sugar for the deliberate stop
```

**`on_success`** is a plain target with no counter. Absent, it means "the next
stage in document order"; on the last stage, `done`. Every stage's success chain
must reach `done` — a success-only cycle is a parse error, because nothing would
bound it (a failing command costs no turns and no dollars).

**Edges** are the recoverable redirects. Each carries its own `max`, and when a
link is exhausted the *next* failure falls through to its `then` — retry, then
escalate. An exhausted chain with no `then` abandons the run.

**What `max` counts.** A counter belongs to one *link* — the triple
(source stage, edge kind, position in the `then` chain) — and counts how many
times that link has been taken **over the whole run**. It does not reset when
the source stage later succeeds: `max` bounds total spend on a loop, which is
what makes it a budget rather than a per-visit allowance. Counters are
persisted in the `edge_taken` events, so they survive a restart exactly, and
pause/play leaves them untouched. **"Try again"** (`run_resumed`) is the only
thing that clears them: a human deciding to retry a dead run restores every
stage's budget from the resume point.

There is deliberately no backoff: every agent/command loop must carry a finite
`max` or a human gate on the link, so no edge can spin unbounded.

| kind | raised when | allowed on | carries |
|---|---|---|---|
| `on_fail` | the stage failed | agent, command, wait | `$FAILURE` |
| `on_reject` | a verdict said REJECT, or a human rejected a gate | agent, human_gate | `$DECISION` |
| `on_timeout` | the stage exceeded its timeout | agent, command, wait | `$FAILURE` |

A timeout uses `on_timeout` when the stage declares one, else `on_fail`.

**An edge does not declare what it carries.** Its kind already decides that, so
saying it again could only disagree. What it carries is available to its target
as a sentinel, and the target binds it like any other input (§4) — or ignores
it. That is the single declaration: *where it goes* lives with the stage that
reads it, next to everything else that stage reads.

**Terminals.** `done` completes the run (only `on_success` may reach it).
`abort` deliberately stops it; an abort link takes no `max`, `feed` or `then`.
Stage ids may not shadow `done`, `abort` or `start`.

A run ends in one of three states, and the distinction is reporting, not
capability (`resume` accepts the last two):

| state | reached by |
|---|---|
| `completed` | `on_success` reached `done` |
| `abandoned` | a deliberate stop — the flow's `abort` terminal, a person pressing abort — or an exhausted retry chain / spent budget |
| `failed` | the engine's own fault: infrastructure, an unresolvable project, a flow that no longer parses |

Aborting is abandonment, not failure: a person saying "stop" is a decision, and
reporting it as a breakage would misstate the run's history.

**`max: -1`** (unlimited) requires a `human_gate` on the link — as its source
(the link only fires when a human rejects) or its target (each firing parks on a
human decision). Either way a person is spending attention per iteration.
Links between agents and commands must carry a finite `max`.

---

## §4 Dataflow

Stages exchange work through files in the workspace. A path is the rendezvous:
one stage writes `artifacts/plan.md`, another declares it reads
`artifacts/plan.md`, and that is the connection. Nothing is passed by value and
nothing is interpolated.

### One binding form

Every binding, on every stage type, is `name: value`:

```yaml
input:
  plan: artifacts/plan.md
  changes: $CHANGED
```

What differs is only how the **name** is checked, which follows from the type:

| type | `input` names | `output` names |
|---|---|---|
| `agent` | must be a declared input | must be a declared output (rebinds its path) |
| `action` | must be in the action's signature | — |
| `command` | free labels | free labels |
| `human_gate` | free label, exactly one binding | — |

### Sentinels

A `$NAME` value is provided by the engine; anything else is a repo-relative
artifact path (no leading `/`, no `.` or `..` segments).

| sentinel | is | bindable on |
|---|---|---|
| `$CHANGED` | `git diff --name-status` of the branch against **the branch this run forked from** — the changed-file list, never the full diff | agent input |
| `$DECISION` | why the last attempt was turned down: a verdict's comments, or a human's gate feedback | agent input |
| `$FAILURE` | what the failing stage printed: the last 8 KB, with every secret the engine injected replaced by `[redacted:<kind>]` | agent input |
| `$MR_URL` | the run's merge request link, produced by `create_mr` | gate input |
| `$WORKSPACE` | the workspace's access link (terminal, else IDE) | gate input |

The scoping is enforced: an agent cannot bind `$MR_URL` (a link is for a human
to click, not for a prompt to read), and a gate cannot bind `$CHANGED` (prompt
content is not something to show a human).

**`$DECISION` and `$FAILURE`** are the retry channel. They hold whatever the
edge that re-entered this stage brought with it, so they are empty on a first
attempt — which is why an input bound to one must be declared `optional`. Only
one can be filled at a time (the edge kind decides which), and an agent that
binds both can tell "the reviewer turned you down" apart from "the tests broke":

```yaml
- id: build
  type: agent
  agent: builder
  input:
    plan: artifacts/plan.md
    rejection: $DECISION      # reviewer comments, or a human's gate feedback
    failure: $FAILURE         # test output, a command's error
```

The names are the agent's own — there is no reserved slot.

Why two sentinels rather than one `$FEEDBACK`? Three reasons, in ascending
order of importance:

1. **The prompt says which setback this was.** A stage can be re-entered by
   different kinds of edge — in the default flow `build` is the target of both
   `test.on_fail` and `review.on_reject`. One slot would make the agent infer
   the situation from the text; two label it.
2. **The agent can instruct differently per slot.** "If you got a rejection,
   address every point first; if you got a failure, reproduce it first" needs two
   descriptions, and a description belongs to an input.
3. **It removed a second declaration.** With one slot the *edge* would still
   have to say what to send (the target could not tell the difference), so the
   old `feed:` field would survive alongside the binding — two statements of one
   fact. With two sentinels the edge kind alone decides which fills, and `feed:`
   became redundant. It is also what lets the validator say *"you bind
   `$DECISION` but only `on_fail` edges reach you, so nothing will ever fill
   it"*, which one undifferentiated slot cannot express.

### Agent contracts

An agent declares its own I/O in the flow's `agents:` block (§1); a stage binds
it. Because both halves are in one document, they cannot drift: a binding that
violates a contract is a parse error.

The prompt an agent receives is assembled, never templated: a `# TASK` section
from the ticket, one `# INPUT: <name> — <description> (file: <path>)` section
per bound input, and an `# OUTPUT CONTRACT` listing every file it must write.

**Declared outputs must be written by the attempt that succeeds.** Before every
fresh attempt the engine deletes the stage's declared outputs, so the check
after it cannot be satisfied by a file an earlier attempt left behind — a
retry that "succeeds" without producing anything fails with `output_contract`,
naming every missing file at once. (Crash recovery re-entering a live stage
does *not* delete: a resumed harness session is mid-work and its partial
outputs must survive.)

**A `verdict` output** holds `{"verdict":"APPROVE"|"REJECT","comments":[…]}`
(comments required on REJECT). It is a file like any other — its existence is
enforced with the rest — plus a shape the engine parses: a REJECT follows the
stage's `on_reject` edge, carrying the comments as `decision`. A file that
exists but does not hold that shape fails the stage with `output_invalid`,
which is a different fault from never writing it. Declaring a verdict output is
what makes `on_reject` reachable; a stage needs no flag of its own, and an
agent may declare at most one — a stage whose agent declares none cannot carry
an `on_reject` edge at all (a parse error).

### What the editor draws

Because consumption and production are declared, the graph editor derives the
dataflow rather than storing it: each stage shows an input dot per thing it
reads and an output dot per thing it writes, and a dashed line connects a
producer's dot to a consumer's dot whenever their paths match. Those lines are
read-only by construction — you rewire them by editing a path, not by dragging.
Control-flow edges (the colored ones) are the draggable layer, and one of them
lands directly on the `$DECISION` or `$FAILURE` dot that receives its content —
so the retry path is visible too.

---

## §5 Failure

A stage failure records a reason, then follows an edge:

| reason | meaning |
|---|---|
| `exit_nonzero` | a command exited non-zero |
| `timeout` | the stage exceeded its timeout |
| `harness_error` | the harness itself failed |
| `input_missing` | a path the stage declared it consumes was not there — upstream wiring, not this stage's work |
| `output_contract` | the stage finished without writing what it declared (its outputs are cleared before each fresh attempt, so a stale file cannot satisfy it) |
| `output_invalid` | a declared output exists but does not hold what its type promises — a malformed verdict |
| `infra` | provisioning, forge or other infrastructure failure |

The recommended shape, and the one the built-in default flow uses: every agent
and command stage first retries itself with its error fed back, then escalates
to its own rescue gate showing `$WORKSPACE`. A human opens the workspace, fixes
what the agents could not, and approving re-runs exactly the stage that
escalated — the success chain replays from there, so no stage is skipped.
Rescue gates are unlimited (`max: -1`): the human decides when to stop, and
rejecting one aborts the run.

Budget exhaustion and cancellation are **run-level** outcomes, not stage
failures: exceeding `budget` abandons the run, and abort/pause act on the run
as a whole (§3).

**Every attempt leaves a record.** As each attempt ends — succeeded or failed —
the engine captures its declared outputs and its full log into the artifact
store and links them to the run's timeline with `artifact_published` events.
The events carry references, never bytes, so they stay replay-cheap while the
content can be any size. Two consequences worth relying on: attempt 1's plan
stays readable after attempt 2 overwrites the file, and the 8 KB tails in
`stage_failed` / `$FAILURE` are a *preview* — the whole log is one link away.
Snapshots outlive the run they describe (archiving keeps them) and are scrubbed
of engine-injected secrets on the way in.

---

## §6 Validation

Two levels, separated by what each one can know. (There were three until
agents moved into the document; the middle one — contract checking that needed
a project's agent library — became parse errors.)

**Parse errors** (the document) — refuse it. Unknown fields and values, fields
that do not apply to a stage's type, unknown action slugs, unbound required
action inputs, malformed edges, success cycles, unreachable stages, `max: -1`
without a gate on the link — and every **contract** violation: an agent a stage
names but the flow does not declare, an input the agent does not declare, a
required input left unbound, a non-optional input bound to `$DECISION`/
`$FAILURE`, an output rebind of something undeclared, `on_reject` on an agent
with no verdict output. A flow cannot be saved with any of these.

**Warnings** — advisory, returned alongside a successful parse by
`POST /api/flows/validate` and never blocking anything. These are legal
constructs that look like mistakes: a path no stage produces, a path produced
on only *some* of the routes that reach its consumer, a feed sentinel no edge
can fill, a `wait` on a signal whose prerequisite isn't opened on every route
reaching it.

**Preflight errors** (this project) — refuse to *create a run*. Checked with the
project's real configuration: `run_ref`s exist in its commands, a pinned
harness is in its set, an action's required integration is configured, the repo
has a devcontainer and an `AGENTS.md`.

A flow whose stored text no longer parses fails its runs terminally with the
parse error — retrying cannot fix a grammar problem.

### Document rules

Normative, and enforced by the parser:

- YAML 1.2 through a strict decoder: **unknown fields are rejected**, and so
  are **duplicate keys** (including inside the hand-written edge and binding
  unmarshalers).
- **No anchors or aliases** (`&name` / `*name`) anywhere. They are legal YAML
  but would not survive the editor's canonical re-emission, and they hide a
  stage's real shape from a reader.
- **One document per file**: anything after a `---` separator is refused rather
  than silently ignored.
- Limits: 1 MiB per document, 128 stages, 64 agents, `then` chains at most 8
  links deep.
- **Paths** are repo-root-relative POSIX paths: no leading `/`, no `.` or `..`
  segment, no leading `$` (that space belongs to sentinels). Beyond the
  lexical rule, the engine resolves symlinks before reading or writing an
  artifact and refuses anything landing outside the checkout — so an
  agent-authored `artifacts/plan.md -> /apothecary/run.env` cannot smuggle the
  run token into a prompt.

---

## §7 Retired spellings

The parser answers each removed v1 spelling with its replacement rather than a
bare "unknown field":

| old | now |
|---|---|
| `verdict: true` on a stage | declare a `type: verdict` output on the agent |
| `type: forge_mr` + `body:` | `type: action, action: create_mr, input: {body: …}` |
| `ping: plan_approval\|final_review\|manual_debug` | `question:` free text |
| `payload:` on a gate | `input: {name: …}` |
| `auto_complete:` | `auto_approve:` (or a `wait` stage to just block) |
| `auto_answer:` | `auto_approve:` / `auto_reject:` — say which answer it supplies |
| `feed: output` / `feed: decision` on an edge | bind `$FAILURE` / `$DECISION` as an input on the target |
| a reserved `feedback` input | any name, bound to `$DECISION` or `$FAILURE` |
| `mr_url` / `workspace` | `$MR_URL` / `$WORKSPACE` |
| `input: [a, b]` or `input: path` | `input: {name: a, other: b}` — name every entry |
| `input: [ticket, …]` on an agent | drop it — the ticket is always the task (§1 run context) |
| `version: 2` with agents in the project library | define them in the flow's `agents:` block and set `version: 3` |
| a project-level agent library (`/api/projects/{id}/agents`) | the `agents:` block of each flow that runs them |

---

## §8 Annotated example

The built-in default flow, abridged — plan-gated build with cross-vendor
adversarial review and per-stage rescue. Full text:
`internal/builtin/flows/default.yaml`.

```yaml
name: default
version: 3
defaults: { timeout: 30m, harness: opencode, model: opencode-go/qwen3.5-plus, max_turns: 50 }
budget: { max_cost_usd: 10.0, max_total_turns: 300 }

# The agents this flow runs, abridged to two. Their contracts are what the
# stages below bind, and what the parser checks those bindings against.
agents:
  - name: planner
    description: explores the repository and writes the implementation plan
    read_only: true
    inputs:
      - { name: rejection, optional: true, description: "why the last plan was rejected" }
      - { name: failure,   optional: true, description: "what the previous attempt printed" }
    outputs:
      - { name: plan, path: artifacts/plan.md, description: "the implementation plan" }
    prompt: |
      You are the planning agent of a software factory...
  - name: reviewer
    description: adversarial code reviewer
    read_only: true
    allow_bash: true
    inputs:
      - { name: plan,    description: "the approved plan to check the change set against" }
      - { name: changes, description: "the set of changed files to review" }
      - { name: failure, optional: true, description: "what the previous attempt printed" }
    outputs:
      # A verdict output is what makes review.on_reject below legal at all.
      - { name: verdict, type: verdict, path: artifacts/verdict.json,
          description: "APPROVE or REJECT with actionable comments" }
    prompt: |
      You are the adversarial reviewer of a software factory...

stages:
  # No output binding: the planner's contract says its plan goes to
  # artifacts/plan.md. It binds both retry values, so being sent back — by a
  # human rejecting the plan, or by its own failure — arrives named.
  - { id: plan, type: agent, agent: planner, max_turns: 25,
      input: { rejection: $DECISION, failure: $FAILURE },
      on_success: plan_gate,
      on_fail: { to: plan, max: 2, then: { to: debug_plan, max: -1 } } }

  - { id: debug_plan, type: human_gate, input: { workspace: $WORKSPACE },
      question: "Planning failed repeatedly. The workspace is parked — fix what the
                 agent could not, then approve to re-run planning (reject aborts).",
      on_success: plan, on_reject: abort }

  # Rejecting sends the plan back; the planner reads the human's words as its
  # `rejection` input. The edge itself says nothing about that.
  - { id: plan_gate, type: human_gate, input: { plan: artifacts/plan.md },
      question: "Approve this implementation plan?",
      on_success: build,
      on_reject: { to: plan, max: -1 } }

  - { id: build, type: agent, agent: builder, max_turns: 80,
      input: { plan: artifacts/plan.md, rejection: $DECISION, failure: $FAILURE },
      on_success: commit_msg,
      on_fail: { to: build, max: 2, then: { to: debug_build, max: -1 } } }

  # The committer writes artifacts/commit.md (its contract); the commit stage
  # declares it reads that file, so the dataflow is visible instead of buried
  # in the script. The label `message` says what the file is for.
  - { id: commit, type: command,
      run: "git add -A && if ! git diff --cached --quiet; then git commit -F artifacts/commit.md; fi",
      input: { message: artifacts/commit.md },
      on_success: test, on_fail: { to: debug_commit, max: -1 } }

  # A different vendor reviews. The reviewer's verdict output is what makes
  # on_reject reachable; its comments reach the builder's `rejection` input.
  - { id: review, type: agent, agent: reviewer, model: opencode-go/glm-5.1,
      input: { plan: artifacts/plan.md, changes: $CHANGED, failure: $FAILURE }, max_turns: 30,
      on_success: redact,
      on_reject: { to: build, max: 3 },
      on_fail: { to: review, max: 1, then: { to: debug_review, max: -1 } } }

  - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md },
      on_success: final_gate }

  # $MR_URL comes from the action above. The forge can answer this gate either
  # way — merging is a yes, closing it unmerged is a no — and so can a human.
  # Whoever gets there first decides.
  - { id: final_gate, type: human_gate, input: { mr: $MR_URL },
      question: "Review the merge request and merge when satisfied.",
      auto_approve: mr_merged,
      auto_reject: mr_closed,
      on_success: done, on_reject: abort }
```

## §9 Agent-side interactions (ask_human)

A stage's agent can stop and ask. This is not a stage type and needs no
grammar — it is a property of every agent stage, and it is worth stating
because it changes how a stage behaves in flight.

The agent calls `ask_human{type, question, context?, options?, artifact?}`
through an MCP tool in its workspace. The engine, not the harness, owns the
exchange:

- **The request is an event.** `ping_raised` records the type
  (`clarification` | `blocker` | `permission`), the question, any attached
  workspace files, the declared answer options, and the stage in flight; the
  answer is `ping_answered`. Both are durable, so an interrupted run re-arms
  its open questions on recovery and a late answer still lands.
- **The stage keeps running; its clock does not.** The agent process stays
  alive waiting for its tool call to return, and the stage's `timeout` is
  **suspended** for as long as any ping is open — a question a human takes an
  hour to answer cannot time the stage out. The run projects as
  `waiting_on_human`.
- **`options` make the answer closed.** When the agent declares them, the
  answer must be one of them (or the reserved `other`, whose free text rides
  in the feedback) — the agent asked a closed question so it can branch
  deterministically.
- **Answers can carry files**, delivered into the workspace under
  `.apothecary/artifacts/`, kept separate from the agent's own `artifacts/`.
- If the engine is unreachable for more than 10 minutes the tool returns an
  error string and the agent proceeds — a broken control plane stalls the work
  but never wedges it forever.

A gate (§2) and an ask_human ping are the same machinery pointed the other way:
a gate is the *flow* stopping to ask at a declared point, a ping is the *agent*
stopping to ask at a point only it can know. Both are pings in the event
stream and the same inbox.

The retired `ping:` field on a gate (§7) named a fixed set of gate kinds; that
enum is gone, but the interaction it was named after is very much alive.

---

## §10 Extending the language

Additive, no grammar break:

- **An action** — one `actionspec` registry entry, one executor case, one
  `requires` port string. Everything else (parsing, binding, the editor's
  picker, the dataflow view) follows from the signature.
- **A signal** — one `signalspec` registry entry plus a poller. `wait` and a
  gate's `auto_approve`/`auto_reject` pick it up for free.
- **A sentinel** — one constant plus one resolution case. The `$NAME` rule
  already tells authors it is engine-provided.
- **An output type** — like `verdict`: a value on an output's `type` plus the
  engine behaviour it implies.
- **An edge kind** — one `DocStage` field, one row in the edge registry, one
  raise site. Parsing, validation, routing and the `edge_taken` event
  generalize.
- **A grammar break** — bump `CurrentVersion`. Every document states its
  version, so old text is refused with a message rather than mis-read (v3 did
  exactly this when agents moved into the flow).

Deliberately *not* in this version, each a real change rather than a patch:

- **Triggers** — starting a run *from* an outside event. That needs a
  subscription path, a rule mapping an event to a ticket and project,
  idempotency, and an auth model: a feature with a security surface, not a
  stage type. A future top-level `triggers:` key is purely additive.
- **Parallel stages** — the engine advances one cursor. Fan-out/join would be an
  engine rework (state machine, replay, budgets), not a grammar patch.
- **Routing on arbitrary structured output** — the edge kinds are fixed; a
  verdict is the one structured decision.
- **General value-passing or templating** — sentinels are the only
  engine-value channel, on purpose.

### Why no AND/OR combinators

A gate answered by a signal *is* an OR, so a general `all_of:` / `any_of:` looks
like the natural generalization. It is not, for three separate reasons:

- **AND already exists.** Signals are state predicates, so sequential `wait`
  stages compose as AND in any order (§2 wait). An `all_of:` would be a second
  way to write something the language already expresses.
- **OR over conditions is narrow; OR over stages is the parallelism rework.**
  First-one-wins across two polls is cheap. First-one-wins across two stages
  that *do work* needs two cursors, two event streams, replay and cancellation
  of the loser. A general `any_of:` would invite the second and deliver only the
  first.
- **The branches are not symmetric.** A signal can only say yes or no; a human
  can say no *and why*. `any_of: [mr_merged, human_approve]` would give a stage
  an `on_reject` edge and a `$DECISION` only one branch could ever reach. Naming
  the two outcomes (`auto_approve`, `auto_reject`) puts that asymmetry in the
  open instead.

Nor is human approval itself modelled as a signal (`for: human_approve`): a gate
is not a condition but an interactive object — a question, a payload to review,
an attachment channel, a reject-with-reasons path, a place in someone's inbox.
Expressing it as a signal would force the `wait` stage to grow every one of
those fields, dissolving the type distinctions this grammar exists to keep.

When a second approving signal appears, the additive move is to let
`auto_approve` take a list — an OR in the one place it is unambiguous. The likely
*next* need after that is not a boolean gate at all but multi-outcome routing
("which signal fired?"), which the edge-kind registry already accommodates.
