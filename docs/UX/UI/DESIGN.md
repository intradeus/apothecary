# Apothecary — Design System & UI Spec

Reference implementation: `Apothecary Live Run v10.dc.html`. It covers the live run, All-guilds desk, sidebar, org switcher and window manager. Where this document and the file disagree, **the file wins**.

Target stack: SvelteKit SPA, Svelte Flow (xyflow), xterm.js, SVG sparklines, polling (2–5 s) + SSE on the run page.

---

## 1. Concept & voice

**"Software, bottled."** A ticket goes in as ingredients; a run brews it through a recipe (flow); a reviewed MR comes out.

The visual metaphor is a **dark medicine bottle**:
- near-black green glass
- a faint gold alchemical chart turning behind it
- labels and stoppers in parchment
- liquid levels showing progress

The UI is professional and dense (an IDE-grade power tool). The lore appears only in:
1. a serif italic accent font for display words
2. vial/flask iconography for progress
3. a small set of renamed nouns (below). There is one vocabulary: no plain/lore switch.

| Domain object | UI label |
|---|---|
| Organisation | **Guild** |
| Project | **Workshop** |
| Run | Batch (badge) · "brewing" (running) · "batch spoiled" (failed) |
| Flow | The formula |
| Plan | The recipe |
| "Needs you" | Awaiting your hand |
| Composer placeholder | Whisper to the builder… |
| Empty window desk | "An empty bench" |
| Egress blocked | "toxic hosts, kept off the shelf" |
| Create org | + Found a guild |

Rules:
- Ticket, stage, ping, workspace, MR, egress and all state names stay technical. Never rename status vocabulary.
- Copy is short, sentence case, and uses no exclamation marks.
- Destructive dialogs say exactly what happens to the **run** vs the **workspace**.

**Domain hierarchy** (multi-tenant):

```
User ─┬─ member of N Guilds (orgs), with a role per guild (Owner / Maintainer / Engineer)
      └─ Guild ── N Workshops (projects) ── N Tickets ── N Runs
```

Integrations, channels and limits live at guild level. Projects own flows, commands and egress policy.

---

## 2. Foundations

### 2.1 Typography

Load from Google Fonts: `Geist:wght@300..800`, `Geist Mono:wght@400;500;600`, `Instrument Serif:ital@0;1`.

| Role | Font | Size / weight | Notes |
|---|---|---|---|
| Body (base) | Geist | 13px / 400, line-height 1.5 | `-webkit-font-smoothing: antialiased` |
| UI labels, tabs | Geist | 12–12.5px / 500; active 600 | |
| Meta, captions | Geist | 11–11.5px / 400–500 | color `--mut` or `--fnt` |
| Page title (h1) | Geist | 24–26px / 600, letter-spacing −.02em, line-height 1.1 | `text-wrap: balance`, `text-shadow: 0 1px 14px var(--tshadow)` |
| Display accent | Instrument Serif *italic* | h1 accent 29–31px / 400; section titles 18–20px; inline 14–16px | One accent phrase per title ("…to *payment webhooks*"). Also used for window/section headings ("Organisations", "The recipe", "Layouts") |
| Agent "thinking" | Instrument Serif italic | 16px, line-height 1.35, color `--mut` | |
| Code, ids, hashes, numbers, costs, ports, branches | Geist Mono | 10–12px / 500–600 | Run ids `r_7f3a`, ws ids, `@3c9e1a`, `$1.234`, counts in badges |
| Inline code | Geist Mono 12px | `padding:1px 5px; radius 5px; background: var(--code)` | |

Formatting conventions:
- relative times ("3m ago", "just now")
- 24-hour clock
- cost as `$0.000` (3 decimals)
- byte sizes
- counts as "×3" in alerts

### 2.2 Color tokens

All tokens are CSS custom properties set on the app root. They are swapped for light mode.

**Base (dark — "Onyx bottle", the locked default)**

| Token | Dark | Light | Use |
|---|---|---|---|
| `--tx` | `#e6efe8` | `#0f2a1b` | primary text |
| `--mut` | `#93ad9c` | `#4e6a58` | secondary text |
| `--fnt` | `#5f7c69` | `#7f9787` | tertiary / disabled / separators "/" |
| `--brand` | `#efe6cf` (parchment) | `#1f5a38` | primary CTA fill, caret, focus, drop zones |
| `--onBrand` | `#10261a` | `#f4f1e6` | text on brand |
| `--p1` | `rgba(170,255,205,.045)` | `rgba(255,255,255,.5)` | faint fill / row hover |
| `--p2` | `rgba(170,255,205,.065)` | `rgba(255,255,255,.55)` | card-in-card, secondary button fill |
| `--p3` | `rgba(200,255,220,.13)` | `rgba(255,255,255,.85)` | hover on menu items, selected list row |
| `--ln` | `rgba(210,255,225,.13)` | `rgba(255,255,255,.7)` | hairline |
| `--ln2` | `rgba(210,255,225,.18)` | `rgba(20,60,35,.14)` | stronger hairline, dashed outlines |
| `--code` | `rgba(0,0,0,.22)` | `rgba(15,60,35,.06)` | code / diff background |
| `--spec` | `inset 0 1px 0 rgba(255,255,255,.12)` | `inset 0 1px 0 rgba(255,255,255,.9)` | top specular on small buttons |

**Semantic hues.** Each hue `X` also derives `--XBg`, `--XLn` and `--XGlow` with `color-mix(in oklch, X N%, transparent)`:
- `--XBg` at 15% (dark) or 12% (light): tinted fill
- `--XLn` at 38%: border
- `--XGlow` at 14%: outer glow

| Token | Dark | Light | Meaning |
|---|---|---|---|
| `--az` (azure) | `#7fb2ff` | `#2f6fe0` | active / running / provisioning / agent stage / read tool |
| `--amb` (amber) | `#f5b54a` | `#b8780c` | needs a human: waiting_on_human, pings, gates, abandoned, edit tool |
| `--grn` (green) | `#7ee2a8` | `#1c8a55` | success: completed, workspace live, command stage, + lines, exec tool |
| `--ros` (rose) | `#ff7a70` | `#d6403c` | failure / danger: failed, blocked egress, − lines, Stop |
| `--vio` (violet) | `#c3a2ff` | `#7a52d6` | action stage, search tool, permission ping |
| `--tea` (teal) | `#5fd6cc` | `#138a85` | wait stage, on_timeout, network, tunnel logs |
| `--brand` | parchment | | brand-only: primary buttons, MR link, user bubble tint (`--brandBg`/`--brandLn`) |

Amber is the **attention color**. Solid amber fill with dark ink `#1d1406` is reserved for "a human must act now": inbox count badges, the Waiting chip and the "N need you" pill. Everything else uses tinted fills.

**Scene (background), dark Onyx.** This is a layered gradient behind everything:

```
radial-gradient(ellipse 34% 48% at 10% 20%, rgba(11,40,23,.38), transparent 70%),
radial-gradient(ellipse 30% 42% at 82% 72%, rgba(6,32,24,.32), transparent 70%),
radial-gradient(ellipse 26% 26% at 58% 6%,  rgba(220,190,110,.05), transparent 70%),
radial-gradient(ellipse 50% 40% at 40% 105%, rgba(2,20,11,.7), transparent 70%),
linear-gradient(160deg, #020906, #010302 60%, #010604)
```

- `body` background: `#000302`
- Light scene: same structure with `rgba(90,190,130,.45)`, `rgba(60,170,140,.35)`, `rgba(240,210,140,.35)` over `linear-gradient(160deg,#eef5ef,#dbe9df 60%,#e4efe6)`; body `#cfdcd2`.

Onyx shade values, for reference:

| Key | Value |
|---|---|
| selection fill `--sel` / `--railSel` | `#0f271b` |
| content pane `--cBg` | `rgba(1,6,3,.64)` |
| rail tint | `rgba(1,6,3,.6)` |
| thick popover glass `--thick` | `rgba(1,8,4,.78)` |

