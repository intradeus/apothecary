// An annotation: lines picked in the Files viewer, with a comment, on their
// way into a chat (the held session's composer or Ask's). It travels as a
// chip until the message is sent; then it is folded into the message's text
// as the reference, the lines fenced in their language, and the comment.
import type { FileLoc, RunCheckout } from '$lib/domain';

export interface Annotation {
	/** Identity within the page, for keyed lists. */
	id: string;
	/** Where the lines are: `repo:path:12-18`, `path:12`, `/abs/path:3-4`. */
	ref: string;
	/** The fence's language ('' none). */
	lang: string;
	/** The lines as they read in the viewer. */
	quote: string;
	comment: string;
}

/** The reference of lines `from`–`to` (1-based, inclusive) in a file. The
 *  checkout is named only when the run has more than one; the primary ('')
 *  by its checkout's name. */
export function annotationRef(
	loc: FileLoc,
	from: number,
	to: number,
	checkouts: RunCheckout[]
): string {
	const lines = from === to ? `${from}` : `${from}-${to}`;
	if ('abs' in loc) return `${loc.abs}:${lines}`;
	if (checkouts.length <= 1) return `${loc.path}:${lines}`;
	const repo = loc.repo || (checkouts.find((c) => c.primary)?.name ?? '');
	return `${repo}:${loc.path}:${lines}`;
}

/** Fence tags whose name differs from the file's extension. */
const FENCE_EXT = new Map([
	['yml', 'yaml'],
	['mjs', 'js'],
	['cjs', 'js'],
	['mts', 'ts'],
	['cts', 'ts'],
	['markdown', 'md'],
	['h', 'c'],
	['hpp', 'cpp'],
	['cc', 'cpp']
]);
/** Files known by their whole name. */
const FENCE_NAME = new Map([
	['.bashrc', 'sh'],
	['.bash_profile', 'sh'],
	['.zshrc', 'sh'],
	['.profile', 'sh'],
	['dockerfile', 'dockerfile'],
	['makefile', 'make']
]);

/** A fenced block's language for a file: its extension, as Markdown names
 *  it ('' when the name says nothing). */
export function fenceLang(path: string): string {
	const base = path.slice(path.lastIndexOf('/') + 1).toLowerCase();
	const named = FENCE_NAME.get(base);
	if (named) return named;
	const dot = base.lastIndexOf('.');
	if (dot <= 0) return '';
	const ext = base.slice(dot + 1);
	if (!/^[a-z0-9+#-]+$/.test(ext)) return '';
	return FENCE_EXT.get(ext) ?? ext;
}

let made = 0;

/** A new annotation on lines `from`–`to` of a file. */
export function makeAnnotation(
	loc: FileLoc,
	from: number,
	to: number,
	quote: string,
	comment: string,
	checkouts: RunCheckout[]
): Annotation {
	return {
		id: `annotation-${++made}`,
		ref: annotationRef(loc, from, to, checkouts),
		lang: fenceLang('abs' in loc ? loc.abs : loc.path),
		quote,
		comment
	};
}

/** The longest run of backticks in text. */
function longestTicks(text: string): number {
	return Math.max(0, ...(text.match(/`+/g) ?? []).map((m) => m.length));
}

function inlineCode(text: string): string {
	const ticks = '`'.repeat(longestTicks(text) + 1);
	const pad = text.startsWith('`') || text.endsWith('`') ? ' ' : '';
	return `${ticks}${pad}${text}${pad}${ticks}`;
}

/** The annotations as message text: each one's reference in inline code,
 *  its lines fenced in their language (a fence longer than any backtick run
 *  in them), then its comment; blank lines between. */
export function annotationText(list: Annotation[]): string {
	return list
		.map((a) => {
			const fence = '`'.repeat(Math.max(3, longestTicks(a.quote) + 1));
			const quote = a.quote.replace(/\n$/, '');
			const parts = [inlineCode(a.ref), `${fence}${a.lang}\n${quote}\n${fence}`];
			const comment = a.comment.trim();
			if (comment) parts.push(comment);
			return parts.join('\n\n');
		})
		.join('\n\n');
}
