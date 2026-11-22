# The sandbox runtime is one named value

Phase 1 of `docs/sandbox-runtimes-plan.md`.

## Why

A run's sandbox pod was shaped by two settings read in three places: a
`Privileged` bool and a `RuntimeClass` string, combined into
`unprivileged()` in `host_k8s.go` and read again by the daemon door. Kata
(phase 5) and a per-project choice (phase 3) need the runtime to be one
value with its traits in a table.

## What now

- The vocabulary is the port's: `envport.Runtime` (`privileged`, `sysbox`)
  and `envport.RuntimeCapabilities` (nested containers, nested Kubernetes,
  user namespaces inside, contained). The engine needs it to refuse a
  project before a pod exists (phase 3), so it lives where the engine can
  read it without an adapter.
- The adapter's `sandboxRuntime` (`internal/adapters/workspace/runtime.go`)
  is how each runtime shapes the pod: default RuntimeClass, privileged
  security contexts (sandbox and `wg`), `hostUsers: false`. `podManifest`
  and the daemon door read traits; nothing branches on a runtime's name.
  The type is not called `runtime` (the plan's word): a package-level
  `runtime` would collide with a test file's import of the standard
  library's.
- `Host.unprivileged()` is `Host.contained()`: the capability, not the
  security context, decides the daemon door under `auto`.
- `Definition.Needs()` states what a definition asks of a runtime
  (devcontainer: nested containers). Data only until phase 3.
- `KubeConfig.Runtime` replaces `KubeConfig.Privileged`; `RuntimeClass` is
  now only an override of the runtime's own class. An unknown runtime fails
  every pod creation and the integration's Test.
- Boot: `APOTHECARY_K8S_RUNTIME`. Unset, the older
  `APOTHECARY_K8S_PRIVILEGED` decides it (on or unset: privileged; off,
  with a class: sysbox). Both set and disagreeing is refused at boot.
  `config` may not import the port, so it keeps its own list of names
  (`config.SandboxRuntimes`), held to `envport.Runtimes()` by a test in
  `cmd/apothecaryd`.
- The Kubernetes integration has a `runtime` field. Precedence: its
  `runtime`, else its older `privileged` field, else the deployment's.
  `runtime_class` is the class override.
- Chart: `workspaces.runtime`; empty maps from `workspaces.privileged`
  (true: privileged, false: sysbox). `workspaces.runtimeClassName` now
  defaults to empty (the runtime's own class), so the schema no longer
  requires it when privileged is off.

One deviation from byte-identical, in the chart only: a values file that set
`workspaces.privileged: true` and left `runtimeClassName` at its old default
used to render privileged pods naming `sysbox-runc`. They now name no class,
which is what such a cluster (no sysbox) can run. The live deployment's
values (`privileged: false`, `runtimeClassName: sysbox-runc`) render the
same pods as before.

The plan's phase-5 traits (block volumes, an entrypoint prelude, limits
required, pod annotations) are not declared yet: a field nothing reads is
dead code to the linter. They land with the Kata row.

## Tests

- `TestSandboxPodGolden`: five pods (privileged and sysbox, pvc and
  emptyDir, gateway or not, a class override) recorded from the code before
  this change in `internal/adapters/workspace/testdata/pods/`, compared
  byte for byte.
- `TestEveryRuntimeHasTraits`, `TestUnknownRuntimeIsRefused`,
  `TestRuntimeClassName`, `TestDevcontainerNeedsNestedContainers`,
  `TestK8sSysboxRuntime`.
- `envport`: `TestRuntimeCapabilities`, `TestParseRuntime`,
  `TestMissingAndSatisfying`.
- `TestBootSandboxRuntime`, `TestBootRefusals`, `TestKubernetesRuntime`,
  `TestBootRuntimesAreThePorts`; `task chart:lint` refuses
  `workspaces.runtime=gvisor`.
