// Markdown, rendered the one safe way for the whole app.
//
// Agent-generated markdown is semi-trusted: raw HTML is escaped instead of
// injected (marked passes it through by default), and only harmless links
// stay links. Render through <Markdown> ($lib/ui/Markdown.svelte) or
// mdToHtml(); never call marked directly.
import { marked, type Token } from 'marked';

// Agent-generated markdown is semi-trusted: escape raw HTML instead of
// injecting it (marked passes HTML through by default).
function escapeHTML(s: string): string {
	return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}
// Only link where a link is harmless: web, mail, and in-page/relative
// targets. A `javascript:` href in agent output is not a link, it is code.
const safeHref = (href: string) => /^(https?:|mailto:|#|\/|\.{0,2}\/|[^:]*$)/i.test(href.trim());
const renderer = {
	html({ text }: { text: string }) {
		return `<pre class="small mono">${escapeHTML(text)}</pre>`;
	},
	link(
		this: { parser: { parseInline: (t: Token[]) => string } },
		tok: { href: string; title?: string | null; tokens: Token[] }
	) {
		const inner = this.parser.parseInline(tok.tokens);
		if (!safeHref(tok.href)) return inner;
		const title = tok.title ? ` title="${escapeHTML(tok.title).replace(/"/g, '&quot;')}"` : '';
		return `<a href="${escapeHTML(tok.href).replace(/"/g, '&quot;')}"${title} target="_blank" rel="noreferrer">${inner}</a>`;
	}
};
marked.use({ renderer });

export { marked };

// mdToHtml renders markdown for ping questions/context/answers (same
// escaped-HTML posture as plans — agent output is semi-trusted).
export function mdToHtml(src: string): string {
	return marked.parse(src, { async: false }) as string;
}
