# Flow editor review: where the sidebar disagreed with the canvas

A pass over the graph editor against the document model, checked in a headless
browser against the dev daemon. What was wrong, and what changed:

- **Implicit success read as "done".** YAML may omit `on_success` (next stage
  in document order). The canvas resolved that sugar; the inspector's select
  bound the raw field and showed `done` — `load` and `post` in the default
  flow's review phase. `normalizeDoc` now materializes every success target;
  `docToYaml` already wrote them explicitly, so no saved document changes.
- **Pickers offered illegal targets.** On-success and edge-target lists
  listed every stage, but a route may not leave its phase (a parse error at
  save). They now list the stage's own phase (`routableFrom`). Dropping an
  edge on another phase's stage on the canvas is refused with a notice
  instead of applied; moving a stage to another phase cuts the routes that
  would cross (`pruneCrossPhase`).
- **Trigger signals in wait and gate pickers.** `mr_conflicts` and
  `mr_review_settled` start phases and are refused as a `wait` or an
  auto-answer; the UI's signal type never carried `trigger`/`debounced`. It
  does now, and those pickers offer only waitable signals.
- **Phases had no editor.** A phase's trigger, `quiet_for`, `confirm`, `max`,
  description and id were reachable only through YAML. Clicking a phase's
  trigger marker (or the entry marker in a phased flow) now opens a phase
  view enforcing the parser's rules: quiet_for only for a debounced signal,
  unlimited runs only with confirm, the entry's trigger fixed to `ticket`.
- **Edges were opaque in the stage view.** A defined edge showed as a bare
  `on_fail →` chip; it now spells the chain (`on_fail → fix_commit_msg ×1 ·
  then debug`).
- **Defaults that broke phased flows.** A new edge pointed at the document's
  first stage (usually another phase); it now retries the stage itself (a
  gate's rejection goes back to the stage feeding it). A new fallback link is
  `debug`, the usual last resort. Renaming refuses `start` and non-slug ids,
  as the parser does.

Still YAML-only, deliberately not built in this pass: the flow's `agents:`
block (prompts, contracts, read_only/allow_bash), and adding or deleting
phases.
