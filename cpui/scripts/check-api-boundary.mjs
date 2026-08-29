// The ACL is only a boundary if nothing crosses it.
//
// The generated client speaks the engine's vocabulary, not ours. Anything
// outside src/lib/api/ that imports it has skipped the mappers — and with them
// the narrowing, the renaming, and the single place an engine change lands.
//
// Run by `task api:check`.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('..', import.meta.url));
const SRC = join(ROOT, 'src');
const ALLOWED_PREFIX = join(SRC, 'lib', 'api');

/** Every import specifier in a file, however it is written. */
const IMPORT_RE = /(?:import|export)[^'"`]*?from\s*['"]([^'"]+)['"]|import\s*\(\s*['"]([^'"]+)['"]\s*\)/g;

function* walk(dir) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) yield* walk(p);
    else if (/\.(ts|js|svelte)$/.test(p)) yield p;
  }
}

const offenders = [];
for (const file of walk(SRC)) {
  if (file.startsWith(ALLOWED_PREFIX)) continue;
  const text = readFileSync(file, 'utf8');
  for (const m of text.matchAll(IMPORT_RE)) {
    const spec = m[1] ?? m[2] ?? '';
    if (spec.includes('api/generated') || spec.includes('generated/sdk') || spec.includes('generated/types')) {
      offenders.push(`${relative(ROOT, file)} imports ${spec}`);
    }
  }
}

if (offenders.length) {
  console.error('The generated API client must not be imported outside src/lib/api/:\n');
  for (const o of offenders) console.error('  ' + o);
  console.error('\nGo through $lib/api (services) and $lib/domain (types) instead.');
  console.error('See cpui/src/lib/api/README.md.');
  process.exit(1);
}
console.log('api boundary: clean');
