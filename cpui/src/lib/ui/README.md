# cpui design system

The spec is `docs/UX/UI/DESIGN.md`. The system has three layers, and each one builds only on the layer below it:

1. **Tokens** (`src/styles/tokens.css`) hold every colour, size, radius, shadow, z-index, duration and easing, plus the themes.
2. **Shared CSS** (`base.css`, `motion.css`, `components.css`, all imported by `src/app.css`) holds element defaults and classes.
3. **Components** (`src/lib/ui/`) are generic. They may import `$lib/domain` types and `$lib/format`, but never `$lib/api` or `$lib/fleet`.

Domain components (`lib/run`, `lib/workshop`, `lib/tickets`, …) and pages build on the layers above them.

## Rules

- **No literals below the token layer.** Nothing below it writes a hex, an `rgba()`, a `px` font size or radius, a z-index number, or a duration. Use tokens: `var(--fs-label)`, `var(--r-card)`, `var(--z-pop)`, `var(--spring)`. Layout px values (paddings, gaps, widths of a specific grid) are fine.
- **Theme rules live in tokens.css.** Never write a `[data-theme]` rule anywhere else.
- **Use the shared piece.** If something below already exists, use it rather than restyling locally. If a local style is needed twice, promote it.
- **Glyphs come from `I`** (`./icons`). Hues come from `./hues`: `STAGE_HUE`, `EDGE_HUE`, `TOOL_HUE`, `GIT_HUE`, `TONE_HUE`, and the `Hue` type.
- **No `!important`.** Element defaults sit in `:where()` with zero specificity, so a class always wins.

## Themes

A theme is one `:root[data-theme='id']` block in `tokens.css` plus one entry in `$lib/themes.ts`.

- `npm run check` runs `check:themes`, which fails if a theme misses a themed token.
- User preferences (`prefers-reduced-motion`, `-transparency`, `-contrast`) are applied in tokens.css against each theme's own tokens, so new themes get them for free.
- Code that CSS can't style (canvas, xterm) reads values with `token('--name')` from `./tokens`.

## Feel (DESIGN 2.5, Apple's fluid-interface rules)

**Press.** Buttons sink on pointer-down (`--press-scale`) and spring back. Options inside a control (segments, tabs, `.mi`, `.rowbtn`) dim instead of scaling. Hovers stay instant.

**Moves use the springs.**

- `--spring` is critically damped (damping 1.0) and is the default.
- `--spring-bounce` (damping 0.8) is only for things released with momentum: a dragged window, a flicked sheet.
- Both are the real spring equations sampled into `linear()`, so CSS transitions stay interruptible (they retarget from the live value).

**Enter and exit.**

- Glass surfaces materialise from their trigger: scale, blur and opacity together (`transition:materialize`).
- Sheets slide from their edge and leave the same way (`transition:slide`).
- Svelte's `transition:` reverses mid-flight, so an interrupted close never jumps.

**Selection slides.** Put `use:indicator` on a container of options. One bead springs to whichever child has `.on`.

**Gestures** (`use:dragDismiss`, desk drags):

- commit after a ~10px intent threshold
- track 1:1 from the grab offset, with pointer capture
- rubber-band at limits
- on release, project momentum with `project()` and hand the velocity to the exit

**Feedback.**

- Status: live dots, `Updated`, `Loading`.
- Completion: `toast()` for actions confirmed away from where the user looks, and `aria-busy` on a button while its action runs.
- Error: `ErrorCard` inline, next to the cause.
- A count that goes up pops once (`use:bump`).
- Destructive actions confirm with `confirmDialog()`. Never use `confirm()`.

**Reduced motion** zeroes the move tokens, so everything above degrades to fades with no per-component work.

## Shared classes

