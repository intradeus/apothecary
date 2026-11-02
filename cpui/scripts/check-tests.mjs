#!/usr/bin/env node
// Every component and module has its tests beside it: Foo.svelte → Foo.test.ts,
// foo.ts → foo.test.ts, foo.svelte.ts → foo.svelte.test.ts. Coverage says how
// much ran; this says nothing was skipped, so a new component can't arrive
// untested.
//
// It also fails on two paths in src/ that differ only by case. macOS and
// Windows can't tell them apart, so the second overwrites the first or an
// import resolves to the wrong one. That includes import specifiers:
// `./foo.svelte` names both foo.svelte.ts and Foo.svelte, so a rune module
// beside its component needs its own name (noiseState.svelte.ts beside
// Noise.svelte), and so does its test.
//
// A module with only types has nothing to run, so it needs no test. Anything
// else that can't be tested usefully goes in EXEMPT, with the reason.
//
//   npm run lint:tests

import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('..', import.meta.url));
const SRC = join(ROOT, 'src');
const LIB = join(SRC, 'lib');

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

/** Every file under dir, generated code and locales included. */
function* all(dir) {
	for (const name of readdirSync(dir)) {
		const p = join(dir, name);
		if (statSync(p).isDirectory()) yield* all(p);
		else yield p;
	}
}

// Names that differ only by case overwrite each other on macOS and Windows.
// A file is keyed by the specifier that imports it (`.ts`/`.js` dropped), and
// by the test it will need, so neither the files nor their tests can collide.
const seen = new Map();
const clashes = [];
/** Claims `name` for `file`; a name differing only by case is a clash. */
const claim = (name, file) => {
	const key = name.toLowerCase();
	const other = seen.get(key);
	if (other && other.name !== name) clashes.push(`src/${other.file} and src/${file} (${name})`);
	seen.set(key, { name, file });
};
for (const file of all(SRC)) {
	const rel = relative(SRC, file);
	claim(rel, rel);
	claim(rel.replace(/\.(ts|js)$/, ''), rel);
}
for (const file of walk(LIB)) {
	const rel = relative(SRC, file);
	claim(rel.replace(/\.ts$|\.svelte$/, '.test.ts'), rel);
}
if (clashes.length) {
	console.error(
		'Paths in src/ differ only by case; rename one (see docs/frontend-standards.md):\n  ' +
			clashes.join('\n  ')
	);
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
