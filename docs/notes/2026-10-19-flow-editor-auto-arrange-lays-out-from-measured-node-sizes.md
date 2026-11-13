# Flow editor: auto-arrange lays out from measured node sizes

`autoLayout` used to assume every node was 240 px wide and 160 px tall. A
stage node is 190–300 px wide and grows a row per dataflow dot, so wide
columns collided and the off-chain lane landed on tall chain nodes ("the order
is right, the nodes overlap"). It now takes the sizes Svelte Flow measured
(`node.measured`, reported by `FlowGraph.measuredSizes()` keyed like the
layout map): each column is as wide as its node, each row as tall as its
tallest, phase bands stack by their real height, and the terminals and
trigger markers are placed from the same sizes. The arrange button passes the
current sizes; a flow opened without a stored layout is placed with guesses
and re-placed once `FitOnInit` sees the nodes measured — before the fit, so
the viewport fits the final picture. Nothing is persisted by that re-place:
the next drag or arrange saves.
