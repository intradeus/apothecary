// The review page's views of a structured file (a gate's port type): a
// verdict as its decision, summary and comments; an mr_threads document as
// its threads, each comment quoted under its author (the notes dropped
// earlier are the reviewer agent's, as in the engine's brief, and are left
// out). Each is still a list of DocBlocks — markdown rendered the review
// page's way — and each block keeps the source lines of what it renders, so
// a comment on a rendered thread lands on that thread's lines of the file
// the next stage reads. A document that does not hold its type's shape —
// a repository file is read unchecked — is shown as written instead.
import { isMap, isScalar, isSeq, LineCounter, parseDocument, type Node, type YAMLMap } from 'yaml';
import { t } from '$lib/i18n/index.svelte';
import type { DocBlock } from '$lib/review/reviewDoc';

/** The port types with a view of their own. */
const STRUCTURED_VIEWS = ['verdict', 'mr_threads'];

/** A structured file's blocks, or null when its type has no view of its own
 *  or it does not hold that type's shape. */
export function structuredBlocks(src: string, type: string): DocBlock[] | null {
	if (!STRUCTURED_VIEWS.includes(type)) return null;
	const lc = new LineCounter();
	const doc = parseDocument(src, { lineCounter: lc });
	if (doc.errors.length || !isMap(doc.contents)) return null;
	const at = new Lines(src, lc);
	return type === 'verdict' ? verdictBlocks(doc.contents, at) : threadsBlocks(doc.contents, at);
}

// Source lines of a node: from its first character to its last one that is
// not whitespace (a block map's range runs on through its trailing newline).
class Lines {
	constructor(
		private src: string,
		private lc: LineCounter
	) {}
	start(n: Node | null | undefined): number {
		return n?.range ? this.lc.linePos(n.range[0]).line : 1;
	}
	end(n: Node | null | undefined): number {
		if (!n?.range) return 1;
		let e = n.range[1];
		while (e > n.range[0] + 1 && /\s/.test(this.src[e - 1])) e--;
		return this.lc.linePos(Math.max(n.range[0], e - 1)).line;
	}
	/** A key's whole pair: its key through its value. */
	pair(m: YAMLMap, key: string): [number, number] | null {
		const p = m.items.find((i) => isScalar(i.key) && i.key.value === key);
		if (!p) return null;
		return [this.start(p.key as Node), this.end((p.value as Node | null) ?? (p.key as Node))];
	}
}

