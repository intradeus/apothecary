# Frontend standards

How `cpui/` is built, and the rules that keep it that way. The visual spec is [DESIGN.md](UX/UI/DESIGN.md); the catalogue of shared classes and components is [cpui/src/lib/ui/README.md](../cpui/src/lib/ui/README.md).

Almost everything here is enforced. Run `npm run lint` in `cpui/`, or `task ui:lint` from the root (svelte-check, every lint, then the production build); CI's ui-lint job runs exactly that.

## Architecture

The import graph is the architecture. Each folder of `src/lib` has a rank, and a module may import only from its own folder or from a lower rank. `scripts/check-layers.mjs` holds the table, so a cycle between two features can't be written, and nothing generic can learn about a feature.

| Rank | Folder | What lives there |
|---|---|---|
| 0 | `i18n`, `domain` | Messages. Domain types and pure domain logic, including the flow document model (`domain/flowdoc`). No UI, no I/O. |
| 1 | `design` | The design vocabulary as data: `hues`, `icons`, `tokens`, the `themes` registry. |
| 2 | `core` | Pure helpers everything shares: `format`, `urls`, `markdown`, `ping`, `errors`. |
| 3 | `api` | The anti-corruption layer around the generated client (see its README). |
| 4 | `ui` | The design system: generic components. May not know what a run is called on the wire. |
| 5 | `app` | What every page shares: `session`, `fleet`, `prefs`, `nav`. |
| 6+ | features | `artifacts`, `desk`, `flow`, `skills`, `system`, `analytics`, then `pings`, `review`, `curation`, `tickets`, `run`, `mrs`, `workshop`, and finally `desk-home`, `inbox`, `shell`. |
| top | `src/routes` | Load and compose. |

**Where does a new file go?** Into the feature that owns it. No loose files in `src/lib`: the layer check fails on one. Used by two features? Move it down to the lowest folder both can import, rather than having one feature import the other.

**Routes stay thin.** A `+page.svelte` reads its params and composes. Anything with state or more than a screen of markup belongs to a feature (`$lib/run/RunPage` is the model).

**Size.** Aim for under 500 lines per component. Past that, split by section: the parent composes, each section takes what it needs through props. Pure code moves to a `.ts` beside it.

## Theming

Every colour is in one file: `src/styles/themes.css`. A retheme touches that file and nothing else.

This works because of three tiers:

1. **Fixed colours** are the same in every theme (shadows, the terminal).
2. **Theme blocks** give one set of neutrals and one raw palette per theme: `--ember`, `--sky`, `--gold`, `--green`, `--rose`, `--violet`, `--teal`. A block also picks the solid attention card's fill and ink: `--attention-fill`, `--ink` (its text) and `--on-ink` (text on an ink fill). Most themes use sky with dark ink; Dark uses a graphite card with light ink.
3. **Roles** say what the palette means. Components only ever use these.

| Role | Palette | Means |
|---|---|---|
| `--running` | ember | Something is brewing. |
| `--attention` | sky | A human must act. |
| `--warning` | gold | Gates, loop-backs, things to watch. |
| `--success` | green | Finished well, or healthy. |
| `--danger` | rose | Failure, destructive actions. |
| `--accent`, `--accent-2` | violet, teal | Categorical only, no meaning of their own. |
| `--muted`, `--brand` | neutrals | The quiet hue, and the brand. |

The palette is private. `check-tokens` fails if anything outside `themes.css` names `--ember`, writes a colour, or selects on `[data-theme]`. That rule is the point: the last reskin touched hundreds of files because components said `--amb` and `hue-amb`. Now they say `--warning`, and only one line knows what a warning looks like.

Every role has one family, so code can build the name from a `Hue`:

| Use | Name |
|---|---|
| Fills, dots, borders | `var(--warning)` |
| Text in the hue | `var(--warning-text)` |
| Tinted fill, border, halo | `var(--warning-bg)`, `-line`, `-glow` |
| Text class | `.hue-warning` |
| Tinted pill | `.pill.tint-warning` |
| Sets `--tone` for a wash or accent | `.tone-warning` |

