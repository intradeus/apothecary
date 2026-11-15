# Environment teardown finishes, or is retried (2026-11-15)

- **Why:** in production (3.5.0, k8s, `storage: pvc`), archives cancelled
  by the client killed `kubectl delete pod` (`exit -1`). `destroySandbox`
  then returned before the PVC, the secrets and the NetworkPolicy. `Archive`
  only logged it, so no `env_destroyed` was written and nothing retried, and
  five archived runs counted against the cap for good. The cap's message
  told the user to archive them, which they already were. Separately,
  `DestroyRunEgress` ignored the gateway's `DELETE /runs/{run}` (and skipped
  it when `Control` said no) and still deleted the `apo-wg-*` secret.
  `AttachRun` allocated addresses from those secrets alone, so the next run
  was given a live orphan's address. WireGuard gives an address to one peer
  only, so the new peer came up with `allowed ips: (none)` and the clone
  failed on DNS.
- **The destroy outlives the request.** `Engine.reclaimEnv` runs
  `destroyEnv` on `context.WithoutCancel` with its own deadline
  (`envDestroyTimeout`, 5 min). Archive, the archive retry and the reaper
  all go through it. It is still synchronous, so a request that stays
  connected gets the result.
- **A failed destroy is recorded once per streak.** It appends
  `env_destroy_failed{env_id, error}` and notifies the owner
  (`Type: env_destroy_failed`). `View.EnvDestroyError` holds the error until
  an `env_destroyed` or `env_ready`, so retries of the same streak add no
  event or notification. `envstate.ErrInFlight` (a rebuild holds the
  workspace) is a deferral, not a failure, and is not recorded.
- **Retries:** `ReapOnce` destroys every `archived` run that still has an
  env, with no window, and retries a completed run's failed reap without
  waiting out the window again (its `env_destroy_failed` moved
  `UpdatedAt`). Failures still go to the `ReaperEnvs` notice. For the manual
  retry, `POST /api/runs/{id}/archive` on an archived run that still has an
  env retries the destroy, answering 409 with the reason if it fails again.
  We chose this over a new `destroy-env` route: archive already means "this
  run's workspace goes", and no other verb would need its own retry. The UI
  shows a **Destroy workspace** button in its place, with the last error
  in the title.
- **Capacity message:** `checkEnvCapacity` lists archived runs that still
  have an env as "workspaces pending cleanup", separate from "archivable
  runs" (terminal and not archived). It never advises archiving an
  archived run.
- **k8s teardown is best-effort:** `destroySandbox` tries the pod, PVC,
  NetworkPolicy and relay secret whatever came before, and joins the
  errors. NotFound was already success (`--ignore-not-found`). In-memory
  state is forgotten only once all deletes succeed.
- **The WireGuard secret goes last.** `Host.Control` now returns an error,
  and a cluster or port-forward failure is no longer read as "no gateway".
  `DestroyRunEgress` returns the lookup or `DELETE` error and keeps the
  secret, so the address stays reserved; it calls `DetachRun` only after the
  gateway let go, or when there is no gateway. `Adapter.Destroy` returns
  that error and keeps the run folder (with its `gwproject` marker) for the
  retry. If the marker is missing, the project is read from the secret's
  `apothecary.io/wg-project` label (`Host.RunProject`). The key names the
  same gateway as the id.
- **Allocation asks the gateway.** `EnsureRunEgress` reads `GET /routes`
  and passes other runs' peer addresses to `AttachRun` as `held`. A new run
  skips them. A run whose own kept address is held by another peer fails
  loudly instead of attaching with no routes. Defence in depth: the gateway
  answers `409` to `PUT /runs/{run}` when the peer address belongs to
  another run.
- **Orphans are pruned.** Each reaper pass calls
  `egressport.EgressPruner.PruneEgress` for every project any run belongs
  to. `Manager.PruneRuns` reads the gateway's table under the project lock
  first and only then asks which envs are live, so an attach in between
  waits rather than being pruned. Live means every run's recorded `EnvID`
  (a failed destroy's included: its retry detaches it), plus the workspace
  name of every run not yet over (`engine.workspaceName`, shared with
  provisioning, because a provisioning run is on the gateway before its
  `env_ready`). Abandoned runs keep their env and therefore their route.
  Each pruned peer is logged as a warning.
- **The 14 orphans of failed provisions [INFERENCE]:** no failed-provision
  path deletes the `apo-wg-*` secret. `Provision` returns what it left, and
  `env_failed` records it. Only `DetachRun`, via `DestroyRunEgress`, deletes
  the secret. Those runs were therefore destroyed later (archived) while
  `Control` read a lookup or port-forward failure as no gateway, or while
  their run folder had lost its `gwproject` marker. Either way the
  `DELETE` was skipped. Both paths now keep the secret, or find the
  project.
- **Tests that hold it:**
  - gateway: `TestControlRunsEdges` "a peer address another run holds is
    refused".
  - `egressgw`: `TestDestroyRunEgressKeepsTheLinkUntilTheGatewayLetsGo`,
    `TestDestroyRunEgressFindsTheProjectFromTheLink`,
    `TestAttachRunSkipsAddressesTheGatewayHolds`,
    `TestEnsureRunEgressHandsTheHostTheGatewaysPeers`,
    `TestPruneRunsDetachesWhatIsNotLive`,
    `TestPruneRunsReportsGatewayFailures`, `TestRunProjectReadsTheLink`,
    `TestK8sControlLookup`.
  - `workspace`: `TestK8sDestroyGoesPastAFailedPodDelete`,
    `TestDestroyFailsWhileTheGatewayKeepsThePeer`,
    `TestDestroyWithoutTheMarkerAsksTheLink`, `TestPruneEgressKeepsTheLive`.
  - `engine`: `TestArchiveDestroyOutlivesTheRequest`,
    `TestArchiveDestroyFailureIsRecordedAndRetried`,
    `TestReaperRetriesAFailedReapAtOnce`,
    `TestEnvironmentCapacityNamesPendingCleanup`,
    `TestReaperPrunesTheGatewaysOrphans`,
    `TestWorkspaceNameIsTheProvisionedOne`.
  - `run`: `TestEnvDestroyFailureKeepsTheEnv`.
  - CPUI: `RunControls.test.ts` "destroy a stranded workspace", plus
    `derive.test.ts` and `mappers.test.ts`.
- **Live check:** not run here. It needs a k8s cluster with a gateway: a
  `curl --max-time 1` archive, then blocking the gateway's control port
  during a destroy and starting a second run.
