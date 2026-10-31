import { render, screen } from '@testing-library/svelte';
import type { NodeProps } from '@xyflow/svelte';
import { expect, it, vi } from 'vitest';
import FlowHubNode from './FlowHubNode.svelte';

vi.mock('@xyflow/svelte', () => import('$test/fixtures/xyflow.svelte'));

const setup = (data: Record<string, unknown>) =>
	render(FlowHubNode, { data } as unknown as NodeProps);

it('says what the run is waiting on', () => {
	setup({ label: 'waiting', sub: 'on the merge request', title: 'The run is open' });
	expect(screen.getByText(/waiting/)).toBeInTheDocument();
	expect(screen.getByText('on the merge request')).toBeInTheDocument();
	expect(screen.getByTitle('The run is open')).toBeInTheDocument();
});

it('takes routes in and connects none out: its branches are drawn', () => {
	setup({ label: 'waiting' });
	const [into, out] = screen.getAllByTestId('handle');
	expect(into).toHaveAttribute('data-connectable', 'true');
	expect(out).toHaveAttribute('data-connectable', 'false');
});
