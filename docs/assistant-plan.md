# Plan: the assistant, and the engine's MCP face

Status: proposed (2026-11-19). Nothing built. TODO item 49.
Owner: the agent implementing it. Read `AGENTS.md` first; every rule there
applies (conventional commits, no model ids, `task fmt`, `task check`, new
numbered migrations only, a note in `docs/notes/` with each deviation).

Items marked **verify** were not confirmed against the code or a running
cluster when this plan was written. Phase 2 opens by settling the ones
the design rests on; the others are settled in the phase that names them.

## Why

- The platform has agents inside runs and none beside the person. Writing a
  ticket, a flow or a skill is done by hand, on pages that know everything
  an agent would need to help.
- People have already registered keys and subscriptions (provider
  credentials behind harness integrations). Nothing outside a run can use
  them.
- An agent outside the platform cannot drive it at all. The permission
  system was built with scoped tokens for exactly that
  (`docs/notes/2026-11-18-permissions-cedar-roles-sharing-and-scoped-tokens.md`,
  "MCP tokens come next").

## What it is

On the flow, ticket and skill pages a panel opens a conversation with an
agent. The agent runs in the person's own harness, on their own credential,
in a small pod of their own. It can read the repositories its page concerns
and their git history, and it can read the platform through the engine's
MCP face. Its one way to act is to **fill the page's inputs**: the engine
checks what it submits, and the page writes it into the existing form or
editor. Nothing is saved until the person saves.

The MCP face is built first and stands on its own: any MCP client with a
scoped token can read the platform through it. Write tools for outside
agents are the next item, not this one.

## Decisions

1. **A harness, never a direct model call.** A subscription login exists
   only as a harness's own credential (`docs/model-providers-plan.md`,
   faces). The assistant uses the **caller's** harness integration, chosen
   once in their settings; the model is picked in the panel. No model id
   enters the tree.
2. **One light pod per person.** Created when the panel opens, stopped
   when idle. Plain runtime, no sysbox: its own user namespace
   (`hostUsers: false`), non-root, every capability dropped, no privilege
   escalation, default seccomp, read-only root, no service account token.
   One exception: the container that brings the tunnel up holds
   `NET_ADMIN`, as a sandbox pod's does.
3. **A volume per person** holds the clones and the harness's session
   files, so a restart costs a fetch and a conversation resumes.
4. **A thin image built `FROM` the tools image**, adding git, ripgrep and
   the shim. Its layers are already on every node that runs sandboxes.
5. **Two containers.** `agent` mounts the repositories read-only and never
   sees a repository credential. `git` mounts them read-write and holds
   the credential in memory for the length of a fetch.
6. **Real clones, full history of the default branch.** Refresh is `fetch`
   then `reset --hard` to the remote head, every repository of a workshop
   in parallel: when the pod wakes, and on a Refresh button.
7. **No shell.** The agent has its harness's own read and search tools,
   and one `git` tool on the shim that passes arguments through to git.
8. **One confined process per session.** The shim launches each session's
   agent process under a Landlock rule set built for that session: read
   its repositories, write its own state folder, execute a short list.
   No Landlock on the node means no session, never an unconfined one.
9. **A gateway per person.** Standing allow list: the model provider
   domains of the credentials behind the assistant harness. A repository
   host is reachable only during a fetch.
10. **The output is a fill.** Checked by the engine, written into the
    page's inputs, never saved by the assistant. The page keeps what was
    there (undo), marks what changed, and does not overwrite edits made
    since the question was asked without a click.
11. **A session belongs to what it fills**: a kind (flow, ticket, skill)
    and a subject (that flow, ticket or skill; a draft id until the first
    save). Its tool, prompt and repositories all follow from that.
12. **Repositories per session.** A flow's or a ticket's session gets its
    workshop's repositories. A skill's gets none unless the person
    attaches a workshop. The mechanism takes any list.
13. **The platform is read through the MCP face**, under a token that
    holds read actions only. The face's tools are derived from the route
    table, so they have the one source the OpenAPI document has.
14. **Codex is not offered as the assistant harness** in the first
    version: it reads files through its shell tool (**verify**, phase 2).
    omp serves the ChatGPT login, so no subscription is left out.
15. Unreleased: no compatibility layer.

## Shape

```
browser ── panel ──► engine ──────────────► assistant pod (one per person)
                      │  conversation        ├─ agent container
                      │  service, pages      │    shim ─► confined process
                      │                      │             per session
                      │  /mcp  ◄─────────────┤    repositories: read-only
                      │  (read tools, fill)  ├─ git container
                      │                      │    repositories: read-write
                      │  keeper ── tokens ──►│    credential: fetch only
                      │                      └─ tunnel ─► gateway (per person)
                      └─ any MCP client with a scoped token
```

