import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { workshopVial } from '$lib/core/urls';
import Vial from './Vial.svelte';

const vial = (root: HTMLElement) => root.querySelector<HTMLElement>('.vial')!;

describe('Vial', () => {
	it('fills to the level and hue of its workshop', () => {
		const { container } = render(Vial, { projectId: 'p-1' });
		const { hue, fill } = workshopVial('p-1');
		expect(vial(container).style.getPropertyValue('--tone')).toBe(hue);
		expect(vial(container).style.getPropertyValue('--f')).toBe(`${100 - fill}%`);
	});

	it('follows a change of workshop', async () => {
		const { container, rerender } = render(Vial, { projectId: 'p-1' });
		await rerender({ projectId: 'another-workshop' });
		const { fill } = workshopVial('another-workshop');
		expect(vial(container).style.getPropertyValue('--f')).toBe(`${100 - fill}%`);
	});

	it('empties when the workshop is idle', () => {
		const { container } = render(Vial, { projectId: 'p-1', idle: true });
		expect(vial(container)).toHaveClass('idle');
	});

	it('is decoration, hidden from assistive tech', () => {
		const { container } = render(Vial, { projectId: 'p-1' });
		expect(vial(container)).toHaveAttribute('aria-hidden', 'true');
	});
});
