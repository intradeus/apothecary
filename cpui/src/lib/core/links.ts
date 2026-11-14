// Plain text with URLs in it — a kind's help, a log line — split into text
// and link parts, so a view can render the links as anchors. Only http(s):
// a `javascript:` URL can never come out of it. Trailing punctuation
// (".,;:)") stays outside the link, as a sentence ends after it.

export type Part = { text: string; href?: string };

const URL_RE = /https?:\/\/[^\s<>()]+[^\s<>().,;:]/g;

export function linkify(s: string): Part[] {
	const parts: Part[] = [];
	let last = 0;
	for (const m of s.matchAll(URL_RE)) {
		const i = m.index ?? 0;
		if (i > last) parts.push({ text: s.slice(last, i) });
		parts.push({ text: m[0], href: m[0] });
		last = i + m[0].length;
	}
	if (last < s.length) parts.push({ text: s.slice(last) });
	return parts;
}