**Backdrop "Sigils"** is a `<canvas>` behind everything, `pointer-events:none`, redrawn at about 20 fps:
- **Large chart:** centred at 66% / 56%, radius 420, alpha .14 dark / .22 light. Rotates at +0.012 rad/s.
- **Small chart:** centred at 13% / 20%, radius 170, alpha .11 / .18. Rotates at −0.02 rad/s.
- **Stroke color:** gold `rgb(233,205,130)` in dark, `rgb(120,90,30)` in light; 1px lines.
- **Each chart contains:**
  - four concentric rings
  - 120 tick marks
  - the seven planetary glyphs ☉☽☿♀♂♃♄ in Instrument Serif, with `\uFE0E` to force text presentation
  - two counter-rotating triangles (a hexagram) and a square
  - dotted crosshairs
- **Glow:** a soft gold radial glow sits under the large chart.
- **Reduced motion:** with `prefers-reduced-motion`, draw one frame and stop.

**Cursor light.** A full-screen overlay at z-index 40 (above content, `pointer-events:none`, `mix-blend-mode:screen`):

```
radial-gradient(360px circle at var(--lx) var(--ly), var(--light), transparent 65%)
```
- `--light` is `rgba(220,255,230,.035)` in dark and `rgba(255,255,255,.12)` in light.
- Update `--lx` / `--ly` on mousemove.

### 2.3 Materials: two kinds of glass

This is the core of the look. **Never stack glass-on-glass.** A control sits on a content pane, or floats over the scene, not on another control group.

**A. Control glass (`lg`)** is used for anything that *floats and controls*:
- the sidebar
- header control groups
- the run badge
- tab capsules
- the composer
- the alert banner
- popovers and modals
- the Layouts menu

| Token | Dark | Light |
|---|---|---|
| `--lgBg` | `radial-gradient(130% 90% at 12% -35%, rgba(255,255,255,.11), transparent 48%), linear-gradient(180deg, rgba(210,255,228,.09), rgba(170,255,210,.045))` | `radial-gradient(130% 90% at 12% -35%, rgba(255,255,255,.7), transparent 50%), linear-gradient(180deg, rgba(255,255,255,.55), rgba(255,255,255,.4))` |
| `--lgBf` (backdrop-filter) | `blur(20px) saturate(185%) brightness(1.07)` | `blur(20px) saturate(165%)` |
| `--lgLn` (1px border) | `rgba(220,255,235,.14)` | `rgba(255,255,255,.75)` |
| `--lgRim` (box-shadow) | `inset 0 1px 0 rgba(255,255,255,.28), inset 0 -1px 0 rgba(255,255,255,.07), inset 0 -14px 24px -18px rgba(170,255,210,.18), 0 14px 34px rgba(0,8,3,.4)` | `inset 0 1px 0 rgba(255,255,255,.9), inset 0 -10px 20px -16px rgba(255,255,255,.5), 0 14px 34px rgba(20,60,35,.13)` |

Popovers and modals add an opaque underlay so text behind never bleeds through:

```
background: var(--lgBg), var(--popBg)
```
`--popBg` is `rgba(6,16,10,.94)` dark and `rgba(250,252,250,.95)` light.

Modal scrim: `rgba(2,12,6,.45)` + `backdrop-filter: blur(6px)`.

**B. Content glass (`c`)** is used for *panes that hold reading material*: windows/leaves and overview cards. It is darker and more solid, so code and transcripts stay legible.

| Token | Dark (Onyx) | Light |
|---|---|---|
| `--cBg` | `rgba(1,6,3,.64)` | `rgba(255,255,255,.58)` |
| `--cBf` | `blur(28px) saturate(155%)` | `blur(28px) saturate(150%)` |
| `--cLn` | `rgba(210,255,225,.11)` | `rgba(255,255,255,.75)` |
| `--cSh` | `inset 0 1px 0 rgba(255,255,255,.1), 0 20px 50px rgba(0,6,2,.4)` | `inset 0 1px 0 rgba(255,255,255,.95), 0 20px 50px rgba(20,60,35,.1)` |

**Sidebar fill** (`--railBg`):
- dark: `linear-gradient(180deg, rgba(1,6,3,.6), rgba(1,6,3,.6)), linear-gradient(180deg, rgba(225,255,238,.07), rgba(160,255,200,.02))`
- light: `linear-gradient(180deg, rgba(16,52,32,.84), rgba(12,42,26,.9))`

The sidebar stays **dark green in light mode too**, with light text.

### 2.4 Shape, spacing, elevation

**Radii** are concentric: outer containers are rounder than inner ones.

| Element | Radius |
|---|---|
| Sidebar, windows/leaves, overview sections | 16px |
| Header control groups, run badge, popovers | 17px |
| Overview "Needs you" panel | 18px |
| Composer and ping card | 20px |
| Modal | 26px |
| Cards inside panes (prompt, tool group, plan) | 13–14px |
| Stage pills | 14px |
| Buttons | 12px (header) / 9–10px (compact, sidebar, popover rows) |
| Tabs | 9px |
| Nav items | 10px |
| Tiny icon buttons | 7px |
| Inline code, mono tags | 5–6px |
| Status chips and type pills | 999px |

- **Spacing:** 4px base. Common steps are 4 / 6 / 8 / 10 / 12 / 14 / 16 / 20 / 22.
  - Window gap and split gutter: **8px**.
  - Shell margin around the sidebar: 6px.
  - Desk margin: 10px top, 16px sides, 12px bottom.
- **Hairlines:** always 1px, using `--ln`, `--cLn` or `--lgLn`. Dashed `--ln2` means "empty / not yet / drop here".
- **Glows:** live indicators get `box-shadow: 0 0 8–10px <hue>`. The state glow (`--stateGlow`, the run color mixed at 45%) sits in the corner of the run badge.

### 2.5 Motion

| Name | Keyframes | Use |
|---|---|---|
| `ap-pulse` | opacity 1→.4, scale 1→.75; 1.4s ease-in-out infinite (2s for ambient) | live dots: running, in-progress tool, current plan item, waiting chip |
| `ap-blink` | steps(1) 1s | streaming caret (7×15px parchment bar with glow) |
| `ap-slosh` | translateX ±2px | liquid in the active stage vial |
| `ap-bubble` | translateY −10px + fade | bubble rising in the active stage vial |

**Feel.** These rules follow Apple's fluid-interface principles. The implementation is in `cpui/src/styles/motion.css` and `cpui/src/lib/ui/motion.ts`, with the tokens in `cpui/src/styles/tokens.css`.

- **Respond on press.**
  - Hovers are instant, with no transitions on dense UI.
  - Buttons sink on pointer-down (`--press-scale` .97, 100ms) and spring back on release.
  - Options inside a control (segments, tabs, menu rows, list rows) dim on press instead of scaling.
- **One spring for movement.** `--spring` is critically damped (damping 1.0, response .35s).
  - `--spring-bounce` (damping .8, response .4s) is only for things released with momentum: a dragged window, a flicked sheet.
  - Both are the real spring equations sampled into CSS `linear()`, so transitions stay interruptible: flipping a state mid-flight retargets from the live value.
- **Enter and exit along the same path.**
  - Popovers and menus materialise from their trigger: scale .96, blur and opacity together, with `transform-origin` on the trigger's side.
  - Dialogs materialise over a fading scrim.
  - Sheets and toasts slide from their edge and leave the same way.
  - An interrupted open or close reverses from where it is.
- **Selection slides.** In segmented controls, tabs and side navs, one bead springs between options.
- **Gestures track 1:1.**
  - A drag commits after a ~10px intent threshold and then follows the pointer from the grab offset, with pointer capture.
  - At a limit it rubber-bands instead of stopping hard.
  - On release, momentum is projected (decay .998) to choose the target, and the release velocity is handed to the animation.
  - This applies to window tabs and gutters, sheets and toasts.
- **Feedback.**
  - Status: live dots, "updated 2s ago".
  - Completion: a toast for things confirmed away from the pointer, and `aria-busy` on a button while its action runs.
  - Error: inline, next to the cause.
  - A count that goes up pops once.
