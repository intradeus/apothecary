# The apothecary flow language

A **flow** is a YAML document describing how a ticket becomes a merge request
and what follows: which agents run, in what order, what each one reads and
writes, where a human decides, what happens when something fails — and what
the run does once the merge request is open, until it merges.

This document is the normative reference. Code cites it as `FLOWLANG §n`.

Four rules shape everything below:

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
   workspace is shared because the engine runs one work stage at a time (*The
   model*, below); that is the design, not a gap.
2. **Values are strict, types are extensible.** Every field takes a closed set
   of values, so a typo is a parse error rather than a surprise at 3am. New
   stage types, actions, signals, sentinels and edge kinds are added by
   extending a registry, not by loosening the grammar.
3. **Each fact is stated once, where it belongs.** An edge says where the run
   goes; the stage it lands on says what to do with what arrived. An agent
   declares what it needs; the stage that runs it says where to find it.
4. **A flow is valid on its own; a project binds its parameters.** The
   document declares every knob with a default and parses and runs as written.
   What varies between projects — the harness, the test command, how a review
   round begins, where the tracker issue goes — is a parameter the workshop
   binds *outside* the document, and a run may override. Nothing
   project-specific is written into the YAML, so one flow fits a GitHub
   project with no tracker and a GitLab + Jira one alike.

---

## The model

A flow is read by one scheduler over one workspace. Everything in the grammar
is one of a few things:

| piece | definition |
|---|---|
| **work stage** | `agent`, `command`, `action`. Runs in the workspace — one checkout, one branch — and at most one runs at any instant: the workspace is a mutex, which is what keeps the git history linear. |
| **wait point** | Where a chain stops for something outside the run. It has a **source** (a signal from the registry, `run_start`, a person, a timer), a **policy** (`auto`: proceed when the source holds; `confirm`: raise a decision first; `manual`: only a person proceeds, the event is advice), an optional **subject** the event carries (which merge request), an occurrence **key** (the same fact never fires twice), a run bound (`max`), optionally a debounce (`quiet_for`) and a `timeout`, and **outcomes**: proceed, declined, can no longer happen, timed out, rejected with feedback. A wait point releases the workspace — unless it is one that owns it: an agent's `ask_human` turn (§9), the `debug` leaf (§3). |
| **its spellings** | A watcher's or an outcome's `on:` is its opening wait point. `type: wait` is one mid-chain, on a signal. `type: human_gate` is one mid-chain whose first source is a person, with `auto_approve` / `auto_reject` as extra signal sources. The confirmation ping in the inbox and the Run button on the run page are the `confirm` and `manual` policies of the same thing. |
| **chain** | A sequential list of stages: the **main** chain a run starts with, a **watcher** that runs on each new occurrence of its signal while the run is open, an **outcome** that fires once and ends the run (§1). Each is a *phase* of the run, with at most one **live run** at a time — active, or parked at a wait point inside it; each owns its cursor, its edge counters, its feed and its tip. |
| **scheduler** | The engine's idle loop. Each tick: observe the sources (the repositories, timers, answered pings), then walk every wait point in document order — parked ones, outcomes, watchers — and resume or open the first whose event is ready under its policy. That chain run takes the workspace and runs work stages until its next wait point or its end. One start per tick. |
| **run end** | Two ends, said apart. `end` finishes a **chain**: it is over and the run goes back to waiting. `done` finishes the **run**, whatever else waits; `abort` abandons it. A flow must have some route to `done` (§3). A run normally ends in an **outcome**, on what became of the merge request (`mr_merged`, `mr_closed`): it fires once and ends the run — the `builder` template's `merged` moves the ticket to done and says `done`, its `closed` says `abort`. Without one the run **completes** when no chain is active, none is parked, and nothing is **armed** — an outcome that has not fired while a merge request is open; a watcher with policy `auto` or `confirm`, its signal live for an open merge request, runs left. A dead end — a failure with nowhere to go — abandons the run in the main chain or an outcome; in a watcher it ends that round (§3). |
| **extension points** | A new thing to react to is one signal plus its poller: it works for watchers, outcomes and mid-chain waits, under every policy, with subjects, and the scheduler does not change. A new kind of work is a stage type and an executor. A new human interaction is a gate variant. A new way to begin runs is an automation outside the run, starting a flow whose `run_start` fits it. Parallelism is deliberately absent: the mutex is the design (§10). |

---

## §1 Document

```yaml
name: builder              # required, slug: the flow's permanent id
version: 5                 # required int: the grammar this document is written in
description: "..."         # optional
run_start: ticket          # what runs are started on: ticket (default) | merge_request (below)
parameters:                # the flow's ONE settings block (below): every entry is a knob
  harness: opencode        #   required: the harness kind agent stages run on
  model: provider/model    #   required: the model they run, unless a stage pins another
  timeout: 30m             #   wall clock per agent/command stage (suspended while a ping waits)
  max_turns: 50            #   per agent stage
  max_cost_usd: 10.0       #   run-wide ceilings; exceeding either abandons the run
  max_total_turns: 300
  verify: verify           #   the flow's own knobs, read where $verify appears
agents: [...]              # the agents this flow runs — definitions, not references
sequences: [...]           # optional: chains of stages written once, run where a stage names them (below)
main: [...]                # the chain a run starts with (below)
watchers: [...]            # optional: chains run on what happens to the merge requests while the run is open
outcomes: [...]            # optional: what ends the run, each firing once
```

`version` is required and must equal the version this build speaks (currently
**5**). Any other document is refused with `this build speaks version 5`
instead of being half-understood — a version-4 one with a word on where its
phases went. That is the entire purpose of the field: it is the seam a
breaking change hangs off.

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
        type: verdict                   # "" (a plain file) | verdict | review_replies | review_threads
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
the repo's own `AGENTS.md` — and the values it binds to the flow's parameters.

**Determinism and pinning.** A flow's identity is the SHA-256 of its raw bytes.
A run records that hash *and the parameter values it was created with* at
creation, and **executes that exact version, with those values, for its whole
life** — recovery after a restart re-reads the same pinned document and
resolves it with the same values, so a flow edited mid-run (including a prompt
inside it) or a binding changed in the workshop cannot change a run already
going. The one deliberate exception is **"try again"** (`run_retried`): resume
is the human-initiated repair moment, so it re-pins the run to the flow's
current version — when that version still resolves with the run's values; fix
a prompt or a binding, then resume. Pause/play and crash recovery never re-pin.

Project-side dependencies are resolved live and audited per attempt instead:
`run_ref`s and integrations are looked up when used, and every attempt records
`stage_started.agent_hash`, the content address of the exact agent definition
that produced it.

The graph editor re-emits a canonical form — agents before stages, stages in
success order, an explicit `on_success` on every stage, no comments — and
`docToYaml ∘ parse` is a fixed point, so opening and saving an already-canonical
flow produces no change.

**Where flows live.** A flow belongs to one project — its stages name that
project's commands and its agents know that repository — or it is a
compiled-in **template** that any project runs as it is until it saves its own
copy. Harness kinds, `run_ref`s and action integrations are *free text* at
parse time and validated against the project when a run is created (§6); what
differs between projects beyond that is a parameter, bound per project without
touching the document.

### Parameters

`parameters:` is the flow's one settings block. Every entry declares a knob
with its default, and the document parses and runs on those defaults alone.
The *values* a run uses come from outside the document, in layers:

1. the flow's default;
2. the **project's binding** — Workshop → Flows → Configure. Stored beside the
   flow, never in it: the document and its hash are untouched, and a template
   is configurable without being copied;
3. the **run's override** — the start-run form's *Advanced* section, or the
   automation that created the run.

The resolved values are recorded on `run_created`, and the run executes with
them for its whole life.

Six names are the engine's own. They always exist, declared or not, and apply
without being referenced anywhere:

| name | type | default | read by |
|---|---|---|---|
| `harness` | string, a harness kind | *required* | every agent stage that pins none |
| `model` | string, `provider/model` | *required* | every agent stage that pins none |
| `timeout` | duration | `30m` | every agent and command stage that declares none |
| `max_turns` | int | `50` | every agent stage that declares none |
| `max_cost_usd` | float | `10` | the run: exceeding it abandons the run |
| `max_total_turns` | int | `300` | the run: exceeding it abandons the run |

The flow's own knobs are any other slug name. A scalar is the default; the
long form adds a description and, for a string, an enum:

```yaml
parameters:
  harness: opencode
  model: opencode-go/qwen3.7-plus
  verify:          { default: verify, description: "the project command that runs the tests" }
  review_model:    { default: opencode-go/glm-5.2 }
  plan_gate:       { default: true }
  conflicts_start: { default: confirm, enum: [auto, confirm, manual] }
```

A parameter's **type** is read from its default: `true`/`false` is a bool, an
integer an int, a decimal a float, anything else a string. A bound value must
parse as that type and, with an enum, be one of its values. The engine-known
names cannot be declared as knobs of another shape, and the editor pins them
at the top of the block.

**Where a parameter is read.** `$name` — lower-case; `$UPPER` is a sentinel
(§4) — at these sites only:

| site | the parameter it takes |
|---|---|
| an agent stage's `model`, `harness`, `max_turns`, `timeout`; a command's `run_ref`, `timeout` | a string; an int; a string that parses as a duration |
| an action input | a string — an enum input needs an enum parameter whose values all sit within the input's |
| a watcher's or an outcome's `on.start`, a watcher's `on.quiet_for`, `on.max` | an enum within `auto \| confirm \| manual`; a string; an int |
| a stage's `when` | a bool |

