# Apothecary — Design System & UI Spec

Reference implementations:
- **Look** (colour, material, type, background, top bar, MR island): `Apothecary Ember x Sky.dc.html`, frames 1a–1g (one per theme). Theme tokens are in `themes.json`.
- **Behaviour** (live run, All-guilds desk, sidebar, org switcher, window manager): `Apothecary Live Run v10.dc.html`.

Where this document and a file disagree, **the file wins**: the Ember × Sky file for anything visual, v10 for anything behavioural.

Target stack: SvelteKit SPA, Svelte Flow (xyflow), xterm.js, SVG sparklines, polling (2–5 s) + SSE on the run page.

---

## 1. Concept & voice

**"Software, bottled."** A ticket goes in as ingredients; a run brews it through a recipe (flow); a reviewed MR comes out.

The visual language is **Ember × Sky**:
- dark frosted-blue glass windows floating over a slow, grainy, living background
- **ember** (warm orange) means *something is brewing*: the running stage, the live dot, the active accent phrase
- **sky** (cool blue) means *you can act*: calls to action, links, things waiting on you, drop zones
- texture, not decoration: grain and drifting noise give warmth and never carry information

The UI is professional and dense (an IDE-grade power tool). The lore appears only in:
1. the ember-coloured accent phrase in titles
2. vial/flask iconography for progress
3. a small set of renamed nouns (below). There is one vocabulary: no plain/lore switch.

| Domain object | UI label |
|---|---|
| Project | **Workshop** |
| Run | Batch (badge) · "brewing" (running) · "batch spoiled" (failed) |
| Flow | The formula |
| Plan | The recipe |
| "Needs you" | Awaiting your hand |
| Composer placeholder | Whisper to the builder… |
| Empty window desk | "An empty bench" |
| Egress held | "kept off the shelf until you allow or block them" |
| First account | "Claim this apothecary" |

Rules:
- Ticket, stage, ping, workspace, MR, egress and all state names stay technical. Never rename status vocabulary.
- Copy is short, sentence case, and uses no exclamation marks.
- Destructive dialogs say exactly what happens to the **run** vs the **workspace**.

**Domain hierarchy** (multi-user, no organisation level — TODO 25, 2026-10-27):

```
User ── N Workshops (projects) ── N Tickets ── N Runs
```

A user owns their integrations, channels, settings and workshops; a workshop owns flows, commands, automations and egress policy. Nothing is shared between users: each person's lists show their own things and no one else's. The one distinction is the **instance admin**, who manages accounts (Users page) and nothing more. The capacity limits are the deployment's, set in the boot env, the same for everyone.

---

## 2. Foundations

### 2.1 Typography

Load from Google Fonts: `Hanken Grotesk:wght@400;500;600;700`, `Bricolage Grotesque:opsz,wght@12..96,400..800`, `JetBrains Mono:wght@400;500;600;700`.

| Role | Font | Size / weight | Notes |
|---|---|---|---|
| Body (base) | Hanken Grotesk | 13–14px / 400, line-height 1.5 | `-webkit-font-smoothing: antialiased` |
| UI labels, tabs | Hanken Grotesk | 12–13px / 500; active 600–700 | |
| Meta, captions | Hanken Grotesk | 11.5–12.5px / 400–500 | color `--mut` or `--fnt` |
| Run title (h1, run page) | Bricolage Grotesque | 19px / 700, letter-spacing −.015em, line-height 1.15 | single line, ellipsis. Small on purpose: the title gives context, the windows hold the information |
| Page title (h1, other pages) | Bricolage Grotesque | 24–28px / 700, letter-spacing −.02em | `text-wrap: balance` |
| Display accent | the same font, coloured `--emb2` | inherits | One accent phrase per title ("Add idempotency keys to <ember>payment webhooks</ember>"). Section and window headings use Bricolage 15–20px / 600 |
| Big readouts | Bricolage Grotesque | 22–30px / 300 | only on overview pages, never in the run top bar |
| Agent "thinking" | Hanken Grotesk italic | 14px, line-height 1.45, color `--mut` | |
| Code, ids, hashes, numbers, costs, ports, branches | JetBrains Mono | 11–13px / 500–700 | Run ids `r_7f3a`, ws ids, `@3c9e1a`, `$1.234`, counts in badges, MR numbers `!20` |
| Inline code | JetBrains Mono 12–12.5px | `padding:1px 5px; radius 5px; background: var(--code)`, colour `--brand` in prose | |

Formatting conventions:
- relative times ("3m ago", "just now")
- 24-hour clock
- cost as `$0.000` (3 decimals)
- byte sizes
- counts as "×3" in alerts

### 2.2 Color tokens

All tokens are CSS custom properties set on the app root, one set per theme (`[data-theme=ember-sky|solarized|forest|brown|light|dark|pink]`). **Ember × Sky is the default.** Values for every theme are in `themes.json`. The tables below give Ember × Sky (dark) and Light.

**Base**

| Token | Ember × Sky | Light | Use |
|---|---|---|---|
| `--frame` | `#0a0a0e` | `#ebe6dd` | page ground under the noise |
| `--tx` | `#e6edf6` | `#1d232c` | primary text |
| `--mut` | `#93a3ba` | `#5b6576` | secondary text |
| `--fnt` | `#7d8ca3` | `#6f7888` | tertiary / meta / separators "/" |
| `--dim` | `#4f5d74` | `#a0a8b4` | drag handles, disabled glyphs, window-control glyphs |
| `--tx2` | `#c5d0df` | `#3c4553` | code text, file paths |
| `--brand` | `#8fb8ea` (sky) | `#8fb8ea` | primary CTA fill, links, focus, caret, drop zones |
| `--brandLt` | `#cfe2fa` | `#2a5c9c` (as text) | text on dark ink buttons, drop-zone label, link hover |
| `--onBrand` | `#0d1a2a` | `#0d1a2a` | text on brand; also the fill of the dark secondary button inside a sky card |
| `--p1` | `rgba(255,255,255,.03)` | `rgba(30,40,60,.03)` | faint fill / row hover |
| `--p2` | `rgba(255,255,255,.05)` | `rgba(30,40,60,.05)` | card-in-card, secondary button fill |
| `--p3` | `rgba(143,184,234,.12)` | `rgba(50,90,140,.10)` | hover on menu items, selected list row |
| `--ln` | `rgba(143,184,234,.10)` | `rgba(50,90,140,.10)` | hairline (window header divider) |
| `--ln2` | `rgba(143,184,234,.22)` | `rgba(50,90,140,.22)` | button borders, dashed outlines |
| `--code` | `rgba(4,9,18,.55)` | `rgba(232,227,219,.7)` | code / diff / input background |
| `--spec` | `inset 0 1px 0 rgba(207,226,250,.08)` | `inset 0 1px 0 rgba(255,255,255,.9)` | top specular on islands and small buttons |

**Semantic hues.** Each hue `X` also derives `--XBg`, `--XLn` and `--XGlow` with `color-mix(in oklch, X N%, transparent)`:
- `--XBg` at 14% (dark) or 12% (light): tinted fill
- `--XLn` at 40%: border
- `--XGlow` at 25%: outer glow

| Token | Ember × Sky | Light | Meaning |
|---|---|---|---|
| `--emb` (ember) | `#ff6a2b` · `--emb2` `#ff9150` · `--embTx` `#ffb488` | `#e0601f` · `#c9541a` · `#b5481a` | active / running / provisioning / agent stage / read tool / CPU. Use `--embTx` for ember text on dark |
| `--sky` | `#8fb8ea` | `#8fb8ea` (fill) · `#2a5c9c` (text) | **a human must act**: waiting_on_human, pings, held egress, the Inbox count, "in your hands", links, network |
| `--gold` | `#e8c25a` | `#9a7410` | gates, warnings, M files, the edit tool, loop-backs, memory, abandoned |
| `--grn` | `#a3c99a` | `#3c7a3a` | success: completed, workspace live, command stage, + lines, exec tool |
| `--ros` | `#ff5468` · text `#ff7d8c` | `#d33c52` · text `#c2334a` | failure / danger: failed, blocked egress, − lines, Abort |
| `--vio` | `#c3a2ff` | `#7a52d6` | action stage, search tool, permission ping |
| `--tea` | `#5fd6cc` | `#138a85` | wait stage, on_timeout, tunnel logs |

Two colours carry the story, and every theme keeps the pairing with its own hues (see `themes.json`: Solarized orange × cyan, Forest gold × sage, Brown ember × cream, Dark ember × silver, Pink coral × rose).

