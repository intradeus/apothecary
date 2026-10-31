# The apothecary flow language

A **flow** is a YAML document describing how a ticket becomes a merge request
and what follows: which agents run, in what order, what each one reads and
writes, where a human decides, what happens when something fails — and what
the run does once the merge request is open, until it merges.

This document is the normative reference. Code cites it as `FLOWLANG §n`.

Four rules shape everything below:

1. **Every value a stage reads or writes is declared, in one form.** No stage
   quietly reads a file another stage happened to leave behind, and nothing
   arrives invisibly — not the ticket, not the feedback from a failed attempt.
   Every port is `name: { from | to, type }`, whatever the stage type, which is
   why the parser can check the dataflow and the graph editor draw it.

   What this rule does *not* claim: that a stage touches nothing else. Agent
   and command stages share one workspace — a checkout with a working tree, an
   index and a history — and that medium is ambient, not declared. A `commit`
   stage reads the whole tree by nature; `test` reads the repository; `push`
   writes a remote. Declaring `input: { message: { from: artifacts/commit.json, type: commit_message } }` says what
   the *artifact interface* is, and is honest about being only that. The
   workspace is shared because a run has one cursor (*The model*, below); that
   is the design, not a gap.
2. **Values are strict, types are extensible.** Every field takes a closed set
   of values, so a typo is a parse error rather than a surprise at 3am. New
   stage types, actions, signals, sources and edge kinds are added by
   extending a registry, not by loosening the grammar.
3. **Each fact is stated once, where it belongs.** An edge says where the run
   goes; the stage it lands on says what to do with what arrived. An agent
   declares what it needs; the stage that runs it says where to find it.
4. **A flow is valid on its own; a project binds its parameters.** The
   document declares every knob — with a default, or, for a model or a
   harness, without one — and parses as written; it runs once the project
   has bound what it left open. What varies between projects — the harness,
   the model, the test command, how a review round begins, where the tracker
   issue goes — is a parameter the workshop binds *outside* the document,
   and a run may override. Nothing project-specific is written into the
   YAML, so one flow fits a GitHub project with no tracker and a GitLab +
   Jira one alike — and nothing that changes under the document, a model id
   above all, is written into it either.

---

## The model

A flow is read by one scheduler over one workspace. Everything in the grammar
is one of a few things:

| piece | definition |
|---|---|
| **work stage** | `agent`, `command`, `action`. Runs in the workspace — one checkout, one branch — and at most one runs at any instant: the workspace is a mutex, which is what keeps the git history linear. |
| **wait point** | Where a chain stops for something outside the run. It has a **source** (a signal from the registry, a person, a timer), a **policy** (`auto`: proceed when the source holds; `confirm`: raise a decision first; `manual`: only a person proceeds, the event is advice), an optional **subject** the event carries (which merge request), an occurrence **key** (the same fact never fires twice), a run bound (`max`), optionally a debounce (`quiet_for`) and a `timeout`, and **outcomes**: proceed, declined, can no longer happen, timed out, rejected with feedback. A wait point releases the workspace — unless it is one that owns it: an agent's `ask_human` turn (§9), the `debug` leaf (§3). |
| **its spellings** | A watcher's or an outcome's `on:` is its opening wait point. `type: wait` is one mid-chain, on a signal. `type: human_gate` is one mid-chain whose source is a person. The confirmation ping in the inbox and the Run button on the run page are the `confirm` and `manual` policies of the same thing. |
| **chain** | A sequential list of stages: the **main** chain a run starts with, a **watcher** that runs on each new occurrence of its signal while the run is open, an **outcome** that fires once and ends the run (§1). Each is a *phase* of the run. |
| **cursor** | A run has exactly one. It stands in one chain, moving or parked at a wait point inside it, or at the **hub** between chains. All of a run's work lands on one branch, so nothing runs beside anything else: not two watchers, and no watcher beside the main chain. Each chain run has its own edge counters, feed and tip. |
| **scheduler** | The engine's idle loop. Each tick: observe the sources (the repositories, timers, answered pings). A parked chain keeps the cursor and is the only thing that can move, except an outcome, which overrides a running or a stopped chain alike (§1). At the hub, walk the wait points in document order — outcomes, then watchers — and open the first whose event is ready under its policy. That chain takes the cursor until its end. One start per tick. |
| **run end** | Two ends, said apart. `end` finishes a **chain**: it is over and the run goes back to waiting. `done` finishes the **run**, whatever else waits; `abort` abandons it. A flow must have some route to `done` (§3). A run normally ends in an **outcome**, on what became of the merge request (`mr_merged`, `mr_closed`): it fires once and ends the run — the `builder` template's `merged` moves the ticket to done and says `done`, its `closed` says `abort`. Without one the run **completes** when no chain is active, none is parked, and nothing is **armed** — an outcome that has not fired while a merge request is open; a watcher with policy `auto` or `confirm`, its signal live for an open merge request, runs left. A dead end — a failure with nowhere to go — abandons the run in the main chain or an outcome; in a watcher it ends that round (§3). |
| **extension points** | A new thing to react to is one signal plus its poller: it works for watchers, outcomes and mid-chain waits, under every policy, with subjects, and the scheduler does not change. A new kind of work is a stage type and an executor. A new human interaction is a gate variant. A new way to begin runs is an automation outside the run, starting a flow whose `operates_on` fits it. Parallelism is deliberately absent: the one cursor is the design (§10). |

---

## §1 Document

```yaml
name: builder              # required, slug: the flow's permanent id
version: 5                 # required int: the grammar this document is written in
description: "..."         # optional
operates_on: ticket        # what each run works on: ticket (default) | merge_request (below)
ask: { harness: opencode } # optional: the Ask pane, a read-only session beside the run (below)
parameters:                # the flow's ONE settings block (below): every entry is a knob
  harness: opencode        #   the harness kind stages read as $harness
                           #   model: left out on purpose — no default; the project binds
                           #   it, a run may override, and stages read it as $model
  timeout: 30m             #   wall clock per agent/command stage (suspended while a ping waits)
  max_turns: 50            #   per agent stage
  max_cost_usd: 10.0       #   run-wide ceilings; exceeding either abandons the run
  max_total_turns: 300
  verify: verify           #   the flow's own knobs, read where $verify appears
schemas: [...]             # optional: the structures the flow's own files hold (§4 Schemas)
mcp_servers: [...]         # optional: tools beyond a harness's own its agents may use (below)
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

### Ask: a read-only session beside the run

```yaml
ask:
  harness: opencode        # the harness kind it runs on — a kind, or a $parameter
```

A flow with an `ask:` section gives every run an **Ask** pane: a session of
its own in the run's workspace, there from the moment the workspace is up
until it is destroyed, that answers questions about the code as it stands.
It is not a stage and runs beside whatever stage is working: the engine
prepares a read-only agent for it (no edit, write or shell tools) and opens
its session read-only, so it cannot step on the stage's changes. The model is
picked in the pane, not here — the flow only says which harness. The
conversation persists with the run.

Each harness keeps the promise its own way: opencode by the agent's tool
definition, codex by its read-only session mode, omp by a second process
that loads only reading tools. A harness that can promise none of that
keeps the pane off, with the reason shown; an unknown harness kind is
refused at run creation like a stage's. The name `ask` is the pane's agent: a flow with `ask:` cannot
declare an agent by that name.

### Agents live in the flow

A flow **defines** every agent it runs. There is no separate library to
resolve against:

```yaml
agents:
  - name: reviewer                      # slug, unique in the document
    description: adversarial code reviewer
    read_only: true                     # deny the harness's edit/write tools
    allow_bash: true
    mcp: [browser]                      # optional: the flow's mcp_servers it may use (below)
    inputs:                             # what it reads, typed — a stage running it says from where (§4)
      - name: plan
        type: file
        description: the approved plan to check the change set against
      - name: failure
        type: failure
        description: what the previous attempt printed when it failed
        optional: true                  # so a stage may feed it edge.failure
    outputs:                            # what it writes, typed — a stage running it says where
      - name: verdict
        type: verdict                   # file | files | verdict | review_threads | review_replies | commit_message
        description: APPROVE or REJECT with actionable comments
    prompt: |
      You are the adversarial reviewer of a software factory...
```

Three things follow, and they are the point:

- **The contracts are parse-time facts.** A port the agent does not declare, a
  required input or an output left unstated, a type the agent does not read,
  or `on_reject` on a stage that writes no verdict are all **errors that
  refuse the document** — not
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

The graph editor re-emits a canonical form — the six parameters the engine
reads first, in their order, then the flow's own; agents before stages; stages
in success order, an explicit `on_success` on every chain's stage; no comments
— and `docToYaml ∘ parse` is a fixed point, so opening and saving an
already-canonical flow produces no change. Everything the document can say,
the editor can write: a flow is made whole in the graph, YAML never required.

**The schema.** `GET /api/flows/schema` serves the document's JSON Schema —
the YAML as written, shorthand included (`on: mr_conflicts`, `on_fail:
debug`, a parameter's bare default) — with its lists taken from the
registries: stage types, signals, actions and each action's inputs, start
policies. The editor's YAML tab checks, completes and documents from it as
you type; any editor can (`# yaml-language-server: $schema=<url>` for VS
Code's YAML extension). It cannot say everything — that a parameter's enum
fits a field, that a route stays in its chain — so the parser stays the
authority, and the YAML tab shows its errors too, on their lines. A test
keeps the schema and the grammar's keys the same.

**Where flows live.** A flow belongs to one project — its stages name that
project's commands and its agents know that repository — or it is a
compiled-in **template** that any project runs as it is until it saves its own
copy. Harness kinds, `run_ref`s and action integrations are *free text* at
parse time and validated against the project when a run is created (§6); what
differs between projects beyond that is a parameter, bound per project without
touching the document.

### MCP servers: tools beyond the harness

An agent's tools are its harness's — read, edit, bash — plus the MCP servers
the flow declares and the agent lists:

```yaml
mcp_servers:
  - name: browser
    builtin: browser                    # apothecary's: the project's browser (below)
  - name: tracker
    description: the team's issue tracker
    command: [npx, -y, "@acme/tracker-mcp"]   # a program speaking MCP on stdio, run in the workspace
    env: { TRACKER_TOKEN: "${secrets.tracker_token}" }
  - name: docs
    url: https://mcp.example.com/       # a remote server (streamable HTTP)
    headers: { Authorization: "Bearer ${secrets.docs_key}" }
agents:
  - name: shooter
    mcp: [browser]                      # what this agent may call; the others never see it
    …
```

A server is exactly one of `builtin`, `command` or `url`; `env` goes with a
command, `headers` with a url. A secret is never written in the flow: a value
says `${secrets.<name>}`, filled from the **project's secrets** (Workshop →
Settings) when the stage starts — never in the command or the url, never in
an event or a transcript. Run creation refuses a flow reading a secret the
project does not have. Each harness registers the servers its agent lists, and
only those.

