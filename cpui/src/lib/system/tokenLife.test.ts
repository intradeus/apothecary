import { describe, expect, it } from 'vitest';
import type { Token } from '$lib/domain';
import { fmtAbs } from '$lib/core/format';
import { tokenLife } from './tokenLife';

const NOW = Date.parse('2026-11-18T12:00:00Z');
const token = (over: Partial<Token> = {}): Token => ({
	id: 't',
	name: 'n',
	prefix: 'apo_abcdef',
	createdAt: '2026-11-18T00:00:00Z',
	...over
});

describe('tokenLife', () => {
	it('says a CLI token signs out after its window unused, in days', () => {
		expect(tokenLife(token({ idleSeconds: 7 * 86_400 }), NOW)).toEqual({
			text: 'signs out after 7 days unused',
			at: '',
			cli: true,
			expired: false
		});
		expect(tokenLife(token({ idleSeconds: 86_400 }), NOW).text).toBe(
			'signs out after 1 day unused'
		);
	});

	it('counts a window under a day in hours, never zero', () => {
		expect(tokenLife(token({ idleSeconds: 13 * 3_600 }), NOW).text).toBe(
			'signs out after 13 hours unused'
		);
		const fresh = token({ createdAt: '2026-11-18T11:59:30Z', idleSeconds: 60 });
		expect(tokenLife(fresh, NOW).text).toBe('signs out after 1 hour unused');
	});

	it('says how long any other token has left, the moment behind it', () => {
		const at = '2026-11-21T13:00:00Z';
		expect(tokenLife(token({ expiresAt: at }), NOW)).toEqual({
			text: 'expires in 3d',
			at: fmtAbs(at),
			cli: false,
			expired: false
		});
	});

	it('says a token with no end never expires', () => {
		expect(tokenLife(token(), NOW)).toEqual({
			text: 'never expires',
			at: '',
			cli: false,
			expired: false
		});
	});

	it('is expired from its expiry on, as the engine refuses it', () => {
		const at = '2026-11-18T10:00:00Z';
		expect(tokenLife(token({ expiresAt: at }), NOW)).toEqual({
			text: 'expired 2h ago',
			at: fmtAbs(at),
			cli: false,
			expired: true
		});
		expect(tokenLife(token({ expiresAt: '2026-11-18T12:00:00Z' }), NOW).expired).toBe(true);
		expect(tokenLife(token({ expiresAt: '2026-11-18T12:00:01Z' }), NOW).expired).toBe(false);
	});

	it('signs a CLI token out once its window passed since its last use', () => {
		const week = 7 * 86_400;
		const used = token({
			createdAt: '2026-11-01T00:00:00Z',
			lastUsedAt: '2026-11-10T12:00:00Z',
			idleSeconds: week
		});
		expect(tokenLife(used, NOW)).toEqual({
			text: 'signed out 1d ago',
			at: fmtAbs('2026-11-17T12:00:00Z'),
			cli: true,
			expired: true
		});
		// A use slides the window.
		expect(tokenLife({ ...used, lastUsedAt: '2026-11-11T12:00:01Z' }, NOW).expired).toBe(false);
	});

	it('counts the window of a CLI token never used from its creation', () => {
		const never = token({ createdAt: '2026-11-11T11:00:00Z', idleSeconds: 7 * 86_400 });
		expect(tokenLife(never, NOW).text).toBe('signed out 1h ago');
		expect(tokenLife(never, NOW).expired).toBe(true);
	});

	it('ends a CLI token at its fixed expiry too, its window still open', () => {
		const both = token({ idleSeconds: 7 * 86_400, expiresAt: '2026-11-18T11:00:00Z' });
		expect(tokenLife(both, NOW)).toMatchObject({
			text: 'expired 1h ago',
			cli: true,
			expired: true
		});
	});

	it('reads the clock when it is given none', () => {
		expect(tokenLife(token({ expiresAt: '2000-01-01T00:00:00Z' })).expired).toBe(true);
		expect(tokenLife(token({ expiresAt: '2999-01-01T00:00:00Z' })).expired).toBe(false);
	});
});
