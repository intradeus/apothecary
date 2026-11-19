import { describe, expect, it } from 'vitest';
import { ticketFill, type TicketFields } from '$lib/domain';
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

describe('FillGuard', () => {
	it('applies a fill, marks what changed, and undoes it', () => {
		const p = page(blank);
		p.guard.ask();
		expect(p.guard.offer({ title: 'Fix the login page', change_type: 'fix' })).toBe(true);
		expect(p.get()).toEqual({ ...blank, title: 'Fix the login page', changeType: 'fix' });
		expect(p.guard.changed).toEqual(['title', 'changeType']);
		p.guard.revert();
		expect(p.get()).toEqual(blank);
		expect(p.guard.changed).toEqual([]);
		p.guard.revert(); // nothing left to undo
		expect(p.get()).toEqual(blank);
	});

	it('holds a fill when the page changed since the question, and never overwrites on its own', () => {
		const p = page(blank);
		p.guard.ask();
		p.edit({ goal: 'my newer notes', typeId: 'bug' });
		p.guard.offer({ title: 'Fix it', goal: 'the assistant’s goal' });
		expect(p.get().title).toBe('');
		expect(p.guard.pending?.changed).toEqual(['title', 'goal']);
		// Applied on a click, to the page as it stands: the type it does not
		// set keeps the newer edit.
		p.guard.applyPending();
		expect(p.get()).toEqual({
			title: 'Fix it',
			goal: 'the assistant’s goal',
			changeType: 'feat',
			typeId: 'bug'
		});
		expect(p.guard.pending).toBeNull();
		p.guard.revert();
		expect(p.get().goal).toBe('my newer notes');
	});

	it('discards a held fill, ignores another page’s, and settles', () => {
		const p = page(blank);
		p.guard.ask();
		p.edit({ title: 'mine' });
		p.guard.offer({ title: 'theirs' });
		p.guard.discardPending();
		expect(p.get().title).toBe('mine');
		expect(p.guard.pending).toBeNull();
		p.guard.applyPending(); // nothing held
		expect(p.guard.offer('not a fill')).toBe(false);
		p.guard.offer({ title: 'again' }); // no question asked since: applied
		p.guard.settle();
		expect(p.guard.undo).toBeNull();
		expect(p.guard.changed).toEqual([]);
	});

	it('takes a second fill in one answer as a correction of the first', () => {
		const p = page(blank);
		p.guard.ask();
		expect(p.guard.asking).toBe(true);
		p.guard.offer({ title: 'First try' });
		p.guard.offer({ title: 'Fix the login page', goal: 'It works.' });
		// Not held: the page changed only by the first fill.
		expect(p.guard.pending).toBeNull();
		expect(p.get()).toMatchObject({ title: 'Fix the login page', goal: 'It works.' });
		expect(p.guard.changed).toEqual(['title', 'goal']);
		// Undo goes back to before the answer, not to its first fill.
		p.guard.revert();
		expect(p.get()).toEqual(blank);
		p.guard.answered();
		expect(p.guard.asking).toBe(false);
	});

	it('holds a fill after an edit made between two fills', () => {
		const p = page(blank);
		p.guard.ask();
		p.guard.offer({ title: 'First' });
		p.edit({ goal: 'mine, after the fill' });
		p.guard.offer({ goal: 'theirs' });
		expect(p.guard.pending?.changed).toEqual(['goal']);
		expect(p.get().goal).toBe('mine, after the fill');
	});

	it('applies an earlier answer’s fill when asked, with its own Undo', () => {
		const p = page(blank);
		expect(p.guard.applyNow('not a fill')).toBe(false);
		p.edit({ title: 'mine' });
		expect(p.guard.applyNow({ goal: 'from the record' })).toBe(true);
		expect(p.get()).toEqual({ ...blank, title: 'mine', goal: 'from the record' });
		p.guard.revert();
		expect(p.get()).toEqual({ ...blank, title: 'mine' });
	});

	it('settles on a save: nothing to undo, no answer to wait for', () => {
		const p = page(blank);
		p.guard.ask();
		p.guard.offer({ title: 'T' });
		p.guard.settle();
		expect(p.guard.asking).toBe(false);
		expect(p.guard.undo).toBeNull();
		p.guard.revert();
		expect(p.get().title).toBe('T');
	});
});
