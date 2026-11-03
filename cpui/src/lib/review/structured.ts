// The review page's views of a structured file (a gate's port type): a
// verdict as its decision, summary and comments; an mr_threads document as
// its threads, each comment quoted under its author. Each is still a list of
// DocBlocks — markdown rendered the review page's way — and each block keeps
// the source lines of what it renders, so a comment on a rendered thread
// lands on that thread's lines of the file the next stage reads. A document
// that does not read as its type is shown as written instead.
import { isMap, isScalar, isSeq, LineCounter, parseDocument, type Node, type YAMLMap } from 'yaml';
import { t } from '$lib/i18n/index.svelte';
import type { DocBlock } from '$lib/review/reviewDoc';

/** The port types with a view of their own. */
export const STRUCTURED_VIEWS = ['verdict', 'mr_threads'];
export const isStructuredView = (type?: string) => !!type && STRUCTURED_VIEWS.includes(type);

/** A structured file's blocks, or null when it does not read as its type. */
export function structuredBlocks(src: string, type: string): DocBlock[] | null {
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
const code = (s: string) => '`' + s + '`';
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

function verdictBlocks(m: YAMLMap, at: Lines): DocBlock[] | null {
	const v = m.toJSON() as { verdict?: unknown; summary?: unknown; comments?: unknown };
	if (v.verdict !== 'APPROVE' && v.verdict !== 'REJECT') return null;
	const head = v.verdict === 'APPROVE' ? t('doc.view.approved') : t('doc.view.rejected');
	const out = [block(`## ${head}`, head, at.pair(m, 'verdict')!)];
	const summary = str(v.summary).trim();
	const sumAt = at.pair(m, 'summary');
	if (summary && sumAt) out.push(block(summary, head, sumAt));
	const list = m.get('comments', true);
	if (!isSeq(list) || !list.items.length) return out;
	const title = t('doc.view.comments', { n: list.items.length });
	const key = at.pair(m, 'comments')![0];
	out.push(block(`### ${title}`, title, [key, key]));
	list.items.forEach((item, i) => {
		const n = item as Node;
		const lines: [number, number] = [at.start(n), at.end(n)];
		const c = n.toJSON() as unknown;
		if (typeof c === 'string') {
			out.push(block(`**${i + 1}.** ${c}`, title, lines));
			return;
		}
		const o = (c ?? {}) as Record<string, unknown>;
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
	});
	return out;
}

// ---- mr_threads ------------------------------------------------------------

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
	for (const item of threads) {
		if (!isMap(item)) return null;
		const th = item.toJSON() as Record<string, unknown>;
		const path = str(th.path);
		const line = num(th.line);
		const loc = path ? code(line ? `${path}:${line}` : path) : t('doc.view.conversation');
		const marks = [
			t('doc.view.thread', { id: str(th.id) }),
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
		const cms = isSeq(comments) ? comments.items : [];
		const headEnd = cms.length ? at.start(cms[0] as Node) - 1 : at.end(item);
		const hunk = str(th.hunk).trimEnd();
		const head = hunk ? `### ${title}\n\n\`\`\`\`diff\n${hunk}\n\`\`\`\`` : `### ${title}`;
		out.push(block(head, label, [at.start(item), Math.max(at.start(item), headEnd)]));
		for (const cn of cms) {
			const c = ((cn as Node).toJSON() ?? {}) as Record<string, unknown>;
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
	const dropped = m.get('dropped', true);
	if (isSeq(dropped) && dropped.items.length) {
		const title = t('doc.view.dropped');
		const key = at.pair(m, 'dropped')![0];
		out.push(block(`### ${title}`, title, [key, key]));
		for (const dn of dropped.items) {
			const d = ((dn as Node).toJSON() ?? {}) as Record<string, unknown>;
			const path = str(d.path);
			const where = path
				? code(num(d.line) ? `${path}:${num(d.line)}` : path)
				: t('doc.view.wholeChange');
			const kind = str(d.kind) ? ` · ${str(d.kind)}` : '';
			out.push(
				block(`${where}${kind}\n\n${quoted(str(d.body))}`, title, [
					at.start(dn as Node),
					at.end(dn as Node)
				])
			);
		}
	}
	return out;
}
