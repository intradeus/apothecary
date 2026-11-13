# The daemon door: docker in workspaces (2026-11-09)

Why: a repo whose tests need a docker daemon — this one (`pgtest` on
testcontainers-go, Ryuk included), any `docker compose` from an agent — had
no way to reach one inside a run. Compose siblings are started *from outside*
by `devcontainer up` (engine-side, `DOCKER_HOST` at the run's parity socket);
the workspace container itself held no socket, and the standard
`docker-outside-of-docker` feature failed late and cryptically on its bind
("source path does not exist").

- **Mechanism.** The sandbox's dockerd gets a second `--host`, its default
  `unix:///var/run/docker.sock` (`sandbox.go`). The dind image is Alpine
  (`/var/run → /run`), so the listener lands at `/run/docker.sock` and the
  feature's bind source resolves through the symlink. Explicit `dockerd` as
  arg1 keeps the entrypoint from adding its tcp listener. Kubernetes pods
  always had the default socket (`apo-dockerd`).
- **The setting.** `APOTHECARY_WORKSPACE_DOCKER=auto|on|off`
  (`config/env.go` → `integrations.New` → `kinds.ProvisionerDeps` → adapter
  `Config`, beside the runtime). `Adapter.doorOpen`: `on`/`off` are the
  operator's word; `auto` (default) is `Host.unprivileged()` — the runtime on
  docker, a runtime class with privileged off on Kubernetes. Why `auto` ties
  to privilege: under `--privileged` the sandbox daemon can start a
  privileged container, i.e. host root, so that hand-over is the operator's
  explicit call (a laptop running its own repos: fine; a shared server:
  sysbox); under a userns runtime the sandbox is the boundary already and its
  daemon adds nothing the run does not hold. `on` under privileged logs one
  boot WARN. A bad value refuses to boot.
- **Shape.** `apothecary.sandbox.shape=2` closed, `3` open — the shape follows
  `doorOpen`, so opening the door (by setting or by runtime) recreates an
  existing sandbox on its next Rebuild/recovery, and a closed-door sandbox is
  byte-identical to before. Side effect: flipping the runtime under `auto`
  now recreates too (before, the shape was constant and a sandbox could
  outlive a runtime change).
- **The refusal.** `Adapter.checkDaemonDoor`, in `provision()`'s detect
  callback — as soon as the checkout exists, before shim, tools, gateway and
  sandbox on docker; after the pod and clone on Kubernetes (the checkout
  lives there). `devcontainerDef.SandboxDaemonUse` reads the raw JSONC (no
  CLI call) and any referenced compose file: the feature (any registry, any
  tag, the old `docker-from-docker` name), a `mounts` or `runArgs` bind whose
  *source* is `/var/run/docker.sock` or `/run/docker.sock`, a compose volume
  of it. Deliberately never `privileged`, `runArgs --privileged` or
  `docker-in-docker` (the documented verbatim stance), nor a bind of some
  other source. Best-effort: a miss costs docker's own late error, never a
  false refusal. The message names what asked and both ways out; with `off`
  it says the setting is off. One policy on both hosts: a privileged pod under
  `auto` refuses too, although its socket physically exists (residual below).
- **The project's setting.** `WorkspaceConfig.Docker` ("Docker in
  workspaces", Workshop → Settings → Workspace) → `Resolution.WorkspaceDocker`
  → `EnvSpec.Docker` (the three builders: provision, rebuild, envcheck). In
  `provision()`'s detect callback it is an ask like the repo's declaration —
  the same refusal when the door is closed, naming the setting — and, when
  the repo declares nothing itself, `upOptions.docker` makes `Up` pass
  `--additional-features {"<pinned docker-outside-of-docker>":{}}`: the CLI's
  own way to extend a build without touching the repo's files, and the same
  feature the repo would declare for VS Code (install, socket proxy, remote
  user permissions all handled by it; image, Dockerfile and compose configs
  alike; both hosts). Never injected on top of a repo's own declaration (the
  feature would install twice). Chosen over a sandbox-wide default (every run
  would get a daemon it did not ask for — under `on`, every run host-root-
  capable) and over shipping a docker CLI in the tools bundle (a second
  mechanism, socket permissions for arbitrary users, a bundle rebuild).
