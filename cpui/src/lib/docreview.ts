// Document review support (DESIGN3 §3): any file a ping hands over — a
// markdown document, YAML, code — split into line-tracked blocks for
// anchoring, and the deterministic composition of accumulated comments into
// ONE feedback prompt for the existing on_reject feed path — the engine
// learns nothing new.
import type { Token } from 'marked';
import { marked } from '$lib/markdown';

export interface DocBlock {
	/** The block's own markdown (a fenced listing for a non-markdown file);
	 *  rendered through <Markdown>. */
	raw: string;
	heading: string; // nearest heading above (or at) this block
	startLine: number;
	endLine: number;
}

// A CommentAnchor pins a comment to the exact stretch of rendered text the
// human selected: character offsets into each block's textContent. Blocks
// render deterministically from the markdown, so the offsets survive a
// reload and can be turned back into DOM Ranges for highlighting.
export interface CommentAnchor {
	startBlock: number;
	startOffset: number;
	endBlock: number;
	endOffset: number;
}

export interface ReviewComment {
	/** Stable identity for the list (edits keep it); absent in old drafts. */
	id?: string;
	quote: string;
	heading: string;
	startLine: number;
	endLine: number;
	text: string;
	anchor?: CommentAnchor;
	files?: File[]; // per-comment attachments — live only, never persisted
}

/** Markdown is rendered; every other file (YAML, JSON, code, plain text) is
 *  shown as written. A name without an extension is read as markdown. */
export function isMarkdown(path: string): boolean {
	const base = path.split('/').pop() ?? '';
	return !base.includes('.') || /\.(md|markdown|mdx)$/i.test(base);
}

/** A non-markdown text as a fenced listing, its fence longer than any run of
 *  backticks inside so the text can never close it. */
