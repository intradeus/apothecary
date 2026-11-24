# Workspaces get size hints and project variables

## Why

A sysbox sandbox sees all of its node's CPUs: `nproc`, `os.cpus()` and
`os.availableParallelism()` report 22 on the k3s host, whatever the
sandbox's CPU limit. In run `01M877B3HNJRGPZPW9CVWXE4KS`, `nx affected -t
test test-unit --parallel=3` with a jest preset of `maxWorkers: "50%"`
started about 33 jest workers, and the sandbox was OOMKilled at 8 GiB and
again at 12 GiB. The repository already reads `JEST_MAX_WORKERS` and
`NX_PARALLEL`, but the only place to set them was its own
`devcontainer.json`, which changes every developer's local setup too.
Project secrets only reach a flow's MCP servers. lxcfs would make `nproc`
tell the truth, but it is a node-level component, and on EKS there is no
node to install one on.

## What now

- **`WorkspaceConfig.Env`** (`internal/project/confstore`): a workshop's
  variables, edited in Settings → Workspace. `validateEnv` refuses a name
  that is not `[A-Za-z_][A-Za-z0-9_]*`, the `APOTHECARY_` and `APO_`
  prefixes (in any case), and what the engine sets itself: `PATH`, `HOME`,
  `TERM`, `COLORTERM`, the harness launch and broker variables, and every
  provider's key variable (`providers.All()`). At most 64 variables and
  4 KiB a value. The CPUI says the same beside each row
  (`domain/envVars.ts`).
- **Plain values, not secrets.** They are shown back by the API and the
  form, and the field sends credentials to Secrets. Secrets in the sandbox
  are a separate design (TODO 56). That is why there is no masking, and
  no sealing at rest.
- **Hints** (`internal/adapters/workspace/wsenv.go`): `APO_CPUS` is the
  CPU limit rounded up to whole CPUs, and `APO_MEMORY_MIB` the memory limit
  in MiB. Each is unset without a limit. They are read from the limits of
  the pod the sandbox runs as (`ensurePod` → `podLimits`), not from the
  settings, so a reused pod keeps the hints of the size it was made with.
  Like the size, a hint changes only when the pod is made again. Under
  Kata the hint is the limit, not the guest's vCPU count (limit + Kata's
  `default_vcpus`). A runner told "5" on a 4-CPU limit would overcommit by
  one worker, and the limit is what the workshop asked for. The project and
  the limits are recorded in the run folder (`workspace-env.json`), so an
  engine restart keeps them.
- **Precedence: repository < hints < project.** The CLI lays the
  configuration's `remoteEnv` *over* `--remote-env` (devcontainers/cli
  0.86.0, `{...probed, ...cliRemoteEnv, ...config.remoteEnv}`), so
  `--remote-env` cannot give the project the last word. Every exec instead
  runs `devcontainer exec … env K=V … <cmd>` (`inWorkspaceWith`). Hints
  come first and the project's variables after, so a project value wins.
  `${APO_CPUS}` and `${APO_MEMORY_MIB}` in a value expand to the hint
  (empty without one). Nothing else expands, so a value is never a shell
  word.
- **Read at each exec.** The adapter reads the project's variables through
  `Config.ProjectEnv` (the resolver's `GetProject`) at every exec, so a
  change reaches the next command with no rebuild. A failed read keeps the
  last set read, so a process never starts without them.
- **The sidecar is told, not started with them.** It lives as long as the
  container and starts the harness, terminals and SSH sessions. If those
  inherited the sidecar's start-time environment, they would keep an old
  set. So the sidecar starts without the set (`exec(…, false)`), and each
  `Sidecar()` dial sends it with `PUT /env` when it differs from what that
  sidecar was last told. The shim appends it to what it starts
  (`engineEnv.over`). Because the sidecar's own environment never held the
  set, a variable the project drops is gone, and a repository value it
  shadowed comes back. A sidecar from before this route answers 404 and is
  left alone; it is not asked again.
- **Services** start through Exec (`apothecary-shim service start`), so
  they take the env prefix. **The browser editor** starts through Exec too.
  It keeps what it had at its start until the sandbox is made again: its
  settings file is the user's own when the project sets no editor
  preferences, so the env is not written there.
- **Visible:** each provision logs the hints and the project's variables,
  values included, with the expansion (`NX_PARALLEL=${APO_CPUS} (→ 4)`).

The ticket said a project could "override a hint". Here it overrides the
*effect* (`NX_PARALLEL=2` instead of `${APO_CPUS}`); it cannot set
`APO_CPUS` itself, because the `APO_` prefix is refused as asked.

## Tests that hold it

- `internal/project/confstore/envvars_test.go`: names, reserved prefixes
  and names, bounds, trimming, round trip; the contract fixture carries
  `Env`.
- `internal/adapters/workspace/wsenv_test.go`: hint rounding and absence,
  expansion, the exec argv and its order, a change reaching the next exec,
  the store failing, the restart, the provision log, pod limits (Kata,
  a reused pod, a ceiling), and the sidecar told once per change and
  started without the set.
- `workspace/shim/wsenv_linux_test.go`: `PUT /env` reaches the next
  terminal over the sidecar's own environment, and bad names are refused.
- CPUI: `domain/envVars.test.ts`,
  `workshop/form/WorkspaceEnvField.test.ts`, the ProjectForm save, and
  the API mappers.
