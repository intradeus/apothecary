# Watchers pane one card per chain

Why: the run page's Watchers view listed every chain of the flow — the main
chain, each watcher, each outcome — as one dense row: the id, a `·`-joined
sentence (what opens it, its start policy, its runs, where it stands), a
`pending` pill whose reasons sat in a tooltip, and a small Run now. It was
hard to read and hard to act on. "Each watcher type should have its own
card" read two ways, one card per kind or one per chain; the task's owner
picked one per chain, grouped by kind.

What now:

- **One card per chain** (`run/PhaseCard.svelte`), under a `SectionHeader`
  per kind in the order main chain → watchers → outcomes, a kind the flow
  lacks left out. `PhasesCard` keeps the reading, the polling, the start
  and the merge-request picker; the outer card and its "Watchers" title
  went, since the desk tab already names the view. The long section
  sentence became one sub per section.
- **A card spells out its facts** as a `dl.kv`: Opens on, Starts, Runs. It
  shows the chain's `description`, which the run already sent and the pane
  never showed, and lists the reasons its signal holds as visible lines.
- **Its tone is the flow editor's chain marker** (DESIGN §7.8), a 3px top
  accent: main brand, watcher accent, an outcome success or danger by its
  `then`; one status pill: running, parked at X, pending, max reached, idle.
- **Run now is a `.btn-md` with the play glyph, and says why** it can or
  cannot start beside it (still in `title` too). It is primary only while it
  can start and its signal is pending: that is when the pane wants a human,
  and several cards can be so at once without the view becoming a wall of
  primaries. A start in flight holds every button and now says so ("A
  watcher is starting"). Starting, its semantics and the API are unchanged.

Tests: `PhaseCard.test.ts` (wording per kind, description, status, Run now's
states, reasons and when it is primary), `PhasesCard.test.ts` (sections and
their order, the cards in each, start, one cursor, max per merge request,
errors, the picker, polling), `PhasesView.test.ts`.
