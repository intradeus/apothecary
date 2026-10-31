import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import type { EdgeProps } from '@xyflow/svelte';
import { describe, expect, it, vi } from 'vitest';
import { LABEL_PAD, LABEL_ROW } from '$lib/domain/flowdoc';
import FlowLabeledEdge from './FlowLabeledEdge.svelte';

vi.mock('@xyflow/svelte', () => import('$test/fixtures/xyflow.svelte'));

const setup = (over: Partial<EdgeProps> = {}) =>
	render(FlowLabeledEdge, {
		id: 'e1',
		label: 'on_fail',
		sourceX: 100,
		sourceY: 50,
		targetX: 300,
		targetY: 80,
		...over
	} as unknown as EdgeProps);
const label = () => screen.getByTestId('edge-label');

describe('FlowLabeledEdge', () => {
	it('draws the path between its ends', () => {
		setup();
		expect(screen.getByTestId('edge-e1')).toHaveAttribute('d', 'M100 50 L300 80');
	});

	it('hangs the label under its source, not at the middle of the path', () => {
		setup();
		expect(label()).toHaveAttribute('data-x', '106');
		expect(label()).toHaveAttribute('data-y', String(50 + LABEL_PAD + LABEL_ROW / 2));
	});

	it('gives each edge of a stage a row of its own', () => {
		setup({ data: { row: 2 } });
		expect(label()).toHaveAttribute('data-y', String(50 + LABEL_PAD + 2.5 * LABEL_ROW));
	});

	it('sets a debug leaf’s label right of it', () => {
		setup({ data: { anchor: 'right' } });
		expect(label()).toHaveAttribute('data-y', '50');
	});

	it('draws no label without one', () => {
		setup({ label: undefined });
		expect(screen.queryByTestId('edge-label')).not.toBeInTheDocument();
	});

	it('opens what the edge is from its label', async () => {
		const pick = vi.fn();
		setup({ data: { pick } });
		await userEvent.click(screen.getByRole('button', { name: 'on_fail' }));
		expect(pick).toHaveBeenCalledOnce();
	});

	it('survives a label with nothing to open', async () => {
		setup();
		await userEvent.click(screen.getByRole('button', { name: 'on_fail' }));
		expect(label()).toBeInTheDocument();
	});
});
