# Assistant pod experiments

Why: phase 2 of `docs/assistant-plan.md` opens with six experiments its
design rests on. Run 2026-11-19 on a k3d cluster (k3s 1.35.5, containerd
2.2.3, kernel 7.2.15, Landlock ABI 10) and in docker on the same host, with
the pinned tools bundle (`dde74f3881cc7e14`). Not run on the k3s box or EKS:
the kernel and ABI there are what decision 9 ("no Landlock, no session")
answers for.

What now:

1. **Landlock in a user-namespaced pod: works.** `shim confine` (raw
   system calls, no library and so no licence to carry) as uid 1000 in a
   `hostUsers: false` pod with every capability dropped, a read-only root
   and the default seccomp profile. git reads its repository's history;
   the sibling repository, another session's state, `/proc/1/environ` and
   anything outside the spec are refused; a git alias that runs a binary
   off the exec list is `Permission denied`. ABI floor: 6 (Linux 6.12),
   set after review (`2026-11-19-the-assistant-pod.md`): every session
   shares the pod's user, so below 6 one could signal another session and
   the agent host, and below 3 truncate another's files.
   - **A shell is any binary that is one.** Alpine's `cat` is busybox, which
     is `sh`. The exec list names git, ripgrep, the harness, the shim and
     the musl loader (execve opens the interpreter for execution), never a
     coreutil, and the image removes `/bin/sh`.
2. **Harnesses without a shell: claude-code, opencode and omp each answer a
   turn and read a repository file with their own read tool,** confined,
   on their Alpine builds, as uid 1000 on a read-only root. opencode's
   shell tool fails cleanly (`PermissionDenied`). What they need: read
   `/lib`, `/usr/lib`, `/usr/share`, `/etc`, the bundle's `musl` tree and
   the session's repositories; write the session's home, which holds
   `TMPDIR` (bun extracts native code there, so the volume must not be
   `noexec`); the devices `/dev/null`, `/dev/zero`, `/dev/random`,
   `/dev/urandom`, `/dev/tty`. Not `/proc`. The working directory may be the
   workshop's folder: a chdir needs no Landlock right.
   - The profiles' commands start with `env K=V …` (busybox). The assistant
     passes that environment itself and executes the binary.
3. **The tunnel with `NET_ADMIN` alone: works.** In a `hostUsers: false` pod
   the kubelet makes the user namespace first, so the pod's network
   namespace is its own: the link, the keys, the address, the MTU and the
   routes all apply, and the `agent` container cannot change them. The
   sysctls `apo-wg-up` writes fail (`/proc/sys` is read-only without
   privilege) and are not needed: they exist for the sandbox's inner
   docker forwarding. With the default route on `wg0` strict reverse-path
   filtering passes both the internet (`wg0`) and the engine (`eth0`), and
   the pod has no IPv6 beyond link-local. The assistant pod's tunnel script
   skips them.
4. **codex: not verified, stays out of v1.** codex-acp signs in with
   `DEFAULT_AUTH_REQUEST={"methodId":"api-key"}`, then compresses its
   request bodies, so the fake model could not read the tool list. It reads
   files through its shell in the models seen so far; decision 14 stands.
5. **The engine's face by URL, and a session loaded in a new process:**
   claude-code and opencode list an HTTP MCP server's tools with its bearer
   header, and both load a session from the same state folder in a new
   process and answer a turn. **omp does not:** it initializes the
   session's MCP servers and lists their tools, stdio or HTTP, and never
   gives them to its model (its ACP session factory sets `enableMCP:
   false`). So omp cannot call `fill`, and **the assistant's harnesses are
   claude-code and opencode** (decision 14 revised). A ChatGPT login reaches
   the assistant only through a harness that serves it there.
   - The same holds for a run's omp stages: `ask_human` rides the session
     the same way. Recorded for the omp harness; not this plan's to fix.
6. **Cold start, image on the node:** the pod Ready on a fresh volume in
   17 s (most of it the local-path provisioner), on an existing one in 5 s;
   a session ready 0.6 s (claude-code) or 2.8 s (opencode) after its
   process starts, and the same again for a relaunch that loads it. The
   two containers share the volume as planned: `git` writes the
   repositories, `agent` reads them (`Read-only file system`) and writes
   its state.

Tests: `workspace/shim/confine_linux_test.go` holds experiment 1 against
the host kernel (reads, writes, no shell, no widening from inside, no
signal out, the refusals). The live suite repeats 1, 2, 3 and 5 in the pod.
