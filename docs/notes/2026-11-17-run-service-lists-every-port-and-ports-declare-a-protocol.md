# run.service lists every port, and ports declare a protocol

Why: `run.service.<name>` handed an agent the address of the service's first
declared port, and a gate its first port's preview link. A service serving a
front end, a back office and a landing page gave the photographer one of
them, whichever was declared first; the agent could not choose the page that
shows the change. And every declared port was taken for a web app: a
database's got a preview link that cannot work (the link is a web proxy) and
an `http://localhost:5432/` address.

What now:

- **A service port declares a protocol**: `http` (the default; an empty
  field is http, so stored projects are unchanged) or `tcp`. A tcp port takes
  no path (refused), is never exposed (`services.Expose` skips it), and its
  address is `localhost:<port>`. The Workshop's services editor picks it per
  port; the run page's Services table offers a tcp port forward only.
- **run.service.<name> is every port**, one line each, `- <label> -
  <address>` (an unlabelled port is `port <n>`, a tcp one marked `(tcp)`):
  - an agent gets each address in the workspace, so it picks the page
    (`internal/engine/service.go`, `serviceInput`);
  - a gate keeps one link — the first web port's preview link, path
    included — and lists every port in its context: a web port's preview
    link, else its `apothecary forward <workspace> <port>` (a tcp port, or a
    web one on an engine without browser links). This is how a gate over a
    multi-repo run lists every merge request; the ping's shape is unchanged.
    The list is left out when the one link says it all (a single web port).
- `service_ready` keeps the first port's address: it records that the
  service came up, not what to open.
- The builder's photographer is told its `app` input lists every page.

Tests: `TestServiceInputSaysWhatItKnows` (`internal/engine/faults_test.go`:
the agent's lines, the gate's, a gate with no service),
`TestChainEndStopsStageServicesOnly` (`service_test.go`: a tcp port first, the
gate's link the first web port's, every port in its context),
`TestExposeHelper` (`internal/workspace/services`), the confstore validation
cases (`services_test.go`), `resolution_test.go`, and the CPUI's
`ServicesEditor.test.ts`, `ServicesView.test.ts`, `mappers.test.ts`,
`index.test.ts`.
