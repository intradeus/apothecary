# The assistant

Phase 3 of `docs/assistant-plan.md`: the conversation beside the flow,
ticket and skill pages, on the pod of
`2026-11-19-the-assistant-pod.md`. What the plan left open, and where the
build departs from it.

## Why

The plan fixes the shape (a session per person and subject, one tool that
acts — `fill` — and a check in the engine before any page sees a fill).
Building it settled how a session reaches the engine, which harnesses can
carry it, where its settings live, and what each page sends and takes.

## What now

- **A session's token is minted for it**, through the auth service as any
  token is: the face's read actions (the tools' own, `httpapi.MCPActions`,
  handed to `assistant.Service.FaceActions` by the wiring) plus
  `assistant:fill`, scoped to the session's workshop, for one turn: minted
  when the turn starts and revoked when it ends, so a harness process left
  running in the pod between turns reaches nothing. A skill session with no
  workshop gets `skill:read` and the fill only.
- **`fill` is on the MCP face, bound to the session.** The route
  (`POST /api/assistant/sessions/{id}/fill`, tool `fill`) is listed only for
  a token that names a session, takes that session's page fill and nothing
  else, and a call from any other token is `ErrNotYours`. It is left out of
  `MCPActions`, so no other token is ever granted it. A fill is checked by
  the page (`internal/assistant/pages.go`): errors go back to the agent in
  the same turn; a good one is emitted once on the person's stream, stored
  with the answer, and changes nothing on the engine. A fill holding one of
  the turn's secrets (its harness's key, a seeded login) is refused, so a
  prompt injection asking for the key cannot put it in a page that a Save
  would send to a tracker. A secret re-encoded by the agent (base64) is
  not caught: that residual is the harness login's, as for a run.
- **Every change to a stored session reads it again first**, under one
  lock (`Service.edit`): the end of a turn writes only what the turn
  learnt (its harness session; a model or harness when none was set), so
  a draft saved, or a model picked, while it answers stays.
- **Two harnesses: Claude Code and opencode.** omp does not give its model
  the MCP servers a session names (it loads its own configuration only), so
  neither the face nor the git tool would reach it; codex reads through its
  shell, which a session has none of. Each profile turns its write and
  shell tools off in the harness's own configuration (Claude Code's
  `settings.json` deny list, opencode's `tools`), runs in its
  approvals-off mode, and its process is confined by Landlock and the
  gateway. The sidecar's read-only flag is not used: it would refuse the
  MCP tools these sessions exist to call, and Landlock is what holds.
- **Settings are boot environment, not instance settings**:
  `APOTHECARY_ASSISTANT`, `_IMAGE`, `_IDLE`, `_VOLUME_DAYS`, `_VOLUME_SIZE`,
  `_STORAGE_CLASS`, from the chart's `assistant:` values. They size and
  place pods, which is the operator's, as the other Kubernetes settings
  are. The harness is each person's choice (`PUT /api/assistant/choice`).
- **The stream is per person**, a ring of 512 frames. A panel connects
  from now (`head` names where), reading what it opens from the record,
  so a fill kept in the ring is never replayed onto a page. A reconnect
  resumes after the last sequence it saw; one behind the ring, or from
  before the engine restarted (its sequence past the head), gets a `gap`
  frame and opens its session again, busy or not.
- **A panel applies only the fills of its own question** (the guard is
  asking from the question to the answer's end): not one replayed, not
  one from the same session open in another tab. An earlier answer's fill
  is applied on a click (*Fill the page with this*). A second fill in one
  answer corrects the first: Undo returns to before both. Loading, saving
  or replacing the page settles the guard (nothing left to undo).
- **The pages:**
  - flow: the editor's YAML as it stands (the graph written out when it was
    edited there), its errors, and the workshop's values for its parameters
    (read when the panel opens). A fill goes into the YAML tab, the way a
    loaded version does; Save makes it current.
  - ticket: the form (title, goal, change type, type, base branch) and the
    workshop's issue types. The fill is `{title, goal, change_type,
    type_id}`: **no labels**, which the plan listed, because the form has no
    labels to fill. The assistant lives in the form, so a remote ticket,
    which has no form here, offers no fill (the plan's first **verify**).
  - skill: the name and the text files; a file that is not text stays as it
    is unless a fill writes its path. An imported skill is editable (edits
    last until its next refresh), so it is filled as any other (the second
    **verify**). The panel can attach a workshop, whose repositories the
    session then reads; each workshop is a session of its own.
- **A draft's conversation follows what it becomes.** A new ticket or skill
  opens a session about a draft id; when the page creates it, the page
  moves the session onto the ticket's key or the skill's id
  (`PUT …/subject`), so editing it later finds the same conversation. A
  failed move leaves the conversation with the draft and the save stands.
- **The guard on each page** (`cpui/src/lib/assistant/fillGuard.svelte.ts`):
  a fill is applied with an Undo and the changed fields named; a fill
  arriving after the person edited the page since asking is held with
  "Fill it anyway" and "Keep mine".

Not done: Undo after a fill that removed an executable support file puts
the file back without its mode (the fill's files carry no mode); the person
sets it again.

## Tests

- `internal/assistant`: `TestATurn` (the pod, the confined start, the
  face and git tool, a refused fill back to the agent, a good one emitted
  once, another token's refused, the secret scrubbed),
  `TestOpenAndOwnership`, `TestTokenIsRemintedWhenOld`,
  `TestWatchResumes`, `TestASkillReadsAnAttachedWorkshop`, the pages'
  checks (`pages_test.go`), the store contract (`assistanttest`, run on
  memory and on Postgres by `pgstore/assistant_contract_test.go`).
- `internal/httpapi`: `assistant_test.go` (each route, a session token's
  fill on the face, another session's refused) and `mcp_test.go` (fill is
  in no other token's catalog).
- CPUI: `AssistantPanel.test.ts`, `fillGuard.svelte.test.ts`, the assistant
  cases of `flowEditor.svelte.test.ts`, `FlowHeader.test.ts`,
  `FlowEditorPage.test.ts`, `TicketForm.test.ts`,
  `routes/skills/[skillId]/page.test.ts`, and `skills/files.test.ts`.
- The chart: `task chart:lint` refuses a bad `assistant.idle` and a
  negative `assistant.volumeDays`.
