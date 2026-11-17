# The ports pane is one table and says open is web only

Why: an exposed port is an app-host URL served by an HTTP reverse proxy
(`internal/httpapi/apps.go`); it cannot carry Postgres or any other TCP
protocol, and the Ingress in front of it routes by `Host` header only. The
pane offered "expose and open" on every detected port, a bare copy glyph for
the forward that does reach any port, and two lists (detected, exposed). A
database port clicked open showed `EOF` and nothing else.

What now:

- **One table** in the Ports view: service (else process), port, status
  (`listening` / `exposed`), a labelled `forward` copy button (`CopyButton`
  takes an optional `label`), and `open` in the browser when the engine
  hands out links. Under it: forward is for any port, open for web apps
  only; then "open another port", then the two whole-workspace forwards.
- **The Services view uses the same table** (`run/PortTable.svelte`) for
  each service's declared ports: label, port, status (`exposed`, `up`,
  `not up`), forward, open. Its open stays the engine's preview link, shown
  once the service is healthy, declared path included; the in-workspace
  address moved to the port's tooltip. The forward and web-only hints show
  once under the list. Chosen over a `protocol` field on a service port: no
  schema change, and the two views behave alike; a database's port still
  gets a preview link, whose 502 now says to forward it.
- **No "forwarded" status.** The engine cannot know it: the client's daemon
  opens a tunnel per connection, so an idle forward leaves no trace on the
  engine. Showing it would need the client to report to the engine.
- **An exposed port keeps its name.** `GET /api/runs/{id}/ports` lists
  exposed ports as `RunPort` (was `portrelay.Exposure`) with the scanner's
  `process` / `service`, added, both optional; the client's `api.Port`
  already read them.
- **The app host's 502 names both causes and the forward.** The workspace
  relay accepts the connection and closes it when nothing listens, exactly
  as a database closes on an HTTP request, so the two read as `EOF` and the
  page cannot tell them apart; it names both and prints
  `apothecary forward <run> <port>`.

Tests: `PortTable.test.ts`, `PortsView.test.ts`, `ServicesView.test.ts`, `CopyButton.test.ts`, `mappers.test.ts`
(`toRunPorts`), `TestAppWorkspaceNotAnswering` and `TestExposeThroughARelay`
(`internal/httpapi/edges_test.go`).
