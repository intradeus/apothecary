import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Run, RunCheckout } from '$lib/domain';
import { aRun, aRunStore } from '$test/fixtures';
import { aLiveMr, settled } from '$test/fixtures/run-components';
import RunMrs from './RunMrs.svelte';

vi.mock('$lib/api', () => ({ api: { runs: { mrStatus: vi.fn() } } }));
vi.mock('$lib/app/fleet.svelte', () => ({
	fleet: { projectById: new Map([['p1', { name: 'ledger-api' }]]) }
}));

const url = (iid: number) => `https://git.test/mr/${iid}`;
const mr = (iid: number, over: Partial<Run['mrs'][number]> = {}) =>
	({ repo: 'repo', iid, url: url(iid), branch: 'feat/x', ...over }) as Run['mrs'][number];
const checkout = (name: string, primary = false) => ({ name, primary }) as RunCheckout;

function setup(over: Partial<Run> = {}, opensMRs = false) {
	const run = aRun(over);
	render(RunMrs, { rs: aRunStore(run, { opensMRs }) });
	return run;
}

const lead = (name: string | RegExp) => screen.getByRole('link', { name });
const slots = () => screen.queryAllByText('not opened yet');
/** Opens the panel: everything the island holds. */
const open = () => userEvent.hover(document.querySelector('.pill-mr') as Element);

