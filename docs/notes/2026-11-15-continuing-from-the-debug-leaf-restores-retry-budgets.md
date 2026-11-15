# Continuing from the debug leaf restores retry budgets

Why: continuing from the debug leaf (2026-10-19-the-debug-leaf.md) re-entered
the stage "as a fresh attempt" but left every edge counter where it was. The
chain that escalated was spent, so the first failure after a person's fix — a
test still red, a reviewer still rejecting — went straight back to the leaf:
`test` failed 5 times through `build`, the person fixed something and
continued, `test` failed once more and the run was parked again with no agent
retry in between. The person was asked to babysit every later failure, which
is not what the leaf is for (a last resort once agents are out of ideas). *Try
again* on an abandoned run already restores every budget (`run_retried`
clears `EdgeCounts`); continue was the odd one out.

What now: `runcore.State.ContinueFromDebug` is the one move. It re-enters the
stage fresh (no feed) and deletes the counters of every non-debug link of
every stage in the escalating stage's phase — the same scope a phase's start
resets. The debug links keep their count, so the timeline still numbers each
visit (n/∞). Another phase's chains are untouched: they reset when that phase
starts. The engine's `routeDebug` calls it as it routes the answer; replay
calls it on the `stage_started` that records the continue — every stage
executor appends one on entry, a skipped stage's included — so live and
folded state agree. No new event: old logs replay
unchanged, and a run parked at the leaf today gets its budgets back when
continued.

Each continue therefore buys at most one more round of the phase's retries:
still bounded by a person's attention, as the leaf's unlimited count already
was (FLOWLANG §3).

Tests: `TestContinueFromDebugRestoresEveryChainOfThePhase` (engine: every
stage of every built-in flow that can escalate, sequences and watchers
included — with every counter spent, continue restores exactly the phase's
non-debug links, keeps the debug counts and other phases' counters, live and
replay agreeing, and every chain of the phase then routes as on a fresh run);
`TestDebugContinueRestoresRetriesAcrossRestart` (a crash after the answer, and
after the stage_started that records the continue: the failing test still
goes back to build first); `TestDebugContinueRestoresRetries` (engine, live);
`TestContinueFromDebugRestoresThePhasesRetryBudgets` (runcore). The engine's
fold check (`main_test.go`) models the continue window with the same method.
Breaking the fix six ways (no reset; the escalating stage only; debug counts
reset too; every phase reset; the replay path or the live path dropped) fails
at least two of these each.