### The pod

- Name `apo-assistant-<person key>`, in the workspaces namespace, with the
  network policy of a sandbox pod: the tunnel and the engine, nothing else.
- Volume `apo-assistant-<person key>`, the workspace storage class, its own
  size value. Two sub-paths: `repos` at `/workshops` (read-only in `agent`,
  read-write in `git`) and `state` at `/state` (`agent` only). With the
  chart's `emptydir` storage the clones are redone at each start.
- Layout: `/workshops/<workshop slug>-<id tail>/<repository>`. A session's
  working directory is its workshop's folder; its home is
  `/state/<session id>`.
- Lifecycle follows the project browser's manager
  (`internal/adapters/browser/manager.go`): created on first need, reused,
  recreated on a stale image when nobody uses it, stopped after an idle
  time. The volume outlives the pod and is deleted after an idle number of
  days. Both times are instance settings.
- Model access tokens are removed when the pod is stopped, as at a run's
  end. No refresh token is ever in it.

### A session

- The shim's `confine` launcher applies Landlock, then executes the
  harness's agent command:
  - read and execute: the image's system folders and the harness;
  - read: the session's repositories;
  - read and write: `/state/<session id>` and a temp folder;
  - execute, beyond the harness: git, ripgrep, the shim. No shell.
- The harness profile for these sessions turns the shell and write tools
  off, so the agent is not offered tools that would fail. The sidecar
  permits read, search and think tool kinds and the two MCP servers below,
  and refuses the rest, web fetches included.
- Two MCP servers ride the session: the shim's (the `git` tool) and the
  engine's face (read tools and `fill`).
- Changing a session's repositories relaunches its process under new
  rules and loads the same harness session. Landlock only tightens.
- A process with no turn for a few minutes exits; the next message starts
  it again.

### The git tool

`git(repo, args)` on the shim's MCP server. The shim executes git itself,
with no shell: `args[0]` must be a read command (`log`, `show`, `blame`,
`diff`, `grep`, the listing commands); the options that run a program or
write a file are refused; every transport is off and no user configuration
is read. It runs below the session's process, so it inherits the session's
rules. Output past a size is written to a file in the session's state
folder and the path returned, as `journal_read` does.

### A fetch

1. The engine adds the repository host to the person's gateway policy.
2. It runs the fetches in the `git` container, in parallel, the credential
   passed to those processes alone and never written under the volume.
3. The processes end; the engine takes the host off the policy.

The credential is the workshop's repository account's, as for a run. A
member of a shared workshop gets its code and never its credential.

### A page

Each of the three pages is one definition (`internal/assistant/pages`):

| | flow | ticket | skill |
|---|---|---|---|
| Subject | workshop + flow name | workshop + ticket, or a draft | skill, or a draft |
| Context | the YAML as the editor holds it, its current errors, the workshop's bound parameters | the form as it stands, the workshop's types and statuses | the skill's fields and files as they stand |
| Fill | `{yaml}` | `{title, goal, change_type, type_id, labels}`, each optional | `{name, description, files}`, each optional |
| Check | what `POST /api/flows/validate` runs | the fixed change types, the workshop's types | the skill store's own rules |
| Repositories | the workshop's | the workshop's | none, or an attached workshop's |

A definition also carries the agent's standing instructions and a few
preset prompts. A turn's prompt is assembled by the engine: instructions,
context, which repositories are ready, then the person's words. The
session's `fill` tool takes that page's fill and nothing else. A fill that
fails its check returns the errors to the agent in the same turn and
reaches no page.

**verify** (phase 3): what a remote ticket source leaves editable on the
ticket page, and whether an imported skill is editable at all. Where a
page has no input to fill, the panel answers and offers no fill.

### The MCP face

- `POST /mcp` on the engine, bearer token, the guard and authorizer every
  route already goes through.
- A `Route` gains an optional tool declaration (a name and a description
  written for a model). The tool's input is reflected from the route's
  path, query and body types, as `openapi.go` reflects them. A call runs
  the route's handler in process.
- `tools/list` returns only what the token's actions and workshops allow.
- The first version declares read routes only: workshops, tickets, flows
  (with `validate`, the schema, the templates), skills, runs.
- An assistant session's token is minted by the engine for that session:
  read actions, the session's workshop, a short life, and the right to
  call that session's `fill`.

### Credentials and the keeper

