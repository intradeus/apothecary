import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
	KEPT_RUNS,
	STORAGE_KEY,
	rememberSession,
	rememberedSession
} from './terminalSessions.svelte';

const saved = () => JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '{}');

describe('the remembered terminal session', () => {
	beforeEach(() => localStorage.clear());
	afterEach(() => vi.restoreAllMocks());

	it('comes back for its run only', () => {
		rememberSession('r1', 'aaaa');
		rememberSession('r2', 'bbbb');
		rememberSession('r1', 'cccc');

		expect(rememberedSession('r1')).toBe('cccc');
		expect(rememberedSession('r2')).toBe('bbbb');
		expect(rememberedSession('r3')).toBe('');
	});

	it.each([
		['not JSON', '{'],
		['not a map', '[1]'],
		['a run that is not an object', JSON.stringify({ r1: 'x' })],
		['an id that is not a string', JSON.stringify({ r1: { at: 1, id: 7 } })]
	])('remembers nothing from storage that is %s, and writes over it', (_, raw) => {
		localStorage.setItem(STORAGE_KEY, raw);
		expect(rememberedSession('r1')).toBe('');

		rememberSession('r1', 'aaaa');

		expect(rememberedSession('r1')).toBe('aaaa');
	});

	it('works on, unsaved, when storage is blocked', () => {
		vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
			throw new Error('blocked');
		});
		vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
			throw new Error('blocked');
		});

		expect(() => rememberSession('r1', 'aaaa')).not.toThrow();
		expect(rememberedSession('r1')).toBe('');
	});

	it('keeps only the most recent runs', () => {
		const old = Object.fromEntries(
			Array.from({ length: KEPT_RUNS + 5 }, (_, i) => [`old${i}`, { at: i + 1, id: 'x' }])
		);
		localStorage.setItem(STORAGE_KEY, JSON.stringify({ ...old, junk: 'x', bare: {} }));

		rememberSession('r1', 'aaaa');

		const kept = Object.keys(saved());
		expect(kept).toHaveLength(KEPT_RUNS);
		expect(kept[0]).toBe('r1');
		expect(kept).toContain(`old${KEPT_RUNS + 4}`);
		expect(kept).not.toContain('old0');
		expect(kept).not.toContain('junk');
	});
});