- **Theme changes cross-fade** (`--dur-theme`) instead of jumping in brightness.
- **Respect the user's settings.**
  - `prefers-reduced-motion` zeroes every move, so motion becomes cross-fades. The sigils animation, slosh and bubbles stop, and pulses become static dots.
  - `prefers-reduced-transparency` makes the glass solid.
  - `prefers-contrast: more` strengthens lines and secondary text.

### 2.6 Iconography

- **No icon font.** Use Unicode glyphs with `\uFE0E` (text presentation) at 13–16px:
  - nav: ◈ Desk, ✉ Inbox, ▶ Runs, ☰ Tickets, ⚙ System
  - controls: ⌕ search, ❚❚ pause, ■ stop, ↻ retry, ↗ external, ▾ menu, ↩ loop-back, × close, « » collapse/expand, ⇅ switcher
- **Vial (workspace/project glyph):** 10×13px, `border-radius:2px 2px 5px 5px`, 1px border in the project hue at 60%. The fill is `linear-gradient(transparent X%, hue X%)`, where the fill level is decorative per project, plus a glow `0 0 8px hue@35%`. An idle project uses an empty vial with a `rgba(255,255,255,.2)` border.
- **Stage flask:** 14×20px, `border-radius:3px 3px 7px 7px`, 1.5px border in the stage-status color. The inner liquid height equals progress (done 100%, running ~55% with slosh and bubble, pending 0%).
- **Logo:** a 30–32px rounded square (radius 12px) holding a "℞" in Instrument Serif in parchment. Background `radial-gradient(circle at 30% 22%, rgba(220,255,230,.75), rgba(60,160,100,.55) 35%, #0f3a22 80%)`, with a green glow and an inner shadow, like a glass stopper.
- **Guild avatar:** a 20–30px rounded square (radius 7–10px) with a 160° gradient between two guild colors, 2-letter initials (700, dark ink `#0b1a12`) and a top specular. Sample guild colors:
  - green `#9ff0c0`→`#3fae72`
  - violet `#d9c6ff`→`#8f6fd8`
  - parchment `#efe6cf`→`#b9a77a`
- **User avatar:** a 30px circle with a parchment gradient, initials, and a presence dot (9px, green, 2px dark ring).

---

## 3. Status vocabulary (must be represented exactly)

### 3.1 Run state chip

Pill (999px), 12px / 600, `padding:3px 11px 3px 9px`, 7px leading dot.

| State | Style | Dot | Lore suffix |
|---|---|---|---|
| `pending` | tint `--p2`, text `--mut` | static muted | — |
| `provisioning` | azure tint (`--azBg` / `--az` / `--azLn`) | pulsing | — |
| `running` | azure tint | pulsing azure + glow | "· brewing" (serif italic 14px, .85 opacity) |
| `waiting_on_human` | **solid amber** fill, ink `#1d1406`, 700, amber glow | pulsing ink dot | label "Waiting on you" |
| `in_review` | violet tint (`--vioBg` / `--vio` / `--vioLn`), label "In review" | static | — |
| `paused` | `--p2` tint, `--mut` text, ❚❚ glyph | static | — |
| `completed` | green tint | ✓ | — |
| `abandoned` | amber **tint** (not solid) | static | — |
| `failed` | rose tint, "✕ Failed" | — | "· batch spoiled" |
| `archived` | transparent, dashed `--ln2` border, `--fnt` text | — | — |

`in_review` is the run idling between phases with its merge requests open: reviewers have it, and reaction phases start when their triggers hold. It holds no concurrency slot.

Groups: **Active** = pending, provisioning, running, waiting_on_human, in_review, paused. **Finished** = completed, abandoned, failed, archived.

### 3.2 Workspace status (separate axis, never merged with the run chip)

Inline label: "Workspace" (`--mut`, 500), then a dot + value in the hue (600), then the mono ws id in `--fnt`.

| Value | Hue | Dot |
|---|---|---|
| live | grn | solid + glow |
| starting / provisioning / rebuilding | az | pulsing |
| destroying | amb | pulsing |
| error | ros | solid |
| stopped | mut | hollow |
| unknown (provisioner unreachable) | amb | dashed ring + tooltip "Provisioner unreachable — check integration token" |
| none | fnt | no dot, text "no workspace" |

The compact form is the dot only (8px), with a tooltip "Workspace: live · ws_19b4".

### 3.3 Stage types

Used as chips (`padding:1px 7px; radius 6px; 10.5px/600`; tinted bg + hue text), as flow-editor node accents, and as stage-strip flask colors when relevant:
- `agent`: az
- `command`: grn
- `human_gate`: amb (chip label "gate")
- `action`: vio
- `wait`: tea

### 3.4 Stage status (derived from events)

| Status | Flask fill | Pill style | Status line |
|---|---|---|---|
| done | 100% grn | `--p1` bg, solid `--cLn` border | "done · $0.21 · 3m" in `--mut` |
| running | ~55% az, slosh + bubble | `--azBg` bg, solid `--azLn` border, az glow | "attempt 2 · 4:12" (live clock) in az |
| waiting (gate open) | amb, pulsing | `--ambBg` bg | "waiting on you" |
| failed | ros | `--rosBg` bg | reason |
| paused | mut, 50% | — | "paused — will re-enter" |
| pending | empty | transparent bg, dashed `--ln2` border, .55 opacity | — |

Every attempt shows **"attempt N"**. Loop-backs render in the transcript and timeline as amber `↩ review → build (1/3)`.

### 3.5 Ping types

Type pill: 999px, 11px / 600, tint fill + hue text + hue border, 6px dot.

