// Review support (DESIGN3 §3): the files a ping shows — a markdown
// document, YAML, code, an image — each text one split into line-tracked
// blocks for anchoring comments, and the comments themselves: kept as a
// draft in this browser, sent as structure. The engine renders the review
// into what the agent reads; nothing here writes the agent's prompt.
import type { Token } from 'marked';
import { marked } from '$lib/core/markdown';
import type { PingFile, ReviewComment } from '$lib/domain';

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

/** One comment as the review page holds it: on one file, on the stretch of
 *  text selected (startLine 0, no quote: the whole file). */
export interface DraftComment {
	/** Stable identity for the list (edits keep it); never sent. */
	id: string;
	path: string;
	quote: string;
	heading: string;
	startLine: number;
	endLine: number;
	text: string;
	anchor?: CommentAnchor;
	files?: File[]; // per-comment attachments — live only, never persisted
	/** A submitted comment's attachments (history). */
	attachmentIds?: string[];
}

export const isWholeFile = (c: Pick<DraftComment, 'startLine'>) => !c.startLine;

/** A fresh comment id (list identity only; never sent). */
export const commentId = () => Math.random().toString(36).slice(2, 10);

/** A draft comment as the answer carries it, its attachments by id. */
export function commentToSend(c: DraftComment, attachmentIds: string[] = []): ReviewComment {
	const whole = isWholeFile(c);
	return {
		path: c.path,
		startLine: whole ? undefined : c.startLine,
		endLine: whole ? undefined : c.endLine,
		heading: c.heading || undefined,
		quote: whole ? undefined : c.quote || undefined,
		body: c.text.trim(),
		attachmentIds
	};
}

/** A submitted comment, held the way the page shows one. */
export const commentAsShown = (c: ReviewComment, i: number): DraftComment => ({
	id: `r${i}`,
	path: c.path,
	quote: c.quote ?? '',
	heading: c.heading ?? '',
	startLine: c.startLine ?? 0,
	endLine: c.endLine ?? c.startLine ?? 0,
	text: c.body,
	attachmentIds: c.attachmentIds
});

// ---- the files a review shows ---------------------------------------------

/** text: shown and commented line by line; image: shown, commented as a
 *  whole; other: offered as a download. */
export type FileKind = 'text' | 'image' | 'other';

// The files the engine reads as pictures (mirror of the Go list): a comment
// on one is about the whole image.
const IMAGE_EXTS = ['.png', '.jpg', '.jpeg', '.gif', '.webp', '.svg', '.avif', '.bmp', '.ico'];
export const isImagePath = (path: string) => {
	const p = path.toLowerCase();
	return IMAGE_EXTS.some((e) => p.endsWith(e));
};

// Plainly not text: never fetched to find out. Anything else is read and
// sniffed (a NUL in its head makes it binary) — the snapshot's type comes
// from the extension first, which says nothing useful of .ts or .lock.
const BINARY_MIME =
	/^(audio|font)\/|^video\/(?!mp2t)|^application\/(pdf|zip|gzip|x-tar|x-7z|wasm|vnd\.)/;

/** What a shown file is, before its content is read. */
export function fileKind(f: Pick<PingFile, 'path' | 'mime'>): FileKind {
	if (isImagePath(f.path) || f.mime?.startsWith('image/')) return 'image';
	return f.mime && BINARY_MIME.test(f.mime) ? 'other' : 'text';
}

/** A file's head holds a NUL: binary, however it is named. */
export const looksBinary = (bytes: Uint8Array) => bytes.subarray(0, 8192).includes(0);

/** How many comments each file carries, by path. */
export const perFile = (comments: Pick<DraftComment, 'path'>[]) =>
	comments.reduce<Record<string, number>>((m, c) => ((m[c.path] = (m[c.path] ?? 0) + 1), m), {});

/** The comparator that orders comments as the review reads: by file, in the
 *  order the files are shown; then whole-file comments; then by position. */