export function listing(src: string, path: string): string {
	const runs = src.match(/`{3,}/g) ?? [];
	const fence = '`'.repeat(Math.max(3, ...runs.map((r) => r.length + 1)));
	const lang = /\.([a-z0-9]+)$/i.exec(path)?.[1]?.toLowerCase() ?? '';
	return `${fence}${lang}\n${src.replace(/\n$/, '')}\n${fence}`;
}

/** The whole file as markdown to render: itself, or its listing. */
export const asMarkdown = (src: string, path: string) =>
	isMarkdown(path) ? src : listing(src, path);

// docBlocks splits a file into the blocks comments anchor to. Markdown is
// lexed into top-level blocks, each carrying its source line range and
// nearest heading (marked tokens keep `raw`, so line numbers are cumulative
// newline counts); any other file into runs of lines between blank lines,
// each a listing of its own.
export function docBlocks(src: string, path: string): DocBlock[] {
	if (!isMarkdown(path)) return listingBlocks(src, path);
	const tokens = marked.lexer(src);
	const out: DocBlock[] = [];
	let line = 1;
	let heading = '';
	for (const tok of tokens) {
		const raw = (tok as Token & { raw: string }).raw ?? '';
		const lines = (raw.match(/\n/g) ?? []).length;
		if (tok.type === 'heading') heading = (tok as Token & { text: string }).text;
		if (tok.type !== 'space') {
			out.push({
				raw,
				heading,
				startLine: line,
				endLine: Math.max(line, line + lines - 1)
			});
		}
		line += lines;
	}
	return out;
}

function listingBlocks(src: string, path: string): DocBlock[] {
	const out: DocBlock[] = [];
	const lines = src.replace(/\n$/, '').split('\n');
	let start = -1;
	const flush = (end: number) => {
		if (start < 0) return;
		out.push({
			raw: listing(lines.slice(start, end).join('\n'), path),
			heading: '',
			startLine: start + 1,
			endLine: end
		});
		start = -1;
	};
	lines.forEach((l, i) => {
		if (l.trim() === '') flush(i);
		else if (start < 0) start = i;
	});
	flush(lines.length);
	return out;
}

// textOffset is the character offset of (node, offset) within root's rendered
// text — Range.toString() concatenates exactly the text nodes rangeAt walks,
// so the two are inverses of each other.
export function textOffset(root: Element, node: Node, offset: number): number {
	const r = document.createRange();
	r.selectNodeContents(root);
	try {
		r.setEnd(node, offset);
	} catch {
		return 0; // node outside root (stale DOM) — degrade to the block head
	}
	return r.toString().length;
}

// rangeAt is textOffset's inverse: the DOM Range covering [start, end) of
// root's rendered text. Null when root holds no text at all.
export function rangeAt(root: Element, start: number, end: number): Range | null {
	const walk = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
	const range = document.createRange();
	let pos = 0;
	let open = false;
	for (let n = walk.nextNode(); n; n = walk.nextNode()) {
		const len = n.nodeValue?.length ?? 0;
		if (!open && pos + len >= start) {
			range.setStart(n, Math.max(0, start - pos));
			open = true;
		}
		if (open && pos + len >= end) {
			range.setEnd(n, Math.max(0, Math.min(len, end - pos)));
			return range;
		}
		pos += len;
	}
	if (!open) return null;
	range.setEnd(root, root.childNodes.length);
	return range;
}

// byPosition orders comments the way they read in the document: block order,
// then position inside the block.
export function byPosition(a: ReviewComment, b: ReviewComment): number {
	return (
		a.startLine - b.startLine ||
		(a.anchor?.startOffset ?? 0) - (b.anchor?.startOffset ?? 0) ||
		a.endLine - b.endLine
	);
}

// composeFeedback renders the single structured feedback string (§3.2),
// headed by the file it is about. Attachments are named, not inlined: the
// files themselves are uploaded with the answer and delivered into the
// workspace, so naming them here is how the agent correlates a file with the
// comment that motivated it.
export function composeFeedback(
	path: string,
	comments: ReviewComment[],
	reviewFiles: string[] = []
): string {
	const n = `${comments.length} comment${comments.length === 1 ? '' : 's'}`;
	const parts = [path ? `REVIEW OF ${path} — ${n}:` : `REVIEW — ${n}:`, ''];
	comments.forEach((c, i) => {
		const where = c.heading
			? `§ ${c.heading}, lines ${c.startLine}–${c.endLine}`
			: `lines ${c.startLine}–${c.endLine}`;
		const quote = c.quote.length > 200 ? c.quote.slice(0, 200) + '…' : c.quote;
		parts.push(`${i + 1}. [${where}]`);
		for (const ql of quote.trim().split('\n')) parts.push(`   > ${ql}`);
		parts.push(`   ${c.text.trim().split('\n').join('\n   ')}`);
		if (c.files?.length) parts.push(`   attached: ${c.files.map((f) => f.name).join(', ')}`);
		parts.push('');
	});
	if (reviewFiles.length) parts.push(`ATTACHED TO THE REVIEW: ${reviewFiles.join(', ')}`);
	return parts.join('\n').trimEnd() + '\n';
}

const draftKey = (pingId: string) => `doc-review-${pingId}`;

/** A fresh comment id (list identity only; never sent). */
export const commentId = () => Math.random().toString(36).slice(2, 10);

export function loadDraft(pingId: string): ReviewComment[] {
	try {
		const raw = localStorage.getItem(draftKey(pingId));
		const list = raw ? (JSON.parse(raw) as ReviewComment[]) : [];
		return list.map((c) => ({ ...c, id: c.id ?? commentId() }));
	} catch {
		return [];
	}
}

export function saveDraft(pingId: string, comments: ReviewComment[]) {
	try {
		if (comments.length === 0) localStorage.removeItem(draftKey(pingId));
		// Attachments are dropped: File handles can't be revived from JSON, and
		// re-picking is better than a chip that points at nothing.
		else
			localStorage.setItem(
				draftKey(pingId),
				JSON.stringify(comments.map(({ files, ...rest }) => rest))
			);
	} catch {
		/* private mode etc. — drafts are best-effort */
	}
}

export function clearDraft(pingId: string) {
	try {
		localStorage.removeItem(draftKey(pingId));
	} catch {
		/* ignore */
	}
}
