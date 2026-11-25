# SVG icons, no focus ring, and the UI audit

A pass over the live instance found small UI faults on most pages. The
fixes are mostly local; these are the decisions among them.

## The navigation's icons are SVG, from Lucide

The sidebar, the rail, the phone tab bar, System's side nav, the user menu,
the sidebar's buttons, the command palette and the log toolbar drew Unicode
glyphs (`$lib/design/icons`). Each came from whatever font had it, at that
font's size and weight: no two matched, and some read as nothing (a copy
icon drawn as a tiny square). Some also meant little (the war room, tickets,
skills, analytics; account, users, observability).

- `$lib/design/glyphs.ts` holds the icons as SVG shapes on one 24×24 grid
  with one 2px stroke, copied from Lucide (lucide-static 1.79.0, ISC, its
  notice kept in the file) rather than added as a dependency: the ones the
  interface uses only. `$lib/ui/Glyph.svelte` draws one in the text's colour.
- Navigation items and side-nav items name a `GlyphName`, not a character.
  Each place gives an explicit size (16px in lists and menus, 18px alone on
  the rail and the phone tab bar), so every icon in a row is the same size.
- New meanings: the war room a radar, tickets a ticket, merge requests a
  pull request, skills an open book, analytics a column chart; account a
  person in a circle, users two people, observability an activity line,
  providers a key, integrations a plug, channels a speech bubble, the
  provisioner a server.
- The rest of the interface still uses `$lib/design/icons`; it moves over
  as it is touched.

## No focus ring

The owner asked for the blue ring around a focused control to go
everywhere. The global `:focus-visible` outline is `none`; a button, a row,
a menu item, a toggle or a select reached by the keyboard takes the fill a
hover gives instead, a checkbox or radio firms its edge, and a field shows
its caret. No border turns blue on focus.

## Smaller rules

- `fmtCost` keeps three decimals under $10 (a tenth of a cent counts on one
  turn) and two from $10, with thousands separators: "$785.61", not
  "$785.607". `fmtPct(0)` is "0%".
- `hasTime` (`$lib/core/format`): Go's zero time is no time. A built-in flow
  never saved showed "saved 739,944d ago".
- A sticky foot (SaveBar) sticks to the window's bottom edge
  (`--foot-stick`), not inside its padding, where scrolled content showed
  below it.
- Tables whose rows are separate grids keep fixed widths for the columns
  whose content varies by row (a run's state, a user's role and actions):
  `auto` sized them per row and broke the alignment.
- On a phone the tickets table keeps the title, the status and the age: the
  other fixed columns left the title no width at all.
- A window's tab row fades the end that has more tabs past it
  (`$lib/ui/scrollEdges`).
- The command palette follows the mouse only when it moves (the list
  scrolling under a still pointer moved the selection), and no longer
  offers theme switching.
- An existing skill's SKILL.md is put in the editor when the skill loads:
  the selection did not change, so the editor stayed empty and would have
  saved its emptiness over the file.

## Tests

`cpui/src/lib/ui/Glyph.test.ts`, `cpui/src/lib/design/glyphs.test.ts`,
`cpui/src/lib/ui/scrollEdges.test.ts`, `cpui/src/lib/core/format.test.ts`
(costs, percents, `hasTime`), `cpui/src/lib/shell/CommandPalette.test.ts`
(no theme rows, the still pointer), `cpui/src/routes/skills/[skillId]/page.test.ts`
(the loaded SKILL.md in the editor), `cpui/src/lib/ui/ConfigCard.test.ts`,
`cpui/src/lib/ui/EmptyState.test.ts`, `cpui/src/lib/notices/Notices.test.ts`,
`cpui/src/lib/shell/sidebar/Rail.test.ts`.
