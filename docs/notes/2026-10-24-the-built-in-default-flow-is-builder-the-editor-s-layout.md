# The built-in "default" flow is "builder"; the editor's layout per phase

2026-10-24, three fixes the user asked for together:

- **Opening the built-in flow crashed the editor** ("Cannot read properties
  of undefined (reading 'on_success')"). `autoLayout` walked a phase's
  success chain until `end` or `done` — the `closed` outcome ends on
  `on_success: abort`, so it looked `abort` up as a stage. The walk now stops
  at anything that is not a stage of the document.
- **Renamed: `default` → `builder`** (`internal/builtin/flows/builder.yaml`).
  *Default* still means what a project starts runs with
  (`projects.default_flow`); the template is called by what it does.
  Migration `0007_builder_flow.sql` moves
  what named the built-in — a project's default flow, its binding, its
  automations — and leaves alone a project's own saved flow named `default`
  (a flow's YAML `name:` must equal its id, so a copy is not renamed behind
  its back). Runs keep the flow name they were created with.
- **Phases as rows, beginnings in a column.** Auto-arrange already gave each
  phase its own band; now every phase marker (the entry's and each later
  phase's) starts at the same x, the widest one setting the column, so the
  entrypoints read down the left edge. Arranging fits the view again, and a
  fit that has to zoom in to stay legible anchors a flow taller than the
  canvas at its top-left — the entry — instead of its vertical middle.
