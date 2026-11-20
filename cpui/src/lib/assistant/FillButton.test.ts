import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { ticketFill, type TicketFields } from '$lib/domain';
import FillButton from './FillButton.svelte';
import { FillGuard } from './fillGuard.svelte';

function page(init: TicketFields) {
	let cur = { ...init };
	const guard = new FillGuard<TicketFields>(
		() => ({ ...cur }),
		(v) => (cur = { ...v }),
		ticketFill
	);
	return { guard, get: () => cur, edit: (f: Partial<TicketFields>) => (cur = { ...cur, ...f }) };
}

const blank = { title: '', goal: 'my notes', changeType: 'feat', typeId: '' };

describe('FillButton', () => {
	it('opens the fill modal', async () => {
		const onclick = vi.fn();
		render(FillButton, { guard: page(blank).guard, onclick });
		await userEvent.click(screen.getByRole('button', { name: 'Fill with the assistant' }));
		expect(onclick).toHaveBeenCalledOnce();
	});

	it('says what a fill changed, and undoes it', async () => {
		const p = page(blank);
		render(FillButton, { guard: p.guard, onclick: vi.fn() });
		p.guard.ask();
		p.guard.offer({ title: 'Fix the login page' });
		expect(await screen.findByRole('status')).toHaveTextContent('Filled: title');
		await userEvent.click(screen.getByRole('button', { name: 'Undo' }));
		expect(p.get()).toEqual(blank);
		expect(screen.queryByRole('status')).toBeNull();
	});

	it('holds a fill over newer edits until the person picks', async () => {
		const p = page(blank);
		render(FillButton, { guard: p.guard, onclick: vi.fn() });
		p.guard.ask();
		p.edit({ title: 'mine' });
		p.guard.offer({ title: 'theirs' });
		expect(await screen.findByRole('status')).toHaveTextContent('You changed the page');
		await userEvent.click(screen.getByRole('button', { name: 'Keep mine' }));
		expect(p.get().title).toBe('mine');
		p.guard.ask();
		p.edit({ goal: 'newer' });
		p.guard.offer({ title: 'theirs' });
		await userEvent.click(await screen.findByRole('button', { name: 'Fill it anyway' }));
		expect(p.get()).toMatchObject({ title: 'theirs', goal: 'newer' });
	});
});
