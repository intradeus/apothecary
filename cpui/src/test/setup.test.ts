import { getConfig, waitFor } from '@testing-library/svelte/pure';
import { describe, expect, it, vi } from 'vitest';

import { ASYNC_TIMEOUT } from './setup';

// A loaded machine (`task ci` runs every suite at once) is several times
// slower than an idle one: a wait whose condition holds a little past a
// second must still hold, whichever library waits.
describe('every async wait', () => {
	const holdsAfter = (ms: number) => {
		const start = Date.now();
		return () => expect(Date.now() - start).toBeGreaterThanOrEqual(ms);
	};

	it("gives Testing Library's waitFor and findBy* the shared budget", async () => {
		expect(getConfig().asyncUtilTimeout).toBe(ASYNC_TIMEOUT);
		await waitFor(holdsAfter(1200));
	});

	it("gives vitest's vi.waitFor the same budget", async () => {
		await vi.waitFor(holdsAfter(1200));
	});

	it('still honours a budget a test gives itself', async () => {
		await expect(vi.waitFor(holdsAfter(1200), { timeout: 100 })).rejects.toThrow();
		await expect(vi.waitFor(holdsAfter(1200), 100)).rejects.toThrow();
	});
});
