import { afterEach, describe, expect, it } from 'vitest';
import { NEUTRAL_MIX, noise } from './noiseState.svelte';

describe('noise', () => {
	afterEach(() => {
		noise.blue = NEUTRAL_MIX;
		noise.speed = 1;
	});

	it('rests half ember, half sky, at the resting pace', () => {
		expect(NEUTRAL_MIX).toBe(50);
		expect(noise).toEqual({ blue: 50, speed: 1 });
	});

	it('is one shared object a page writes to', async () => {
		noise.blue = 100;
		noise.speed = 0;
		const again = await import('./noiseState.svelte');
		expect(again.noise).toEqual({ blue: 100, speed: 0 });
	});
});
