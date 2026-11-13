# Opt-in unprivileged DinD sandbox (TODO 19 c)

`APOTHECARY_SANDBOX_RUNTIME` (deployment env, §1) names the OCI runtime each
run's DinD sandbox is created with. Unset — the default — is byte-identical to
before: `docker run --privileged docker:29-dind`. Set to `sysbox-runc` (sysbox
installed on the **host**) and `createSandbox` emits `--runtime` *instead of*
`--privileged`; the two are mutually exclusive, since sysbox rejects privileged
containers and keeping the flag would defeat the point.

What it changes, concretely: `--privileged` gives the sandbox all host devices,
unmasked `/proc`+`/sys` and no seccomp, so sandbox root **is** host root — and
the paths there are ordinary, not exotic: a repo's `devcontainer.json`
`runArgs` or a compose `privileged: true` run verbatim by design (see the
`--id-label` comment in devcontainer.go), and `Rebuild` re-provisions from the
checkout the agent has been writing to all run. From host root everything
follows: every other run's folder under `RunsDir`, the mounted host docker
socket, and `APOTHECARY_MASTER_KEY` out of `docker inspect` — i.e. every
integration secret. Under a user-namespace runtime the same compromise yields
root in a userns mapped to an unprivileged host uid, with only that run's own
folder bind-mounted: the blast radius becomes what the run already legitimately
holds. Egress is unaffected either way — enforcement was always outside the
sandbox.

**Opt-in, never auto-detected.** Auto-detection would make a run's isolation a
property of whatever happened to be installed on the machine; one env var makes
it readable off the deployment. The cost is that a typo would otherwise surface
minutes into a provision as docker's bare "Unknown runtime specified", so the
adapter probes `docker info --format '{{json .Runtimes}}'` before creating
anything and fails with the variable name plus the runtimes the daemon actually
has. Only success is cached (`runtimeOK`) — a transient docker blip must not
wedge the provisioner for the process lifetime. `Test()` runs the same probe,
so the integration's Test button is where a missing host install shows up.

**Limits (researched, not assumed):** sysbox is Linux-only — it installs
systemd units on the host, so it cannot go into Docker Desktop's VM; macOS and
Windows keep the privileged sandbox (Docker Desktop *Business* gets the same
technology transparently via Enhanced Container Isolation, which is not
something we can drive). Official packages are Ubuntu/Debian, with
Fedora/Rocky/Alma/CentOS/Amazon Linux listed compatible and everything else
building from source; kernel ≥5.12. Sysbox's own docs steer away from
`docker:dind` images (breakages at dind 20.10.7 and 27.4.0; v0.7.0 added
openat2 trapping so newer runc works inside), which left `docker:29-dind`
under sysbox as the one thing to verify live — done below.

**Verified live (2026-10-19) and the default decision.** Host: Arch, kernel
7.2.11, docker 29.9.1, sysbox-ce 0.7.1 (registered in daemon.json with
`--no-kernel-check`, Arch being an unlisted distro), sandbox image
`docker:29-dind` (dockerd 29.6.2 inside). Daemon level, mirroring
`createSandbox` by hand: the inner dockerd boots in ~1s (privileged: ~0.7s)
and listens on the parity socket; the sandbox's init is host uid 165536
(`uid_map 0 165536 65536`) with a 16-entry `/dev` where the privileged
sandbox sees every host device; the parity bind mount is an ID-mapped mount,
so the inner socket surfaces on the host as `root:2375 0660` and inner-root
writes land as host root — byte-identical to the privileged sandbox, which is
why the engine (root in deploy/) needs no change and why a uid-1000 developer
cannot talk to the inner socket under either runtime. Inner pulls, bind mounts
of the run folder, buildkit builds, the egress `ip route replace` + ipv6
sysctl, an inner `--privileged` container (full CapEff but still inside the
userns: sysrq read-only, the same 16 devices) and a depth-2 dind all work.
Provisioner level: `TestLiveLifecycle` (`-tags live`) passes end-to-end under
`sysbox-runc`, without and with the egress gateway (--internal network,
gateway DNS, pull-through mirror, default route via the gateway): provision →
exec → file round-trips → loopback bridge → port exposure → destroy, with the
sandbox inspected as `privileged=false runtime=sysbox-runc`. The live test
had rotted since the DinD move (stale `wsimage.New` signature; `t.TempDir()`
RunsDir, which the dind guard rightly rejects; a host-daemon `docker exec`
against a workspace that now lives on the inner daemon) — it is repaired and
now takes its RunsDir, sandbox runtime and gateway switch from the
environment, skips unless root, and asserts the sandbox posture; the recipe
to run it inside the engine image is in its header.

Decision: it **stays opt-in**. It cannot become the default because it is a
host-level install we do not ship (Linux-only, distro packaging, a daemon.json
entry), and auto-detection was rejected above so a deployment's isolation
stays readable off one variable. What changes is the recommendation: on a
Linux host, set it. TODO 19 is closed.
