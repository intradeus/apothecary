# A page's panel shows on its conversation alone

Adds to `2026-11-25-one-assistant-two-modes-in-one-corner-over-the-page.md`
("the mode goes when the page does").

## Why

With the assistant opened about a page (a skill, a dashboard, a ticket…),
leaving that page by a link broke the app: `can't access property "convo",
… is null`, and the page navigated to never drew (Analytics stayed on its
skeleton). Opening the panel was enough; nothing had to be asked.

`AssistantDock` showed the page's panel under `{#if paged &&
assistant.page}` and handed it `convo={paged.convo}`. The two halves of
that guard go at different moments of one update:

1. the route's page is destroyed, and its `offerPage` withdraws the offer
   (`assistant.page` is null): the guard is marked to run again;
2. later in the same update the dock's effect ends the conversation
   (`paged = null`).

Svelte (5.56, `mark_reactions` in `reactivity/sources.js`) runs the
`{#if}`s that read a state written inside an effect right after that
effect, parents first, but only those not already marked. The guard was
marked at step 1, so it was left for later, and the panel's own `{#if}`s
ran first, reading `paged.convo` on null. The throw ended the update, with
the new page's effects still to run.

Withdrawing an offer from outside an update (what the dock's tests did)
runs the guard first and never showed it.

## What now

- The page's panel shows on `paged` alone. The conversation holds the page
  it opened on (`paged.target`), and the panel is given `assistant.page ??
  paged.target`: the page as it offers itself now (its name changes under
  the same conversation), else the one it opened on for the moment between
  the offer going and the conversation ending. One state written in one
  place guards the panel, so its guard always runs before it.
- A guard over a panel reads one state. Two that change at different
  moments of an update (one in a teardown, one in an effect) can run the
  panel between them.

*Rejected:* `paged?.convo ?? general`: the page's panel would draw
Everywhere's conversation for one pass. Ending the conversation in the
offer's withdrawal: the dock, not the page, owns the conversation.

## Tests

`cpui/src/lib/assistant/AssistantDock.test.ts` ("lets the page go under
its open panel when a navigation takes it", "… once a click on a link has
folded the panel"), through `src/test/fixtures/ShellWithDock.svelte`: the
page before the dock, as the layout has them, so the offer is withdrawn
inside the shell's update. Both fail on the old guard with the error
above.
