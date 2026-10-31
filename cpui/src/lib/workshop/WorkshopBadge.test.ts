import { render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import { workshopVial } from '$lib/core/urls';
import WorkshopBadge from './WorkshopBadge.svelte';

vi.mock('$lib/api', () => ({ api: {} }));

describe('WorkshopBadge', () => {
	it('captions the vial and tones the tile from the workshop', () => {
		render(WorkshopBadge, { projectId: 'p1' });
		const tile = screen.getByText('Workshop').parentElement!;
		expect(tile.style.getPropertyValue('--tone')).toBe(workshopVial('p1').hue);
	});
});
