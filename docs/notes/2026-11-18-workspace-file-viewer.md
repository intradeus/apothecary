# Workspace file viewer

Why: the Files pane was a one-file, plain-text tree that refreshed on a
click. It could not follow a conversation: several files open, a line
pointed at in the chat, a path the agent mentioned. It is now a read-only
CodeMirror viewer with tabs, absolute paths, annotations into the active
chat, live refresh and linked paths. These are the choices the code does not
explain on its own.

What now:

- **Svelte is highlighted as HTML.** `@codemirror/language-data` has no
  Svelte grammar, so `ui/codeLanguages.ts` reads a `.svelte` file as HTML
  with TypeScript `<script>` and CSS `<style>` nesting. Markup, script and
  style are coloured; `{#if}` blocks are not. A third-party Svelte grammar
  would be one entry there. Shell dotfiles and shebang scripts map to their
  language too. Each grammar is its own lazy chunk, loaded through the
  description's cached `load()`.
- **Absolute reads go through the sidecar, as the remote user.** `GET
  /fs/abs/tree` and `/fs/abs/file` (engine: `workspace/path/tree|file`)
  expand `~` to the sidecar's `$HOME` and read whatever that user can: the
  same reach as the run's terminal, never more. Nothing is hidden, not the
  shim's directory, not `/apothecary/run.env`: the ticket decided the run's
  owner (`ScopeRun`, the same people as the terminal) has that access
  already. Unreadable is 403; a FIFO, device or socket is refused before it
  is opened (400), so a read cannot block on one; reads go through a limit
  reader rather than trusting the size (`/proc` files report 0). Checkout
  reads keep their containment check.
- **Live refresh polls size and mtime; it does not watch.** One batched
  `POST /workspace/stat` every 3 s while the Files pane is visible and the
  browser tab is shown, covering the open tabs and expanded folders (at
  most 256 items, `os.Stat` only, no git). It never overlaps itself, backs
  off to 30 s after errors and stops against a sidecar that predates the
  route. Only a changed active tab is fetched again; a hidden one gets the
  dot. A file-change stream from the sidecar stays deferred, as the ticket
  says.
- **mtime is a string.** Nanoseconds since the epoch exceed JavaScript's
  safe integers, so `mtime` is a decimal string on every entry, file and
  stat; the UI only compares it.
- **Old sidecars answer 501.** A workspace started before this keeps its old
  shim, whose unknown route is a plain-text 404 that would read as a
  missing file. The sidecar client tells it from the shim's JSON 404
  (`sidecar.Error.NoRoute`) and the engine answers 501: restart or rebuild
  the workspace.
- **Paths resolve in a fixed order.** `GET /workspace/resolve`: a relative
  path in the named checkout first (the primary, the agent's working
  directory, by default), then the run's other checkouts, then an absolute
  (or `~`) path; an absolute path inside a checkout's real directory maps
  back to that checkout. Links are checked on click, so a false match in
  prose costs a toast, not a dead link.

Tests that hold it: `workspace/shim/workspace_test.go` and `sidecar_test.go`
(FIFO and `/dev/null` refused without blocking, mode-000 refused, `~`
expansion, the stat batch, the resolve order),
`internal/workspace/sidecar/client_test.go` (`NoRoute`),
`internal/httpapi/workspace_views_test.go` (relays, the stat cap, 501),
`cpui/src/lib/ui/codeLanguages.test.ts`, `cpui/src/lib/run/openFiles.svelte.test.ts`
(the poller) and `cpui/src/lib/core/paths.test.ts`.
