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

	it.each(['"', "'", '`'])('leaves the quotes %s around a URL outside the link', (q) => {
		expect(linkify(`git clone ${q}https://a.test/repo.git${q} repo`)).toEqual([
			{ text: `git clone ${q}` },
			{ text: 'https://a.test/repo.git', href: 'https://a.test/repo.git' },
			{ text: `${q} repo` }
		]);
	});

	it('ends a URL at a quote inside a word', () => {
		expect(linkify(`url='https://a.test/x'&next`)).toEqual([
			{ text: `url='` },
			{ text: 'https://a.test/x', href: 'https://a.test/x' },
			{ text: `'&next` }
		]);
	});

	it('finds a URL at the very start and at the very end', () => {
		expect(linkify('https://a.test/ then https://b.test/z')).toEqual([
			{ text: 'https://a.test/', href: 'https://a.test/' },
			{ text: ' then ' },
			{ text: 'https://b.test/z', href: 'https://b.test/z' }
		]);
	});
});