| Type | Hue | Classification |
|---|---|---|
| `gate` | grn | **wanted** (human_gate stage) |
| `debug` | amb | **wanted** (the flow's `debug` leaf: "Stage X failed — check why", the failure, the workspace link; continue re-runs the stage, abandon ends the run; label "Debug") |
| `clarification` | amb | unwanted (counts against run) |
| `blocker` | ros | unwanted |
| `permission` | vio | unwanted |
| `phase_confirm` | grn | **wanted** (a reaction phase asks before running; label "Phase") |
| `select` | tea | **wanted** (pick which review threads to address; label "Select") |

- Ping status: pending (solid amber count badge), answered ("answered 4m ago — approve" in `--mut`), withdrawn (struck-through, `--fnt`).
- A queued ping shows "· queued behind N".

### 3.6 Edges and handles (flow editor)

| Edge | Color | Style |
|---|---|---|
| success | grn | solid |
| on_fail | ros | dashed, animated |
| on_timeout | tea | dashed, animated |
| on_reject | amb | dashed, animated |
| fallback ("then") | same hue at 50% opacity | dashed |
| dataflow | `--ln2` | faint, dotted |

- Failure edges are labelled "×3" or "×∞" in a mono 10px pill.
- Handles are 8px dots in the edge color.

### 3.7 Tool calls

- **Kind chips:** read (az), edit (amb), search (vio), exec (grn), delete (ros), move (tea), fetch (az), think (mut), other (mut).
- **Status:** pending (hollow), in progress (pulsing az dot), completed (grn ✓), failed (ros ✕).

---

## 4. Components

All interactive controls use `all:unset; cursor:pointer; white-space:nowrap` as a base and are laid out with flex + `gap`.

### 4.1 Buttons

There is **one selected / secondary look everywhere**: sidebar items, rail items, tabs, segmented controls, popover choices, the theme toggle and the layout button all share it. Don't introduce grey or translucent variants.

| Variant | Spec |
|---|---|
| **Primary** | bg `--brand`, color `--onBrand`, 600, radius 12px, `padding:4px 11px`, glow `0 0 14px var(--brandLn)`. One per group, max. (MR link, Retry, "Open Windows") |
| **Secondary** | bg `--p2`, 1px `--ln2`, `box-shadow: var(--spec)`, 500. Hover bg `--p3` |
| **Selected / chrome button** | bg `--railSel` (`#0f271b` Onyx; light `#24553a`), 1px `rgba(140,230,175,.26)`, `box-shadow: inset 0 1px 0 rgba(255,255,255,.07), 0 1px 3px rgba(0,0,0,.35)`, text `#e6efe8` / `#fff`, 600 |
| **Danger outline** | transparent, rose text, 1px `--rosLn`; hover `--rosBg`. Destructive actions end in "…" and open a confirm |
| **Danger solid** | bg `--ros`, ink `#1b0a0a`, 600 (only inside confirm dialogs and alert banners) |
| **Ghost** | transparent, `--mut`; hover `--p3` + `--tx` |
| **Icon button** | 24×24 (window chrome) or 28×28 (sidebar), radius 7–9px, `--mut`; hover `--p3` |
| **Disabled / not yet** | dashed 1px `--ln2`, `--fnt` text, e.g. "MR 2/2 —" |

Size: 12px text in headers; 11–11.5px compact in the sidebar; 12.5px in menus.

### 4.2 Badges and counts

- **Count in nav or tab:** Geist Mono 10–10.5px / 600, `padding:0 7px`, radius 13px. Neutral is transparent with `--mut`. Hot is solid amber with ink `#1d1406`.
- **Rail badge:** absolute top-right, 9–9.5px, `line-height:14px`, solid amber.
- **"+N elsewhere":** amber tint, 1px amber@35% border, mono 9.5px / 700 (on the guild switcher when other guilds have pending pings).
- **Mono tag:** ticket keys and hashes, 11px, `padding:0 6px`, radius 5px, `--p2` bg + `--ln` border.

### 4.3 Tabs (window tab strip)

- Strip height 38px with a bottom border `--cLn`. Tabs sit in a flex row, gap 2px, `overflow:hidden`.
- **Tab:** `padding:3px 4px 3px 10px`, radius 9px, 12.5px, `cursor:grab`.
  - Inactive: `--mut`, 500, transparent.
  - Active: bead style (`--sel` + inset ring `--selLn` + `--selSh`), `--tx`, 600.
  - Each tab carries an optional count and a 17px "×" (hide window).
- **Right side of the strip:**
  - a hint text (11px `--fnt`, ellipsis), e.g. "following", "refreshes every 5s", "plan-build-review @3c9e1a"
  - then window controls: split right, split down, maximise/restore (⤢ / ⤡), hide (×)

### 4.4 Segmented control

Used for filters and for repo, verdict and log-source pickers.

- The container is control glass, `padding:3px`, radius 12px, gap 3px.
- Options are 12.5px, `padding:5px 12px`, radius 9px. The selected option uses the bead/selected style.
- Options can carry a trailing mono count in `--mut`.

### 4.5 Header control group ("glass group")

A labelled cluster on control glass: radius 17px, `padding:8px 10px`, column with gap 7px. Its parts:
- **Label row:** 11px / 500 `--mut` (e.g. "Run", "Workspace" + status, "Merge requests").
- **Button row:** gap 5px.

The run header has three groups, so **run lifecycle** and **workspace lifecycle** are visibly separate:
1. **Run**
   - active: Pause, Stop…
   - terminal: Retry \<stage\> (primary), New run from this…, Archive…
2. **Workspace:** status, Env ▾, Editor ↗, Rebuild ▾ (menu: Rebuild, Rebuild without cache)
3. **Merge requests:** "MR 1/2 ↗" as primary; not-yet MRs are dashed

Naming fixes the old ambiguity. Use these names in the UI:
- **Retry \<stage\>** (same run, same workspace)
- **New run from this…** (new run; asks for direction, files and flow)
- **Rebuild workspace** (machine only; the run is untouched)

### 4.6 Run badge ("batch label")

- A 96×78 control-glass tile, radius 17px:
  - top row: "Batch" / "Run" with a mono "Nº"
  - middle: the mono short id at 21px / 600
  - bottom: "brewed 13:21"
- A radial glow in `--stateGlow` sits in the bottom-right corner.
- It sits left of the title.

### 4.7 Alert banner (in-flow)

- Control glass over a hue tint: bg `--rosBg`, border `--rosLn`, radius 12px, `padding:5px 5px 5px 7px`, margin 0 16px.
- Contents, left to right:
  - an 18px round "!" badge (solid hue, glow)
  - a bold hue title ("Egress blocked ×3")
  - an optional serif lore aside
  - a mono list with ellipsis and "+N more"
  - a spacer
  - a solid danger CTA ("Review & allow", which focuses or opens the Egress window)
  - a ghost "Dismiss"
- Amber variant: workspace unknown, provisioner unreachable.

### 4.8 Popover / menu

- Control glass + `--popBg`, radius 17px, `padding:6–10px`.
- Shadow: `--lgRim, 0 24px 60px rgba(0,0,0,.45)`.
- Title in Instrument Serif 19px, plus an optional right-aligned 11px legend.
- Rows: radius 10–14px, `padding:7px 8–10px`, hover `--p3`. The selected row gets the selected style.
- Dividers: 1px `--ln`, margin 4px 6px.
- Popovers are **positioned beside their trigger, never on top of it**. The org switcher opens to the right of the sidebar (`left: sidebarWidth + 16px`, `top: 56px`).
- Close on outside mousedown or Esc.

### 4.9 Modal / confirm

- Scrim (above), then a dialog of 440px, radius 26px, control glass + `--thick`, shadow `0 30px 80px rgba(0,0,0,.5)`.
- Header: a 34px round hue icon (danger: `--rosBg` / `--ros`) and a title.
- Body text is `--mut`, `text-wrap: pretty`, and states consequences per object:
  - *"The current turn is cancelled and the run becomes **abandoned**. The workspace stays up so you can inspect it; it's reaped after 48h."*
- Footer: ghost Cancel, then the destructive solid button.
- **Required confirms:** Stop (Abort), Archive, Delete ticket, Delete project, Delete integration, Remove channel, Delete review comment, Clear build cache, Leave editor with unsaved changes.
- No native `confirm()`.

### 4.10 Content cards (inside panes)

- **Card-in-pane:** radius 13–14px, `--p2` bg, 1px `--ln`.
- **Collapsible row** (`<details>`): summary line with `padding:7px 12px`, 12px text, a bold ▸ label, and a muted ellipsised preview.
- **Tool group** (folds when >3 calls): "▸ 12 tool calls", kind chips (tinted 999px pills, 11px, "read 4"), and right-aligned "1 failed" in rose.
  - Live groups never fold.
- **Tool call row:** status glyph, kind label in its hue (600), mono path, and mono `+14 −6`. An expanded call shows the diff.
- **Diff:** Geist Mono 11px on `--code`, radius 8px.
  - Grid columns `36px 1fr`, with the line number right-aligned at .6 opacity.
  - Added lines: `--grnBg` / `--grn`. Removed lines: `--rosBg` / `--ros`.
  - A "truncated · showing 400 of N lines" footer.
  - A binary-file state: centred `--mut` text "Binary file · 24 KB".
- **Plan checklist:**
  - Title in serif 18px, plus "v4 · 2 of 5".
  - Items are 15px circles. Done: green fill + ✓, text struck through and muted. In progress: az ring + pulsing dot, text az / 600. Todo: `--ln2` ring.
- **Stage divider (transcript):** a hairline gradient, then the serif italic stage name (19px), then "attempt 2 · builder · gpt-5-codex · 14:02:11", then a hairline gradient.
- **Status line:** 12px in the hue with a glyph (↩ loop, ✓ done, ✕ failed, ⏸ parked, ↻ resumed, ⟳ engine restarted).
- **User message bubble:**
  - right-aligned, max-width 70%
  - `--brandBg` + `--brandLn`, radius `10px 14px 4px 14px`
  - meta below: "you · delivered 14:05", "queued — read when its turn ends", or "interrupt" (amber)
- **Agent message:** max-width 70ch, 13.5px / 1.6, markdown. While streaming it ends with the blinking caret.
- **Thinking:** a `<details>` (open while live) with an az gradient tint `linear-gradient(90deg, var(--azBg), transparent 70%)`, a pulsing "Thinking ▾" summary, and the serif italic body.
- **Inline agent question (non-gate ping):** a ping card (4.11) embedded in the transcript. Until answered it carries a "waiting for your answer" line.

### 4.11 Ping card (inbox, run page, inline)

- Sits above the composer when the run is waiting. Radius 20px, control glass with an amber wash:
  - `radial-gradient(ellipse 70% 140% at 0% 0%, var(--ambBg), transparent 70%), var(--lgBg)`
  - border `--ambLn`, glow `0 0 44px var(--ambGlow)`
- **Head:** a 22px solid amber badge, the type pill, the stage, "raised 2m ago", and the run + ticket title (inbox cards always show which run and ticket they belong to).
- **Body:** markdown question + context, and either an MR link or "Review & comment →" (opens plan review).
- **Actions:**
  - gate: Approve (primary) / Request changes… (feedback required)
  - options ping: one secondary button per option, plus "Other…"
  - free-text ping: textarea + Send
- Every answer can carry attachments (4.15).
- **Answered state:** collapsed to a single muted line.

### 4.12 Composer

- Control glass, radius 20px, margin 0 12px 12px.
- Textarea area: `min-height:44px`, placeholder in `--fnt`.
- Footer row: key hints ("↵ send · ⌘↵ send now (interrupts) · ⇧↵ newline", 11px `--fnt`), an attach button, then **Send** (secondary) and **Send now** (primary).
- Shown only while the run is active.
- Sending snaps the transcript back to auto-follow. If the user has scrolled up, show a floating "↓ Follow" pill.

### 4.13 Tables and lists

- Rows: `padding:8–10px 14–16px`, 1px `--cLn` top border, hover `--p1`.
- Header row: 11px / 500 `--mut`, sticky.
- Numbers and ids are in mono and right-aligned. Times are relative, with an absolute 24h tooltip.
- Prefer a CSS grid per row (e.g. `118px minmax(0,1fr) 210px 64px auto`) so long text ellipsises instead of wrapping.
- **File list:** a status letter (A grn, M amb, D ros, R vio, C tea, U `--mut`) in a 16px mono column, the path, `+n −n` or "bin", and a ● dot for uncommitted files.
- **Selected row:** `--p3`.

### 4.14 Forms

- **Field:**
  - label 12px / 500 `--tx`
  - optional help text 11.5px `--mut`
  - input: `padding:7px 10px`, radius 10px, bg `--p1`, 1px `--ln2`
  - focus: 1px `--brand` + `0 0 0 3px var(--brandBg)`
  - error: border `--ros`, message 11.5px `--ros`
- **Secret input:** shows "•••• set" plus a "Replace" ghost button. Leaving it empty keeps the current value.
- **Domain / tag list input:** chips (999px, `--p2`, × to remove). Paste splits on whitespace and commas; wildcards are allowed.
- **Select / combobox:** the same shell + ▾. Flow pickers list "Default · \<name\>" first, then project flows, then a "Built-in templates" group. It falls back to free text if loading fails.
- **Toggle:** a 28×16 track. Off: `--p2`. On: `--grn` with a white 12px knob.
- **Section:** an Instrument Serif 20px title + a 12px `--mut` sub. Fields sit on a 2-column grid (`minmax(0,1fr)`) with gap 12×16.
- **"How to set this up" help:** a collapsible `<details>` card in `--p2`.

### 4.15 Attachments

- A drop zone (dashed `--ln2`, radius 12px, "Drop files or browse · max 20 MB · images, pdf, md").
- Below it, chips: a 28px thumbnail or file glyph, the name, size in mono `--mut`, and ×. Over-limit chips turn rose with a reason.
- Read-only file chips have the same look without the ×, and link to the stored file.

### 4.16 Sparklines (Vitals / Resources)

- Three tiles in a 3-column grid, gap 6px. Each tile: `--p2`, radius 13px, `padding:7px 10px 6px`.
- Tile content:
  - label 11px `--mut`
  - value in mono 13px / 600
  - an SVG of 120×26 (viewBox) with a 1.5px polyline in the hue and an area at 15% hue
- Hues: CPU az, memory vio, network tea (in = solid, out = dashed).
- A shared hover crosshair across all three tiles shows a value and timestamp tooltip.
- **"Collecting samples…"** state: a dashed baseline and an az shimmer dot.

### 4.17 Empty, loading and error states

- **Empty:** a dashed 1.5px `--ln2` box, a serif italic title of 22–26px ("An empty bench", "Nothing running", "All still on the shelf"), a 1-line `--mut` explanation (max 40ch), and one primary action.
- **Loading:**
  - Skeleton bars: `--p1` with a 1.4s shimmer (`linear-gradient(90deg, transparent, var(--p2), transparent)`).
  - For live panes, prefer "Connecting…" plus a pulsing az dot.
  - Never use a blank page.
- **Error:**
  - Inline rose card with a short cause, a mono detail in a `<details>`, and a "Retry" secondary.
  - Integration test results persist: "✓ ok · tested 3h ago" / "✕ 401 Unauthorized · tested just now".
- **Stale / unknown:** an amber dashed ring with a tooltip that explains (e.g. provisioner token expired).
- **Live indicators:** anything polled shows "updated 2s ago" or a pulsing dot where it matters. Polling pauses when the tab is hidden; show "paused" on return until the first refresh lands.

---

## 5. Global layout (app shell)

```
┌───────────────────────────────────────────────────────────────────┐ scene + sigils canvas (z0)
│┌──────────┐┌─────────────────────────────────────────────────────┐│
││ SIDEBAR  ││ MAIN (page)                                         ││
││ 232px    ││  page header                                        ││
││ (60 rail)││  optional alert banner                              ││
││          ││  page body / window desk                            ││
│└──────────┘└─────────────────────────────────────────────────────┘│
└───────────────────────────────────────────────────────────────────┘ cursor-light overlay (z40)
```

- **Root:** full viewport, `display:flex; flex-direction:column; overflow:hidden; color: var(--tx); background: var(--scene)`.
  - The canvas is absolutely positioned at z0. Content is at z1. Popovers are at z30 and modals at z20+ (scrim covers everything).
  - The design reference is 1440×900, but the layout is fluid.
- **Row:** `flex:1; display:flex; min-height:0`.
- **Sidebar:** `flex:none; width:232px; margin:6px 0 6px 6px; radius 16px; overflow:hidden`, control glass with `--railBg`. Column layout:
  1. **Brand row** (56px): logo, then "Apothecary" (15px / 600) with the serif italic tagline "software, bottled" below, then a « collapse button (28px).
  2. **Guild switcher:** a selected-style button with margin 0 10px 8px, radius 12px:
     - 26px guild avatar, guild name (13px / 600, ellipsis), sub "4 workshops · 14 members" (11px `--mut`, ellipsis)
     - an optional "+N" elsewhere badge, then ⇅
     - opens the Guilds popover
  3. **Search / command bar:** "Search, jump, do…" plus a ⌘K mono key cap. It opens a command palette: jump to run, ticket, project, guild or action.
  4. **Nav**, scoped to the current guild: Desk ◈, Inbox ✉ (amber count), Runs ▶ (count), Tickets ☰, System ⚙. Item: `padding:6px 10px`, radius 10px, 16px glyph column, label, right count. Active item uses the selected style.
  5. **Workshops section:** a header in serif italic 14px `--mut`, plus a legend (▶ live · ✉ pings) and "+". A scrollable list:
     - Each row shows the vial, name, live count (mono az), and ping count (solid amber pill, or empty).
     - The **current workshop expands** to show its live runs as an indented tree: 15px indent with a 1px left rule. Each run row has a state dot (az running / amb waiting), the title (ellipsis) and the mono id. The current run is highlighted with `rgba(255,255,255,.06)`.
     - Quiet workshops are dimmer (`--mut`).
  6. **Capacity:** a mini card in a 2-column grid, "Runs 3/6" and "Envs 4/10", each with a 5px bar (parchment and green gradients).
  7. **User row:** a top hairline, then the avatar with presence, name (12.5px / 600), role line "Maintainer · Northwind Labs", and the **theme toggle button** (Dark / Light). This is the only appearance control end users get.
- **Collapsed rail:** 60px wide with the same margins and material; a centered column with gap 4px:
  - logo, then a » expand button
  - guild avatar button (with an amber dot when there are pings elsewhere)
  - ⌕ search
  - nav icons (40×36, with amber count badges)
  - a 24px divider
  - workshop vials (40×34, with amber ping badges)
  - a spacer, then the theme icon (☾ / ☀) and the user avatar
  - Collapse state persists (localStorage). Shortcut ⌘\\.
  - **Mobile:** the same sidebar becomes an off-canvas drawer, and the rail becomes a bottom tab bar (Desk, Inbox, Runs, Tickets, More).
- **Main:** `flex:1; min-width:0; display:flex; flex-direction:column`. It is transparent; the scene shows through between panes.

### 5.1 Guild switcher popover

- 312px wide, positioned **to the right of the sidebar** (left 248px, or 72px when collapsed), top 56px.
- Contents:
  - Title "Guilds" (serif 19px) + a legend "▶ live ✉ pings".
  - Row **"All guilds"**: a dashed ◈ tile, "Your desk across every guild", the total live count (az mono) and the total pings (amber pill).
  - A divider, then one row per guild: avatar, name, "role · N workshops", live count, ping pill. The current guild uses the selected style.
  - A divider, then a footer with two ghost buttons: "+ Found a guild" and "Join with invite".
- Picking a guild re-scopes the sidebar nav, workshops list, Desk, Inbox, Runs, Tickets and System to that guild.

### 5.2 Routing

| Route | Page |
|---|---|
| `/` | All-guilds desk (default landing for multi-guild users; single-guild users land on their guild desk) |
| `/g/:guild` | Guild desk |
| `/g/:guild/inbox` · `/g/:guild/inbox/:pingId` | Inbox · plan/document review |
| `/g/:guild/runs` · `/g/:guild/runs/:runId` | Runs list · **live run** |
| `/g/:guild/tickets` · `/new` · `/:ticketId` | Tickets |
| `/g/:guild/w/:workshop` | Workshop overview (runs, tickets, flows, agents, health) |
| `/g/:guild/w/:workshop/flows/:name` | Flow editor |
| `/g/:guild/w/:workshop/settings` | Workshop setup (repo, commands, execution, workspace and egress) |
| `/g/:guild/system/{integrations,channels,limits,storage,members}` | System hub |
| `/inbox` | Cross-guild inbox (from the "All guilds" desk) |

Breadcrumbs always follow the hierarchy: **Guild / Workshop / Ticket key / Run id**. Each segment is a link (12px `--mut`, "/" in `--fnt`, current segment in `--tx` mono), and the row is `nowrap` with overflow hidden.

---

## 6. Window system (live run desk)

The run page body is a **user-arrangeable tiling desk**, like an IDE.

- **Views** (13): Flow, Session, Pings, Timeline, Record, Changes, Files, Terminal, Logs, Egress, Ports, Resources, Vitals. Each view lives in exactly one leaf at a time, or is hidden.
- **Model:** a binary-ish split tree.
  ```
  leaf  = { k:'leaf', id, tabs:[view…], act:view }
  split = { k:'split', dir:'row'|'col', s:[fractions summing to 1], c:[children] }
  ```
  Normalise after every change:
  - drop empty leaves
  - collapse single-child splits
  - flatten same-direction nested splits, multiplying their fractions
- **Persistence:** save `{tree, preset}` per user, in localStorage now and on the server later.
- **Geometry:** gap 8px between panes. Leaf header 38px. Leaves are content glass with radius 16px. Focused leaf border is `--brandLn`; others `--cLn`.
- **Interactions:**
  - **Drag a tab** to move it. A floating "ghost" pill follows the cursor (selected style + big shadow). Drop targets:
    - the center of a leaf stacks the tab there
    - the outer 26% of a leaf's left, right, top or bottom edge splits that side
    - drop preview: an absolute box with `--brandBg` fill, 1.5px dashed `--brand`, glow, and a serif italic label ("Stack here", "Split right"…)
  - **Gutter (8px)** between panes: cursor `col-resize` / `row-resize`. A centered 2px grip line appears on hover with a `--brandBg` highlight. Drag to resize, with a minimum pane size of about 120px.
  - **Leaf buttons:** split right / split down (move the active tab into a new half), maximise (fills the desk; others hidden until restore), hide.
  - **Tab ×** hides that view.
  - **Layouts popover** (from the "Split ▾" button at the right end of the breadcrumb row). Positioned top-right under the header, 300px wide:
    - "Layouts" title + "saved in this browser"
    - preset grid (3 columns): Split, Focus, Dock, Recipe, Bench, Custom
    - "Windows" checklist (2 columns): a 14px checkbox tile per view (green ✓ when shown) + label; clicking toggles it
    - help line: "Drag a tab onto another window to stack it, or onto an edge to split. Drag the gaps to resize."
  - **All hidden:** the "An empty bench" empty state with an "Open Windows ▾" primary button.
- **Presets** (fractions):
  - **Split** (default):
    - col [.16 Flow, .84 →]
    - → row [.65 main tabs (Session, Pings, Timeline, Changes, Files, Terminal, Logs, Record), .35 →]
    - → col [.3 Vitals, .7 (Egress, Ports, Resources)]
  - **Focus:** col [.16 Flow, .84 one leaf containing every other view].
  - **Dock:**
    - col [.16 Flow, .52 row(.6 Session/Pings/Timeline/Record, .4 Changes/Files), .32 row(.62 Terminal/Logs/Egress/Ports, .38 Vitals/Resources)]
  - **Recipe:**
    - row [.19 Flow (vertical), .45 Session/Pings/Timeline/Record/Logs, .36 col(.68 Changes/Files/Terminal/Egress/Ports, .32 Vitals/Resources)]
  - **Bench:**
    - row [.34 Session/Pings, .66 col(.17 Flow, .22 Vitals, .61 everything else)]
- **The Flow view adapts:** a **horizontal stage strip** when the leaf is wide and short, and a **vertical recipe list** when it is tall and narrow (switch on aspect ratio).
  - Horizontal: stage pills (flask + mono id + status line) joined by 10px connectors, ending in a dashed serif "done" pill.
  - Vertical: a flask column joined by vertical connectors, a card per stage, a serif title "The formula", and the mono flow @hash.

**Per-view content:**

| View | Contents |
|---|---|
| Session | Transcript (4.10) + ping card + composer. Auto-follows the bottom. |
| Pings | All pings raised by this run (ping cards); summary "1 pending · 2 answered". |
| Timeline | Event rows: 24h time (mono), a tone dot (ok grn / bad ros / warn amb / info az), text, and inline "log" / "open" actions. |
| Record | Snapshots and attachments table: name, kind chip, source ("build · attempt 2", "ping answer", "run goal"), size, time, open/download. Kind filter segmented control. Note "Kept after the workspace is destroyed". |
| Changes | Repo segmented picker (primary ★). Summary "branch vs main · fork a1b2c3 · 7 files +212 −48 · 3 commits", collapsible commits, file list (4.13) + unified diff. "Refreshes every 5s". |
| Files | Repo picker, lazy tree (show ignored toggle, refresh), file viewer with line numbers, a 1 MB cap notice and a binary state. |
| Terminal | Repo picker, connection status pill (connecting az pulse / connected grn / disconnected ros + Reconnect), xterm surface on `--code`. "Session ends when you leave". |
| Logs | Source segmented control (Workspace / Provisioning / stage logs). Mono 11.5px log tail with colored sources (tunnel tea, firewall ros, harness az). |
| Egress | Verdict filter (All 41 / Allowed / Denied 3) + host search. Table: host:port (mono), seen verdict chip, **rule now** toggle (allow/block, applies live, saved to the workshop policy), protocol, count, bytes out/in, last seen. Footer links to the workshop egress settings. |
| Ports | Detected ports as buttons (`:4000 beam.smp`), clicking exposes and opens. Manual "Expose port" input (1–65535, Enter submits). |
| Resources | Process count, time window, and per-process breakdown. *The engine samples only sandbox totals today (`ResourceSample.pids`), so the breakdown waits on a per-process sampling API; until then the view shows totals.* |
| Vitals | The three sparklines with a shared crosshair (4.16). |

---

## 7. Page layouts

Every page uses the shell (section 5). The page header pattern is: breadcrumb row, then an h1 with one serif accent phrase and an optional status chip, then a meta row (12px `--mut`, flex-wrap, gap 4px 14px), then right-aligned glass control groups. Header padding is 16–22px 20–22px 12px.

### 7.1 Live run: `/g/:guild/runs/:id` (reference: v10, default view)

1. **Header grid:** `auto | minmax(0,1fr) | auto`, gap 16px.
   - **Left:** the run badge (4.6).
   - **Center:**
     - breadcrumb, plus the "Split ▾" layout button at the far right
     - h1 "Add idempotency keys to *payment webhooks*" + run state chip
     - meta row: ticket key tag + "ticket ↗"; flow name + mono @hash; mono branch "← main"; cost · turns
   - **Right:** three glass groups (Run / Workspace / Merge requests, 4.5).
   - The Env ▾ popover lists environment links. URL links open in a new tab ↗. Command-only links are copy buttons that flip to "copied ✓" for 1.5s.
2. **Alert banner** (egress blocked or workspace unknown), only when relevant.
3. **Desk** (section 6) fills the rest.
- **Paused:** the Flow view's current stage shows "paused — will re-enter build". The Run group shows ▶ Resume (primary) + Stop….
- **Waiting:** the chip turns solid amber, the ping card docks above the composer, and the sidebar Inbox count increments.
- **Terminal states:** the composer is hidden and the Session shows a closing status line. Run group: Retry \<stage\> / New run from this… / Archive…. When the workspace is destroyed, workspace-only views (Changes, Files, Terminal, Ports, Egress live) show an empty state: "Workspace destroyed 2h ago — the Record keeps every snapshot". The Record stays.

### 7.2 All-guilds desk: `/` (reference: v10, "All guilds" in the switcher)

A single scrolling column, `padding:22px`, gap 18px.
1. **Header row:**
   - Left: caption "All guilds · 3 guilds · 7 workshops", then an h1 "Good afternoon, Maya. *4 things need you, 6 brews on the fire.*"
   - Right: a segmented filter: Everything (n) / Needs me (n) / Running (n).
2. **Needs you panel** (content glass, radius 18px):
   - head: an amber count badge, a serif title "Awaiting your hand", "oldest first, across every guild", and "Open inbox →"
   - rows (grid `118px | 1fr | 210px | 64px | auto`):
     - type pill
     - question (500, ellipsis), with "run id · run title · stage" below (and "· queued behind N" when relevant)
     - guild avatar + guild / workshop
     - age (mono)
     - CTA in the selected style: Review plan (gate) / Answer / Decide (permission)
3. **One section per guild:**
   - Section header: avatar 30px, name (15px / 600), "role · N members", a "N live" az pill, a "N need you" solid amber pill, a hairline spacer, "today · 3 completed · 1 failed", and "Open →".
   - Then a card grid `repeat(auto-fill, minmax(330px,1fr))`, gap 10px:
     - **Workshop card** (content glass, radius 16px): head with vial + name + "2 live · 1 ping". Run rows (grid `8px | 1fr | auto | auto`): state dot, title over "state · stage" (state word in its hue), stage-type chip, mono elapsed over cost. A click opens the run.
     - **Quiet card:** a dashed border with a serif italic "2 quiet workshops" and the names "infra-tf · docs-site — nothing running". If the whole guild is idle: "All still on the shelf".
- The filter applies to the run rows, and a card hides when all its runs are filtered out.

### 7.3 Guild desk: `/g/:guild` (not mocked; follow 7.2)

The same anatomy, scoped to one guild:
1. Header: "Northwind Labs" with an accent phrase, plus the filter.
2. Needs-you panel (this guild only).
3. The workshop card grid, the same cards as 7.2 at larger size. Add the last-finished runs (completed / failed with MR link) under the live ones, and a ticket count with "+ New ticket".
4. A "Recently finished" table (Finished-group runs, 24h): title + id, state chip, workspace dot, workshop, flow, MR link, updated.
5. The capacity strip (concurrent runs, environments, queue length when pending FIFO).

### 7.4 Runs list: `/g/:guild/runs`

1. Header with a segmented control Active (n) / Finished (n), a workshop filter (select, mirrors `?project=`), and search.
2. Table columns: title + mono id, state chip, workspace dot, current stage (flask + id + attempt), workshop, flow, MR, updated.
3. Polls every 4s with a "updated 2s ago" caption.
4. There is no "New run" button: runs start from tickets. The empty state links to Tickets.

### 7.5 Inbox and plan review: `/g/:guild/inbox`, `/:pingId`

- **Inbox:** a two-pane layout.
  - **Left list** (380px, content glass):
    - Pending / Answered segmented control, and a type filter chip row.
    - One card per run, showing its newest pending ping: type pill, stage, age, and **run title + ticket key + workshop**.
    - "Answer this first — N earlier pings queued" in amber.
  - **Right:** the selected ping card (4.11) at full size, plus a run summary strip (state, stage flask, cost, "Open run →").
  - Answered tab: a flat history table.
  - The cross-guild `/inbox` uses the same layout, with a guild column and a guild filter.
- **Plan review (document workbench):** a three-column layout.
  - **Left:** a document outline (headings) that jumps to sections.
  - **Center:** the document, max-width 72ch, markdown blocks rendered from the **raise-time snapshot**. A caption reads "snapshot from 14:02 · not the live file".
    - Selecting text opens a floating composer (control glass, radius 17px): section heading, "L12–18", the quoted text, a comment box, attachments, and "⌘↵ add · Esc close".
    - Commented blocks get a 3px `--brand` left bar. Hovering a comment highlights its exact range in `--brandBg`.
    - Extra agent files appear as a thumbnail row, with "not captured" in a dashed chip.
  - **Right sidebar:** review (320px): comments sorted by position (jump, edit, delete with confirm), review-level attachments, and a footer with the actions:
    - gate: **Approve** (warns when unsent comments exist) / **Request changes (N)**
    - options ping: option buttons; comments ride along as the note
    - other pings: **Send review (N)**
    - A "Draft saved" caption (drafts persist locally; attachments must be re-picked, and the UI says so).
  - **Answered:** read-only, with the answer banner and the document "as reviewed".

### 7.6 Tickets: `/g/:guild/tickets`, `/new`, `/:id`

- **List:** search (title, key, goal) and filters (workshop, source, has-active-run).
  - Columns: title, source key tag, workshop, base branch (mono), **derived status** (latest run state chip, or "no runs"), created.
- **New ticket:** a two-column form:
  - **Left:** Workshop (select, locked after create), Title, Goal (large textarea, markdown), Base branch (auto-slugged, mono, with "edited" when overridden), Attachments.
  - **Right card "Import from tracker":** integration select, then search (key / words / `jql:`), then a result list (key, title, type, status, assignee). Picking one prefills the form and lists the attachments to copy, with "Open in tracker ↗" and "Clear". Skipped attachments show a warning list + Continue.
- **Ticket detail:**
  - Header: title + source tag + workshop link + branch; Edit / Delete… on the right.
  - **Left:** the goal and attachments ("delivered into every run's workspace").
  - **Right:** a "Start a run" card: direction (textarea), run-only files, flow picker, source-branch picker (default marked ★), and a primary "Start run".
  - Below: the runs table for this ticket (polls every 4s).

### 7.7 Workshop overview: `/g/:guild/w/:workshop`

1. Header: vial + name + repo URL (mono) + repository / provisioner / harness chips; "Edit setup" secondary, Delete… (blocked until all runs are archived, with an explanation).
2. A tab strip: **Overview · Runs · Tickets · Flows · Agents · Health**.
   - **Overview:** the live runs cards (as 7.2) and a read-only summary (repositories incl. additional repos, commands, default flow, egress mode + lists).
   - **Flows:** a list with "default" and "from template" chips, hash and updated; New flow, Copy, "Use built-in template". Built-in templates in their own group.
   - **Agents:** the agent library (name, harness / model, read_only, allow_bash, inputs / outputs incl. verdict). It is editable here, which fixes the old YAML-only gap.
   - **Health:**
     - Preflight: a step list with ok / bad and errors.
     - Envcheck: a live step timeline (image, firewall, egress, harness, shim, tunnel → ok / bad / skip), Cancel / Restart, "keep workspace" toggle; resumes the latest check on load.
     - Build cache: size + "Clear…" (confirm), and image-cache status.

### 7.8 Flow editor: `/g/:guild/w/:workshop/flows/:name`

1. **Header:**
   - title (flow name, mono @hash) + a "Built-in" banner when applicable ("Saving creates an editable copy")
   - Graph | YAML segmented control
   - Validate, which shows a result popover: stage count, hash, errors (ros), binding warnings (amb)
   - Save (primary, disabled until dirty, with an "Unsaved changes" amber dot)
   - Clone, Delete… or "Reset to built-in"
2. **Body:** row [canvas 1fr | inspector 340px].
   - **Canvas:** Svelte Flow on a transparent background over the scene, with a faint dot grid (`--ln` 1px, 20px).
     - **Nodes:** content glass, radius 14px, with a 3px top accent in the stage-type hue.
       - Head: type chip + mono id, plus an ENTRY tag (parchment outline) on the first stage, plus a detail line (agent name / command / action / signal / "gate").
       - Rows: inputs (bound grn dot, unbound amb hollow, sentinel vio `$DIFF_TARGET`) and outputs with data dots.
     - Start / Done / Abort are serif italic pills (Done dashed grn, Abort dashed ros).
     - Handles and edges as in 3.6.
     - Bottom-left controls: zoom, fit, lock, auto-arrange. Node positions autosave silently.
   - **Inspector** (control glass):
     - With nothing selected: flow description, defaults (harness, model, timeout, max turns), budget (max USD, max total turns), and "+ agent / + command / + gate / + action / + wait".
     - Stage selected: id (rename updates every reference), type, description, success target, add on_fail / on_timeout / on_reject, plus per-type fields:
       - agent: name autocomplete, overrides, input bindings, output paths, verdict note
       - command: run_ref or literal script, reads, writes, **each_repo** toggle
       - gate: question, shows (artifact / `$WORKSPACE` / `$MR_URL`), auto-approve / auto-reject signals
       - action: registry picker, description, required integration, inputs / outputs
       - wait: signal picker, timeout (empty = ∞)
     - Edge selected: a chain of links (target + max ×N / ∞), "then" fallbacks, remove last / remove edge.
     - Delete selected node or edge: inspector button **or** ⌫ / Del key.
   - **Versions:** a drawer from the header (list, current marked, "Load into editor").
   - Leaving with unsaved changes triggers a confirm modal.

### 7.9 Workshop setup: `/g/:guild/w/:workshop/settings` (create and edit)

A stepped form with a left step-rail and section anchors: **1 Repository → 2 Workshop → 3 Execution → 4 Workspace**. Sticky footer: Cancel / Save (primary). A "Run envcheck after saving" toggle.

1. **Repository:**
   - repository account select
   - "Browse repositories" (searchable list; each row has "Use" and "+ extra")
   - URL, locator, default branch, AGENTS.md path
   - additional repos as an editable table (name, URL, locator: empty = read-only, branch), with the note "cloned to ~/repos/\<name\>, one MR each"
2. **Workshop:** name; commands as a key → script table (`setup` pinned first, the others referenced by `run_ref`); default flow.
3. **Execution:** provisioner select; harness integrations as a multi-select (one per harness kind).
4. **Workspace:**
   - Egress mode segmented control **Allowlist / Denylist**, with an explanation under each. The two lists are kept separately.
   - Domain chip input; "+ Add sensible defaults" (allowlist); a note that force-blocked hosts are managed from the run's Egress window.
   - code-server extensions (chip input).
   - Editor defaults: theme select (15), font size, tab size, format on save, word wrap, trust workspace.

### 7.10 System hub: `/g/:guild/system/*`

Layout: row [section nav 220px | content]. The section nav is a vertical list in the sidebar nav style:
**Integrations · Channels · Members & roles · Limits · Reaper · Storage**.

- **Integrations:** four groups (Repositories, Provisioner, Harness, Ticket source), each with "+ Add \<kind\>" buttons for its kinds.
  - **Integration card** (content glass): name, kind chip, config summary (secrets masked), **persisted test result** chip (✓ ok / ✕ error + "tested 3h ago"), Test (secondary), Edit (inline expand), Delete….
  - Forms are generated from descriptors: a "How to set this up" help panel, then fields (text / secret / JSON secret).
  - A skeleton list shows while loading.
- **Channels** (folded in from the old page): Matrix and Slack, as one-way mirrors ("Pings are always answered in Apothecary"). Each card has Test, Edit and Remove… (confirm).
- **Members & roles:** a table of avatar, name, email, role select (Owner / Maintainer / Engineer / Viewer) and last active; an Invite button. (This is new for multi-tenant.)
- **Limits:** max concurrent runs (stepper; "beyond this, runs queue as pending, FIFO") and max environments ("at the cap, new runs are refused").
- **Reaper:** destroy completed environments after a duration; destroy abandoned environments (`never` only today, shown disabled with a reason).
- **Storage:** sweep unclaimed uploads after a duration; expire snapshot content after a duration or never.
- **Duration inputs:** a number + unit select (m / h / d / never) replaces free text. Show "Applies immediately — no restart".

---

## 8. Interaction and keyboard

| Context | Keys |
|---|---|
| Global | ⌘K command palette · ⌘\\ collapse sidebar · G then I / R / T / D to go to Inbox / Runs / Tickets / Desk |
| Composer | ↵ send (queued until turn ends) · ⌘↵ send now (interrupts) · ⇧↵ newline |
| Review composer | ⌘↵ add comment · Esc close |
| Flow editor | ⌫ / Del delete selection · ⌘S save · ⌘Z / ⇧⌘Z undo / redo |
| Search and port inputs | ↵ submits |
| Popovers and modals | Esc closes; focus is trapped in modals |

- **Live data:** poll every 2.5–5s and pause when the document is hidden. The run page uses SSE for transcript deltas (messages, thinking, tool calls).
- **In-flight states:** always show something: a pulsing dot, a "Connecting…" line, or a streaming caret. Never a blank pane.
- **Accessibility:**
  - Text meets 4.5:1 on its glass; muted text sits only on content glass, never over the raw scene.
  - Focus ring: 2px `--brand` with a 2px offset.
  - Hit targets are at least 28px on desktop and 44px on mobile.
  - Color is never the only signal: every state has a word or glyph.

---

## 9. Implementation notes for Svelte

- Put all tokens in one `:root` / `[data-theme=light]` stylesheet, using the exact values from section 2. Derive the hue variants with `color-mix(in oklch, …)`.
- Components to build:
  - `StatusChip`, `WorkspaceBadge`, `StageChip`, `StageFlask`, `PingPill`, `PingCard`
  - `GlassGroup`, `Button`, `SegmentedControl`, `Tabs`, `Popover`, `ConfirmDialog`
  - `Banner`, `Card`, `DataTable`, `FileList`, `DiffView`, `Sparkline`, `AttachmentPicker`, `FlowPicker`
  - `EmptyState`, `Skeleton`
  - `Sidebar`, `GuildSwitcher`, `Desk` (window manager), `LeafWindow`
- **Desk:** keep the tree in a store and persist it. Layout is computed (absolute rects from the tree), so views can stay mounted while moving between leaves: xterm and SSE survive a re-dock.
- **Backdrop:** one `<canvas>` component for Sigils (section 2.2), rendering about 20 fps, paused on `visibilitychange` and under reduced motion.
- Backdrop blur is expensive. Limit to one blurred layer per stack, and avoid blur on scrolling content.
