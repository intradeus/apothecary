# Ports: every value a stage reads or writes, typed, with its source

Same day, after a long design exchange with the user, which rewrote the flow
grammar in place (still `version: 5` — the app is unreleased, so no upgrader).

- **What the user asked for, in order.** Stages should declare their inputs
  and outputs "as a function does" — typed, without pointing at a location —
  and "more declarative = better": every stage restates its I/O even where an
  agent's or an action's contract already says it. Rejected along the way:
  edges passing every argument (the function-call model taken literally:
  ~19 edge sites re-passing `plan` in the builder, and chains opened by a
  signal have no caller); `producer.output` references (a reader would name a
  stage); a named run scope (names would need a uniqueness that paths
  already give); `$IN_`/`$OUT_` environment variables for scripts (the user
  dislikes them — scripts keep literal paths).
- **What it is.** A port is `{from | to, type | schema, description?,
  optional?}`. Sources: a path some stage writes, `repo:<path>`, `run.*`
  (ticket, git.status/diff_target/diff_phase, mr.url, workspace) and
  `edge.*` (failure, decision). Paths stay the wiring — a path written by
  several stages gives the most recent, two stages to a third use two paths
  — so "functions over a typed run scope" is the workspace's files keyed by
  path. Edges carry control and only the transition's payload.
- **Contracts say what, stages say where.** Agent decls are typed and lose
  `path:`; a stage states every output and every required input, and may
  narrow (add a schema), never change a type. Actions: file ports typed in
  the registry (`PortSig`), literals under `with:` (`SettingSig`); every
  action output is stated (load_review_comments' `review`). create_mr's link
  became `run.mr.url`, not an output.
- **The TASK section is a port now** (`run.ticket`, type `ticket`): an agent
  without one never sees the ticket — the conflict resolver deliberately.
- **Checks became errors**: nothing writes a path (the hint is `repo:`),
  not written on every route (unless `optional`), writers disagreeing, an
  edge source no edge fills. A typo'd `run.tiket` suggests `run.ticket`
  (Levenshtein ≤ 3). The runtime checks every stage's file inputs before it
  runs and agent/command outputs after — except an action with a pending
  effect, which reconciles first (caught by the test-conversion agent: a
  retried ticket_comment on a rebuilt workspace).
- **Editor**: `PortRows` replaces `BindingRows` — contract rows for agents and
  actions, free rows for commands and gates, a source datalist (engine
  sources + paths the flow writes), type and schema selects; a value the
  engine provides fixes the type. The doc model keeps the wire shape
  (`input`/`output` maps of `DocPort`, `with` a string map).
- The `{path, schema}` command long form from earlier the same day is gone:
  every port carries its shape.
