import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { aUnit } from '$test/fixtures/swarm';
import SectorMap from './SectorMap.svelte';

const NAMES: Record<string, string> = { p1: 'Billing', p2: 'Docs' };
const nameOf = (id: string) => NAMES[id] ?? id;

const units = [
	aUnit({ runId: 'r1', projectId: 'p1', title: 'One' }),
	aUnit({ runId: 'r2', projectId: 'p1', title: 'Two' }),
	aUnit({ runId: 'r3', projectId: 'p2', title: 'Three' })
];

const mount = (selected: string | null = null) => {
	const onselect = vi.fn();
	render(SectorMap, { units, nameOf, selected, onselect });
	return onselect;
};

describe('SectorMap', () => {
	it('labels a sector per workshop', () => {
		mount();
		expect(screen.getByText('Billing')).toBeInTheDocument();
		expect(screen.getByText('Docs')).toBeInTheDocument();
	});

	it('puts a focusable unit on the map for each unit', () => {
		mount();
		const buttons = screen.getAllByRole('button');
		expect(buttons).toHaveLength(3);
		for (const b of buttons) expect(b).toHaveAttribute('tabindex', '0');
	});

	it('is one labelled group', () => {
		mount();
		expect(screen.getByRole('group', { name: 'Sandboxes by workshop' })).toBeInTheDocument();
	});

	it('tints neighbouring sectors apart', () => {
		const { container } = render(SectorMap, { units, nameOf, onselect: vi.fn() });
		expect(container.querySelectorAll('.sector')).toHaveLength(2);
		expect(container.querySelectorAll('.sector.alt')).toHaveLength(1);
	});

	it('selects the unit clicked', async () => {
		const onselect = mount();
		await userEvent.click(screen.getByRole('button', { name: /^Three,/ }));
		expect(onselect).toHaveBeenCalledExactlyOnceWith('r3');
	});

	it('shows which unit is selected', () => {
		mount('r2');
		expect(screen.getByRole('button', { name: /^Two,/ })).toHaveAttribute('aria-pressed', 'true');
		expect(screen.getByRole('button', { name: /^One,/ })).toHaveAttribute('aria-pressed', 'false');
	});
});
