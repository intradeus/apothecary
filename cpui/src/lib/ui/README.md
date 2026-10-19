# cpui design system

Spec: `docs/UX/UI/DESIGN.md`. Tokens and shared classes live in `src/app.css`;
components here. Style with tokens (`var(--mut)`, `var(--azBg)`, …), never raw
colours.

## Materials

- `.lg`: control glass, for things that float and control (header groups,
  composer, banners). `.lg-pop` is the popover/modal variant with an opaque
  underlay.
- `.c`: content glass, for panes that hold reading material (windows, cards).
- `.card-in`: a card inside a pane.
- Never put glass on glass.

## Shared classes (app.css)

| Kind           | Classes                                                                                                                                                                                                                                                           |
| -------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Buttons        | bare `<button>` = secondary; `.btn-primary` (one per group) `.btn-sel` `.btn-danger` (outline, ends in "…", confirms) `.btn-danger-solid` (dialogs/banners only) `.btn-ghost` `.btn-icon` `.btn-notyet` `.btn-sm` `.linklike`. Use `<a class="btn …">` for links. |
| Pills          | `.pill` + tint `.t-az/.t-amb/.t-grn/.t-ros/.t-vio/.t-tea/.t-mut/.t-brand`, optional `<span class="dot">`                                                                                                                                                          |
| Tags, counts   | `.tag` (mono key/hash), `.count` / `.count.hot` (solid amber means a human must act now)                                                                                                                                                                          |
| Tabs, segments | `.tabs > button.on`, `.seg.lg > button.on`, `.bead`                                                                                                                                                                                                               |
| Forms          | inputs are styled globally; `.field > label + input + .help/.err`, `.grid2`                                                                                                                                                                                       |
| Lists          | `table` (sticky th), or `.rows > .r` grid rows (set `grid-template-columns` per list)                                                                                                                                                                             |
| Panes          | `.pane.c` with `.pane-head` + `.pane-body`                                                                                                                                                                                                                        |
| Text           | `.mono` `.serif` `.mut` `.fnt` `.ell` `.num` `.md` (rendered markdown) `.hue-*`                                                                                                                                                                                   |
| Page           | `.page` = the standard scrolling page body inside the shell                                                                                                                                                                                                       |
| Motion         | `.pulse` `.pulse-slow` `.caret` `.skel`                                                                                                                                                                                                                           |

## Components

| Component                                        | Purpose                                                                    |
| ------------------------------------------------ | -------------------------------------------------------------------------- |
| `StatusChip`                                     | run state (3.1); `suffix` adds the lore "· brewing"                        |
| `WorkspaceStatus` (+ `wsValue()`)                | workspace axis (3.2); `compact` = dot only. Never merged with the run chip |
| `StageChip`, `StageFlask`                        | stage type (3.3), stage status flask (3.4)                                 |
| `PingPill` (+ `pingMeta()`)                      | ping type pill (3.5)                                                       |
| `Vial`, `GuildAvatar`                            | workshop and guild glyphs                                                  |
| `PageHeader`, `Breadcrumbs`                      | page header pattern (§7)                                                   |
| `GlassGroup`                                     | labelled control cluster (4.5)                                             |
| `Segmented`                                      | filters and pickers (4.4)                                                  |
| `Popover`                                        | beside its anchor; `.mi` rows, `.div` dividers inside                      |
| `Modal`, `confirmDialog()`                       | dialogs; `await confirmDialog({title, body, action})`. Never `confirm()`   |
| `Banner`                                         | in-flow alert (4.7)                                                        |
| `EmptyState`, `Skeleton`, `ErrorCard`, `Updated` | empty, loading, error and live states (4.17)                               |
| `Toggle`, `TagInput`                             | form controls (4.14)                                                       |
| `Sparkline`                                      | vitals tile (4.16); bind `hover` across tiles for a shared crosshair       |

## App modules

- `$lib/guild`: `to.*` builds every in-app URL. Don't hand-write `/g/…`
  paths. `currentGuild()`, `workshopVial()`.
- `$lib/fleet.svelte`: shared polled runs, pending pings, projects and
  settings (sidebar, desk, lists). Call `fleet.refresh()` after an action
  that changes them.
- `$lib/format`: `stateLabel`, `stateHue`, `shortId`, `ago`, `age`,
  `fmtCost` ($0.000), `fmtClock` (24h), `fmtDuration`, `fmtBytes`.
- `$lib/lore`: `lore('workshop')` and friends, for the flavor switch.
  Status words are never renamed.
- `$lib/prefs.svelte`: theme, flavor, sidebar collapse.
