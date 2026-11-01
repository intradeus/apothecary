import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { TicketComment } from '$lib/domain';
import { deferred } from '$test/helpers';
import TicketComments from './TicketComments.svelte';

vi.mock('$lib/api', () => ({
	api: { tickets: { comments: vi.fn(), addComment: vi.fn() } }
}));

const comment = (over: Partial<TicketComment>): TicketComment => ({
	id: 'c1',
	author: 'ann',
	bodyMd: 'Looks **good**',
	origin: 'remote',
	createdAt: '2026-10-31T11:00:00Z',
	updatedAt: '2026-10-31T11:00:00Z',
	...over
});

beforeEach(() => {
	vi.resetAllMocks();
	vi.useFakeTimers({ now: new Date('2026-10-31T12:00:00Z'), shouldAdvanceTime: true });
});
afterEach(() => {
	vi.useRealTimers();
});

describe('TicketComments', () => {
	it('says it is loading, then that there are none', async () => {
		const d = deferred<TicketComment[]>();
		vi.mocked(api.tickets.comments).mockReturnValue(d.promise);
		render(TicketComments, { ticketId: 't1' });
		expect(screen.getByText('Loading comments…')).toBeInTheDocument();
		d.resolve([]);
		expect(await screen.findByText('No comments yet.')).toBeInTheDocument();
	});

	it('shows each comment: author, where it came from, when, and its markdown', async () => {
		vi.mocked(api.tickets.comments).mockResolvedValue([
			comment({ id: 'a' }),
			comment({ id: 'b', author: '', origin: 'user', bodyMd: 'mine' }),
			comment({
				id: 'c',
				author: 'builder',
				origin: 'apothecary',
				runId: '01RUN000000000000000ABCDEF'
			})
		]);
		render(TicketComments, { ticketId: 't1' });

		expect(await screen.findByText('ann')).toBeInTheDocument();
		expect(screen.getAllByText('good')[0].tagName).toBe('STRONG');
		expect(screen.getByText('tracker')).toHaveClass('tint-accent');
		expect(screen.getByText('You')).toBeInTheDocument();
		expect(screen.getByText('run')).toHaveClass('tint-running');
		expect(screen.getAllByText('1h ago')).toHaveLength(3);
		expect(screen.getByRole('link', { name: 'r_cdef' })).toHaveAttribute(
			'href',
			expect.stringContaining('01RUN000000000000000ABCDEF')
		);
	});

	it('posts a comment and shows the thread the tracker answers with', async () => {
		vi.mocked(api.tickets.comments).mockResolvedValue([]);
		vi.mocked(api.tickets.addComment).mockResolvedValue([comment({ bodyMd: 'on it' })]);
		render(TicketComments, { ticketId: 't1' });
		const box = screen.getByRole('textbox', { name: 'Add a comment' });
		await userEvent.type(box, '  on it ');
		await userEvent.click(screen.getByRole('button', { name: 'Comment' }));

		expect(api.tickets.addComment).toHaveBeenCalledWith('t1', 'on it');
		expect(await screen.findByText('on it')).toBeInTheDocument();
		expect(box).toHaveValue('');
	});

	it('keeps the draft when the tracker refuses it', async () => {
		vi.mocked(api.tickets.comments).mockResolvedValue([]);
		vi.mocked(api.tickets.addComment).mockRejectedValue(new Error('tracker down'));
		render(TicketComments, { ticketId: 't1' });
		const box = screen.getByRole('textbox', { name: 'Add a comment' });
		await userEvent.type(box, 'hello');
		await userEvent.click(screen.getByRole('button', { name: 'Comment' }));

		expect(await screen.findByText('tracker down')).toBeInTheDocument();
		expect(box).toHaveValue('hello');
	});

	it('does not post an empty comment', () => {
		vi.mocked(api.tickets.comments).mockResolvedValue([]);
		render(TicketComments, { ticketId: 't1' });
		expect(screen.getByRole('button', { name: 'Comment' })).toBeDisabled();
	});

	it('reloads the thread when the ticket syncs again', async () => {
		vi.mocked(api.tickets.comments).mockResolvedValue([]);
		const { rerender } = render(TicketComments, { ticketId: 't1', syncedAt: 'a' });
		await waitFor(() => expect(api.tickets.comments).toHaveBeenCalledOnce());
		await rerender({ ticketId: 't1', syncedAt: 'b' });
		await waitFor(() => expect(api.tickets.comments).toHaveBeenCalledTimes(2));
	});
});
