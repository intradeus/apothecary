# Screenshots in the merge request: services, the project browser, MCP, files

The test the user set the flow language: bring the app up, have an agent look
at it in a browser, put the screenshots in the merge request. Four pieces,
built at once (2026-10-26), each usable without the others.

- **`type: files`** — a directory port (`artifacts/shots/`, the trailing `/`
  is the type's mark). Emptied before a fresh attempt (`rm -rf` of the path
  without its slash, so a planted symlink goes, never its target), present
  when it holds ≥1 file, listed (never pasted) in a reader's prompt, captured
  one snapshot per file (≤20, ≤10 MB — the store tail-truncates, which would
  corrupt an image). `file` readers are not satisfied by it. New required
  `EnvProvisioner.ListFiles` (POSIX `find` + `wc -c`, busybox-safe, same
  containment guard as a read).
- **Attachments** — `create_mr` / `ticket_comment` take an optional
  `attachments` (`files`). `RepoHost.UploadAttachment` → `{URL, Link,
  Markdown}`: GitLab `POST /projects/:id/uploads`; GitHub
  `uploads.github.com/user-attachments/assets` (what `gh` uses — classic PAT
  with `repo` only; fine-grained/app tokens 404, GHES has no such endpoint —
  both refused with that sentence); Jira issue attachments (v3 links them —
  ADF media needs a Media API id; v2 wiki `!name!`) through the optional
  `ports.TicketAttacher`. Body rewrite: link/image targets and whole-path
  mentions become the upload; unmentioned files follow under Screenshots /
  Attachments. Limits checked before any upload. create_mr reconciles
  (FindMR) before reading anything, so an adopted MR never re-uploads; a
  crash between upload and creation leaves orphan uploads, never a second
  MR. Known: a path inside a fenced code block is rewritten too.
- **Services** — declared in the project (`workspace.services`: start, stop?,
  one health probe, ports), named by flows through a parameter (`service:
  $app`, default `default` — the user's own project calls it `aio`; nothing
  hardcodes either). A shim supervisor (`apothecary-shim service …`)
  detaches with setsid, survives the exec and engine restarts, keeps
  `/apothecary/services/<name>/{spec,state}.json` + `output.log` in the
  container (a rebuild honestly wipes them with the processes) and is read
  through the sidecar (exec fallback). A start that exits 0 daemonized
  something; then only the probe decides. Stage-started services stop at
  their chain's end (every one at the run's); human-started ones never by the
  engine. Human actions append events from outside the loop, so appends now
  take a per-run mutex. Health flips are polled state, not events. Run page:
  `GET/POST /api/runs/{id}/services[/{name}/start|restart|stop|logs]`.
- **MCP** — flow `mcp_servers:` (builtin | command | url; `${secrets.x}` in
  env/headers) + agent `mcp:`. Resolved at stage start (secrets re-read and
  taught to the redactor) into `ports.AgentInput.MCPServers` and sent on the
  ACP `session/new`/`session/load` — not harness config files: opencode
  reads `opencode.json` once per process, so per-stage secrets and a server
  per session (one Chromium each) only work through ACP. opencode keeps a
  session's servers for the process, so each agent's markdown turns the
  others' tools off (`"browser_*": false`). Known: two concurrent sessions
  on one server in one workspace (a watcher beside the main chain) — the
  second replaces the first's process; sidechat sessions get no toggles.
  Project secrets: new table `project_secrets` (one sealed row each) in
  migration 0009 — not 0001, which `TestInitMigrationIsFrozen` pins for
  databases that applied it.
- **The project browser** — `apo-browser-<project>` (egress-gateway
  lifecycle: create on need, recreate when stale and idle, stop after 10
  idle minutes, warmed at run creation), `--network none`, caps dropped;
  image `node:22` + `@playwright/mcp@0.0.83` + its Chromium + the shim.
  Session: the workspace's `shim browser-mcp` upgrades
  `GET /api/runs/{id}/browser-mcp` (run token), the engine `docker exec`s
  `shim browser-session -- playwright-mcp --isolated …` with
  `--proxy-server` at a per-session unix socket the daemon serves: loopback
  hosts dial the run's port through `ExposePort`, every other host 403.
  Named screenshots are taken inline and written into the workspace by the
  shim (Playwright would write them in the container). Known: an agent can
  run code in the shared container, so sessions of one project can reach each
  other's proxy sockets (projects stay isolated); no reconnect mid-session.
- **Builder template** — `screenshots: false` + `app: default`; on, `app_up`
  (service, best effort: a dead app opens the MR without shots) and `shoot`
  (a `photographer` agent with the browser, to the debug leaf like every
  agent) run after the review; the redactor may show shots inline and
  `create_mr` attaches them. Off, run creation asks for no service and no
  browser: `Flow.Services/Secrets/UsesBuiltin` skip stages `when:` folded
  off.
