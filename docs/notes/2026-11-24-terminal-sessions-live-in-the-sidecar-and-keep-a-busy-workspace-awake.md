# Terminal sessions live in the sidecar and keep a busy workspace awake

The run page's terminal used to be one shell per WebSocket: the sidecar
sent it SIGHUP when the socket closed, so a refresh, a tab switch or a
closed browser killed whatever ran in it. Now the shells are sessions the
workspace keeps, and the Terminal pane manages them itself (tabs: open,
switch, close), so nobody needs tmux or screen to keep a program alive.

This extends `2026-11-22-idle-sandboxes-stop-and-keep-their-disks.md`:
"what counts as use" gains a program running in a terminal.

## Where the sessions live, and who sees them

- **In the sidecar** (`workspace/shim/termsession.go`). It is one
  long-lived process per workspace container, so a session outlives the
  page, the browser and an engine restart; the engine holds no session
  state at all. `GET /term` without `?session=` starts a login shell in
  the checkout `?repo=` names, with `?session=<id>` attaches to a live one;
  a socket close only detaches. `GET /term/sessions` lists them,
  `DELETE /term/sessions/{id}` hangs one up. The engine exposes them as
  `GET /api/runs/{id}/terminal/sessions` and
  `DELETE /api/runs/{id}/terminal/sessions/{session}`, both run:shell and
  never MCP tools.
- **Shared** (the owner's decision): everyone holding run:shell on the run
  sees, attaches to and closes the same sessions, scrollback included. The
  shells already ran as the workspace's user whoever opened them; per-person
  sessions would have hidden nothing that user cannot reach anyway. Several
  clients may attach at once: each sees the same output, any of them types,
  and the last resize wins.
- **They end with the workspace.** A stop on idle (with nothing running),
  a rebuild or a destroy ends every session; the pane says the workspace
  restarted rather than showing a silently fresh shell.

## Replay, cap, sizes

- Each session keeps its newest **512 KiB** of output in a ring and replays
  it to whoever attaches, after a `{"type":"session","id","n","created"}`
  text frame and before live output. The attach registers the client and
  snapshots the ring under one lock, so nothing is skipped or repeated.
- The replay is raw bytes: where the ring wrapped it may start mid escape
  sequence. The client resets xterm before it, and a garbled first line is
  accepted. A full-screen program is sent SIGWINCH on attach (the kernel
  sends none when the size did not change), so it redraws.
- At most **16** live sessions per sidecar (409 past that): 8 MiB of rings
  at worst. Each client has a bounded queue (256 chunks); one that falls
  behind is dropped and disconnected, so the PTY reader never waits.
- When the shell exits, clients get `{"type":"exit"}` and a close. A job
  the shell left holding the terminal is hung up 500 ms later, when the
  master closes.
- The PTY master's ioctls go through its raw conn instead of `Fd()`, which
  had put the master in blocking mode: a `Close` then waited for the read in
  flight instead of ending it, and a session could not be hung up while a
  background job held the terminal.

## What keeps the workspace awake

- **The rule** (the owner's decision, answering the first plan's "a detached
  session does not keep it awake"): a session is busy while any live
  process other than its shell runs in its session (session id = the
  shell's pid; zombies do not count). A foreground program, an agent's TUI
  waiting for its next prompt, vim left open, a background job: all busy.
  A shell at its prompt is not. **No time cap**: a forgotten program keeps
  the workspace up until someone closes the session, rebuilds or destroys
  the workspace. The tab shows the program (the terminal's foreground
  process group's `comm`, else a background job's) and a "keeps the
  workspace awake" marker, the close confirmation names it, and the
  workshop's idle-period help says so.
- **Not seen**: a process that left the shell's session (`setsid`, a
  double-forking daemon, containers started through `docker compose up -d`,
  which run under the docker daemon) keeps nothing awake, as before. A
  shell's own transient children (a prompt's `git status`) caught by a look
  count as one moment of use.
- **Pull, not push.** `SleepIdle` asks only a run that is already due and
  can sleep, through `engine.EnvKeepAwake` (implemented by
  `wsinfo.Service.TerminalsBusy`, which calls `GET /term/sessions`). The
  sidecar computes busy on that request from one `/proc` scan; nothing
  polls in the background, and the engine keeps no session state, so an
  engine restart loses nothing. A sidecar pushing "busy" would have needed
  a channel to the engine and state that survives its restarts.
- **A busy answer is a Touch.** It marks a moment of use, so the run is not
  due again for one idle period and is not asked every sweep. The price:
  after the program ends, the workspace may stay up for up to two idle
  periods.
- **The probe never pins a workspace up or stalls the sweep.** Each look
  has a 10 s deadline; any error is "not busy" (logged once per sweep as a
  warning) and the workspace stops as it did before. A provisioner without
  a sidecar, or a sidecar without the route, is "not busy" with no error.
  `envport.Sidecar` starts a sidecar when none serves: a fresh one has no
  sessions, so it answers not busy, one wasted start at most per sleep.

## An old sidecar

A sidecar started before this change ignores `?session=`, has no
`/term/sessions` (the engine answers 501) and so never keeps its workspace
awake. The pane detects it (no session frame on the socket, or 501 from the
list) and keeps the old single, throwaway terminal with a hint to restart
the workspace; it never shows a tab strip it cannot back.

## Tests

- Sessions, replay, sharing, close, refusals and busy over a real PTY:
  `workspace/shim/terminal_linux_test.go` (`TestSidecarTerminalSurvivesTheClient`,
  `TestSidecarTerminalSharedByTwo`, `TestSidecarTerminalDelete`,
  `TestSidecarTerminalRefusals`, `TestSidecarTerminalReplayIsBounded`,
  `TestSidecarTerminalBusy`, `TestSidecarTerminalInput`).
- The ring, the bookkeeping and cap, the slow client, the `/proc` parsing:
  `workspace/shim/termsession_test.go`.
- The engine's client and the proxy, a re-attach through it:
  `internal/workspace/sidecar` (`TestTerminalSessions`,
  `TestTerminalProxyRewrites`, `TestTerminalThroughTheProxy`).
- The sweep: `internal/engine/sleep_test.go`
  (`TestABusyTerminalKeepsAWorkspaceAwake`,
  `TestAFailedTerminalProbeLetsTheWorkspaceSleep`, the "terminal busy" row
  of `TestWhatKeepsAWorkspaceUp`); the probe:
  `internal/workspace/wsinfo` `TestTerminalsBusy`.
- The routes, the 501 and the permissions:
  `internal/httpapi` `TestTerminalSessionsThroughTheSidecar`,
  `TestSharedWorkshop`, `TestDemotedOwnerKeepsNothing`.
- The pane: `cpui/src/lib/run/Terminals.test.ts`, `Terminal.test.ts`,
  `terminalSessions.svelte.test.ts`, `views/WorkspaceView.test.ts`.