- A resolution by person: the harness integration named in their settings,
  its credentials opened as a workshop's are
  (`internal/project/integrations`).
- `authsink` learns a second kind of holder beside a run's workspace: a
  person's pod. A refreshed login is pushed to it by credential id, the
  token registered with the assistant's redactor first.
- omp's login broker is served for the pod under a token of its own
  (**verify**: the route is per run today).

### Data and API

One new numbered migration: `assistant_sessions` (person, kind, workshop,
subject, harness session, model, repositories) and `assistant_messages`
(session, sequence, role, text, fill, cost). The chosen harness
integration is a setting of the person's.

Routes, each declaring one action of a new personal kind
(`assistant:read`, `assistant:write`); reading a workshop through a
session also needs that workshop's read:

- `GET /api/assistant`: on or off and why, the harness, its models, the
  pod's state, each repository's state.
- `POST /api/assistant/warm`: start the pod and the session's process for
  a page.
- `GET|POST /api/assistant/sessions`, `POST …/{id}/messages`,
  `PUT …/{id}/model`.
- `GET /api/assistant/stream`: answers as they are written, fills,
  repository states.
- `POST /api/assistant/repos/refresh`.

### Packages

- `internal/assistant`: the conversation service, the page contract, the
  turn. It reaches harnesses, the pod and the stores through interfaces
  it declares; `archtest` holds it off the engine, the API and the
  adapters.
- `internal/assistant/pages`: the three definitions.
- `internal/ports/assistantport`, `internal/adapters/assistant`: the pod's
  host on Kubernetes. To a harness adapter the `agent` container is an
  environment like any other (**verify**: the part of `envport` the
  adapters use).
- `workspace/shim`: `confine`, the `git` tool.
- `workspace/images/assistant`: the image.
- `cpui/src/lib/assistant`: the panel.

## Phases

Three. Each ends green on `task check`, with its notes written.

### Phase 1 — the MCP face

Stands alone: nothing later changes it, and it is useful the day it lands.

- The tool declaration on `Route`; input schemas reflected with the code
  `openapi.go` already has; the read routes declared as tools.
- `POST /mcp`: initialize, list, call, behind the API's own authentication
  and guard. A list shows a token only what its grant allows.
- Token creation in the UI offers a read-only grant for an MCP client, and
  a page in the docs says how to connect one.

Tests: every declared tool names a live route and its schema is valid; a
list equals the grant; a call goes through the guard (the 404 and 403
split holds); a scoped token never lists or calls a write route; the face
refuses a session cookie.

Done when: an MCP client on a laptop, with a scoped token, lists a
workshop's tickets and validates a flow.

### Phase 2 — the pod, its sessions and its repositories

Everything below the conversation. At its end an agent runs confined, on a
person's credential, over their repositories.

**First, what the design rests on.** Short experiments, a note each. If
one of the first three fails, stop and revise this plan.

1. Landlock applied by the shim as a non-root process in a pod with
   `hostUsers: false`, on the k3s box and in k3d; the kernel and ABI
   floor; the library's licence.
2. claude-code, opencode and omp each answer a turn with no shell
   executable and one writable folder, on their Alpine builds; what each
   must read and execute.
3. The tunnel container comes up with `NET_ADMIN` alone in a
   user-namespaced pod on the plain runtime.
4. Codex cannot read a repository without its shell, or the configuration
   under which it can.
5. Each harness loads its session in a new process from the same state
   folder, and takes the engine's face by URL and header. Where one does
   not, the shim bridges the face over stdio, as it does the browser.
6. Cold start, measured: pod with the image on the node, volume attach,
   gateway, harness boot.

**The pod.**

- `workspace/images/assistant` (`FROM` the tools image), built once in CI
  with the others; the chart's image list, the pins and the devtools tests
  follow.
- The pod, its volume and network policy, the two containers, the
  security context of decision 2.
- The person's gateway: the gateway manager keyed by person, the standing
  policy, the pod as its one peer (**verify**: nothing reads that key as a
  workshop id).
- The manager: ensure, reuse, stale image, idle stop, volume reaping.
  Instance settings (on or off, idle minutes, volume days) and chart
  values (volume size, resources).

**Confined sessions.**

- The shim's `confine` launcher. No Landlock: the start is refused, with a
  message the panel can show.
- The assistant session in each harness profile (tools off, the two MCP
  servers on) and the sidecar's permission rule for it.
- One process per session: its state folder, the idle exit, the relaunch.
- The resolution by person; model credentials seeded into the session;
  `authsink`'s second holder; omp's broker for the pod.

