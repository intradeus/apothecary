#!/usr/bin/env node
// The token contract: what keeps a retheme to one file.
//
//   1. The raw palette (--ember, --sky, …) is private to themes.css. Everything
//      else speaks in roles, so renaming a hue never reaches a component.
//   2. A colour is written in themes.css and nowhere else.
//   3. No [data-theme] rule outside themes.css (selecting on the attribute in
//      a component would make that component theme-aware).
//   4. Every var(--x) resolves: a token from tokens.css or themes.css, or a
//      variable the same file declares. A typo fails instead of rendering as
//      nothing, which is how the last reskin left dead names behind.
//   5. Every Hue in $lib/design/hues has its full token and class family.
//
// Mechanical rules with no project knowledge (px font sizes, z-index numbers,
// !important) are stylelint's: see stylelint.config.mjs.
//
//   npm run lint:tokens

import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('..', import.meta.url));
const SRC = join(ROOT, 'src');
const STYLES = join(SRC, 'styles');
const THEMES = join(STYLES, 'themes.css');
const TOKENS = join(STYLES, 'tokens.css');

const PALETTE = /--(ember(-2|-text)?|sky(-text)?|gold|green|rose(-text)?|violet|teal)(?![\w-])/g;
const COLOUR = /#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?|oklch|oklab|lab|lch)\(/g;
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, (c) => c.replace(/[^\n]/g, ' '));
const stripCode = (s) =>
	s
		.replace(/<!--[\s\S]*?-->/g, (c) => c.replace(/[^\n]/g, ' '))
		.replace(/(^|\s)\/\/.*$/gm, (c) => c.replace(/[^\n]/g, ' '));
const declared = (s) => [...s.matchAll(/(--[\w-]+)\s*:/g)].map((m) => m[1]);

function* walk(dir) {
	for (const name of readdirSync(dir)) {
		const p = join(dir, name);
		if (statSync(p).isDirectory()) {
			if (!['generated', 'assets', 'locales', 'test'].includes(name)) yield* walk(p);
		} else if (/\.(ts|svelte|css)$/.test(p) && !p.endsWith('.test.ts')) yield p;
	}
}

// The shared sheets count: .tone-* sets --tone for any component to read.
const sheets = ['base.css', 'components.css'].map((f) => readFileSync(join(STYLES, f), 'utf8'));
const defined = new Set(
	[readFileSync(THEMES, 'utf8'), readFileSync(TOKENS, 'utf8'), ...sheets].flatMap((s) =>
		declared(stripComments(s))
	)
);

const problems = [];
const report = (file, text, index, msg) => {
	const line = text.slice(0, index).split('\n').length;
	problems.push(`${relative(ROOT, file)}:${line}  ${msg}`);
};

for (const file of walk(SRC)) {
	const raw = stripCode(stripComments(readFileSync(file, 'utf8')));
	const themeFile = file === THEMES;
	if (!themeFile) {
		for (const m of raw.matchAll(PALETTE))
			report(file, raw, m.index, `palette token ${m[0]}: use a role (see themes.css, tier 3)`);
		for (const m of raw.matchAll(COLOUR))
			report(file, raw, m.index, `raw colour ${m[0]}: colours live in themes.css`);
		for (const m of raw.matchAll(/\[data-theme/g))
			if (!/data-theme=\{/.test(raw.slice(m.index, m.index + 14)))
				report(file, raw, m.index, 'theme selector: theme rules live in themes.css');
	}

	// Variables a component sets for itself: a declaration, or a style:--x directive.
	const local = new Set([
		...declared(raw),
		...[...raw.matchAll(/style:(--[\w-]+)/g)].map((m) => m[1])
	]);
	for (const m of raw.matchAll(/var\(\s*(--[\w-]+)(\s*,)?/g)) {
		const name = m[1];
		if (m[2] || defined.has(name) || local.has(name) || name.startsWith('--xy-')) continue;
		report(file, raw, m.index, `unknown token ${name}`);
	}
	for (const m of raw.matchAll(/\b(?:token|px|ms)\(\s*'(--[\w-]+)'/g))
		if (!defined.has(m[1])) report(file, raw, m.index, `unknown token ${m[1]}`);
}

// Rule 5: a Hue is a name other code builds on (`var(--${hue}-line)`, `.tint-${hue}`).
const hueSource = readFileSync(join(SRC, 'lib', 'design', 'hues.ts'), 'utf8');
const hues = [
	...hueSource
		.slice(
			hueSource.indexOf('export type Hue'),
			hueSource.indexOf(';', hueSource.indexOf('export type Hue'))
		)
		.matchAll(/'([\w-]+)'/g)
].map((m) => m[1]);
const hasClass = (c) => new RegExp(`\\.${c}(?![\\w-])`).test(sheets.join('\n'));
for (const hue of hues) {
	for (const t of [`--${hue}`, `--${hue}-text`, `--${hue}-bg`, `--${hue}-line`, `--${hue}-glow`])
		if (!defined.has(t)) problems.push(`src/lib/design/hues.ts  Hue '${hue}' has no token ${t}`);
	for (const c of [`hue-${hue}`, `tint-${hue}`, `tone-${hue}`])
		if (!hasClass(c)) problems.push(`src/lib/design/hues.ts  Hue '${hue}' has no class .${c}`);
}

if (problems.length) {
	console.error(
		`Token contract broken (${problems.length}):\n` + problems.map((p) => `  ${p}`).join('\n')
	);
	console.error('\nSee docs/frontend-standards.md.');
	process.exit(1);
}
console.log(`tokens ok: ${defined.size} tokens, ${hues.length} hues`);
