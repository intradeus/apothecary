# Built-in template: reviewer

Second pass, step 6 (first piece), 2026-10-24 — ahead of the Reviews tab,
which needs a flow to start.

- `internal/builtin/flows/reviewer.yaml`: created on someone's merge request
  (`first_look` on `mr_opened`), reviews it through one `review` block —
  `pr_reviewer` (read-only, bash for tests; `$DIFF_TARGET`, `$DECISION`,
  `$FAILURE`) writes a `review_threads` file, an optional gate
  (`$review_gate`, off by default) lets a person read it, `post_review` posts
  it — and again from a reaction on `mr_head_moved` after the author's pushes
  stop for `$rereview_quiet` (10m). Outcomes `merged`/`closed` say `then:
  done`: either way the review is over. Never pushes, opens or merges (the
  builtin test holds that).
- Re-reviews were meant not to repeat themselves by reading their earlier
  review file. That never worked: the engine deletes a stage's outputs
  before every fresh attempt (M18), so the re-review never saw it. Since
  2026-10-30 the sequence polls the merge request first (`load_review_comments`
  with `scope: all`) and the engine refuses a repeated note — see *Review
  memory* below.
- Notes are prefixed `[blocking]`, `[suggestion]`, `[question]`, `[nit]`, at
  most fifteen (the action allows fifty): the summary takes the rest.
- `TestBuiltinReviewerTemplateRuns` drives the shipped YAML itself through
  the engine harness (harness/model swapped for the fake).
