import { describe, expect, it } from 'vitest';
import { esc } from './html';

// What a user or the engine could put in a name that lands in a confirm body.
const HOSTILE = [
	'<script>alert(1)</script>',
	'<img src=x onerror=alert(1)>',
	'"><svg onload=alert(1)>',
	"' onmouseover='alert(1)",
	'" onmouseover="alert(1)',
	'</b><iframe src=javascript:alert(1)></iframe>',
	'<a href="javascript:alert(1)">click</a>',
	'<style>*{display:none}</style>',
	'<!-- <b>',
	'&lt;script&gt;alert(1)&lt;/script&gt;',
	'&#60;img src=x onerror=alert(1)&#62;',
	'<<script>script>alert(1)<</script>/script>',
	'<ScRiPt>alert(1)</sCrIpT>'
];

const parsed = (html: string) => {
	const host = document.createElement('div');
	host.innerHTML = html;
	return host;
};

describe('esc', () => {
	it.each([
		['&', '&amp;'],
		['<', '&lt;'],
		['>', '&gt;'],
		['"', '&quot;'],
		["'", '&#39;']
	])('escapes %s', (raw, safe) => {
		expect(esc(raw)).toBe(safe);
	});

	it('escapes every occurrence, not just the first', () => {
		expect(esc('<<>>&&')).toBe('&lt;&lt;&gt;&gt;&amp;&amp;');
	});

	it('escapes what it produced only once per pass', () => {
		expect(esc('<&>')).toBe('&lt;&amp;&gt;');
	});

	it('defuses an entity already in the text', () => {
		expect(esc('&lt;')).toBe('&amp;lt;');
	});

	it('leaves plain text alone', () => {
		expect(esc('')).toBe('');
		expect(esc('Nightly build · café 42')).toBe('Nightly build · café 42');
	});

	it.each(HOSTILE)('keeps %s inert as element content', (name) => {
		const host = parsed(`The workshop <b>${esc(name)}</b> is removed.`);
		expect(host.querySelectorAll('*')).toHaveLength(1);
		expect(host.querySelector('b')?.textContent).toBe(name);
	});

	it.each(HOSTILE)('keeps %s inside a double-quoted attribute', (name) => {
		const a = parsed(`<a title="${esc(name)}">x</a>`).querySelector('a')!;
		expect(a.getAttributeNames()).toEqual(['title']);
		expect(a.title).toBe(name);
		expect(a.textContent).toBe('x');
	});

	it.each(HOSTILE)('keeps %s inside a single-quoted attribute', (name) => {
		const a = parsed(`<a title='${esc(name)}'>x</a>`).querySelector('a')!;
		expect(a.getAttributeNames()).toEqual(['title']);
		expect(a.title).toBe(name);
		expect(a.textContent).toBe('x');
	});
});