Never in a prompt, a stage id, a path or a `run:` script. The parser checks a
reference the way it checks a literal — the resolved value goes through the
same validators, with the parameter named in the error — and warns about a
parameter nothing reads.

**Resolution.** Binding is a re-parse of the pinned bytes with the values, so
parse-time and resolve-time validation are one code path. A binding that
breaks a rule — a `run_ref` that is no command slug, a model without a
provider, a duration that does not parse — is refused where it is bound:
in the workshop's sheet, or at run creation as `preflight: parameters: …`.
Never discovered mid-run. A project binding naming a parameter the flow no
longer declares is dropped with a warning; a run override naming one refuses
the run.

### Main, watchers, outcomes

A flow says what its runs are started on and what they do, in three
sections, each a list of chains of stages (§2):

- **`main`** — the chain a run starts with. A person or an automation starts
  the run; nothing the flow listens for does.
- **`watchers`** — chains that run while the run is open, on each new
  occurrence of a signal on one of its merge requests, or when a person starts
  them: one at a time, in the run's workspace. A watcher never ends the run.
- **`outcomes`** — what ends the run: each fires once, on what became of the
  merge request.

```yaml
run_start: ticket                                        # what runs are started on: ticket (default) | merge_request
main:                                                    # runs once, when the run is started
  - { id: in_progress, … }                               # in_progress, plan … mr, in_review — ends: the run is in review
watchers:
  - id: conflicts
    on: { signal: mr_conflicts, start: $conflicts_start } # the target moved; by itself, after asking, or by hand
    stages: [ sync, resolve, sync_test, sync_push ]
  - id: review_fixes
    on: { signal: mr_review_settled, start: auto, quiet_for: 1h }
    stages: [ load, fix, …, reply, post ]
  - id: polish
    on: manual                                           # only when someone starts it from the run page
    stages: [ tidy, push ]
outcomes:
  - id: merged
    on: mr_merged                                        # once, and the run is done
    stages: [ ticket_done ]
  - id: closed
    on: mr_closed                                        # once: the ticket goes back, the run is abandoned
    then: abort
    stages: [ ticket_back ]
```

Each chain is one **phase** of the run: the main chain is the phase `main`, a
watcher or an outcome the phase its `id` names — watchers and outcomes share
one namespace, and `main` is taken.

- **`run_start`** says what the flow's runs are started on: `ticket`, the
  default — from the ticket's page, or by a `tracker_query` or `cron`
  automation — or `merge_request`, a merge request someone opened — named on
  the start-run form (the open ones are listed), picked on the Repositories
  tab, or by a `merge_request` automation. Starting a run is always
  someone's decision; the automations are the listeners. A run started on
  the other kind of thing is refused.
- **The main chain** runs once, when the run is started; its
  `phase_started` records who started it (`by: human`, or the automation). It
  is never started again — a rerun is a new run. Its last stage ends it with
  `end` when the flow has watchers or outcomes (the run stays open), and with
  `done` when it has neither (the run is over). A flow with no `main` only
  watches — a *caretaker* — and must be `run_start: merge_request`: there is
  nothing to watch on a bare ticket. Its runs start idle.
- **On someone's merge request.** With `run_start: merge_request` the run
  works on that merge request's branch, checked out from the ref its
  repository keeps for the head (`refs/pull/<n>/head`,
  `refs/merge-requests/<iid>/head` — a fork's too, on a local `mr-<iid>`
  there is no pushing to). The merge request is the run's from the start: the
  main chain acts on it as its subject (no `create_mr`, no `when: subject`
  needed), `$DIFF_TARGET` is what it merges, each later chain on it first
  fast-forwards to its current head, and the outcomes follow its fate. A
  rerun stays on the merge request. The built-in `reviewer` is this shape: one
  `review` sequence (read, an optional gate, `post_review`) run by its main chain
  and again by the watcher `again` on `mr_head_moved`, and outcomes on
  `mr_merged` and `mr_closed`. `conflict-resolver` is the same shape around a
  `settle` sequence — `sync_target`, the `resolver` agent on a conflict, the
  project's tests, `push_branch` to the merge request's own branch, leased
  against the head the chain began on — run by its main chain and again by a
  watcher on `mr_conflicts` (`start: $again_start`, `confirm` by default). A
  fork's merge request cannot be pushed to: its push fails to `debug`.
- **Watchers.** `on:` a signal the engine observes (§2 *wait*), or `manual`.
  An observed signal runs the watcher on the merge request its occurrence
  carries — the chain's *subject* — under its `start` policy. The order
  watchers are written in is *not* a sequence: a conflict can be fixed before,
  after or between two review rounds, as often as the signal brings something
  new. A watcher never finishes the run: its chain closes with `end` and the
  run goes back to waiting; a failure in it with nowhere to go ends that
  round, not the run (§3). `on: manual` is a watcher only a person starts,
  from the run page while the run is idle; any watcher without a live run can
  be started the same way, whether or not its signal holds, and its `max`
  counts those starts.
