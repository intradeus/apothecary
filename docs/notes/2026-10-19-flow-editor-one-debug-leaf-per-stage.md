# Flow editor: one debug leaf per stage

The canvas used to draw `debug` as a single node every `on_fail: debug` /
`then: debug` edge converged on — a sink, like `abort`. It is not one: taking a
debug link parks the run with the cursor still on the stage that took it
(`phDebug`), the ping names that stage, and *continue* re-enters it. The editor
now draws one leaf per stage that escalates (`__debug:<stage>`, layout key
`debug:<stage>` like `trigger:<phase>`), beside that stage, with a dashed
return edge back to it ("then <stage> again"). `autoLayout` gives each row a
strip for its leaves. Dropping an edge on any leaf still means `to: debug`;
clicking a leaf or its return edge selects the stage. The YAML, parser and
engine are untouched — `debug` stays a reserved edge target, and the old
`layout.debug` key is simply unused. `abort` and `done` stay shared: those are
terminals.

The flow canvas is the one surface drawn opaque (`--xy-background-color:
var(--solidBg)`, the theme's "glass made opaque" token): the sigils turning
behind the shell read through a transparent canvas and fought the edges and
labels. Everything else keeps its glass.

Edge labels sit by their source, not at the path midpoint (`FlowLabeledEdge`,
edge type `labeled`). xyflow centres a label on its path, so a retry loop's
label landed on the node it loops around and every edge leaving a stage put
its label in the same crowded middle. Now each failure link's label hangs
under the handle it leaves from, one row per link of that stage
(`flowdoc.edgeLinks` — the single list both FlowGraph's numbering and
autoLayout's reservation read), and `autoLayout` leaves those rows free under
every row of nodes, above the debug-leaf strip. A link to `debug` no longer
spells out `→ debug`: its leaf sits right below. A leaf's return edge carries
a short "↻ again" to the right of the leaf.
