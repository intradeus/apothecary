import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { FlowStep } from '$lib/domain';
import TicketStatusChip from './TicketStatusChip.svelte';

describe('TicketStatusChip', () => {
	it.each<[FlowStep, string]>([
		['todo', 'tint-muted'],
		['in_progress', 'tint-running'],
		['in_review', 'tint-attention'],
		['done', 'tint-success'],
		['blocked', 'tint-danger']
	])('tints the %s step with %s', (flowStep, tint) => {
		render(TicketStatusChip, { status: { id: 's', name: 'X', isDone: false, flowStep } });
		expect(screen.getByText('X')).toHaveClass(tint);
	});

	it('reads a done status with no step as finished', () => {
		render(TicketStatusChip, { status: { id: 's', name: 'Closed', isDone: true } });
		expect(screen.getByText('Closed')).toHaveClass('tint-success');
		expect(screen.getByText('Closed')).toHaveAttribute(
			'title',
			'Status in its tracker: Closed (done)'
		);
	});

	it('says when the ticket has not synced yet', () => {
		render(TicketStatusChip, {});
		expect(screen.getByText('not synced yet')).toBeInTheDocument();
	});
});