export function byPlace(order: string[]) {
	const at = (p: string) => {
		const i = order.indexOf(p);
		return i < 0 ? order.length : i;
	};
	return (a: DraftComment, b: DraftComment) =>
		at(a.path) - at(b.path) ||
		a.startLine - b.startLine ||
		(a.anchor?.startOffset ?? 0) - (b.anchor?.startOffset ?? 0) ||
		a.endLine - b.endLine;
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
				// A raw may or may not end in newlines (marked keeps a blank line
				// apart, as a space token): the last line is the last with text.
				endLine: line + (raw.trimEnd().match(/\n/g) ?? []).length
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

// locate finds a submitted comment's quote again in its file's rendered
// blocks (texts = each block's textContent), within the blocks its lines
// span: the anchor the person's selection had, rebuilt from what was sent.
// Whitespace is compared loosely — a selection's text breaks lines where the
// blocks do. Null when the quote is not there to find.
export function locate(
	texts: string[],
	blocks: DocBlock[],
	c: Pick<DraftComment, 'quote' | 'startLine' | 'endLine'>
): CommentAnchor | null {
	const needle = c.quote.replace(/\s+/g, ' ').trim();
	if (!needle || !blocks.length) return null;
	const inLines = blocks
		.map((b, i) => (b.endLine >= c.startLine && b.startLine <= c.endLine ? i : -1))
		.filter((i) => i >= 0);
	const from = inLines[0] ?? 0;
	const to = inLines[inLines.length - 1] ?? blocks.length - 1;
	// The blocks' text with each run of whitespace one space, and where each
	// of its characters came from.
	let hay = '';
	const at: [number, number][] = [];
	for (let b = from; b <= to; b++) {
		const s = texts[b] ?? '';
		for (let o = 0; o <= s.length; o++) {
			const ch = o < s.length ? s[o] : ' '; // a block ends a word
			if (!/\s/.test(ch)) {
				hay += ch;
				at.push([b, o]);
			} else if (hay && !hay.endsWith(' ')) {
				hay += ' ';
				at.push([b, o]);
			}
		}
	}
	const i = hay.indexOf(needle);
	if (i < 0) return null;
	const [startBlock, startOffset] = at[i];
	const [endBlock, end] = at[i + needle.length - 1];
	return { startBlock, startOffset, endBlock, endOffset: end + 1 };
}

// ---- drafts ---------------------------------------------------------------

// A draft lives in this browser only: the comments, each on its file, and
// the summary. A draft from before comments named their file (a bare list)
// is read as being on the primary file.
const draftKey = (pingId: string) => `doc-review-${pingId}`;

export interface Draft {
	comments: DraftComment[];
	summary: string;
}

export function loadDraft(pingId: string, primary: string): Draft {
	try {
		const raw = localStorage.getItem(draftKey(pingId));
		const v = raw ? (JSON.parse(raw) as unknown) : null;
		const list = (
			Array.isArray(v) ? v : ((v as Draft | null)?.comments ?? [])
		) as Partial<DraftComment>[];
		const summary = Array.isArray(v) ? '' : ((v as Draft | null)?.summary ?? '');
		return {
			summary,
			comments: list.map((c) => ({
				quote: '',
				heading: '',
				startLine: 0,
				endLine: 0,
				text: '',
				...c,
				id: c.id ?? commentId(),
				path: c.path ?? primary
			}))
		};
	} catch {
		return { comments: [], summary: '' };
	}
}

export function saveDraft(pingId: string, d: Draft) {
	try {
		if (d.comments.length === 0 && !d.summary.trim()) localStorage.removeItem(draftKey(pingId));
		// Attachments are dropped: File handles can't be revived from JSON, and
		// re-picking is better than a chip that points at nothing.
		else
			localStorage.setItem(
				draftKey(pingId),
				JSON.stringify({
					summary: d.summary,
					comments: d.comments.map(({ files, ...rest }) => rest)
				})
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
