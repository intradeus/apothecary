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

A theme is one `:root[data-theme='id']` block in `tokens.css` plus one entry in `$lib/themes.ts`. Every theme keeps the pairing: `--emb` means something is brewing, `--sky` means you can act. Text in a hue uses its text variant (`--embTx`, `--skyTx`, `--rosTx`, `--link`); the bare hue is for fills, dots and borders.

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

- Surfaces materialise from their trigger: scale, blur and opacity together (`transition:materialize`).
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

| Kind          | Classes                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Materials     | `.c` (window glass: what sits on the backdrop; a `.c` inside a `.c` falls back to an inset by itself), `.in` (inset: a group or field inside a window), `.isl` (island: opaque, floats over content), `.skycard` (solid sky: a human must act; it re-points the tokens, so what sits in it reads as ink), `.card-in` (card inside a pane). Never glass on glass. |
| Buttons       | Bare `<button>` is the secondary button. `.btn-primary` (one per group), `.btn-sel`, `.btn-danger` (outline, label ends in "…", confirms), `.btn-danger-solid` (dialogs only), `.btn-hot` / `.btn-hot-outline` (the ink pair inside a `.skycard`), `.btn-dashed`, `.btn-ghost`, `.btn-icon` (`.btn-icon.btn-sm`: 20px, in chips and rows), `.btn-notyet`, `.btn-term`. Sizes `.btn-sm` / `.btn-md` / `.btn-lg`. A toggle button shows its state with `.btn-sel` **and** `aria-pressed`. `.linklike` (`.brand` for the brand hue). `<a class="btn …">` for links. |
| Pills         | `.pill` + `.t-<hue>` (tinted) or `.t-hot` (solid sky). Modifiers `.sm`, `.flat`, `.sq`, `.mono`. Optional `<span class="dot">`. `.chips` is a wrapping row of them.                                                                                                                                                                                                                                                                                                                                                                            |
| Counts, tags  | `.count` (`.hot` sky, `.bad` rose; `.sm` compact: window tabs, icon badges; `.mark` the round 22px needs-you mark, with `.hot`), `.tag` (mono key/hash), `.kbd`                                                                                                                                                                                                                                                                                                                                                                                |
| Live          | `.ldot` (+ `.hue-x`, `.pulse`, `.sm`, `.flat`, `.hollow`, `.dashed`), `.unsaved`                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| Hue variables | `.h-<hue>` sets `--h` for washes and accents. `.wash` is a tinted corner.                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| Selection     | `.bead` (the one selected look), `.on` inside `.seg`, `.tabs`, `.mi`, `.rowbtn` (always `.on`, never `.sel`)                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| Controls      | `.seg` (use `Segmented`), `.tabs` (use `Tabs`), `.mi` + `.menu-div` (menu rows), `.rowbtn` (full-width list row), `.choices` (`.row`): a set of radios/checkboxes                                                                                                                                                                                                                                                                                                                                                                                |
| Lists         | `table` (sticky th), `.rows > .r` grid rows (set `grid-template-columns` per list), `.stretch` (the row link covering the row), `.over-stretch` (links above it)                                                                                                                                                                                                                                                                                                                                                                                 |
| Panes         | `.pane.c` with `.pane-head`, `.pane-body`, `.pane-sub`, `.pane-foot`. `.card-head`. `.card-grid` (`--min`). `dl.kv` (`.code`, `--k`). `.note` (dashed hint).                                                                                                                                                                                                                                                                                                                                                                                     |
| Pages         | `.page.c` (a page body: one window filling what the header leaves, its content centred up to `--content-max`, DESIGN 7), `.readable` (passes that down a wrapper), `.sections` (whole sections side by side), `.filters` (the row above a list; `--filter-h`)  |
| Views         | `.view` (a desk window's scroll body), `.vbar` (its toolbar, `.hint` at the right)                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| Forms         | Inputs are styled by default; there is no native `<select>`, checkbox or radio: use `Select`, `Checkbox`, `Radio`. `.field > label + input + .help/.err` (a group: `fieldset.field > legend`; no label element: `.flabel`), `.req` (the required `*`), `.fieldbox` (a component's own field box, parts inside are `input.bare`), `.invalid`, `.grid2`, `.wide`, `.form-dense` (dense fields; `Select` follows it), `.form-actions`, `input.bare`                                                                                                 |
| Text          | `.mono`, `.display`, `.mut`, `.fnt`, `.tx`, `.ell`, `.num`, `.lede`, `.lbl`, `.empty-line`, `.hue-*`, `.disclosure`                                                                                                                                                                                                                                                                                                                                                                                                                                |
| Documents     | `.md` (rendered markdown; prefer `Markdown`), `.md.doc`, `.paper`, `.diff` (prefer `CodeView`), `.log` (program output: `pre.log`, or a block of lines)                                                                                                                                                                                                                                                                                                                                                                                          |
| Overlays      | `.scrim`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| Page          | `.page` (the standard scrolling page body)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| Motion        | `.pulse`, `.pulse-slow`, `.caret`, `.skel`, `.materialize`, `.bumped`, `[aria-busy='true']`                                                                                                                                                                                                                                                                                                                                                                                                                                                      |

## Components (`lib/ui`)

