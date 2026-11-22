# A workshop picks its sandbox runtime from the provisioner's approved list

Phase 3 of `docs/sandbox-runtimes-plan.md`. The plan left one choice open:
where the admin's list of approved runtimes lives, System → Limits or the
Kubernetes integration's deployment defaults.

## Why the provisioner, not System → Limits

Whether a runtime can be approved depends on the cluster, not on policy. A
runtime is usable only when its RuntimeClass is installed and its nodes run
the handler (sysbox-runc today, Kata with KVM next), and only whoever
installs the cluster knows that. So the list sits beside the default runtime
in two places:

- in the deployment: the chart's `workspaces.runtimes`, read at boot as
  `APOTHECARY_K8S_RUNTIMES`;
- on the instance's provisioner: its `runtimes` field (System →
  Provisioner), where an admin may narrow the list.

The provisioner's Test button starts a pod under each runtime on the list,
so a runtime approved on a cluster that lacks it fails there. A setting in
System → Limits would let an admin approve a runtime the cluster cannot run,
and nothing would say so until a run failed.

## What now

- **Which runtimes are approved:**
  - the integration's `runtimes` field, which must include the integration's
    default runtime;
  - otherwise the deployment's list, with the default runtime added when it
    is missing;
  - with neither set, the default runtime alone, so a deployment that sets
    nothing behaves as before.

  `envport.RuntimeChoice` carries the default, the approved list and what
  the workspace definition needs (`workspace.Needs()`; a devcontainer needs
  nested containers).
- **A workshop's `workspace.runtime`** (`""` means the default) is checked by
  `RuntimeChoice.Choose`:
  - a runtime that is not approved is refused, and the error names the
    approved ones;
  - a runtime that cannot give what the definition and the workshop need
    (Docker in workspaces means nested containers) is refused, and the error
    names the approved runtimes that would do.
- **When the check runs:**
  - when the workshop is saved (`runtimeFits`, a 400);
  - at each provision, rebuild and envcheck. The resolver computes the
    runtime on every resolve but stores the refusal on the resolution
    (`WorkspaceRuntimeErr`) instead of failing the resolve, so a workshop
    whose runtime an admin later withdraws keeps its merge requests, tickets
    and logs readable. Only a new sandbox is refused (`SandboxRuntime()`),
    and envcheck reports it as its first step, `runtime`.
- **The adapter builds a pod for each env's own runtime** (`EnvSpec.Runtime`)
  and refuses one it was not offered (`runtimeFor`).
  - The integration's `runtime_class` override applies only to its default
    runtime. Another runtime uses its own RuntimeClass, because an override
    names one class and the runtimes would otherwise share it.
  - The daemon door's `auto` setting follows the env's runtime.
- **The Test button's probe**: a pod per approved runtime, in that runtime's
  shape (its class, its user namespace, its privilege), running `shim
  version`. It is labelled `apothecary.io/runtime-probe` with no run label,
  so nothing takes it for a sandbox, and it is deleted afterwards. A missing
  RuntimeClass is refused when the pod is created. A class whose handler no
  node has leaves the pod Pending, so the probe times out and reports the
  kubelet's warning.
- **The form** reads `GET /api/provisioner/runtimes` and shows the pick only
  when there is a choice. A pick the provisioner no longer approves stays
  visible and is flagged.

## Tests that hold it

- `envport`: `TestRuntimeChoiceValidate`, `TestRuntimeChoiceChoose`,
  `TestCapabilitiesUnion`.
- `kinds`: `TestKubernetesRuntimeChoice`.
- `integrations`: `TestResolveChoosesTheRuntime`, `TestCheckRuntime`,
  `TestRuntimeChoiceOffered`.
- `envcheck`: `TestRunChecksTheSandboxRuntime`.
- The workspace adapter: `TestEnvRunsUnderItsRuntime`,
  `TestDaemonDoorFollowsTheEnvRuntime`, `TestRuntimeProbe`, and the live
  `TestLiveK8sRuntimeProbe` on k3d.
- `httpapi`: `TestWorkshopRuntimeIsChecked`, `TestRuntimeOffer`.
- `config`: `TestBootApprovedRuntimes`.
- The chart: `chart:lint` refuses `workspaces.runtimes` without the default
  runtime.
- The UI: the `SandboxRuntimeField` tests.
