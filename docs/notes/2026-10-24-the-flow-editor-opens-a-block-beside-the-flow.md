# The flow editor opens a block beside the flow

2026-10-24, the user: an include node only said "include" — "not being able
to see in the flow what this node does". Two changes in the editor:

- **The node says what it runs.** Its chip reads *block* (`STAGE_WORD`), its
  line the block's chain (`review: read → gate → post`), its tooltip the
  block's description.
- **Selecting it opens the block beside the flow.** The canvas splits: the
  flow on the left, the block on the right (55%), drawn by the same
  `FlowGraph` in a read-only *block mode* (`block`, `readonly` props): a
  marker named after the block, its stages stacked top to bottom
  (`columnLayout` — a block is a short chain, read best down a narrow pane),
  and its two ways out instead of the run's ends — *back · X* (the including
  stage's success, X resolved) and *fail · Y* (its on_fail), new terminal
  tones `exit` / `failexit`. An include inside the block opens that block in
  its place, with a breadcrumb back. Selecting anything else, clicking the
  empty flow canvas or Escape closes the pane; clicks inside it keep it open.
  Both graphs fit again on every open and close (`FitOnInit` `refit`,
  animated, after the canvas has resized), the block's with a lower legible
  floor so it always shows whole.
- Blocks are still edited in the YAML tab; the view is for reading.
