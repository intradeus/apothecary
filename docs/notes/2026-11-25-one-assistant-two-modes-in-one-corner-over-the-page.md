# One assistant, two modes, in one corner over the page

This corrects `2026-11-20-the-permanent-assistant.md` on how the assistant
is shown. The engine is unchanged: sessions are still of a page's kind or
`general`, a page's session fills and never requests, and the general one
requests and never fills.

## Why

There were two assistants behind two buttons: the global drawer (the
sparkle at the top right) and a page's fill modal (its own "Fill with the
assistant" button). The person had to know which one to open, and the two
looked like one thing in two places. The modal also covered the page being
filled, so a fill landed out of sight and the modal closed on it.

## What now

- **One corner, over the page, never in its room.** The shell's buttons
  float at the bottom right of every page in one place, `FabCorner`, each a
  squircle (the `.fab` class, the run page's Layout button's look): the
  assistant in the corner itself, on every page; above it what a page adds
  (a run's Layout button). Stacked upwards, so the corner stays one button
  wide: a run's window in the corner still steps aside by one button
  (`--fab-clear`), a `SaveBar` keeps its trailing end clear
  (`--fab-clear-x`), and toasts rise beside the corner, not over it. The
  page headers no longer reserve room for a button at their top right.

  The assistant unfolds from its button:
  - closed, the button (the corner's sky); a **pill** (the button with
    words) while an answer is written ("Brewing your ticket…") and for 8 s
    after a fill lands ("Filled: title, goal");
  - open, a **card** beside the button, to the left of the corner's column
    (so its buttons stay in reach), as tall as what it shows (at most 70%
    of the screen): the mode switch, the latest exchange (from the person's
    last question on, "N earlier messages" above it), the box to ask in. No
    model picker or repositories row in the card. The button stays where it
    is and folds the card again;
  - **expanded** (⤢), a drawer the screen's height beside the corner with
    the whole conversation, the model and the repositories. Opening again
    is the card.
  It floats over the page with no scrim, in the corner's layer (a modal the
  page opens covers it). A click in the page folds it back to the button,
  so a fill is always in view; what was typed, the scroll and the mode stay
  (the panel stays mounted, hidden). A click in it, or in a dropdown, menu
  or dialog it opened, does not. Esc and × fold it too; on a narrow screen
  it is the screen above the tab bar.
- **The page can be filled: the button says so in colour, not with a dot.**
  While a page offers itself the squircle wears the page mode's accent
  (violet) instead of the corner's sky, and arriving on such a page the
  star twinkles and a sheen crosses the tile, once (none with reduced
  motion). A dot on the button hid the star.

  *Rejected:* docking it beside the page (the page narrowed for it): the
  owner's page space matters more than seeing page and panel side by side.
  The old full-height overlay drawer: it covered a whole side for as long
  as it was open. The top right corner: the card covered page headers'
  actions, and the run page already had its Layout button at the bottom
  right. A looping animation for "can fill": it would pull the eye for as
  long as the page is open.
- **Two modes the person picks, told apart by what they may do.**
  - *This ticket / this flow / …* (`page`): about the page on screen. It
    fills the page, unsaved and undoable, and never acts. It wears the
    accent colour (the mode switch, a line on the panel's top edge, the
    scope line, the composer's border), the page's name, and the
    placeholder "What should this ticket hold?".
  - *Everywhere* (`general`): reads everything and acts once the person
    confirms; it never touches the page. Neutral colours.
  A scope line under the switch says this in words. ⌘J opens the panel,
  then switches modes; with only one mode it closes the panel.
- **A page offers itself.** The fill modal and `FillButton` are gone. A
  page calls `offerPage(() => ({ kind, subject, workshop, name, guard,
  pageState, workshopPicker?, handoff? }))`; the last page offered is the
  page mode's, and the mode goes when the page does. The button shows a dot
  while a page can be filled, and its tooltip says so. Opening the panel
  starts in page mode when a page offers itself, and in Everywhere when not.
- **Each mode keeps its own conversation and its own draft.** The page's
  conversation is opened the first time its mode shows (a page visit alone
  opens no session) and *resumes* the latest session about that page,
  where the modal opened a fresh one each time. It is kept while the page
  stays, whether the panel is open or not, and ends when the page goes,
  cancelling an answer still on its way. Everywhere is the person's general
  conversation, which follows them from page to page.
- **Fills show in the conversation.** An answer that filled the page shows
  a card: what it changed, with Undo; the stale-edit choice (Fill it anyway
  / Keep mine); a run's handoff to the flow editor. An earlier answer's
  fill, or the last one once undone, can be applied again (`applyNow`). The
  page keeps its own marker (`FillMark`, formerly the marker half of
  `FillButton`), so Undo stays on the page when the panel is closed.
- **Moving a question across, never by itself.** Under the page mode's
  last answer that filled nothing, "Ask in Everywhere" switches modes and
  puts the question that led to it into Everywhere's composer, for the
  person to send or reword.
- The flow editor reads the flow's bound parameters once the panel is open,
  not once the modal was.
- Not done: outlining the fields a fill changed on the page itself (each
  page would mark which element shows which field).

## Tests

`cpui/src/lib/assistant/assistant.svelte.test.ts` (offers, modes, the
toggle, carrying a question), `AssistantDock.test.ts` (the general
conversation as before; the page mode opened lazily and kept apart, a fill
with Undo and Apply again from the panel, carrying a question, the page
going, ⌘J and Esc; folding on a click in the page with the draft kept,
the pill, the card's latest exchange and the expanded conversation, the
accent while a page can be filled, the button toggling the card),
`cpui/src/lib/ui/FabCorner.test.ts`, `FillMark.test.ts`, `fillGuard.svelte.test.ts`
(`fillHook`), `src/routes/skills/[skillId]/assistant.test.ts` (the skill
page beside the real dock). The pages' tests offer through
`src/test/fixtures/AssistantStub.svelte`.
