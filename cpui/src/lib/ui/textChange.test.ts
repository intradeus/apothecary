import { describe, expect, it } from 'vitest';
import { minimalChange } from './textChange';

const apply = (prev: string, next: string) => {
	const c = minimalChange(prev, next)!;
	return prev.slice(0, c.from) + c.insert + prev.slice(c.to);
};

describe('minimalChange', () => {
	it('is nothing for the same text', () => {
		expect(minimalChange('a\nb', 'a\nb')).toBeNull();
	});

	it('keeps the common prefix and suffix', () => {
		expect(minimalChange('one\ntwo\nthree', 'one\nTWO\nthree')).toEqual({
			from: 4,
			to: 7,
			insert: 'TWO'
		});
	});

	it('inserts and deletes', () => {
		expect(minimalChange('ac', 'abc')).toEqual({ from: 1, to: 1, insert: 'b' });
		expect(minimalChange('abc', 'ac')).toEqual({ from: 1, to: 2, insert: '' });
		expect(minimalChange('', 'x')).toEqual({ from: 0, to: 0, insert: 'x' });
		expect(minimalChange('x', '')).toEqual({ from: 0, to: 1, insert: '' });
	});

	it('never lets the suffix overlap the prefix', () => {
		expect(minimalChange('aaa', 'aaaa')).toEqual({ from: 3, to: 3, insert: 'a' });
		expect(minimalChange('abab', 'ab')).toEqual({ from: 2, to: 4, insert: '' });
		for (const [a, b] of [
			['aba', 'ababa'],
			['xyzzy', 'xy'],
			['a\n\nb', 'a\n\n\nb']
		])
			expect(apply(a, b)).toBe(b);
	});
});