**`builtin: browser`** is the project's browser: one container per project,
shared by its runs, that starts a Chromium for each agent session and drives it
through Playwright's MCP server — navigate, click, type, screenshot. It reaches
the *run's* services at `http://localhost:<port>` (§2 *service*), exactly as
the agent would from its terminal, and nothing else — any other host is
refused, so the browser is no way around the project's egress rules. The
project turns it on (Workshop → Settings → Browser); run creation refuses a
flow whose agents use it while it is off. A screenshot the agent names
(`browser_take_screenshot` with a filename) is written into the workspace,
relative to where the agent works — `artifacts/shots/home.png` lands where a
`files` port reads it; what else the browser saves (page snapshots, PDFs)
stays in its container, so an agent reads a page with `browser_snapshot`.

The servers ride the agent's session: each session starts its own (one
Chromium per session), and an agent's harness sees only the ones it lists. A
`url` server needs a harness that takes remote servers — one that does not
fails the stage saying so.

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
| `harness` | string, a harness kind | *none*: unbound until the project binds it, or a run sets it | every agent stage, and the Ask pane, written `harness: $harness` |
| `model` | string, `provider/model` | *none*: unbound until the project binds it, or a run sets it | every agent stage written `model: $model` |
| `timeout` | duration | `30m` | every agent and command stage that declares none |
| `max_turns` | int | `50` | every agent stage that declares none |
| `max_cost_usd` | float | `10` | the run: exceeding it abandons the run |
| `max_total_turns` | int | `300` | the run: exceeding it abandons the run |

