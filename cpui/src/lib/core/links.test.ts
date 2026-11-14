import { describe, expect, it } from 'vitest';
import { linkify } from './links';

describe('linkify', () => {
	it('leaves text without a URL as one plain part', () => {
		expect(linkify('nothing to see here')).toEqual([{ text: 'nothing to see here' }]);
	});

	it('gives nothing for no text', () => {
		expect(linkify('')).toEqual([]);
	});

	it('turns http and https URLs into links', () => {
		expect(linkify('see http://a.test/x and https://b.test/y?q=1 now')).toEqual([
			{ text: 'see ' },
			{ text: 'http://a.test/x', href: 'http://a.test/x' },
			{ text: ' and ' },
			{ text: 'https://b.test/y?q=1', href: 'https://b.test/y?q=1' },
			{ text: ' now' }
		]);
	});

	it.each(['.', ',', ';', ':', ')'])('leaves a trailing "%s" outside the link', (p) => {
		expect(linkify(`(at https://a.test/docs${p}`)).toEqual([
			{ text: '(at ' },
			{ text: 'https://a.test/docs', href: 'https://a.test/docs' },
			{ text: p }
		]);
	});

	it.each(['javascript:alert(1)', 'ftp://files.test/a'])('keeps %s plain', (s) => {
		expect(linkify(`go ${s}`)).toEqual([{ text: `go ${s}` }]);
	});

	it('finds a URL at the very start and at the very end', () => {
		expect(linkify('https://a.test/ then https://b.test/z')).toEqual([
			{ text: 'https://a.test/', href: 'https://a.test/' },
			{ text: ' then ' },
			{ text: 'https://b.test/z', href: 'https://b.test/z' }
		]);
	});
});
