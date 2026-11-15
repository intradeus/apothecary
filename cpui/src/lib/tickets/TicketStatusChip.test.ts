import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import TicketStatusChip from './TicketStatusChip.svelte';

describe('TicketStatusChip', () => {
	it('tints an open status muted', () => {
		render(TicketStatusChip, { status: 'In Review', done: false });
		expect(screen.getByText('In Review')).toHaveClass('tint-muted', 'sm');
		expect(screen.getByText('In Review')).toHaveAttribute(
			'title',
			'Status in its tracker: In Review'
		);
	});

	it('reads a done status as finished', () => {
		render(TicketStatusChip, { status: 'Closed', done: true, size: 'md' });
		expect(screen.getByText('Closed')).toHaveClass('tint-success');
		expect(screen.getByText('Closed')).not.toHaveClass('sm');
		expect(screen.getByText('Closed')).toHaveAttribute(
			'title',
			'Status in its tracker: Closed (done)'
		);
	});

	it('shows a dash when the tracker gives no status', () => {
		render(TicketStatusChip, { status: '', done: false });
		expect(screen.getByText('—')).toHaveAttribute(
			'title',
			'The tracker gives this issue no status'
		);
	});
});
