import { describe, expect, it } from 'vitest';
import { mdToHtml } from './markdown';

/** The rendered markdown as the browser would hold it, without running it. */
const dom = (src: string) => {
	const root = document.createElement('div');
	root.innerHTML = mdToHtml(src);
	return root;
};

/** What must never come out of a render, whatever went in. */
function expectHarmless(src: string) {
	const root = dom(src);
	for (const bad of [
		'script',
		'iframe',
		'object',
		'embed',
		'style',
		'link',
		'meta',
		'form',
		'base',
		'svg'
	])
		expect(root.querySelector(bad), `<${bad}> from ${src}`).toBeNull();
	for (const el of root.querySelectorAll('*'))
		for (const { name, value } of [...el.attributes]) {
			expect(name.startsWith('on'), `${name} from ${src}`).toBe(false);
			// eslint-disable-next-line no-control-regex
			expect(value.replace(/[\s\x00-\x1f]/g, ''), `${name}="${value}"`).not.toMatch(
				/^(javascript|vbscript|data):/i
			);
		}
}

describe('mdToHtml', () => {
	describe('ordinary markdown', () => {
		it('renders headings, emphasis and lists', () => {
			const root = dom('# Plan\n\nSome **bold** text.\n\n- one\n- two\n');
			expect(root.querySelector('h1')).toHaveTextContent('Plan');
			expect(root.querySelector('strong')).toHaveTextContent('bold');
			expect(root.querySelectorAll('li')).toHaveLength(2);
		});

		it('renders code without interpreting it', () => {
			const root = dom('`<b>x</b>`\n\n```\n<script>alert(1)</script>\n```\n');
			expect(root.querySelector('code')).toHaveTextContent('<b>x</b>');
			expect(root.querySelector('pre code')).toHaveTextContent('<script>alert(1)</script>');
			expect(root.querySelector('script')).toBeNull();
			expect(root.querySelector('b')).toBeNull();
		});

		it('renders nothing for nothing', () => {
			expect(mdToHtml('')).toBe('');
		});
	});

	describe('links', () => {
		it.each([
			['https://example.test/a?b=1'],
			['http://example.test'],
			['HTTPS://EXAMPLE.TEST'],
			['mailto:someone@example.test'],
			['#section'],
			['/runs/1'],
			['./plan.md'],
			['../plan.md'],
			['plan.md'],
			['docs/plan.md']
		])('keeps %s a link, opening safely', (href) => {
			const a = dom(`[here](${href})`).querySelector('a');
			expect(a).toHaveAttribute('href', href);
			expect(a).toHaveAttribute('target', '_blank');
			expect(a).toHaveAttribute('rel', 'noreferrer');
			expect(a).toHaveTextContent('here');
		});

		it.each([
			['javascript:alert(1)'],
			['JaVaScRiPt:alert(1)'],
			['  javascript:alert(1)'],
			['vbscript:msgbox(1)'],
			['data:text/html;base64,PHNjcmlwdD4='],
			['data:text/html,<script>alert(1)</script>'],
			['file:///etc/passwd'],
			['blob:https://x/1'],
			['ftp://example.test/x']
		])('reduces [text](%s) to its text', (href) => {
			const root = dom(`[click me](<${href}>)`);
			expect(root.querySelector('a')).toBeNull();
			expect(root.textContent).toContain('click me');
		});

		it('keeps a link’s inner formatting when it drops the link', () => {
			const root = dom('[**bold** text](javascript:alert(1))');
			expect(root.querySelector('a')).toBeNull();
			expect(root.querySelector('strong')).toHaveTextContent('bold');
		});

		it.each([
			['a reference link', '[x][a]\n\n[a]: javascript:alert(1)'],
			['an autolink', '<javascript:alert(1)>'],
			['an escaped colon', '[x](javascript\\:alert(1))']
		])('drops %s to a script', (_, src) => {
			expect(dom(src).querySelector('a')).toBeNull();
		});

		it.each([
			['a decimal entity', '[x](javascript&#58;alert(1))'],
			['a hex entity', '[x](javascript&#x3A;alert(1))'],
			['a named entity', '[x](javascript&colon;alert(1))'],
			['an entity in the scheme', '[x](&#106;avascript:alert(1))'],
			['a tab in the scheme', '[x](java&#9;script:alert(1))']
		])('never lets %s decode into a script', (_, src) => {
			const a = dom(src).querySelector('a');
			if (a) expect(a.protocol).toMatch(/^(http|https|about|file):$/);
			expectHarmless(src);
		});

		it('escapes a title that tries to leave its attribute', () => {
			const a = dom('[x](https://a.test "t\\" onclick=\\"alert(1)")').querySelector('a');
			expect(a).toHaveAttribute('title', 't" onclick="alert(1)');
			expect(a).not.toHaveAttribute('onclick');
		});

		it('escapes an href that tries to leave its attribute', () => {
			const a = dom('[x](<https://a.test/" onclick="alert(1)>)').querySelector('a');
			expect(a).not.toHaveAttribute('onclick');
		});
	});

	describe('raw HTML', () => {
		it.each([
			['<script>alert(1)</script>'],
			['<SCRIPT SRC=https://evil.test/x.js></SCRIPT>'],
			['<img src=x onerror=alert(1)>'],
			['<svg onload=alert(1)>'],
			['<iframe src="javascript:alert(1)"></iframe>'],
			['<a href="javascript:alert(1)">x</a>'],
			['<div onclick="alert(1)">x</div>'],
			['<style>body{display:none}</style>'],
			['<form action="https://evil.test"><input name=x></form>'],
			['<object data="x"></object>'],
			['<embed src="x">'],
			['<base href="https://evil.test/">'],
			['<meta http-equiv="refresh" content="0;url=https://evil.test">'],
			['<details open ontoggle=alert(1)>x</details>'],
			['<math><mi xlink:href="javascript:alert(1)">x</mi></math>']
		])('shows %s as text', (src) => {
			const root = dom(src);
			expect(root.textContent?.trim()).toBe(src);
			expectHarmless(src);
		});

		it.each([
			['inline', 'Text <img src=x onerror=alert(1)> more'],
			['inside emphasis', '**<script>alert(1)</script>**'],
			['inside a list', '- <script>alert(1)</script>'],
			['inside a quote', '> <img src=x onerror=alert(1)>'],
			['inside a table', '| a |\n|---|\n| <img src=x onerror=alert(1)> |'],
			['inside a link', '[<img src=x onerror=alert(1)>](https://a.test)'],
			['as a comment', '<!-- --><script>alert(1)</script>'],
			['split by a newline', '<scr\nipt>alert(1)</scr\nipt>'],
			['unclosed', '<img src=x onerror=alert(1)//']
		])('neutralises HTML %s', (_, src) => {
			expectHarmless(src);
			expect(dom(src).querySelector('img')).toBeNull();
		});
	});

	describe('images', () => {
		it('keeps an ordinary image', () => {
			const img = dom('![a plan](https://a.test/x.png)').querySelector('img');
			expect(img).toHaveAttribute('src', 'https://a.test/x.png');
			expect(img).toHaveAttribute('alt', 'a plan');
		});

		it.each([
			['alt text', '![" onerror="alert(1)](https://a.test/x.png)'],
			['a title', '![x](https://a.test/x.png "\\" onerror=\\"alert(1)")'],
			['HTML in the alt', '![<img src=x onerror=alert(1)>](https://a.test/x.png)']
		])('lets no handler in through %s', (_, src) => {
			expectHarmless(src);
		});

		// No current browser runs a javascript: src, but "only harmless links stay
		// links" holds here as well.
		it.each([
			['javascript:alert(1)'],
			['vbscript:msgbox(1)'],
			['data:text/html,<script>alert(1)</script>']
		])('drops an image whose source is %s', (src) => {
			expectHarmless(`![x](<${src}>)`);
		});

		it('leaves the alt text of a dropped image, escaped', () => {
			const root = dom('![<b>a plan</b>](javascript:alert(1))');
			expect(root.querySelector('img')).toBeNull();
			expect(root.querySelector('b')).toBeNull();
			expect(root).toHaveTextContent('<b>a plan</b>');
		});

		it('keeps a relative image', () => {
			expect(dom('![a plan](./x.png)').querySelector('img')).toHaveAttribute('src', './x.png');
		});
	});
});