Sky is the **attention colour** as well as the CTA colour. Solid sky fill with ink `#0d1a2a` (`--onBrand`) is reserved for:
- primary buttons (one per group, max)
- "a human must act now": the waiting-on-you card, inbox count badges, the Waiting chip, the "N need you" pill

Everything else uses tinted fills. Ember is never a button fill except the brand logo tile; it marks what is running.

**Background ("noise").** One `Noise` component: a single WebGL2 fragment shader on one canvas behind everything, over the `--frame` base colour. It draws:
- four grainy circles around the middle of the screen, ember and sky in turn: radii 344, 280, 240 and 208 on a 1280px frame, orbits up to 750 across, sized from the screen height so an ultrawide doesn't inflate them
- a faint warm floor grain, its strength taken from `--grainA`

Every dot is computed from its device-pixel coordinate, never scaled: one device pixel on any screen, at full brightness (`.85`, with per-dot variation), and each circle has its own grain so two passing through each other don't merge. Density falls off from each centre, capped at `.72`.

Each theme colours it with `--nEmb` and `--nSky`, plus `--nBlend`: dots `screen` over the frame, `multiply` on Light.

Motion:
- Circles orbit at their own pace (97, 143, 211 and 167s), the second and fourth the other way round, on a slightly wandering path. The grain itself doesn't move; dots appear and vanish as a circle passes. It must read as still at a glance.
- Circles repel each other, harder the closer they get, and two centres never come closer than one radius (the larger of the pair's). They can sit side by side and overlap, but two dense circles on top of each other would add up to white.
- 10 frames a second at rest, 20 during a mix change or above resting speed, on a timer rather than `requestAnimationFrame`. Every frame re-blurs all the glass above it, so it must cost almost nothing.
- `noise.speed` multiplies the pace, live. 0 stops them.
- **Mix:** `noise.blue` (0–100) sets the share of sky: 0 all ember, 50 two of each, 100 all sky. Any change takes 3s. Dots change colour one by one, each at its own moment, so a change never passes through a muddy blend of the two hues.
  - The run page drives it: ≈20 while an agent stage runs, ≈80 while the run is waiting on you, 50 elsewhere.
- Nothing is drawn in a hidden tab. Under `prefers-reduced-motion` the circles hold still and only a mix change draws. Without WebGL2, or after losing the GPU context, the backdrop is plain `--frame`.
- No foreground particles. Nothing animated ever sits above content.

### 2.3 Materials: frosted windows, opaque islands

This is the core of the look. Windows are glass; anything that floats *over* a window is opaque. **Never stack glass-on-glass**: a nested `backdrop-filter` only blurs its parent, so it reads as a flat translucent box.

**A. Window glass (`c`)** is for everything that sits on the background:
- windows / leaves
- the top bar
- the sidebar and rail
- overview cards

| Token | Ember × Sky | Light |
|---|---|---|
| `--cBg` | `rgba(11,20,36,.58)` (sidebar `.62`) | `rgba(250,248,244,.58)` |
| `--cBf` | `blur(22px) saturate(140%)` | `blur(22px) saturate(140%)` |
| `--cLn` | `1px rgba(143,184,234,.16)` | `1px rgba(50,90,140,.16)` |
| radius | 16px | 16px |

There is no rim, specular or gradient on windows: flat tinted glass and a hairline only.

**B. Inset (`in`)** is a control group or field inside a window or the top bar:
- `--inBg`: `rgba(6,12,24,.45)` (Light `rgba(238,234,227,.45)`)
- border `rgba(143,184,234,.14)`
- radius 12px
- No blur.

**C. Island (`isl`)** is for everything that floats over content: popovers, menus, the MR island, the Layouts menu, modals and drag ghosts.

| Token | Ember × Sky | Light |
|---|---|---|
| `--solid` | `#09101e` (opaque) | `#fbf9f5` |
| border | `1px rgba(143,184,234,.28)` | `1px rgba(50,90,140,.28)` |
| shadow | on a **parent** `filter: drop-shadow(0 22px 34px rgba(0,0,0,.55)) drop-shadow(0 0 1px rgba(0,0,0,.6))`, so the shadow follows clip-path animations | same with `rgba(30,40,60,.2)` |
| specular | `--spec` | `--spec` |
| radius | 22px (island), 17px (popover), 26px (modal) | |

Modal scrim: `rgba(4,9,18,.5)` + `backdrop-filter: blur(6px)`.

**Selection fill** (`--sel` / `--railSel`): `rgba(143,184,234,.12)` + an inset ring `--selLn` `rgba(143,184,234,.3)`. **Running fill** (current stage tile, current run row): `linear-gradient(135deg, var(--embBg) 0%, color-mix(in oklch, var(--emb) 8%, transparent))`, border `--embLn`, glow `0 0 24px var(--embGlow)`.

The sidebar keeps window glass in every theme. In Light it is light glass with dark text.

### 2.4 Shape, spacing, elevation

**Radii** are concentric: outer containers are rounder than inner ones.

| Element | Radius |
|---|---|
| Sidebar, windows/leaves, overview sections | 16px |
| Popovers | 17px |
| Top-bar control groups (Run, Workspace) | 12px |
| MR pill (compact) | 20px (height 40px) |
| MR island (expanded) | 22px |
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
| `ap-blink` | steps(1) 1s | streaming caret (7×15px sky bar with glow) |
| `ap-slosh` | translateX ±2px | liquid in the active stage vial |
| `ap-bubble` | translateY −10px + fade | bubble rising in the active stage vial |
| `es-morph-in` / `es-morph-out` | `clip-path: inset(...)` from the trigger's exact rect (`round 20px`) to the full island (`round 22px`); 0.56s in, 0.34s out, `cubic-bezier(.32,.72,0,1)`, **no overshoot** | the MR island (4.5) |
| `es-content-in` | opacity 0→1, blur 6→0px, translateY −6→0, scale .98→1; 25% delay | island content |
| noise orbit | one shader, 10fps at rest, orbits of 97–211s; `noise.blue` changes in 3s | background (2.2) |

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
  - Islands grow **out of** their trigger instead: the outline follows the trigger's contour outward (clip-path, never scale), the content arrives slightly behind it, and closing pours back along the same path.
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
  - `prefers-reduced-motion` zeroes every move, so motion becomes cross-fades. The background circles freeze, slosh and bubbles stop, and pulses become static dots.
  - `prefers-reduced-transparency` makes the glass solid.
  - `prefers-contrast: more` strengthens lines and secondary text.

### 2.6 Iconography

- **No icon font.** Use Unicode glyphs with `\uFE0E` (text presentation) at 13–16px:
  - nav: ◈ Desk, ✉ Inbox, ▶ Runs, ☰ Tickets, ⚙ System
  - controls: ⌕ search, ↻ retry, ↗ external, ▾ menu, ↩ loop-back, × close, « » collapse/expand, ⇅ switcher
- **Play, pause, stop** on buttons are drawn, in one 12-unit box (`TransportIcon`): as text they come from different fonts at different sizes. The text glyphs ▶ ❚❚ ■ remain only in running text, such as the transcript.
- **Vial (workspace/project glyph):** 10×13px, `border-radius:2px 2px 5px 5px`, 1px border in the project hue at 60%. The fill is `linear-gradient(transparent X%, hue X%)`, where the fill level is decorative per project, plus a glow `0 0 8px hue@35%`. An idle project uses an empty vial with a `rgba(255,255,255,.2)` border.
- **Stage flask:** 14×20px, `border-radius:3px 3px 7px 7px`, 1.5px border in the stage-status color. The inner liquid height equals progress (done 100%, running ~55% with slosh and bubble, pending 0%).
- **Logo:** the brand tile `src/assets/mark/svg/apothecary-tile-primary.svg` at 36px, with a glow `0 0 22px var(--embGlow)`. It is the only ember fill in the UI, and also the favicon (with `app-icons/favicon-32x32.png` as the fallback for browsers without SVG icons).
- **Guild avatar:** a 20–30px rounded square (radius 7–10px) with a 160° gradient between two guild colors, 2-letter initials (700, dark ink `#0b1a12`) and a top specular. Sample guild colors:
  - green `#9ff0c0`→`#3fae72`
  - violet `#d9c6ff`→`#8f6fd8`
  - sky `#efe6cf`→`#b9a77a`
- **User avatar:** a 30px circle with a sky gradient, initials, and a presence dot (9px, green, 2px dark ring).

---

## 3. Status vocabulary (must be represented exactly)

### 3.1 Run state chip

Pill (999px), 12px / 600, `padding:3px 11px 3px 9px`, 7px leading dot.

| State | Style | Dot | Lore suffix |
|---|---|---|---|
| `pending` | tint `--p2`, text `--mut` | static muted | — |
| `provisioning` | ember tint (`--embBg` / `--emb` / `--embLn`) | pulsing | — |
| `running` | ember tint | pulsing ember + glow | "· brewing" (display 14px, .85 opacity) |
| `waiting_on_human` | **solid sky** fill, ink `#0d1a2a`, 700, sky glow | pulsing ink dot | label "Waiting on you" |
| *(held)* | an extra sky **tint** pill beside the state chip, "● In your hands · 4:12" (live clock) — not a state: the run is running (or waiting, when parked on its holder) AND in someone's hands | pulsing sky | — |
| `in_review` | violet tint (`--vioBg` / `--vio` / `--vioLn`), label "In review" | static | — |
| `paused` | `--p2` tint, `--mut` text, ❚❚ glyph | static | — |
| `completed` | green tint | ✓ | — |
| `abandoned` | gold **tint** | static | — |
| `failed` | rose tint, "✕ Failed" | — | "· batch spoiled" |
| `archived` | transparent, dashed `--ln2` border, `--fnt` text | — | — |

`in_review` is the run idling between phases with its merge requests open: reviewers have it, and reaction phases start when their triggers hold. It holds no concurrency slot.

Groups: **Active** = pending, provisioning, running, waiting_on_human, in_review, paused. **Finished** = completed, abandoned, failed, archived.

### 3.2 Workspace status (separate axis, never merged with the run chip)

Inline label: "Workspace" (`--mut`, 500), then a dot + value in the hue (600), then the mono ws id in `--fnt`.

| Value | Hue | Dot |
|---|---|---|
| live | grn | solid + glow |
| starting / provisioning / rebuilding | emb | pulsing |
| destroying | gold | pulsing |
| error | ros | solid |
| stopped | mut | hollow |
| unknown (provisioner unreachable) | gold | dashed ring + tooltip "Provisioner unreachable — check integration token" |
| none | fnt | no dot, text "no workspace" |

The compact form is the dot only (8px), with a tooltip "Workspace: live · ws_19b4".

### 3.3 Stage types

Used as chips (`padding:1px 7px; radius 6px; 10.5px/600`; tinted bg + hue text), as flow-editor node accents, and as stage-strip flask colors when relevant:
- `agent`: emb
- `command`: grn
- `human_gate`: gold (chip label "gate")
- `action`: vio
- `wait`: tea

### 3.4 Stage status (derived from events)

| Status | Flask fill | Pill style | Status line |
|---|---|---|---|
| done | 100% grn | `--p1` bg, solid `--cLn` border | "done · $0.21 · 3m" in `--mut` |
| running | ~55% emb, slosh + bubble | `--embBg` bg, solid `--embLn` border, emb glow | "attempt 2 · 4:12" (live clock) in emb |
| waiting (gate open) | sky, pulsing | `--skyBg` bg | "waiting on you" |
| failed | ros | `--rosBg` bg | reason |
| paused | mut, 50% | — | "paused — will re-enter" |
| pending | empty | transparent bg, dashed `--ln2` border, .55 opacity | — |

Every attempt shows **"attempt N"**. Loop-backs render in the transcript and timeline as gold `↩ review → build (1/3)`.

### 3.5 Ping types

Type pill: 999px, 11px / 600, tint fill + hue text + hue border, 6px dot.

| Type | Hue | Classification |
|---|---|---|
| `gate` | grn | **wanted** (human_gate stage) |
| `debug` | sky | **wanted** (the flow's `debug` leaf: "Stage X failed — check why", the failure, the workspace link; continue re-runs the stage, abandon ends the run; label "Debug") |
| `clarification` | sky | unwanted (counts against run) |
| `blocker` | ros | unwanted |
| `permission` | vio | unwanted |
| `phase_confirm` | grn | **wanted** (a reaction phase asks before running; label "Phase") |
| `select` | tea | **wanted** (pick which review threads to address; label "Select") |

- Ping status: pending (solid sky count badge), answered ("answered 4m ago — approve" in `--mut`), withdrawn (struck-through, `--fnt`).
- A queued ping shows "· queued behind N".

### 3.6 Edges and handles (flow editor)

| Edge | Color | Style |
|---|---|---|
| success | grn | solid |
| on_fail | ros | dashed, animated |
| on_timeout | tea | dashed, animated |
| on_reject | gold | dashed, animated |
| fallback ("then") | same hue at 50% opacity | dashed |
| dataflow | `--ln2` | faint, dotted |

- Failure edges are labelled "×3" or "×∞" in a mono 10px pill.
- Handles are 8px dots in the edge color.

### 3.7 Tool calls

- **Kind chips:** read (emb), edit (gold), search (vio), exec (grn), delete (ros), move (tea), fetch (emb), think (mut), other (mut).
- **Status:** pending (hollow), in progress (pulsing emb dot), completed (grn ✓), failed (ros ✕).

---

## 4. Components

All interactive controls use `all:unset; cursor:pointer; white-space:nowrap` as a base and are laid out with flex + `gap`.

### 4.1 Buttons

There is **one selected / secondary look everywhere**: sidebar items, rail items, tabs, segmented controls, popover choices, the theme toggle and the layout button all share it. Don't introduce grey or translucent variants.

| Variant | Spec |
|---|---|
| **Primary** | bg `--brand`, color `--onBrand`, 600, radius 12px, `padding:4px 11px`, glow `0 0 14px var(--brandLn)`. One per group, max. (MR link, Retry, "Open Windows") |
| **Secondary** | bg `--p2`, 1px `--ln2`, `box-shadow: var(--spec)`, 500. Hover bg `--p3` |
| **Selected / chrome button** | bg `--sel` (`rgba(143,184,234,.12)`), inset ring 1px `--selLn` (`rgba(143,184,234,.3)`), text `--tx`, 600. Hover on unselected: `--p3` |
| **Danger outline** | transparent, rose text, 1px `--rosLn`; hover `--rosBg`. Destructive actions end in "…" and open a confirm |
| **Danger solid** | bg `--ros`, ink `#1b0a0a`, 600 (only inside confirm dialogs and alert banners) |
| **Ghost** | transparent, `--mut`; hover `--p3` + `--tx` |
| **Icon button** | 24×24 (window chrome) or 28×28 (sidebar), radius 7–9px, `--mut`; hover `--p3` |
| **Disabled / not yet** | dashed 1px `--ln2`, `--fnt` text, e.g. "MR 2/2 —" |

Size: 12px text in headers; 11–11.5px compact in the sidebar; 12.5px in menus.

### 4.2 Badges and counts

- **Count in nav or tab:** JetBrains Mono 10–10.5px / 600, `padding:0 7px`, radius 13px. Neutral is transparent with `--mut`. Hot is solid sky with ink `#0d1a2a`.
- **Rail badge:** absolute top-right, 9–9.5px, `line-height:14px`, solid sky.
- **"+N elsewhere":** sky tint, 1px sky@35% border, mono 9.5px / 700 (on the guild switcher when other guilds have pending pings).
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

- The container is window glass, `padding:3px`, radius 12px, gap 3px.
- Options are 12.5px, `padding:5px 12px`, radius 9px. The selected option uses the bead/selected style.
- Options can carry a trailing mono count in `--mut`.

### 4.5 Top bar and control groups

The run top bar is one window-glass strip (radius 16px, `padding:8px 10px 8px 16px`, gap 8–10px). From left to right:

1. **Title block** (`flex:1; min-width:0`):
   - breadcrumb, 12px `--mut`: "Northwind / ledger-api / r_7f3a · PROJ-482 ↗" (ticket link in `--brand`)
   - run title, Bricolage 19px / 700, ellipsis, with the ember accent phrase
   - status line, 12px: a **small** state chip (11.5px, `padding:1px 8px`, 6px dot), then mono 11.5px "`14:08` · $1.23 · 42 turns" with the elapsed time in `--embTx`
   - No big stat numbers and no run badge in the top bar.
2. **Run** group (inset, 4.5a): `❚❚ Pause`, `■ Abort…` (danger outline).
   - Terminal state: Retry \<stage\> (primary), New run from this…, Archive….
3. **Workspace** group (inset, max-width 330px):
   - label row: "Workspace", the status dot + value, the mono ws name (ellipsis)
   - button row: Env ▾, Editor ↗, Rebuild ▾ (disabled .45 when not applicable)
4. **MR island** (4.5b).

The **Layout** control is **not** in the top bar. It is a floating button at the bottom right of the run page (6).

**4.5a Inset control group.** Inset material (2.3 B), `padding:7px 10px 8px`, column with gap 6px.
- Label row: 12px `--mut`, nowrap.
- Button row: gap 5px. Buttons are 12.5px / 600, `padding:4px 11px`, radius 9px, `--p2` + 1px `--ln2`.

**4.5b MR island (Dynamic-Island pattern).** This is the only top-bar control that expands.

- **Compact pill:** 170×40, radius 20px, `--inBg`, 1px `--ln2`. It contains:
  - "Merge request", or "Merge requests" with more than one (12px `--mut`), always, so the pill says what it is before it opens
  - the primary MR as a sky chip: mono "!20 ↗", 700, ink text
  - an overflow chip "+N" with a 6px dot in the **worst** status among the rest (conflict ros > CI failed ros > CI running gold > draft `--fnt` > merged sky)
- **Expanded** (on hover / focus; Esc or pointer-leave closes): a 420px island anchored to the pill's top-right (−6px), z 60, so it sits **over** the panes below.
  - Head: "Merge requests", a count badge, a "N conflict" warning (ros), and the target "ledger-api → main".
  - One row per MR (grid `auto 52px 1fr auto`, `padding:7px 9px`, radius 11px):
    - a status dot
    - the mono number
    - the title over a status line ("CI passing · no conflicts", "conflict in priv/repo/…", "CI running · 3m", "draft", "merged 2h ago")
    - an action: Open ↗ (primary MR, sky), Resolve (conflict, sky), or ↗ (ghost)
  - The primary MR row has a `--p3` fill and a `--brandLn` border.
- **Animation:** `es-morph-in` / `es-content-in` (2.5). The island is opaque `--solid`, so nothing ghosts through. Its shadow sits on the parent `drop-shadow`.
- **With one MR:** the pill shows "!20 ↗" only, and hover still expands for its status. **With none:** a dashed "no MR yet" pill that does not expand.

### 4.6 Waiting-on-you card

- Solid `--sky` fill, ink `--onBrand`, radius 12–16px, `padding:12px 14px`, no border.
- Head: 11.5px / 700, letter-spacing .04em, e.g. "WAITING ON YOU" or "CLARIFICATION · THIS RUN".
- Question: 14–16px / 600.
- Actions:
  - primary: dark ink `#0d1a2a` fill with `--brandLt` text
  - secondary: 1.5px ink outline
- This is the compact form of the ping card (4.11). It is used in the Pings window and on overview pages.

### 4.7 Alert banner (in-flow)

- Opaque, because it may sit straight on the backdrop and its text must stay readable: `--solid` with the hue tint mixed in (`--rosSolid`, `--skySolid`), border `--rosLn`, radius 12px, `padding:5px 5px 5px 7px`, margin 0 16px.
- Contents, left to right:
  - an 18px round "!" badge (solid hue, glow)
  - a bold hue title ("Egress held ×3")
  - an optional display lore aside
  - a mono list with ellipsis and "+N more"
  - a spacer
  - a solid CTA ("Review", which focuses or opens the Egress window on its Held filter)
  - a ghost "Dismiss" (a snooze: until the page reloads or another host is held)
- Sky variant: held egress, workspace unknown, provisioner unreachable. Held egress names only hosts nobody ruled on yet: a host someone blocked is reviewed and never raises it again.

### 4.8 Popover / menu

- Island material (2.3 C): opaque `--solid`, 1px border `rgba(143,184,234,.28)`, `--spec`, radius 17px, `padding:6–10px`.
- Shadow: on the parent, `drop-shadow(0 22px 34px rgba(0,0,0,.55))`.
- Title in Bricolage Grotesque 19px, plus an optional right-aligned 11px legend.
- Rows: radius 10–14px, `padding:7px 8–10px`, hover `--p3`. The selected row gets the selected style.
- Dividers: 1px `--ln`, margin 4px 6px.
- Popovers are **positioned beside their trigger, never on top of it**. The one exception is the MR island (4.5b), which grows out of its trigger. The org switcher opens to the right of the sidebar (`left: sidebarWidth + 16px`, `top: 56px`).
- Close on outside mousedown or Esc.

### 4.9 Modal / confirm

- Scrim (above), then a dialog of 440px, radius 26px, island material (2.3 C), shadow `0 30px 80px rgba(0,0,0,.5)`.
- Header: a 34px round hue icon (danger: `--rosBg` / `--ros`) and a title.
- Body text is `--mut`, `text-wrap: pretty`, and states consequences per object:
  - *"The current turn is cancelled and the run becomes **abandoned**. The workspace stays up so you can inspect it; it's reaped after 48h."*
- Footer: ghost Cancel, then the destructive solid button.
- **Required confirms:** Abort, Archive, Delete ticket, Delete project, Delete integration, Remove channel, Delete review comment, Clear build cache, Leave editor with unsaved changes.
- No native `confirm()`.

### 4.10 Content cards (inside panes)

- **Card-in-pane:** radius 13–14px, `--p2` bg, 1px `--ln`.
- **Collapsible row** (`<details>`): summary line with `padding:7px 12px`, 12px text, a bold ▸ label, and a muted ellipsised preview.
- **Tool group** (folds when >3 calls): "▸ 12 tool calls", kind chips (tinted 999px pills, 11px, "read 4"), and right-aligned "1 failed" in rose.
  - Live groups never fold.
- **Tool call row:** status glyph, kind label in its hue (600), mono path, and mono `+14 −6`. An expanded call shows the diff.
- **Diff:** JetBrains Mono 11px on `--code`, radius 8px.
  - Grid columns `36px 1fr`, with the line number right-aligned at .6 opacity.
  - Added lines: `--grnBg` / `--grn`. Removed lines: `--rosBg` / `--ros`.
  - A "truncated · showing 400 of N lines" footer.
  - A binary-file state: centred `--mut` text "Binary file · 24 KB".
- **Plan checklist:**
  - Title in display 18px, plus "v4 · 2 of 5".
  - Items are 15px circles. Done: green fill + ✓, text struck through and muted. In progress: emb ring + pulsing dot, text emb / 600. Todo: `--ln2` ring.
- **Stage divider (transcript):** a hairline gradient, then the display stage name (19px), then "attempt 2 · builder · gpt-5-codex · 14:02:11", then a hairline gradient.
- **Status line:** 12px in the hue with a glyph (↩ loop, ✓ done, ✕ failed, ⏸ parked, ↻ resumed, ⟳ engine restarted, ◉ take-over).
  - A take-over brackets its turns: sky "You took over — the agent waits for your word between turns; the stage cannot end until you release it", then "Handed back — the agent finishes its task · 12m 08s · 3 turns · 2 messages · $0.42" (the hold's own account, from `hold_released`). A holder who went quiet reads "Parked — the agent is in your hands and waits for your word".
  - The handback is a prompt card ("Handed back — finish the task"), like the stage prompt.
- **User message bubble:**
  - right-aligned, max-width 70%
  - `--brandBg` + `--brandLn`, radius `10px 14px 4px 14px`
  - meta below: "you · delivered 14:05", "queued — read when its turn ends", or "interrupt" (sky)
- **Agent message:** max-width 70ch, 13.5px / 1.6, markdown. While streaming it ends with the blinking caret.
- **Thinking:** a `<details>` (open while live) with an emb gradient tint `linear-gradient(90deg, var(--embBg), transparent 70%)`, a pulsing "Thinking ▾" summary, and the display body.
- **Inline agent question (non-gate ping):** a ping card (4.11) embedded in the transcript. Until answered it carries a "waiting for your answer" line.

### 4.11 Ping card (inbox, run page, inline)

- Sits above the composer when the run is waiting. Radius 20px, **solid `--sky`** with ink text (the full form of 4.6):
  - fill `--sky`, text `--onBrand`, no border, glow `0 0 30px var(--skyGlow)`
  - inner buttons: primary = ink `#0d1a2a` fill with `--brandLt` text; secondary = 1.5px ink outline
- **Head:** a 22px solid sky badge, the type pill, the stage, "raised 2m ago", and the run + ticket title (inbox cards always show which run and ticket they belong to).
- **Body:** markdown question + context, and either an MR link or "Review & comment →" (opens plan review).
- **Actions:**
  - gate: Approve (primary) / Request changes… (feedback required)
  - options ping: one secondary button per option, plus "Other…"
  - free-text ping: textarea + Send
- Every answer can carry attachments (4.15).
- **Answered state:** collapsed to a single muted line.

### 4.12 Composer

- Control glass, radius 20px, margin 0 12px 12px.
- Textarea area: `min-height:44px`, placeholder in `--fnt` ("Whisper to the agent…").
- Footer row: key hints ("↵ send · ⇧↵ newline", held only; before a take-over the composer is the Take over button alone, 11px `--fnt`), an attach button, then **Take over** (tertiary, only while an agent stage runs), **Send now** (secondary) and **Send** (primary).
- Shown only while the run is active.
- Sending snaps the transcript back to auto-follow. If the user has scrolled up, show a floating "↓ Follow" pill.
- **Whisper vs take-over.** A whisper is one message: read at the end of the agent's turn (or now, interrupting), and the agent carries on — the end of its next turn ends the stage as usual. **Take over** holds the session: while held, a turn ending with nothing queued is NOT the stage's end; the agent waits for your next message, every message is a turn in the same session, and the stage can only end once you **Release** — the engine then sends the fixed handback prompt, and when that turn ends the stage's output check runs as usual.
- **Held state:** the composer takes the attention colour (sky border, `--skyBg` wash, sky glow, as the ping card 4.11), a head line "● In your hands · 4:12 · 2 messages" (live clock), the placeholder "The agent is in your hands. Say what to change…", and **Release** (sky) replaces Take over. After Release the button reads "Handing back…" until the engine records it — the agent's turn in flight ends first.
- A holder who says nothing for the live hold (30m) parks the run: it reads Waiting on you, the slot is freed, the composer stays held; the next message (or Release) wakes it.

### 4.13 Tables and lists

- Rows: `padding:8–10px 14–16px`, 1px `--cLn` top border, hover `--p1`.
- Header row: 11px / 500 `--mut`, sticky.
- Numbers and ids are in mono and right-aligned. Times are relative, with an absolute 24h tooltip.
- Prefer a CSS grid per row (e.g. `118px minmax(0,1fr) 210px 64px auto`) so long text ellipsises instead of wrapping.
- **File list:** a status letter (A grn, M gold, D ros, R vio, C tea, U `--mut`) in a 16px mono column, the path, `+n −n` or "bin", and a ● dot for uncommitted files.
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
- **Section:** an Bricolage Grotesque 20px title + a 12px `--mut` sub. Fields sit on a 2-column grid (`minmax(0,1fr)`) with gap 12×16.
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
- Hues: CPU emb, memory gold, network sky (in = solid, out = dashed).
- A shared hover crosshair across all three tiles shows a value and timestamp tooltip.
- **"Collecting samples…"** state: a dashed baseline and an emb shimmer dot.

### 4.17 Empty, loading and error states

- **Empty:** a dashed 1.5px `--ln2` box, a display title of 22–26px ("An empty bench", "Nothing running", "All still on the shelf"), a 1-line `--mut` explanation (max 40ch), and one primary action.
- **Loading:**
  - Skeleton bars: `--p1` with a 1.4s shimmer (`linear-gradient(90deg, transparent, var(--p2), transparent)`).
  - For live panes, prefer "Connecting…" plus a pulsing emb dot.
  - Never use a blank page.
- **Error:**
  - Inline rose card with a short cause, a mono detail in a `<details>`, and a "Retry" secondary.
  - Integration test results persist: "✓ ok · tested 3h ago" / "✕ 401 Unauthorized · tested just now".
- **Stale / unknown:** a sky dashed ring with a tooltip that explains (e.g. provisioner token expired).
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
- **Sidebar:** `flex:none; width:232px; margin:6px 0 6px 6px; radius 16px; overflow:hidden`, window glass (`--cBg` at .62 + `--cBf` + `--cLn`). Column layout:
  1. **Brand row** (56px): logo, then "Apothecary" (15px / 600) with the display tagline "software, bottled" below, then a « collapse button (28px).
  2. **Guild switcher:** a selected-style button with margin 0 10px 8px, radius 12px:
     - 26px guild avatar, guild name (13px / 600, ellipsis), sub "4 workshops · 14 members" (11px `--mut`, ellipsis)
     - an optional "+N" elsewhere badge, then ⇅
     - opens the Guilds popover
  3. **Search / command bar:** "Search, jump, do…" plus a ⌘K mono key cap. It opens a command palette: jump to run, ticket, project, guild or action.
  4. **Nav**, scoped to the current guild: Desk ◈, Inbox ✉ (sky count), Runs ▶ (count), Tickets ☰, System ⚙. Item: `padding:6px 10px`, radius 10px, 16px glyph column, label, right count. Active item uses the selected style.
  5. **Workshops section:** a header in display 14px `--mut`, plus a legend (▶ live · ✉ pings) and "+". A scrollable list:
     - Each row shows the vial, name, live count (mono emb), and ping count (solid sky pill, or empty).
     - The **current workshop expands** to show its live runs as an indented tree: 15px indent with a 1px left rule. Each run row has a state dot (emb running / sky waiting), the title (ellipsis) and the mono id. The current run is highlighted with `rgba(255,255,255,.06)`.
     - Quiet workshops are dimmer (`--mut`).
  6. **Capacity:** a mini card in a 2-column grid, "Runs 3/6" and "Envs 4/10", each with a 5px bar (sky and green gradients).
  7. **User card** (inset, radius 12px), at the very bottom: the avatar with presence, name (12.5px / 600) and handle, which open the user menu (5.1); under a hairline, the **theme picker** (Ember × Sky, Solarized, Forest, Brown, Light, Dark, Pink), showing the theme's two hues and its name. This is the only appearance control end users get.
- **Collapsed rail:** 60px wide with the same margins and material; a centered column with gap 4px:
  - logo, then a » expand button
  - guild avatar button (with a sky dot when there are pings elsewhere)
  - ⌕ search
  - nav icons (40×36, with sky count badges)
  - a 24px divider
  - workshop vials (40×34, with sky ping badges)
  - a spacer, then the user card: the theme's two hues over the user avatar
  - Collapse state persists (localStorage). Shortcut ⌘\\.
  - **Mobile:** the same sidebar becomes an off-canvas drawer, and the rail becomes a bottom tab bar (Desk, Inbox, Runs, Tickets, More).
- **Main:** `flex:1; min-width:0; display:flex; flex-direction:column`. It is transparent; the scene shows through between panes.

### 5.1 User menu popover

- Opens above the user row at the bottom of the sidebar (above the rail avatar when collapsed), 240px wide, flipping below when there is no room.
- Contents: title = the user's name, legend = their `@handle`; rows **Account** (profile, password, personal tokens), **Users** (admins only), a divider, **Sign out**.
- There is no switcher: a person has exactly one desk, their own.

### 5.1b Sign-in and setup

- A browser without a session sees only the **sign-in** page: handle or email, password, one button. A locked-out hint names the terminal command (`apothecaryd user reset-password <handle>`).
- An instance with no account yet shows the **setup** page instead ("Claim this apothecary"): handle, name, email, password twice.
- Both are centred cards on the scene, with the logo and tagline; no sidebar until signed in. A 401 from any later call drops the shell back to sign-in in place, so a deep link survives.

### 5.2 Routing

| Route | Page |
|---|---|
| `/` | The desk |
| `/inbox` · `/inbox/:pingId` | Inbox · plan/document review |
| `/runs` · `/runs/:runId` | Runs list · **live run** |
| `/tickets` · `/new` · `/:ticketId` | Tickets |
| `/w/:workshop` | Workshop overview (runs, tickets, flows, agents, health) |
| `/w/:workshop/flows/:name` | Flow editor |
| `/w/:workshop/settings` | Workshop setup (repo, commands, execution, workspace and egress) |
| `/system/{integrations,channels,account,users,limits,reaper,storage}` | System hub (`users` for admins only) |
| `/g/…` | Old guild-scoped links redirect to the same page without the segment |

Breadcrumbs always follow the hierarchy: **Desk / Workshop / Ticket key / Run id**. Each segment is a link (12px `--mut`, "/" in `--fnt`, current segment in `--tx` mono), and the row is `nowrap` with overflow hidden.

---

## 6. Window system (live run desk)

The run page body is a **user-arrangeable tiling desk**, like an IDE.

- **Views** (14): Flow, Session, Ask, Pings, Timeline, Record, Changes, Files, Terminal, Logs, Egress, Ports, Resources, Vitals. Each view lives in exactly one leaf at a time, or is hidden.
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
    - drop preview: an absolute box, radius 12px, `color-mix(--brand 20%)` fill, **2px dashed `--brand`**, `inset 0 0 30px var(--brandGlow)`, and a 13px / 600 `--brandLt` label ("Stack here", "Drop to split →"…)
  - **Gutter (8px)** between panes: cursor `col-resize` / `row-resize`. A centered 2px grip line appears on hover with a `--brandBg` highlight. Drag to resize, with a minimum pane size of about 120px.
  - **Leaf buttons:** split right / split down (move the active tab into a new half), fullscreen, hide.
  - **Fullscreen** (⤢) is a moment, not a layout: the window, with all its tabs, springs out over the sidebar and the top bar to fill the screen, on solid `--solid` so nothing shows through, with an `esc` key cap in its strip. Esc (or ⤡) puts everything back exactly as it was. It is never saved and the tree does not change. Esc in a focused terminal stays the terminal's (vim needs it); an open popover or modal takes Esc first.
  - **Tab ×** hides that view.
  - **Layouts popover** (from the ⊞ **Layout** button, shortcut **L**: a 40px sky-tinted square floating at the bottom right of the run page, `--brandBg` over `--solid`, 1px `--brandLn`, with a tiny "L" key badge). It hovers over every window, a fullscreen one included (`--z-fab`); the view in the desk's bottom-right corner keeps its docked composer or ping card clear of it (`--fabClear`). The popover opens above the button, right-aligned, 300px wide, as an island (2.3 C):
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
  - Horizontal: stage pills (flask + mono id + status line) joined by 10px connectors, ending in a dashed display "done" pill.
  - Vertical: a flask column joined by vertical connectors, a card per stage, a display title "The formula", and the mono flow @hash.

**Per-view content:**

| View | Contents |
|---|---|
| Session | Transcript (4.10) + ping card + composer. Auto-follows the bottom. |
| Ask | A read-only session beside the run (a flow's `ask:` section) that answers questions about the code as it stands. Head: display "Ask", the harness in mono, a model select (the harness's reachable models; a free field when unknown). Thread: your questions as right-aligned brand bubbles ("you · 14:05"), answers as markdown with "model · $0.012 · 14:05" under each; the answer being written streams in with a pulsing "thinking…" / "Read main.go…" line. One question at a time: the box and Ask button wait while an answer is written. Off state: an empty state with the reason (no `ask:` section, no workspace, a harness that cannot be read-only). The conversation persists with the run and outlives it while the workspace does. |
| Pings | All pings raised by this run (ping cards); summary "1 pending · 2 answered". |
| Timeline | Event rows: 24h time (mono), a tone dot (ok grn / bad ros / warn sky / info emb), text, and inline "log" / "open" actions. |
| Record | Snapshots and attachments table: name, kind chip, source ("build · attempt 2", "ping answer", "run goal"), size, time, open/download. Kind filter segmented control. Note "Kept after the workspace is destroyed". |
| Changes | Repo segmented picker (primary ★). Summary "branch vs main · fork a1b2c3 · 7 files +212 −48 · 3 commits", collapsible commits, file list (4.13) + unified diff. "Refreshes every 5s". |
| Files | Repo picker, lazy tree (show ignored toggle, refresh), file viewer with line numbers, a 1 MB cap notice and a binary state. |
| Terminal | Repo picker, connection status pill (connecting emb pulse / connected grn / disconnected ros + Reconnect), xterm surface on `--code`. "Session ends when you leave". |
| Logs | Source segmented control (Workspace / Provisioning / stage logs). Mono 11.5px log tail with colored sources (tunnel tea, firewall ros, harness emb). |
| Egress | Rule-now filter (All 41 / Allowed / Held 2 / Blocked 3) + host search. Table: host:port (mono), seen verdict chip, **rule now** toggle (allow / block, applies live, saved to the workshop policy; a held host — refused by default, not yet ruled on — shows a sky "held" between the two until someone picks one), protocol, count, bytes out/in, last seen. Footer links to the workshop egress settings. |
| Ports | Detected ports as buttons (`:4000 beam.smp`), clicking exposes and opens. Manual "Expose port" input (1–65535, Enter submits). |
| Resources | Process count, time window, and per-process breakdown. *The engine samples only sandbox totals today (`ResourceSample.pids`), so the breakdown waits on a per-process sampling API; until then the view shows totals.* |
| Vitals | The three sparklines with a shared crosshair (4.16). |

---

## 7. Page layouts

Every page uses the shell (section 5).

**The backdrop is not looked at directly.** It shows only in the gaps between windows and, blurred, through their glass, on every page and at any screen size. So the windows always fill the main column:

- A page body is one window (`.page.c`) filling all the height and width its header leaves. It never ends short of the pane, and the window itself has no `max-width`. Short content leaves empty glass, never bare backdrop.
- What sits in that window is an inset, which `.c` inside `.c` gives by itself. A page holding one table needs no inset box: the table sits directly in the window.
- Pages with their own columns (the System hub, the inbox, the review workbench, the flow editor) make every column a full-height window instead.
- Cards in a `.card-grid` share the row, up to `--card-max` (860px) each.
- Dialogs and sheets leave for `<body>`: a `position:fixed` element inside glass is trapped by it.
- The sign-in, setup and engine-unreachable screens are the exception: they have no panes, so they stay a card on the backdrop.

**Wide screens keep their content readable.** The glass stays full width, but stretched content leaves a row's ends too far apart to read. So content never simply stretches:

- Whole sections go side by side: `.sections` is a grid of columns at least `--section-min` (640px) wide, as many as the window has room for (the Desk, Integrations, Skills, Account, a workshop's flows).
- Anything else keeps to `--content-max` (1600px), centred in its window; `.readable` passes this down a wrapper. A page that is one settings form keeps to `--settings-max` (900px).
- The tiling desk and the flow editor are exempt: the person lays those out.

**Every list page has the same anatomy,** the one Tickets and Runs set:

- One main action, in the top bar: solid, `.btn-primary.btn-lg`, worded "+ New …". A page below a shared header puts it there with `HeaderAction`. Several kinds to start from share one button and a menu (`ActionMenu`). Actions on one row or pane stay on that row or pane; pages with nothing to create have no main action.
- Filters in the window, directly above the list, in a `.filters` row: search first, then what narrows it, every control `--filter-h` tall. Filters never sit in the top bar.
- The list sits directly on the window's glass, with no box around it. Tables use `RunTable`, `TicketTable` or `.rows`.

The page header pattern is: breadcrumb row, then an h1 with one display accent phrase and an optional status chip, then a meta row (12px `--mut`, flex-wrap, gap 4px 14px), then right-aligned glass control groups. Header padding is 16–22px 20–22px 12px.

### 7.1 Live run: `/g/:guild/runs/:id` (reference: v10, default view)

1. **Top bar** (4.5): title block, then the Run and Workspace groups, then the MR island. It is one glass strip; the layout below is v10's, kept for reference.
   - **Left:** (no run badge in Ember × Sky)
   - **Center:**
     - breadcrumb
     - h1 "Add idempotency keys to *payment webhooks*" + run state chip
     - meta row: ticket key tag + "ticket ↗"; flow name + mono @hash; mono branch "← main"; cost · turns
   - **Right:** three glass groups (Run / Workspace / Merge requests, 4.5).
   - The Env ▾ popover opens on the SSH door: *Open in VS Code*, then a copy row with the two desktop `settings.json` lines that make one's extensions follow (`remote.defaultExtensionsIfInstalledLocally`, pre-filled with the workspace editor's list, and `remote.downloadExtensionsLocally`), the ssh command and the known_hosts line as copy buttons that flip to "copied ✓" for 1.5s, the door fingerprint — or the one line that stands in the way (SSH off, no key registered). Under a divider, the workspace's app and service links open in a new tab ↗.
2. **Alert banner** (egress blocked or workspace unknown), only when relevant.
3. **Desk** (section 6) fills the rest.
- **Paused:** the Flow view's current stage shows "paused — will re-enter build". The Run group shows ▶ Resume (primary) + Abort….
- **Waiting:** the chip turns solid sky, the ping card docks above the composer, and the sidebar Inbox count increments.
- **Taken over:** a sky "In your hands · 4:12" pill beside the state chip, the composer in its held state (4.12), and the run row's second line ends "· in your hands". The timeline carries `hold_taken` / `hold_released` with duration, turns, messages and cost.
- **Terminal states:** the composer is hidden and the Session shows a closing status line. Run group: Retry \<stage\> / New run from this… / Archive…. When the workspace is destroyed, workspace-only views (Changes, Files, Terminal, Ports, Egress live) show an empty state: "Workspace destroyed 2h ago — the Record keeps every snapshot". The Record stays.

### 7.2 All-guilds desk: `/` (reference: v10, "All guilds" in the switcher)

One window of two stacks: what needs you and what is brewing, then what finished and the room left. They sit side by side once the window has room, and with room for three columns the first stack splits in two.
1. **Header row:**
   - Left: caption "All guilds · 3 guilds · 7 workshops", then an h1 "Good afternoon, Maya. *4 things need you, 6 brews on the fire.*"
   - Right: the main action, **+ New ticket**. The segmented filter Everything (n) / Needs me (n) / Running (n) sits in the window, above the workshop cards it filters.
2. **Needs you panel** (content glass, radius 18px):
   - head: a sky count badge, a display title "Awaiting your hand", "oldest first, across every guild", and "Open inbox →"
   - rows (grid `118px | 1fr | 210px | 64px | auto`):
     - type pill
     - question (500, ellipsis), with "run id · run title · stage" below (and "· queued behind N" when relevant)
     - guild avatar + guild / workshop
     - age (mono)
     - CTA in the selected style: Review plan (gate) / Answer / Decide (permission)
3. **One section per guild:**
   - Section header: avatar 30px, name (15px / 600), "role · N members", a "N live" emb pill, a "N need you" solid sky pill, a hairline spacer, "today · 3 completed · 1 failed", and "Open →".
   - Then a card grid `repeat(auto-fill, minmax(330px,1fr))`, gap 10px:
     - **Workshop card** (content glass, radius 16px): head with vial + name + "2 live · 1 ping". Run rows (grid `8px | 1fr | auto | auto`): state dot, title over "state · stage" (state word in its hue), stage-type chip, mono elapsed over cost. A click opens the run.
     - **Quiet card:** a dashed border with a display "2 quiet workshops" and the names "infra-tf · docs-site — nothing running". If the whole guild is idle: "All still on the shelf".
- The filter applies to the run rows, and a card hides when all its runs are filtered out.

### 7.3 Guild desk: `/g/:guild` (not mocked; follow 7.2)

The same anatomy, scoped to one guild:
1. Header: "Northwind Labs" with an accent phrase, plus the main action.
2. Needs-you panel (this guild only).
3. The workshop card grid, the same cards as 7.2 at larger size. Add the last-finished runs (completed / failed with MR link) under the live ones, and a ticket count with "+ New ticket".
4. A "Recently finished" table (Finished-group runs, 24h): title + id, state chip, workspace dot, workshop, flow, MR link, updated.
5. The capacity strip (concurrent runs, environments, queue length when pending FIFO).

### 7.4 Runs list: `/g/:guild/runs`

1. Header with one call to action, **+ New run**. In the page window, above the table, the filters as on Tickets: search, a workshop filter (select, mirrors `?project=`), and a segmented control Active (n) / Finished (n).
2. Table columns: title + mono id, state chip, workspace dot, current stage (flask + id + attempt), workshop, flow, MR, updated.
3. Polls every 4s with a "updated 2s ago" caption.
4. Runs start from tickets, so **+ New run** opens a dialog that picks one and goes to its page, where the flow and parameters are chosen. With no ticket yet, it offers **New ticket**. The empty state links to Tickets.

### 7.5 Inbox and plan review: `/g/:guild/inbox`, `/:pingId`

- **Inbox:** one window, the filters on top (Pending / Answered, then the ping types), a two-pane layout under them. It has no main action: nothing is created here.
  - **Left list** (380px, content glass):
    - One card per run, showing its newest pending ping: type pill, stage, age, and **run title + ticket key + workshop**.
    - "Answer this first — N earlier pings queued" in sky.
  - **Right:** the selected ping card (4.11) at full size, plus a run summary strip (state, stage flask, cost, "Open run →").
  - Answered tab: a flat history table.
  - The cross-guild `/inbox` uses the same layout, with a guild column and a guild filter.
- **Plan review (document workbench):** a three-column layout.
  - **Left:** a document outline (headings) that jumps to sections.
  - **Center:** the document, max-width 72ch, markdown blocks rendered from the **raise-time snapshot**. A caption reads "snapshot from 14:02 · not the live file".
    - Selecting text opens a floating composer (window glass, radius 17px): section heading, "L12–18", the quoted text, a comment box, attachments, and "⌘↵ add · Esc close".
    - Commented blocks get a 3px `--brand` left bar. Hovering a comment highlights its exact range in `--brandBg`.
    - Extra agent files appear as a thumbnail row, with "not captured" in a dashed chip.
  - **Right sidebar:** review (320px): comments sorted by position (jump, edit, delete with confirm), review-level attachments, and a footer with the actions:
    - gate: **Approve** (warns when unsent comments exist) / **Request changes (N)**
    - options ping: option buttons; comments ride along as the note
    - other pings: **Send review (N)**
    - A "Draft saved" caption (drafts persist locally; attachments must be re-picked, and the UI says so).
  - **Answered:** read-only, with the answer banner and the document "as reviewed".
- **Curation (a gate over a review document):** when the primary file a gate shows is typed `review_threads` (the reviewer's draft) or `review_replies` (a round's answers) — the engine says so in `file_types`, the page never sniffs — the same three-column frame opens as the curation page instead of the document workbench. The documents come decoded from `/api/pings/:id/curation`, as the gate showed them.
  - **Left:** the files shown; the `mr_threads` file beside the review opens read-only.
  - **Center:** a toolbar — an indeterminate checkbox "{kept} of {total} posted", All / None / Drop nits — then one card per note or reply: its keep checkbox (everything starts kept; unpicking is the deliberate act), a kind pill (blocking / suggestion / question / nit, or fixed / declined / answered for a reply), the mono `path:line`, the posted text in a growing textarea with a "reworded" pill and "Restore as written", a "continues / answers a thread" pill that unfolds the thread's comments (new ones and apothecary's own marked), "Resolve the thread" for a reply, and a folded "Why the agent says so — never posted" holding the rationale, evidence and example. Dropped cards dim.
  - **Right sidebar:** the ask, the summary editor (posted; reworded pill + restore), the notes a person dropped earlier in the run, the feedback box for the agent, attachments, and the actions: **Approve — post N** (disabled while a kept text is blank) / **Request changes** (needs feedback). A file not captured when the question was raised can only be approved as written, and the page says so.
  - The answer carries decisions, not a rewritten file (`curated`: dropped items, reworded text, changed resolve flags, a reworded summary). The ping card's inline actions are "Curate & post →", "Approve as written" and "Request changes…".
  - **Answered:** the tally ("posted 7 of 9 · 2 dropped · 1 reworded"), each item kept / dropped (struck through) / reworded with its as-written text, the summary as posted.

### 7.6 Tickets: `/g/:guild/tickets`, `/new`, `/:id`

- **List:** search (title, key, goal) and filters (workshop, source, has-active-run).
  - Columns: title, source key tag, workshop, base branch (mono), **derived status** (latest run state chip, or "no runs"), created.
- **New ticket:** a two-column form:
  - **Left:** Workshop (select, locked after create), Title, Goal (large textarea, markdown), Base branch (auto-slugged, mono, with "edited" when overridden), Attachments.
  - **Right card "Import from tracker":** integration select, then search (key / words / `jql:`), then a result list (key, title, type, status, assignee). Picking one prefills the form and lists the attachments to copy, with "Open in tracker ↗" and "Clear". Skipped attachments show a warning list + Continue.
- **Ticket detail:**
  - Header: title + source tag + workshop link + branch; Edit / Delete… on the right.
  - **Left:** the goal and attachments ("delivered into every run's workspace"), then the runs table for this ticket (polls every 4s).
  - **Right:** a "Start a run" card: direction (textarea), run-only files, flow picker, source-branch picker (default marked ★), and a primary "Start run".

### 7.7 Workshop overview: `/g/:guild/w/:workshop`

1. Header: vial + name + repo URL (mono) + repository / provisioner / harness chips; "Edit setup" secondary, Delete… (blocked until all runs are archived, with an explanation), then the open tab's main action, if it has one (New run, New ticket, New flow, New automation).
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
   - Validate, which shows a result popover: stage count, hash, errors (ros), binding warnings (sky)
   - Save (primary, disabled until dirty, with an "Unsaved changes" sky dot)
   - Clone, Delete… or "Reset to built-in"
2. **Body:** row [canvas 1fr | inspector 450px].
   - **Canvas:** Svelte Flow on a transparent background over the scene, with a faint dot grid (`--ln` 1px, 20px).
     - **Nodes:** content glass, radius 14px, with a 3px top accent in the stage-type hue.
       - Head: type chip + mono id, plus an ENTRY tag (sky outline) on the first stage, plus a detail line (agent name / command / action / signal / "gate" / the sequence it runs).
       - Rows: inputs (bound grn dot, unbound sky hollow, sentinel vio `$DIFF_TARGET`) and outputs with data dots. Dataflow lines join dots within a chain only.
     - **Chains**, one row each under their section's label (main / watchers / outcomes), each opened by a marker: the main chain's start (brand, solid, *main · operates on a ticket*), a watcher's (vio, dashed), an outcome's (grn or ros, the end it says).
     - **The hub** (tea, solid, display *waiting · on the merge request*): once a flow has watchers or outcomes, the main chain's end. It stands left of the marker column below the main chain; one dashed tea line runs down from it to every watcher's and outcome's marker. A flow without a main chain starts at it.
     - **Frames**: each watcher and outcome is a sub-run in a faint wash of its marker's hue, with its own ends at the end of its row (*end* back to waiting; *done* / *abort* for an outcome) — nothing crosses the canvas to a shared terminal. The frame's corner (▾ / ▸) folds the chain to its marker (which then counts its stages). Dragging a marker moves its whole chain.
     - Start / End / Done / Abort are display pills (End dashed tea, Done dashed grn, Abort dashed ros); the debug leaf sits beside each stage that escalates to it.
     - Handles and edges as in 3.6.
     - Bottom-left controls: zoom, fit, lock, auto-arrange. Node positions autosave silently.
   - **Sequence pane**: selecting a sequence stage, or a sequence in the inspector, splits the canvas — the sequence on the right, its stages top to bottom, its two ways out (*back · …*, *fail · …*). Its stages and edges are edited in the inspector like any other; a sequence stage inside opens its sequence in place, with a breadcrumb.
   - **Inspector** (window glass) — everything the YAML can say:
     - With nothing selected: description, what each run operates on (*Operates on*: a ticket or a merge request), parameters (the six the engine reads, each with an optional description and allowed values, then the flow's own), the agents and the sequences (one chip each, + agent / + sequence), + main chain (when there is none) / + watcher / + outcome, and a stage of each kind for the main chain.
     - Chain marker selected: the main chain — what each run operates on; a watcher or an outcome — id, description, its signal (or *by hand*, for a watcher), start policy (or an enum `$param`), quiet for (debounced signals), runs per merge request (watcher) or then (outcome), and **Runs**: its own stages or a sequence (`sequence: X`). Each panel adds a stage of each kind to its chain, wired after the last.
     - Stage selected: id (rename updates every reference), its chain (or the sequence it is in), condition, description, per-type fields, and its **routes** — one row per link, all in the same shape: the route's kind in its hue (`success`, `on_fail`, `on_timeout`, `on_reject`; `then` for a fallback), where it goes and, for a failure link to a stage, ×N (or ∞ where a person decides: from a gate, or to one); "+ then" adds a fallback, × removes a link, "+ on_timeout" a route the stage has none of yet. Per-type fields:
       - agent: name autocomplete, *Edit agent* (or *Declare agent* for a name the flow lacks), overrides, input bindings, output paths, verdict note
       - command: run_ref or literal script, timeout, reads, writes, **each_repo** toggle
       - gate: question, shows (artifact / `$WORKSPACE` / `$MR_URL`), auto-approve / auto-reject signals
       - action: registry picker, description, required integration, inputs (an enum input also offers fitting `$params`) / outputs
       - wait: signal picker, timeout (empty = ∞)
       - sequence: what it runs (a sequence, or `main` from another chain), *Edit sequence*
     - Inside a sequence, a stage routes to the sequence's stages, the ends, and `fail` (the sequence stage's on_fail); saying nothing goes on to the next stage, or out.
     - Edge selected (on the canvas, or by its label): its stage, that route picked out and scrolled to.
     - Agent selected: name, description, read-only, allow shell, prompt, inputs (name, description, optional), outputs (name, path, type, description), the stages that run it, delete.
     - Sequence selected: id, description, its stages (reorder ↑ ↓), add a stage of each kind, the stages that run it, delete (once nothing does).
     - Delete what is selected: inspector button **or** ⌫ / Del key. A rename the document refuses puts the field back.
     - **Back**: a panel opened from another — *Edit agent*, *Edit sequence*, a sequence's stage, *Run by*, the flow panel's chips, a stage just added — offers "← stage build" (or agent, sequence, watcher, *The formula*) at its top, as many steps back as it was opened through; renames carry along, deleted panels are skipped. Selecting on the canvas starts afresh.
   - **YAML tab:** the document in a code editor (CodeMirror), in the theme's hues — keys, comments, a `$parameter` (brand), a `$SENTINEL` (vio). Checked as you type against the flow's JSON Schema (an unknown key, a stage type, a signal, a start policy, an action's input) and by the parser (what the schema cannot say), each problem once, underlined on its line with a dot in the gutter; keys and values complete from the schema (Ctrl+Space), hovering a key shows what it is. Loaded with the tab, never with the graph.
   - **Versions:** a drawer from the header (list, current marked, "Load into editor").
   - Leaving with unsaved changes triggers a confirm modal.

### 7.9 Workshop setup: `/g/:guild/w/:workshop/settings` (create and edit)

A stepped form with a left step-rail and section anchors: **1 Repository → 2 Workshop → 3 Execution → 4 Workspace**. Sticky footer: Cancel / Save (primary). A "Run envcheck after saving" toggle.

1. **Repository:**
   - repository account select
   - "Browse repositories" (searchable list; each row has "Use" and "+ extra")
   - URL, locator, default branch, AGENTS.md path
   - additional repos as an editable table (name, URL, locator: empty = read-only, branch), with the note "cloned to ~/repos/\<name\>, one MR each"
2. **Workshop:** name; commands as a key → script table, each run only by a flow's `run_ref`; default flow.
3. **Execution:** provisioner select; harness integrations as a multi-select (one per harness kind).
4. **Workspace:**
   - Egress mode segmented control **Allowlist / Denylist**, with an explanation under each. The two lists are kept separately.
   - Domain chip input; "+ Add sensible defaults" (allowlist); a note that force-blocked hosts are managed from the run's Egress window.
   - code-server extensions (chip input).
   - Editor defaults: theme select (15), font size, tab size, format on save, word wrap, trust workspace.

### 7.10 System hub: `/g/:guild/system/*`

Layout: row [section nav 220px | content], both full-height windows. The section nav is a vertical list in the sidebar nav style:
**Integrations · Channels · Account · Users (admins) · Limits · Reaper · Storage**. Everything but Users and Limits is the signed-in user's own.

- **Integrations:** four groups (Repositories, Provisioner, Harness, Ticket source). **+ New integration** in the top bar lists every kind by group, and the form opens in that group.
  - **Integration card** (content glass): name, kind chip, config summary (secrets masked), **persisted test result** chip (✓ ok / ✕ error + "tested 3h ago"), Test (secondary), Edit (inline expand), Delete….
  - Forms are generated from descriptors: a "How to set this up" help panel, then fields (text / secret / JSON secret).
  - A skeleton list shows while loading.
- **Channels** (folded in from the old page): Matrix, Slack and Discord, as one-way mirrors ("Pings are always answered in Apothecary"). Each card has Test, Edit and Remove… (confirm).
- **Account:** three panes — Profile (handle, name, email; which methods the account signs in with), Password (current, new twice; every other session is signed out), Personal tokens (name + "Mint a token"; the secret shows once in a sky callout with Copy, then a row per token with prefix, created/last used, Revoke).
- **Users (admins only):** a table of avatar, name + `@handle`, email, role pill (Admin / Member, plus Disabled), last seen, and per-row actions: Make/Remove admin, Disable…/Enable, Reset password… (inline field). "+ Add account" opens an AddCard: handle, name, email, initial password, Admin checkbox. Disabling says exactly what happens (signed out, tokens revoked, runs aborted, automations off; workshops and history stay).
- **Limits:** max concurrent runs and max environments for the whole instance ("no cap" allowed), with the current counts beside them. An admin edits them in place (number + "No cap" toggle, sticky save bar); everyone else sees them read-only.
- **Reaper:** destroy completed environments after a duration; destroy abandoned environments (`never` only today, shown disabled with a reason).
- **Storage:** sweep unclaimed uploads after a duration; expire snapshot content after a duration or never.
- **Duration inputs:** a number + unit select (m / h / d / never) replaces free text. Show "Applies immediately — no restart".

---

## 8. Interaction and keyboard

| Context | Keys |
|---|---|
| Global | ⌘K command palette · ⌘\\ collapse sidebar · G then I / R / T / D to go to Inbox / Runs / Tickets / Desk |
| Composer | ↵ send (queued until turn ends) · ⇧↵ newline |
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
- **Backdrop:** one `Noise` component (section 2.2): one fragment shader on one canvas. The `noise` store holds its live parameters, `blue` (0–100) and `speed`; everything else is fixed in the component.
- **Themes:** generate the `[data-theme=…]` blocks from `themes.json`. The theme toggle in the user row becomes a theme picker (7 themes).
- Backdrop blur is expensive, and it is more so over an animated background. Windows are the only blurred layer; islands and popovers are opaque. Avoid blur on scrolling content.
- Anything that animates forever runs in `steps()`, however small it is. A smooth endless animation has the compositor redraw every window's blur at the display's rate: one pulsing status dot cost more than the whole backdrop until it was stepped.
