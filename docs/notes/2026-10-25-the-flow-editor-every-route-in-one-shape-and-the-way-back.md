# The flow editor: every route in one shape, and the way back

2026-10-25, the user, of the stage panel: "why is this different for on
success vs on fail/reject/timeout?" — the success target was a dropdown, the
failure routes chips that opened an edge editor. The data differs (one target,
always there, vs optional chains of links with a limit and fallbacks), the
editing need not:

- **Routes**, in the stage panel: one row per link, the same shape for all —
  the route's kind in its hue, its target, and for a failure link to a stage
  ×N (∞ where a person decides each time: from a gate, or to one); `then`
  rows for the fallbacks, "+ then", × to remove a link (the links after it
  move up; the last one takes the route with it), "+ on_timeout" for a route
  the stage lacks. The separate edge panel is gone: selecting an edge — on
  the canvas, or now by its label, which Svelte Flow reports no click for
  (`data.pick` in `FlowLabeledEdge`) — opens its stage with that route picked
  out and scrolled to.
- **The way back** — "when we go into those sub-editors, can we add a go back
  button which goes back to the stage we were just looking at": the inspector
  now moves two ways, `onselect` (another selection in this one's place — a
  rename, a delete) and `onopen` (from this panel into another — Edit agent,
  Edit sequence, a sequence's stage, Run by, the flow panel's chips, a stage
  just added). The page keeps the trail `onopen` leaves; every panel offers
  "← stage build" (agent, sequence, watcher, *The formula*) back along it.
  A rename renames what the trail remembers (a sequence's stages under the
  sequence's new name), a delete goes back where it came from, a panel that
  no longer exists is skipped, and selecting on the canvas starts afresh.
