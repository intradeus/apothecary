import { describe, expect, it, vi } from 'vitest';
import { branchTail, manualRunBranch, manualRunName } from './manualRunName';

describe('manualRunName', () => {
	it('names a run for the minute it was started, in local time, zero-padded', () => {
		expect(manualRunName(new Date(2026, 10, 7, 9, 5, 59))).toBe('manual run - 2026-11-07 09:05');
		expect(manualRunName(new Date(2026, 0, 31, 23, 40))).toBe('manual run - 2026-01-31 23:40');
	});

	it('is now when not told', () => {
		vi.useFakeTimers();
		vi.setSystemTime(new Date(2027, 2, 4, 14, 3));
		expect(manualRunName()).toBe('manual run - 2027-03-04 14:03');
		vi.useRealTimers();
	});
});

describe('manualRunBranch', () => {
	it('names the fresh branch after the run, in the engine’s shape', () => {
		expect(manualRunBranch('manual run - 2026-11-17 18:40', 'gxjj')).toBe(
			'apothecary/manual-run-2026-11-17-18-40-gxjj'
		);
		expect(manualRunBranch('  Poke at the Cache! ', '0a1b')).toBe(
			'apothecary/poke-at-the-cache-0a1b'
		);
	});

	it('still names one for a name a branch cannot carry', () => {
		expect(manualRunBranch('¿?', 'gxjj')).toBe('apothecary/run-gxjj');
	});
});

describe('branchTail', () => {
	it('draws four of a run id’s letters', () => {
		const draw = vi
			.spyOn(crypto, 'getRandomValues')
			.mockImplementation((a) => ((a as Uint8Array).set([0, 31, 32, 255]), a));
		expect(branchTail()).toBe('0z0z');
		draw.mockRestore();
		expect(branchTail()).toMatch(/^[0-9a-hjkmnp-tv-z]{4}$/);
	});
});
