# Blocks are sequences

2026-10-24, the user: "I don't like block" — and of five names and five more,
"just go with sequence". "Template" was out: it already means the built-in
flows (builder, reviewer, conflict-resolver). One word now names the concept
everywhere:

- **Grammar** (still `version: 5`, unreleased): `sequences:` lists the chains
  written once; a **sequence stage** runs one in its place with `sequence:
  X` (was `include:`); a watcher or an outcome that only runs one says
  `sequence: X` itself (was `run: X` — which also freed `run:` to mean only
  a command's script). The main chain is the sequence `main`. The old
  `blocks:`, `include:` and watcher `run:` are refused with the new spelling
  (`Doc.LegacyBlocks`, `DocStage.Include` among the retired fields,
  `DocWatcher.LegacyRun`).
- **Code**: `internal/flow/sequences.go` (was `blocks.go`), `DocSequence`,
  `Stage.Sequence` (the path of sequence stages a stage was expanded
  through), `maxSequenceDepth`; the parser's messages say "sequence" and
  "sequence stage". The templates' `review` and `settle` are sequences.
- **UI**: `StageKind` `'sequence'`, `DocSequence`, `SequenceView`, the
  sequence pane beside the flow, the inspector's *Sequence* field, the node
  chip *sequence*; the API document carries `sequences` and a stage's
  `sequence`.
- **Not renamed**: the plain word — the "settings block" of parameters,
  blocked hosts, blocker pings — and the history in this file.
