# Compiled-in defaults (internal/builtin): flow "default" + agent library

The product now ships working defaults in the binary instead of requiring
manual seeding: internal/builtin embeds flows/default.yaml (the plan-gated
build with manual-debug rescue) and the five agents it references
(planner, builder, committer, reviewer, redactor). Nothing is ever written
to postgres; the layering is read-through with DB-wins shadowing:

- Engine: builtin.FlowSource(conf.FlowSource()) — a missing name falls
  back to the embedded flow, so an empty database still runs "default".
- resolve: agent names the project hasn't authored resolve to the
  embedded specs (hashed with the same content addressing); preflight and
  prompt materialization see them like any authored agent.
- confstore: project.default_flow defaults to "default" and validates
  against DB-then-builtin. DeleteAgent's referenced-guard allows deleting
  an override whose name has a builtin fallback (that IS reset-to-default)
  and keeps refusing when deletion would break the default flow.
- API: flows/agents responses carry builtin / shadows_builtin flags;
  GET serves embedded content when unshadowed; saving under a builtin
  name creates the editable override (copy-on-write); deleting the
  override resets; deleting an unshadowed builtin is a 409.
- UI: "built-in" badges, editor banner, "Reset to built-in" delete label,
  AgentsPanel "Customize" instead of Edit/Delete for builtin rows.

Upgrades propagate by rebuild (a seeded row would freeze at seed time);
runs keep pinning content hashes, so the existing drift warning covers a
binary upgrade changing the default. examples/agents/ remains as
documentation; the embedded copies under internal/builtin are canonical.