- **Outcomes.** `on:` a signal the engine observes. An outcome fires **once**
  and **ends the run**: after its chain, if it has one, the way its `then`
  says — `done` (the default) or `abort`; an outcome with nothing to do is
  its `then` alone. It takes a `start` policy (`confirm` asks "…and end the
  run?"), no `max`. A signal that waits for every merge request —
  `mr_merged`, `mr_approved`, `mr_mergeable` — holds for an outcome once each
  merge request of the run has it (a merged one counts as approved and as
  mergeable), with no subject; any other holds as soon as one merge request
  has it, that one being the subject (`mr_closed`: someone closed it
  unmerged). An open merge request keeps an outcome armed — which is what
  keeps the run waiting in review.
- **`sequence: X`** on a watcher or an outcome stands for a chain of one
  stage that runs the sequence `X` (*Sequences*, below), named after its
  watcher or outcome: `{ id: again, on: mr_head_moved, sequence: review }`
  runs `again.read`, `again.post`, …. The main chain is a sequence too: a
  watcher with `sequence: main` runs it again.

`on:` is a signal name, or a mapping:

| field | meaning |
|---|---|
| `signal` | what the watcher or outcome waits for: a signal the engine observes (§2 *wait*), or — a watcher only — `manual` |
| `start` | the policy when the signal holds: `auto` (default) runs the chain; `confirm` raises an inbox decision (*Run it* / *Not now*) first, and nothing is spent until yes; `manual` never starts it by itself — the run page shows the hold, with a Run button. A `$param` whose enum sits within the three. Not with `manual`: the event *is* a person's act |
| `quiet_for` | for a signal that fires on activity (`mr_review_settled`, `mr_head_moved`): only once nothing new happened for this long (≥ 1m). Comments arriving mean someone is reviewing; the watcher fixes the whole batch when they stop |
| `max` | an optional cap on a watcher's runs per subject (a merge request; the subject-less bucket otherwise), starts by hand included. Unbounded by default, whatever the policy: an occurrence never fires twice (below), and the run's budget bounds what the rounds may cost. `-1` says unbounded explicitly. Not on an outcome: it fires once |

**No re-fire on the same fact.** Each occurrence of an observed signal has a
key — for `mr_conflicts` the (target tip, head) pair, for `mr_review_settled`
the newest pending comment, for `ci_failed` the head and its failed checks,
for `mr_approved`, `mr_mergeable` and `mr_head_moved` the head, for
`mr_merged` and `mr_closed` the merge request itself. A watcher's run, or a
*Not now*, for a key is never repeated: a declined conflict is offered again
only when the target (or the branch) moves, answered comments start a new
round only when a reviewer writes again, an approval runs its watcher again
only once someone pushes and it is approved again. Under `start: manual` the
hold is shown until the fact changes.

**One live run per chain, one chain at a time.** A chain has at most one run
alive — active, or *parked* at a wait point inside it (§2 `wait`,
`human_gate`). While it is parked the run is idle: watchers start on their
signals or by hand, and the parked one resumes where it stopped once its
event arrives. A watcher whose run is parked cannot be started again until
that run finishes. The scheduler opens or resumes one wait point per tick, in
document order, and the chosen chain holds the workspace until its next wait
point or `done` — so two chains never touch the branch at once.

**On a subject** — a chain run on one merge request: a watcher on a
merge-request signal or started by hand on one, the main chain of a run
started on one, an outcome holding for one — everything is scoped to the
subject: command stages run in its checkout (artifacts stay in the primary's
`artifacts/`, so address them as `$HOME/repo/artifacts/…`), `$MR_URL` is its
merge request, `$DIFF_TARGET` is its branch against its target and
`$DIFF_PHASE` what *this chain run* committed, and agents get a `# SUBJECT`
prompt section saying which checkout to edit (the others are read-only
context). `each_repo` does not apply there (the parser warns). A *subject
action* — `sync_target`, `load_review_comments`, `reply_review_comments`,
`mr_merge`, `post_review` — in a chain run that has no subject fails
routably with `input_missing`: guard it with `when: subject` (§2) or route
`on_fail`; the parser warns where that can happen. Each chain run's edges
start with fresh counters (`max` is per run of the chain); the budget is the
whole life's. A dead end in a watcher — a failure with nowhere to go, a
person giving up at the debug leaf — ends that round and the run goes back
to waiting; `abort` and `done` still end the run itself.

The run page starts a watcher by hand with
`POST /api/runs/{id}/phases/{phase}/start`, naming in its body the checkout
whose merge request is the subject — else the single open one, else none —
while the run is idle and the watcher has no live run. The main chain is
never started by hand.

### How a run ends

Three terminals end things, and they are not the same thing (§3):

- **`end`** finishes a **chain**. It is over and the run goes back to
  waiting: a parked chain resumes when its event comes, a watcher starts on
  its signal. A chain's last stage ends this way unless it says otherwise, and
  a watcher, which may run any number of times, always does.
- **`done`** finishes the **run**: it completes now, whatever else is parked or
  armed, and their open questions are withdrawn. It is said where the work is
  truly finished — and a flow must say it somewhere: a stage's success or
  edge, or an outcome's `then: done`. A flow with no route to `done` is
  refused, because it would say nothing about when the work is finished.
- **`abort`** abandons the **run**, deliberately: an outcome's `then: abort`,
  `on_success: abort`, or any edge.

The run's end is normally an **outcome**: a chain that fires once, on what
became of the merge request, and ends the run. The `builder` template has
two. Its main chain ends with `end` once the merge request is open — the run
is in review — and from there the watchers keep the merge request healthy, as
many times as they need, while the outcomes wait for its fate:

```yaml
outcomes:
  - id: merged
    on: mr_merged                      # every merge request merged: the ticket is done, and so is the run
    stages:
      - { id: ticket_done, type: action, action: ticket_transition, input: { to: done }, on_fail: done }
  - id: closed
    on: mr_closed                      # closed unmerged: the ticket goes back, the run is abandoned
    then: abort
    stages:
      - { id: ticket_back, type: action, action: ticket_transition, input: { to: todo }, on_fail: abort }
```

A team whose work is finished when a reviewer approves — the merge is
someone else's job — says so with a third outcome; the first one to hold ends
the run:

```yaml
  - { id: approved, on: mr_approved, then: done }   # every merge request approved
```

Inside an outcome nothing may route to `end`: there is nothing to go back to.
A dead end there — the tracker refusing the move with no `on_fail` — abandons
the run, as it would in the main chain: the run's end itself failed. The
other way to say "once the merge request merged" is a `wait` on the same
signal (§2) parked mid-chain, for a main chain that must do something after
the merge in the same chain.

Without an outcome or a `done`, a run **completes** when nothing is left to do
and nothing is waiting: no chain run active, none parked, and nothing
*armed* — an outcome that has not fired while a merge request is open; a
watcher with runs left whose signal is live for an open merge request under
`auto` or `confirm`; a confirmation still open in the inbox. There is no
`end:` key and no hidden rule about merge requests. The parser warns about the
two slips this invites: a watcher that ends the whole run (`done` or `abort`
in a watcher opened by a signal), and a main chain that opens a merge request
and says `done` without waiting for it while watchers or outcomes watch it —
the run would complete before they could. The budget abandons the run; so
does a dead end in the main chain or an outcome (§3).

### Sequences: chains written once

Some chains recur: commit and test after every change an agent makes; plan,
build, review, publish in both a feature and a bugfix. A **sequence** is such
a chain, written once under `sequences:` — like a watcher with no `on:`:
nothing opens it; a **sequence stage**, one that names it with `sequence:`,
runs it in its place.

```yaml
sequences:
  - id: ship
    description: "commit what the agent left and run the tests"
    stages:
      - { id: commit, type: command, each_repo: true, run: "git add -A && git commit -F artifacts/commit.md",
          on_fail: debug }
      - { id: test,   type: command, run_ref: $verify,
          on_fail: fail,                                # the sequence stage's on_fail
          on_timeout: { to: test, max: 1, then: fail } }
  - id: make
    stages:
      - { id: build,  type: agent, agent: builder, input: { failure: $FAILURE },
          on_fail: { to: build, max: 2, then: debug } }
      - { id: ship,   sequence: ship, on_fail: { to: build, max: 5, then: debug } }
      - { id: review, type: agent, agent: reviewer, on_reject: { to: build, max: 3, then: debug } }

main:
  - { id: plan, type: agent, agent: planner }
  - { id: make, sequence: make }                        # runs make.build, make.ship.commit, …
watchers:
  - id: review_fixes
    on: mr_review_settled
    stages:
      - { id: address,  type: agent, agent: builder }
      - { id: fix_ship, sequence: ship, on_fail: { to: address, max: 3, then: debug } }
```

A watcher or an outcome that does nothing but run a sequence says so with
`sequence:` itself — `{ id: again, on: mr_head_moved, sequence: review }` is a
chain of one sequence stage, `{ id: again, sequence: review }`. The main chain
is a sequence named `main` other chains can run: `sequence: main` runs the
whole of it again.

A sequence stage **expands in place** when the document is parsed; the
engine, the events and the run page only ever see ordinary stages:

- **Ids.** A sequence's stages take the sequence stage's id and a dot in
  front: `make.build`, `make.ship.test`, `fix_ship.commit`. A hand-written id
  cannot contain a dot, so an expansion never collides with a stage, and the
  same sequence runs under different ids wherever a stage runs it.
- **Routes inside a sequence** name the sequence's own stages, and stay
  inside it. A sequence is left two ways: through its **last stage**, whose
  success (written or implicit) is the sequence stage's `on_success` — the
  next stage, by default — or through **`fail`**, a reserved edge target that
  means *the sequence stage's `on_fail`*. That chain is spliced in where
  `fail` is said, so each stage saying it counts its own `max`; a sequence
  stage with no `on_fail` makes `fail` a dead end (§3). `fail` takes no `max`
  or `then`, and it exists only inside a sequence.
- **Routes outside** name a sequence stage by its id, which is a route
  target: its first stage. Nothing routes into the middle of a sequence.
- **A sequence stage** takes `id`, `sequence`, `description`, `on_success`
  and `on_fail` — nothing else: it has no type of its own, and it fails as a
  whole. Its `on_fail` may itself say `fail` when the sequence stage sits in
  a sequence: it is then the enclosing sequence stage's.
- **Nesting.** Sequences may run sequences, at most **3** levels deep; a
  sequence that runs itself, directly or not, is refused. A sequence nothing
  runs is never checked — the parser warns.

Everything else applies to the expanded stages as if they had been written
out: one live run per chain, `create_mr` once per chain, the reachability and
contract checks, the stage limit (counted after expansion).

### Run context

A run supplies these without any declaration; they are the ambient facts every
flow is written against, and no binding names them:

| context | what it is |
|---|---|
| the ticket | title + goal — always the prompt's `# TASK` section |
| the run goal | extra direction for *this* attempt, its own prompt section |
| the repository | the project's repo, cloned into the workspace — plus, for a multi-repo project, its *additional* repositories beside it (`~/repos/<name>`), each on the same branch name |
| the branch | the ticket's base branch (`apothecary/<slug>` unless the ticket names one) with the run's id tail appended, forked from the run's source branch — or, for a run created on a merge request someone opened, that merge request's own branch |
| the workspace | one checkout shared by every agent and command stage |
| the parameters | the values the run was created with (§1 *Parameters*) |
| run identity | run id and its bearer token, for the ask_human channel |

A flow declares none of it because none of it varies per stage. `$DIFF_TARGET`,
`$WORKSPACE` and `$MR_URL` (§4) are how a stage reaches into this context
explicitly, and those *are* declared.

---

## §2 Stages

```yaml
- id: build                # required, slug, unique; `end`/`done`/`abort`/`debug`/`start`/`fail` reserved
  type: agent              # agent | command | human_gate | action | wait
  description: "..."       # optional note; documentation only, reaches no prompt
  when: $plan_gate         # optional condition (below); false ⇒ the stage completes as skipped
```

| type | what it does | key fields |
|---|---|---|
| `agent` | runs an agent this flow declares (§1) | `agent`, `input`, `output`, `model`, `harness`, `max_turns`, `timeout` |
| `command` | runs a shell script in the workspace | `run` **or** `run_ref`, `input`, `output`, `timeout`, `each_repo` |
| `human_gate` | parks the phase on a human decision | `question`, `input`, `auto_approve`, `auto_reject` |
| `action` | invokes a built-in apothecary integration | `action`, `input` |
| `wait` | parks the phase until a signal holds | `for`, `timeout` |

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
  model: $review_model                  # optional overrides of the flow's parameters —
  harness: codex                        #   a literal, or a parameter of the right type
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
  run: "git add -A && git commit -F artifacts/commit.md"   # ...or run_ref: verify, or run_ref: $verify
  input: { message: artifacts/commit.md }   # read before the script runs; missing ⇒ input_missing
  output: { report: artifacts/report.txt }  # must exist when the script exits 0
  timeout: 20m                              # optional; absent = parameters.timeout
```

Exactly one of `run` (literal bash) or `run_ref` (a key in the project's
`commands`, often read from a parameter so the project names it) is required.
A command has no contract to check its binding names against, so they are
free labels — and that is the point: they say what each file is *for*, and
they title the file's dot in the graph. Declaring them is what makes
`git commit -F artifacts/commit.md` visible as dataflow rather than an opaque
string.

**`each_repo: true`** runs the script once per checkout of a multi-repo run
(see *Several repositories* below): the primary first, then every additional
repository, each in its own directory. The first non-zero exit fails the stage
with that repository named; a single-repo run is unaffected. Only a command
may say it — an agent sees every checkout at once, and an action knows which
repositories it touches.

### human_gate

```yaml
- id: plan_gate
  type: human_gate
  when: $plan_gate                                # a project may switch the gate off
  question: "Approve this implementation plan?"   # what the human is asked
  input: { plan: artifacts/plan.md }             # what they are shown
  on_reject: { to: plan, max: -1 }
```

A gate is a wait point mid-chain (*The model*) whose first source is a person.
It **parks its chain**: the run is idle while the question is open — nothing
is spent, watchers may run meanwhile (a conflict fixed while the plan awaits
approval), and the chain resumes with the answer. The workspace is not held:
a watcher a person starts by hand may edit it while a gate is open; that
person chose so.

Every gate is the **same machine**: approve follows `on_success`, reject follows
`on_reject` carrying the human's feedback. There is no gate kind — what a
particular gate means ("approve the plan", "merge when satisfied") is said in its
`question`, and what it hands over is its `input` (one binding today). Both are
optional; a gate with no question is labelled by its stage id. The one decision
every flow needs — "the workspace is parked, fix it, then run the stage again" —
is not a gate you write but the `debug` leaf (§3).

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
immediately. Registry (`GET /api/actions`); *requires* names the integration
the project must have — a repository (GitLab, GitHub) or a tracker:

| action | requires | inputs | outputs | notes |
|---|---|---|---|---|
| `create_mr` | repository | `body` (file, required) | `mr_url` (value → `$MR_URL`) | at most one per chain; the body's first `# ` heading becomes the MR title. On a multi-repo run: one merge request per repository the run changed, each onto the branch that checkout forked from. A merge request the branch already has is adopted, never duplicated — so a chain that opens one may safely run again |
| `push_branch` | — | — | — | pushes the run's branch (every checkout's, or the subject's on a subject). A branch it pushed before is force-pushed **leased** against the tip it recorded — on a subject, at worst the head when the chain started — never blind; a first push is plain. Refused = someone else wrote to the branch: `on_fail` |
| `sync_target` | — | `strategy` (`merge` \| `rebase`, default `merge`) | — | a *subject action*. Takes the subject's target branch in, in the engine: a no-op when already contained; a conflict leaves the merge (or rebase) **paused** in the working tree and fails the stage with the files named. Whoever resolves it edits those files — no git — and the retried stage checks no marker is left and continues. `rebase` needs a `push_branch` in the same chain |
| `load_review_comments` | repository | `select` (`human` \| `all`, default `human`) | `review` (file → `artifacts/review.md`) | a subject action. Writes the subject's **pending** threads (unresolved, last word a reviewer's — inline threads, review bodies, conversation comments; bot accounts excluded): file, line, diff hunk, comments, thread id. With `select: human` a person first picks which threads to fix (an inbox checklist; the chain parks); unpicked threads are skipped until someone comments on them again, and picking none ends the round without running anything else. A re-entry rewrites the same set |
| `reply_review_comments` | repository | `replies` (file, required) | — | a subject action. Posts one reply per entry of a `review_replies` file, resolving where it says so; each reply carries an invisible marker, so a crash between two posts is reconciled from the repository and nothing is posted twice |
| `post_review` | repository | `review` (file, required) | — | a subject action. Posts a `review_threads` file as a review: a note on each line it names, and its summary. A note on a line outside the change joins the summary, quoted with its place, rather than failing the review. Never approves nor requests changes — that stays a person's decision. Every part carries a marker, so a retry posts only what is missing, and a review's notes never count as a reviewer's comments (`mr_review_settled`). An empty review posts nothing. GitHub gets one review; GitLab a discussion per note and a note for the summary |
| `mr_merge` | repository | `method` (`merge` \| `squash` \| `rebase`, default `merge`), `branch` (`keep` \| `delete`, default `keep`) | — | a subject action, needing no workspace. Merges the subject merge request at the newest head the chain knows: the tip it pushed, else the head it began on — a head someone else pushed is not merged. The repository's rules apply: a merge it declines (a draft, checks or approvals missing, the head moved) fails the stage with its reason, for `on_fail`. `rebase` is GitHub's; GitLab merges by the project's merge method (fast-forward for a linear history), so it refuses it. The merge is what the `merged` outcome then sees |
| `ticket_transition` | `tracker` | `to` (enum: `todo` \| `in_progress` \| `in_review` \| `done` \| `blocked`) **or** `status` (text), exactly one | — | moves the ticket's tracker issue. `to` is a **canonical** status the ticket's tracker translates into its own vocabulary — Jira through the integration's status map (the stock words by default; `done=Ready for prod` for a team whose Done is a deploy away), GitHub Issues to open/closed — and a status the tracker has no word for **skips** the stage with a note. `status` is the tracker's own status or transition name, verbatim (case-insensitive). Already there: nothing happens. Not reachable from the current status: the stage fails with `input_invalid` naming the statuses that are |
| `ticket_comment` | `tracker` | `body` (file, required → `artifacts/ticket_comment.md`) | — | posts the markdown artifact as a comment, ending with a short trailer naming the run, the chain run and the stage — also how a crash between posting and recording is reconciled, so nothing is posted twice |
| `ticket_set_field` | `tracker` | `field` (text, required); `value` (file) **or** `text` (text), exactly one; `mode` (`replace` \| `append`, default `replace`) | — | writes one field by its display name (`"Feature flags"`, `labels`). `append` merges comma-separated items into the current value (a union: re-applying changes nothing). A field the tracker has no name for, or cannot write from text, fails the stage with `input_invalid` |
| `ticket_link` | `tracker` | — | — | attaches every merge request the run opened (the subject's on a subject) to the issue — a remote link on Jira, a comment on GitHub — titled with the branch; an upsert, so a re-entry attaches nothing twice. Needs a merge request: `input_missing` without one, and the validator warns when no `create_mr` precedes it |

An action's enum input may read an enum parameter whose values all sit within
the input's (`to: $final_status`); a text input may read a string parameter.

**Tracker actions** (`ticket_*`) are the one family whose prerequisite is per
*ticket* rather than per project: they write through the `ticketsource`
integration the ticket was imported from, and a ticket written by hand has
none. Such a stage then completes as **skipped** — `stage_completed{skipped,
note}` on the timeline, `on_success` followed — so one flow serves every
ticket; so does a canonical status this tracker has no word for. Run creation
refuses a flow with tracker actions only when the ticket *is* linked and its
integration cannot write (a read-only adapter, a token without write scopes),
naming the stage. A tracker that errors at stage time — unreachable, refused —
is an outcome failure like any other action's (`on_fail`, with the reason as
`$FAILURE`), never terminal: route it to the next stage for "best effort".
Every tracker action is *remote*: it needs no workspace unless one of its file
inputs is bound, so a flow may open with `ticket_transition` and have the
issue moved seconds in, before anything is provisioned — and the `merged`
outcome's move never re-provisions a workspace the reaper took.

