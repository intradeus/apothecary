#!/usr/bin/env node
// The theme contract: every theme in src/lib/themes.ts has a
// `:root[data-theme='<id>']` block in src/styles/tokens.css, and every block defines
// every themed token (the set the default Ember × Sky block defines). A theme that
// forgets one would silently inherit the default value, which is the bug this
// catches: a light theme with a dark-mode scrim, say.
//
//   npm run check:themes

import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const root = join(dirname(fileURLToPath(import.meta.url)), '..', 'src');
const css = readFileSync(join(root, 'styles', 'tokens.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
const reg = readFileSync(join(root, 'lib', 'themes.ts'), 'utf8');

/** The custom properties a rule body declares. */
const tokens = (body) => new Set([...body.matchAll(/(--[\w-]+)\s*:/g)].map((m) => m[1]));

/** Every `:root[data-theme='x']` block (a selector list counts for each id). */
const blocks = new Map();
for (const m of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
	const ids = [...m[1].matchAll(/:root\[data-theme='([\w-]+)'\]\s*(?:,|$)/g)].map((x) => x[1]);
	for (const id of ids) blocks.set(id, tokens(m[2]));
}

const registered = [...reg.matchAll(/\btheme\('([\w-]+)'/g)].map((m) => m[1]);
const contract = blocks.get('ember-sky');
const problems = [];

if (!contract) problems.push("tokens.css has no :root[data-theme='ember-sky'] block (the contract)");
for (const id of registered) {
	const b = blocks.get(id);
	if (!b) {
		problems.push(`theme '${id}' is registered in themes.ts but has no block in tokens.css`);
		continue;
	}
	const missing = [...(contract ?? [])].filter((t) => !b.has(t));
	if (missing.length) problems.push(`theme '${id}' does not define: ${missing.join(', ')}`);
	const extra = [...b].filter((t) => !contract?.has(t));
	if (extra.length) problems.push(`theme '${id}' defines tokens the default block lacks: ${extra.join(', ')}`);
}
for (const id of blocks.keys())
	if (!registered.includes(id)) problems.push(`tokens.css block '${id}' is not registered in themes.ts`);

if (problems.length) {
	console.error('Theme contract broken:\n' + problems.map((p) => `  - ${p}`).join('\n'));
	process.exit(1);
}
console.log(`themes ok: ${registered.join(', ')} (${contract.size} themed tokens each)`);
