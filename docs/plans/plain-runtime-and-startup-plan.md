# Plan: the `plain` runtime and the `startup` definition

Status: proposed (2026-11-27). Nothing is built. The work item is
`docs/plain-runtime-and-startup-ticket.md` plus what its ticket settled
since (the 2026-11-26 spike on the k3s host). This plan turns it into
phases. It rests on nine decisions that are the owner's (below, each with
a recommendation): confirm or change them before phase 1 starts.
Owner: the agent implementing it. Read `AGENTS.md` first; every rule there
applies (conventional commits, no model ids, `task fmt`, `task check`, a
note in `docs/notes/` with each decision and deviation).

Each phase is one pull request, in this order. A phase leaves the tree
green and the product working; none depends on a later one.

## What the code says today

Ten findings from reading the code. Each one shapes a phase or a decision.

1. **The pod is made before the checkout exists.** `k8sHost.prepare`
   (`host_k8s.go`) runs gateway, `ensurePod`, `ensureCheckout` (a `git
   clone` exec'd into the sandbox container), then `detect`. Under `plain`
   the sandbox container's image is `startup.json`'s, and that file is in
   the checkout. So the image is unknown when a run's first pod is made
   (decision 7).
2. **The adapter names docker outside the definition.** `host.go` says
   everything after the workspace container exists is "definition- and
   host-blind". It is not: `containerID`, `relayTarget`,
   `ScanSiblingPorts`, `startPortScan`, `ExposePort` (`bridge.go`),
   `ownsContainer`, `renameContainer`, `rootInit`, `workspaceState`,
   `pushBuildCache` (`devcontainer.go`) and `resetSandbox` (`host_k8s.go`)
   all build `docker` command lines (phase 1).
3. **Nothing in the shim changes user.** Every process in a workspace
   gets its uid from `devcontainer exec`. Under `plain` the engine enters
   with `kubectl exec`, as the container's root (phase 3).
4. **A definition's needs are one static value.** `RuntimeChoice.Needs`
   is `workspace.Needs()`, the union over every definition: nested
   containers. Left as it is, `plain` could never be chosen. And
   `TestDevcontainerNeedsNestedContainers` asserts that every runtime
   nests containers (phase 4).
5. **The repository host can say a file exists, not read it.**
   `repoport.RepoHost.FileExists` reads the default branch only.
   `engine.preflight` and `PreflightProject` use it to refuse a
   repository without a devcontainer (phase 4).
6. **The tunnel script is likely to fail under `plain` as written.**
   `apo-wg-up` runs `sysctl -qw net.ipv6.conf.all.disable_ipv6=1
   net.ipv4.conf.*.rp_filter=0` under `set -eu`. An unprivileged runc
   container has `/proc/sys` read-only; sysbox emulates it, which is why
   it works there. Its userspace fallback makes `/dev/net/tun` with
   `mknod`, which a user namespace cannot do. The 2026-11-26 spike ran
   the sidecar in the `userns` pod's relaxed context (seccomp and
   AppArmor unconfined), not under `plain`'s, so it does not settle this
   (spike 0.1).
7. **The tools image cannot be a workspace.** It is `alpine` with
   `seed.sh`: no bash, no git, and musl, so no editor (code-server has no
   musl build). The ticket's default image does not work as it stands
   (decision 1).