```yaml
- { id: kick,   type: action, action: ticket_transition, input: { to: in_progress },
    on_success: plan, on_fail: { to: plan, max: 1 } }   # best effort: a tracker hiccup never blocks the work
…
- { id: mr,     type: action, action: create_mr, input: { body: artifacts/mr.md }, on_success: link }
- { id: link,   type: action, action: ticket_link, on_success: flags }
- { id: flags,  type: action, action: ticket_set_field,
    input: { field: "Feature flags", value: artifacts/feature_flags.txt, mode: append },
    on_success: in_review }
- { id: in_review,   type: action, action: ticket_transition, input: { to: in_review }, on_fail: end }
…
# the outcome: once every merge request merged, the ticket moves and the run is done
- id: merged
  on: mr_merged
  stages:
    - { id: ticket_done, type: action, action: ticket_transition, input: { status: "Ready for prod" }, on_fail: done }
```

`artifacts/feature_flags.txt` is an agent output like any other — a small
agent that reads the diff for the flags it introduced and writes them,
comma-separated — and the field name is the author's, written in the flow
where the graph shows it. "Done once the merge request is accepted" is not a
hook but an outcome (§1): it fires once the merge request merges, and the move
is its stage. `status: "Ready for prod"` is the one Jira
spelling written into this flow; `to: done` with the project's status map
bound to the same word would keep the flow portable.

**Edges on actions.** `on_success` routes normally. An action may carry
`on_fail`, taken when its *outcome* is wrong in a way an agent, a retry or a
human can fix — a merge that conflicts, a push the remote refused, a subject
action with nothing to act on — with the reason as `$FAILURE`. Its
infrastructure errors (the repository unreachable, the workspace gone) stay
terminal, and an action without `on_fail` fails the run on any error. Adding
an action is one registry entry, one executor, and (if it needs an
integration) a `requires` string that run creation asserts.

### Several repositories

A project may list **additional repositories** beside its primary (name, URL,
default branch, an optional locator — its id on GitLab or GitHub). A run clones each at
`~/repos/<name>` on the same branch name, forked from its default branch; the
primary stays `~/repo` and still supplies the devcontainer. What follows from
one more checkout, without any flow change:

- agents see a `# REPOSITORIES` prompt section naming each checkout and its
  path; `$DIFF_TARGET` and `$STATUS` are grouped per repository;
- a command stage marked `each_repo: true` runs in each checkout in turn, so
  the default template's `commit` and `push` stages commit and push every
  repository the run touched;
- `create_mr` opens one draft merge request **per repository the run changed**
  (an untouched extra repository gets none), each onto the branch that
  checkout forked from; a gate on `$MR_URL` lists them all, and `mr_merged` /
  `mr_closed` wait for all / fire on any;
- each merge request is its own subject: a conflict on one repository's merge
  request is fixed in that checkout only;
- a repository listed **without a locator** is context-only: cloned for the
  agent to read, never published.

