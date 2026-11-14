# The Dark theme darker, with a dark ping card (2026-11-14)

- **Why:** the Dark theme read as dark grey rather than dark, and its solid
  ping card (`PingCard`'s `.attention-card`, docked above the run composer
  and in the inbox) was a light-grey slab (#c0c3ca) with near-black text:
  the brightest thing on the screen. The task's owner asked for both to be
  darker and confirmed the scope: the Dark theme only, and the "ping pane"
  is that solid card.
- **The card's fill and ink moved from tier 3 to the theme blocks.**
  `--attention-fill`, `--ink` and `--on-ink` used to be derived once in the
  roles tier (`--attention`, `--on-brand`, `--attention` mixed 45% with
  white), so no theme could pick its own card. Every theme block now
  declares them; the six other themes write the same derivation against
  their own palette (`--sky`, `--on-brand`, `--sky` 45% with white), so they
  render exactly as before. The declarations left tier 3 entirely: it comes
  later in `themes.css` at equal specificity and would override the blocks.
  `check-themes` takes its token set from the Ember × Sky block, so the trio
  joined the contract with no script change.
- **Only Dark's card inverts.** It is graphite (#1b1c20) with soft-white ink
  (#e6e7ea); `--on-ink`, the label on the light `.btn-hot` and the "?"
  mark, is the fill colour. `.attention-card` derives its muted, faint,
  fill, line and code colours from `--ink`, so fields, `.card-in` refs and
  the outline button follow without touching `components.css` or
  `PingCard`. The `--attention-glow` halo, still from `--sky`, keeps the card
  standing out from the glass. Small solid "needs you" markers outside the
  card (`.count.hot`, `.pill.tint-hot`) read `--attention` and `--on-brand`
  and stay light grey on purpose: the ask named the pane only.
- **The rest of Dark:** a near-black frame (#040405) and island (#0b0b0d),
  deeper glass, inset, code and scrim, a softer specular, and a slightly
  lower `--line-hue` so hairlines keep their weight on the darker base. The
  backdrop's `--noise-ember` and `--noise-sky` are dimmed literal hexes
  (`Noise.svelte` parses them as hex) with less grain: its screen blend
  lightens the frame, so the full palette would undo the dark. The palette
  itself and the text tokens are unchanged.
- **Tests:** `cpui/src/lib/design/themes.test.ts`, "the Dark theme reads",
  parses the Dark block and holds WCAG contrast at 4.5:1 or more for
  `--text`, `--text-muted` and `--text-faint` over `--frame` and
  `--island-bg`; for the card's ink at 100%, 74% and 62% (how the card mixes
  its text, muted and faint) over `--attention-fill`; and for `--on-ink`
  over `--ink`. A token that is not a literal hex fails by name.
  `npm run check:themes` holds every block to the trio.
