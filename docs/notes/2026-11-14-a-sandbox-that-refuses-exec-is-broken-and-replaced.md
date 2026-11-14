# A sandbox that refuses exec is broken, and replaced (2026-11-14)

- **Why:** on a production k3s + sysbox node, `needrestart` restarted
  containerd and the sysbox services after an unattended libc/systemd
  upgrade. sysbox-fs then no longer tracked the running sandbox pods, and
  every `kubectl exec` into them failed with `OCI runtime exec failed: …
  unsafe procfs detected: openat2 fsmount:fscontext:proc/thread-self/fd/:
  operation not permitted`. kubectl wraps that in `Internal error
  occurred`, a transport sign, so the status probe errored, the reconciler
  recorded `unknown`, the run page blamed the integration token, recovery
  stopped at `recovery: env status`, and Rebuild failed the same way: it
  reused the still-`Running` pod. Only deleting the pod by hand helped.
- **`broken` is a status, not an error.** `kube.IsExecRefused` is true only
  for a `TransportError` whose stderr carries `OCI runtime exec failed`
  (never a command result, never another transport failure).
  `k8sHost.runInner` wraps such a failure, after `runWith`'s retries, in
  `errSandboxBroken`, and `Adapter.workspaceState` reads it as
  `envport.EnvBroken` with no error: the provisioner did answer, and an
  error would keep meaning "cannot tell" (the reconciler's `unknown`, and
  recovery waiting). `devcontainer.go` stays host-blind: it only knows the
  sentinel.
- **Readiness caveat [INFERENCE, unverified live]:** whether kubelet's exec
  readiness probe (`docker version`) takes a sysbox-broken pod out of Ready
  is not known. So `sandboxState` probes a Running-but-not-Ready pod with
  `true` and returns `broken` when it is refused, and `ensurePod` probes
  every Running pod it finds, Ready or not. The cost is one exec per status
  pass, for not-Ready pods only.
- **Replacement lives in `ensurePod`**, which runs inside Provision and
  Rebuild, never in the reconciler. A Running pod whose probe is refused is
  deleted (waiting for it) and recreated. Its claim survives, so the
  checkout, agent commits and the inner daemon's `/var/lib/docker` subPath
  stay. With `storage: emptydir` the log line says the checkout goes with the
  pod. Any other probe failure keeps today's reuse path.
- **Recovery heals on its own.** `engine.ensureEnv` already re-provisions
  every status that is neither `running` nor `absent`, after one reprobe.
  A run the engine restarted under (the incident's "left non-terminal for
  recovery") therefore gets a new pod and a rewritten `run.env`, instead of
  sitting in `running` with no loop, which neither Pause nor Rebuild can
  touch. The owner chose this over a banner-only fix.
- **Compose Rebuild empties the sandbox after prepare, and the compose
  check moved there too.** `provision` takes `rebuild bool` instead of
  `removeExisting bool`. After `prepare` (so after `ensurePod` may have
  replaced the pod), a rebuild reads `p.def.IsCompose`: compose →
  `resetSandbox`, then `up` (a failure keeps the `rebuild <name>: teardown:`
  wording); otherwise `up --remove-existing-container`. The plan kept the
  compose check in `Rebuild` and passed a three-way mode. But `IsCompose` is
  `devcontainer read-configuration` run in the pod: on a pod refusing exec
  it reads "not compose", so a compose workspace would have been rebuilt
  with the single-container flag instead of being reset. `prepare` creates
  no inner containers, volumes or networks, so the reset prunes nothing it
  made.
- **The shim's 401s:** run tokens live in engine memory and are minted at
  every loop start. Recovery minted a new one, but `writeRunEnv` runs only
  after a successful `Status`, so `run.env` in the pod kept the old token.
  The shim's port scanner then retried `observed-ports` every 3s. Now it
  backs off on 401 (3s doubling to 2 min, `authBackoffBase`/`authBackoffCap`),
  re-reading `run.env` on every attempt, and resets on any other answer. A
  replaced and bootstrapped pod gets a new `run.env`, which ends the 401s.
- **UI:** `broken` gets a danger badge with a dashed dot, and the tooltip
  says the container runtime stopped responding. A danger banner says the
  same and that the checkout is kept. Its action follows the header's
  rule, now one `RunStore.canRebuild`: Rebuild when the run is paused or
  over. Otherwise it offers Pause, with a "pause, then rebuild" line, because
  the engine refuses to rebuild under a live loop. `RunPage` owns
  `HeaderActions`, so a rebuild started from the banner shows busy in the
  header too. The `unknown` banner is unchanged.
- **Host:** `deploy/k3s/install.sh` writes
  `/etc/needrestart/conf.d/apothecary.conf` (`override_rc` 0 for
  `qr(^sysbox)`, `qr(^containerd)`, `qr(^k3s)`) whether or not needrestart is
  installed yet. `docs/kubernetes.md` gives the file for clusters installed
  any other way, or before this change.
- **Out of scope:** detecting the restart before anything execs (comparing
  sysbox-mgr's start time with each pod's). The reconciler reports `broken`
  within one pass anyway.
- **Tests that hold it:**
  - `kube`: `TestIsExecRefused`.
  - `workspace`:
    - `TestK8sStatusMapping` (refused Ready and not-Ready pods are `broken`;
      an API outage is still an error);
    - `TestK8sEndedPodIsReplacedAndALiveOneKept` (replacement, the pvc and
      emptydir log lines, no replacement on another transport error, a
      failed delete stops it);
    - `TestK8sRebuildReplacesAPodRefusingExec` (single-container and
      compose: pod replaced, claim kept, the reset and build on the new pod);
    - `TestRebuildComposeResetsThenProvisions` (reset after prepare, before
      `up`) and `TestRebuildTeardownFailureStopsIt`;
    - `TestStatusMapping`'s `broken` row.
  - `engine`: `TestRecoveryReprovisionsABrokenWorkspace`.
  - shim: `TestPortScanBacksOffOn401UntilTokenRefreshed`.
  - CPUI: `WorkspaceStatus.test.ts`, `stats.test.ts`,
    `store.svelte.test.ts`, `header/rebuild.test.ts`, and `RunPage.test.ts`'s
    "a broken workspace".
- **Live check:** needs a k8s + sysbox node (`systemctl restart sysbox`
  under a live run, then the banner, Rebuild, and an engine restart while
  broken). It is left to the verify stage; not run here.