The run page's *Changes*, *Files* and *Terminal* tabs take a repository
selector when a run has more than one checkout.

**Side effects and interruption.** An action's effect lands outside the run, so
the engine records an `action_started` intent event *before* the call. If the
process dies between the effect landing and its completion event, recovery sees
that intent alone and **reconciles** rather than re-running: `create_mr` asks
the repository what the run's branch already has (the branch is unique per run, so
it is the idempotency key) and adopts an existing merge request instead of
opening a second one. A reconciliation that cannot reach the repository fails the
stage rather than guessing — a duplicate MR is worse than a retry.

### wait

```yaml
- id: wait_merge
  type: wait
  for: mr_merged        # a waitable signal from the registry
  timeout: 24h          # optional; absent stays parked indefinitely
  on_fail: abort        # the signal can no longer hold: every merge request closed unmerged
  on_timeout: { to: nudge, max: 1 }
```

A signal is something that happens *to* the run from outside it. A `wait` is a
wait point mid-chain (*The model*): it **parks its chain** — the run is idle,
nothing is spent, watchers run meanwhile — and resumes it when the signal
holds (`on_success`), when it can no longer hold (`on_fail`: waiting for a
merge once a merge request is closed, or with no merge request at all), or
when `timeout` passes (`on_timeout`, else `on_fail`). A parked wait survives
a restart: the engine replays to the park and resumes polling, raising
nothing twice.

Registry (`GET /api/signals`). A signal says what it **is**, never what it
does: the section that names it decides that (§1). Two are not observed:

| signal | holds when |
|---|---|
| `run_start` | a person or an automation starts the run: how the main chain opens. Nothing the flow listens for starts a run |
| `manual` | never on its own: a person starts the watcher from the run page |

Every other signal is **observed** on the run's merge requests — it requires a
repository integration and needs a merge request — and any of them may run a
watcher, fire an outcome, be waited on, or answer a gate:

| signal | holds on a merge request when | for the whole run | occurrence |
|---|---|---|---|
| `mr_merged` | it merged | every merge request merged | the merge request |
| `mr_closed` | it was closed *without* being merged — someone decided against it | as soon as one is | the merge request |
| `mr_approved` | it is approved: on GitHub, someone's latest review approves and nobody's latest requests changes; on GitLab, the approval rules are met by at least one approval (a project without rules reports "approved" with nobody's — that is not an approval) | every one approved or merged | the head it was approved at: a push after the approval, approved again, is a new one |
| `mr_conflicts` | the repository reports it cannot merge cleanly into its target (one still computing is *not* a conflict) | as soon as one does | (target tip, head) |
| `mr_review_settled` | it has pending review threads — unresolved, last word a reviewer's — and the newest of those comments is older than `quiet_for` | as soon as one does | the newest pending comment |
| `ci_failed` | a CI check on its head commit failed — GitHub check runs and commit statuses, GitLab's head pipeline (its failed jobs named; one allowed to fail is not a failure). Cancelled and skipped checks are neutral | as soon as one does | the head and its failed checks: a push, or another check failing, is a new one |
| `mr_mergeable` | the repository would merge it now: no conflict, its required approvals and checks in, not a draft — GitHub's `mergeable_state` clean, GitLab's merge status `mergeable`. The project's own rules decide; the run's merge requests are opened as drafts, so this waits for a person to mark one ready | every one mergeable or merged | the head |
| `mr_head_moved` | its head is a commit the run does not know — not one it pushed (`push_branch`), opened or adopted the merge request at, or already began a chain on: someone else pushed. With `quiet_for`, once the head has stood still that long | as soon as one does | the head |

A watcher runs on one merge request, the one its occurrence is on. A wait or
a gate in a chain that has a subject watches that merge request; one with
none (the main chain of a run started on a ticket) watches the run's — *for
the whole run* above, which is also how an outcome holds. A merged or closed
merge request has nothing left but its fate: a wait on any other signal then
fails (`on_fail`). `needs` is a prerequisite *within the run*: waiting for
`mr_merged` in a flow that never opens a merge request fails with
`input_missing` and says so, rather than parking forever on something that
cannot happen.

Four related concepts, kept distinct: an **action** does something now, a
**wait** parks until something happens, a **watcher** runs a chain each time
something new happens to a merge request — or a person says so — and an
**outcome** fires once and ends the run, when the merge request's fate is
known.

### when

```yaml
- { id: plan_gate,   type: human_gate, when: $plan_gate, … }       # a bool parameter
- { id: in_progress, type: action, action: ticket_transition, when: tracker, … }
- { id: sync,        type: action, action: sync_target, when: subject, … }
- { id: note,        type: agent, agent: writer, when: not tracker, … }
```

`when:` names one condition: `tracker` (the ticket has a tracker issue),
`subject` (this chain run acts on a merge request), or a bool parameter — each
negatable as `not x`. When it does not hold, the stage completes as
**skipped** (`stage_completed{skipped, note}` on the timeline) and `on_success`
follows, so the chain reads the same whether or not the project has a tracker
or the knob is on. The validator counts what a conditional stage produces as
produced only on some routes, and a skipped agent stage never silences the
model check a later one needs. Tracker actions skip without a tracker on
their own; `when: tracker` says it in the graph.

### Timeouts

One field, `timeout`, whose meaning follows the stage type:

| type | what `timeout` bounds | absent means |
|---|---|---|
| `agent` | execution wall clock, suspended while an ask_human ping is open (§9) | `parameters.timeout` (30m) |
| `command` | execution wall clock | `parameters.timeout` |
| `wait` | how long the chain stays parked on the signal | parked indefinitely |
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
on_success: build            # a stage id, `end`, `done` or `abort`
on_success: done             # the run is done
on_fail:    { to: build, max: 5, then: debug }
on_reject:  { to: build, max: 3 }
on_timeout: { to: test,  max: 1 }
on_reject:  abort            # scalar sugar for the deliberate stop
on_fail:    debug            # scalar sugar for the debug leaf
on_fail:    end              # best effort: the chain ends either way
```

**`on_success`** is a plain target with no counter. Absent, it means "the next
stage in document order"; on a chain's last stage, `end` — the run goes back
to waiting — except where nothing could follow: a main chain with no watcher
and no outcome is `done`, and an outcome ends the run the way its `then`
says. `on_success: abort` is the flow abandoning
the run from a success — an outcome deciding against the ticket. Every
stage's success chain must reach `end`, `done` or `abort` — a success-only
cycle is a parse error, because nothing would bound it (a failing command
costs no turns and no dollars).

**Edges** are the recoverable redirects. Each carries its own `max`, and when a
link is exhausted the *next* failure falls through to its `then` — retry, then
escalate. An exhausted chain with no `then` is a dead end (below).

**What `max` counts.** A counter belongs to one *link* — the triple
(source stage, edge kind, position in the `then` chain) — and counts how many
times that link has been taken **over one chain run**. It does not reset when
the source stage later succeeds: `max` bounds total spend on a loop, which is
what makes it a budget rather than a per-visit allowance. Counters are
persisted in the `edge_taken` events, so they survive a restart exactly, and
pause/play leaves them untouched. **"Try again"** (`run_retried`) is the only
thing that clears them: a human deciding to retry a dead run restores every
stage's budget from the resume point.

There is deliberately no backoff: every agent/command loop must carry a finite
`max` or a human gate on the link, so no edge can spin unbounded.

| kind | raised when | allowed on | carries |
|---|---|---|---|
| `on_fail` | the stage failed | agent, command, action, wait | `$FAILURE` |
| `on_reject` | a verdict said REJECT, or a human rejected a gate | agent, human_gate | `$DECISION` |
| `on_timeout` | the stage exceeded its timeout | agent, command, wait | `$FAILURE` |

A timeout uses `on_timeout` when the stage declares one, else `on_fail`.

**An edge does not declare what it carries.** Its kind already decides that, so
saying it again could only disagree. What it carries is available to its target
as a sentinel, and the target binds it like any other input (§4) — or ignores
it. That is the single declaration: *where it goes* lives with the stage that
reads it, next to everything else that stage reads.

**Terminals.** `end` finishes the **chain**: it is over and the run goes
back to waiting (§1 *How a run ends*). `done` finishes the **run**: it
completes now, whatever else is parked or armed. `abort` deliberately abandons
the run. Any edge may target any of them — a failure edge to `end` is "best
effort", the chain ends either way — and such a link takes no `max`, `feed` or
`then`. Two rules follow from what they mean: an outcome may not route to
`end` (there is nothing to go back to), and a flow must have some route to
`done` — a stage's success or edge, or an outcome's `then: done`.

**Dead ends.** A failure with nowhere to go — no edge for it, or its edge
chain exhausted — or a person giving up at the debug leaf is a dead end. In
the main chain or an outcome it abandons the run: the work failed, or its end
did. In a watcher — opened by its signal or by hand — it ends that round
only: its `phase_completed` records why, and the run goes back to waiting,
because the watcher may run again and the merge request it watches is still
waiting.

**The `debug` leaf** is the third reserved edge target, and not a terminal: the
run comes back from it. Taking a `debug` link parks the chain for a human who
has the workspace — the decision is attributed to the stage that took the link,
with what it printed and the workspace link — and nothing is spent while they
look. Unlike a gate it **holds the workspace**: no other chain runs while a
person has the checkout. *Continue* re-enters that same stage as a fresh
attempt (the success chain replays from there, so no stage is skipped);
*giving up* is a dead end — the run is abandoned in the main chain or an
outcome, a watcher's round ends, and the inbox card says which before you
click. Like
`abort` it takes no `max` or `then`: it is unlimited by nature, bounded by
human attention, and nothing can follow it but the stage itself. It is
written `then: debug` at the end of a retry chain, or `on_fail: debug` on its
own. Stage ids may not shadow `end`, `done`, `abort`, `debug`, `start` or `fail`. Because the leaf is that stage's pause rather than a place runs go
to, the editor draws one per stage that escalates to it, beside the stage,
with the return edge — never a shared sink.

A run ends in one of three states, and the distinction is reporting, not
capability (`resume` accepts the last two):

| state | reached by |
|---|---|
| `completed` | the flow said `done` — an outcome, usually — or every chain run finished with nothing parked and nothing armed (§1 *How a run ends*) |
| `abandoned` | a deliberate stop — the flow's `abort` terminal (an outcome's `then: abort`, `on_success: abort`, an edge), a person pressing abort — or a dead end in the main chain or an outcome, or a spent budget |
| `failed` | the engine's own fault: infrastructure, an unresolvable project, a flow that no longer parses or no longer resolves with the run's parameters |

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
  changes: $DIFF_TARGET
```

What differs is only how the **name** is checked, which follows from the type:

| type | `input` names | `output` names |
|---|---|---|
| `agent` | must be a declared input | must be a declared output (rebinds its path) |
| `action` | must be in the action's signature; an `enum` or `text` input takes a literal (or a parameter), not a path | — |
| `command` | free labels | free labels |
| `human_gate` | free label, exactly one binding | — |

### Sentinels

A `$NAME` value is provided by the engine; a `$name` value is a parameter (§1);
anything else is a repo-relative artifact path (no leading `/`, no `.` or `..`
segments).

| sentinel | is | bindable on |
|---|---|---|
| `$DIFF_TARGET` | `git diff --name-status <target>...HEAD` — what the branch changes against **its target**, the branch it forked from and its merge request merges into: the changed-file list, never the full diff. The same range in every chain; on a multi-repo run one section per checkout, on a subject the subject's only | agent input |
| `$DIFF_PHASE` | `git diff --name-status <chain start>...HEAD` — what **this chain run** committed since the head it began on: a review round's fixes, not the whole branch. A chain run that began on no recorded head (the main chain of a run started on a ticket, which forked the branch) counts from the target | agent input |
| `$STATUS` | `git status --short` of the working tree — staged, unstaged and untracked files, i.e. what a commit stage would record right now (the diffs compare commits, so they cannot see this); scoped per checkout like `$DIFF_TARGET` | agent input |
| `$DECISION` | why the last attempt was turned down: a verdict's comments, or a human's gate feedback | agent input |
| `$FAILURE` | what the failing stage printed: the last 8 KB, with every secret the engine injected replaced by `[redacted:<kind>]` | agent input |
| `$MR_URL` | the run's merge request link, produced by `create_mr`; the first when a multi-repo run opened several — a gate then also lists them all in its context; on a subject, the subject's | gate input |
| `$WORKSPACE` | the workspace's access link (terminal, else IDE); the `debug` leaf hands it out by itself | gate input |

The scoping is enforced: an agent cannot bind `$MR_URL` (a link is for a human
to click, not for a prompt to read), and a gate cannot bind a git sentinel
(prompt content is not something to show a human). Each git sentinel is named
after the git it runs and means that one range wherever it is bound.

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
   different kinds of edge — in the built-in `builder` flow `build` is the target of both
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

**A `review_replies` output** is a review round's answers: a YAML list of
`{thread, reply, resolve}`, one entry per thread the round loaded. It is
checked when the stage ends — every `thread` one of the round's, none twice,
every `reply` non-empty (an empty list is valid) — so a reply to an unknown
thread fails *that* stage with `output_invalid`, its reason as `$FAILURE`,
before anything reaches the repository. `reply_review_comments` posts it.

**A `review_threads` output** is a review of the merge request, for
`post_review`:

```yaml
summary: "Reads well; two things before it merges."   # optional, markdown
threads:                                              # optional, at most 50
  - { path: internal/flow/parse.go, line: 412, body: "This error loses the line number." }
```

Checked when the stage ends — every thread on a repository-relative path, a
line from 1 (in the changed file), a non-empty body — the same way: a bad one
fails the stage, its reason as `$FAILURE`. Whether a line is in the change is
the repository's business at posting time, not a failure (below).

### What the editor draws

Because consumption and production are declared, the graph editor derives the
dataflow rather than storing it: each stage shows an input dot per thing it
reads and an output dot per thing it writes, and a dashed line connects a
producer's dot to a consumer's dot whenever their paths match. Those lines are
read-only by construction — you rewire them by editing a path, not by dragging.
Control-flow edges (the colored ones) are the draggable layer, and one of them
lands directly on the `$DECISION` or `$FAILURE` dot that receives its content —
so the retry path is visible too. Every chain is drawn the same way, one row
each with its opening — the main chain's start, a watcher's or an outcome's
wait point — as a marker on its first stage, under its section's label (main,
watchers, outcomes); the stages that park are marked too.