| Component                                                                                                           | Purpose                                                                                                                                                                                                                   |
| ------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `StatusChip` (`compact` = dot)                                                                                      | Run state (3.1). `suffix` adds the lore word.                                                                                                                                                                             |
| `WorkspaceStatus` (+ `wsValue()`)                                                                                   | The workspace axis (3.2). Never merge it into the run chip.                                                                                                                                                               |
| `StageChip`, `StageFlask`                                                                                           | Stage type (3.3) and stage status flask (3.4)                                                                                                                                                                             |
| `PingPill` (+ `pingMeta()`)                                                                                         | Ping type (3.5)                                                                                                                                                                                                           |
| `StatusMark`                                                                                                        | A round ✓ / ✕ / ! / live / pending glyph (timelines, checklists, steps)                                                                                                                                                   |
| `Vial`, `UserAvatar`, `Logo`, `Noise` (the backdrop; `noise.blue` and `noise.speed` drive it)                                                                         | Identity glyphs                                                                                                                                                                                                           |
| `PageHeader`, `Breadcrumbs`, `SectionHeader`                                                                        | Page and section headings                                                                                                                                                                                                 |
| `HeaderAction`, `ActionMenu`                                           | A page's main action. `HeaderAction` puts it in a shared header from a page below it (the layout calls `provideHeaderSlot`). `ActionMenu` is that button when there are several kinds to start from. |
| `TransportIcon`                                           | Play, pause and stop on buttons, drawn so they match. |
| `GlassGroup`, `Segmented`, `Tabs`, `SideNav`, `Popover`, `Modal`, `Sheet`                                           | Controls and surfaces, all with motion built in. `Segmented` picks (a filter, a view: a radio group); `Tabs` are a page's or pane's sections (links or buttons). `Popover` takes `portal`, `matchWidth`, `flip` for lists |
| `confirmDialog()` + `ConfirmHost`                                                                                   | Destructive confirms                                                                                                                                                                                                      |
| `toast()` + `Toaster`                                                                                               | Completion feedback                                                                                                                                                                                                       |
| `Banner` (alerts), `Callout` (informational notes)                                                                  | Strips                                                                                                                                                                                                                    |
| `EmptyState` (`compact`, `inline`), `Skeleton` (`pane`), `Loading`, `ErrorCard` (`action`, `ondismiss`), `Updated`  | The four states                                                                                                                                                                                                           |
| `Select` (+ `SelectOption`: `group`, `detail`, `disabled`, `dot`)                                                   | The dropdown: popover list, keyboard and type-ahead, a filter on long lists, `size` `sm`, `inline`, `bare`, `mono`, `required`                                                                                            |
| `Checkbox`, `CheckboxGroup`, `Radio`, `RadioGroup` (+ `ChoiceOption`)                                               | Choices: any of a set, or exactly one. A setting that applies at once is a `Toggle`                                                                                                                                       |
| `Toggle`, `TagInput`, `Stepper`, `DurationInput`, `SearchInput` (`size`), `KeyValueEditor`, `SettingRow`, `SaveBar` | Form controls                                                                                                                                                                                                             |
| `Markdown` (`doc`, `quiet`), `CodeView`                                                                             | Reading material                                                                                                                                                                                                          |
| `FileChip`                                                                                                          | One file (thumb, name, size, missing/refused, removable)                                                                                                                                                                  |
| `Meter`                                                                                                             | Capacity bar                                                                                                                                                                                                              |

Modules: `motion.ts` (springs, `materialize`, `slide`, `scrim`, `indicator`, `bump`, `dragDismiss`, `project`, `rubberband`), `portal.ts` (`use:portal`: under `<body>`, out of a glass pane's clip; `Modal` and `Sheet` always use it), `tokens.ts`, `icons.ts` (`I`), `hues.ts`, `trap.ts`, `confirm.svelte.ts`, `toast.svelte.ts`, `html.ts` (`esc`), `duration.ts`.

## App modules

- `$lib/urls`: `to.*` builds every in-app URL. Never hand-write a path.
- `$lib/session.svelte`: who is signed in (`session.user`, `session.admin`), the setup/sign-in/sign-out flow; the root layout gates the shell on it.
- `$lib/nav`: the top-level destinations (sidebar, tab bar, palette, chords).
- `$lib/fleet.svelte`: the shared polled state. It provides runs, pings, projects and the deployment's limits, plus `projectName()`, `capacity`, `runsByTicket`, `liveByProject` and `pingsByProject`. Call `fleet.refresh()` after an action that changes any of them.
- `$lib/format`: `stateLabel`, `stateHue` (`stateHueClass`: the same as a `.hue-*` class), `shortId`, `ago`, `age`, `fmtCost`, `fmtClock`, `fmtDuration`, `fmtBytes`.
- `$lib/markdown`: the one safe markdown renderer.
- `$lib/themes`, `$lib/prefs.svelte`: the theme registry, plus the theme and sidebar preferences.
- Domain building blocks:
  - `$lib/run`: `RunTable` (`detail`), `RunRow`, `MrLink`, `runFacts`
  - `$lib/workshop`: `WorkshopName`
  - `$lib/tickets`: `TicketTable`
  - `$lib/skills`: `SkillsetPicker` (what a run takes along), `SkillFileEditor` (CodeMirror for a skill's files), `files.ts` (bytes ↔ text)