| Kind          | Classes                                                                                                                                                                                                                                                                                                                                                                                                                    |
| ------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Materials     | `.lg` (control glass), `.lg-pop` (popover glass), `.c` (content glass), `.card-in` (card inside a pane). Never glass on glass.                                                                                                                                                                                                                                                                                             |
| Buttons       | Bare `<button>` is the secondary button. `.btn-primary` (one per group), `.btn-sel`, `.btn-danger` (outline, label ends in "…", confirms), `.btn-danger-solid` (dialogs only), `.btn-hot` / `.btn-hot-outline` (a human must act now), `.btn-dashed`, `.btn-ghost`, `.btn-icon`, `.btn-notyet`, `.btn-term`. Sizes `.btn-sm` / `.btn-md` / `.btn-lg`. `.linklike` (`.brand` for parchment). `<a class="btn …">` for links. |
| Pills         | `.pill` + `.t-<hue>` (tinted) or `.t-hot` (solid amber). Modifiers `.sm`, `.flat`, `.sq`, `.mono`. Optional `<span class="dot">`. `.chips` is a wrapping row of them.                                                                                                                                                                                                                                                      |
| Counts, tags  | `.count` (`.hot` amber, `.bad` rose), `.tag` (mono key/hash), `.kbd`                                                                                                                                                                                                                                                                                                                                                       |
| Live          | `.ldot` (+ `.hue-x`, `.pulse`, `.sm`, `.flat`, `.hollow`, `.dashed`), `.unsaved`                                                                                                                                                                                                                                                                                                                                           |
| Hue variables | `.h-<hue>` sets `--h` for washes and accents. `.wash` is a tinted corner.                                                                                                                                                                                                                                                                                                                                                  |
| Selection     | `.bead` (the one selected look), `.on` inside `.seg`, `.tabs`, `.mi`, `.rowbtn`                                                                                                                                                                                                                                                                                                                                            |
| Controls      | `.seg` (prefer `Segmented`), `.tabs`, `.mi` + `.menu-div` (menu rows), `.rowbtn` (full-width list row)                                                                                                                                                                                                                                                                                                                     |
| Lists         | `table` (sticky th), `.rows > .r` grid rows (set `grid-template-columns` per list), `.stretch` (the row link covering the row), `.over-stretch` (links above it)                                                                                                                                                                                                                                                           |
| Panes         | `.pane.c` with `.pane-head`, `.pane-body`, `.pane-sub`, `.pane-foot`. `.card-head`. `.card-grid` (`--min`). `dl.kv` (`.code`, `--k`). `.note` (dashed hint).                                                                                                                                                                                                                                                               |
| Views         | `.view` (a desk window's scroll body), `.vbar` (its toolbar, `.hint` at the right)                                                                                                                                                                                                                                                                                                                                         |
| Forms         | Inputs are styled by default. `.field > label + input + .help/.err`, `.grid2`, `.wide`, `.form-dense`, `.form-actions`, `input.bare`                                                                                                                                                                                                                                                                                       |
| Text          | `.mono`, `.serif`, `.mut`, `.fnt`, `.tx`, `.ell`, `.num`, `.lede`, `.lbl`, `.empty-line`, `.hue-*`, `.disclosure`                                                                                                                                                                                                                                                                                                          |
| Documents     | `.md` (rendered markdown; prefer `Markdown`), `.md.doc`, `.paper`, `.diff` (prefer `CodeView`), `pre.log`                                                                                                                                                                                                                                                                                                                  |
| Overlays      | `.scrim`                                                                                                                                                                                                                                                                                                                                                                                                                   |
| Page          | `.page` (the standard scrolling page body)                                                                                                                                                                                                                                                                                                                                                                                 |
| Motion        | `.pulse`, `.pulse-slow`, `.caret`, `.skel`, `.materialize`, `.bumped`, `[aria-busy='true']`                                                                                                                                                                                                                                                                                                                                |

## Components (`lib/ui`)

| Component                                                                                                          | Purpose                                                                 |
| ------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------- |
| `StatusChip` (`compact` = dot)                                                                                     | Run state (3.1). `suffix` adds the lore word.                           |
| `WorkspaceStatus` (+ `wsValue()`)                                                                                  | The workspace axis (3.2). Never merge it into the run chip.             |
| `StageChip`, `StageFlask`                                                                                          | Stage type (3.3) and stage status flask (3.4)                           |
| `PingPill` (+ `pingMeta()`)                                                                                        | Ping type (3.5)                                                         |
| `StatusMark`                                                                                                       | A round ✓ / ✕ / ! / live / pending glyph (timelines, checklists, steps) |
| `Vial`, `GuildAvatar`, `UserAvatar`, `Logo`                                                                        | Identity glyphs                                                         |
| `PageHeader`, `Breadcrumbs`, `SectionHeader`                                                                       | Page and section headings                                               |
| `GlassGroup`, `Segmented`, `SideNav`, `Popover`, `Modal`, `Sheet`                                                  | Controls and surfaces, all with motion built in                         |
| `confirmDialog()` + `ConfirmHost`                                                                                  | Destructive confirms                                                    |
| `toast()` + `Toaster`                                                                                              | Completion feedback                                                     |
| `Banner` (alerts), `Callout` (informational notes)                                                                 | Strips                                                                  |
| `EmptyState` (`compact`, `inline`), `Skeleton` (`pane`), `Loading`, `ErrorCard` (`action`, `ondismiss`), `Updated` | The four states                                                         |
| `Toggle`, `TagInput`, `Stepper`, `DurationInput`, `SearchInput`, `KeyValueEditor`, `SettingRow`, `SaveBar`         | Form controls                                                           |
| `Markdown` (`doc`, `quiet`), `CodeView`                                                                            | Reading material                                                        |
| `FileChip`                                                                                                         | One file (thumb, name, size, missing/refused, removable)                |
| `Meter`                                                                                                            | Capacity bar                                                            |

Modules: `motion.ts` (springs, `materialize`, `slide`, `scrim`, `indicator`, `bump`, `dragDismiss`, `project`, `rubberband`), `tokens.ts`, `icons.ts` (`I`), `hues.ts`, `trap.ts`, `confirm.svelte.ts`, `toast.svelte.ts`, `html.ts` (`esc`), `duration.ts`.

## App modules

- `$lib/guild`: `to.*` builds every in-app URL. Never hand-write `/g/…`.
- `$lib/nav`: the top-level destinations (sidebar, tab bar, palette, chords).
- `$lib/fleet.svelte`: the shared polled state. It provides runs, pings, projects and settings, plus `projectName()`, `capacity`, `runsByTicket`, `liveByProject` and `pingsByProject`. Call `fleet.refresh()` after an action that changes any of them.
- `$lib/format`: `stateLabel`, `stateHue`, `shortId`, `ago`, `age`, `fmtCost`, `fmtClock`, `fmtDuration`, `fmtBytes`.
- `$lib/markdown`: the one safe markdown renderer.
- `$lib/themes`, `$lib/prefs.svelte`: the theme registry, plus the theme and sidebar preferences.
- Domain building blocks:
  - `$lib/run`: `RunTable` (`detail`), `RunRow`, `MrLink`, `runFacts`
  - `$lib/workshop`: `WorkshopName`
  - `$lib/tickets`: `TicketTable`
