# Flow v4: blocks — chains written once, expanded at parse time

Second pass, step 2 (2026-10-23). The default flow's model repeats chains
(commit + test after every agent change; plan … publish in deliver and
bugfix), and copying them invites the copies to drift. A block is written
once under `blocks:` and run by a stage that `include:`s it.

- **Grammar.** `blocks:` is a list like `phases:` — `{id, description,
  stages}` — rather than the `name: [stages]` mapping sketched while
  modeling: document order and line numbers come for free, the strict
  decoder applies inside it (a custom mapping unmarshaler would have
  bypassed `KnownFields`), and a block can say what it is for. An include is
  a stage `{id, include, description?, on_success?, on_fail?}` with no type.
- **Expansion is the parser's** (`internal/flow/blocks.go`), at the
  `DocStage` level inside `buildPhases`: every later rule — routes stay in
  their phase, `create_mr` once per phase, reachability, contracts, the done
  rule, the 128-stage limit (counted after expansion) — runs on ordinary
  stages, and the engine is untouched. Ids are the include path
  (`make.ship.test`; `idPathRe`), hand-written ids stay slugs, so nothing
  collides. `Stage.Include` records the path for whoever wants to group.
- **Routes.** In a block: its own ids (an include's id = its first stage,
  `entryOf`), terminals, and `fail` = the include's `on_fail`, *spliced* where
  it is said (each stage saying it gets its own copy and counters — a macro,
  not a shared budget). An include without `on_fail` makes `fail` a dead end.
  The block's last stage without `on_success` takes the include's; an include
  without one leaves it empty and the flattened sugar (next stage, or the
  phase's end) does the rest — which is why nested includes need no special
  case. In a phase: an include's id resolves to its first stage; a dotted
  target (into an include) is refused.
- **Checked whether used or not**: block ids, local ids (`fail` reserved
  everywhere now), include names, cycles and depth ≤ 3 (`blockDepth`). Only
  the expansion checks the stages themselves, so an unused block warns.
- **Lines**: each `DocStage` carries its source line (unexported) through
  the expansion, so an error inside a block points into the block.
- **cpui.** The editor keeps blocks as written (`FlowDoc.blocks`, emitted
  before `phases:`; their stages keep their success sugar) and shows an
  include as one node — kind `include` in `StageKind`, the block picker and
  the block's expanded stage list in the inspector, editing the block itself
  in the YAML tab. What shows a *run* — the stage strip, run facts, the
  agents tab — goes through `expandDoc`, a TS mirror of the expansion, since
  the events name expanded stages; checked against the Go expansion on the
  test document, both for the server's JSON and for raw YAML parsed in the
  browser (whose scalar edges `normalizeDoc` now unfolds).
- Not done: blocks as bands on the canvas (edit in place), parameters passed
  into a block.
