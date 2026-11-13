# Reviews: a gate over many files, and a decision with a shape

A gate shows what a run made — a plan, a folder of screenshots — and the
person answers file by file (2026-10-26). Three changes, one idea: the
review page's structure reaches the engine, and the engine writes the text.

- **A gate shows files and one link.** Any number of `file`/`files` ports
  plus at most one link (was: exactly one port). A folder is expanded when the
  gate opens (`gateFiles`): its files in order, the attachment bounds (≤20 in
  all, none over 10 MB — a cut snapshot ruins an image), each snapshotted as
  shown. `Ping.Files` was already a list; only the gate ever put one in it.
- **A review is structured.** `PingAnswered.Review []ReviewComment{path,
  start_line, end_line, heading, quote, body, attachment_ids}`; `feedback` is
  its summary. Checked on both answer paths (`validateReview`): each on a
  shown file, non-empty, a real range, an image (by extension) whole-file only,
  its attachments the answer's. Projected one row per comment into
  `ping_review_comments` (migration 0010 — 0001 is frozen), rebuilt with the
  pings, read back with them. The page no longer composes a feedback text
  (`composeFeedback` is gone): one Go renderer (`renderReview`) writes it
  grouped by file in the order shown, numbered, attachments named where a
  comment names them and listed after otherwise — for a gate's decision and
  for an `ask_human` answer alike (`composeAnswerText`).
- **An approval carries its decision.** `flow.Decides` — a gate, or an agent
  with a verdict — makes on_success carry `edge.decision` when the approval
  said something (a summary, comments, files; a verdict's comments). The
  validator counts that as filling `edge.decision`. Persisted as
  `StageCompleted.Feed` (as `EdgeTaken.Feed` is for a rejection), so recovery
  between the gate and the next stage hands it on (`completedFeed`). Replay of
  `stage_completed` now also clears a feed the stage was entered with — the
  live `advance` always did, replay did not, so a run recovered after a
  rejection loop could re-hand the old rejection to the stage after next.
  Decisions keep their head (32 KB, `capDecision`); failures their tail (8 KB).
- Found on the way: live gate pings never entered `rs.raisedPings` (replay
  put them there), so anything reading what a gate showed differed between a
  live run and a recovered one; `raiseDecision` now mirrors replay.
- Not done: the engine checks a comment's lines are a range, not that they
  fall inside the file (that would mean reading the snapshot on every answer).