---

## §5 Failure

A stage failure records a reason, then follows an edge:

| reason | meaning |
|---|---|
| `exit_nonzero` | a command exited non-zero |
| `timeout` | the stage exceeded its timeout |
| `harness_error` | the harness itself failed |
| `input_missing` | a path the stage declared it consumes was not there, or a subject action ran with no merge request to act on — upstream wiring, not this stage's work |
| `input_invalid` | a value the flow wrote means nothing where it lands — a status the tracker cannot reach, a field it has no name for or cannot write from text; or a parked wait's signal can no longer hold. Wiring again, but the input is there |
| `output_contract` | the stage finished without writing what it declared (its outputs are cleared before each fresh attempt, so a stale file cannot satisfy it) |
| `output_invalid` | a declared output exists but does not hold what its type promises — a malformed verdict |
| `infra` | provisioning, repository or other infrastructure failure |

A stage that **skips** — its `when` does not hold, a tracker action with no
tracker or no word for the status — is not a failure: it completes with
`skipped` and a note, and `on_success` follows.

The recommended shape, and the one the built-in `builder` flow uses: every agent
and command stage first retries itself with its error fed back, then escalates
to the `debug` leaf (`then: debug`). A human opens the workspace, fixes what the
agents could not, and continuing re-runs exactly the stage that escalated — the
success chain replays from there, so no stage is skipped. The leaf is unlimited:
the human decides when to stop, and giving up there is a dead end (§3) — the
run is abandoned in the main chain or an outcome, a watcher's round ends.

Budget exhaustion and cancellation are **run-level** outcomes, not stage
failures: exceeding `max_cost_usd` or `max_total_turns` abandons the run, and
abort/pause act on the run as a whole (§3).

**Every attempt leaves a record.** As each attempt ends — succeeded or failed —
the engine captures its declared outputs and its full log as **snapshots** and
links them to the run's timeline with `snapshot_captured` events. The events
carry references, never bytes, so they stay replay-cheap while the content can
be any size. Two consequences worth relying on: attempt 1's plan stays
readable after attempt 2 overwrites the file, and the 8 KB tails in
`stage_failed` / `$FAILURE` are a *preview* — the whole log is one link away.
Snapshots outlive the run they describe (archiving keeps them) and are
scrubbed of engine-injected secrets on the way in.

> **Terminology** (DESIGN-FILES): an **artifact** is this document's word — a
> declared file interface at a repo-relative path, living in the workspace. An
> **attachment** is a file a human gave the factory (delivered under
> `.apothecary/attachments/`). A **snapshot** is the engine-captured, immutable
> copy of workspace content in the control plane's record. The three never
> share storage: artifacts die with the workspace, attachments and snapshots
> outlive it.

---

## §6 Validation

Three levels, separated by what each one can know.

**Parse errors** (the document) — refuse it. Unknown fields and values
(`defaults:`, `budget:`, `confirm:`, `end:` are not grammar), fields that do
not apply to a stage's type, unknown action slugs, unbound required action
inputs, an action's "exactly one" group bound twice or not at all, malformed
edges, `each_repo` outside a command, success cycles, unreachable stages, and
for the chains and wait points: v4's `phases:` or top-level `stages:` (each
named with its v5 spelling); a `run_start` other than `ticket` or
`merge_request`; no `main` in a flow whose runs are started on a ticket; a
flow with neither `main` nor a watcher; a watcher or an outcome whose `id` is
missing, not a slug, `main`, or another's (watchers and outcomes are one
namespace); a watcher with neither `run` nor `stages`, or both; `then` on a
watcher; an outcome with neither a chain nor `then`, or a `then` other than
`done` / `abort`; `on:` `run_start` anywhere, `manual` on an outcome, or a
signal the registry does not know; a `wait` on, or a gate auto-answered by, a
signal the engine does not observe (`run_start`, `manual`); `start` on
`manual`; `quiet_for` on a signal that does not fire on activity, or under 1m;
a watcher `max` of 0; `max` on an outcome; a route to `end` inside an outcome;
no route to `done` anywhere; a route leaving its chain; a success chain that
reaches none of `end`, `done`, `abort`; `create_mr` twice in one chain; a
`rebase` without a `push_branch` in its chain; `timeout` on a gate or an action; `max`
or `then` on a link to `end`, `done`, `abort` or `debug`; `max: -1` without a
gate on the link. For sequences: a sequence stage naming no sequence, carrying
anything but `id`/`description`/`on_success`/`on_fail`, or reusing an id; a
sequence's route that leaves it other than by its last stage or `fail`;
`fail` outside a sequence, on an `on_success`, or with `max`/`then`; a route
into the middle of a sequence (a dotted target); a sequence that runs itself,
sequences nesting deeper than 3, an empty or duplicate sequence, a sequence
named `main`, a hand-written id with a dot; a watcher or an outcome with both
`sequence` and `stages`; the retired `blocks:`, `include:` and a watcher's
`run:` (each named with its new spelling). For
parameters: an
engine-known name declared with the wrong shape, `harness` or `model` missing,
a default that fails its own validator (a model without a provider, a
duration that does not parse, a ceiling at or below zero), an enum on a
non-string or a default outside it, a reference to an undeclared parameter,
one of the wrong type or at a site that takes none (a prompt, a path, a
`run:` script), an enum parameter wider than its site; `when` naming anything
but `tracker`, `subject` or a bool parameter. And every **contract**
violation: an agent a stage names but the flow does not declare, an input the
agent does not declare, a required input left unbound, a non-optional input
bound to `$DECISION`/`$FAILURE`, an output rebind of something undeclared,
`on_reject` on an agent with no verdict output. A flow cannot be saved with
any of these.

