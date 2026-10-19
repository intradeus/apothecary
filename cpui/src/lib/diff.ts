// Parsers that turn diffs and files into CodeView lines (DESIGN 4.10).
// Rendering lives in $lib/ui/CodeView; this only decides what each line is.

import type { CodeLine } from '$lib/ui/CodeView.svelte';

const GIT_HEADER = [
	'diff --git',
	'index ',
	'new file',
	'deleted file',
	'similarity',
	'rename ',
	'--- ',
	'+++ '
];

/** A unified diff (git's view) → lines with old/new numbers, for a
 *  two-gutter CodeView. File headers are dropped; hunks and "\ No newline"
 *  markers stay. */
export function parseUnified(d: string): CodeLine[] {
	const out: CodeLine[] = [];
	let o = 0;
	let n = 0;
	for (const raw of d.split('\n')) {
		if (GIT_HEADER.some((h) => raw.startsWith(h))) continue;
		const h = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/.exec(raw);
		if (h) {
			o = Number(h[1]);
			n = Number(h[2]);
			out.push({ kind: 'hunk', text: raw });
		} else if (raw.startsWith('+')) out.push({ kind: 'add', n: n++, text: '+' + raw.slice(1) });
		else if (raw.startsWith('-')) out.push({ kind: 'del', old: o++, text: '−' + raw.slice(1) });
		else if (raw.startsWith('\\')) out.push({ kind: 'meta', text: raw });
		else if (raw.length || out.length)
			out.push({ kind: 'ctx', old: o++, n: n++, text: ' ' + raw.slice(1) });
	}
	while (out.length && out.at(-1)!.kind === 'ctx' && !out.at(-1)!.text.trim()) out.pop();
	return out;
}

/** An agent's own edit (old text → new text, not git's view) as −old/+new
 *  lines around the changed middle, the unchanged head and tail folded into
 *  one "… N unchanged lines" each. */
export function editLines(d: { oldText?: string; newText: string }): CodeLine[] {
	const out: CodeLine[] = [];
	const oldL = d.oldText ? d.oldText.split('\n') : [];
	const newL = d.newText.split('\n');
	let head = 0;
	while (head < oldL.length && head < newL.length && oldL[head] === newL[head]) head++;
	let tail = 0;
	while (
		tail < oldL.length - head &&
		tail < newL.length - head &&
		oldL[oldL.length - 1 - tail] === newL[newL.length - 1 - tail]
	)
		tail++;
	const fold = (k: number) => `… ${k} unchanged line${k > 1 ? 's' : ''}`;
	if (head > 0) out.push({ kind: 'meta', text: fold(head) });
	oldL
		.slice(head, oldL.length - tail)
		.forEach((l, i) => out.push({ kind: 'del', n: head + i + 1, text: '−' + l }));
	newL
		.slice(head, newL.length - tail)
		.forEach((l, i) => out.push({ kind: 'add', n: head + i + 1, text: '+' + l }));
	if (tail > 0) out.push({ kind: 'meta', text: fold(tail) });
	return out;
}

/** +added / −removed line counts of a set of lines. */
export function lineCounts(lines: CodeLine[]): { add: number; del: number } {
	let add = 0;
	let del = 0;
	for (const l of lines) {
		if (l.kind === 'add') add++;
		else if (l.kind === 'del') del++;
	}
	return { add, del };
}

/** A plain file → numbered lines. */
export const fileLines = (content: string): CodeLine[] =>
	content.split('\n').map((text, i) => ({ n: i + 1, text }));
