# M18 follow-up — "add to egress" instant unblock

Since the proxy is engine-side in-memory state and the workspace already
routes everything through it, a run's allowlist updates live:
`egressproxy.AllowHost(runID, host)` appends to the run's policy and
clears the host's denial — the agent's next request succeeds with no
restart/re-provision. `POST /api/runs/{id}/egress-allow` does both: the
instant proxy unblock (Engine.AllowEgress) AND persists the host to the
project's workspace.extra_domains (best-effort, for future runs). The run
page's egress-blocked banner grows an "Add to egress" button per denied
host. Host validation rejects scheme/port/path; wildcards (`*.x`) allowed.

Scope note: one proxy process per apothecary instance, but policy is
per-RUN (map[runID]) — stricter than per-project. Each run's allowlist is
its project's derived domains ∪ that project's extra_domains, isolated by
per-run credentials; runs of different projects can never see each other's
allowlist. Per-project egress config already exists as
workspace.extra_domains (what the button persists to).
