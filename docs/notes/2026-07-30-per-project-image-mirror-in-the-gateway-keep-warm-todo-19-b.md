# Per-project image mirror in the gateway + keep-warm (TODO 19 b+e)

Every run's inner dockerd used to cold-pull everything (repo base image,
feature layers, compose siblings) into its own per-run `/var/lib/docker`
volume, which dies with the run. The gateway is already per-project and
already multi-homed onto every run's `--internal` network, so it is the one
place a shared cache can sit with **no new networking**: its nft ruleset only
hooks `prerouting`/`forward`, so a gateway-local listener is reachable from
every run and the cache's own upstream fetch (gateway-originated, `output`
path) needs no rule either.

- **The mirror runs INSIDE `apo-gw-<project>`**, not as a sibling container:
  the distribution `registry` binary is COPYed into the gateway image from
  `registry:2` (digest-pinned — `gatewayHash` covers the Dockerfile *text*,
  not upstream content, so a re-tag would otherwise change the image without
  changing the tag) and supervised as a child by the gateway daemon
  (`workspace/gateway/registry.go`, stdlib `os/exec`: capped backoff on exit,
  SIGTERM-restart when the config file changes since the registry has no
  config hot-reload, child logs relayed under a `registry:` prefix). The
  `--mirror-config` flag is opt-in, so a gateway without it behaves exactly
  as before.
- **Cache = named volume `apo-gw-<project>-cache`** at `/var/lib/registry`,
  deliberately NOT under `gwDir` (which the reap used to delete). Config
  (`registry-config.yml`, `proxy.remoteurl` = Docker Hub) rides the existing
  identical-path gwDir bind mount, written only when the content changed so
  a provision doesn't restart the registry child on every pass.
- **Inner dockerd** gets `--registry-mirror=http://<gwIP>:5000
  --insecure-registry=<gwIP>:5000` (plain HTTP inside the `--internal` net).
  `--registry-mirror` applies only to docker.io, matching the single-upstream
  proxy; a down mirror is not a failure mode (dockerd falls back to upstream).
  `GatewayOff`/gatewayless runs get no flags.
- **Keep-warm (e):** the refcount reap in `DestroyRunEgress` is gone —
  destroying a project's last run keeps the gateway, its policy dir and its
  cache — plus `--restart unless-stopped` so it survives daemon restarts (a
  restarted container is a fresh netns, so stale nft rules are impossible).
- **Upgrade path replaces the reap.** The reap used to be how a new gateway
  build shipped. `ensureGateway` now resolves the image tag on *every* path
  (the warm path skipped it before) and recreates a gateway whose
  `.Config.Image` is stale — but only while the project has no *running*
  sandbox, since `rm -f` would sever egress for every attached run. Stopped
  sandboxes baked `--dns`/`--registry-mirror` at the old gwIP and can't be
  re-flagged by `docker start`, so the replacement is re-attached to each run
  network at its previous address (`network connect --ip`). Only the image
  tag is compared: argv changes here don't trigger a recreate on their own,
  but they always ride a source/Dockerfile change that bumps the tag.
- **Accepted carve-out:** cache misses are fetched by the gateway itself, so
  they bypass the SNI/Host policy — a project denying `registry-1.docker.io`
  can still pull public Hub images through the mirror. Documented in README;
  folding Hub auth into (g) credential brokering is the future step (the
  registry `proxy` block takes username/password).
- **Cache eviction:** `registry:2` proxy TTL is hardcoded at 168h and
  historically imperfect; `storage.delete.enabled: true` is what lets it
  evict at all. Blessed cleanup is dropping the volume (pure cache, recreated
  on the next ensure) rather than running `garbage-collect`. `registry:3`
  with a configurable `proxy.ttl` is the upgrade if it ever matters.

Verified live end-to-end: run 1 cold-pulls `alpine:3.20` through the mirror
(blobs land in the volume, `/v2/_catalog` lists `library/alpine`); the run and
its volume are destroyed; run 2 pulls the same tag in ~2s **with
`registry-1.docker.io` denied by policy** — proving both the cross-run warm
cache and the carve-out, while the sandbox's own direct `wget` to Hub is reset
and recorded as a denial. `pkill registry` in the gateway is restarted by the
supervisor within a second; touching the config restarts the child.

### (h) assessed and dropped — the relay stays

Routing the engine↔workspace relay through the gateway was considered and
rejected. The forward half (workspace→engine) would work — the sandbox
already routes to gwIP, and a gateway listener could hand off to a unix
socket on the gwDir mount, collapsing N per-run relays into one per-project
hop — but the socket seam remains either way (container→host-IP is blocked on
hardened hosts), so it removes nothing structural. The reverse half
(engine→workspace port exposure) is the blocker: inner containers sit behind
the inner dockerd's NAT on subnets that collide across runs and that the
gateway has no route to, which is precisely why `ExposePort` runs the relay
*inside* the workspace container. And `firewall: off` runs have no gateway at
all, so the unix path would have to be kept as a fallback regardless.