**Resolve-time errors** (the document with values) — the same rules, re-run
over the pinned bytes with a project's binding or a run's overrides laid in:
a `run_ref` bound to something that is no command slug, a model without a
provider, a value outside its enum or of the wrong type, a name
the flow does not declare (dropped with a warning in a project binding;
refused in a run override). Refused where the value is bound — the workshop's
Configure sheet, run creation — with the parameter named.

**Warnings** — advisory, returned alongside a successful parse by
`POST /api/flows/validate` and never blocking anything. These are legal
constructs that look like mistakes: a path no stage produces, a path produced
on only *some* of the routes that reach its consumer (a conditional producer
among them), a feed sentinel no edge can fill, a `wait` on a signal — or a
`ticket_link` — whose prerequisite merge request isn't opened on every route
reaching it, a parameter nothing reads, a subject action in a chain that may
have no subject, `each_repo` on a subject, a watcher or an outcome on a
merge-request signal in a flow that opens no merge request (and is not
started on one), a stage of a watcher opened by a signal that ends the whole
run (`done` or `abort` — almost always a slip for `end`), and a main chain
that opens a merge request and says `done` without waiting for it while
watchers or outcomes watch it; and a sequence nothing runs, whose stages
nothing has checked.

**Preflight errors** (this project, this ticket) — refuse to *create a run*.
Checked with the project's real configuration on the **resolved** flow, so a
bound value is held to the same rules as a literal: `run_ref`s exist in its
commands, the harness — the flow's, the binding's or a stage's — is in the
project's set, every agent stage's model is one its harness lists (when the
list can be fetched), an action's required integration is configured, every
watcher and outcome on an observed signal has whoever raises it — and a flow
started on merge requests has where they are: a repository integration — the repo has a
devcontainer and an `AGENTS.md` — and, for a ticket imported from a tracker,
that its integration can write when the flow has a tracker action (a ticket
written by hand passes; those stages skip). The same checks answer
`POST /api/projects/{id}/preflight` step by step, with a `parameters` step for
the binding.

A flow whose stored text no longer parses, or no longer resolves with a run's
recorded values, fails that run terminally with the error — retrying cannot
fix a grammar problem.

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
  segment, no leading `$` (that space belongs to sentinels and parameters).
  Beyond the lexical rule, the engine resolves symlinks before reading or
  writing an artifact and refuses anything landing outside the checkout — so
  an agent-authored `artifacts/plan.md -> /apothecary/run.env` cannot smuggle
  the run token into a prompt.
- **Parameter names** are `[a-z][a-z0-9_]*`; a reference is `$` + name. The
  engine-known six are reserved.

---

## §7 Retired spellings

