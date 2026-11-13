# Budgets pause; retry provisions; provisioning is open and legible (2026-11-10)

- **A spent budget pauses the run.** `checkBudget` no longer abandons: it
  returns a `budgetPaused` error the loop's exit turns into
  `run_paused{reason: budget, note: <the ceiling that tripped>}`. The run
  reads `paused` (view `pause_reason`/`pause_note`), its work and workspace
  stay, and play grants another budget's worth: `RunUnpaused` after a budget
  pause sets `Facts.BudgetCostBase/BudgetTurnsBase` to the spend at that
  play, and `Facts.BudgetSpend` is what the ceilings are held to. The fold
  derives the window, so no new event field and replay agrees with the live
  state. The `apothecary.budget.stops` metric counts budget pauses. What
  still abandons a run: abort, the flow's `abort` terminal, and a dead end in
  the main chain or an outcome (an absent or exhausted failure edge, giving
  up at the debug leaf or a manual stage, a missing MR body). Retry accepts
  all of them in the same workspace.
- **Retry after `env_failed` provisions again.** The view gets
  `provision_failed`. `RetryRun` accepts such a run whether or not a stage
  started or anything was left, and records `run_retried{provision: true}`.
  runcore then clears the env id and `EnvRequested` and leaves the cursor
  where the failure caught it, so the loop requests the workspace again
  under the same name (the provisioner picks up the leftovers) and
  bootstraps it. A failed bootstrap left a running container with no
  checkout, so re-attaching to it would have skipped the bootstrap. Rebuild
  stays what it was: recreating a workspace that exists.
- **Provisioning egress is open.** The gateway keeps `Routes.Open`
  (run → deadline), set by `PUT /runs/{run}/open` and cleared by
  `DELETE /runs/{run}/open` or a detach. An open run's :80/:443 traffic is
  judged by `verdictOpen`: everything but the project's blocked domains,
  and in deny mode its denylist. Its raw egress is accepted by an nft
  `ip saddr` rule placed after the protected-destination drops. The
  provisioner opens the window as soon as the run is attached (docker and
  Kubernetes), with the provision timeout plus a minute as the deadline,
  and `provision` closes it on every exit. The deadline, enforced by the
  gateway's watch loop, closes a window an engine killed mid-build never
  closed. Per run, never a project-mode flip: other runs of the project keep
  the policy. A window the gateway refuses is logged and provisioning goes on
  under the policy.
- **The provisioning log says where it is.** Every step line carries
  `[+mm:ss]` since provisioning began. New steps: the checkout's repository
  and branch (userinfo stripped), the definition, container preparation,
  connecting to the engine, waiting for the workspace, and on Kubernetes
  reusing, creating or replacing the pod and cloning into it. While waiting
  for the pod, its phase and each unready container's state
  (`ImagePullBackOff: …`, exits, restarts) are written whenever they change
  (`kube.ContainerStatus.State`). The log ends with `=== workspace … ready
  in …` or `=== provisioning failed after …: <cause> ===`.
- **Relay links name the UI's host.** A relay bound to every interface
  cannot know the machine's reachable name. `portrelay.ForHost` rewrites
  its `localhost` URLs to the host of the request that asks (expose, open
  editor, the ports list, service previews). A specific bind, host routing
  and a request on localhost are left alone. `SSHAdvertiseAddr` no longer
  refuses a wildcard door with nothing to advertise: it returns `:port`, and
  `GET /runs/{id}/ssh` fills in the request's host. Residual: those
  listeners are raw TCP without sign-in, as they always were on a wildcard
  bind. `APOTHECARY_APPS_DOMAIN` is the mode behind the session. code-server
  over plain HTTP on a non-localhost origin has no secure context, so its
  webviews need TLS in front.
- **Thinking** reads in the body face at the label size. A thought you open
  or close keeps that state, keyed by its segment, through new messages and
  through the live thought giving way to the recorded one. Thoughts you
  never touched still follow the stream.
