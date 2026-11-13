# Kubernetes only, one chart, tunnels and the apothecary client (2026-11-11)

The plan is `docs/kubernetes-only-plan.md`. What was decided while building
it, and where it deviates:

- **The docker provisioner is gone.** That means its host
  (`workspace/host_docker.go`, `sandbox.go`), the docker browser and gateway
  hosts, the `docker` kind, `wsimage` (the engine-side image builds),
  `APOTHECARY_SANDBOX_RUNTIME`, the compose deployment and its tasks. The
  engine image drops docker, Node and the devcontainer CLI: they run in the
  sandbox pod, and the engine only runs `kubectl`.
- **No backward compatibility with the docker provisioner.** Nothing was
  ever deployed on it, so no migration and no special case: a `docker` kind
  is unknown like any other.
- **The `Host` seam stays.** The adapter's host-blind logic (definition,
  exec, sidecar, ports, editor, daemon-door decisions, egress orchestration)
  is tested on a test-only host (`testHost` in `fixtures_test.go`), which
  mirrors the pod host's prepare without a cluster.
- **The pin guards moved, not vanished.**
  - `TestShimDependencyPinsMatchGoMod` now reads the published shim
    Dockerfile (`workspace/embed_test.go`).
  - `TestToolsDockerfilePinsMatchKinds` lives in `internal/project/kinds`.
  - The embedded sources stay: the k8s host hashes them to detect a stale
    seeded shim or tools bundle.
- **`test:integration:harness` was not ported** (a deviation). It never used
  the docker provisioner; Docker is only its Linux test runner.
- **The sandbox shape has deployment defaults.** `APOTHECARY_K8S_RUNTIME_CLASS`
  and `APOTHECARY_K8S_PRIVILEGED` fill an integration's empty fields, and
  boot refuses privileged off without a runtime class. The daemon-door
  warning reads the deployment's setting. Sandbox placement is the
  RuntimeClass's own `scheduling` (sysbox's installer sets it up), not chart
  values or engine code.
- **Every account gets a Kubernetes provisioner** (`confstore.EnsureProvisioner`
  through `httpapi.Server.NewAccount`, wired only on Kubernetes). It runs
  when the instance is claimed and when an admin adds an account, and is a
  no-op for an account that has any provisioner. It is created at account
  time rather than first boot because integrations are per user.
- **One network surface: the engine's HTTPS address.**
  - Port tunnel: `GET /api/runs/{id}/ports/{port}/tunnel`.
  - SSH tunnel: `GET /api/ssh/tunnel?workspace=…`.
  - Both are WebSockets (`internal/platform/wstunnel`, on
    gorilla/websocket), because every ingress and load balancer passes a
    WebSocket and not all pass a bare upgrade.
  - Pings run on a timer to stay under 60-second idle timeouts. A failed
    ping write is how a dead peer shows.
  - Read deadlines are not used for liveness: gorilla answers pongs only
    while something reads, so they would kill an idle tunnel.
- **runports keeps one path per run and port** (`Dial`), because opening one
  starts a relay in the workspace. The path is dropped on teardown and on
  rebuild, and reopened once if it stops answering. Tunnels need no relay.
- **On Kubernetes without an apps domain the relay is off.** A listener per
  port would hand out URLs nothing reaches. `RunPorts.links` tells the UI,
  which then shows the forward command and no Expose button.
- **The SSH door has no port of its own on Kubernetes** (`APOTHECARY_SSH_LISTEN`
  is for the dev loop).
  - The CLI's `ssh-config` writes one wildcard block,
    `Host *.apothecary`, with `ProxyCommand apothecary ssh-proxy %h`.
  - It pins the door's single host key under one `HostKeyAlias`, with
    `StrictHostKeyChecking accept-new`. Trust on first use is acceptable
    here: it rides an authenticated TLS tunnel.
  - The tunnel names the workspace, so the door ignores the SSH username
    (`ServeConn`).
  - The door throttles failures by the API's user, because every tunnel
    shares the ingress's address.
  - The run page's SSH facts carry `setup`: the one-time command.
- **The terminal client** is `cmd/apothecary` (wiring only),
  `internal/client/api` (the one HTTP client) and `internal/client/cli`
  (thin commands, one printer).
  - archtest: the client imports only itself and `internal/platform`, and
    only the client and its binary import it.
  - `login` and `forward` were reworked the next day (below).
  - The commands use the standard `flag` package, as `apothecaryd` does. The
    TUI will be Bubble Tea, in the same binary, on the same `api` package;
    the dependency is not added yet.
- **The chart is `charts/apothecary`.**
  - Its schema refuses the removed keys (`ssh`, `ingress`, `service`,
    `engine.replicas`), unprivileged sandboxes without a runtime class, an
    ingress without a host, and an apps domain without the UI's address.
  - It runs one engine with `Recreate`.
  - `pullPolicy` is automatic: Always for latest/local/dev tags,
    IfNotPresent otherwise.
  - Examples in `ci/` are linted and rendered by `task lint:helm`, which also
    diffs the helm-docs README. helm-docs runs non-strict, because strict
    mode demands an entry for every parent object.
  - Nothing names a VPN product: the private example passes any ingress
    class through untouched.
- **Release.** The chart is pushed to `oci://ghcr.io/intradeus/charts`,
  stamped `X.Y.Z` / `vX.Y.Z`. The client is cross-built for
  Linux/macOS/Windows on amd64/arm64 and attached to the GitHub Release with
  `SHA256SUMS`.
- **Not done here:**
  - The k3d integration test that forwards a port and SSHs through the
    tunnel is not written yet.
  - The run-page preview pane and shareable app URLs are out of scope.
