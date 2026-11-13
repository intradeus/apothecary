# Devcontainer egress firewall

The §11 default-deny egress firewall now covers repo-authored dev
containers. CLI gates verified live (@devcontainers/cli 0.86): a **relative**
`--additional-features './apothecary-firewall'` local ref resolves, and a
feature's `capAdd`+`entrypoint` apply on image, Dockerfile AND compose
configs.

- **Two mechanisms, one script.** Repos with no devcontainer use the §8
  pipeline image (which already carries iptables + init-firewall.sh + the
  sudoers grant): `writeFallbackConfig` adds `capAdd: [NET_ADMIN]`,
  `containerEnv` (the `APOTHECARY_*` inputs) and a BLOCKING
  `postStartCommand: sudo init-firewall.sh` (up waits on lifecycle hooks →
  no race). Repos WITH a devcontainer get an injected feature
  (`workspace/feature/`, materialized into
  `.devcontainer/apothecary-firewall/` + `.git/info/exclude`) whose
  container-start entrypoint sources `/opt/apothecary/firewall.env` (written
  per run into the shim mount — never feature options, which would bust the
  build cache) and runs the SAME `init-firewall.sh` verbatim. `waitReady`
  probes `/run/apothecary-firewall.done` when firewalled; fail-closed writes
  no lock, so a firewall failure times out into an honest `env_failed` with
  the container left up for inspection. Loopback is ACCEPTed first, so the
  unix-socket bridge/relay survive the DROP.
- **`ports.EgressOpen` marker → `ports.EgressReporter` (per-env method).**
  The firewall varies per env, so a type-level marker no longer fits: the
  devcontainer adapter firewalls image/Dockerfile configs but leaves
  **compose** open (its primary service reaches sibling services by name,
  which default-deny would break) and honors a per-project `firewall: off`
  switch. It records the fact in memory + an on-disk marker (survives engine
  restart) and reports it; `envcheck` keys its firewall invariants on the
  reported value (skips honestly for open envs), defaulting to closed when
  the capability is absent.
- **Limits:** compose stays egress-open (feature works, but firewalling
  would break sibling-service traffic — a future enhancement could allow the
  compose subnet); a non-root container user makes the entrypoint's iptables
  fail closed; a base image with no apt/apk/dnf fails the feature build (use
  the firewall-off escape hatch). Feature injection adds a first-provision
  image build (~apt install iptables), docker-layer-cached thereafter.

All three verified live (`-tags live`): the shim relay end-to-end on a
node-LESS `debian:bookworm-slim`; a full port-exposure round-trip
(portrelay → reverse relay → in-container → forward relay → engine); and the
firewall dropping foreign egress + DNS while loopback/relay stay alive.
