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
