# The assistant pod

Why: phase 2 of `docs/assistant-plan.md`, everything below the
conversation: a person's pod, its sessions confined, its repositories.
The experiments it rests on are `2026-11-19-assistant-pod-experiments.md`.
These are the choices the code does not explain on its own.

What now:

- **The image is the assistant's own, not `FROM` the tools image**
  (decision 4 revised). CI builds images on a buildx builder that cannot
  start from a locally built tools image, and the bundle is 1.1 GB of musl
  harnesses the assistant runs two of. `workspace/images/assistant` is
  Alpine with git, ripgrep and libstdc++ (26 MB); the pod seeds the shim
  from the shim image (an emptyDir) and the claude-code and opencode builds
  from the tools image onto the person's volume, once per bundle
  (`TOOLS_HASH`, 0.6 GB, so a volume defaults to 10 GiB).
- **No shell in the image.** busybox is every applet, `sh` among them: the
  image deletes it and every link to it. The tunnel container runs the
  gateway image, which keeps its shell; the agent cannot reach it.
- **Four containers, two seeds.** `tunnel` (the gateway image, the pod's
  only `NET_ADMIN`, root in the pod's user namespace only), `relay` (the
  shim image: the engine's way in, behind a token), `agent` (the shim's
  agent host; repositories read-only, `/state` read-write) and `git`
  (repositories read-write; a fetch is the only thing that runs in it).
  Every container: `hostUsers: false`, uid 1000 but the tunnel, all
  capabilities dropped, no escalation, `RuntimeDefault` seccomp, read-only
  root, no service account token.
- **The tunnel skips the sandbox's sysctls**: a non-privileged container
  cannot write them and this pod forwards nothing (experiment 3).
- **The gateway admits a peer by label.** A gateway takes WireGuard from
  its project's pods labelled `apothecary.io/sandbox`; an assistant pod is
  not a sandbox, so the gateway's network policy gains a second peer,
  `apothecary.io/project` + `apothecary.io/gateway-peer: "true"`. Nothing
  selects pods on either label to act on them (checked: the only readers
  are network policies).
- **The person's gateway is `assistant-<person key>`.** The gateway manager
  takes it as its project; nothing that walks workshops reaches it (the
  egress reaper prunes by workshop ids from the store). Its standing allow
  list is the model providers' hosts; a fetch widens it to the repository
  hosts and narrows it back in a deferred call on a context of its own, so
  a cancelled fetch cannot leave them open (`egressgw.Manager.SetPolicy`, a
  whole-policy write). `EgressGateway.Base` keeps raw CIDRs and ports in
  every list: a model endpoint on a private address.
- **A fetch is `shim fetch` in the git container.** The credential arrives
  on its stdin and reaches git as `GIT_CONFIG_*` environment (an
  `http.extraHeader`), never argv, a URL or a file; clone the default
  branch's whole history, then `fetch` + `reset --hard FETCH_HEAD`; every
  repository at once (8 at a time).
- **A session is one confined process.** The agent host (the shim sidecar)
  gains a `confine` spec on its start request: the process runs under
  `shim confine` with the request's environment alone (never the
  sidecar's), the rules part of its identity (two sessions, two
  processes), its write folders made first (Landlock names paths that
  exist). Rules: read the system folders, the harnesses and the session's
  repositories; write its home (`/state/<session>`, which holds `TMPDIR`);
  run the harness, git, ripgrep, the shim and the musl loader. An idle
  process exits (`--agent-idle`, reported `agent_idle`, not as a crash);
  `POST /agent/stop` can stop one process.
- **The git tool rides the session** as an MCP server (`shim git-mcp`) the
  harness starts below itself, so it inherits the session's rules: read
  subcommands only, no option that writes, runs or reaches out, every
  transport off, no configuration but its own, external diff and textconv
  switched off per subcommand.
- **Files reach a session through the agent host:** `POST /fs/write`, only
  under `--write-root` (`/state`), never through a symbolic link a session
  planted.
- **The manager** (`assistantport.Pods`): ensure (gateway, pod, the kernel
  check `shim confine --check` once per pod, the agent host's answer),
  reuse, recreate on a stale image only when no fetch or turn is in flight,
  stop when idle, delete a volume unused for long; the volume's last use is
  an annotation, so it survives an engine restart.

Review (an independent pass before the commit) found two holes that chained
into an escape, and what now closes them:

- **A session could drive the agent host.** Landlock below ABI 9 does not
  cover connecting to a pathname unix socket, and the agent host's socket is
  in the session's container, as its user. Now the agent host takes a token
  on every request (`--token-file`: the relay's, mounted at
  `/run/apo-token`, outside every session's read rules), refuses an
  unconfined start (`--confined-only`), and from ABI 9 the rules handle
  `RESOLVE_UNIX` and grant it nowhere. `TestConfineReachesNoSocket`,
  `TestSidecarTokenAndConfinedOnly`, and the live test's request without
  the token.
- **The git tool's filter was bypassable**: git takes long options by any
  unambiguous prefix (`--open=`, `--ext`, `--no-inde`) and short ones
  bundled (`grep -nOprog`). Every prefix of a refused long option is now
  refused (`--text`, a real option, excepted), and grep's `-O` in any
  bundle.
- **The Landlock floor is ABI 6**, not 1: sessions share a user, so below 6
  one could signal another and the agent host. A kernel under it serves no
  session (`shim confine --check`, exit 3).
- **`/fs/write` walks with `openat`**, one component at a time with
  `O_NOFOLLOW|O_DIRECTORY`: a check of each folder then an open by path
  could be raced by a session swapping a folder for a link.
- **A credential goes to its own host only**: per repository, as
  `http.<scheme>://<host>/.extraHeader`, so a fetch mixing hosts never hands
  one host's token to another (`assistantport.FetchItem`).
- **The fetch window closes whatever happened**: its closing is registered
  before the widening, so a widening that took effect but answered an error
  is closed too.
- **Folders carry a hash** of the workshop's id (and the repository's name):
  two names that read the same in a folder never share one.
- The relay token's cache goes with its Secret; the idle reaper takes a
  process out of the agent host's tables before killing it, so a prompt in
  between gets a new process rather than a dying one; the pod sweep decides
  under the person's lock.

What remains, on purpose:

- **The exec list is not the boundary; Landlock's file and socket rules and
  the gateway are.** execve opens the ELF interpreter for execution, so the
  musl loader is on the list, and `ld <file>` runs any ELF the session can
  read (a file a repository committed, say), confined like the session.
- **During a fetch every session in the pod can reach the repository
  hosts**, unauthenticated (the credential is in the git container alone):
  enough for a session holding a token it read in a repository to write
  there. The pod is one peer of its gateway, so the window cannot be one
  container's; closing it per session would mean pausing them for a fetch.
- **Same user, so `chmod`, `utimes` and extended attributes** on another
  session's files are not Landlock's to refuse; their contents are.
- A session writes its own home, which a harness reads configuration and
  plugins from (opencode's `~/.config/opencode/plugin`): whatever it loads
  runs confined as the session.

Tests: `internal/adapters/assistant` (the manifest field by field, the
network policy, folders, the manager's lifecycle, rules, fetch window and
sweep against a fake host and gateway, the Kubernetes host against a
scripted kubectl); `workspace/shim` (`confine_linux_test.go`,
`gitmcp_test.go`, `fetch_test.go`, the sidecar's confined start, idle exit,
stop and write); `internal/adapters/egressgw` (`SetPolicy`). Live:
`internal/adapters/assistant/live_k8s_test.go` (build tag `live`).
