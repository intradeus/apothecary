# The rest of the UI, audited for flow-agnosticism

Same rule as the Repositories tab: nothing in the UI assumes one particular flow,
its stage ids, its agent names or its artifacts. What the audit found and
what changed:

- **New flow was broken.** The skeleton named an agent `builder` it never
  declared, so the document did not parse. It now declares one neutral
  agent (`worker`, stage `work`); `apothecaryd validate` accepts it.
- **Plan review → document review.** The workbench reviews whatever file a
  ping hands over (`PlanReview.svelte`/`planreview.ts` →
  `DocReview.svelte`/`docreview.ts`, `plan.*` → `doc.*` strings). Markdown
  is rendered and anchored by heading as before; any other file (YAML,
  JSON, code) is shown as written, split at blank lines into listings that
  comments anchor to by line. The feedback is headed `REVIEW OF <path> — N
  comments:` (was `PLAN REVIEW`), the call to action is `Review <file>`
  (was "Review plan") and the page title names the file (was "the recipe").
  The reviewer template's gate on `artifacts/review.yaml` reads right now.
- **Run page.** The stage strip shows the phase the run began at, then every
  other phase in the order it first started, grouped and labelled — no
  phase is the main one (was: the begun phase, other stages tacked on). The
  header's merge-request slot shows the merge request a run was created on
  (`adopted`, new on the run in the UI) and the ones it opened; the "not
  opened yet" placeholders appear only when the pinned flow has a
  `create_mr` stage, and a run with neither shows no slot.
- **Wording.** "builder" (composer placeholder, agent note), "keeps the
  plan, build and commits" (retry), the agent's to-do list called "The
  recipe" (now "Checklist"; *recipe* stays the lore word for a run's flow),
  outcome text naming merges and closes, "review verdict: APPROVE", the
  project form pre-filling a `verify` command, and the flow editor's
  `builder` / `artifacts/plan.md` / `verify` / "implementation plan"
  placeholders — all made neutral. `in_review` keeps its label: the
  projection sets it only when the run has open merge requests.
