# A pass for phones

2026-10-24, the user: "The mobile view is very bad for the runs… default to
the focus layout — the focus layout should have a single pane with all
windows… with a horizontal scroll for when the tabs go off-screen." Every
page was loaded at 390×844 (touch) and measured for content past the edge;
what changed:

- **Focus is one window** holding all fourteen views (it was the flow strip
  over the rest).
- **The run page on a narrow screen is compact** (page ≤ 760px wide):
  `DeskState.setCompact` swaps in the focus layout in memory — the saved
  layout is set aside, never written over, and comes back as it was when the
  page is wide again; the view in front stays in front. Compact tabs are
  tapped (no drag, so a swipe scrolls the row: `touch-action: pan-x`),
  without hide ×, split, maximise or the layouts button. The page itself
  scrolls: the header scrolls away and the window keeps the page's full
  height (`--desk-h`), so it never collapses to nothing on a short phone.
- **Tab rows scroll sideways and keep the selected one in view**
  (`keepInView`, `lib/ui/inview.ts`): the run window's tabs, the workshop's
  sections (their selected tab was off-screen from Flows on), and the side
  nav in its row form (System sections, the workshop form's steps), whose
  items no longer shrink to "I…", "C", "Me…". Their ends fade.
- **Wrapping instead of clipping**: section headers (Integrations' "+ Add"
  buttons), pane heads, the Flows tab's rows (name, then version and age,
  then the actions), the Agents tab's "runs on" (stage and harness whole).
- **A bug any width had**: short-form parameters (`harness: fake`) read as
  "undefined" on the Agents tab — `normalizeDoc` now gives every parameter
  its long form, as the editor already assumed.
