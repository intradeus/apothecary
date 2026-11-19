# The engine's MCP server

The engine serves the [Model Context Protocol](https://modelcontextprotocol.io)
at `/mcp`, over its Streamable HTTP transport. An agent connected to it
reads the platform with your token: your workshops, their tickets, flows,
automations and parameter bindings, your skills and skillsets, and runs. It
can check a flow's YAML without saving it. It changes nothing you could
change in the UI: reading a remote ticket only refreshes the engine's own
copy of it, as opening its page does.

## Connect a client

1. **Account → Personal tokens:** name the token, choose **MCP client**,
   and optionally tick the workshops it may reach (none ticked: every
   workshop you can reach). Mint it.
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

## What a token sees

- The tools a token lists are the ones whose action it holds. An **MCP
  client** token holds exactly the read actions the tools need (the token
  form shows them); a full-access token lists every tool; a limited token
  lists the tools of the actions it was given.
- A call runs the API route the tool stands for, as the token: the same
  checks, the same answers. A workshop the token may not reach is not
  found, as it is over HTTP.
- The server never sees more than the account may at the time. Revoke the
  token on the Account page to cut the client off.

## What it is not

- A browser session cannot use it: it takes a bearer token only, and a
  request a browser sends from another site is refused. A client that runs
  in a browser tab (the MCP Inspector's web page, for one) cannot connect
  directly; run it through its local proxy.
- It has no OAuth: on a refused token a client finds no metadata under
  `/.well-known/` and should say the token is wrong.
- It keeps no session and opens no stream: each request is answered on its
  own, and `GET /mcp` is refused.
- It has no write tools yet. Creating tickets and starting runs are planned
  (`docs/assistant-plan.md`, phase 4).

## The tools

Every tool is an API route that declares one (`Tool` in
`internal/httpapi/routes.go`). Its arguments are the route's path and query
parameters and its request body's fields, reflected from the same Go types
the OpenAPI document is, so a tool cannot drift from its route.

| Tool | Reads |
|---|---|
| `list_workshops`, `get_workshop`, `list_workshop_branches` | workshops and their repository's branches |
| `list_tickets`, `get_ticket`, `list_ticket_comments`, `list_ticket_statuses`, `list_ticket_types` | a workshop's tickets, from its ticket source |
| `list_flows`, `get_flow`, `list_flow_versions`, `get_flow_version` | a workshop's flows and their saved versions |
| `list_flow_templates`, `get_flow_schema`, `validate_flow` | the built-in flows, the flow schema, and a check of a flow's YAML |
| `list_automations`, `list_flow_bindings` | how a workshop's runs start, and the values its flows are bound to |
| `list_skills`, `get_skill`, `list_skillsets`, `get_skillset` | your skill library |
| `list_runs`, `get_run` | runs and their state |