The flow's own knobs are any other slug name, and each **declares what it
holds**: a `type` — `bool | int | float | string | duration` — and, with
`list: true`, any number of values of it. The engine's six take a bare default
(their type is the engine's: the table above):

```yaml
parameters:
  harness: opencode
  verify:          { type: string, default: verify, description: "the project command that runs the tests" }
  review_model:    { type: string, default: $model, description: "the reviewer's model — bind a second vendor's" }
  plan_gate:       { type: bool, default: true }
  conflicts_start: { type: string, default: confirm, enum: [auto, confirm, manual] }
  rereview_quiet:  { type: duration, default: 10m }
  mr_labels:       { type: string, list: true, default: [apothecary] }
```

The type is declared, never read off the default: the default reads *as* it
(`default: "true"` is a bool's true when the type says bool), and must parse
as it — a `duration` as a positive Go duration (`30m`, `2h`). A list's default
is a list. An `enum` is a string's (or each item of a list of strings) and
holds the default. A bound value is the type's text — a list's a JSON array,
`["apothecary", "bot"]` — and, with an enum, one of its values. A declaration
without a type is refused with the one it would have had (*"verify: type is
required — type: string"*). The engine-known names cannot be declared as
another type, nor as lists, and the editor pins them at the top of the
block.

**No default: the project's to bind.** A parameter may be declared without a
default — `model: { description: "..." }`, or simply left out of the block
for `harness` and `model`, which exist regardless. It then has **no value**
until the project binds it or a run sets it, and the document is still valid:
it parses, it is saved and edited, the editor shows the parameter as *no
value yet*. Only a parameter read as a **model** or a **harness** may wait
like this (anywhere else — a `run_ref`, a timeout — the parser demands a
value now); run creation refuses while one is unbound, naming it, the stages
reading it, and where a value comes from (*"model" has no value (read by
build, review): bind it in Workshop → Flows → Configure, or set it when
starting the run*), and the project's preflight reports the same under its
`parameters` step. This is why no template carries a model: model ids change
under a document that must not, and a run that pinned yesterday's model
would fail before its first stage.

**A default that reads another parameter.** `default: $name` makes a
parameter's value follow another's — `review_model: { type: string, default:
$model }` runs the review on the build's model until the project binds a
second vendor's. The other must be declared, of the same type and shape, and
the chain may not loop back; a bound value still wins over the followed one,
and an unbound end of the chain is reported as that parameter, not as the one
following it.

**Stages sharing a model parameter share a harness.** Two harnesses rarely
list the same model ids, so one value cannot be right for both. Two agent
stages reading the same model parameter must therefore run on the same
harness *as written* — the same kind at both, or the same harness parameter:
`harness: $harness` at both is one harness, `harness: codex` at one and
`$harness` at the other is not, even on a day the binding happens to say
`codex`. The parser refuses the document otherwise and says which stage to
give its own parameter. What follows for free is what the forms show: a
model parameter belongs to exactly one harness, so Configure and the
start-run form list that harness's models beside it — the one named at its
stages, or the current value of the harness parameter they read.

**Where a parameter is read.** `$name` — lower-case, as a field's whole
value; the only other thing `$` spells is `${secrets.<name>}` inside an MCP
server's `env` or `headers` (above) — at these sites only:

| site | the parameter it takes |
|---|---|
| an agent stage's `model`, `harness`, `max_turns`, `timeout`; a command's `run_ref`, `timeout`; a service stage's `service`, `timeout` | a string; an int; a duration |
| an action setting (`with:`) | a string — an enum setting needs an enum parameter whose values all sit within the setting's; a list setting (`create_mr`'s `labels`) a list of strings |
| a watcher's or an outcome's `on.start`, a watcher's `on.quiet_for`, `on.max` | an enum within `auto \| confirm \| manual`; a duration; an int |
| a stage's `run_if` | a bool |
| a source `run.service.$name` | a string |

A list parameter is read only where a list is taken, and a single value never
where one is: `labels: $mr_labels`, not `timeout: $mr_labels`.

Nowhere else. A field that reads no parameter refuses one written as its
whole value — a description (the flow's, a stage's, a watcher's, an
outcome's, a sequence's, a parameter's), a gate's `question`, a `run:`
script, an agent's prompt and descriptions, a path: there `$name` would stay
the text "$name", never the bound value. Names — stage ids, signals,
sequences, agents — refuse it as the names they are not. A declared
parameter named *inside* a `run:` script or a prompt is warned about: neither
is templated, so the shell reads `$name` as its own variable and the agent
reads the text. The parser checks a reference the way it checks a literal —
the resolved value goes through the same validators, with the parameter named
in the error — and warns about a parameter nothing reads.

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
operates_on: ticket                                      # what each run works on: ticket (default) | merge_request
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

- **`operates_on`** says what each run of the flow works on — not what
  starts it: `ticket`, the default — started from the ticket's page, or by a
  `tracker_query` or `cron` automation — or `merge_request`, a merge request
  someone opened — named on the start-run form (the open ones are listed),
  picked on the Repositories tab, or by a `merge_request` automation.
  Starting a run is always someone's decision; the automations are the
  listeners. A run started on the other kind of thing is refused. (The key
  was `run_start:`; that spelling is refused with this one.)
- **The main chain** runs once, when the run is started; its
  `phase_started` records who started it (`by: human`, or the automation). It
  is never started again — a rerun is a new run. Its last stage ends it with
  `end` when the flow has watchers or outcomes (the run stays open), and with
  `done` when it has neither (the run is over). A flow with no `main` only
  watches — a *caretaker* — and must be `operates_on: merge_request`: there is
  nothing to watch on a bare ticket. Its runs start idle.
- **On someone's merge request.** With `operates_on: merge_request` the run
  works on that merge request's branch, checked out from the ref its
  repository keeps for the head (`refs/pull/<n>/head`,
  `refs/merge-requests/<iid>/head` — a fork's too, on a local `mr-<iid>`
  there is no pushing to). The merge request is the run's from the start: the
  main chain acts on it as its subject (no `create_mr`, no `run_if: run.subject`
  needed), `run.git.diff_target` is what it merges, each later chain on it first
  fast-forwards to its current head, and the outcomes follow its fate. A
  rerun stays on the merge request. The built-in `reviewer` is this shape: one
  `review` sequence (`load_review_comments` of every thread already there, read,
  an optional curating gate, `post_review`) run by its main chain and again by
  the watcher `again` on `mr_head_moved`, and outcomes on `mr_merged` and
  `mr_closed`. `conflict-resolver` is the same shape around a
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

**One cursor.** A run has one cursor, always. It is in one chain, moving or
*parked* at a wait point inside it (§2 `wait`, `human_gate`), or at the hub
between chains. A chain keeps it until it ends: only one watcher runs at a
time, no watcher runs while the main chain does, and all of a run's work
lands on one branch in order. A parked chain still holds it: the run is idle
and nothing is spent, but no watcher starts, on a signal or by hand, and none
is asked about, until that chain ends. What holds meanwhile is shown on the
run page and is picked up at the hub, if it still holds.

**Outcomes override.** An outcome is terminal, so it takes the cursor from
wherever it stands, the moment its signal holds. A running stage is stopped; a
chain parked at a gate or a `wait`, or stopped on an agent's question, the
thread checklist, the debug leaf or a person's pause, is left where it is. That
chain never resumes, and its open questions are withdrawn when the run ends.
Four things go first:

- **A take-over.** A person has the agent, and an outcome never takes it from
  them: it fires once they let go.
- **A `wait` the signal settles,** so a chain that has something to do after the
  merge still does it.
- **A chain that merged the merge request itself** (`mr_merge`) goes on to its
  end: the merge is its own doing, and what follows it is why it is there.
- **An action stage** finishes, because what it calls cannot be taken back
  halfway. The cursor moves as soon as it has.

An outcome that asks first (`start: confirm`) or waits for a person
(`start: manual`) overrides nothing: it waits for the hub, where it can ask.

A confirmation (`start: confirm`) is asked at the hub and stays open while
other chains take their turn: it is a question, not a chain. Approved, its
watcher starts the next time the cursor is at the hub.

**On a subject** — a chain run on one merge request: a watcher on a
merge-request signal or started by hand on one, the main chain of a run
started on one, an outcome holding for one — everything is scoped to the
subject: command stages run in its checkout (artifacts stay in the primary's
`artifacts/`, so address them as `$HOME/repo/artifacts/…`), `run.mr.url` is its
merge request, `run.git.diff_target` is its branch against its target and
`run.git.diff_phase` what *this chain run* committed, and agents get a `# SUBJECT`
prompt section saying which checkout to edit (the others are read-only
context). `each_repo` does not apply there (the parser warns). A *subject
action* — `sync_target`, `load_review_comments`, `reply_review_comments`,
`mr_merge`, `post_review` — in a chain run that has no subject fails
routably with `input_missing`: guard it with `run_if: run.subject` (§2) or route
`on_fail`; the parser warns where that can happen. Each chain run's edges
start with fresh counters (`max` is per run of the chain); the budget is the
whole life's. A dead end in a watcher — a failure with nowhere to go, a
person giving up at the debug leaf — ends that round and the run goes back
to waiting; `abort` and `done` still end the run itself.

The run page starts a watcher by hand with
`POST /api/runs/{id}/phases/{phase}/start`, naming in its body the checkout
whose merge request is the subject — else the single open one, else none —
while the cursor is at the hub. The main chain is never started by hand.

### How a run ends

Three terminals end things, and they are not the same thing (§3):

- **`end`** finishes a **chain**. It is over and the cursor goes back to the
  hub: a watcher starts on its signal, an outcome fires. A chain's last stage ends this way unless it says otherwise, and
  a watcher, which may run any number of times, always does.
- **`done`** finishes the **run**: it completes now, whatever is still armed,
  and the open questions are withdrawn. It is said where the work is
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
      - { id: ticket_done, type: action, action: ticket_transition, with: { to: done }, on_fail: done }
  - id: closed
    on: mr_closed                      # closed unmerged: the ticket goes back, the run is abandoned
    then: abort
    stages:
      - { id: ticket_back, type: action, action: ticket_transition, with: { to: todo }, on_fail: abort }
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
the merge in the same chain. That chain keeps the cursor until the merge, so
no watcher runs in between, and the parser warns; an outcome's chain does not
have that cost.

Without an outcome or a `done`, a run **completes** when nothing is left to do
and nothing is waiting: no chain holds the cursor, and nothing is
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
      - { id: commit, type: action, action: commit,
          input: { message: { from: artifacts/commit.json, type: commit_message } },
          on_fail: debug }
      - { id: test,   type: command, run_ref: $verify,
          on_fail: fail,                                # the sequence stage's on_fail
          on_timeout: { to: test, max: 1, then: fail } }
  - id: make
    stages:
      - { id: build,  type: agent, harness: $harness, model: $model, agent: builder,
          input: { failure: { from: edge.failure, type: failure, optional: true } },
          on_fail: { to: build, max: 2, then: debug } }
      - { id: ship,   sequence: ship, on_fail: { to: build, max: 5, then: debug } }
      - { id: review, type: agent, harness: $harness, model: $model, agent: reviewer, on_reject: { to: build, max: 3, then: debug } }

main:
  - { id: plan, type: agent, harness: $harness, model: $model, agent: planner }
  - { id: make, sequence: make }                        # runs make.build, make.ship.commit, …
watchers:
  - id: review_fixes
    on: mr_review_settled
    stages:
      - { id: address,  type: agent, harness: $harness, model: $model, agent: builder }
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
| the services | the project's long-running processes — its app, a database — each a start script, a stop, a health probe and its ports, started by a `service` stage (§2) or by a person from the run page |
| the parameters | the values the run was created with (§1 *Parameters*) |
| run identity | run id and its bearer token, for the ask_human channel |

A flow declares none of it because none of it varies per stage. `run.ticket`,
`run.git.*`, `run.mr.url`, `run.workspace` and `run.service.<name>` (§4) are how a stage reaches into
this context explicitly, and those *are* declared, as ports.

---

## §2 Stages

```yaml
- id: build                # required, slug, unique; `end`/`done`/`abort`/`debug`/`start`/`fail` reserved
  type: agent              # agent | command | human_gate | action | wait | service
  description: "..."       # optional note; documentation only, reaches no prompt
  run_if: $plan_gate         # optional condition (below); false ⇒ the stage completes as skipped
```

| type | what it does | key fields |
|---|---|---|
| `agent` | runs an agent this flow declares (§1) | `agent`, `input`, `output`, `model`, `harness`, `max_turns`, `timeout` |
| `command` | runs a shell script in the workspace | `run` **or** `run_ref`, `input`, `output`, `timeout`, `each_repo` |
| `human_gate` | parks the phase on a human decision | `question`, `input` |
| `action` | invokes a built-in apothecary integration | `action`, `input`, `output`, `with` |
| `wait` | parks the phase until a signal holds | `for`, `timeout` |
| `service` | starts, restarts or stops one of the project's services | `service`, `do`, `timeout` |

A field that does not apply to a stage's type is a parse error (`model` on a
command, `question` on an agent, and so on).

### agent

```yaml
- id: build
  type: agent
  agent: builder                        # a name in this flow's agents block
  input:                                # the agent's inputs: from where, what
    ticket:    { from: run.ticket, type: ticket }
    plan:      { from: artifacts/plan.md, type: file }
    rejection: { from: edge.verdict, type: verdict, optional: true }   # what an edge brings back
  output:                               # every output the agent declares, and where it goes
    notes:     { to: artifacts/build-notes.md, type: file }
  harness: $harness                     # required: the harness kind, or a parameter ($harness)
  model: $review_model                  # required: provider/model, or a parameter — stages reading
                                        #   the same model parameter run on the same harness (§1)
  max_turns: 80                         # optional overrides of the flow's parameters —
  timeout: 45m                          #   a literal, or a parameter of the right type
```

An agent stage **states its harness and its model**; neither falls back to a
flow-wide default. The usual spelling is the parameters, `harness: $harness`
and `model: $model`, so that what every stage runs on is read off the stage
and bound by the project in one place.

The agent's definition in this document's `agents:` block — its **contract** —
declares what it reads and writes (§1, §4). The stage states that contract as
ports, saying where each comes from and goes; it cannot invent ports the agent
does not declare, leave out an output or a required input, or change a type,
and the parser refuses the document if it tries.

### command

```yaml
- id: coverage
  type: command
  run: "make coverage > artifacts/coverage.txt"   # ...or run_ref: verify, or run_ref: $verify
  input:                                           # checked before the script runs
    thresholds: { from: repo:ci/thresholds.json, type: file }
  output:                                          # checked once it exits 0
    report: { to: artifacts/coverage.txt, type: file }
  timeout: 20m                                     # optional; absent = parameters.timeout
```

Exactly one of `run` (literal bash) or `run_ref` (a key in the project's
`commands`, often read from a parameter so the project names it) is required.
A command has no contract to check its port names against, so they are free
labels — and that is the point: they say what each file is *for*, and they
title the file's dot in the graph. Declaring them is what makes the files a
script reads and writes visible as dataflow rather than buried in an opaque
string; the script itself names the same paths, literally. A port reads files
only — `run.*` and `edge.*` are an agent's or a gate's (§4).

Every command script can call **`apothecary-shim jq`** — a full jq CLI (gojq,
inside apothecary's own static shim), taking jq's usual flags and arguments:
`apothecary-shim jq -r .line artifacts/thresholds.json`. Use it instead of `jq`
or `python`, which the repo's image may not have.

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
  run_if: $plan_gate                                # a project may switch the gate off
  question: "Approve this implementation plan?"   # what the human is asked
  input: { plan: { from: artifacts/plan.md, type: file } }   # what they are shown
  on_reject: { to: plan, max: -1 }
```

A gate is a wait point mid-chain (*The model*) whose first source is a person.
It **parks its chain**: the run is idle while the question is open and nothing
is spent, but the chain keeps the cursor, so nothing else starts until the
answer resumes it (only an outcome overrides it, §1).

Every gate is the **same machine**: approve follows `on_success`, reject follows
`on_reject`, each carrying the person's verdict. There is no gate kind — what a
particular gate means ("approve the plan", "merge when satisfied") is said in its
`question`, and what it hands over is its `input`: the files to review — a `file`
port each, a `files` port's folder file by file (the first 20, none over 10 MB) —
and at most one link (`run.mr.url`, `run.workspace`, `run.service.<name>`). Both
are optional; a gate with no question is labelled by its stage id. The one
decision every flow needs — "the workspace is parked, fix it, then run the stage
again" — is not a gate you write but the `debug` leaf (§3).

```yaml
- id: look_gate
  type: human_gate
  question: "Does it look right?"
  input:
    plan:  { from: artifacts/plan.md, type: file }
    shots: { from: artifacts/shots/, type: files }    # each screenshot, reviewed in turn
    app:   { from: run.service.$app, type: link }     # and the app itself
  on_reject: { to: build, max: 2 }
```

**A review is the answer's shape.** The person reviews every file on one page and
answers with comments, each on one file: on a stretch of a text file's lines,
quoting what they selected, or on the whole file — always, on an image. With them
come a summary and any files they attach. Approving can say something too. The
engine writes that structure, file by file, into the verdict the next stage reads
as `edge.verdict` (§4) — the page never writes the agent's prompt — and keeps it
on the record, comment by comment. An approval that says nothing carries nothing.

**A review document is curated.** A gate that shows a `review_threads` file (the
reviewer's draft before `post_review`) or a `review_replies` file (a round's
answers before `reply_review_comments`) opens the curation page instead: every
note or reply as the agent wrote it — its kind, its posted text, and beside it
what the agent wrote for the person alone (its rationale, evidence, example),
with the thread it continues or answers when the gate also shows the
`mr_threads` file. All of it is selected; the person unpicks what should not go
out, rewords what should read differently, rewords the summary, and approves.
The answer carries their decisions over the document as the gate showed it —
never a rewritten file — and the engine rebuilds the document from the snapshot
it pinned when it raised the question: the next stage that reads the path gets
the curated document, so `post_review` and `reply_review_comments` post exactly
what was kept, and need no port for it. The decisions go on the record
(`review_curated`): a note dropped is never proposed again in the run — the
re-review is handed it under `dropped` and refused if it proposes it anyway —
and a reply held back leaves its thread alone until someone writes there again,
exactly as unpicking it in the round's checklist would. A rejection carries the
same decisions to the redo in its verdict, and writes nothing.

### action

```yaml
- id: mr
  type: action
  action: create_mr                     # a slug from the compiled-in registry
  input:                                # the files it reads, typed as the registry says
    body: { from: artifacts/mr.md, type: file }
```

```yaml
- id: sync
  type: action
  action: sync_target
  with: { strategy: rebase }            # its settings: literals, or a $parameter
```

Actions are apothecary's built-in integrations. Unlike agents they are compiled
in, so an unknown `action:` is a **parse** error and its ports and settings are
checked immediately: every file it reads and writes is a port (§4), typed as
the registry says — every output stated, the stage saying where — and every
literal choice a setting under `with:`. Registry (`GET /api/actions`); *requires* names the integration
the project must have — a repository (GitLab, GitHub) or a tracker:

| action | requires | `input` (ports) | `with` (settings) | `output` (ports) | notes |
|---|---|---|---|---|---|
| `create_mr` | repository | `body` (`file`, required), `attachments` (`files`) | `labels` (a list: `[a, b]` or a list `$parameter`) | — (its link is `run.mr.url`) | at most one per chain; the body's first `# ` heading becomes the MR title. Each attachment is uploaded to the repository host (below) and linked where the body names its path; the rest follow the body, images under *Screenshots*. On a multi-repo run: one merge request per repository the run changed, each onto the branch that checkout forked from. A merge request the branch already has is adopted, never duplicated — so a chain that opens one may safely run again. `labels` go on once it is open (a label the repository lacks is created); a host refusing them is logged, and the merge request stands |
| `commit` | — | `message` (`commit_message`, required) | — | — | commits everything uncommitted — staged, unstaged, untracked — with the message's subject and body, in every checkout (the subject's alone on a subject). A checkout with nothing to commit is left alone, so a re-entry after the commit landed does nothing. A message that is not a `commit_message` fails with `input_invalid` before git runs; a refused commit (a hook, the git identity) fails with git's words |
| `push_branch` | — | — | — | — | pushes the run's branch (every checkout's, or the subject's on a subject). A branch it pushed before is force-pushed **leased** against the tip it recorded — on a subject, at worst the head when the chain started — never blind; a first push is plain. Refused = someone else wrote to the branch: `on_fail` |
| `sync_target` | — | — | `strategy` (`merge` \| `rebase`, default `merge`) | — | a *subject action*. Takes the subject's target branch in, in the engine: a no-op when already contained; a conflict leaves the merge (or rebase) **paused** in the working tree and fails the stage with the files named. Whoever resolves it edits those files — no git — and the retried stage checks no marker is left and continues. `rebase` needs a `push_branch` in the same chain |
| `load_review_comments` | repository | — | `scope` (`pending` \| `all`, default `pending`), `select` (`human` \| `all`, default `human`) | `threads` (`mr_threads`, conventionally `artifacts/threads.yaml`) | a subject action. Writes the subject's threads as an `mr_threads` file: file, line, diff hunk, thread id, the comments in order — the factory's own marked `ours`, the ones newer than its last word marked `new`, what an earlier reply did read off its marker — and the notes a person dropped at a gate earlier in the run. `scope: pending` (a fixing round) loads the threads waiting on the factory (unresolved, last word a reviewer's — inline threads, review bodies, conversation comments; bot accounts excluded): with `select: human` a person first picks which to fix (an inbox checklist; the chain parks), unpicked threads are skipped until someone comments on them again, and picking none ends the round without running anything else. `scope: all` (a reviewer about to write) loads every thread, resolved and the factory's own included, picks nothing, and an empty list ends nothing. A re-entry rewrites the same set |
| `reply_review_comments` | repository | `replies` (`review_replies`, required) | — | — | a subject action. Posts one reply per entry of a `review_replies` file — every thread the round loaded has one, minus those a person held back at a gate — resolving where it says so; each reply carries an invisible marker with what it did, so a crash between two posts is reconciled from the repository and nothing is posted twice |
| `post_review` | repository | `review` (`review_threads`, required) | — | — | a subject action. Posts a `review_threads` file: its notes as a review — one on each line it names, its kind as a leading tag, the summary on top — and its replies in the threads they name, resolving where it says so. A note on a line outside the change joins the summary, quoted with its place, rather than failing the review. Never approves nor requests changes — that stays a person's decision. Every part carries a marker (a note's kind in it), so a retry posts only what is missing, a later poll knows the factory's threads, and a review's notes never count as a reviewer's comments (`mr_review_settled`). An empty review posts nothing. GitHub gets one review; GitLab a discussion per note and a note for the summary |
| `mr_merge` | repository | — | `method` (`merge` \| `squash` \| `rebase`, default `merge`), `branch` (`keep` \| `delete`, default `keep`) | — | a subject action, needing no workspace. Merges the subject merge request at the newest head the chain knows: the tip it pushed, else the head it began on — a head someone else pushed is not merged. The repository's rules apply: a merge it declines (a draft, checks or approvals missing, the head moved) fails the stage with its reason, for `on_fail`. `rebase` is GitHub's; GitLab merges by the project's merge method (fast-forward for a linear history), so it refuses it. The merge is what the `merged` outcome then sees |
| `ticket_transition` | `tracker` | — | `to` (`todo` \| `in_progress` \| `in_review` \| `done` \| `blocked`) **or** `status` (text), exactly one | — | moves the ticket's tracker issue. `to` is a **canonical** status the ticket's tracker translates into its own vocabulary — Jira through the integration's status map (the stock words by default; `done=Ready for prod` for a team whose Done is a deploy away), GitHub and GitLab Issues to open/closed — and a status the tracker has no word for **skips** the stage with a note. `status` is the tracker's own status or transition name, verbatim (case-insensitive). Already there: nothing happens. Not reachable from the current status: the stage fails with `input_invalid` naming the statuses that are |
| `ticket_comment` | `tracker` | `body` (`file`, required), `attachments` (`files`) | — | — | attaches each file to the issue and links it where the body names its path (the rest listed after it), then posts the markdown artifact as a comment, ending with a short trailer naming the run, the chain run and the stage — also how a crash between posting and recording is reconciled, so nothing is posted twice |
| `ticket_set_field` | `tracker` | `value` (`file`) — exactly one of `value` / `text` | `field` (text, required); `text` (text); `mode` (`replace` \| `append`, default `replace`) | — | writes one field by its display name (`"Feature flags"`, `labels`). `append` merges comma-separated items into the current value (a union: re-applying changes nothing). A field the tracker has no name for, or cannot write from text, fails the stage with `input_invalid` |
| `ticket_link` | `tracker` | — | — | — | attaches every merge request the run opened (the subject's on a subject) to the issue — a remote link on Jira, a comment on GitHub — titled with the branch; an upsert, so a re-entry attaches nothing twice. Needs a merge request: `input_missing` without one, and the validator warns when no `create_mr` precedes it |
| `ticket_link_branch` | `tracker` | — | — | — | attaches the run's branch to the issue — every checkout's that is on its repository host (the subject's on a subject) — as the host's own page for it, titled with the branch; an upsert, like `ticket_link`. Needs no merge request, only the branch pushed: the primary's (or the subject's) not being on the repository host yet fails with `input_missing`, so it goes after a `push_branch`. This is a link on the issue, not Jira's *Development* panel — that panel lists a branch whose **name** carries the issue key (`feat/proj-123-…`, what an imported ticket's branch is named by default), once the repository host is connected to Jira |

An action's enum setting may read an enum parameter whose values all sit within
the setting's (`to: $final_status`); a text setting may read a string parameter.

**Attachments.** `create_mr` and `ticket_comment` take a `files` directory —
an agent's screenshots — as `attachments`. Each file is uploaded where the
body will be read: GitLab's project uploads, GitHub's user attachments (which
need a **classic** personal access token with the `repo` scope — a
fine-grained or an app's token cannot upload, and GitHub Enterprise Server has
no such endpoint, so both fail the stage saying so), Jira's issue
attachments, GitHub Issues as GitHub, GitLab Issues as GitLab. Where the body names a file by its path —
a link or an image `![home](artifacts/shots/home.png)`, or the bare path — the
reference becomes the uploaded file's link; a file it never names is listed
after it, images under *Screenshots*, the rest under *Attachments*. At most 20
files of at most 10 MB each, checked before anything is uploaded
(`input_invalid`); an upload the host refuses is an `on_fail`. A merge request
the branch already has is adopted before anything is read, so a re-entry
uploads nothing.

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
`edge.failure`), never terminal: route it to the next stage for "best effort".
Every tracker action is *remote*: it needs no workspace unless one of its file
inputs is bound, so a flow may open with `ticket_transition` and have the
issue moved seconds in, before anything is provisioned — and the `merged`
outcome's move never re-provisions a workspace the reaper took.

```yaml
- { id: kick,   type: action, action: ticket_transition, with: { to: in_progress },
    on_success: plan, on_fail: { to: plan, max: 1 } }   # best effort: a tracker hiccup never blocks the work
…
- { id: mr,     type: action, action: create_mr,
    input: { body: { from: artifacts/mr.md, type: file } }, on_success: link }
- { id: link,   type: action, action: ticket_link, on_success: flags }
- { id: flags,  type: action, action: ticket_set_field,
    input: { value: { from: artifacts/feature_flags.txt, type: file } },
    with: { field: "Feature flags", mode: append },
    on_success: in_review }
- { id: in_review,   type: action, action: ticket_transition, with: { to: in_review }, on_fail: end }
…
# the outcome: once every merge request merged, the ticket moves and the run is done
- id: merged
  on: mr_merged
  stages:
    - { id: ticket_done, type: action, action: ticket_transition, with: { status: "Ready for prod" }, on_fail: done }
```

`artifacts/feature_flags.txt` is an agent's output like any other — a small
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
action with nothing to act on — with the reason as `edge.failure`. Its
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
  path; `run.git.diff_target` and `run.git.status` are grouped per repository;
- a command stage marked `each_repo: true` runs in each checkout in turn, and
  the `commit` and `push_branch` actions commit and push every repository the
  run touched;
- `create_mr` opens one draft merge request **per repository the run changed**
  (an untouched extra repository gets none), each onto the branch that
  checkout forked from; a gate on `run.mr.url` lists them all, and `mr_merged` /
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
nothing is spent, and the chain keeps the cursor — and resumes it when the signal
holds (`on_success`), when it can no longer hold (`on_fail`: waiting for a
merge once a merge request is closed, or with no merge request at all), or
when `timeout` passes (`on_timeout`, else `on_fail`). A parked wait survives
a restart: the engine replays to the park and resumes polling, raising
nothing twice.

Registry (`GET /api/signals`). A signal says what it **is**, never what it
does: the section that names it decides that (§1). One is not observed:

| signal | holds when |
|---|---|
| `manual` | never on its own: a person starts the watcher from the run page |

A run's start is no signal: a person or an automation starts the run, and its
main chain — which no `on:` opens — runs. Nothing the flow listens for starts
a run.

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

### service

```yaml
parameters:
  app: { default: default, description: the project service the run brings up }
main:
  …
  - id: up
    type: service
    service: $app        # a project service: a name, or a $parameter naming one
    do: start            # start (default) | restart | stop
    timeout: 5m          # optional: how long it may take to be healthy (default: the service's own)
    on_fail: { to: fix_app, max: 2 }   # never healthy: the log's tail is edge.failure
  - id: shoot
    type: agent
    harness: $harness
    model: $model
    agent: shooter
    input:
      app: { from: run.service.$app, type: link }   # its address while it runs
    output:
      shots: { to: artifacts/shots/, type: files }
```

A **service** is the project's, not the flow's — like a command, it is the
project that knows how its app starts — declared in Workshop → Settings →
Services: a `start` script run in the primary checkout, an optional `stop`, a
health probe (an HTTP URL answering 2xx/3xx, a TCP port accepting, or a
command exiting 0) with how long a start may take and how often it is probed
after, and the ports it listens on. A start that keeps running *is* the
service; one that exits 0 started it in the background (`docker compose up
-d`, `make dev-up`) and its `stop` brings it down. A flow names a service,
usually through a parameter, so one flow serves projects that call their app
anything — the convention is `default`, and a project whose app is `aio`
binds `app: aio`.

`start` succeeds once the service is healthy — immediately when it already is
— and fails when its start script exits non-zero (`on_fail`) or it is not
healthy within `timeout` (`on_timeout`, else `on_fail`); either way
`edge.failure` holds why and the tail of its output, for an agent that can fix
it. `restart` stops it first (a re-entry after a crash waits instead of
restarting again). `stop` succeeds once it is down; stopping a service that is
not running does nothing. While it starts it is probed every second; once
healthy, at its interval, and three failures in a row make it unhealthy (one
success, healthy again) — a flip the run page shows, never an event. Without
a probe a service is up once its start has run a second without failing.

A service a stage started is stopped when its chain ends — every one, when the
run does; one a person started from the run page stays up until a person stops
it or the workspace goes (a stage that restarts it makes it the stage's). The
timeline records each start, readiness, stop and failure (`service_started`,
`service_ready`, `service_stopped`, `service_failed`) and who did it. Its ports get preview links on the run page either way, and
`run.service.<name>` (§4) is its address — `http://localhost:<port>` — for an
agent, and the preview link for a gate. Run creation refuses a flow naming a
service the project does not declare.

### run_if

```yaml
- { id: plan_gate,   type: human_gate, run_if: $plan_gate, … }       # a bool parameter
- { id: in_progress, type: action, action: ticket_transition, run_if: run.ticket.tracker, … }
- { id: sync,        type: action, action: sync_target, run_if: run.subject, … }
- { id: note,        type: agent, harness: $harness, model: $model, agent: writer, run_if: not run.ticket.tracker, … }
- { id: repro,       type: agent, harness: $harness, model: $model, agent: reproducer, run_if: run.ticket.change_type == fix, … }
- { id: plan,        type: agent, harness: $harness, model: $model, agent: planner, run_if: run.ticket.change_type != chore, … }
```

`run_if:` states one condition, naming a run fact by its path like a
source (§4):

| condition | holds when |
|---|---|
| `run.ticket.tracker` | the run's ticket has a tracker issue |
| `run.subject` | this chain run acts on a merge request — its *subject* (§3) |
| `run.ticket.change_type == fix` | the ticket is that kind of change — `feat`, `fix`, `hotfix`, `perf`, `docs` or `chore`, set on the ticket or read from an imported issue's type through the integration's type map; `!=` for the opposite |
| `$name` | a bool parameter is true |

Each but the comparison negates as `not x`; only `run.ticket.change_type`
compares, and only with `==` or `!=`. When it does not hold, the stage completes as
**skipped** (`stage_completed{skipped, note}` on the timeline) and `on_success`
follows, so the chain reads the same whether or not the project has a tracker
or the knob is on. The validator counts what a conditional stage produces as
produced only on some routes, and a skipped agent stage never silences the
model check a later one needs. Tracker actions skip without a tracker on
their own; `run_if: run.ticket.tracker` says it in the graph.

### Timeouts

One field, `timeout`, whose meaning follows the stage type:

| type | what `timeout` bounds | absent means |
|---|---|---|
| `agent` | execution wall clock, suspended while an ask_human ping is open (§9) | `parameters.timeout` (30m) |
| `command` | execution wall clock | `parameters.timeout` |
| `wait` | how long the chain stays parked on the signal | parked indefinitely |
| `service` | how long a start or restart may take to be healthy | the service's health timeout (5m) |
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
| `on_fail` | the stage failed | agent, command, action, wait | `edge.failure` |
| `on_reject` | a verdict said REJECT, or a human rejected a gate | agent, human_gate | `edge.verdict` |
| `on_success` | the stage succeeded | every stage | nothing — but a gate's approval, or a verdict's APPROVE, that said something carries `edge.verdict` |
| `on_timeout` | the stage exceeded its timeout | agent, command, wait | `edge.failure` |

A timeout uses `on_timeout` when the stage declares one, else `on_fail`.

**An edge does not declare what it carries.** Its kind already decides that, so
saying it again could only disagree. What it carries is available to its target
as an edge source, which the target reads through a port like any other input
(§4) — or ignores. That is the single declaration: *where it goes* lives with the stage that
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

Every value a stage reads or writes is a **port**, stated on the stage: its
name, where it comes from (`from:`) or goes (`to:`), and what it is. A stage
reads like a function's signature — `commit_msg(ticket: ticket, changes:
changeset, plan: file) -> message: commit_message` — and nothing it uses is
left implicit.

Control and data are separate. **Edges carry control** — which stage runs
next — and the payload of the transition: the failure or the decision that
sent the run there. **Ports carry data**, and a source never names a stage:
an input reads a path, and whatever stage last wrote that path is what it
gets. Two stages pass their own values to a third with two paths; one path
written by several stages (a plan, then a replan) gives its readers the most
recent.

### Ports

```yaml
- id: commit_msg
  type: agent
  harness: $harness
  model: $model
  agent: committer
  input:
    ticket:  { from: run.ticket, type: ticket }
    changes: { from: run.git.status, type: changeset }
    plan:    { from: artifacts/plan.md, type: file }
    failure: { from: edge.failure, type: failure, optional: true }
  output:
    message: { to: artifacts/commit.json, type: commit_message }
```

An input is `{from, type | schema, description?, optional?}`, an output
`{to, type | schema, description?}`. A port is always this mapping — a bare
value (`plan: artifacts/plan.md`) is a parse error that spells the fix. An
action's literal choices are not ports: they are its **settings**, under
`with:` (§2 action).

What the names are checked against follows from the stage type:

| type | input ports | output ports |
|---|---|---|
| `agent` | the agent's declared inputs: every required one stated, none it does not declare | every output the agent declares (it writes them all; the stage says where) |
| `action` | the action's file inputs, typed as the registry says | every file the action writes |
| `command` | free labels, files only | free labels |
| `human_gate` | one: a file to review, or a link | — |

### Sources

| source | is | type |
|---|---|---|
| `artifacts/plan.md` | a path some stage of the flow writes (`to:`); nothing writing it is a parse error | the writer's |
| `repo:docs/spec.md` | a file of the repository nothing in the flow writes | any file type |
| `run.ticket` | the run's ticket — title, change type, goal, the person's direction for this attempt, the files attached to it; an agent reading it gets the `# TASK` section | `ticket` |
| `run.git.diff_target` | `git diff --name-status <target>...HEAD` — what the branch changes against **its target**, the branch it forked from and its merge request merges into: the changed-file list, never the full diff. On a multi-repo run one section per checkout, on a subject the subject's only | `changeset` |
| `run.git.diff_phase` | `git diff --name-status <chain start>...HEAD` — what **this chain run** committed since the head it began on: a review round's fixes, not the whole branch. A chain run that began on no recorded head counts from the target | `changeset` |
| `run.git.status` | `git status --short` — staged, unstaged and untracked files, what a commit would record right now (the diffs compare commits, so they cannot see this); scoped per checkout like the diffs | `changeset` |
| `run.mr.url` | the run's merge request link (create_mr's); the first when a multi-repo run opened several — a gate then lists them all; on a subject, the subject's | `link` |
| `run.workspace` | the workspace's app link — the first port it exposed at provision (a devcontainer's `forwardPorts`), nothing when it declared none; the `debug` leaf hands it out by itself. Getting into the workspace is the run page's business: its terminal, its editor, the SSH door | `link` |
| `run.service.<name>` | a project service's address while it runs: `http://localhost:<port>` for an agent (its first port), its preview link for a gate; not running, the agent is told so. The name may be a parameter: `run.service.$app` | `link` |
| `run.ci.failure` | what failed in CI on the merge request a `ci_failed` watcher opened on — each failed check with the CI file that declares it, its job (and GitLab stage), its failed steps (GitHub Actions) and its link; a check another app reports, its name and summary. Never a log: the agent finds the job's script and runs it. Read once as the chain starts and kept on its `phase_started`. Only a `ci_failed` watcher's stages read it | `failure` |
| `edge.failure` | what the failing stage printed — the last 8 KB, every secret the engine injected replaced by `[redacted:<kind>]` | `failure` |
| `edge.verdict` | the last verdict on the stage before: an agent's, or a person's review at a gate — the summary and the comments, file by file. A rejection always carries one; an approval when it said something | `verdict` |

