# Parked, not blocked: a run waiting on a human gives up its slot

The ask_human rework above moved the wait for a human into the engine. That
made a cost visible that gates had been paying silently since M15: the wait was
held by a live goroutine. `scheduleLocked` admits runs while
`len(e.handles) < MaxConcurrentRuns` (default 3), so three runs parked on
unanswered questions blocked every other run in the queue — overnight, for as
long as nobody looked at the inbox.

The obvious idea — reuse the pause button — does not work as-is, and the reason
is worth recording: `Pause` cancels the run context, the loop exits, `finish`
prunes every ping of that run from `e.pings`, and `AnswerPing` then refuses
with `ErrRunNotActive`. A raw pause would make the question **unanswerable
until someone pressed play**, in the one flow whose whole point is that the
human answers whenever they get to it.

So: the same mechanism, tagged with why, plus a wake that needs no button.

- `run_paused` gained a `Reason`. `user` is the button and still reads
  `paused`. `waiting_on_human` is the engine parking a run that has nothing
  left to do — it keeps reading `waiting_on_human`, because a run waiting for
  YOU is not a run someone paused. `run.View.Dormant` (derived, not a column)
  carries "has no loop and is owed none" independently of the state, and
  `run_unpaused` clears it.
- `stageAgent` returns `errWaitingOnHuman` where it used to block; so does
  `stageGate` — **unless a signal is armed**. `auto_approve`/`auto_reject`
  poll an external service on a ticker and genuinely need a live loop, so
  signal-armed gates stay resident. Wait stages likewise.
- `AnswerPing` has two paths now. Live: hand it to the loop and WAIT FOR AN
  ACK. Dormant: validate against the durable projection, append `ping_answered`
  (plus `run_unpaused` when it was the last open question and no human has
  pressed pause), and re-queue the run. `validateAnswer` and `recordedAnswer`
  are shared by both, because two writers with two ideas of what a valid
  answer is would drift within a month.
- Re-entry needs no new machinery. Gates already route an answer found in the
  log (`rs.gateAnswer`), and the agent path rebuilds its resume message from
  `stage_paused.ping_ids` + the `ping_answered` events — the run had no loop
  when the human answered, so the live ping registry never saw it. `replay`
  now keeps `raisedPings`/`answeredPings`/`pausedAsks`, and the runtime path
  writes the same maps as it goes, so a resume reads identically whether the
  answer arrived in this process or is being found after a reboot.

### The three races, and what closes each

**An answer arriving as the loop exits.** It used to land in the buffered
`answers` channel of a loop nobody was draining any more: `AnswerPing` returned
nil and nothing ever happened. Answers now carry an `ack` channel; the sender
waits for it, `finish` fails anything still queued with `errNoLoop`, and the
caller falls through to the dormant path. Either the loop recorded it or the
sender is told to try the other door — never silence.

**An answer arriving as the loop starts.** Neither path owns the ping yet.
`AnswerPing` retries across both for `answerSettle` (5 s) rather than
returning "run is busy" to a human who would only click the same button again.

**An answer that beat the park.** The human answers while the agent is still
ending its turn — the tool call already handed the turn back, so the agent
cannot see it, but the ping is answered before the stage parks. `Ping.HandOff`
(set by the HTTP hold: `true` when the answer went back in-band, `false` on the
204) is what tells the loop the difference. An answer the agent never saw does
not close the question; the attempt pauses on it and delivers it on resume.
And when everything it parked on is already answered, the loop resumes on the
spot instead of parking a run with nothing to wait for.

### What had to follow

`PendingPings` folds the durable projection for runs with no loop. It read only
the in-memory registry, which after this would have hidden exactly the pings
waiting longest. The HTTP inbox was already projection-backed
(`EventPingStore` / pgstore's view), so it needed nothing — but the engine's
own list did. `Engine.SetPingLookup` wires pgstore's view in production; the
fallback folds the event log.

`Recover` skips dormant runs instead of `paused` ones, with one exception that
is the whole reason it is a rule and not a state check: a parked run whose
answers have all landed IS owed a loop. That is the crash window between
recording an answer and re-queuing the run, and skipping it would park the run
forever.

`Pause` works on a parked run (append `run_paused{user}`) — pausing something
that is waiting is meaningful: it says "do not pick this up when the answer
lands". `Play` refuses a run that is merely waiting, since playing it would
re-enter the stage and park again on the same unanswered question. And
`ping_answered` no longer forces the projection to `running`: a parked run has
no loop to put back to work, and a paused one keeps its pause.

### The behavioural change worth knowing

Answering no longer resumes a run instantly. The woken run goes back into
`e.queue` and waits for a slot behind whatever was admitted while it slept.
That is the trade: the factory keeps working while people think, and the answer
buys a place in the queue rather than a goroutine that was never released.
