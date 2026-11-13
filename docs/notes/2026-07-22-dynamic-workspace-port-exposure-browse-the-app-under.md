# Dynamic workspace-port exposure (browse the app under development)

A human can publish a port the workspace app listens on and open it in a
browser; N concurrent runs, zero fixed host ports. New optional
`ports.WorkspacePortExposer { ExposePort, AppPorts }` returning a
`ports.PortConn` (dial + close), plus `internal/portrelay.Manager` (one
kernel-allocated engine-host listener per (run, port), pumping browser
conns through the PortConn). Applied to BOTH provisioners.

- **Ownership is the engine's, not the adapter's.** Adapters are
  refcount-closed on config edits (`internal/resolve`), so a forward
  process parented in an adapter instance could be killed by an unrelated
  edit — the capability hands the engine a `PortConn` it owns for the run's
  life.
- **Never re-emit `env_ready` for dynamic links.** `run.Apply` would clobber
  `WaitingOnHuman`/`Paused` → `Running` (durable corruption), and an
  out-of-band append races the run loop's single-writer seq. Live exposures
  live in an in-memory registry served by `GET /api/runs/{id}/ports`; only
  provision-time auto-exposures (declared `forwardPorts`) fold into the
  single `env_ready` (appended on the writer goroutine). The CPUI merges the
  live registry over the event's `app` links (its 2.5 s poll), so restarts —
  which re-expose on fresh ports via the recovery path — stay correct.
- devcontainer: `shim relay --reverse` over a bind-mounted unix
  socket (host→container IP is blocked on hardened hosts, so the reverse
  relay mirrors the engine bridge). The dead container-IP `app` links the
  devcontainer adapter used to emit are gone — unreachable off-host.
- API `POST /api/runs/{id}/expose-port`; `cmd/apothecaryd` binds listeners
  on the CPUI's own interface (loopback by default; the deploy already runs
  `network_mode: host`). Same no-auth trust level as the CPUI itself —
  documented. This SUPERSEDES the earlier appPort/compose-collision
  follow-up: exposure needs no published host ports.
