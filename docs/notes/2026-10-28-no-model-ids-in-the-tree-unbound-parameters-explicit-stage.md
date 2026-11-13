# No model ids in the tree: unbound parameters, explicit stage harness and model (2026-10-28)

Commit 8d0f33c renamed a model id across the repo and broke four engine tests
on the way: their deliberately absent fixture model became present. The
inventory behind that one incident found about ninety test documents, three
templates, the UI's new-flow skeleton, the dev flow and the docs all carrying
a real model id, for one reason — the parser demanded a literal
`parameters.model` in every document, even when a project binding would have
supplied one. Model ids change under documents that must not, so every rename
was a sweep, and a template pinned yesterday's model until someone noticed.
Decisions, in the user's words: no per-project default; only the flow's bound
override and the run's override, the run winning; each stage declares what it
runs on; two stages pointing at one model parameter from two harnesses fail
the YAML; the dropdowns infer the harness.

- **A parameter may have no default** (`flow.Param.Required`). `harness` and
  `model` always exist, unbound when the document leaves them out; any other
  declaration may omit `default` too. Only a site reading a **model** or a
  **harness** tolerates that (`paramSite.unbound`): `lookup` refuses an
  unbound `run_ref` or timeout at parse time as before. The document parses,
  saves and edits; `Flow.Unbound()` lists what has no value and where it is
  read, `flow.UnboundError` says it the way a run is refused — *"model" has
  no value (read by build, review): bind it in Workshop → Flows → Configure,
  or set it when starting the run* — at run creation right after
  `fl.Resolve`, and as the project preflight's `parameters` step
  (`bindFlow`).
- **Every agent stage states `harness:` and `model:`** — a kind or a
  `provider/model`, or a `$parameter`; the flow-wide fallback is gone
  (`Stage.Model/Harness` are the stage's own, `""` while unbound). The usual
  spelling, `harness: $harness, model: $model`, is now written on every
  template and test stage: 358 insertions by a sweep script, kept
  deliberately so what a stage runs on is read off the stage.
- **Stages sharing a model parameter share a harness** (`roles.go`), judged
  *as written*: the same kind, or the same `$parameter` — a literal at one
  stage and `$harness` at the other is refused even when today's binding
  agrees. The same pass derives `Param.Role` (model | harness, one role per
  parameter) and `Param.Harness` (a model parameter's one harness: a kind or
  `$name`), which is what the forms needed.
- **`default: $other`** (`Param.DefaultRef`): a default that reads another
  parameter — `review_model: { type: string, default: $model }` — same type
  and shape, no cycles; bound wins over followed; an unbound end of the chain
  is reported as that parameter. `resolveParams` fills the chain last, after
  bindings and overrides, so `review_model` follows an overridden `model`.
- **Templates** carry no model: `model` is left out of the parameters block
  with a comment saying why; the builder's `review_model` defaults to
  `$model`. The UI skeleton and the dev flow (`fake/model`) likewise.
- **Forms**: `ParamFields` picks the control by `role`, not by name, and each
  model field suggests the models of *its* harness — `paramValues.ts`
  resolves `Param.harness` against the form's layers (override, binding,
  default, followed default), and the Configure sheet and the start-run form
  fetch one list per harness kind the flow's model parameters need. A
  parameter with no value reads *no value yet*; one following another reads
  *follows $model*. The stage inspector marks harness and model required with
  `$harness` / `$model` placeholders; the parameters editor writes a
  model/harness-read parameter with an empty default as one with none.
- **Tests** never name a vendor: `fake/model` and `fake/other` where a value
  is only required, `acme/alpha-1..3` (+ `alpha-codex/high`, `other/alpha-1`,
  `acme/kappa-3`) where model matching is the subject, `go-model-N`,
  `alpha-1`, `gamma-2`, `luna-6`, `kappa-2` as catalog fixtures in `kinds`.
  `TestBuiltinFlowsCarryNoModel` holds the templates and the shipped example
  to it; `task lint:models` (in `task verify`) greps the tree for
  vendor-looking ids outside the notes and the UX mocks.
- The project-level default harness/model stays absent (2026-07 decision);
  `Resolution.HarnessKind` remains the envcheck fallback, now documented as
  unreachable by a stage since run creation refuses an unbound harness.
