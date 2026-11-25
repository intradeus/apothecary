# A log draws its newest 2000 lines

`LogOutput` drew every line of its text as DOM. On a live run whose
workspace log was 54,168 lines (the Logs pane's first read is the last
2 MiB, `2026-11-10-the-workspace-log-is-read-by-byte-range.md`), opening
the pane was about 5 s of a frozen main thread: 2.4 s of Svelte building
the rows, 1.1 s of per-line work (links, the line tint), 0.75 s of GC. The
page held 55,000 DOM nodes and 280 MB of JS heap. `content-visibility:
auto` on the lines, tried on that page, saved nothing: the cost is building
the rows, not laying them out.

## What now

- `LogOutput` draws only the newest `LOG_WINDOW` (2000) lines. Above them,
  "Show N earlier lines" draws 2000 more, and scrolling to within 300 px of
  the top does the same; both keep what the reader sees in place.
- A tail that grows slides the window while the box follows the bottom,
  and widens it while the reader is up the log, so their lines stay drawn.
  Text put on top (the pane's "Load earlier output") is drawn: it was asked
  for. Any other text is a new log, drawn from its newest window.
- Find still searches every line and counts every match; stepping to a
  match above the window draws out to it. Copy still copies all the text.
- The pane's 2 MiB first read is unchanged: the window, not the read, was
  the cost.

Separately, the desk handed each view a fresh `{ visible, w, h }` on every
frame of a resize, so every effect reading `visible` ran again: the Logs
pane reloaded and redrew its whole log while a divider moved. Each view now
gets one `ViewPlace` for the desk's life, each field derived on its own
(`Desk.svelte`).

## Tests

`cpui/src/lib/ui/LogOutput.test.ts` ("a long log": the newest lines and the
ones above on request, scrolling to the top keeping the place, a followed
and an unfollowed tail, earlier output on top and a new log, find drawing
out to a match, copy of the whole text), and
`cpui/src/lib/desk/Desk.place.svelte.test.ts` (an effect reading `visible`
runs once while a divider moves).
