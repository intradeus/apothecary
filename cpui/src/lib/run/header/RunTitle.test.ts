import { render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import type { Run } from '$lib/domain';
import { aRun, aTicket } from '$test/fixtures';
import { aHold } from '$test/fixtures/run-components';
import { AT, anEvent, aViewStore, NOW } from '$test/fixtures/run-views';
import RunTitle from './RunTitle.svelte';

vi.mock('$lib/api', () => ({ api: {} }));
vi.mock('$lib/app/fleet.svelte', () => ({
	fleet: { projectById: new Map([['known', { name: 'From the fleet' }]]) }
}));

const setup = (over: Partial<Run> = {}, store: Parameters<typeof aViewStore>[1] = {}) => {
	const rs = aViewStore(aRun(over), store);
	render(RunTitle, { rs });
	return rs;
};
const link = (name: string | RegExp) => screen.getByRole('link', { name });
const noLink = (name: string | RegExp) => screen.queryByRole('link', { name });

describe('RunTitle', () => {
	it('titles the page with the whole run title, on hover too', () => {
		setup({ title: 'Add idempotency keys' });
		const h1 = screen.getByRole('heading', { level: 1 });
		expect(h1).toHaveTextContent('Add idempotency keys');
		expect(h1).toHaveAttribute('title', 'Add idempotency keys');
	});

	it('keeps a one-word title whole', () => {
		setup({ title: 'Fix' });
		expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent(/^Fix$/);
	});

	describe('the breadcrumb', () => {
		it('starts at the desk and ends at the short id, unlinked', () => {
			setup({ id: '01TESTRUN0000000000000ABCD' });
			expect(link('Desk')).toHaveAttribute('href', '/');
			expect(screen.getByText('r_abcd')).toHaveAttribute('aria-current', 'page');
		});

		it('names the workshop from the fleet, and links it', () => {
			setup({ projectId: 'known' });
			expect(link('From the fleet')).toHaveAttribute('href', expect.stringContaining('known'));
		});

		it("falls back to the store's copy until the fleet has landed", () => {
			setup({ projectId: 'unknown' }, { projectName: 'From the store' });
			expect(link('From the store')).toBeInTheDocument();
		});

		it('falls back to a word when neither knows', () => {
			setup({ projectId: 'unknown' });
			expect(link('workshop')).toBeInTheDocument();
		});

		it('skips the workshop for a run outside one', () => {
			setup({ projectId: undefined });
			expect(screen.getAllByRole('link')).toHaveLength(1);
		});
	});

	describe('the links beside it', () => {
		it('links the ticket by a generic name until its key is known', () => {
			setup({ ticketId: 't1' });
			expect(link(/^ticket/)).toHaveAttribute('href', '/tickets/t1');
		});

		it('names the ticket by its tracker key, with its title on hover', () => {
			const rs = aViewStore(aRun({ ticketId: 't1' }));
			rs.ticket = aTicket({ title: 'The title', source: { key: 'ABC-12' } as never });
			render(RunTitle, { rs });
			expect(link(/^ABC-12/)).toHaveAttribute('title', 'The title');
		});

		it('has no ticket link without a ticket', () => {
			setup();
			expect(noLink(/^ticket/)).not.toBeInTheDocument();
		});

		it('opens the tracker issue in a new tab', () => {
			setup({ ticketUrl: 'https://tracker.test/1' });
			const issue = link(/tracker issue/);
			expect(issue).toHaveAttribute('href', 'https://tracker.test/1');
			expect(issue).toHaveAttribute('target', '_blank');
		});

		it('has no tracker link without an issue', () => {
			setup();
			expect(screen.queryByText(/tracker issue/)).not.toBeInTheDocument();
		});
	});

	describe('the status line', () => {
		const ended = (seq: number, type = 'run_completed') => ({
			...anEvent(seq, type),
			ts: new Date(NOW + seq * 60_000).toISOString()
		});

		it('counts how long the run has brewed, live', () => {
			setup({ createdAt: AT }, { now: NOW + 125_000 });
			expect(screen.getByText('2:05')).toBeInTheDocument();
		});

		it('stops the clock at the last word of a finished run', () => {
			setup(
				{ createdAt: AT, state: 'completed' },
				{ now: NOW + 9_999_000, events: [ended(1), ended(3, 'run_failed')] }
			);
			expect(screen.getByText('3:00')).toBeInTheDocument();
		});

		it('stops it at the last update for a run that never said one', () => {
			const updatedAt = new Date(NOW + 60_000).toISOString();
			setup({ createdAt: AT, updatedAt, state: 'abandoned' }, { now: NOW + 9_999_000 });
			expect(screen.getByText('1:00')).toBeInTheDocument();
		});

		it('says what the run has spent', () => {
			const spend = anEvent(1, 'stage_completed', { usage: { cost_usd: 0.75 }, turns_used: 5 });
			setup({}, { events: [spend] });
			expect(screen.getByText(/\$0\.750 · 5 turns/)).toBeInTheDocument();
		});

		it('says turn, singular, for one', () => {
			const spend = anEvent(1, 'stage_completed', { turns_used: 1 });
			setup({}, { events: [spend] });
			expect(screen.getByText(/1 turn$/)).toBeInTheDocument();
		});

		it('adds the lore to a failed run only', () => {
			setup({ state: 'failed' });
			expect(screen.getByText(/spoiled/i)).toBeInTheDocument();
		});
	});

	describe('a take-over', () => {
		it('says the run is in your hands, and for how long', () => {
			setup({ hold: aHold({ since: AT }) }, { now: NOW + 65_000 });
			expect(screen.getByText(/in your hands/i)).toBeInTheDocument();
			expect(screen.getByText('1:05', { selector: '.pill .tick' })).toBeInTheDocument();
		});

		it('drops the pill once the run is over', () => {
			setup({ hold: aHold(), state: 'failed' });
			expect(screen.queryByText(/in your hands/i)).not.toBeInTheDocument();
		});

		it('shows nothing with no take-over', () => {
			setup();
			expect(screen.queryByText(/in your hands/i)).not.toBeInTheDocument();
		});
	});

	describe('skillsets', () => {
		it('lists each with its version', () => {
			setup({
				skillsets: [
					{ id: 's1', name: 'Reviewing', seq: 3 },
					{ id: 's2', name: 'Testing', seq: 1 }
				] as Run['skillsets']
			});
			expect(screen.getByText('Reviewing')).toHaveTextContent('Reviewing v3');
			expect(screen.getByText('Testing')).toHaveTextContent('Testing v1');
		});

		it('shows nothing with none', () => {
			setup({ skillsets: [] });
			expect(screen.queryByTitle('Skillsets installed in this run')).not.toBeInTheDocument();
		});
	});
});
