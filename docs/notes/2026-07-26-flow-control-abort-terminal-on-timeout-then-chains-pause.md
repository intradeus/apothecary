# Flow control: abort terminal, on_timeout, then-chains, pause/play, rescue gates

Five additions making every way a run ends or loops an explicit, visible
part of the flow document (extends DESIGN3 §2.1's registry as designed):

- **`abort` terminal**: reserved edge target mirroring `done` (ids may
  shadow neither). `on_reject: abort` (scalar sugar or `{to: abort}`)
  stops the run deliberately → `run_abandoned` with the feedback in the
  reason — the explicit twin of the implicit "no edge / exhausted edge"
  abandons. `max`/`feed`/`then` are rejected on abort links; `to: done`
  is rejected on edges (only on_success completes a run). The graph
  editor renders an abort terminal node; the run-page Abort button is
  unchanged (kill switch → run_failed "aborted").
- **`on_timeout` edge kind**: one registry row + the two timeout raise
  sites (agent, command); recovery's routeAfterFail picks the kind from
  the persisted stage_failed reason. Absent on_timeout falls back to
  on_fail — existing flows behave identically. The agent timeout now
  passes the cancelled attempt's output tail as feed content (edges
  without feed still record "").
- **`then` chains (retry-then-escalate)**: an Edge is now a chain; each
  link keeps its own cumulative counter (edge_taken gains `depth`,
  replay keys edgeCounts by from|kind|depth; pre-chain events land on
  depth 0 unchanged). A full link falls through to its `then`; an
  exhausted chain abandons with the combined budget ("max 2+3").
  Counters are cumulative, not per-visit — total loop work stays bounded
  by the sum of maxes (plus flow budgets, as before).
- **pause/play**: `run_paused` / `run_unpaused` events + `paused` state
  (non-terminal; Recover skips it). Pause = cancel the loop with
  errPaused — a controlled crash; the goroutine exits, freeing a §14
  slot, and the workspace is kept. Play re-queues the run; replay
  re-enters mid-stage via the existing crash-recovery paths (agent
  sessions resume, commands re-execute, gates re-arm without a new
  ping_raised — run_unpaused carries `waiting` so the projection lands
  back on waiting_on_human). Play resets NO retry budgets (contrast
  ResumeRun's "try again"). Abort now also works handle-less (paused or
  queued runs): dequeue under the lock, append run_failed directly.
  stageAgent gained the same ctx-cancellation guard stageCommand had, so
  a cancel racing the harness result is never misread as harness_error.
- **Rescue human gate** + `input: {workspace: $WORKSPACE}`: parks the run
  and hands the human the workspace link (terminal > ide > first, from
  env_ready; runState.links now also set on the live provision path).
  approve = continue on_success, reject = on_reject (typically abort),
  feedback feeds along. Counted as a WANTED ping (gatePingType).

The shipped example (artifacts/flows/v2.yaml) exercises all of it:
test on_fail → build ×5 then debug (a gate showing $WORKSPACE) → abort on
reject; on_timeout self-retry then build; final_gate on_reject: abort.
Locked by flow.TestReferenceFlowParses.