**Repositories.**

- Clone and fetch in the `git` container: the credential in memory, the
  gateway window, the parallel fetch, the folder names, a fetch when the
  pod wakes.
- The `git` tool on the shim.
- A session's repositories in its rules.

Tests: the manifest (every field of decision 2, no credential, no token
mount) and the manager's lifecycle against a fake host. In the shim and
live in the pod: a session reads its folder and not a sibling's, cannot
read another session's state, cannot execute a shell, cannot write outside
its state. The seeded files hold no refresh token; a pushed token reaches
the pod and is known to the redactor first; on a shared workshop the
caller's credential is used. No file under the volume and nothing in the
`agent` container ever holds the repository credential; the gateway
refuses the repository host before and after a fetch and allows it during
one; the `git` tool refuses each option on its list, has no transport and
cannot leave its repository. The harness conformance tests gain an
assistant session each, and the k3d suite a pod that reaches a provider
domain and nothing else, and cannot reach a second person's pod.

Done when: in a person's pod on k3d, a confined session on each of the
three harnesses answers "what does the test job run" from a repository and
"when did that change" from its history; the pod idles out and comes back
on the same volume.

### Phase 3 — the assistant

What a person sees. Built in this order: the flow page end to end (its
check is the strongest), then the ticket and skill pages, then the
release work.

- `internal/assistant`: sessions, messages, the stream, the turn, the
  redactor; the store, its migration and the store contract tests.
- The page contract and the three definitions; the session token and
  `fill` on the face; the API routes (`task api`).
- The panel: the composer, presets, sessions, the model picker, the
  harness choice in settings, repository states and the Refresh button;
  the workshop picker on the skill page.
- Applying a fill: through the path the YAML view uses for a flow
  (`cpui/src/lib/domain/flowdoc.ts`), into the form for a ticket and a
  skill; undo, the changed marker with its diff, the click before
  overwriting newer edits. The two **verify** items under "A page".
- Release: every rule under "Rules that ship as tests" exists and is named
  in a note; the live Kubernetes suite covers the pod end to end;
  `docs/kubernetes.md` (the pod, the gateway, the Landlock requirement),
  `docs/operations.md`, the README and the chart's README; cold start
  measured again and the idle defaults set from it.

Tests: a fill that fails its check returns its errors and emits nothing; a
valid one is emitted once and stores nothing; a session token calls only
its own session's `fill`; a fill for another subject is not applied; the
stream resumes by sequence; each definition's check. CPUI: the panel, the
fill, the undo and the guard on each page, each with its test beside it.
`task flows:check` still passes, and a filled flow comes back from the
editor the same flow.

Done when: on a flow page, "add a review step before the merge" fills the
editor with a flow that validates, and Undo restores the old one; a line
of notes on the new ticket page becomes a filled title, goal and change
type; a skill's files are filled from a description.

## Rules that ship as tests

1. No repository credential is ever readable from the `agent` container:
   not in a file, not on the shared volume, not in its environment.
2. A session reads its own repositories and state, and nothing else.
3. A session cannot execute a shell.
4. The `git` tool cannot write, cannot reach a network, cannot run a
   program.
5. The assistant's token changes nothing: every write route refuses it,
   and a fill stores nothing.
6. No refresh token reaches the pod. A pushed token is registered with
   the redactor before it leaves the engine.
7. A repository host is reachable only during a fetch. One person's pod
   never reaches another's.
8. The assistant spends the caller's credential, on a shared workshop too.
9. No Landlock, no session.

## Residuals

- **A fill is reviewed by the save alone.** A flow is executable, and the
  agent reads text the person did not write (an issue from a tracker, a
  file in a repository). The marker and the diff are what make that review
  possible; they do not make it happen.
- **What the agent reads goes to the model provider**, as in a run.
- **Code at rest.** A stopped pod's volume holds the clones. A pod that
  dies without being stopped may leave an access token on it until the
  token expires.
- **A shared workshop's code** sits on each member's volume. A viewer can
  already read a run's checkouts.
- **Clusters without Landlock** get no assistant.
- **Cost per person:** a second small pod (the gateway), and a harness
  process per live session.
- **A node that has never pulled the tools image** pays for it on the
  first start.

## Out of scope, on purpose

- Write tools on the MCP face, and everything an outside agent would do
  with them (start a run, create a ticket): phase 4, outlined under
  "After the assistant".
- Any page but the three: phase 5. The terminal client.
- The face inside a run. No flow hands the engine's MCP server to its
  stage agents, and the flow grammar gains nothing for it. The face has
  two clients: the assistant, and an agent a person connects with a token.
