import { describe, expect, it } from 'vitest';
import { drawer } from './drawer.svelte';

describe('the assistant drawer', () => {
	it('starts closed', () => {
		expect(drawer.open).toBe(false);
	});
});
