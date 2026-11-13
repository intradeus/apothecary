# The `debug` leaf (TODO 28 retired, 2026-10-19)

`debug` is a reserved edge target (`flow.ReservedDebug`, next to `ReservedStart`
— not a terminal: the run comes back from it). `then: debug` / `on_fail: debug`
replaces the ~20 `debug_<stage>` human gates the default flow carried. Taking
the link records `edge_taken{to: debug, max: -1}` and parks the run in a new
interpreter phase (`phDebug`) with the cursor still on the failing stage; the
engine raises a `ping_raised{type: debug, stage_id: <that stage>}` carrying only
facts — the `$FAILURE` text as context, the workspace link, the label
"<stage> failed" — and gives its slot back (`errWaitingOnHuman`). The card is
the UI's ("Stage X failed — check why"). Approve re-enters the stage as a
fresh attempt (no feed, like a gate's approve → advance); reject abandons at
that stage. No new event types, so old logs replay unchanged; stored
`run_resumed`-era logs are unaffected. Replay: `edge_taken` to `debug` sets
`pos = Index(From)` and `ph = phDebug` (a plain `Index("debug")` would be -1
and fail the run as "flow changed"); a `ping_answered` is the routing decision
only when the ping is the stage's own and the phase matches
(`runState.decidesHere`). Terminal events now clear `rs.openPings`, so a user
Abort while parked followed by Retry no longer re-registers a withdrawn ping.
`stageGate` and `stageDebug` share `openDecision` / `raiseDecision`.
`PingRaised.Question/Context` are scrubbed on append like the other
workspace-sourced payloads.
