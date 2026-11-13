#!/usr/bin/env node
// The import graph IS the architecture. Each folder of src/lib has a rank, and
// a module may only import from its own folder or from a lower rank. So nothing
// generic ever learns about a feature, and two features can't depend on each
// other: a cycle is unrepresentable. Why each folder sits where it does:
// docs/frontend-standards.md.
//
//   npm run lint:layers

import { readdirSync, readFileSync, statSync, existsSync } from 'node:fs';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('..', import.meta.url));
const SRC = join(ROOT, 'src');
const LIB = join(SRC, 'lib');

// Lowest first. Folders on one line are peers: neither may import the other.
const LAYERS = [
	// Foundations: no UI, no I/O.
	['i18n', 'domain'],
	['design'],
	['core'],
	['api'],
	// The design system, then what every page shares.
	['ui'],
	['app'],
	// Features, in dependency order.
	['artifacts', 'desk', 'flow', 'skills', 'system', 'analytics', 'notices'],
	['pings'],
	['review'],
	['curation'],
	['tickets'],
	['run'],
	['mrs'],
	['workshop'],
	['desk-home', 'inbox', 'shell', 'swarm']
];
const rank = new Map(LAYERS.flatMap((peers, i) => peers.map((name) => [name, i])));

const IMPORT = /(?:from\s+|import\s*\(\s*|import\s+)['"]([^'"]+)['"]/g;

function* walk(dir) {
	for (const name of readdirSync(dir)) {
		const p = join(dir, name);
		if (statSync(p).isDirectory()) {
			if (name !== 'generated') yield* walk(p);
		} else if (/\.(ts|svelte)$/.test(p) && !p.endsWith('.test.ts')) yield p;
	}
}

/** The src/lib folder a path belongs to, or 'routes' above them all. */
const folderOf = (path) => {
	const rel = relative(LIB, path);
	if (rel.startsWith('..')) return 'routes';
	const [first, ...rest] = rel.split('/');
	return rest.length || rank.has(first) ? first : `(${rel})`;
};

const problems = [];
for (const file of walk(SRC)) {
	const from = folderOf(file);
	if (from === 'routes') continue; // routes may import any layer

	if (from.startsWith('(')) {
		problems.push(`${relative(ROOT, file)}: no loose files in src/lib, it belongs to a layer`);
		continue;
	}
	if (!rank.has(from)) {
		problems.push(`${relative(ROOT, file)}: folder '${from}' has no layer in check-layers.mjs`);
		continue;
	}
	for (const m of readFileSync(file, 'utf8').matchAll(IMPORT)) {
		const spec = m[1];
		let target;
		if (spec.startsWith('$lib/')) target = join(LIB, spec.slice(5));
		else if (spec.startsWith('.')) target = resolve(dirname(file), spec);
		else continue;
		if (!target.startsWith(LIB) && existsSync(target.replace(/\?.*$/, ''))) continue; // assets
		const to = folderOf(target);
		if (to === from) continue;
		if (to === 'routes') {
			problems.push(`${relative(ROOT, file)}: imports a route (${spec})`);
			continue;
		}
		if (!rank.has(to) && !to.startsWith('(')) continue;
		if (!(rank.get(to) < rank.get(from)))
			problems.push(`${relative(ROOT, file)}: ${from} may not import ${to} (${spec})`);
	}
}

if (problems.length) {
	console.error(`Layers broken (${problems.length}):\n` + problems.map((p) => `  ${p}`).join('\n'));
	console.error('\nSee docs/frontend-standards.md.');
	process.exit(1);
}
console.log(`layers ok: ${rank.size} folders in ${LAYERS.length} ranks`);
