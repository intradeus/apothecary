# Built-in template: conflict-resolver

2026-10-24, the user: "add a new builtin flow: conflict resolver". A third
template beside `builder` and `reviewer`, for a run created on a merge
request (`on: mr_opened` — the Repositories tab or a merge-request
automation): it brings the merge request up to date with its target.

- **One block, `settle`**: `sync_target` (the engine merges or rebases —
  `$strategy`, `merge` by default so nobody's commits are rewritten unasked);
  a conflict pauses it with the files named and sends the `resolver` agent
  (the builder's resolver, told the change is someone else's: the TASK is the
  merge request, `changes` is `$DIFF_TARGET`) to edit the markers away, up to
  six rounds, then `debug`; the project's tests (`$verify`); `push_branch` to
  the merge request's branch, leased against the head the phase began on.
- **Phases**: `first_pass` (the entrypoint) runs it; `again` (on
  `mr_conflicts`, `start: $again_start`, `confirm` by default) runs it each
  time the target moves and it conflicts anew; `merged` / `closed` end the
  run. It never opens, merges or reviews a merge request.
- **A fork** cannot be pushed to: the push fails to `debug`, where a person
  has the workspace with the resolution in it.
- Tests: `TestConflictResolverTemplate` (shape), and
  `TestBuiltinConflictResolverTemplateRuns` (engine, end to end with the
  fake git: conflict → resolver → merge finished → tests → leased push; a
  second conflict confirmed and settled; closing ends the run).
