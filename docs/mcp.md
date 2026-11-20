# The engine's MCP server

The engine serves the [Model Context Protocol](https://modelcontextprotocol.io)
at `/mcp`, over its Streamable HTTP transport. An agent connected to it
works with your token. It reads the platform: your workshops, their
tickets, flows, automations and parameter bindings, your skills and
skillsets, dashboards, and runs. It acts where the token lets it: it files
tickets, starts runs and waits on them, and writes flows, skills and
dashboards. Some things stay yours whatever the token holds (see
[Never a tool](#never-a-tool)).

## Connect a client

1. **Account → Personal tokens:** name the token, choose **MCP client**,
   then what it is for (below), and optionally tick the workshops it may
   reach (none ticked: every workshop you can reach). Mint it.
2. The page shows the token once, with a configuration most MCP clients
   take:

   ```json
   {
     "mcpServers": {
       "apothecary": {
         "type": "http",
         "url": "https://apothecary.example.com/mcp",
         "headers": { "Authorization": "Bearer <token>" }
       }
     }
   }
   ```

   A client configured on the command line wants the same two things: the
   URL, and the header `Authorization: Bearer <token>`.

| For | The token holds |
|---|---|
| Reading | every read tool's action: it changes nothing |
| Tickets | the reads, and `ticket:write`: it files and edits tickets |
| Runs | the reads, `ticket:write` and `run:operate`: it files tickets, starts, pauses, resumes, retries and aborts runs, and waits on them |
| Authoring | the reads, `project:author`, `skill:write` and `analytics:write`: it writes flows, skills, skillsets, dashboards and scorecards |

Two writes need more than their purpose. A flow that reads the workshop's
secrets (`${secrets.<name>}`) also needs `project:secrets`, which no purpose
holds: a flow runs with what it reads, so writing one that reads a secret
reads it. An automation also needs `run:operate`: it starts runs. A limited
token with both actions, or a full-access one, writes them. Skills,
skillsets, dashboards and scorecards are yours, not a workshop's: a token
limited to some workshops still writes them, and every workshop's runs use
the skills.

The engine says which actions each purpose holds (`GET /api/permissions`,
each action's `purposes`), so the form never drifts from the tools.

## What a token sees

- The tools a token lists are the ones whose action it holds. A
  full-access token lists every tool; a limited token lists the tools of
  the actions it was given.
- A call runs the API route the tool stands for, as the token: the same
  checks, the same answers. A workshop the token may not reach is not
  found, as it is over HTTP.
- The server never sees more than the account may at the time. Revoke the
  token on the Account page to cut the client off.
- **What it did is on its record.** Every write a token makes, through a
  tool or straight through the API, and everything it was refused, is
  listed under its **Calls** on the Account page (its last 200). Reads
  that were answered are not kept, so an agent waiting on a run for hours
  does not push its writes out. The act itself names you, as one made in
  the browser does; the record names the token.

## Waiting on a run

`wait_run` answers when a run changes (its state, a question or gate
opened for a person, a merge request opened) or when its time runs out
(50 seconds unless asked otherwise, at most 55: under the minute an MCP
client gives a call). It returns the run and
`seen`. Passing `seen` back waits for the next change, so no change between
two calls is missed. So an agent files a ticket, starts a run on it, and
waits for its merge request without polling.

## Never a tool

Whatever the token holds, these are not tools. They stay a person's, in
the UI or with the API directly:

- credentials, secrets, tokens, members, the account;
- the shell, the terminal, the editor and the tunnels into a workspace;
- an egress rule;
- an answer to a gate or a question, a take-over, a message to a run's
  agent;
- any deletion, and archiving a run.

`TestMCPNeverATool` holds the list.

## The assistant's own tools

The in-app assistant reaches the same server with a token of its own,
minted per answer and holding the read tools only. Two more tools are its
alone, bound to its conversation, and no other token lists them:

- `fill` puts a proposal in the page beside it (a flow's YAML, a ticket's
  fields, a skill's files, a dashboard's panels), unsaved;
- `request` asks you to confirm acts: each one of the write tools above,
  with its arguments. The panel shows them; **Do it** runs them in order,
  as you, through the same routes, until one fails, and the assistant
  learns what came of them with your next message.

## What it is not

- A browser session cannot use it: it takes a bearer token only, and a
  request a browser sends from another site is refused. A client that runs
  in a browser tab (the MCP Inspector's web page, for one) cannot connect
  directly; run it through its local proxy.
- It has no OAuth: on a refused token a client finds no metadata under
  `/.well-known/` and should say the token is wrong.
- It keeps no session and opens no stream: each request is answered on its
  own, and `GET /mcp` is refused.

## The tools

Every tool is an API route that declares one (`Tool` in
`internal/httpapi/routes.go`). Its arguments are the route's path and query
parameters and its request body's fields, reflected from the same Go types
the OpenAPI document is, so a tool cannot drift from its route. A tool that
changes something says so to the client (`readOnlyHint` false), and
`abort_run`, which cannot be taken back, is marked destructive.

| Tool | Does |
|---|---|
| `list_workshops`, `get_workshop`, `list_workshop_branches` | reads workshops and their repository's branches |
| `list_tickets`, `get_ticket`, `list_ticket_comments`, `list_ticket_statuses`, `list_ticket_types` | reads a workshop's tickets, from its ticket source |
| `create_ticket`, `update_ticket` | files and edits a ticket on a workshop's built-in tracker |
| `list_flows`, `get_flow`, `list_flow_versions`, `get_flow_version` | reads a workshop's flows and their saved versions |
| `list_flow_templates`, `get_flow_schema`, `validate_flow` | reads the built-in flows and the flow schema; checks a flow's YAML |
| `create_flow`, `update_flow` | saves a workshop's flow |
| `list_automations`, `list_flow_bindings` | reads how a workshop's runs start, and the values its flows are bound to |
| `create_automation`, `update_automation` | writes a workshop's automations (with `run:operate` too) |
| `list_skills`, `get_skill`, `list_skillsets`, `get_skillset` | reads your skill library |
| `create_skill`, `update_skill`, `create_skillset`, `update_skillset` | writes it |
| `list_runs`, `get_run`, `wait_run`, `read_run_journal` | reads runs and their journals (each stage's attempts, verdicts, decisions and notes), and waits for one to change |
| `start_run`, `pause_run`, `resume_run`, `retry_run`, `abort_run` | starts a run on a ticket, and steers it |
| `get_analytics_catalog`, `list_dashboards`, `get_dashboard`, `list_scorecards`, `get_scorecard` | reads the analytics catalog and your dashboards and scorecards |
| `query_analytics`, `analytics_values`, `drill_analytics` | runs an analytics query over your runs, a dimension's values, and the runs behind a number |
| `create_dashboard`, `update_dashboard`, `create_scorecard`, `update_scorecard` | writes them |
