# Ticket: the `plain` runtime and `.apothecary/startup.json`

Status: open, not started (2026-11-23). This is the write-up of a spike:
the sandbox-runtimes work (`docs/sandbox-runtimes-plan.md`, built
2026-11-22) designed for these two pieces and left them out. Nothing here
is built. The design below is a starting point, and the open questions are
the owner's to settle before building. Read `AGENTS.md` first.

## The idea

Every sandbox today nests a docker daemon, because the only workspace
definition, `devcontainer`, runs the workspace as a container on that
daemon. Nesting a daemon is the only reason a sandbox needs `privileged`,
sysbox or Kata at all.

Two pieces remove that cost for projects that don't need docker:

- **`startup`, a second workspace definition.** A repository describes its
  workspace in `.apothecary/startup.json`: an image, setup commands, ports,
  and what it needs. It is inspired by `devcontainer.json`, without docker:
  the workspace is the sandbox container itself, not a container inside it.
- **`plain`, a fourth runtime.** An ordinary unprivileged pod in a user
  namespace of its own: no docker daemon, no VM, no sysbox. It works on any
  node and shares memory with the node like any container. It is the
  cheapest runtime, and only useful once `startup` exists, because
  `devcontainer` cannot run on it.

The aim: most web projects (a frontend, an API, a library, a CLI) run on
`plain` with `startup`. Sysbox and Kata stay for projects that need docker,
nested Kubernetes or a kernel of their own. `devcontainer` stays too: it is
widely adopted and makes onboarding easy.

## Who installs docker vs. who can run it