- A shell, codex, branches other than the default, submodules, LFS
  content.
- A fill written into the page as it is produced.
- One shared gateway with a policy per pod, if a gateway per person proves
  heavy.

## After the assistant

An outline, in the order to build it. Nothing here is settled in detail:
each phase gets its own decisions before it starts, and the open ones are
named. Both phases keep the face an ordinary MCP server: its clients stay
the assistant and an agent a person connects with a token.

### Phase 4 — outside agents act

Write tools on the face, for an agent that holds a scoped token.

- **Finer actions first.** One action is too much to hand an agent today.
  `run:operate` starts a run, and also answers its gates, takes it over,
  messages its agent and allows an egress host. `project:write` writes a
  flow, and also the workshop's settings and secrets. Split them, with the
  policy matrix, before any write route becomes a tool: starting and
  steering a run; answering it; its egress; a flow; a workshop's settings
  and secrets.
- **Write routes declared as tools:** create and edit a ticket; start,
  retry, pause and abort a run; create and edit a flow, a skill, a
  skillset, a dashboard, a scorecard, an automation.
- **Never a tool, held by a test:** credentials, secrets, tokens, members,
  the account, the shell, the terminal and the tunnels, an egress rule, an
  answer to a gate, a take-over, a deletion.
- **Waiting on a run:** a tool that returns when a run changes state or a
  time runs out, so an agent does not poll.
- **On the record:** what a token did names the token, wherever the same
  act by a person names the person (**verify** what each write records
  today), and a token's page lists its recent calls.
- The token form offers grants by purpose: read, tickets, runs, authoring.

Done when: an agent on a laptop files a ticket, starts a run on it, waits
for its merge request and reports the link, with a token that can do
nothing else.

Open: whether an agent may ever answer a gate. Recommended: no. A gate is
a person's.

### Phase 5 — the assistant acts, on more pages

- **Actions by confirmation.** The assistant's token stays read-only. A
  tool on the face lets it request an action: any declared write tool,
  with its arguments. The panel shows what would happen; the person's
  click sends it through the normal route, as the person. One request may
  hold several acts (five tickets cut from one). The outcome reaches the
  agent on its next turn.
- **More pages,** one definition each: a run, the analytics pages (a fill
  of the dashboard editor, written from the analytics catalog), a
  skillset, an automation, the New run dialog. And one entry with no page,
  from the command palette.
- **A run, read.** The journal, verdicts, attempts, costs and pings as
  read tools (the journal's routes are the run token's today). From a run
  the assistant suggests a change to the flow it ran on: a fill that opens
  in the flow editor, against the flow as it is now. The run is pinned to
  a version that may be older.
- **Many runs, read.** Analytics queries as read tools, so "where do this
  flow's runs fail" has an answer, and ends in a fill of the flow or of a
  dashboard.

Done when: from a ticket's page, "start a run on this" shows one
confirmation and starts it; from a failed run's page, the assistant fills
the flow editor with a change that addresses the failure; from the
analytics page, a described dashboard is filled into its editor.

Open: a confirmation for every action (recommended), or a standing
permission per kind of action.

## Facts verified while designing

- Provider credential → harness integration → workshop; workspaces hold
  access tokens only; the keeper pushes by credential id
  (`docs/model-providers-plan.md`, `internal/engine/authsink`).
- Every route declares an id, a summary, its body and return types and
  one action; the specification is reflected from that table
  (`internal/httpapi/routes.go`).
- A token is the intersection of its account and its grant; scoped tokens
  hold actions and workshops (migration 0012).
- A session can be given an MCP server by URL and headers
  (`agentport.MCPServer`).
- The tools bundle is copied onto each run's volume by an init container
  (`workspace/images/tools/seed.sh`); its image is Alpine and holds both
  libc builds.
- A run's clone passes the credential into the pod for the length of the
  script (`internal/adapters/workspace/host_k8s.go`, `ensureCheckout`),
  and the workspace holds it for the run
  (`internal/workspace/bootstrap`).
- A gateway has one policy, hot-reloaded over its control API; its peers
  cannot reach each other (`workspace/gateway`,
  `docs/notes/2026-11-13-no-run-reaches-another-through-the-gateway.md`).
- The shim hosts several agent processes; a session names the one it runs
  in (`workspace/shim/acp.go`).
- The project browser is the precedent for a pod that is not a sandbox:
  the shim seeded, the engine as its only peer, stopped when idle
  (`internal/adapters/browser`).
- Nothing in the tree uses Landlock today.
