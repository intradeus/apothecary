import { describe, expect, it } from 'vitest';
import { linkify } from '$lib/core/links';
import { findMatches, matchFrom, segmentLine } from './logSegments';

describe('findMatches', () => {
	it('matches whatever the case', () => {
		expect(findMatches(['Error here', 'an ERROR', 'error'], 'error')).toEqual([
			{ line: 0, start: 0, end: 5 },
			{ line: 1, start: 3, end: 8 },
			{ line: 2, start: 0, end: 5 }
		]);
	});

	it('finds several on one line, in order', () => {
		expect(findMatches(['ab ab', '', 'xab'], 'ab')).toEqual([
			{ line: 0, start: 0, end: 2 },
			{ line: 0, start: 3, end: 5 },
			{ line: 2, start: 1, end: 3 }
		]);
	});

	it.each(['', '   '])('finds nothing for %j', (q) => {
		expect(findMatches(['a b c', '   '], q)).toEqual([]);
	});

	it('takes the query literally', () => {
		expect(findMatches(['a.c abc (x)'], '(x)')).toEqual([{ line: 0, start: 8, end: 11 }]);
		expect(findMatches(['abc'], 'a.c')).toEqual([]);
	});
});

describe('matchFrom', () => {
	const ms = findMatches(['ab ab', 'x', 'ab'], 'ab');

	it('finds a match by where it starts', () => {
		expect(matchFrom(ms, 0, 0)).toBe(0);
		expect(matchFrom(ms, 0, 3)).toBe(1);
		expect(matchFrom(ms, 2, 0)).toBe(2);
	});

	it('takes the next one when none starts there', () => {
		expect(matchFrom(ms, 0, 1)).toBe(1);
		expect(matchFrom(ms, 1, 0)).toBe(2);
	});

	it('wraps past the last to the first', () => {
		expect(matchFrom(ms, 2, 1)).toBe(0);
	});

	it('says -1 when there are none', () => {
		expect(matchFrom([], 0, 0)).toBe(-1);
	});
});

describe('segmentLine', () => {
	const URL = 'https://a.test/x';
	const line = `go ${URL} now`;
	const marksOf = (l: string, q: string) => findMatches([l], q);
	const plain = (text: string) => ({ text });
	const marked = (text: string) => ({ text, mark: true });
	const cur = (text: string) => ({ text, mark: true, current: true });

	it('keeps a part without a match whole', () => {
		expect(segmentLine(linkify(line), [])).toEqual([
			{ text: 'go ', pieces: [plain('go ')] },
			{ text: URL, href: URL, pieces: [plain(URL)] },
			{ text: ' now', pieces: [plain(' now')] }
		]);
	});

	it('gives an empty line no segments', () => {
		expect(segmentLine(linkify(''), [])).toEqual([]);
	});

	it('marks matches in plain text, and only the current one as current', () => {
		const l = 'one two one';
		expect(segmentLine(linkify(l), marksOf(l, 'one'), 1)).toEqual([
			{ text: l, pieces: [marked('one'), plain(' two '), cur('one')] }
		]);
	});

	it('makes a match inside a link both link and mark', () => {
		expect(segmentLine(linkify(line), marksOf(line, 'a.test'), 0)[1]).toEqual({
			text: URL,
			href: URL,
			pieces: [plain('https://'), cur('a.test'), plain('/x')]
		});
	});

	it('splits a match that crosses a link edge at the edge', () => {
		expect(segmentLine(linkify(line), marksOf(line, 'go https'), -1).map((s) => s.pieces)).toEqual([
			[marked('go ')],
			[marked('https'), plain('://a.test/x')],
			[plain(' now')]
		]);
		expect(segmentLine(linkify(line), marksOf(line, '/x no'), 0).map((s) => s.pieces)).toEqual([
			[plain('go ')],
			[plain('https://a.test'), cur('/x')],
			[cur(' no'), plain('w')]
		]);
	});

	it('marks a match that spans a whole link and beyond', () => {
		expect(segmentLine(linkify(line), marksOf(line, line), 0).map((s) => s.pieces)).toEqual([
			[cur('go ')],
			[cur(URL)],
			[cur(' now')]
		]);
	});

	it('rebuilds the line exactly', () => {
		const l = `x ${URL}, y ${URL} z`;
		for (const q of ['x', 'a.test', ', y h', 'z', 'q'])
			expect(
				segmentLine(linkify(l), marksOf(l, q), 0)
					.flatMap((s) => s.pieces!.map((p) => p.text))
					.join('')
			).toBe(l);
	});
});