The engine's sources are typed, and the port states that type. Who may read
what is enforced: an agent reads any source; a gate shows files and a link; a
command and an action read files only — a script has nothing to read a value
the engine provides from. A whole-value `$name` means a parameter (§1), a
`${secrets.<name>}` inside an MCP server's `env` or `headers` a project
secret, and `$` means nothing else.

**`edge.failure` and `edge.verdict` are the retry channel.** They hold what
the edge that entered the stage carried — on_fail and on_timeout a failure,
on_reject a verdict, and the on_success of a gate or of an agent with a verdict
the verdict of an approval that said something ("approved — keep step two
small") — and are empty when the stage was entered any other way, so a port
reading one is `optional`, and something that carries it must enter the stage (a
port nothing can fill is a parse error). A failure keeps its last 8 KB; a
verdict its first 32 KB, since what was decided and the first comments matter
most. An agent that reads both can tell "the reviewer turned you down" apart
from "the tests broke":

```yaml
- id: build
  type: agent
  harness: $harness
  model: $model
  agent: builder
  input:
    plan:      { from: artifacts/plan.md, type: file }
    rejection: { from: edge.verdict, type: verdict, optional: true }   # reviewer comments, a person's feedback
    failure:   { from: edge.failure, type: failure, optional: true }     # test output, a command's error
```

Why two sources rather than one `edge.feedback`? Because the prompt then says
which setback this was (in the built-in `builder` flow `build` is the target of
both `test.on_fail` and `review.on_reject`), the agent can be instructed
differently per input ("a rejection: address every point first; a failure:
reproduce it first"), and the edge kind alone decides which fills — no edge
says what it carries, and the validator can say *"no on_reject edge enters this
stage, nor the success of a gate or a verdict, so edge.verdict is never
filled"*.

### What is checked

When the flow is saved — every one an error that refuses the document:

1. **The contract.** An agent stage states every output its agent declares and
   every required input, and no port the agent lacks; an action stage likewise
   against the registry. A port's shape **satisfies** the declaration's: the
   same type (any file satisfies `file`), the same schema — and on an output, a
   schema may be added to narrow it, never the type changed.
2. **The source.** A path is written by some stage (`repo:` says on purpose
   that nothing does — and a path a stage writes cannot be read as `repo:`); an
   engine source is one of the list (a typo suggests the nearest: *did you mean
   run.ticket?*).
3. **The shape agrees.** Every stage writing a path states the same shape, and
   that shape satisfies every reader's. A stage cannot write one path twice.
4. **Written before it is read.** On every route through the edges that reaches
   a reader, a writer of its path runs first — what another chain wrote counts
   as written. An `optional` input is exempt: its stage does without it.

When the run executes, before any stage — agent, command, action — every file
it reads must be there (`input_missing`, unless the port is optional) and hold
its type or schema (`input_invalid`); after an agent or a command, every file
it writes must be there (`output_contract`) and hold its shape
(`output_invalid`, the violations handed to the retry as `edge.failure`) — its
schema first, then what only the round knows (a review's replies name loaded
threads; its notes repeat neither an open thread of the factory's nor a note a
person dropped; a round's replies answer every thread).
**A stage has one session.** A new agent stage opens a session of its own, so
two stages never share context, even with the same agent. An attempt sent back
to a stage (a rejection, a failure, a later round of a watcher) continues that
stage's session, so the agent remembers what it did, and its prompt says why it
is back. A session the harness can no longer load (the workspace was rebuilt)
costs the agent that memory, not the stage: the attempt opens a new one.

Before every fresh attempt the engine deletes the stage's outputs, so the check
after it cannot be satisfied by a file an earlier attempt left behind. (Crash
recovery re-entering a live stage does *not* delete: a resumed harness session
is mid-work and its partial outputs must survive. Nor does an action whose
effect is still pending check its inputs again: it reconciles first.)

### Agent contracts

An agent declares what it reads and writes in the flow's `agents:` block (§1),
each typed — **never where**:

```yaml
agents:
  - name: committer
    inputs:
      - { name: ticket,  type: ticket }
      - { name: changes, type: changeset, description: "the uncommitted files" }
      - { name: plan,    type: file, optional: true }
      - { name: failure, type: failure, optional: true }
    outputs:
      - { name: message, type: commit_message }
```

A stage running it states each of those again as a port, with its source or
destination, and the parser holds the two together — so a stage reads on its
own, and the agent stays reusable: `builder` reads `plan` from
`artifacts/plan.md` in the main chain and `comments` from
`artifacts/threads.yaml` in a watcher. An output with a `path:` is refused: the
path is the stage's to say.

The prompt is assembled, never templated, from the stage's ports: each input
renders by its type — a `ticket` as the `# TASK` section, a file as `# INPUT:
<name> — <description> (file: <path>)` and its content, a changeset as its
file list, a failure or a verdict as what the edge carried — and the outputs
as the `# OUTPUT CONTRACT`, each path with its shape. An agent sees the ticket
only through a ticket port.

### Types

| type | is | read by |
|---|---|---|
| `file` | any file: only its existence is checked. Every file type satisfies it — but `files` | anything |
| `files` | a directory of files — screenshots, a report's pages — its path ending in `/` (`artifacts/shots/`): written, it must hold at least one file. Never a schema | `create_mr`, `ticket_comment` (`attachments`), a `human_gate` (each file reviewed) |
| `verdict` | `{"verdict":"APPROVE"\|"REJECT","summary"?,"comments":[…]}` — each comment a remark or `{path, line?, end_line?, quote?, body}` (below); a REJECT says why | the router: REJECT follows the stage's `on_reject`; an APPROVE with comments carries them on `on_success` |
| `review_threads` | a review of the merge request (below): a summary, notes that open threads, replies in threads already there | `post_review`, a curating `human_gate` |
| `review_replies` | a review round's answers (below): a list of `{thread, did, reply, resolve, rationale, evidence}` | `reply_review_comments`, a curating `human_gate` |
| `mr_threads` | what is on the merge request, as `load_review_comments` read it (below): every thread with its comments, and the notes a person dropped at a gate | the reviewing and fixing agents, the curation page, the engine's own guards |
| `commit_message` | `{"subject": "...", "body": "..."}` — a one-line subject (at most 100 characters), an optional body | `commit` |
| `ticket`, `changeset`, `failure`, `link` | values the engine provides — and `verdict`, from `edge.verdict` | inputs only |

**Why these, and nothing else.** A file type exists for a structure something
*built in* reads — the router, an action — and the action's port names it, so
binding `post_review`'s `review` to a file written as a plain `file` is a parse
error, not a run that fails at posting time. A structure only the flow's own
stages pass around is the author's to define: a schema.

A **verdict** is what makes `on_reject` reachable: a stage whose ports write
none cannot carry the edge (a parse error), and an agent declares at most one.
Every structured type's shape is a JSON Schema the engine holds, shown verbatim
in the agent's OUTPUT CONTRACT and checked like a flow schema, first; what a
schema cannot say, the engine checks after it. A file that exists but does not
hold a type's shape fails the stage with `output_invalid` — a different fault
from never writing it. A **review_threads** file is

```yaml
summary: |                       # optional, markdown — POSTED as the review's body
  Reads well; two things before it merges.
notes:                           # new threads to open, at most 50 notes and replies in all
  - path: internal/flow/parse.go # repository-relative
    line: 412                    # in the NEW version of the file
    kind: blocking               # blocking | suggestion | question | nit — the posted text's leading tag
    body: This error loses the line number the caller formats into its message.   # POSTED
    rationale: parse_test.go:88 asserts the position; without it the editor cannot jump there.   # never posted
    evidence: "ran go test ./internal/flow/: TestParseErrors fails"                          # never posted, optional
    example: 'return fmt.Errorf("%d: %w", n.Line, err)'                                       # never posted, optional
replies:                         # in threads already on the merge request (ids from the mr_threads input)
  - { thread: PRRT_kwDOabc, body: "Addressed in 3f2a1c, thanks.", resolve: true }
```

checked, past its schema, against the round: a reply names a thread the phase
run's `load_review_comments` loaded, once, and resolves only one the factory
opened; a note does not sit on the place of an open thread of the factory's, nor
say what one already says on the same file (the words come from the `mr_threads`
input), nor repeat a note a person dropped at a gate in this run — each refusal
says what to write instead, and the retry reads it. Whether a line is in the
change is the repository's business at posting time, not a failure. A
**review_replies** file is

```yaml
- thread: d1                     # a thread the round loaded, verbatim
  did: fixed                     # fixed | declined | answered
  reply: Renamed fetchAll to listWidgets in api/widgets.go and its two callers.   # POSTED
  rationale: the old name promised a full fetch the function never did           # never posted; required with declined
  evidence: "commit 3f2a1c; api/widgets_test.go still passes"                     # never posted, optional
- { thread: d2, did: declined, reply: "The test covers it already.", rationale: see widgets_test.go }
```

checked against the threads the round loaded: every one answered exactly once —
a comment judged wrong gets `did: declined` and why, never silence — and a
declined thread is not resolved (the reviewer closes a disagreement). An
**mr_threads** file is what `load_review_comments` writes and nothing else
produces: `{merge_request, threads: [{id, path, line, resolved, ours, kind, hunk,
comments: [{id, author, at, ours, new, did, body}]}], dropped: [notes]}` — `ours`
a thread the factory opened, `new` a comment after the factory's last word in
its thread, `did` what an earlier reply of the factory's said it did, `dropped`
the notes a person declined to post at a gate earlier in the run.

A **verdict** has one shape whoever gives it. An agent writes it as a file —
JSON or YAML, as its path says; the templates bind `artifacts/verdict.yaml`:

```json
{ "verdict": "REJECT",
  "summary": "Two problems.",
  "comments": [ "the change has no test",
                { "path": "internal/flow/parse.go", "line": 412, "body": "this error loses the line number" },
                { "path": "docs/a.md", "body": "out of date" } ] }
```

— a comment is a remark on the whole change, or one on a file: on a line or a
range (`line`, `end_line`, with the `quote` it is about) or the whole file. A
REJECT says why (a summary, comments, or both); a path stays inside the
repository. The type's JSON Schema is shown in the agent's OUTPUT CONTRACT and
checked like a flow schema. A person gives the same at a gate: a summary, and
comments each on one file the gate showed — on its lines (quoting the
selection, naming the markdown section) or on the whole file — with the files
they attach. Read from `edge.verdict`, the next agent gets it written out
file by file, numbered, with the files attached to each comment:

```text
REJECTED at gate look_gate.

Close, two things.

2 comments, on 2 files:

artifacts/plan.md
1. [§ Steps, lines 3–4]
   > Step one.
   say what each step changes
   attached: .apothecary/attachments/ping-…/step.txt

artifacts/shots/home.png
2. [the whole image]
   the header is cut off
```

### Schemas: the flow's own structures

A flow declares its structures once, under `schemas:`, as JSON Schemas
(draft 2020-12) written in YAML, and a port — or an agent's declaration — names
one:

```yaml
schemas:
  - name: coverage
    description: line coverage of the change
    json_schema:
      type: object
      properties:
        percent: { type: number, minimum: 0, maximum: 100 }
      required: [percent]
main:
  - id: measure
    type: command
    run: ./scripts/coverage.sh > artifacts/coverage.json
    output:
      report: { to: artifacts/coverage.json, schema: coverage }
  - id: gate
    type: command
    run: apothecary-shim jq -e '.percent >= 80' artifacts/coverage.json
    input:
      report: { from: artifacts/coverage.json, schema: coverage }
```

- **A file with a schema** is JSON (`.json`) or YAML (`.yaml`, `.yml`) — the
  extension decides how it is read, and any other is a parse error. Writer and
  readers agree on it like on any shape, whoever they are.
- **Checked like a type,** after the existence check: a file that does not
  satisfy it fails its stage, every violation named at its place (`at /percent:
  maximum: got 140, want 100`) — and an agent writing it is shown the schema in
  its OUTPUT CONTRACT.
- **Beside a type a schema narrows it:** the file holds both, and the agent is
  shown both — a commit message that must follow Conventional Commits is
  `{type: commit_message, schema: conventional_commit}`. Beside `file` it is
  refused: the schema alone says what the file is.
- **Nothing is loaded.** A `$ref` resolves inside the schema only; one naming a
  file or a URL is a parse error, so a flow cannot make the engine read either.
  A schema JSON Schema itself refuses is a parse error too, and one nothing
  names is a warning.

### What the editor draws

Because consumption and production are declared, the graph editor derives the
dataflow rather than storing it: each stage shows an input dot per thing it
reads and an output dot per thing it writes, and a dashed line connects a
producer's dot to a consumer's dot in the same chain whenever their paths
match (a watcher reading what the main chain wrote shows the path on its dot).
Those lines are read-only by construction — you rewire them by editing a path,
not by dragging. Control-flow edges (the colored ones) are the draggable
layer, and one of them lands directly on the `edge.verdict` or `edge.failure`
dot that receives its content — so the retry path is visible too. Every chain is
drawn the same way, one row each with its opening — the main chain's start, a
watcher's or an outcome's wait point — as a marker on its first stage, under
its section's label (main, watchers, outcomes); the stages that park are
marked too. Once a flow has watchers or outcomes, the main chain's `end` is
the **hub** — the run open, waiting on its merge requests — and each watcher
and outcome branches from it, framed as the sub-run it is, with its own ends:
`end` back to the hub's waiting, `done` or `abort` for an outcome.

---

## §5 Failure

A stage failure records a reason, then follows an edge:

| reason | meaning |
|---|---|
| `exit_nonzero` | a command exited non-zero |
| `timeout` | the stage exceeded its timeout |
| `harness_error` | the harness itself failed |
| `input_missing` | a file the stage reads was not there (a port not `optional`), or a subject action ran with no merge request to act on — upstream wiring, not this stage's work |
| `input_invalid` | a value the flow wrote means nothing where it lands — a status the tracker cannot reach, a field it has no name for or cannot write from text; a file the stage reads that does not hold its port's type or schema; or a parked wait's signal can no longer hold. Wiring again, but the input is there |
| `output_contract` | the stage finished without writing what it declared (its outputs are cleared before each fresh attempt, so a stale file cannot satisfy it) |
| `output_invalid` | a declared output exists but does not hold what its type or schema promises — a malformed verdict, a report that does not satisfy its schema; the violations are the retry's `edge.failure` |
| `infra` | provisioning, repository or other infrastructure failure |

A stage that **skips** — its `run_if` does not hold, a tracker action with no
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
`stage_failed` / `edge.failure` are a *preview* — the whole log is one link away.
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
named with its v5 spelling); `run_start:` (now `operates_on:`); an `operates_on`
other than `ticket` or `merge_request`; no `main` in a flow whose runs are started on a ticket; a
flow with neither `main` nor a watcher; a watcher or an outcome whose `id` is
missing, not a slug, `main`, or another's (watchers and outcomes are one
namespace); a watcher with neither `run` nor `stages`, or both; `then` on a
watcher; an outcome with neither a chain nor `then`, or a `then` other than
`done` / `abort`; `manual` on an outcome, or a signal the registry does not
know (`run_start` among them: a run's start is no signal); a `wait` on, or a
gate auto-answered by, a signal the engine does not observe (`manual`); `start` on
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
`run:` script), an enum parameter wider than its site; `run_if` naming anything
but `run.ticket.tracker`, `run.subject`, `run.ticket.change_type` compared
with a change type, or a bool parameter. And every **contract**
violation: an agent a stage names but the flow does not declare; a port the
agent or the action does not declare, a required input or any output of the
agent's or the action's left unstated, a port whose shape does not satisfy its
declaration's; `on_reject` on a stage that writes no verdict. And every
**port**: a bare value, an unknown key, an input with `to:` or an output with
`from:`, no type and no schema, an unknown type, `file` beside a schema, a
value type on a file or on an output, an engine source with another type, an
edge source not `optional` or that no edge of its kind enters, a command or an
action reading `run.*` or `edge.*`, a gate shown anything but a file or a
link, a destination outside the repository or under `repo:`, one stage writing
a path twice; a path nothing in the flow writes (unless `repo:`) or a `repo:`
path a stage writes; writers of one path stating different shapes, or a shape
that does not satisfy a reader's; a non-optional input not written on every
route that reaches it. And for structures: a schema whose name is not a slug
or is declared twice, with no `json_schema` or one JSON Schema refuses, or a
`$ref` outside it; a port or a declaration naming a schema the flow does not
declare; a file with a schema or a structured type whose path is not `.json`,
`.yaml` or `.yml`. And for actions: a setting given as a port or a port under
`with:`, an unknown setting, an enum setting outside its values, a required
setting left out. A flow cannot be saved with any of these.

**Resolve-time errors** (the document with values) — the same rules, re-run
over the pinned bytes with a project's binding or a run's overrides laid in:
a `run_ref` bound to something that is no command slug, a model without a
provider, a value outside its enum or of the wrong type, a name
the flow does not declare (dropped with a warning in a project binding;
refused in a run override). Refused where the value is bound — the workshop's
Configure sheet, run creation — with the parameter named.

**Warnings** — advisory, returned alongside a successful parse by
`POST /api/flows/validate` and never blocking anything. These are legal
constructs that look like mistakes: a `wait` on a signal — or a
`ticket_link`, or a port reading `run.mr.url` — whose prerequisite merge
request isn't opened on every route reaching it, a parameter nothing reads, a subject action in a chain that may
have no subject, `each_repo` on a subject, a watcher or an outcome on a
merge-request signal in a flow that opens no merge request (and is not
started on one), a stage of a watcher opened by a signal that ends the whole
run (`done` or `abort` — almost always a slip for `end`), and a main chain
that opens a merge request and says `done` without waiting for it while
watchers or outcomes watch it; a sequence nothing runs, whose stages
nothing has checked; and a schema no input or output names.

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
  segment, no leading `$` (that space belongs to parameters); a destination
  never `repo:`.
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
| a phase `on: mr_opened` (v4) | `operates_on: merge_request`, the chain under `main:` |
| `run_start:` (v5, before its rename) | `operates_on:` — the same values |
| a reaction phase, on a firing signal or `manual` (v4) | a watcher, under `watchers:` |
| an outcome phase, on `mr_merged`, `mr_closed` or `mr_approved` (v4) | an outcome, under `outcomes:` — on any observed signal |
| a `phase` on a start-run, rerun or automation request (v4) | nothing: a flow has one main chain, and its `operates_on` says what an automation of each kind may start |
| a person beginning the entry phase again from the run page (v4) | the main chain runs once; a rerun is a new run |
| `defaults:` and `budget:` blocks (v3) | one `parameters:` block (§1): the same six names, plus the flow's own knobs |
| the entry phase `on: manual` (or `on: ticket`), the first by position (v3) | the `main` chain |
| `on: { signal: mr_conflicts, confirm: true }` (v3) | `on: { signal: mr_conflicts, start: confirm }` — `auto` / `confirm` / `manual`, or `$param` |
| a reaction `on: { signal: manual }` (v3) | a watcher `on: manual` |
| the run ending by itself from its merge requests (v3) | an **outcome**: `on: mr_merged` ending with `done`, `on: mr_closed` with `then: abort` (§1 *How a run ends*) |
| `$CHANGED` (the branch against its target in an entry phase, *this round's* changes in a subject phase) | `run.git.diff_target` or `run.git.diff_phase` — one range each, wherever read |
| `$UNCOMMITTED` | `run.git.status` |
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
| `payload:` on a gate | `input: { name: { from: …, type: … } }` |
| `feed: output` / `feed: decision` on an edge | a port on the target reading `edge.failure` / `edge.verdict` |
| `when:` on a stage | `run_if:` — the message names it |
| `edge.decision`, `type: decision` | `edge.verdict`, `type: verdict` |
| `mr_url` / `workspace` | `run.mr.url` / `run.workspace` |
| a bare binding, `plan: artifacts/plan.md` (v5 until ports) | a port: `plan: { from: artifacts/plan.md, type: file }` |
| the sentinels `$DIFF_TARGET`, `$DIFF_PHASE`, `$STATUS`, `$MR_URL`, `$WORKSPACE`, `$DECISION`, `$FAILURE` | the sources `run.git.diff_target`, `run.git.diff_phase`, `run.git.status`, `run.mr.url`, `run.workspace`, `edge.verdict`, `edge.failure` — the message names the one to use |
| `path:` on an agent's output | `output: { name: { to: <path>, type: … } }` on every stage running it |
| an action's literal under `input:` (`to: done`) | `with: { to: done }` |
| the implicit `# TASK` in every agent prompt | a `ticket` port reading `run.ticket` |

---

## §8 Annotated example

The built-in `builder` flow, abridged — plan-gated build with cross-vendor
adversarial review, the debug leaf as every stage's rescue, the ticket's
tracker issue following the work in canonical statuses, and the merge
request's life after it opens. Full text: `internal/flow/builtin/flows/builder.yaml`.

```yaml
name: builder
version: 5
operates_on: ticket                # the default: a run works on a ticket
# One settings block. A project binds any of these in the workshop without
# touching this document; a run may override.
parameters:
  harness: opencode                # model: none here — the project binds it, stages read $model
  timeout: 30m
  max_turns: 50
  max_cost_usd: 20.0
  max_total_turns: 600
  verify:          { default: verify, description: "the project command that runs the tests" }
  review_model:    { type: string, default: $model, description: "the reviewer's model — bind a second vendor's" }
  plan_gate:       { default: true, description: "stop for a human to approve the plan before building" }
  conflicts_start: { default: confirm, enum: [auto, confirm, manual] }
  review_start:    { default: auto,    enum: [auto, confirm, manual] }

# The agents this flow runs, abridged to two. Their contracts — what each
# reads and writes, typed, never where — are what the stages below state as
# ports, and what the parser checks those ports against.
agents:
  - name: planner
    description: explores the repository and writes the implementation plan
    read_only: true
    inputs:
      - { name: ticket,    type: ticket }
      - { name: rejection, type: verdict, optional: true, description: "why the last plan was rejected" }
      - { name: failure,   type: failure,  optional: true, description: "what the previous attempt printed" }
    outputs:
      - { name: plan, type: file, description: "the implementation plan" }
    prompt: |
      You are the planning agent of a software factory...
  - name: reviewer
    description: adversarial code reviewer
    read_only: true
    allow_bash: true
    inputs:
      - { name: plan,    type: file,      description: "the approved plan to check the change set against" }
      - { name: changes, type: changeset, description: "the set of changed files to review" }
      - { name: failure, type: failure, optional: true, description: "what the previous attempt printed" }
    outputs:
      # A verdict is what makes review.on_reject below legal at all.
      - { name: verdict, type: verdict, description: "APPROVE or REJECT with actionable comments" }
    prompt: |
      You are the adversarial reviewer of a software factory...

# The main chain: it runs once, when the run is started on the ticket.
main:
  # The tracker issue moves first — before any workspace exists, since a
  # tracker action needs none. A canonical status: Jira says "In Progress",
  # GitHub Issues has no word for it and the stage skips; so does a ticket
  # written by hand. A tracker hiccup routes straight on to the plan.
  - { id: in_progress, type: action, action: ticket_transition, with: { to: in_progress },
      on_success: plan,
      on_fail: { to: plan, max: 1 } }

  # Every port stated: the ticket from the engine, both retry values from
  # whichever edge sends the run back — a human rejecting the plan, its own
  # failure — and where the plan goes. Two retries with the error fed back,
  # then the debug leaf.
  - id: plan
    type: agent
    harness: $harness
    model: $model
    agent: planner
    max_turns: 25
    input:
      ticket:    { from: run.ticket, type: ticket }
      rejection: { from: edge.verdict, type: verdict, optional: true }
      failure:   { from: edge.failure, type: failure, optional: true }
    output:
      plan:      { to: artifacts/plan.md, type: file }
    on_success: plan_gate
    on_fail: { to: plan, max: 2, then: debug }

  # A wait point: the chain parks while the human reads; a conflict fix
  # may run meanwhile. A project that trusts its planner binds
  # plan_gate=false and the stage completes as skipped.
  - { id: plan_gate, type: human_gate, run_if: $plan_gate,
      input: { plan: { from: artifacts/plan.md, type: file } },
      question: "Approve this implementation plan?",
      on_success: build,
      on_reject: { to: plan, max: -1 } }

  - id: build
    type: agent
    harness: $harness
    model: $model
    agent: builder
    max_turns: 80
    input:
      ticket:    { from: run.ticket, type: ticket }
      plan:      { from: artifacts/plan.md, type: file }
      rejection: { from: edge.verdict, type: verdict, optional: true }
      failure:   { from: edge.failure, type: failure, optional: true }
    on_success: commit_msg
    on_fail: { to: build, max: 2, then: debug }

  # commit_msg (abridged) writes artifacts/commit.json as a commit_message;
  # the commit action reads one, so a writer of anything else is a parse
  # error. Every checkout is committed; a clean one is left alone.
  - { id: commit, type: action, action: commit,
      input: { message: { from: artifacts/commit.json, type: commit_message } },
      on_success: test, on_fail: debug }

  # The project's test command, by the name the project binds.
  - { id: test, type: command, run_ref: $verify,
      on_success: review,
      on_fail: { to: build, max: 5, then: debug },
      on_timeout: { to: test, max: 1, then: { to: build, max: 2, then: debug } } }

  # A different vendor reviews. The reviewer's verdict output is what makes
  # on_reject reachable; its comments reach the builder's `rejection` input.
  # Three rejected rounds, then a human: no chain ends without a person.
  - id: review
    type: agent
    harness: $harness
    agent: reviewer
    model: $review_model
    max_turns: 30
    input:
      plan:    { from: artifacts/plan.md, type: file }
      changes: { from: run.git.diff_target, type: changeset }
      failure: { from: edge.failure, type: failure, optional: true }
    output:
      verdict: { to: artifacts/verdict.yaml, type: verdict }
    on_success: redact
    on_reject: { to: build, max: 3, then: debug }
    on_fail: { to: review, max: 1, then: debug }

  - { id: push, type: action, action: push_branch, on_success: mr, on_fail: debug }
  - { id: mr,   type: action, action: create_mr,
      input: { body: { from: artifacts/mr.md, type: file } },
      on_success: in_review }

  # The merge request is open: the issue moves to its in-review status.
  # Best effort again — a failure ends the chain like a success would.
  # Then the main chain ENDS: the run is in review, the watchers below keep
  # the merge request healthy, and the outcomes at the bottom end the run.
  - { id: in_review, type: action, action: ticket_transition, with: { to: in_review },
      on_fail: end }

watchers:
  # The target moved under the merge request. How it begins is the project's
  # call: by itself, after asking in the inbox, or only by hand. A round ends
  # with `end` — never `done`: the run goes back to waiting.
  - id: conflicts
    on: { signal: mr_conflicts, start: $conflicts_start }
    stages:
      - { id: sync,      type: action, action: sync_target, with: { strategy: merge },
          on_success: sync_test, on_fail: { to: resolve, max: 6, then: debug } }
      - { id: resolve,   type: agent, harness: $harness, model: $model, agent: resolver, max_turns: 30,
          input: { plan:     { from: artifacts/plan.md, type: file },
                   conflict: { from: edge.failure, type: failure, optional: true } },
          on_success: sync, on_fail: debug }
      - { id: sync_test, type: command, run_ref: $verify, on_success: sync_push, on_fail: debug }
      - { id: sync_push, type: action, action: push_branch, on_success: end, on_fail: debug }

  # Reviewers commented, then nothing for an hour. You pick which threads to
  # fix (inbox); they are fixed in one batch, tested, pushed, and answered —
  # every one, the replies read by you first ($reply_gate) — as many rounds as
  # the reviewers need.
  - id: review_fixes
    on: { signal: mr_review_settled, start: $review_start, quiet_for: 1h }
    stages:
      - { id: load, type: action, action: load_review_comments, with: { select: human },
          output: { threads: { to: artifacts/threads.yaml, type: mr_threads } } }
      - { id: fix,  type: agent, harness: $harness, model: $model, agent: builder, max_turns: 60,
          input: { ticket:   { from: run.ticket, type: ticket },
                   plan:     { from: artifacts/plan.md, type: file },
                   comments: { from: artifacts/threads.yaml, type: mr_threads },
                   failure:  { from: edge.failure, type: failure, optional: true } },
          on_success: fix_test, on_fail: { to: fix, max: 2, then: debug } }
      # … commit, test, push, reply, then:
      - { id: reply_gate, type: human_gate, run_if: $reply_gate, question: "Send these replies?",
          input: { replies: { from: artifacts/replies.yaml, type: review_replies },
                   threads: { from: artifacts/threads.yaml, type: mr_threads } },
          on_reject: { to: reply, max: -1 } }
      - { id: post, type: action, action: reply_review_comments,
          input: { replies: { from: artifacts/replies.yaml, type: review_replies } }, on_success: end }

# Outcomes: once, and the run is over. Merged, the ticket is done and so is
# the run; closed unmerged, the ticket goes back and the run is abandoned —
# someone decided against it, nobody is asked again.
outcomes:
  - id: merged
    on: mr_merged
    stages:
      - { id: ticket_done, type: action, action: ticket_transition, with: { to: done }, on_fail: done }
  - id: closed
    on: mr_closed
    then: abort
    stages:
      - { id: ticket_back, type: action, action: ticket_transition, with: { to: todo }, on_fail: abort }
```

## §9 Agent-side interactions (ask_human)

A stage's agent can stop and ask. This is not a stage type and needs no
grammar — it is a property of every agent stage, and it is worth stating
because it changes how a stage behaves in flight.

The agent calls `ask_human{type, question, context?, fields?,
artifacts?}` through an MCP tool in its workspace. A harness's own question
tool reaches the same place: the workspace sidecar advertises ACP form
elicitation, and an `elicitation/create` it receives is put to the human as
the same ping and answered with the human's form. The engine, not the
harness, owns the exchange:

- **The request is an event.** `ping_raised` records the type
  (`clarification` | `blocker` | `permission`), the question, any attached
  workspace files, the form, and the stage in flight; the answer is
  `ping_answered`. Both are durable, so an interrupted run re-arms
  its open questions on recovery and a late answer still lands.
- **The stage keeps running; its clock does not.** The agent process stays
  alive waiting for its tool call to return, and the stage's `timeout` is
  **suspended** for as long as any ping is open — a question a human takes an
  hour to answer cannot time the stage out. The run projects as
  `waiting_on_human`. Unlike a gate, an open question **holds the workspace**:
  the agent's turn is still open, so no other chain runs until it is answered.
- **A question is a form** — ACP's elicitation form, MCP elicitation's
  restricted JSON Schema, kept as an ordered list of `fields`. Each has an
  `id` the answer is keyed by and a `type`: `string` (free text, with
  `format` / `pattern` / `min_length` / `max_length`), `string` with
  `options` (pick one — the agent branches on the id), `array` with
  `options` (pick any, `min_items` / `max_items`), `number` / `integer`
  (`minimum` / `maximum`) or `boolean`; plus `title`, `description`,
  `required`, `default`. An option is a string, or `{id, label, detail}`.
  Asked without fields, the question is one free-text answer. The tool
  returns a lone field's value (`selected: id, id` / `none selected` for an
  array), several as one `id: value` line each.
- **The human may decline** instead of answering, with a note: the tool then
  returns `declined —` and their reason. "None of these fit" is a decline,
  never an option the agent declares.
- **Every ping declares the shape of its answer** — `ping_raised.answer_kind`:
  `yes_no` (approve | reject: a gate, a watcher's or outcome's
  confirmation, the debug leaf) or `form` (accept with `content`, the value
  of each field, or decline with a note: every question — an agent's, a
  harness's own, a review round's thread selection). The inbox renders that
  shape and the engine validates against it; nothing is inferred from a
  ping's type.
- **Secrets are never asked.** A field a harness marks secret is not put to
  the human (an answer is kept in the run's log); the agent is told to have
  it configured as a project secret.
- **Answers can carry files**, delivered into the workspace under
  `.apothecary/attachments/`, kept separate from the agent's own `artifacts/`.
- If the engine is unreachable for more than 10 minutes the tool returns an
  error string and the agent proceeds — a broken control plane stalls the work
  but never wedges it forever.

**A flow says *when* to ask, never *how*.** Every agent's role ends with a
standing `# ASKING THE HUMAN` section the harness renderer appends after the
prompt (the Ask pane's excepted — it answers, it does not ask): the name the
harness lists the tool under (opencode namespaces MCP tools by server, so
there it is `ask_human_ask_human`; where the renderer cannot know, that it may
be prefixed), the three types and when each applies, and that the call waits
for the human however long that takes. The tool's own description carries the
form and decline semantics. A prompt therefore states only its stage's
policy — "if the ticket is ambiguous in a way that changes the design, ask
the human (type=clarification) before writing the plan" — and nothing about
the tool. The flow editor's "what a stage adds" preview lists the section.

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
  signal is picked up by `wait` for free; a *firing* signal (the `Trigger` flag and a predicate in the idle
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
  run of a flow whose `operates_on` matches it — `ticket`, or `merge_request`
  for one watching merge requests — with its parameters, recorded as
  `phase_started{by: automation}`. The flow is untouched.
- **An action** — one `actionspec` registry entry, one executor case, one
  `requires` port string. Everything else (parsing, binding, the editor's
  picker, the dataflow view) follows from the signature.
- **A source** — one constant and its type in the engine's list, plus one
  resolution case (a prompt rendering, a gate link). The `run.` / `edge.`
  prefix already tells authors the engine provides it.
- **A parameter site** — one `lookup` call where a field is read, declaring
  which type it takes; the resolve path and the editor's `$name` pickers
  follow.
- **An output type** — like `verdict`: a value on an output's `type` plus the
  engine behaviour it implies — and only for a structure something built in
  reads (the router, an action, whose file input then names the type). A
  structure the flow's own stages pass around needs no change at all: it is a
  schema the author declares.
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
- **General value-passing or templating** — sources and parameters are the
  only engine-value channels, on purpose: a parameter is read at a typed site,
  never spliced into text.

### Why no AND/OR combinators

A general `all_of:` / `any_of:` looks like a natural addition to `wait`. It is
not, for three separate reasons:

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
  an `on_reject` edge and an `edge.verdict` only one branch could ever reach.

Nor is human approval itself modelled as a signal (`for: human_approve`): a gate
is not a condition but an interactive object — a question, a payload to review,
an attachment channel, a reject-with-reasons path, a place in someone's inbox.
Expressing it as a signal would force the `wait` stage to grow every one of
those fields, dissolving the type distinctions this grammar exists to keep.

The likely next need is not a boolean gate at all but multi-outcome routing
("which signal fired?"), which the edge-kind registry already accommodates.
