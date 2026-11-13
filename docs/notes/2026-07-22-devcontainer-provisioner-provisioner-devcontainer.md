# Devcontainer provisioner (provisioner/devcontainer)

The TODO called for a DevPod adapter; research (2026-07-21, verified against
GitHub) found loft-sh/devpod abandoned-in-fact (no development since 2025-06,
v0.7 never shipped, maintainers silent) and the niche's only maintained
piece being the reference @devcontainers/cli. Decision: the repo-devcontainer goal ("run whatever the
target repo defines") is implemented on **@devcontainers/cli + docker CLI**
(`internal/adapters/devcontainer`).

Decisions and deviations:

- **Clone-first inversion.** The workspace definition lives in the repo, so
  the adapter materializes an engine-side checkout before `devcontainer up`
  (bind-mounted as the workspace folder). `ports.EnvSpec` gained
  `GitUser/GitPass` for exactly this: the credential rides a temp
  `credential.helper=store` file (never argv, never the container); the
  in-workspace credential entry is still bootstrap step 3's. The §11
  `$HOME/repo` contract is preserved via a root-init symlink onto the
  mounted folder, so bootstrap's idempotent clone step no-ops unchanged.
- **Repos without a devcontainer** fall back to a generated config around
  the §8 pipeline image (`--override-config`); repos with one are honored
  untouched. Custom `workspace.template` is rejected at kind construction.
- **Exec transport** is `devcontainer exec` (script on stdin): exit codes
  propagate verbatim and stdout/stderr stay separated — no exit-255
  ambiguity. Container state lives in docker labels (`apothecary.env=<ws>`);
  status/destroy/links go through `docker ps`/`rm`/`inspect`.
- **Engine bridge instead of tunnels.** Hardened docker hosts drop
  container→host IP traffic entirely (verified live: even a bare listener on
  the bridge gateway IP is unreachable), so workspace→engine rides
  docker-sanctioned channels only: engine-side unix listeners in the
  bind-mounted `/opt/apothecary` pump to the engine's loopback, and a ~30
  line `relay.mjs` (`docker exec -d node …`) re-exposes them on the
  workspace's own `127.0.0.1`. Loopback parity holds, so run.env/proxy.env
  are provisioner-invariant and the engine needed no URL-rewriting surface.
- **node is a hard workspace requirement** (shim + relay; harnesses are
  npm-distributed anyway). Missing node fails Provision with guidance to add
  `ghcr.io/devcontainers/features/node`. Auto-injecting the feature was
  rejected for MVP: it could shadow the repo's own node version.
- **Security posture (deliberate):** repo-authored containers get no
  NET_ADMIN and no apothecary firewall — egress is OPEN apart from
  engine-side proxy filtering of proxy-honoring tools. The
  `ports.EgressOpen` marker declares this; envcheck skips the firewall
  invariants with an honest reason instead of false-failing, and the tunnel
  probe stays valid (it now validates the relay). Kernel-level enforcement
  (injected devcontainer feature contributing capAdd + firewall, or an
  internal-network egress sidecar) is the recorded follow-up.
- **Concurrent runs and declared ports:** headless @devcontainers/cli does
  not publish `forwardPorts` (an IDE-session feature), so N parallel
  workspaces cost zero host ports; declared ports surface as `app` links on
  the container's own IP (per-instance unique, engine-host reachable).
  `appPort` and fixed compose `ports:` DO publish and can collide across
  concurrent runs of the same repo — the second `devcontainer up` fails
  loudly; remapping is a follow-up.
- **Known gaps:** harness binaries are not installed into repo-authored
  devcontainers (InstallLayer belongs to the image pipeline) — the repo's
  devcontainer must provide them for agent stages; compose side services
  are not reaped by Destroy (unlabeled by the CLI); `devcontainer stop`
  does not exist upstream, so there is no adapter-initiated stop (Destroy
  only), matching the port's surface.

Verified live against @devcontainers/cli 0.86.0 + docker 29.5 (unit fakes +
a `-tags live` end-to-end test: provision from a real repo, exec, file
round-trips, loopback engine probe through the relay, links, destroy).
