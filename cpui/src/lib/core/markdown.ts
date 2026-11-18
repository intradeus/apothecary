// Markdown, rendered the one safe way for the whole app.
//
// Agent-generated markdown is semi-trusted: raw HTML is escaped instead of
// injected (marked passes it through by default), and only harmless links
// stay links. Render through <Markdown> ($lib/ui/Markdown.svelte) or
// mdToHtml(); never call marked directly.
import { Marked, marked, type Token, type Tokens } from 'marked';
import { findPaths, type PathRef } from './paths';

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
	},
	// The same rule for a source; false hands a harmless one to marked's own.
	image({ href, text }: { href: string; text: string }) {
		return safeHref(href) ? false : escapeHTML(text);
	}
};
marked.use({ renderer });

export { marked };

// Path mode: the same rendering, plus file references (paths.ts) in prose
// and in an inline code span that is exactly one, as buttons carrying the
// reference in data-path/-line/-end, for a view's click handler
// (pathClick). Its own instance, so the default stays exactly as it was.
// Never inside a link's text (a button in an anchor); block code never
// reaches these renderers.
const ESC: Record<string, string> = {
	'&': '&amp;',
	'<': '&lt;',
	'>': '&gt;',
	'"': '&quot;',
	"'": '&#39;'
};
/** marked's own escape for prose: an entity the source wrote stays one.
 *  Quotes too, so it holds in an attribute. */
const escapeText = (s: string) =>
	s.replace(/[<>"']|&(?!(#\d{1,7}|#[Xx][a-fA-F0-9]{1,6}|\w+);)/g, (c) => ESC[c]);

function pathButton(ref: PathRef, inner: string): string {
	const line = ref.from ? ` data-line="${ref.from}"` : '';
	const end = ref.to ? ` data-end="${ref.to}"` : '';
	return `<button type="button" class="linklike path-link" data-path="${escapeText(ref.path)}"${line}${end}>${inner}</button>`;
}

let inLink = 0;
const pathMarked = new Marked({
	renderer: {
		...renderer,
		link(
			this: { parser: { parseInline: (t: Token[]) => string } },
			tok: { href: string; title?: string | null; tokens: Token[] }
		) {
			inLink++;
			const html = renderer.link.call(this, tok);
			inLink--;
			return html;
		},
		codespan({ text }: Tokens.Codespan) {
			const [s] = findPaths(text);
			if (inLink || !s || s.start > 0 || s.end < text.length) return false;
			return pathButton(s.ref, `<code>${escapeText(text)}</code>`);
		},
		// false hands the token to marked's own: no path in it, a link's
		// text, raw text, or a block's text (its inline tokens come back here).
		text(tok: Tokens.Text | Tokens.Escape) {
			const { tokens, escaped } = tok as Tokens.Text;
			const spans = inLink || tokens || escaped ? [] : findPaths(tok.text);
			if (!spans.length) return false;
			let out = '';
			let last = 0;
			for (const s of spans) {
				out += escapeText(tok.text.slice(last, s.start));
				out += pathButton(s.ref, escapeText(tok.text.slice(s.start, s.end)));
				last = s.end;
			}
			return out + escapeText(tok.text.slice(last));
		}
	}
});

// mdToHtml renders markdown for ping questions/context/answers (same
// escaped-HTML posture as plans — agent output is semi-trusted). `paths`
// turns file references into buttons (path mode, above).
export function mdToHtml(src: string, opts: { paths?: boolean } = {}): string {
	return (opts.paths ? pathMarked : marked).parse(src, { async: false }) as string;
}
