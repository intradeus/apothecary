# Tools bundle (TODO 39 retired, 2026-10-27)

The harness CLIs and code-server no longer ride into repo-authored dev
containers as injected devcontainer features. They arrive the way the shim
does — apothecary-owned material the HOST mounts — so a future
`.apothecary/apothecary.json` definition gets them for free, and nothing of
apothecary's touches the repo's build any more (no features, no apt/npm/curl
inside the image build, no Node pulled into images that have none).

- **One image, `apothecary/tools:<hash>`** (`workspace/images/tools`, built
  with that directory as its context): opencode, codex, codex-acp and omp as
  standalone builds for both libcs, and code-server's standalone tree (glibc
  only: it bundles its own node and the project ships no musl build), laid
  out as `/tools/{gnu,musl}/bin`. 2.3 GB (omp is 276 MB per libc, codex 290
  MB, code-server's tree about a gigabyte). codex-acp has no prebuilt
  binaries, so it is compiled from the npm package with `bun build --compile`
  as its own package.json does — in a glibc bun image for the gnu variant and
  an Alpine one for musl, because bun on a musl host silently builds musl for
  an unsuffixed target (found live: both outputs had one BuildID and asked
  for ld-musl). It finds codex through `CODEX_PATH`, which the wrappers set
  to the bundled static codex; that pins `CodexVersion` at 0.160.1, what
  codex-acp 2.2.2 requires — the npm install had been running codex-acp
  against its own nested 0.160.x all along. x64 takes the `-baseline` builds
  (no AVX2 requirement, what opencode's own installer falls back to). The
  musl wrappers add the bundle's libstdc++/libgcc to `LD_LIBRARY_PATH` only
  when the image lacks its own. Verified on `debian:bookworm-slim` and
  `alpine:3.21`: every tool answers `--version`.
- **Versions are release pins:** the Dockerfile's ARG defaults, held to
  `kinds.{Opencode,Codex,CodexACP,Omp}Version` by
  `TestToolsDockerfilePinsMatchKinds`. The per-integration `version` /
  `acp_version` fields are gone (user decision): a CI-published bundle cannot
  honor them on Kubernetes, and one bundle per version set would be
  gigabytes each. `workspace.ToolsHash()` (`apothecaryd tools-hash`: the hash
  of the build directory) names the bundle everywhere — `TOOLS_HASH` inside
  it, the image tag, the cache dir.
- **Delivery, docker:** `wsimage.EnsureTools` builds and extracts to
  `<RunsDir>/tools-cache/<hash>` (create + cp, like the shim). The sandbox
  bind-mounts the PARENT dir read-only at its own path, so a Rebuild sees an
  upgrade's new bundle without a sandbox recreate — and so no bundle dir is
  ever swept here (a running workspace may execute from it; a sweep must
  first check what sandboxes mount; ~2 GB per engine version until then).
  `devcontainer up --mount` adds `/opt/apothecary/tools`; the CLI's mount
  parser keeps only type/source/target (verified in its dist), read-only
  comes from the sandbox's `:ro`. `cmdServe` warms the build at boot
  (`Resolver.WarmImages`, same Builder so its lock serialises with a
  provision) — the first build takes minutes. A sandbox from before the
  mount carries no `apothecary.sandbox.shape=2` label and is recreated on
  first use with its /var/lib/docker volume kept: the gateway's shape
  mechanism, because Rebuild and recovery re-enter `ensureSandbox` on an
  existing sandbox and docker refuses a bind whose source is missing.
- **Delivery, Kubernetes:** a `tools-seed` init container (the image's
  `seed.sh`) copies `/tools` onto the run volume once per bundle version
  (`TOOLS_HASH` equal ⇒ skip); `tools_image` on the integration
  (`APOTHECARY_TOOLS_IMAGE`, the sixth image of `deploy/images.sh` and the
  chart). The engine checks the seeded hash like the shim's but only warns
  on a mismatch — tool versions are not an ABI. Every first pod start copies
  the bundle onto the run PVC; image volumes (default-on since 1.35,
  containerd ≥ 2.1) would mount it instead, later.
- **Links, not PATH:** `rootInit` probes for musl's loader and `ln -sfn`s the
  matching `bin/` entries into `/opt/apothecary`, which every Exec'd PATH
  already has APPENDED — `command -v opencode`, the ACP launch and `omp
  auth-broker import` are untouched, and an image shipping its own harness
  still wins. Absolute container paths into the mount, so an engine upgrade
  needs no relink on Reattach; Rebuild relinks anyway.
- **The editor runs as the workspace's user, through Exec.** User
  requirement: the integrated terminal must be a real workspace shell. So
  `StartEditor` is one `devcontainer exec` script — remote user,
  containerEnv/remoteEnv, the probed login PATH plus /opt/apothecary, hence
  the repo's toolchain AND the harness CLIs in the terminal: settings
  re-applied, the extension union (the definition's own list through the
  new `Definition.EditorExtensions` ∪ `EditorSettings.Extensions`) installed
  into `/apothecary/code-server/extensions` whenever the marker holds
  another list, then a detached start (a bash background with every fd
  redirected; no nohup/setsid assumed) and the bounded wait. One exec where
  there were four (they count on Kubernetes). The old path ran `docker exec`
  as the image's default user, which could differ from `remoteUser`
  (settings and `$HOME/repo` in the wrong home). A musl image has no
  code-server link: the script exits 61, reported as "needs a glibc-based
  image". `EnvSpec.{HarnessInstall,CodeServerExtensions}` and
  `Harness.InstallLayer` are gone; the extension list rides `EditorSettings`.
- **Extensions come through the gateway** (user decision): `--gallery`
  serves an Open VSX reverse proxy on :5002 (`workspace/gateway/gallery.go`),
  reachable from every run like the mirror; `EXTENSIONS_GALLERY` points the
  run's code-server at it — the install step AND the editor's own
  marketplace UI, inside allow-mode projects too (gateway-originated fetches
  are the documented carve-out). Version-addressed responses
  (`/api/{ns}/{name}/{ver}/file/…`, `/vscode/asset|unpkg/…`) are cached for
  good under `<state>/gallery` and served from there first (a project's
  second run installs with no upstream fetch; an upstream outage does not
  matter); queries pass through, their absolute upstream URLs rewritten to
  the listener as the client addressed it; storage 302s (Azure blob) are
  followed on the gateway, `/vscode/item`'s is handed to the browser. The
  build-cache wipe clears it too. No gateway (`gwip` marker absent) ⇒ no
  override: code-server reaches Open VSX itself, egress being open. The
  gateway's address is a per-run marker (`gwip`, beside `gwproject`) so
  StartEditor finds it after an engine restart.
- **Residuals.** The image is big (two copies of each bun-built CLI plus
  code-server's tree); `-baseline` costs AVX2 users a little speed; the
  gallery cache has no size cap; an editor on musl images would need
  code-server in a sidecar container, which breaks terminal parity —
  rejected. Four engine model-check tests (`TestAgentAccepts`,
  `TestRunRefusedForAModelItsHarnessCannotReach`, …) were already failing
  on the tree this landed on; untouched.
