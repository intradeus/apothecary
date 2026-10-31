#!/usr/bin/env node
// Every component and module has its tests beside it: Foo.svelte → Foo.test.ts,
// foo.ts → foo.test.ts, foo.svelte.ts → foo.svelte.test.ts (so that foo.svelte.ts
// beside Foo.svelte can't collide on a case-insensitive filesystem). Coverage says how much ran; this says
// nothing was skipped, so a new component can't arrive untested.
//
// A module with only types has nothing to run, so it needs no test. Anything
// else that can't be tested usefully goes in EXEMPT, with the reason.
//
//   npm run lint:tests

import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('..', import.meta.url));
const LIB = join(ROOT, 'src', 'lib');

/** Path from src/lib → why it has no test. */
const EXEMPT = {};

function* walk(dir) {
	for (const name of readdirSync(dir)) {
		const p = join(dir, name);
		if (statSync(p).isDirectory()) {
			if (name !== 'generated' && name !== 'locales') yield* walk(p);
		} else if (/\.(ts|svelte)$/.test(p) && !p.endsWith('.test.ts') && !p.endsWith('.d.ts')) yield p;
	}
}

/** True when the file exports nothing that exists at runtime. */
const typesOnly = (text) =>
	!/^\s*export\s+(?!type\b|interface\b)/m.test(text.replace(/\/\*[\s\S]*?\*\//g, '')) ||
	/^(?:\s*(?:\/\/.*|export \* from .*|export type .*|import type .*))*\s*$/.test(text);

const missing = [];
for (const file of walk(LIB)) {
	const rel = relative(LIB, file);
	if (rel in EXEMPT) continue;
	if (file.endsWith('.ts') && typesOnly(readFileSync(file, 'utf8'))) continue;
	if (/(^|\/)index\.ts$/.test(rel) && !rel.startsWith('api/') && !rel.startsWith('i18n/')) continue; // a barrel
	const test = file.replace(/\.ts$|\.svelte$/, '.test.ts');
	if (!existsSync(test)) missing.push(rel);
}

// Names that differ only by case overwrite each other on macOS and Windows.
const seen = new Map();
const clashes = [];
for (const file of walk(LIB)) {
	const test = relative(LIB, file.replace(/\.ts$|\.svelte$/, '.test.ts'));
	const key = test.toLowerCase();
	if (seen.has(key)) clashes.push(`${seen.get(key)} and ${test}`);
	seen.set(key, test);
}
if (clashes.length) {
	console.error('Test names differ only by case; rename the module:\n  ' + clashes.join('\n  '));
	process.exit(1);
}

if (missing.length) {
	console.error(
		`No test beside ${missing.length} file(s):\n` + missing.map((m) => `  src/lib/${m}`).join('\n')
	);
	console.error('\nSee docs/frontend-standards.md.');
	process.exit(1);
}
console.log('tests ok: every component and module has a test beside it');
