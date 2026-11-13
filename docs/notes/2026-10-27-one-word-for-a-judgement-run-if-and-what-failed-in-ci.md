# One word for a judgement, `run_if`, and what failed in CI

Three renames and a source (2026-10-26).

- **verdict, everywhere.** `edge.decision`/`type: decision` are `edge.verdict`/
  `type: verdict`: a person's review at a gate and an agent's verdict file are
  one thing now, so they have one name. `verdict` stays a file type and is the
  one an engine source also gives (`SourceVerdict` → `TypeVerdict`); the type
  rules needed no exception beyond the source table, since a port's source
  already says file or value. `flow.Decides` → `GivesVerdict`,
  `engine/decision.go` → `verdict.go`.
- **The verdict file has the review's shape.** `{verdict, summary?, comments:
  [remark | {path, line?, end_line?, quote?, body}]}`, a built-in JSON Schema
  (shown in the OUTPUT CONTRACT, checked with the outputs, as commit_message)
  plus `parseVerdict`'s rules a schema cannot say: a REJECT says why (a
  summary now counts), a path `filepath.IsLocal`, a range runs forward.
  `ports.Verdict{Approve, Summary, Comments []VerdictComment}`. One renderer
  for both givers (`renderVerdict` → `renderReview`): remarks first, then by
  file — the gate's order, else first mention.
- **yes_no.** The answer kind of approve/reject questions (gates, watcher and
  outcome confirmations, the debug leaf) was also "decision"; it is `yes_no`.
  Migration 0011 rewrites `pings.answer_kind` and, the app being unreleased,
  the `ping_raised` events themselves rather than reading both spellings.
- **`run_if:`** replaces a stage's `when:` (Go `Stage.RunIf *Cond`,
  `Cond*` terms). A document still saying `when:` gets the retired-name
  message with the line rewritten.
- **`run.ci.failure`** — what failed, never the log: `RepoHost.CIFailures`
  (GitHub: the newest check run per name; an Actions run is its job, whose
  failed steps and workflow run's `path` — a called workflow's `@ref` cut —
  say where; an app's run its output title; a failed status its description.
  GitLab: the head pipeline's failed jobs, not allowed-to-fail, with stage
  and the project's `ci_config_path`). Read once in `beginPhase` for a chain
  that reads the source, kept on `phase_started.ci` (replayed into `rs.ci`,
  parked with the phase run), rendered by `ciFailureText`. Only a `ci_failed`
  watcher's stages may read it (the parser says so); the editor offers it
  there only. `builder` gains the `ci_fix` watcher (`$ci_start`, default
  confirm) and a `ci_fixer` agent told to re-run the failed steps itself and
  not to loosen CI.