- **Non-root remote users (found on the way).** `ensureSrc` clones as the
  engine's uid (root in the image) and bind-mounts; the CLI's
  `updateRemoteUserUID` does nothing when the CLI runs as root and never for
  compose; `rootInit` chowned only `/apothecary`. So any `remoteUser` other
  than root met a root-owned tree at its own hooks and at bootstrap's `git
  switch`. Now `up` runs with `--skip-post-create --skip-post-attach`,
  `rootInit` does `chown -R <user>: <workspaceFolder>`, then
  `devcontainerDef.RunUserCommands` (`devcontainer run-user-commands`, the
  CLI's own marker files keep once-per-container semantics) runs the hooks.
  Same place in the sequence as before — before bootstrap — on an owned tree.
  Host-side git is only the clone, so the chown changes nothing engine-side.
- **Testcontainers notes.** Inside the workspace `DOCKER_HOST` is unset and
  the feature's socat serves `/var/run/docker.sock`; testcontainers-go
  (v0.44) detects it runs in a container and uses the gateway IP of the
  daemon's default network for published ports — the sandbox bridge, reachable
  from the workspace; Ryuk binds `/var/run/docker.sock`, which now exists in
  the sandbox. No `TESTCONTAINERS_*` override. Containers it starts are
  siblings of the workspace on the run's daemon: destroyed with the run,
  egressing through the gateway. The feature's CLI install dials
  `download.docker.com` (`moby: false`, under `*.docker.com`; listed
  explicitly in the egress defaults now) or `packages.microsoft.com`.
- **Residuals.** (a) The parity socket `<RunsDir>/<run>/docker.sock` is
  bind-mountable by a `mounts` entry using `${localWorkspaceFolder}/../docker.sock`
  on any runtime — pre-existing, unchanged, and under privileged equal to what
  `runArgs --privileged` already grants. (b) Privileged Kubernetes pods keep
  the default socket whatever the setting (`apo-dockerd`): the refusal is
  policy there, not a physical barrier, until a relocation (an
  `APO_DOCKER_SOCK` env for `apo-dockerd` + `DOCKER_HOST` in the sandbox
  container's env; needs a sandbox-image version gate and a publish).
  (c) A plain socket mount without the feature yields a `root:docker 0660`
  socket; a non-root `remoteUser` needs the feature's socat proxy or a
  matching gid — the repo's concern. (d) Engine preflight does not see the
  door: refusing at run creation would need a `RepoHost` content read (three
  implementations), the door state through `resolution.Resolution`, the
  scanner in a shared package and `PreflightProject`; the adapter refusal
  already fires before anything expensive, so deferred.
- **Live.** `TestLiveLifecycle` asserts the socket exists iff the door is
  open; `TestLiveDaemonDoor` provisions a docker-outside-of-docker repo and
  drives the daemon (nested socket mount, as Ryuk does; under sysbox the
  shared tools cache stays read-only to its containers) or asserts the
  refusal; `TestLiveNonRootUser` the ownership hand-over.
  `test:integration:docker` forwards `APOTHECARY_SANDBOX_RUNTIME` and
  `APOTHECARY_WORKSPACE_DOCKER` into the test container — note the
  Taskfile's `dotenv` also applies `deploy/.env`, so "unset" through the
  task means "whatever .env says"; the privileged cells below were run with
  the variables forced empty on the `docker run` directly. Verified live on
  2026-11-09 (Arch, kernel 7.2, docker 29.9.1 host / 29.6.2 inner, sysbox-ce
  0.7.1, gateway on): sysbox + auto → posture `false sysbox-runc`, socket
  present, DooD repo drives the daemon (nested socket mount, `door-ok`), the
  tools cache mount seen as `ext4 ro,relatime,idmapped` with remount and
  write both refused; privileged + auto → posture `true runc`, no socket,
  the DooD repo refused before anything existed; privileged + on → socket
  present, daemon driven; `TestLiveNonRootUser` green in every cell;
  `TestLiveProjectDocker` (sysbox + auto): a repo declaring nothing, the
  project's setting on → the feature injected, `docker run` through the door.
