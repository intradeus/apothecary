# A long run's session shows its tail

Why: a run with 17 build attempts (3,779 events, 10.5 MB replayed) no longer
opened its Session pane. Two causes, measured on that run's own log:

- **The same tool call recorded twice.** A harness sometimes reports a call
  once more after it completed, with its fuller output. The engine had
  already recorded the call and dropped it from what it tracked, so the
  late report became a second `agent_tool` for the same `tool_call_id`
  (77 times in that run). The transcript showed both in one group, and the
  group's keyed list throws `each_key_duplicate` on a repeated id.
- **Everything rendered at once.** 2,231 transcript items, ~14,600
  elements, 1.6 MB of text (each turn's prompt, tens of kB, inside its
  fold); about 2 s to render in jsdom, rebuilt on every live delta of a
  running run.

What now:

- **Engine** (`internal/engine/transcript.go`): a call that reports
  completed (or failed) is held until something other than a tool report is
  recorded (text, a plan, a notice, the turn's end), so a late report folds
  into the one record. Calls still record in the order they began. A report
  after the call is on the log is dropped: the record keeps the output it
  had.
- **CPUI**: `buildTranscript` merges a repeated id within an attempt into the
  call already shown, so runs recorded before the fix render too.
- **The Session pane renders its last 200 items**, from a stage divider when
  one is in reach (`windowStart`); "Show earlier" reaches back 200 at a
  time and keeps the reader's place. The window slides while the reader
  follows the bottom and holds still once they scroll up. A scroll that
  comes with a change in content height is not taken for the reader's.
- A prompt's text and a folded thought's text are built only once opened.

Not done: the run page still replays the whole event log over SSE when it
opens (10.5 MB for that run). Paging the replay is the next step if a run's
page load itself becomes the problem.

Tests that hold it: `TestTranscriptRecordsACallOnce` (engine);
`transcriptItems.test.ts` (the merge, `windowStart`), `Transcript.test.ts`
(text built only when open), `SessionView.test.ts` ("a long run").
