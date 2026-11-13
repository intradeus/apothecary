# Live run feedback (working card + workspace log tail)

Between stage_started and a stage's end the UI used to show nothing —
stage logs were post-hoc and the provisioner buffered exec output in
memory. Now:

- `ports.ExecLogTailer` is an OPTIONAL EnvProvisioner capability
  (`TailExecLog(env, n)`). Read-side by design: a sink parameter on Exec
  would have rippled into the AgentRunner path (harness adapters call
  Envp.Exec directly); a tail lets each adapter capture however it wants
  and the API degrade honestly when a provisioner can't stream.
- fake: in-memory per-env transcript, so dev mode and tests exercise the
  same surface.
- `Engine.WorkspaceLogTail` (type-asserts the capability, ErrNoLiveLog
  otherwise) → `GET /api/runs/{id}/workspace-log?n=` (text/plain tail,
  404 when unsupported/no env).
- Run page: non-pending active runs show a Working card — pulsing dot,
  current stage + ticking elapsed (derived from the SSE event stream's
  latest stage_started), and a collapsible autoscrolling live tail
  polled every 2.5 s (paused when the tab is hidden). Transport note:
  nothing here is websockets — run events are SSE, the tail and the
  inbox poll.
