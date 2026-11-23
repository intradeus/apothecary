# A stalled write is let go once the verb is parked

The engine's window tests stall one event write (`stallStore`), run a
human's verb in the window, and then let the write go. `inWindow` let it go
20 ms after it started the verb. That only made it likely that the verb was
already waiting on the loop. `TestStartHeldAdmitsTheOwnerBeforeItsPrompt`
("message") failed once under the full `task go:test` load. On a
saturated machine (22 busy loops on 11 cores, `-race -cpu 1,2,4`) it failed
3 times in 900 runs.

## Why

A start hold's opening prompt carries the owner's messages that are already
waiting to go in. `promptOpening` takes them under the opening lock with a
non-blocking drain of the loop's unbuffered `messages` channel
(`takeOpeningMessages`). A send that has not yet parked on that channel
when the drain runs misses the prompt. The engine then takes it as the
owner's next turn, as its comments say: it is never lost or taken twice.
So the engine is right. The test assumed that a goroutine started 20 ms
earlier had reached its send. Under load it had not, and the message came
after the prompt.

## What now

- `inWindowParked(t, s, fn, act)` lets the write go only once act's
  goroutine is parked in `fn`. Parked means blocked on a channel or a lock,
  with `fn` as its innermost frame outside the runtime and `sync`
  packages. The runtime's own goroutine trace says so (`waitParked`); a
  timer does not. The 10 s limit there only catches a hang. It does not
  decide the outcome.
- The start-hold tests park as follows: the message in `messageLive`, the
  message sent while the prompt is written in `SendMessage` (on the
  opening lock), the release in `holdLive`, and
  `TestStartHeldOpeningPromptLandsWhole`'s message in `messageLive`.
- An engine change was the other option: admit a message to the opening
  prompt under the opening lock, into a slice instead of the channel. That
  would make the lock the exact admission point. It would also mean
  handing back the admitted messages on every way out between
  `admitOpening` and `promptOpening`, and that path is not broken. Not done.
- The parking tests (`TestPauseWhileParkingPausesTheRun` and the others
  stalled at `run_paused`) keep `inWindow`. Their verbs wait in
  `liveLoop`'s polling, not in one blocking call, and they pass whether the
  verb lands in the window or just after it.
- Stop-on-idle's `errWakeRace` (`wakeOnce`: another wake claimed the run
  between this wake's look and its claim) had no test. That adds to the
  race tests that
  `2026-11-22-idle-sandboxes-stop-and-keep-their-disks` lists.
  `TestAWakeWhoseClaimWasTakenWaitsForTheOtherWake` forces it. The first
  wake's look (its first provisioner `Status`) starts a second wake and
  returns once that wake holds the `waking` op, parked in its `Provision`
  (`WakeHook`). The second wake goes on only once the first wake's look is
  over: that look's resolution is closed (`Release`), after the refused
  claim. The first wake waits the second out and returns nil. There is one
  wake and one `env_woke`, the other wake's, and the workspace is running.

## Tests that hold it

`TestStartHeldAdmitsTheOwnerBeforeItsPrompt`,
`TestStartHeldOpeningPromptLandsWhole`,
`TestAWakeWhoseClaimWasTakenWaitsForTheOtherWake` (engine). After the
change, the same saturated run gave 0 failures in 4200 runs (the two
start-hold tests × 700 × `-cpu 1,2,4`).
