# The hub: what the run waits on between its chains

2026-10-24, the user: watchers "are just a line under" the main chain. The
canvas now draws what a run does: the main chain, then the run **waiting** on
its merge requests, and what branches from that.

- **The hub** (`FlowHubNode`, layout key `hub`) stands, once a flow has
  watchers or outcomes, left of the marker column under the main chain: the
  main chain's `end` routes land on it (a drop on it means `end`), and one
  dashed line runs from it down to each watcher's and outcome's marker. A flow
  without a main chain starts at it.
- **Each chain has its own ends**, at the end of its row: a watcher's `end`
  (back to waiting), an outcome's `done` / `abort` (`__end:<phase>`, layout
  keys `end:<phase>` …, `chainEnds`, `terminalKey`). Nothing routes across the
  canvas to a shared terminal any more; the shared ones are the main chain's.
- **Frames**: each watcher and outcome is wrapped in a faint wash of its
  marker's hue (a `ViewportPortal` behind the nodes, from where they stand
  now, so it follows a drag); its corner folds the chain to its marker, which
  counts what it hides. Dragging a marker takes its chain along, saved where
  it lands.
- Dataflow lines join dots within a chain only: a watcher reading the main
  chain's plan shows the path on its dot instead of a line across the canvas.
- `autoLayout` and `placeChain` place the hub (on a row of its own above the
  first watcher or outcome) and each chain's ends; a layout stored before
  them still opens sensibly — the hub left of the first marker, each chain's
  ends right of its last stage — and auto-arrange redoes it.
