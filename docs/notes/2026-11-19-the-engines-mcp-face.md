# The engine's MCP face

Why: phase 1 of `docs/assistant-plan.md`. An agent outside the platform
could not read it; the scoped tokens of the permissions work were built for
this ("MCP tokens come next"). These are the choices the code does not
explain on its own.

What now:

- **A tool is a route.** `Route.Tool` declares a name, a description for a
  model and a doc for each path parameter. The input schema is reflected
  from the route's path, query and body types, with the generator the
  OpenAPI document uses, inlined (a cycle is an error) and with `nullable`
  rewritten as a JSON Schema type list. A body's fields are flattened into
  the arguments, required when the type always sends them (no `omitempty`,
  as the OpenAPI document reads it) and described by `Tool.Args` like the
  path parameters; a name clash between path, query and body fails the
  catalog, not a call.
- **A call is the route.** It builds a request from the arguments and runs
  the route's own `guard`, with the caller's bearer header: the same
  authentication, authorizer and 404/403 split. The face adds no access
  rule. A route's error comes back as a tool error result the model reads
  (`404 Not Found: …`), never a protocol error.
- **The list is the grant.** `tools/list` holds a tool when the authorizer
  allows its action with no resource, so a scoped token lists only its
  actions' tools, and a server without a dependency (no skill library)
  lists none of its routes. A call to a tool outside the list is "unknown
  tool", the answer for a tool that does not exist.
- **Bearer only.** A request without `Authorization: Bearer` is 401, a
  session cookie included: a page on another site would ride a cookie.
  Every 401 carries `WWW-Authenticate: Bearer`, and `/.well-known/` answers
  404 JSON (not the UI), so a client that tries OAuth discovery fails
  plainly. An `Origin` from another host is 403; that only keeps browsers
  out, the token is the protection.
- **A ticket key stays in its source.** Review found that the GitHub,
  GitLab and Jira sources read any issue key their token could reach: a
  viewer of a shared workshop could name the owner's other repositories
  (`org/private#1`) over HTTP already, and a model passes such a key as
  plain text. A key outside the source's repositories, projects, groups or
  filter projects is now not found, before any request (`parseKey`,
  `inScope`). A source that lists none still covers what its token sees.
- **Stateless, JSON answers.** No `Mcp-Session-Id`, no SSE stream: `GET`
  and `DELETE /mcp` are 405. Batches are answered (older revisions send
  them). The revisions spoken are 2024-11-05 to 2025-11-25; nothing the
  face does differs between them.
- **Reads only.** Every declared tool is a read route: a GET, or a POST
  whose action reads (`validate_flow`). A tool's answer is cut at 256 KiB,
  on a character boundary, with a line telling the model to narrow it; a
  refusal carries its `Retry-After`.
- **JSON-RPC is checked before it is answered.** An id is a string or a
  number; `jsonrpc` is "2.0" on every message; a message with neither a
  method nor a result is a bad request owed an answer, not a response.
- **The token form's MCP client grant comes from the engine.** `GET
  /api/permissions` marks the actions a tool needs (`mcp`); the form's
  **MCP client** choice holds exactly those, and shows the client
  configuration once with the secret.

Tests: `internal/httpapi/mcp_test.go` (the catalog: unique snake_case names,
read routes only, every schema compiles, every path parameter required, bad
declarations refused; the protocol and transport; bearer only; the list
equals the grant; a call answers as the route, the guard's 404 and a
limited token's workshops included; the cut). The trackers' key scope:
`TestIssuesParseKey` (GitHub, GitLab), `TestKeysOutsideTheFilterAreNotFound`
(Jira). CPUI:
`TokenGrantForm.test.ts`, `TokenSection.test.ts`.