A version 4 (or older) document is refused whole (`this build speaks version
5`, with a word on where v4's phases went); there is no legacy reading.
Converting one by hand:

| old | now (v5) |
|---|---|
| `phases:` (v4) | the sections: `main:` — the chain a run starts with, a list of stages — `watchers:` and `outcomes:` (§1) |
| top-level `stages:` (v4's shorthand, one phase `main`) | `main:` |
| a phase `on: run_start` — an entrypoint, several allowed (v4) | the `main` chain, one per flow: a person or an automation starts the run, and a chain shared with another is a sequence (§1 *Sequences*) |
| a phase `on: mr_opened` (v4) | `run_start: merge_request`, the chain under `main:` |
| a reaction phase, on a firing signal or `manual` (v4) | a watcher, under `watchers:` |
| an outcome phase, on `mr_merged`, `mr_closed` or `mr_approved` (v4) | an outcome, under `outcomes:` — on any observed signal |
| a `phase` on a start-run, rerun or automation request (v4) | nothing: a flow has one main chain, and its `run_start` says what an automation of each kind may start |
| a person beginning the entry phase again from the run page (v4) | the main chain runs once; a rerun is a new run |
| `defaults:` and `budget:` blocks (v3) | one `parameters:` block (§1): the same six names, plus the flow's own knobs |
| the entry phase `on: manual` (or `on: ticket`), the first by position (v3) | the `main` chain |
| `on: { signal: mr_conflicts, confirm: true }` (v3) | `on: { signal: mr_conflicts, start: confirm }` — `auto` / `confirm` / `manual`, or `$param` |
| a reaction `on: { signal: manual }` (v3) | a watcher `on: manual` |
| the run ending by itself from its merge requests (v3) | an **outcome**: `on: mr_merged` ending with `done`, `on: mr_closed` with `then: abort` (§1 *How a run ends*) |
| `$CHANGED` (the branch against its target in an entry phase, *this round's* changes in a subject phase) | `$DIFF_TARGET` or `$DIFF_PHASE` — one range each, wherever bound |
| `$UNCOMMITTED` | `$STATUS` |
| `wait_merge` + `ticket_done` closing the delivery phase (the first v4 cut) | the main chain ends with `end` once the merge request is open; the `merged` outcome moves the ticket and says `done` |
| `on_success: done` closing a reaction (v3: the phase ended) | `on_success: end`, or nothing — `done` ends the run |
| a reaction's `max` (v3: 10 by default) | no default cap: a watcher runs as often as the signal brings something new |
| `ticket_transition` `to: "In Progress"` | `to: in_progress` (canonical, translated by the tracker) — or `status: "In Progress"` for the tracker's own word |
| `POST …/phases/{phase}/start?repo=` | a `subject` in the request body |
| `blocks:` and a stage's `include:` (v4, early v5) | `sequences:` and a stage's `sequence:` — the same chains under one word |
| a watcher's or an outcome's `run: X` (early v5) | `sequence: X` — `run:` is a command's script |

Older spellings the parser still answers by name rather than with a bare
"unknown field":

| old | now |
|---|---|
| `ping: plan_approval\|final_review\|manual_debug` | `question:` free text |
| `payload:` on a gate | `input: {name: …}` |
| `auto_complete:` | `auto_approve:` (or a `wait` stage to just park on it) |
| `auto_answer:` | `auto_approve:` / `auto_reject:` — say which answer it supplies |
| `feed: output` / `feed: decision` on an edge | bind `$FAILURE` / `$DECISION` as an input on the target |
| `mr_url` / `workspace` | `$MR_URL` / `$WORKSPACE` |

---

## §8 Annotated example

The built-in `builder` flow, abridged — plan-gated build with cross-vendor
adversarial review, the debug leaf as every stage's rescue, the ticket's
tracker issue following the work in canonical statuses, and the merge
request's life after it opens. Full text: `internal/builtin/flows/builder.yaml`.

```yaml
name: builder
version: 5
run_start: ticket                  # the default: a run is started from a ticket
# One settings block. A project binds any of these in the workshop without
# touching this document; a run may override.
parameters:
  harness: opencode
  model: opencode-go/qwen3.7-plus
  timeout: 30m
  max_turns: 50
  max_cost_usd: 20.0
  max_total_turns: 600
  verify:          { default: verify, description: "the project command that runs the tests" }
  review_model:    { default: opencode-go/glm-5.2, description: "a second vendor reviews the change" }
  plan_gate:       { default: true, description: "stop for a human to approve the plan before building" }
  conflicts_start: { default: confirm, enum: [auto, confirm, manual] }
  review_start:    { default: auto,    enum: [auto, confirm, manual] }

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

# The main chain: it runs once, when the run is started on the ticket.
main:
  # The tracker issue moves first — before any workspace exists, since a
  # tracker action needs none. A canonical status: Jira says "In Progress",
  # GitHub Issues has no word for it and the stage skips; so does a ticket
  # written by hand. A tracker hiccup routes straight on to the plan.
  - { id: in_progress, type: action, action: ticket_transition, input: { to: in_progress },
      on_success: plan,
      on_fail: { to: plan, max: 1 } }

  # No output binding: the planner's contract says its plan goes to
  # artifacts/plan.md. It binds both retry values, so being sent back — by
  # a human rejecting the plan, or by its own failure — arrives named. Two
  # retries with the error fed back, then the debug leaf.
  - { id: plan, type: agent, agent: planner, max_turns: 25,
      input: { rejection: $DECISION, failure: $FAILURE },
      on_success: plan_gate,
      on_fail: { to: plan, max: 2, then: debug } }

  # A wait point: the chain parks while the human reads; a conflict fix
  # may run meanwhile. A project that trusts its planner binds
  # plan_gate=false and the stage completes as skipped.
  - { id: plan_gate, type: human_gate, when: $plan_gate, input: { plan: artifacts/plan.md },
      question: "Approve this implementation plan?",
      on_success: build,
      on_reject: { to: plan, max: -1 } }

  - { id: build, type: agent, agent: builder, max_turns: 80,
      input: { plan: artifacts/plan.md, rejection: $DECISION, failure: $FAILURE },
      on_success: commit_msg,
      on_fail: { to: build, max: 2, then: debug } }

  # The committer writes artifacts/commit.md (its contract); the commit
  # stage declares it reads that file, so the dataflow is visible instead
  # of buried in the script.
  - { id: commit, type: command, each_repo: true,
      run: "git add -A && if ! git diff --cached --quiet; then git commit -F \"$HOME/repo/artifacts/commit.md\"; fi",
      input: { message: artifacts/commit.md },
      on_success: test, on_fail: debug }

  # The project's test command, by the name the project binds.
  - { id: test, type: command, run_ref: $verify,
      on_success: review,
      on_fail: { to: build, max: 5, then: debug },
      on_timeout: { to: test, max: 1, then: { to: build, max: 2, then: debug } } }

  # A different vendor reviews. The reviewer's verdict output is what makes
  # on_reject reachable; its comments reach the builder's `rejection` input.
  # Three rejected rounds, then a human: no chain ends without a person.
  - { id: review, type: agent, agent: reviewer, model: $review_model,
      input: { plan: artifacts/plan.md, changes: $DIFF_TARGET, failure: $FAILURE }, max_turns: 30,
      on_success: redact,
      on_reject: { to: build, max: 3, then: debug },
      on_fail: { to: review, max: 1, then: debug } }

  - { id: push, type: action, action: push_branch, on_success: mr, on_fail: debug }
  - { id: mr,   type: action, action: create_mr, input: { body: artifacts/mr.md },
      on_success: in_review }

  # The merge request is open: the issue moves to its in-review status.
  # Best effort again — a failure ends the chain like a success would.
  # Then the main chain ENDS: the run is in review, the watchers below keep
  # the merge request healthy, and the outcomes at the bottom end the run.
  - { id: in_review, type: action, action: ticket_transition, input: { to: in_review },
      on_fail: end }

watchers:
  # The target moved under the merge request. How it begins is the project's
  # call: by itself, after asking in the inbox, or only by hand. A round ends
  # with `end` — never `done`: the run goes back to waiting.
  - id: conflicts
    on: { signal: mr_conflicts, start: $conflicts_start }
    stages:
      - { id: sync,      type: action, action: sync_target, input: { strategy: merge },
          on_success: sync_test, on_fail: { to: resolve, max: 6, then: debug } }
      - { id: resolve,   type: agent, agent: resolver, max_turns: 30,
          input: { plan: artifacts/plan.md, conflict: $FAILURE },
          on_success: sync, on_fail: debug }
      - { id: sync_test, type: command, run_ref: $verify, on_success: sync_push, on_fail: debug }
      - { id: sync_push, type: action, action: push_branch, on_success: end, on_fail: debug }

  # Reviewers commented, then nothing for an hour. You pick which threads to
  # fix (inbox); they are fixed in one batch, tested, pushed, and answered —
  # as many rounds as the reviewers need.
  - id: review_fixes
    on: { signal: mr_review_settled, start: $review_start, quiet_for: 1h }
    stages:
      - { id: load, type: action, action: load_review_comments, input: { select: human } }
      - { id: fix,  type: agent, agent: builder, max_turns: 60,
          input: { plan: artifacts/plan.md, comments: artifacts/review.md, failure: $FAILURE },
          on_success: fix_test, on_fail: { to: fix, max: 2, then: debug } }
      # … commit, test, push, reply, post — as in the full text.

# Outcomes: once, and the run is over. Merged, the ticket is done and so is
# the run; closed unmerged, the ticket goes back and the run is abandoned —
# someone decided against it, nobody is asked again.
outcomes:
  - id: merged
    on: mr_merged
    stages:
      - { id: ticket_done, type: action, action: ticket_transition, input: { to: done }, on_fail: done }
  - id: closed
    on: mr_closed
    then: abort
    stages:
      - { id: ticket_back, type: action, action: ticket_transition, input: { to: todo }, on_fail: abort }
```

## §9 Agent-side interactions (ask_human)

A stage's agent can stop and ask. This is not a stage type and needs no
grammar — it is a property of every agent stage, and it is worth stating
because it changes how a stage behaves in flight.

The agent calls `ask_human{type, question, context?, options?, multi?,
artifacts?}` through an MCP tool in its workspace. The engine, not the
harness, owns the exchange:

- **The request is an event.** `ping_raised` records the type
  (`clarification` | `blocker` | `permission`), the question, any attached
  workspace files, the declared answer options, and the stage in flight; the
  answer is `ping_answered`. Both are durable, so an interrupted run re-arms
  its open questions on recovery and a late answer still lands.
- **The stage keeps running; its clock does not.** The agent process stays
  alive waiting for its tool call to return, and the stage's `timeout` is
  **suspended** for as long as any ping is open — a question a human takes an
  hour to answer cannot time the stage out. The run projects as
  `waiting_on_human`. Unlike a gate, an open question **holds the workspace**:
  the agent's turn is still open, so no other chain runs until it is answered.
- **`options` make the answer closed.** When the agent declares them, the
  answer must be one of their ids (or the reserved `other`, whose free text
  rides in the feedback) — the agent asked a closed question so it can branch
  deterministically. An option is a string, or `{id, label, detail}` when the
  human needs a word more than the id. With **`multi: true`** the human may
  pick any number of them, or none: the tool returns `selected: id, id` or
  `none selected`.
- **Every ping declares the shape of its answer** — `ping_raised.answer_kind`:
  `decision` (approve | reject, a gate or a watcher's or outcome's
  confirmation), `choice`
  (one option id | other), `multi` (option ids | none) or `text`. The inbox
  renders that shape and the engine validates against it; nothing is inferred
  from a ping's type.
- **Answers can carry files**, delivered into the workspace under
  `.apothecary/attachments/`, kept separate from the agent's own `artifacts/`.
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

Everything below follows from *The model*: one scheduler walking wait points
over one workspace. Additive, no grammar break:

- **A signal** — one `signalspec` registry entry plus a poller. A *waitable*
  signal is picked up by `wait` and a gate's `auto_approve`/`auto_reject` for
  free; a *firing* signal (the `Trigger` flag and a predicate in the idle
  poller) opens phases under every policy — `auto`, `confirm`, `manual` — on
  the subject it carries, and the scheduler does not change; an *outcome*
  signal (the `Outcome` flag, `waitState` saying when it holds) ends the run
  the same way — every approval given, a deploy landed, would each be one.
  CI failing on
  the merge request, a tracker status, a schedule: each is one signal. A
  signal that needs its own options (which status, which cron) is the one seam
  left open: a per-signal option block on `on:`, not built yet.
- **A sequence** — a chain several others share is a `sequences:` entry and
  a `sequence:` where it runs; the parser writes it out, so nothing
  downstream changes. A sequence with parameters of its own (a sequence
  stage passing values in) would be the next step, not built: today a
  sequence reads the flow's
  parameters like any stage.
- **A stage type** — a new kind of work is one `type` value and one executor
  case under the workspace mutex. The parser's field table, the dataflow
  checks and the editor follow.
- **A gate variant** — a new human interaction is a variant of `human_gate`:
  the same park, the same inbox, a different question shape.
- **An automation** — a new way to *begin* runs lives outside the run: a rule
  (a tracker query, a schedule, the merge requests others open) that starts a
  run of a flow whose `run_start` matches it — `ticket`, or `merge_request`
  for one watching merge requests — with its parameters, recorded as
  `phase_started{by: automation}`. The flow is untouched.
- **An action** — one `actionspec` registry entry, one executor case, one
  `requires` port string. Everything else (parsing, binding, the editor's
  picker, the dataflow view) follows from the signature.
- **A sentinel** — one constant plus one resolution case. The `$NAME` rule
  already tells authors it is engine-provided.
- **A parameter site** — one `lookup` call where a field is read, declaring
  which type it takes; the resolve path and the editor's `$name` pickers
  follow.
- **An output type** — like `verdict`: a value on an output's `type` plus the
  engine behaviour it implies.
- **An edge kind** — one `DocStage` field, one row in the edge registry, one
  raise site. Parsing, validation, routing and the `edge_taken` event
  generalize.
- **Another flow's chains** — a future `use:` would declare another flow of
  the project once and run its `main` or a sequence of it in place, expanded
  at parse time like a sequence; nothing in the scheduler changes.
- **A grammar break** — bump `CurrentVersion`. Every document states its
  version, so old text is refused with a message rather than mis-read (v4 did
  exactly this when wait points and parameters replaced the fixed envelope,
  v5 when one main chain, watchers and outcomes replaced v4's list of
  phases).

Deliberately *not* in this version, each a real change rather than a patch:

- **Parallel stages or chains** — the workspace is a mutex: one checkout, one
  branch, one history. Watchers on two merge requests take turns. Fan-out /
  join would be an engine rework (several cursors, replay, budgets, a second
  workspace), not a grammar patch — and it is not the design.
- **Routing on arbitrary structured output** — the edge kinds are fixed; a
  verdict is the one structured decision.
- **General value-passing or templating** — sentinels and parameters are the
  only engine-value channels, on purpose: a parameter is read at a typed site,
  never spliced into text.

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
