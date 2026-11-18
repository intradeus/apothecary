// A flow the editor opens and writes back must still be the flow.
//
// The editor never edits YAML: it reads the engine's document (the `doc` of
// /api/flows/validate), and what it validates, saves or copies is that
// document written out again. This script is that trip for every document in
// a directory, with nothing changed in between: <name>.doc.json, as the engine
// serves it, becomes <name>.editor.yaml, as the editor writes it.
//
// Run by `task flows:check`, between cmd/flowcheck's two halves: the engine
// writes the documents, and reads the YAML back.
import { readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { docToYaml, normalizeDoc } from '../src/lib/domain/flowdoc.ts';

const SUFFIX = '.doc.json';

const dir = process.argv[2];
if (!dir) {
	console.error('usage: node scripts/flows-through-editor.mjs <dir>');
	process.exit(2);
}
const docs = readdirSync(dir)
	.filter((f) => f.endsWith(SUFFIX))
	.sort();
if (docs.length === 0) {
	console.error(`flows-through-editor: no *${SUFFIX} in ${dir} (flowcheck docs writes them)`);
	process.exit(1);
}
for (const file of docs) {
	const doc = normalizeDoc(JSON.parse(readFileSync(join(dir, file), 'utf8')));
	writeFileSync(join(dir, file.slice(0, -SUFFIX.length) + '.editor.yaml'), docToYaml(doc));
}
console.error(`flows-through-editor: ${docs.length} flows written as the editor writes them`);