8. **Under sysbox a stop keeps the whole container**, because docker's
   data is on the claim. That includes `/apothecary` (`run.env`,
   `provider.env`, `git-credentials`, skills, roles, services, the
   sidecar's log) and the user's home, where the harnesses keep their
   sessions. Under `plain` a new pod is a new container: only what the
   pod mounts from the claim survives (decision 4).
9. **"The default seccomp profile" has to be asked for.** A pod that
   names no `seccompProfile` runs unconfined unless the kubelet was
   started with `--seccomp-default`. The `plain` pod must say
   `RuntimeDefault` itself (phase 5).
10. **Under `plain` the node pulls the workspace image.** Today every
    image a repository names is pulled by the sandbox's own dockerd,
    through the project's gateway: its allowlist, its mirror. A `plain`
    pod's image is pulled by the kubelet, on the node's network, with the
    node's credentials (decision 9).

## Decisions (the owner's)

The first six are the ticket's. The last three came out of the reading
above. "If otherwise" says what changes in the phases.

1. **The base image.** Recommended: `image` is required in v1, with no
   default.
   - Why: the tools image cannot be one (finding 7). A default means a
     new published image (Debian, bash, git, curl, sudo) that Apothecary
     builds and maintains.
   - If otherwise: phase 2 adds `workspace/images/base`, its entry in
     `deploy/images.sh`, the chart's image values and CI's image list.
2. **Services.** Recommended: out of v1. A project with a database uses
   a devcontainer with compose, or `startup` with `needs: ["docker"]`,
   on sysbox or kata.
3. **`startup` on a runtime that nests docker.** Recommended: the
   workspace runs inside the sandbox's daemon, as a devcontainer does.
   "The workspace is the pod's container" is `plain`'s shape alone.
   - Why, against a sibling container in the pod:
     - under Kata on its raw-device claim, the run folder is a disk the
       sandbox container formats and mounts itself; another container in
       the pod cannot see it;
     - the pod's shape would depend on the file under every runtime, so
       every runtime would get the two-step start of decision 7, where a
       pod start costs most (a VM, a dockerd);
     - the sandbox's size would have to be split between two containers;
     - inside the daemon it is one `docker run` on code that exists.
   - If otherwise: phase 3 becomes a new pod shape per runtime (a
     sibling container, a shared socket, the run folder at one path in
     both), Kata needs an answer to the first point, and phase 5's
     two-step start applies to every runtime.
4. **What survives a stop under `plain`.** Recommended: the claim keeps
   the checkout, `/apothecary` and the user's home. Everything else is
   the image's, new with each pod.
   - The hooks mean what they mean in a devcontainer: `onCreate` runs
     once per container, and under `plain` a new pod is a new container.
     So `setup` and `onCreate` rerun on every new pod, a wake included.
     Heavy installs belong in the image; the docs say so.
   - Why the home: without it an agent's session does not survive a
     stop (finding 8), and a run that wakes for a review comment starts
     with no memory.
   - If otherwise (only the run folder and `/apothecary` are kept):
     phase 5 drops `home-seed` and the home rule, and the docs say a
     stop ends the harness sessions.
5. **Both files present.** Recommended: `.apothecary/startup.json` wins,
   under every runtime. It is first in the detection order.
6. **Default approved runtimes.** Recommended:
   - the chart's default stays as it is (the default runtime alone);
   - `deploy/k3s/install.sh` approves `sysbox` and `plain` (`kata` too
     with `--kata`);
   - the docs recommend `plain` plus `sysbox`. The default runtime stays
     `sysbox`.
   - Why: a chart default would approve `plain` on clusters nobody
     checked. The provisioner's Test starts a pod under each approved
     runtime, which is where a cluster that cannot run it says so.
   - If otherwise: `values.yaml`'s `workspaces.runtimes` default and the
     `chart:lint` cases change.
7. **A run's first pod (finding 1).** Recommended: the first pod of a
   run under `plain` starts on the sandbox image, which has git and bash,
   with no daemon. After the checkout the file's image is known, and the
   pod is replaced once.
   - On `pvc` storage the checkout stays on the claim. On `emptydir` it
     is cloned again in the new pod.
   - A wake or a reprovision reads the image recorded in the engine's
     run folder and starts the right pod at once.
   - Not chosen:
     - changing the pod's image while an init container holds it back:
       the API allows it, but it rests on kubelet behaviour nothing else
       here uses;
     - reading the file through the repository host alone: a rebuild
       must read the checkout, where the agent may have changed the
       file. It can serve as a hint (phase 8).
8. **A root step.** Hooks run as `user`, as a devcontainer's do. But
   `startup` has no build, so a non-root user without sudo could install
   nothing. Recommended: one more field, `setup`, run as root once per
   new container, before `onCreate`.
9. **The workspace image's pull (finding 10).** Recommended for v1:
   document it, and add no control. The image is pulled by the node,
   outside the workshop's egress policy; a private image works only if
   the node can already pull it. If the owner wants a control, it is a
   provisioner field (registries a `plain` image may come from, or a pull
   secret) added in phase 5.

## The file (v1)

`.apothecary/startup.json`, strict JSON. An unknown field is refused, so
a typo is named.

```json
{
  "image": "node:22-bookworm",
  "user": "node",
  "workdir": ".",
  "env": {"NODE_ENV": "development"},
  "needs": [],
  "setup": "apt-get update && apt-get install -y jq",
  "onCreate": "npm ci",
  "postStart": "npm run dev &",
  "ports": [5173, {"port": 3000, "label": "api"}]
}
```

| Field | Default | Meaning |
|---|---|---|
| `image` | required (decision 1) | the workspace's image |
| `user` | `root` | a user name or uid from the image's passwd |
| `home` | `/root`, or `/home/<user>` | must be that user's home in the image; the claim is mounted there (decision 4) |
| `workdir` | `.` | where the hooks run, relative to the checkout, never outside it |
| `env` | none | literal values; the workshop's variables win, as today |
| `needs` | `[]` | `docker`, `kubernetes`, `user_namespaces`: nested containers, nested Kubernetes (and containers), user namespaces inside |
| `setup` | none | as root, once per container (decision 8) |
| `onCreate` | none | as `user`, once per container |
| `postStart` | none | as `user`, at every start |
| `ports` | none | a port, or `{port, label}`; auto-exposed like a devcontainer's `forwardPorts` |

- A hook is a string (run by `sh -c`) or an array (an argv). A failing
  hook fails the provision, as a devcontainer's does.
- `label` is read and checked but shown nowhere yet: an exposure has no
  name today (`AppPorts` returns numbers).
- No environment probe: a process gets the image's `ENV` and the file's
  `env`. A terminal still runs a login shell.
- What the image must have: `sh`, `bash`, coreutils and `awk` (what the
  adapter's scripts call), and `git` (a run's bootstrap switches
  branches in the workspace). The editor needs glibc, as today.

## Phase 0: spikes

No product code. Run each on the k3s host through a throwaway kubeconfig,
never the default context. Record each finding in a note; phase 5 builds
on them.

- **0.1 The tunnel under `plain`'s security context** (finding 6). A pod
  with `hostUsers: false`, seccomp `RuntimeDefault`, the default AppArmor
  profile, and the `wg` container (`apo-wg-up`, `NET_ADMIN`) against a
  real gateway attachment.
  - Expected: the `sysctl` line fails; `ip link add wg0 type wireguard`
    passes (the pod's user namespace owns its network namespace); the
    userspace fallback fails for lack of `/dev/net/tun`.
  - Fixes for the sysctls, in this order:
    1. `procMount: Unmasked` on the `wg` container alone. Kubernetes
       allows it only with `hostUsers: false`. Check what Pod Security
       level the namespace then needs (it already needs `NET_ADMIN`).
    2. The script reads each value and fails only when it is not already
       what it wants; decide for each sysctl whether the tunnel needs it.
  - For the fallback: record that `plain` with the firewall on needs the
    node's kernel WireGuard module, unless the device turns out to be
    there.
  - If no fix keeps `hostUsers: false`, stop and go back to the owner.
    The ticket doc's fallback (`hostUsers: true` for the pod) removes
    what makes `plain` contained.
- **0.2 Idmapped claims** on EBS gp3 and Longhorn: a `hostUsers: false`
  pod on a claim, files written as uid 0 and uid 1000, read back from a
  second pod. Without such a cluster at hand, the docs say "untried", as
  the runtimes plan did for EKS.
- **0.3 Root inside.** In `node:22-bookworm`: `apt-get install` as root,
  `id` as `node`, the image's files still owned by `node`. Then `sudo`
  in an image that has it, with `allowPrivilegeEscalation` unset and set
  to false. This decides whether the pod sets it (recommended: unset, so
  sudo works; the user namespace is the boundary).
- **0.4 Headless Chromium** under `RuntimeDefault` in the pod: a
  Playwright test with its sandbox, then with `--no-sandbox`. The answer
  goes in the docs.
- **0.5 An image without a shell** (a distroless one): the pod starts
  (the shim is its command) and the image check names what is missing.
- **0.6 The pod replacement of decision 7.** Time from deleting the
  first pod to the second being ready, on `local-path` (and on EBS if
  0.2 has a cluster, where the volume may move to another node). In the
  same pod, the mounts of phase 5: the claim's `run` at the run folder,
  `run/shim` at `/opt/apothecary`, `run/tools` under it, `state`, `home`.
- **0.7 Sampling and sizes.** `/sys/fs/cgroup` in the `plain` container
  is its own cgroup, so `sampleScript` reads it unchanged; an unsized
  pod behaves as under sysbox.

Done when: a note per item, and the owner has seen 0.1.

## Phase 1: the adapter stops naming docker

No behaviour change. The call sites of finding 2 move behind the
definition, so the adapter asks its definition instead of building
`docker` arguments.

- What the adapter needs of a workspace, wherever it runs:
  - run a script as root (`rootInit`);
  - run a command as the remote user (`ExecArgs`, as today);
  - start a detached process, as root or as the user (the port scanner,
    a reverse relay);
  - its state (`workspaceState`);
  - where a port is served (`relayTarget`) and what its siblings listen
    on (`ScanSiblingPorts`): compose's, so the devcontainer's;
  - rebuild from scratch (`resetSandbox` for compose).
- `ownsContainer`, `renameContainer` and `pushBuildCache` move into the
  devcontainer definition: they are steps of its `Up`.
- `host.go`'s and `definition.go`'s comments say what is now true: a
  docker daemon is the devcontainer's detail.
- `Rebuild`'s log line names the definition.

Done when: the golden pods are byte-identical, `task check` passes,
`task sandbox:test:privileged` passes, and `DockerBin` is read only in
`def_devcontainer.go`.

## Phase 2: the file

- A leaf package, `internal/workspace/startup`, which the adapter and
  the engine both read (the adapter already imports
  `internal/workspace/checkout`): the types, `Parse`, the mapping of
  `needs` onto `envport.RuntimeCapabilities`.
- The JSON schema, embedded there and checked with the library already
  in `go.mod` (`santhosh-tekuri/jsonschema`). An error names the file and
  the field's path: `.apothecary/startup.json: /ports/0: …`.
- The schema is served by the engine at a public path, so a file's
  `$schema` can point at it (`routes.go`, then `task api`).
- `docs/startup.md`: the table above, the hooks, what the image must
  have.

Tests: a table of valid and invalid files, each invalid one asserting
the path it names; the schema's properties and the Go struct's fields
are the same set; the `needs` mapping.

## Phase 3: `startup` on the runtimes that nest docker (decision 3)

After this phase a repository with only `.apothecary/startup.json` runs
on sysbox, kata and privileged. `plain` does not exist yet.

- `def_startup.go`, first in `definitions()` (decision 5). `Detect`
  hands back the definition bound to the checkout's parsed file, so
  `Needs`, `DeclaredPorts` and `ConfigFiles` read it. An invalid file
  fails the provision with the field named; it never falls through to
  the devcontainer.
- `Up`, in the sandbox's daemon: pull the image, then `docker run` with
  the id labels, the shim dir at `/opt/apothecary`, the tools under it,
  the checkout at its own path, the file's `env`, and the shim as the
  container's command. A restart reuses the container; a rebuild removes
  it first.
- `needs` with `docker` or `kubernetes` asks for the sandbox's daemon
  socket: `SandboxDaemonUse` reports it and the daemon door decides, as
  for a devcontainer's docker-outside-of-docker.
- Three shim subcommands (finding 3). The shim's source hash changes, so
  the shim image is rebuilt.
  - **the container's command**: stays up, reaps orphans, ends on TERM
    (today's `idle` waits but does not reap);
  - **run as a user**: looks the user up in the image's passwd and group
    files, sets groups, gid, uid, `HOME`, `USER`, `LOGNAME` and `SHELL`,
    then execs. It can also start the process detached, as `sidecar
    --daemon` does, so nothing depends on `su`, `runuser`, `setsid` or
    `nohup` in the image;
  - **the image check**: names what the image lacks (`sh`, `bash`,
    `git`, `awk`) without needing a shell to say it.
- The hooks: `setup` as root, then `onCreate`, then `postStart`. The
  once-per-container marks live in the container's own filesystem, never
  on the claim (decision 4).
- `rootInit` runs as it does today, through phase 1's seam.
- The engine's preflight asks for a workspace definition, either file:
  `hasDevcontainer`, the `devcontainer` step of `PreflightProject`, and
  `errNoDefinition`'s text.

Tests: `def_test.go` cases for detection order, an invalid file, the
daemon door, the hooks' order and marks; shim tests for the three
subcommands. Live: the fixture repository gets a `startup` twin, and
`TestLiveSandboxLifecycle` takes its definition from
`APOTHECARY_LIVE_DEFINITION`; `task sandbox:test:privileged` runs both.

Done when: a `startup` repository provisions on privileged and sysbox,
with terminal, SSH, editor, ports and hooks working, and the live suite
passes under both definitions.

## Phase 4: refusals follow the repository's definition

- `RuntimeChoice.Needs` and `workspace.Needs()` go (finding 4). What a
  workshop needs of its runtime is its own settings (`projectNeeds`)
  plus its repository's definition.
- `repoport.RepoHost` gains `ReadFile`, on the default branch like
  `FileExists`: GitHub, GitLab, the fake (finding 5).
- The definition's needs, read from the repository: `startup.json`'s
  `needs`; else, with a devcontainer, nested containers; else nothing.
- Where it is checked, earliest first:
  1. **Saving the workshop** (`runtimeFits`): a 400 that says what asks
     and the fix, for example "sandbox runtime plain cannot give this
     repository's devcontainer nested containers: choose kata, sysbox,
     or add .apothecary/startup.json". If the repository cannot be
     reached, the workshop's own needs are checked alone.
  2. **Run creation and the workshop's preflight** (`engine.preflight`,
     `PreflightProject`'s step, renamed `definition`): the same check,
     and an invalid file named by its field.
  3. **The provision**, in the adapter's `detect`, from the checkout:
     the run's own branch, so the final word. It refuses before `Up`,
     with `envport.Satisfying` over the approved runtimes as the fix.
- `Resolver.Resolve` stays as it is: it reaches nothing, so it checks
  the workshop's settings only.
- The UI shows the save's refusal and the preflight step where it shows
  them today. The form's `rt_noDocker` hint stays.

This is testable before `plain` exists: `needs: ["user_namespaces"]` on
sysbox is refused, naming kata.

Tests: `TestCheckRuntime` cases over a fake repository host (each
definition, an unreachable repository, an invalid file);
`TestWorkshopRuntimeIsChecked`; the preflight step; the adapter's
refusal before `Up`; `TestDevcontainerNeedsNestedContainers` reduced to
the definition's own claim.

## Phase 5: the `plain` runtime

- **The tables.**
  - `envport`: `RuntimePlain`, `Contained` alone.
  - `runtime.go`: no class, not privileged, `ownUserNamespace`, and a new
    trait: the sandbox container is the workspace, with no daemon.
    `podManifest` and the `startup` definition read the trait, never the
    name.
- **Every list of names** follows: `config.SandboxRuntimes`, the chart
  schema's two enums, `kubernetes.go`'s help text, and the tests that
  spell the list (`envport/runtime_test.go`, `config/env_more_test.go`).
- **The workspace pod.**
  - No RuntimeClass, `hostUsers: false`, seccomp `RuntimeDefault` said
    in the pod (finding 9), AppArmor the runtime's default. No
    capability added to the sandbox container, none dropped: `apt` and
    `chown` need the default set. `allowPrivilegeEscalation` per 0.3.
  - Init containers: `shim-seed`, `tools-seed`, `home-seed` (decision 4:
    the workspace image running the shim, which copies the image's home
    onto the claim the first time), `wg` with `NET_ADMIN` and what 0.1
    found, `relay`.
  - The `sandbox` container: the file's image, the shim as its command,
    the file's `env`, a readiness probe that runs the shim, and
    `sandboxResources` as today, so sizes, the ceiling and the OOM notes
    work unchanged.
  - Mounts, all from the claim: `run` at the run folder (the same path
    as today, so every engine path holds), `run/shim` at
    `/opt/apothecary`, `run/tools` under it read-only, `state` at
    `/apothecary`, `home` at the user's home. No `docker` subPath.
- **The first pod** (decision 7): the same pod with the sandbox image
  and no daemon. After `detect`, the adapter records the image, user and
  home in the engine's run folder, beside `definition`, and replaces the
  pod if it is not of that shape.
- **The pod's shape** (`k8s_shape.go`) gains the image and the home
  path. A rebuild then replaces a pod whose image changed, under
  `shapeChange`'s existing rule: kept on `emptydir`, with a note that
  the change waits for the next run.
- **`startup` in the pod.** The trait picks where the definition runs:
  the pod (here) or the daemon (phase 3). In the pod, `Up` is the image
  check; root scripts are a plain exec; user commands and detached
  starts go through the shim; the workspace's state is the pod's; a
  port is always on loopback; a rebuild from scratch replaces the pod.
- **The port scanner** now shares the pod's network namespace, so it
  excludes the relay's port (7070) as it excludes the editor's.
- **Refusals** need nothing new: a devcontainer on `plain`, `needs:
  ["docker"]` on `plain` and the workshop's Docker setting on `plain`
  are all phase 4's check.
- **The probe** (`runtime_probe.go`) builds its pod from the traits, so
  the provisioner's Test covers `plain` once it is approved.
- **Stop-on-idle**: `plain` is not a VM, so `CanStopUnder` gives the
  storage's answer: it sleeps on `pvc`.
- **The chart**: the schema enums, a `chart:lint` case, `task
  chart:docs`. `deploy/k3s/install.sh` per decision 6.
- **The workshop form**: `rtAboutPlain` in `i18n/locales/en/misc.ts`,
  its entry in `SandboxRuntimeField.svelte`'s `about` map, and a case in
  the test beside it (which already has a `plain` fixture). The text:
  "Pick this if your project needs no docker: a frontend, an API, a
  library. The lightest and the most restricted."

Tests:
- golden pods: `plain-pvc-gateway`, `plain-emptydir`, `sized-plain-pvc`,
  and the first pod, `plain-checkout-pvc`;
- `TestEveryRuntimeHasTraits` (it walks both tables), `TestRuntimeProbe`,
  `TestCanStop`, `TestPodShape` (an image change), `TestRuntimeCapabilities`;
- the first pod is replaced once, and not at all on a wake;
- a rebuild on `emptydir` keeps the pod and says why.

## Phase 6: every surface, live, in CI

- `task sandbox:test:plain`: `k8s:test-live` with `RUNTIME: plain` on
  the k3d cluster, like `sandbox:test:privileged`. No rig: `plain`
  installs nothing on a node, and the k3d cluster already runs a
  user-namespaced pod (the assistant's).
- CI: a `plain` entry in the `sandbox-test` matrix, with the `k3d`
  entry's prerequisites, under `APOTHECARY_LIVE_STRICT`.
- `TestLiveSandboxLifecycle` under `plain` with the `startup` fixture.
  Its image needs git. Each surface is an assertion:
  - exec as the user, binary files, the declared ports;
  - the tools bundle and the editor;
  - the sidecar, a terminal, the SSH tunnel;
  - an exposed port through the relay, which is also how the browser
    session reaches the run's localhost;
  - resource samples;
  - egress: a host denied, then allowed, the tunnel's bypass refused, no
    route to another run;
  - reattach.
- The helpers that use docker inside the sandbox get `plain` twins.
  Sleep and wake asserts what decision 4 says: the checkout, a file in
  the home and `/apothecary` are kept; a file under `/usr/local` is
  gone; `setup`, `onCreate` and `postStart` ran again.
- Telemetry needs no code: the gauges label by `WorkspaceRuntime`. Check
  the `runtime="plain"` series on the dashboard once.

Done when: the `plain` entry passes in CI, strict.

## Phase 7: documentation and defaults

- `docs/kubernetes.md`: a `plain` row in the runtimes table (and the
  sentence under it, "All three run docker inside"), the cluster
  requirements (user namespaces for pods, idmapped mounts on the
  storage, what 0.1 and 0.2 found), stop-on-idle (what a `plain` stop
  keeps), sizing.
- `README.md`: "Unprivileged sandboxes" compares four; "Cluster
  requirements"; the image pull of finding 10 under "Security".
- `docs/startup.md` gains the ticket doc's capability tables and "which
  definition runs where".
- The chart's README and `NOTES.txt`.
- The status lines of `docs/plain-runtime-and-startup-ticket.md` and
  `docs/sandbox-runtimes-plan.md`.

## Phase 8, optional: the first pod right the first time

`RepoHost.ReadFile` takes a ref. The engine reads `startup.json` at the
run's ref before provisioning and passes its image in the `EnvSpec` as a
hint. The adapter starts the first pod on it and still checks the
checkout's file afterwards. Worth building only if 0.6 shows the
replacement is slow, which is most likely on storage that moves between
nodes.

## Risks

- **0.1 fails with no fix that keeps the user namespace.** Then `plain`
  works only with the firewall off, or not as designed. This is why it
  is first.
- **Storage without idmapped mounts** (NFS, some network filesystems):
  the pod never starts. The provisioner's Test probe does not mount a
  claim today. Consider giving the `plain` probe one.
- **The first pod's replacement** on storage that reattaches slowly
  (phase 8 is the answer).
- **Images**: the node pulls them (decision 9), and an image short of
  the contract fails at the image check, not at build time as a
  devcontainer would.

## Not in this plan

- The `userns` runtime (nested docker and Kubernetes in a
  user-namespaced pod): its own ticket.
- Services in `startup.json` (decision 2), a default image (decision 1).
- Showing a port's `label`; editor extensions in `startup.json`;
  comments in the file.
- Sampling through the shim, which would drop `awk` from the image's
  contract.
- Converting `devcontainer.json`; retiring sysbox; Kata peer pods; a
  desktop display; Windows and macOS targets.

## Notes to write

One per decision above, in the phase that builds it, and one per spike
item. Each is a new file `docs/notes/<date>-<slug>.md` with `# <title>`
first (`go test ./internal/devtools/...` holds the names).
