import { describe, expect, it } from 'vitest';
import { activeDesk } from './active.svelte';
import { DeskState } from './desk.svelte';

describe('activeDesk', () => {
	it('starts with no desk on screen, so the Layout button stays away', () => {
		expect(activeDesk.desk).toBeNull();
	});

	it('holds the desk a page registers, and lets it go', () => {
		const desk = new DeskState();
		activeDesk.desk = desk;
		desk.preset = 'dock';
		expect(activeDesk.desk.preset).toBe('dock');
		activeDesk.desk = null;
		expect(activeDesk.desk).toBeNull();
	});
});
