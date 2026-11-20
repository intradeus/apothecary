// Bytes ↔ text for the skill editor. A skill's files are bytes on the wire;
// the editor shows the ones that read as UTF-8 text and keeps the rest
// (images, archives) as opaque chips.

const decoder = new TextDecoder('utf-8', { fatal: true });
const encoder = new TextEncoder();

/** Text when it decodes as UTF-8 and has no NUL in its first 8 KiB. */
export function isText(bytes: Uint8Array): boolean {
	const head = bytes.subarray(0, 8192);
	for (const b of head) if (b === 0) return false;
	try {
		decoder.decode(bytes);
		return true;
	} catch {
		return false;
	}
}

export const decodeText = (bytes: Uint8Array): string => decoder.decode(bytes);
export const encodeText = (text: string): Uint8Array => encoder.encode(text);

/** The editor's language for a path: markdown for .md, else plain text. */
export const languageFor = (path: string): 'markdown' | 'plain' =>
	/\.(md|markdown)$/i.test(path) ? 'markdown' : 'plain';

/** The spec's name grammar, as the form checks it before the engine does. */
export const SKILL_NAME = /^[a-z0-9]+(-[a-z0-9]+)*$/;

/** A file as the editor holds it: text when it reads as UTF-8, else the
 *  bytes stay opaque. */
export type EditFile = { path: string; bytes: Uint8Array; mode?: number; text: string | null };

/** The editor's text files, as an assistant reads and fills them. */
export const textFiles = (files: EditFile[]): { path: string; content: string }[] =>
	files.flatMap((f) => (f.text === null ? [] : [{ path: f.path, content: f.text }]));

/** An assistant's fill over the editor's files: its files become the text
 *  files (a file's mode kept by path); a file that is not text stays unless
 *  the fill writes that path. */
export function fillFiles(
	files: EditFile[],
	next: { path: string; content: string }[]
): EditFile[] {
	const kept = files.filter((f) => f.text === null && !next.some((n) => n.path === f.path));
	return [
		...next.map((n) => ({
			path: n.path,
			bytes: new Uint8Array(),
			mode: files.find((f) => f.path === n.path)?.mode,
			text: n.content
		})),
		...kept
	];
}