These are two separate questions, and the design turns on the second.
Anyone can install docker or k3d in any sandbox, but a daemon only runs
where the runtime allows nested containers. A `plain` pod cannot run one,
whoever installs it. So a project that launches itself with k3d (the
owner's own projects are moving that way) still needs sysbox or Kata, even
with `startup.json`. It needs Kata if it uses user-namespaced pods.
`startup.json` must therefore declare what the project needs, and the
engine checks that against the runtime. It does not pick the runtime from
the definition.

## What already exists (the seam)

Phase 1 of the runtimes plan built the seam for this.

- **Runtimes** are one table:
  - `envport.RuntimeCapabilities` in `internal/ports/envport/runtime.go`:
    NestedContainers, NestedKubernetes, UserNamespaces, Contained.
  - Their pod traits in `internal/adapters/workspace/runtime.go`: class,
    privileged, ownUserNamespace, vm.
  - `plain` is a reserved name (plan, decision 3) with no row yet.
- **Definitions**: `Definition` in `internal/adapters/workspace/definition.go`.
  - It has `Name`, `Needs() envport.RuntimeCapabilities`, `Detect` (first
    match wins), `Up`, `RunUserCommands` and `SandboxDaemonUse`.
  - `devcontainer` (`def_devcontainer.go`) needs NestedContainers.
  - A runtime that cannot give what a definition needs is refused before
    anything is built.
- **Runtime choice**: a workshop picks from the provisioner's approved list
  (`RuntimeChoice.Choose`). Sizes, stop-on-idle and sleep refusals are
  per runtime (`NoSleepUnder`).
- **The pod**, under sysbox for example (golden
  `testdata/pods/sysbox-pvc-gateway.json`):
  - init containers `shim-seed` and `tools-seed` copy the shim and the tools
    bundle into the run folder;
  - the restartable init containers are `wg`, the WireGuard tunnel to the
    run's egress gateway with `NET_ADMIN`, and `relay`;
  - the `sandbox` container runs `apo-dockerd`;
  - the run's claim is mounted with subPaths, `run` and `docker`.
  - The surfaces (terminal and SSH, editor, exposed ports, browser) reach
    the shim in the workspace.

## Capabilities, as designed

`plain`'s values come from general knowledge of user-namespaced pods.
Nothing is built or measured.

| Capability | plain | sysbox | kata | privileged |
|---|---|---|---|---|
| root inside, install any package | yes | yes | yes | yes |
| a docker daemon, compose | no | yes | yes | yes |
| nested Kubernetes (k3d, kind) | no | yes | yes | yes |
| user-namespaced pods inside it | no | no | yes | yes |
| build images without a daemon (rootless BuildKit, kaniko) | limited | yes | yes | yes |
| headless browser tests | yes (may need a node setting) | yes | yes | yes |
| kernel-level work (eBPF, mounts, loop devices) | no | limited | yes, fixed guest kernel | yes, node's kernel |
| sandbox root contained from the node | yes | yes | yes | no |
| node requirement | none | sysbox | KVM | none |
| memory | shared with the node | shared | held per VM | shared |

Which definition runs where:

| | plain | sysbox | kata | privileged |
|---|---|---|---|---|
| `devcontainer` (needs a docker daemon) | no | yes | yes | yes |
| `startup` needing nothing | yes | yes | yes | yes |
| `startup` declaring docker / Kubernetes / user namespaces | no | yes / yes / no | yes | yes |

The rule of thumb: pick the lowest of `plain` < `sysbox` < `kata` that
meets the project's needs.

| Project | Lowest runtime | Notes |
|---|---|---|
| Web frontend, API, library, CLI | plain (with `startup`) | sysbox today, because `devcontainer` needs docker |
| App plus databases through compose | sysbox | or plain, if `startup` declares services (open question 2) |
| Builds and pushes images | sysbox | plain is possible with daemonless builders |
| Kubernetes apps and operators on k3d or kind | sysbox | |
| Anything with user-namespaced pods (Apothecary itself) | kata | |
| Networking, eBPF, filesystem tooling | kata | the guest kernel loads no modules |

Where the user-facing runtimes could end up:

| Phase | User-facing | Internal only |
|---|---|---|
| Today (devcontainer only) | sysbox, kata | privileged |
| With `startup` | plain, sysbox, kata | privileged |
| A possible end state | plain, kata | privileged, and sysbox retired if nobody needs its niche (docker on nodes without KVM) |

## What we want

1. **`.apothecary/startup.json`**, a definition named `startup`.
   - A sketch, not a decided format:

     ```json
     {
       "image": "node:22-bookworm",
       "user": "node",
       "workdir": ".",
       "env": {"NODE_ENV": "development"},
       "needs": [],
       "onCreate": "npm ci",
       "postStart": "npm run dev &",
       "ports": [{"port": 5173, "label": "web"}]
     }
     ```

   - `needs` names capabilities (`docker`, `kubernetes`,
     `user_namespaces`), which map onto `RuntimeCapabilities`. Empty means
     any runtime, `plain` included.
   - The lifecycle commands mirror the devcontainer hooks Apothecary
     already runs as the remote user (`RunUserCommands`).
   - A JSON schema ships with it, and the UI and the engine say which field
     is wrong.
2. **The `plain` runtime.**
   - A row in the `envport` table: Contained, nothing else.
   - A runtime trait in `runtime.go`: no class, not privileged, and
     `hostUsers: false`.
   - The `sandbox` container is the workspace. Its image is `startup.json`'s
     and its command is the shim, with no `apo-dockerd`. The shim and tools
     are seeded as today.
   - The claim with subPaths, as under sysbox, so it can stop when idle.
   - Approved like any runtime: chart `workspaces.runtimes`, the provisioner,
     then the workshop.
3. **Detection and refusal.**
   - `startup` is detected by its file.
   - A repository with both files: open question 5.
   - A definition whose needs the runtime cannot meet is refused at
     configuration time, naming the fix ("needs docker: choose sysbox or
     kata").
4. **Every surface works on `plain`**: terminal and SSH, the editor, exposed
   ports, the browser session, the tools bundle, the egress gateway.
5. **Docs and tests**:
   - `docs/kubernetes.md`'s runtimes table;
   - a golden `plain-*` pod;
   - unit tests for the definition and refusals;
   - a live suite under `plain` in CI (`sandbox-test` matrix);
   - a note in `docs/notes/` for every choice below.

## Open questions (the owner's)

1. **Where the base environment comes from.** An image named in the file,
   pulled as the pod's image? Or one Apothecary-provided image plus install
   steps? Recommendation: the file names an image, with Apothecary's tools
   image as the default when it names none. The shim is static and seeded,
   so any image with a shell works.
2. **Services (Postgres, Redis).** Without docker, these would be extra
   containers in the same pod (sidecars), not a compose file. Should
   `startup.json` declare them, or leave services out of v1 and send those
   projects to sysbox? Recommendation: out of v1, and design the field
   later.
3. **`startup` on a docker runtime.** When a `startup` project declares
   `docker` on sysbox or Kata, where does the daemon run? Two options:
   - the workspace image as a sibling container in the pod, with the
     sandbox image's daemon beside it on a shared socket and the run folder
     mounted at the same path in both, so bind mounts resolve;
   - the workspace inside the daemon, as `devcontainer` does today.

   The first keeps "the workspace is the pod's container" true everywhere.
4. **What survives a stop.** On `plain`, anything installed outside the run
   folder (an `apt install` in `onCreate`) is lost when the pod is
   replaced. Under `devcontainer` the container's layers live on the claim.
   Does `onCreate` rerun on every new pod, or does the image have to carry
   it? This decides how stop-on-idle feels.
5. **Both files present.** Which wins, `.apothecary/startup.json` or
   `.devcontainer/`? Or a workshop setting? Recommendation: `startup.json`
   wins, because it is Apothecary's own and deliberate.
6. **Default approved runtimes** once `plain` exists: `plain` plus `sysbox`,
   with `kata` where nodes have KVM, and `privileged` opt-in.

## To verify first (spike items)

None of these has been tried. Settle each before building on it, and
record what you find in a note.

- **The tunnel under `plain`.** The `wg` sidecar needs `NET_ADMIN` on the
  pod's network namespace. Under sysbox it works with `hostUsers: false`,
  but sysbox makes its own namespaces. Under plain runc with `hostUsers:
  false`, check whether the pod's user namespace owns its network namespace
  (containerd ≥ 2.0). If it doesn't, give the `wg` container `NET_ADMIN`
  with `hostUsers: true`, and keep the workspace container unprivileged.
- **Idmapped mounts on the claim.** `hostUsers: false` needs a filesystem
  that supports idmapped mounts, and a kernel ≥ 6.3. ext4 and xfs are
  fine; some CSI drivers (NFS, some network filesystems) are not. Check
  `local-path`, EBS gp3 and Longhorn. Also check the Kubernetes version
  where `UserNamespacesSupport` is on by default.
- **Running as root inside.** `apt install` as uid 0 in the pod's user
  namespace, and package managers that `chown`, work with
  `allowPrivilegeEscalation` off. The image's own users and file ownership
  survive the id mapping.
- **Headless browsers.** Chromium's sandbox needs unprivileged user
  namespaces or `--no-sandbox`. Find out which one works in a `plain` pod
  under the default seccomp profile.
- **The image's entrypoint and shell.** The shim replaces the image's
  entrypoint. Check that a distroless or shell-less image is refused with a
  clear message.
- **Telemetry and sizing.** A `plain` sandbox reports by runtime like the
  others (`sandbox telemetry labels`), and an unsized one behaves as under
  sysbox.

## Out of scope

- Kata peer pods, a desktop display, Windows and macOS targets.
- Retiring sysbox: decide that once `plain` and `kata` have run in
  production for a while.
- Converting `devcontainer.json` to `startup.json` automatically.

## Pointers

- `docs/sandbox-runtimes-plan.md`: the layers table, decisions 3 and 6, and
  "Not in this plan".
- `docs/notes/2026-11-22-the-sandbox-runtime-is-one-named-value.md` and
  `2026-11-22-a-workshop-picks-its-sandbox-runtime-from-the-provisioners-approved-list.md`:
  how a runtime is added and chosen.
- `docs/notes/2026-11-22-idle-sandboxes-stop-and-keep-their-disks.md`:
  stop-on-idle and its refusals.
- `internal/adapters/workspace/runtime.go`, `definition.go`,
  `def_devcontainer.go` and `host_k8s.go` (`podManifest`).
