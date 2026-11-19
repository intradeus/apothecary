# Outside agents act

Phase 4 of `docs/assistant-plan.md`: write tools on the MCP face, for an
agent that holds a scoped token. What the plan left open, and how it was
settled.

## Why

Before a write route could be a tool, one action was too much to hand an
agent. `run:operate` started a run and also answered its gates, took it
over and widened its egress. `project:write` wrote a flow and also the
workshop's settings and secrets. Granting an agent "start runs" granted it
those too.

## What now

- **Finer actions.**
  - `run:operate` keeps starting and steering: start, pause, resume, retry,
    abort, archive, start a phase, rebuild, a run's services.
  - New `run:answer`: answering a gate or a question, a take-over, a
    message to the agent, the Ask pane.
  - New `run:egress`: allowing or denying a host.
  - `project:write` keeps the workshop's settings: its repositories,
    workspace, preflight and build cache.
  - New `project:author`: flows (copies of a template included), their
    bindings and the automations. Automations sit with the flows, not the
    settings: they are what an author writes beside a flow. Since they
    start runs, writing one needs `run:operate` too (below).
  - New `project:secrets`.

  A person's role holds the new actions as it held the old ones (operator:
  answer and egress; maintainer: author and secrets), so nothing changes
  in the UI for a person. Its checks name the precise action. Migration
  `0014_split_token_actions` gives a token that held an old action the
  ones split from it, so no token loses what it could do.
- **What authoring cannot reach** (found in review). Writing a flow
  writes code that runs with what the flow reads, and an automation starts
  runs with no person. So:
  - a flow whose document reads a secret (`${secrets.`, matched in the
    text, whether the secret is new or was already there) needs
    `project:secrets` to be saved, created or copied in;
  - an automation needs `run:operate` to be created or changed.

  A person who writes flows (a maintainer) holds both, so nothing changes
  for people. A token with the Authoring purpose writes neither. Residual:
  any run's workspace holds the run's harness and git credentials, and a
  flow's command stages read them as any agent there can; that is the
  workspace's trust boundary, not the author's (README, Security).
- **Write tools**: tickets (create, update), runs (start, pause, resume,
  retry, abort), flows, automations, skills, skillsets, dashboards and
  scorecards (create, update), with read tools for dashboards, scorecards
  and the analytics catalog so an agent sees what it edits. `abort_run` is
  marked destructive.
- **Never a tool** (`TestMCPNeverATool`): any route needing a credential,
  integration, secrets, members, account, settings, shell, egress or
  answer action, `project:write`, any DELETE, and any path under tokens,
  auth, hold, answer, egress, terminal, tunnel, ssh or archive. The
  plan's open question, whether an agent may ever answer a gate, is
  settled as **no** (agreed 2026-11-19): `run:answer` is never a tool's.
- **The assistant's token stays read-only.** `MCPActions` is the read
  tools' actions only; an assistant session's token is minted from it. The
  write tools are a client's whose token holds them.
- **`wait_run`** (`GET /api/runs/{id}/wait`) answers when the run's state,
  its open pings, its merge requests or its take-over differ from `seen`,
  or when its time runs out (50 s by default, at most 55: under the minute
  an MCP client gives a call by default). Its log position is taken before
  the run is read, so an event between the two is replayed, not skipped. It answers at once for an ended run. `seen` is
  returned and passed back, so a change between two calls is never
  missed; it is `|`-separated, since Go's query parser refuses `;`.
- **The record** (the plan's **verify**). What an act records of who did
  it today: a run its owner (`user_id`, the person who started it) and
  `started_by: human`; a ping's answer its person; the personal resources
  (skills, dashboards, credentials) their owner. Some acts record no one at
  all (a flow saved, an automation changed), and none names a token. A
  token's act records what the same act in a browser does, its person,
  and that stays. Threading a token id through every act's record was not
  done. What names the token is a call log beside the acts:
  `api_token_calls` (`0015_token_calls`). The guard records every write a
  token makes and everything it is refused, with the route, path, MCP tool
  and status. A read that was answered is not kept: an agent waiting on a
  run for hours would push its writes out of the 200. Each token keeps its last 200, deleted with it,
  and they are listed at `GET /api/me/tokens/{id}/calls` and under the
  token's **Calls** on the Account page. A browser session's acts are not
  recorded there: they are the person's own.
- **Skills, skillsets, dashboards and scorecards are personal**: a token
  limited to some workshops still writes them (they pass the workshop
  limit as instance resources), and every workshop's runs use the skills.
  The Authoring help says so.
- **The token form offers purposes** for an MCP client: Reading, Tickets
  (`ticket:write`), Runs (`ticket:write`, `run:operate`), Authoring
  (`project:author`, `skill:write`, `analytics:write`), each on top of the
  read tools' actions. The engine marks each action's purposes
  (`Permission.Purposes`), and `TestMCPPurposesCoverTheTools` holds every
  write tool's action in some purpose and no purpose granting what no
  tool needs.

## Tests

- `internal/httpapi/agent_test.go`, `TestAnOutsideAgentShipsATicket`: the
  done-when. With the Runs purpose in one workshop, an agent files a
  ticket, starts a run on a flow that opens a merge request, waits for it
  with `wait_run`, and reads its link. Its settings, flow, answer, egress,
  secret and token calls are each refused 403, a real gate's answer among
  them. Its record lists its writes and its refusal, not its waits.
  `TestWaitRun` covers the wait's edges; `TestAuthoringStopsAtSecretsAndRuns`
  the two limits.
- `internal/httpapi/mcp_test.go`: `TestMCPCatalog` (reads marked, writes
  are POST or PUT), `TestMCPNeverATool`, `TestMCPPurposesCoverTheTools`,
  `TestPermissionsMarkTheMCPGrant`.
- `internal/account/authz`: the policy matrix with the new actions.
  `internal/adapters/pgstore`: `TestSplitTokenActionsMigration`, and the
  call log in the auth store contract (`authtest`, on memory and Postgres).
- CPUI: `TokenGrantForm.test.ts` (purposes), `TokenSection.test.ts`,
  `TokenCalls.test.ts`.
