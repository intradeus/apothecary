# The devcontainer build cache

A run's inner daemon is thrown away with the run — `sandboxVolume` is keyed by
the RUN name, so `/var/lib/docker` (image store and buildkit cache alike) is
empty every single time. The per-project registry mirror on the gateway saved
the network round trip, but every run still extracted every layer and rebuilt
the devcontainer image from nothing. Measured on a real provision: **76.8s of a
89.8s devcontainer-CLI span was `docker compose build`**.

The cache now outlives the run in the one place that already does: the project
gateway. It gains a SECOND `registry serve` child on :5001 — writable, its own
storage volume — because the mirror on :5000 is in proxy mode and a registry in
proxy mode refuses pushes. The sandbox's dockerd trusts both
(`--insecure-registry`), and the build reads and writes through them.

### Two mechanisms, because the CLI has two

`devcontainer up` takes `--cache-from` and `--cache-to`, but they are not
equally supported, and the difference is invisible until you read the CLI's own
source:

- **`:cache`** — a buildkit cache manifest (`--cache-to ...,mode=max`, so
  intermediate stages too). Works for a single-container devcontainer. For a
  compose devcontainer the CLI refuses it outright ("--cache-to not
  supported.", its words); in `up` it is silently ignored, which is how the
  first attempt here looked like it worked while exporting nothing.
- **`:image`** — the built image itself, pushed after the build
  (`pushBuildCache`). The CLI always builds with `BUILDKIT_INLINE_CACHE=1` —
  including in the compose override file it generates — so the pushed image
  carries the metadata `--cache-from` restores from. This is the only export
  path a compose devcontainer has, and compose devcontainers are the common
  case here.

Both refs are offered as `--cache-from` every time; a missing one is a 404
buildkit shrugs at.

Three properties make it safe rather than clever:

- **`ignore-error=true`** on the export. A wiped, restarting or wedged cache
  registry costs a cache miss, never a failed build (verified directly: the
  export error is swallowed and the build exits 0). `pushBuildCache` is
  best-effort for the same reason — a failed push means a slower next build and
  nothing else.
- **Cache export needs a driver that supports it.** The default docker driver
  refuses ("Cache export is not supported for the docker driver") *unless* the
  containerd image store is on — and `docker:29-dind`, which the sandbox runs,
  has it on by default. Verified inside the sandbox image before building
  anything on top of it.
- **No gateway, no flags.** A provisioner without one behaves exactly as before
  rather than pointing buildkit at a registry that does not exist.

### Clearing it

`DELETE /api/projects/{id}/build-cache`, surfaced on the project page beside
its size. Empty-then-restart, in that order and both parts required: the wipe
removes the storage under the registry, and touching the config makes the
gateway's supervisor restart the child — a registry left serving manifests
whose blobs are gone would hand out cache entries that fail to pull, which is
worse than no cache.

It is a safe button by construction: the next build repopulates it, so the cost
of clearing needlessly is one slow build. That is what the confirmation says.

### The plumbing

`ports.BuildCacheController` is an optional EnvProvisioner capability (the same
shape as `EgressController`), so a provisioner that caches elsewhere — or not
at all — simply does not implement it and the API answers 503. One trap worth
recording: the gateway is keyed by project NAME, not id, because that is what
`Provision` passes it. Passing the id reads as "no cache" — `available:false`
on a project holding 800MB of cache, which is exactly how it was found.

### Rebuilding without it

A checkbox beside "Rebuild workspace" (`no_cache` on the rebuild endpoint →
`EnvSpec.NoCache` → `--build-no-cache`). Deliberately manual, and deliberately
NOT a config-change detector: buildkit's cache key already hashes what the
build actually reads — the resolved base-image digest, every instruction, every
context file — so a changed Dockerfile, devcontainer.json or compose build
config rebuilds itself. A detector of our own would hash what we GUESS it reads,
which is both weaker (a compose file outside the .devcontainer folder) and
noisier (a comment edit forcing 77s).

What a cache key genuinely cannot see is time: a floating `FROM node:22` whose
tag moved, an `apt-get update` layer from three months ago. No config changed,
so no detector would fire — a human pressing the button is the honest interface.

Two things make the checkbox true rather than decorative: `--no-cache` beats
`--cache-from` in buildkit (measured in a scratch daemon: 1s warm vs 32s with
--no-cache against the same populated registry), and the CLI drops its own
`--cache-from` when the flag is set. `--cache-to` and the push still run, so
ONE no-cache build repairs the cache for every run after it.

Worth recording next to this: the pull-through mirror's `proxy.ttl` is unset,
and the distribution default is 168h. For a floating tag that is up to a week
of staleness BEFORE the build cache is involved at all — buildkit resolves FROM
against the mirror, and a fresh sandbox has no local image to resolve from.
Tuning that is a one-line change nobody has made deliberately yet.

### What it costs on disk

~800MB per project for the greeter test project, and it is per project by
design. That is real, and it lands on the same disk as the sandbox volumes and
the image mirror: the box this was measured on hit 100% full during the A/B.
The cache is the cheapest of those to give back (one button, one slow build),
but a retention policy for build caches — or at least a size shown next to the
other reclaimables — is the obvious next piece of work.
