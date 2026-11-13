# The owner's answers reach later stages: run.ping_answers (2026-11-10)

- Found live: the builder asked, the answer changed the plan, the builder
  followed the answer — and the reviewer, whose inputs were the ticket, the
  plan and the diff, rejected the build for deviating from the plan. The
  answers lived in the pings, where no later stage could read them.
- A new engine source, `run.ping_answers`, of a new value type,
  `ping_answers`, read from the run's log (`run.ReducePings`) when the
  prompt is built. Engine-side on purpose: it is the record of what the
  person said, which a builder-written "decisions" file could forget or
  misquote, and any flow's stage opts in with one port.
- What counts: every answered agent question; a gate, debug leaf or manual
  stage only when the person wrote feedback or review comments; never a
  phase confirmation or a selection (they decide what runs, not what is
  built). Each answer is rendered by `composeAnswerText`, the renderer the
  asking agent read it through, its attachments named (`answerFileRefs`,
  no delivery: the workspace has them). Quoted line by line, oldest first,
  capped at `flow.PingAnswersBudget` (32 KiB): the oldest go first, a long
  question gives way to its answer, a long answer is cut on a line.
- Unlike a ticket's comments the answers are the owner's direction, and the
  section says so: they amend the plan and other inputs where they differ, a
  later answer over an earlier one. Nothing answered still renders a line,
  so a prompt naming the input never meets a silence.
- The `builder` template wires it into build, review, redact and the
  review-fix round; the reviewer's prompt checks the plan "as the task
  owner's answers amend it". Take-over messages are not in it: the source
  is about pings, and a held session's words reach that session already.