**Common changes:**

- **Change a colour:** edit its value in the theme block. That includes the ping card's fill and ink (`--attention-fill`, `--ink`, `--on-ink`).
- **Add a theme:** copy a block in `themes.css`, add one line to `$lib/design/themes` and a `theme.<id>` label. `check-themes` holds every block to the same tokens.
- **Change what something means:** "warnings are amber now" is one line in tier 3. "Commands are teal now" is one line in `$lib/design/hues`.
- **Rename a palette colour:** rename it inside `themes.css`. No other file can have noticed.
- **Add a role:** add it to tier 3 and to the `Hue` type, then give it `.hue-*`, `.tint-*` and `.tone-*`. `check-tokens` lists what's missing.

## Styling

- **Tokens, not literals.** Font sizes, radii, z-index, durations and font families come from `tokens.css`. Layout px (paddings, gaps, a grid's widths) are free, since there's no spacing scale.
- **Name by role, never by look.** `--line`, `.attention-card`, `--font-display`. A name that says a colour or a typeface is the next rename.
- **Kebab-case** for tokens and classes.
- **`<style>` is for what's local.** If a rule is needed twice, promote it: a class in `components.css`, or a component if there's behaviour.
- **No `!important`.** Element defaults sit in `:where()`, so any class wins.
- **A hue is never a class of your own.** Don't write `.fill.success`. Use `.tone-success` and read `--tone`, so the class doesn't drift from the hue.
- **Inside glass, never glass.** See DESIGN 2.3.
- **The backdrop is never looked at directly.** It shows only in the gaps between windows and, blurred, through them. So every page body is one `.page.glass` window filling what the header leaves, on every page and at every screen size: short content leaves empty glass, never bare backdrop. A page with its own columns (the System hub, the inbox, the review workbench, the flow editor) makes each column a full-height window instead; dialogs and sheets leave for `<body>`. Tickets and Runs are the model: `PageHeader`, then `<div class="page glass">` holding the filters and the list. No script checks this yet, so it is a review item: a page component that wraps its content in a bare `<div>` with its own padding is the bug the Notices page shipped with. See DESIGN 7.

Five files in cascade order, described at the top of `src/app.css`. `themes.css` and `tokens.css` are the only ones that define tokens for the whole app.

## Components

A component earns its place with behaviour or structure. A wrapper that only renames a CSS class doesn't: `.field`, `.pane` and `.card-in` stay classes. Before writing one, check the [catalogue](../cpui/src/lib/ui/README.md).

`src/lib/ui` is generic. If a component needs `fleet`, the session or the API, it's a feature component.

- Svelte 5 runes only. `$props()` with an inline type, `$derived` for anything computed from props. Never copy a prop into `$state` to read it later.
- State that outlives a component goes in a `.svelte.ts` class in `app/` or the feature.
- Destructive actions use `confirmDialog()`; completions use `toast()`; failures use `ErrorCard` or `.error` next to the cause. Never `alert()` or `confirm()`.
- Every page handles its four states: loading (`Skeleton`, `Loading`), empty (`EmptyState`), error (`ErrorCard`), and content.

## Code

- **User-facing text** comes from `t()`. Keys are typed against the English catalogue.
- **`{@html}`** takes `th()` or `mdToHtml()` directly, nothing else. `th()` escapes its params, `mdToHtml()` sanitizes. `ConfirmHost` is the one marked exception.
- **URLs** come from `to.*` in `$lib/core/urls`. Never hand-write a path.
- **Engine access** goes through `$lib/api`. The generated client is never imported outside it.
- **Types:** no `any`. Type-only imports use `import type`.
- **Errors in a catch:** `errorMessage(e)` from `$lib/core/errors`.
- **Glyphs** come from `I` in `$lib/design/icons`.
- **Vocabulary:** see the lore list in DESIGN 1. `repository`, never "forge"; `sequence`, never "block"; `verdict`, never "decision".

## Tests

Every component and module has its tests beside it: `Foo.svelte` → `Foo.test.ts`, `foo.ts` → `foo.test.ts`, `foo.svelte.ts` → `foo.svelte.test.ts`. `check-tests` fails on a file without one, so a new component can't arrive untested. Run them with `npm test`, `npm run test:watch` or `task ui:test:run`; CI's ui-test job runs them as `task ui:test` (vitest with coverage), so the coverage floor in `vite.config.ts` fails a pull request, not only a local `task ui:test`.

No two names under `src/` may differ only by case: macOS's filesystem is case-insensitive, so `noise.svelte.ts` beside `Noise.svelte` resolves to the wrong file there. A rune module next to its component is named after what it exports instead (`noiseState.svelte.ts`, `headerSlot.svelte.ts`). `check-tests` enforces this too.

The stack is Vitest, Testing Library and jsdom. Shared pieces live in `src/test`, imported as `$test`: the jsdom stubs (`setup.ts`), `text()`, `deferred()` and `rect()` (`helpers.ts`), and domain fixtures such as `aRun({ state: 'failed' })` (`fixtures.ts`).

- **Test what a user or caller can see:** what renders for given props, what a click or key does, which callback or API call fires, and each state (loading, empty, error, disabled, busy). Start from the branches and the edge cases.
- **Query like a user:** by role, label or visible text. Assert on a class only when the class is the behaviour, such as `.tint-success` chosen from a prop.
- **No snapshots, and no test that only checks "it renders".**
- **Mock at the boundary:** `vi.mock('$lib/api', …)`, plus whatever jsdom can't run (xterm, CodeMirror, Svelte Flow). A stray `fetch` throws by design.
- **In-flight states use `deferred()`:** it holds a request open, which is the only way to test what happens during it.
- **Deterministic:** fake timers, never sleeps, and `vi.setSystemTime` for anything relative to now.
- **A fix starts with a failing test.** Write the test that reproduces the bug first; `run/header/actions.test.ts` is the model. A known bug nobody has fixed yet is an `it.fails` with a comment saying what's wrong, never a test bent to match it.

jsdom has no layout and no CSS, so tests can't see a rule that stopped applying. Tests cover behaviour; a visual change still needs a look in the browser.

## What checks what

| Command | Tool | Holds |
|---|---|---|
| `npm run check` | svelte-check | Types, a11y and Svelte warnings. |
| `npm test` | Vitest | The tests. |
| `npm run test:coverage` | Vitest | The tests, plus the coverage floor in `vite.config.ts`. Raise it when coverage rises, never lower it. |
| `npm run lint:code` | ESLint | No `any`, unused code, `{@html}`, `alert`, `console.log`. |
| `npm run lint:styles` | Stylelint | Literals where a token belongs, `!important`, naming. |
| `npm run lint:tokens` | `scripts/check-tokens.mjs` | Palette privacy, raw colours, `[data-theme]`, every `var()` resolves, every `Hue` has its family. |
| `npm run lint:layers` | `scripts/check-layers.mjs` | The rank table, no loose files. |
| `npm run lint:tests` | `scripts/check-tests.mjs` | Every component and module has a test beside it; no two names under `src/` differ only by case. |
| `npm run check:themes` | `scripts/check-themes.mjs` | Every theme block defines every token. |
| `npm run lint:format` | Prettier | Formatting. `npm run format` fixes it. |
| `task api:check` | `scripts/check-api-boundary.mjs` | The generated client stays behind `$lib/api`. |

`npm run lint` runs every lint and format check; `task ui:lint` adds `check` (svelte-check) and the production build, `task lint` adds the Go and repository lints (`api:check` among them), and `task check` adds the tests with coverage.

A disable comment needs a reason on the same line. Prefer changing the rule's config, with a comment saying why, when a rule is wrong for a whole category of code.
