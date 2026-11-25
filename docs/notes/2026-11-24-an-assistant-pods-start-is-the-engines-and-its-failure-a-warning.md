# An assistant pod's start is the engine's, and its failure a warning

A failed start of a person's assistant pod turned the assistant off: the
pod's error became the status's `reason`, `enabled` went false, and the
panel offered nothing but the settings. Nothing retried it short of a
reload, and the cause was usually passing (a slow node, a gateway still
rolling out). The start was also tied to whoever asked for it: a refresh
ran on the browser request's context, a warm on a 2-minute deadline,
shorter than one start can take (the gateway's rollout, up to 3 minutes,
and its control API, up to 1, before the pod's own 5 to come Ready). A
start cut short ended in `confine --check exited -1`, and only a warm
logged anything, so the engine's log had nothing to show for it.

## What now

- **The start is the engine's.** `Manager.Ensure` runs the start on its
  own goroutine, on `context.WithoutCancel(ctx)` with the manager's
  deadline (`StartTimeout`, `DefaultStartTimeout` = 12 minutes, counted
  once it holds the person's lock). A caller that stops waiting (the
  browser gone, a turn stopped) gets its context's error; the start goes
  on and its outcome is recorded all the same. `Service.Warm` has no
  deadline of its own any more.
- **Every failed start is logged**, in `Manager.start`
  (`assistant: start a pod`), whoever asked for it; the warm's own log
  line is gone.
- **A failed start is a warning.** `Status.Pod.Error` no longer becomes
  the reason: the assistant stays enabled, the panel and the fill modal
  show the error as a dismissible warning, and the next question or warm
  starts the pod again. The error goes when a start succeeds, when the
  idle sweep stops the pod, or when `Status` finds the pod well (running,
  past its kernel check, its agent host answering `/health`). A pod that
  never passed the kernel check (a node without Landlock) keeps it. The
  client puts it away once an answer comes back without an error.
- A start now marks the pod unchecked before it creates one, not after:
  a start that failed half-way could leave a new pod the next start would
  have taken as checked.

## Tests

`internal/adapters/assistant`: `TestEnsureOutlivesItsCaller` (the caller's
cancel ends the wait, not the start; the start's deadline; the failure
recorded and logged; a start past `StartTimeout`), `TestAPodErrorClears`.
`internal/assistant`: `TestStatus` (a pod error leaves the assistant on),
`TestWarm` (a warm outlives its request). CPUI: `AssistantPanel.test.ts`
and `FillAssistant.test.ts` (the warning, its dismissal, an answer
clearing it).