describe('RunMrs', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		vi.mocked(api.runs.mrStatus).mockResolvedValue([]);
	});

	afterEach(() => {
		vi.useRealTimers();
	});

	describe('no merge requests', () => {
		it('shows no island when the flow opens none', () => {
			const { container } = render(RunMrs, { rs: aRunStore(aRun(), { opensMRs: false }) });
			expect(container.querySelector('.pill-mr')).toBeNull();
		});

		it('asks the repository host nothing', async () => {
			setup();
			await settled();
			expect(api.runs.mrStatus).not.toHaveBeenCalled();
		});

		it('holds a "not yet" place when the flow will open one', () => {
			setup({}, true);
			const pill = screen.getByText('No merge request yet');
			expect(pill).toHaveAttribute('title', 'Not opened yet');
		});

		it('names the first checkout still without one', () => {
			setup({ checkouts: [checkout('api', true), checkout('web')] }, true);
			expect(screen.getByText('No merge request yet')).toHaveAttribute(
				'title',
				'api: not opened yet'
			);
		});
	});

	it('leads with the merge request and links it', () => {
		setup({ mrs: [mr(20)] });
		const chip = lead(/^!20/);
		expect(chip).toHaveAttribute('href', url(20));
		expect(chip).toHaveAttribute('target', '_blank');
	});

	it.each<[string, Partial<Run['mrs'][number]>, string]>([
		['the main checkout', { branch: 'feat/x', target: 'main' }, 'feat/x → main'],
		['with no target', { branch: 'feat/x' }, 'feat/x'],
		['another checkout', { repo: 'web', branch: 'feat/x' }, 'web: feat/x']
	])('describes one on %s', (_, over, title) => {
		setup({ mrs: [mr(20, over)] });
		expect(lead(/^!20/)).toHaveAttribute('title', title);
	});

	it('reads a run from before multi-repo by its one url', () => {
		setup({ mrUrl: url(7), branch: 'feat/old' });
		const chip = lead(/^MR|merge request/i);
		expect(chip).toHaveAttribute('href', url(7));
		expect(chip).toHaveAttribute('title', 'feat/old');
	});

	describe('an adopted merge request', () => {
		const adopted = { repo: 'repo', iid: 9, url: url(9), title: 'Their fix', author: 'ana' };

		it('leads, crediting its author', () => {
			setup({ adopted });
			expect(lead(/^!9/).getAttribute('title')).toMatch(/Their fix.*ana/);
		});

		it('is listed once when the run also lists it', async () => {
			setup({ adopted, mrs: [mr(9), mr(10)] });
			await open();
			expect(screen.getAllByText('!9', { selector: '.num' })).toHaveLength(1);
			expect(screen.getAllByText('!10', { selector: '.num' })).toHaveLength(1);
		});

		it('falls back to its number and an unknown author', () => {
			setup({ adopted: { repo: 'repo', iid: 9, url: url(9) } });
			expect(lead(/^!9/).getAttribute('title')).toMatch(/!9.*\?/);
		});

		it('fills the slot of its checkout', async () => {
			setup({ adopted, mrs: [mr(10, { repo: 'web' })], checkouts: [checkout('api', true)] }, true);
			await open();
			expect(slots()).toEqual([]);
		});
	});

	describe('checkouts still without one', () => {
		it('lists a slot only when the flow opens merge requests', async () => {
			const run = { mrs: [mr(20)], checkouts: [checkout('api', true), checkout('web')] };
			const { unmount } = render(RunMrs, { rs: aRunStore(aRun(run), { opensMRs: false }) });
			await open();
			expect(slots()).toEqual([]);
			unmount();

			setup(run, true);
			await open();
			expect(slots()).toHaveLength(1);
		});

		it('skips the checkouts that have one', async () => {
			setup(
				{
					mrs: [mr(20), mr(21, { repo: 'web' })],
					checkouts: [checkout('api', true), checkout('web'), checkout('docs')]
				},
				true
			);
			await open();
			expect(slots()).toHaveLength(1);
			expect(screen.getByText('docs: not opened yet')).toBeInTheDocument();
		});
	});

	it('finds the merge request of a main checkout filed under its own name', async () => {
		setup({ mrs: [mr(20, { repo: 'api' })], checkouts: [checkout('api', true)] }, true);
		await open();
		expect(slots()).toEqual([]);
	});

	describe('live status', () => {
		it('sinks a merged one, so the pill leads with what is in play', async () => {
			vi.mocked(api.runs.mrStatus).mockResolvedValue([
				aLiveMr({ url: url(20), state: 'merged' }),
				aLiveMr({ url: url(21), state: 'open' })
			]);
			setup({ mrs: [mr(20), mr(21)] });
			expect(lead(/^!20/)).toBeInTheDocument();
			expect(await screen.findByRole('link', { name: /^!21/ })).toBeInTheDocument();
			expect(screen.queryByRole('link', { name: /^!20/ })).not.toBeInTheDocument();
		});

		it.each([
			['its own url', 20, /conflict/i],
			['another url', 99, null]
		])('joins a status on %s', async (_, iid, want) => {
			vi.mocked(api.runs.mrStatus).mockResolvedValue([
				aLiveMr({ url: url(iid), mergeable: 'conflicts' })
			]);
			setup({ mrs: [mr(20), mr(21)] });
			await settled();
			await open();
			const found = screen.queryAllByText(/conflict/i);
			if (want) expect(found.length).toBeGreaterThan(0);
			else expect(found).toEqual([]);
		});

		it('names the repository with more than one to tell apart', async () => {
			vi.mocked(api.runs.mrStatus).mockResolvedValue([
				aLiveMr({ url: url(20), repo: 'billing' }),
				aLiveMr({ url: url(21), repo: 'portal' })
			]);
			setup({ mrs: [mr(20), mr(21)] });
			await settled();
			await open();
			expect(screen.getByText(/billing/)).toBeInTheDocument();
		});

		it('leaves it out with one', async () => {
			vi.mocked(api.runs.mrStatus).mockResolvedValue([aLiveMr({ url: url(20), repo: 'billing' })]);
			setup({ mrs: [mr(20)] });
			await settled();
			await open();
			expect(screen.queryByText(/billing/)).not.toBeInTheDocument();
		});

		it('keeps the island up through a repository host hiccup', async () => {
			vi.mocked(api.runs.mrStatus).mockRejectedValue(new Error('502'));
			setup({ mrs: [mr(20)] });
			await settled();
			expect(lead(/^!20/)).toBeInTheDocument();
		});
	});

	describe('the panel says where they land', () => {
		it.each<[string, Partial<Run>, string]>([
			[
				'the workshop and target',
				{ projectId: 'p1', mrs: [mr(20, { target: 'main' })] },
				'ledger-api → main'
			],
			[
				'the checkout for an unknown workshop',
				{ mrs: [mr(20, { target: 'main' })] },
				'repo → main'
			],
			[
				'the source branch with no target',
				{ mrs: [mr(20)], sourceBranch: 'develop' },
				'repo → develop'
			],
			['a word with neither', { mrs: [mr(20)] }, 'repo → default']
		])('naming %s', async (_, over, target) => {
			setup(over);
			await open();
			expect(screen.getByText(target)).toBeInTheDocument();
		});
	});

	describe('polling', () => {
		beforeEach(() => {
			vi.useFakeTimers();
		});

		const mine = () => vi.mocked(api.runs.mrStatus);

		it('reads the status on arrival and every thirty seconds', async () => {
			setup({ mrs: [mr(20)] });
			await vi.advanceTimersByTimeAsync(0);
			expect(mine()).toHaveBeenCalledOnce();

			await vi.advanceTimersByTimeAsync(29_999);
			expect(mine()).toHaveBeenCalledOnce();

			await vi.advanceTimersByTimeAsync(1);
			expect(mine()).toHaveBeenCalledTimes(2);
		});

		it('reads a finished run once and stops', async () => {
			setup({ mrs: [mr(20)], state: 'completed' });
			await vi.advanceTimersByTimeAsync(90_000);
			expect(mine()).toHaveBeenCalledOnce();
		});

		it('skips while the tab is hidden', async () => {
			vi.spyOn(document, 'hidden', 'get').mockReturnValue(true);
			setup({ mrs: [mr(20)] });
			await vi.advanceTimersByTimeAsync(90_000);
			expect(mine()).toHaveBeenCalledOnce();
		});

		it('stops when it leaves the page', async () => {
			const { unmount } = render(RunMrs, { rs: aRunStore(aRun({ mrs: [mr(20)] })) });
			await vi.advanceTimersByTimeAsync(0);
			unmount();
			await vi.advanceTimersByTimeAsync(90_000);
			expect(mine()).toHaveBeenCalledOnce();
		});
	});
});
