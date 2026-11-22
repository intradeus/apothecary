# The permanent assistant

The assistant becomes two: a global one, behind a button at the top right
of every page, and a local one on each page that can be filled. Both run on
the person's one assistant pod, which they configure in one place. This
supersedes, in part, `2026-11-19-the-assistant.md` (the panel beside the
pages, and the rebinding of a draft's session) and
`2026-11-19-the-assistant-acts-by-confirmation.md` (the palette's session,
acts from every page, and the run desk's Assistant view).

## Why

Each fill page embedded its own panel, which moved the page's layout when
it opened, and every other page reached the assistant only through the
command palette. The ticket asked for one permanent assistant: one button
always in the same place, one drawer over the page, aware of the page it is
on through a registration store and a "filling mode" toggle.

The owner turned that design down at the first plan gate and in the
planning that followed: a toggle between filling and asking is poor
experience, and the page awareness it needs is a second mechanism for what
the page already knows. The design they chose instead is two assistants,
each doing one job.

## What now

- **Two assistants, no toggle, no registration.** The global assistant
  (`GlobalAssistant`, mounted once by the shell) is the person's `general`
  conversation, subject `palette`, reading the workshop of the route it is
  on. The local assistant (`FillAssistant`, opened by a page's
  `FillButton`) is a session of the page's kind. A page hands the modal its
  guard and its `pageState` as it handed the panel; nothing registers with
  the shell, and nothing infers the page from the URL. The ticket's page
  registration store and its toggle are dropped.
- **Page sessions fill; the general session acts** (`Page.Acts`, true only
  for `general`). A page's token lists `fill` and not `request`
  (`boundTool`), and `Propose` refuses a page session's request with an
  answer that names the global assistant, so a stale tool list acts
  nowhere. The general session lists `request` and not `fill`. The
  instructions are split to match: a page's says its one way to act is
  fill, that its content is in the context, and that tidying keeps the
  meaning; the general one is worded for the button on every page, and its
  presets start and retry runs. `checkActs` and the confirm handler bound
  acts by the face's catalog alone (served, a write, not the assistant's
  own), so the never-a-tool list is still the only bound on what the
  general session may request; `TestAssistantRequestTool` holds every
  served write tool as a valid act.
- **A fresh session per modal opening.** `POST /api/assistant/sessions`
  takes `fresh`, which skips finding the latest session about the subject.
  Each opening of the local modal is a new conversation, so the current
  settings and skillsets apply, and a draft's session never needs moving to
  what it was saved as: `Rebind` and `PUT …/sessions/{id}/subject` are
  removed.
- **A conversation holds one session at a time.** With fresh sessions,
  several about one subject are normal, so the latest is no longer the one
  a page holds. After a stream gap the conversation reads its own session
  again by id (`GET /api/assistant/sessions/{id}`, `Service.Reopen`), not
  through `open`. While a session opens (the first, *New conversation*,
  another workshop's) the conversation is `switching`: it sends no
  question, decision or model, and the panel disables them; an answer the
  old session gives after the switch is dropped. *New conversation* waits
  while an answer is written (Stop ends it first). A fill modal's
  conversation is its page's kind, subject and workshop: another page in
  the same component (Back to another skill) ends it, cancelling its answer,
  and its late frames neither fill the new page nor end its question.
- **The drawer is an overlay.** It is `Sheet` (fixed, portalled, over a
  scrim) with a `quick` slide of about 200 ms instead of the spring. The
  button floats at the viewport's top right; only the page headers'
  trailing padding grows (`--assistant-inset`), so their actions never sit
  under it. The main column keeps its width. The conversation lives in
  `GlobalAssistant`, outside the sheet, so closing the drawer loses neither
  the stream nor the session; *New conversation* opens a fresh one.
- **The modal shows only the brew.** While the local assistant answers,
  the modal shows the reusable `Brewing` spinner and Stop, never the text
  as it streams nor its tools. A fill applies through the page's guard
  (Undo, the changed marker, the click before overwriting newer edits) and
  the modal closes; the page's `FillButton` carries the marker. A run's
  fill (a change to its flow) keeps the modal open on its "Open in the flow
  editor" callout. Closing while answering cancels, and a late fill is
  dropped.
- **Pre-warm on the first keystroke.** Typing into either assistant calls
  `POST /api/assistant/warm`, which ensures the person's pod with their
  harness's model hosts in the background and answers at once. The client
  throttles it to once a minute, one clock for both assistants, and the
  service coalesces it to one warm in flight per person. A pod warmed and
  never used is reaped by the idle sweep.
- **Settings in the System hub.** `/system/assistant` holds the harness,
  the model and the skillsets (`assistant_choices.skillsets`, migration
  `0017_assistant_skillsets`); the drawer's settings button leads there.
  A choice names the person's own skillsets only. A new session pins them
  (`assistant_sessions.skillsets`), resolving each ref on its own and
  leaving out one deleted since; every turn installs the pinned skills into
  the harness's user-level skills directory in the session's home
  (`~/.claude/skills`, `~/.config/opencode/skills`), skipped when a marker
  there names the same pins. The settings apply to new conversations: an
  open one keeps its harness and skills until *New conversation*, and its
  model picker offers the standing choice's models only while the
  conversation runs on that harness (otherwise its own model and the
  default).
- **The run desk's Assistant view is retired.** The run's local assistant
  sits beside its title; a stored desk layout that names the view loads
  without it and keeps its other tabs.

## Tests that hold it

- `internal/assistant`: `TestOnlyTheGeneralPageActs`,
  `TestAPageRequestsNothing`, `TestOpenAndOwnership` (fresh, reopen by id),
  `TestSkillsetsArePinnedAtOpen`, `TestATurnInstallsItsSkills`, `TestWarm`,
  `TestProfilesHaveASkillsDirectory`; the store contract round-trips the
  skillsets.
- `internal/httpapi`: `TestAssistantRequestTool`,
  `TestAssistantRequestConfirmed` (a general session's `start_run`,
  confirmed, starts the run; declined, runs nothing),
  `TestAssistantWarm`, `TestAssistantChoiceSkillsets`,
  `TestAssistantSessionsArePersonal` (fresh, by id, the subject route gone).
- CPUI: `conversation.svelte.test.ts` (a gap rereads the session held, not
  a newer one; switching sessions), `GlobalAssistant.test.ts` (no question
  while *New conversation* opens), `AssistantPanel.test.ts` (an older
  conversation after a harness switch), `FillAssistant.test.ts` (another
  page under the open modal), `FillButton.test.ts`, `Brewing.test.ts`, the
  fill pages' tests and `routes/system/assistant/page.test.ts`.
