# i18n

`t(key, params?)` from `$lib/i18n/index.svelte`; English catalog in
`locales/en/<area>.ts` (flat dotted keys prefixed by area, registered in
`locales/en/index.ts`). Keys are type-checked (`npm run check`).

- Params: `{name}` placeholders. Plurals: `key_one` / `key_other` + `t(key, { count })`.
- Markup in a message (`<b>`, `<code>`): `th()` + `{@html}` — params are escaped.
- Never call `t()` at module scope; wrap in a function so it follows the locale.
- Adding a locale: `locales/<lc>.ts` exporting a `Partial` of the English keys,
  then register it in `LOCALES`. Missing keys fall back to English.
- Not translated on purpose: API/user data, ids, CLI/code literals, status
  wire values, keyboard glyphs.
