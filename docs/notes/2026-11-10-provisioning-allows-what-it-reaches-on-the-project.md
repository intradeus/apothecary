# Provisioning allows what it reaches, on the project (2026-11-10)

- The provisioning window used to let a run reach anything but what the
  project blocks, and remember nothing. The hosts went through unrecorded
  as rules, so the egress view had nothing to rule on. Once the window
  closed, the next run's agent met the same hosts held.
- The gateway now marks an audit record `provisioning` when the window
  alone let the connection through (allow mode, a host the policy would
  have held). In deny mode the window is the policy, so nothing is marked.
- The engine's egress ingest (every 5 s, and once more before destroy)
  adopts each marked hostname. It saves the hostname to the project's
  `extra_domains` first, then allows it live on the gateway. Adoption is
  the same as a person clicking Allow. Hosts that are blocked, or ruled on
  since, are left alone. IP literals stay held. Each host is looked at
  once per run, so taking it off the allowlist mid-run sticks. The
  project save (`confstore.SaveEgressRule`) is shared with the HTTP rule
  endpoints, which used to duplicate it.
- The gateway counts a connection when it is let through and adds its
  bytes when it ends, so a long download is on the trail from its start.
- The run page treats a provisioning run as live (`RunStore.workspaceLive`).
  The egress view loads the policy, so a host can be blocked
  mid-provision, and the workspace log is tailed from the first line of
  the clone instead of once.
- `k8sHost.sample` reports the working set (`memory.current` minus
  `inactive_file`), as the kubelet and `docker stats` do. A build's page
  cache read as a workspace about to run out of memory.