const str = (v: unknown) => (typeof v === 'string' ? v : v == null ? '' : String(v));
const num = (v: unknown) => (typeof v === 'number' && v > 0 ? v : 0);
// A code span its text cannot close: a fence longer than any run of
// backticks inside, padded when the text starts or ends with one.
const code = (s: string) => {
	const fence = '`'.repeat(Math.max(0, ...(s.match(/`+/g) ?? []).map((r) => r.length)) + 1);
	const pad = s.startsWith('`') || s.endsWith('`') ? ' ' : '';
	return fence + pad + s + pad + fence;
};
const quoted = (s: string) =>
	s
		.trimEnd()
		.split('\n')
		.map((l) => `> ${l}`)
		.join('\n');
const block = (raw: string, heading: string, [startLine, endLine]: [number, number]): DocBlock => ({
	raw,
	heading,
	startLine,
	endLine: Math.max(startLine, endLine)
});

// ---- verdict ---------------------------------------------------------------

// The shape the engine reads (parseVerdict): APPROVE or REJECT, and each
// comment a remark or a {path?, line?, end_line?, quote?, body}, never blank.
function verdictBlocks(m: YAMLMap, at: Lines): DocBlock[] | null {
	const v = m.toJSON() as { verdict?: unknown; summary?: unknown };
	if (v.verdict !== 'APPROVE' && v.verdict !== 'REJECT') return null;
	const list = m.get('comments', true);
	if (list != null && !isSeq(list)) return null;
	const head = v.verdict === 'APPROVE' ? t('doc.view.approved') : t('doc.view.rejected');
	const out = [block(`## ${head}`, head, at.pair(m, 'verdict')!)];
	const summary = str(v.summary).trim();
	const sumAt = at.pair(m, 'summary');
	if (summary && sumAt) out.push(block(summary, head, sumAt));
	if (!isSeq(list) || !list.items.length) return out;
	const title = t('doc.view.comments', { n: list.items.length });
	const key = at.pair(m, 'comments')![0];
	out.push(block(`### ${title}`, title, [key, key]));
	for (const [i, item] of list.items.entries()) {
		const n = item as Node;
		const lines: [number, number] = [at.start(n), at.end(n)];
		const c = n.toJSON() as unknown;
		if (typeof c === 'string') {
			if (!c.trim()) return null;
			out.push(block(`**${i + 1}.** ${c}`, title, lines));
			continue;
		}
		if (!isMap(n)) return null;
		const o = c as Record<string, unknown>;
		if (!str(o.body).trim()) return null;
		const path = str(o.path);
		const line = num(o.line);
		const end = num(o.end_line);
		const where = !path
			? t('doc.view.wholeChange')
			: !line
				? `${code(path)} · ${t('doc.view.wholeOfFile')}`
				: end > line
					? `${code(path)} · ${t('doc.view.lines', { from: line, to: end })}`
					: `${code(path)} · ${t('doc.view.line', { n: line })}`;
		const parts = [`**${i + 1}.** ${where}`];
		if (str(o.quote).trim()) parts.push(quoted(str(o.quote)));
		parts.push(str(o.body));
		out.push(block(parts.join('\n\n'), path || title, lines));
	}
	return out;
}

// ---- mr_threads ------------------------------------------------------------

// The shape the engine reads (reviewfile.ParseThreads): a list of threads,
// each a mapping with its own id, its comments a list of mappings.
function threadsBlocks(m: YAMLMap, at: Lines): DocBlock[] | null {
	const list = m.get('threads', true);
	if (list != null && !isSeq(list)) return null;
	const out: DocBlock[] = [];
	const mr = str(m.get('merge_request')).trim();
	const mrAt = at.pair(m, 'merge_request');
	if (mr && mrAt) out.push(block(`${t('doc.view.mergeRequest')}: ${mr}`, '', mrAt));
	const threads = isSeq(list) ? list.items : [];
	if (!threads.length)
		out.push(block(t('doc.view.noThreads'), '', at.pair(m, 'threads') ?? [1, 1]));
	const ids = new Set<string>();
	for (const item of threads) {
		if (!isMap(item)) return null;
		const th = item.toJSON() as Record<string, unknown>;
		const id = str(th.id);
		if (!id || ids.has(id)) return null;
		ids.add(id);
		const path = str(th.path);
		const line = num(th.line);
		const loc = path ? code(line ? `${path}:${line}` : path) : t('doc.view.conversation');
		const marks = [
			t('doc.view.thread', { id }),
			str(th.kind),
			th.resolved === true ? t('doc.view.resolved') : '',
			th.ours === true ? t('doc.view.ours') : ''
		].filter(Boolean);
		const title = [loc, ...marks].join(' · ');
		const label = [
			path ? (line ? `${path}:${line}` : path) : t('doc.view.conversation'),
			...marks
		].join(' · ');
		const comments = item.get('comments', true);
		if (comments != null && !isSeq(comments)) return null;
		const cms = isSeq(comments) ? comments.items : [];
		if (!cms.every((cn) => isMap(cn))) return null;
		const headEnd = cms.length ? at.start(cms[0] as Node) - 1 : at.end(item);
		const hunk = str(th.hunk).trimEnd();
		const fence = '`'.repeat(Math.max(3, ...(hunk.match(/`{3,}/g) ?? []).map((r) => r.length + 1)));
		const head = hunk ? `### ${title}\n\n${fence}diff\n${hunk}\n${fence}` : `### ${title}`;
		out.push(block(head, label, [at.start(item), Math.max(at.start(item), headEnd)]));
		for (const cn of cms) {
			const c = (cn as Node).toJSON() as Record<string, unknown>;
			const by = [
				`**${str(c.author) || '?'}**`,
				str(c.at),
				c.new === true ? t('doc.view.new') : '',
				c.ours === true ? t('doc.view.apothecary') : '',
				str(c.did) ? t('doc.view.did', { did: str(c.did) }) : ''
			].filter(Boolean);
			out.push(
				block(`${by.join(' · ')}\n\n${quoted(str(c.body))}`, label, [
					at.start(cn as Node),
					at.end(cn as Node)
				])
			);
		}
	}
	return out;
}
