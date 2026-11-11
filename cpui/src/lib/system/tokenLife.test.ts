import { describe, expect, it } from 'vitest';
import type { Token } from '$lib/domain';
import { fmtAbs } from '$lib/core/format';
import { tokenLife } from './tokenLife';

const token = (over: Partial<Token> = {}): Token => ({
	id: 't',
	name: 'n',
	prefix: 'apo_abcdef',
	createdAt: '2026-11-01T00:00:00Z',
	...over
});

describe('tokenLife', () => {
	it('says a CLI token signs out after its window unused, in days', () => {
		expect(tokenLife(token({ idleSeconds: 7 * 86_400 }))).toEqual({
			text: 'signs out after 7 days unused',
			cli: true
		});
		expect(tokenLife(token({ idleSeconds: 86_400 })).text).toBe('signs out after 1 day unused');
	});

	it('counts a window under a day in hours, never zero', () => {
		expect(tokenLife(token({ idleSeconds: 12 * 3_600 })).text).toBe(
			'signs out after 12 hours unused'
		);
		expect(tokenLife(token({ idleSeconds: 60 })).text).toBe('signs out after 1 hour unused');
	});

	it('gives the fixed expiry of any other token', () => {
		const at = '2026-12-01T00:00:00Z';
		expect(tokenLife(token({ expiresAt: at }))).toEqual({
			text: `expires ${fmtAbs(at)}`,
			cli: false
		});
	});

	it('says nothing of a token that never expires', () => {
		expect(tokenLife(token())).toEqual({ text: '', cli: false });
	});
});
