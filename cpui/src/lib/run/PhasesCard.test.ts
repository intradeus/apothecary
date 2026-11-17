import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { MergeRequestLive, RunPhase } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { aLiveMr, aPhase } from '$test/fixtures/run-components';
import { deferred } from '$test/helpers';
import PhasesCard from './PhasesCard.svelte';

vi.mock('$lib/api', () => ({
	api: {
		runs: { mrStatus: vi.fn(), phases: vi.fn(), startPhase: vi.fn() },
		system: { signals: vi.fn() }
	}
}));

function load(phases: RunPhase[], mrs: MergeRequestLive[] = []) {
	vi.mocked(api.runs.phases).mockResolvedValue(phases);
	vi.mocked(api.runs.mrStatus).mockResolvedValue(mrs);
}
async function show(
	phases: RunPhase[],
	mrs: MergeRequestLive[] = [],
	props: { idle?: boolean; ended?: boolean } = {}
) {
	load(phases, mrs);
	const view = render(PhasesCard, { runId: 'r1', idle: true, ...props });
	await screen.findByRole('article', { name: phases[0].id });
	return view;
}
const runNow = () => screen.getByRole('button', { name: 'Run now' });
const chain = (id: string) => screen.getByRole('article', { name: id });
const section = (name: string) => screen.queryByRole('region', { name });

describe('PhasesCard', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		toasts.list = [];
		vi.mocked(api.runs.startPhase).mockResolvedValue(undefined as never);
		vi.mocked(api.system.signals).mockResolvedValue([]);
	});

	it('shows nothing for a flow with no chains', async () => {
		load([]);
		const { container } = render(PhasesCard, { runId: 'r1', idle: true });

		await vi.waitFor(() => expect(api.runs.phases).toHaveBeenCalled());
		expect(container).toHaveTextContent('');
	});

	it('asks for this run', async () => {
		await show([aPhase()]);

		expect(api.runs.phases).toHaveBeenCalledWith('r1');
		expect(api.runs.mrStatus).toHaveBeenCalledWith('r1');
	});

	describe('open merge requests', () => {
		it('lists the open ones with their status spelled out', async () => {
			await show(
				[aPhase()],
				[
					aLiveMr({ url: 'https://git.test/20', branch: 'feat/a', mergeable: 'conflicts' }),
					aLiveMr({ url: 'https://git.test/21', branch: 'feat/b', state: 'merged' })
				]
			);

			expect(screen.getByRole('link', { name: /feat\/a/ })).toHaveAttribute(
				'href',
				'https://git.test/20'
			);
			expect(screen.getByText('conflicts with main')).toBeInTheDocument();
			expect(screen.queryByRole('link', { name: /feat\/b/ })).not.toBeInTheDocument();
		});

		it('names the repo of a secondary checkout', async () => {
			await show([aPhase()], [aLiveMr({ repo: 'docs', branch: 'feat/a' })]);
			expect(screen.getByRole('link', { name: /docs: feat\/a/ })).toBeInTheDocument();
		});
	});

	describe('one card per chain, by kind', () => {
		it('puts each chain under its kind, main chain first, then watchers, then outcomes', async () => {
			await show([
				aPhase({ id: 'merged', kind: 'outcome', signal: 'mr_merged', then: 'done' }),
				aPhase({ id: 'fix-ci' }),
				aPhase({ id: 'main', kind: 'main', signal: '' }),
				aPhase({ id: 'address-review', signal: 'review_comment' })
			]);

			expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual([
				'Main chain',
				'Watchers',
				'Outcomes'
			]);
			const cards = (name: string) =>
				within(section(name)!)
					.getAllByRole('article')
					.map((a) => a.getAttribute('aria-label'));
			expect(cards('Main chain')).toEqual(['main']);
			expect(cards('Watchers')).toEqual(['fix-ci', 'address-review']);
			expect(cards('Outcomes')).toEqual(['merged']);
			expect(within(section('Watchers')!).getByText('2')).toBeInTheDocument();
		});

		it('leaves out a kind the flow has none of', async () => {
			await show([aPhase({ id: 'fix-ci' })]);

			expect(section('Watchers')).toBeInTheDocument();
			expect(section('Main chain')).not.toBeInTheDocument();
			expect(section('Outcomes')).not.toBeInTheDocument();
		});

		it('uses the signals’ titles once they are in', async () => {
			vi.mocked(api.system.signals).mockResolvedValue([
				{ slug: 'ci_failed', title: 'CI failing' }
			] as never);
			await show([aPhase({ id: 'p' })]);

			expect(await within(chain('p')).findByText('CI failing')).toBeInTheDocument();
		});

		it('keeps the raw names when the titles cannot be read', async () => {
			vi.mocked(api.system.signals).mockRejectedValue(new Error('nope'));
			await show([aPhase({ id: 'p' })]);

			expect(within(chain('p')).getByText('ci_failed')).toBeInTheDocument();
		});
	});

	describe('Run now', () => {
		it('starts a watcher when no chain holds the run', async () => {
			await show([aPhase({ id: 'fix-ci' })]);
			expect(runNow()).toBeEnabled();
			expect(runNow()).toHaveAttribute('title', 'Start it now, whether or not its signal holds');

			await userEvent.click(runNow());

			expect(api.runs.startPhase).toHaveBeenCalledExactlyOnceWith('r1', 'fix-ci', '');
			expect(toasts.list.map((t) => t.text)).toEqual(['fix-ci started']);
		});

		it('waits while the run is not idle', async () => {
			await show([aPhase()], [], { idle: false });

			expect(runNow()).toBeDisabled();
			expect(runNow()).toHaveAttribute('title', 'Another chain holds the run');
		});

		// One cursor: a chain parked at a wait point still holds it.
		it('waits while another chain holds the cursor, even parked', async () => {
			await show([aPhase({ id: 'p' }), aPhase({ id: 'q', live: { parkedAt: 'review' } })]);

			const [free, holding] = screen.getAllByRole('button', { name: 'Run now' });
			expect(free).toBeDisabled();
			expect(free).toHaveAttribute('title', 'Another chain holds the run');
			expect(holding).toBeDisabled();
			expect(holding).toHaveAttribute('title', 'This watcher has a run in progress or parked');
		});

		it('stops at its max', async () => {
			await show([aPhase({ max: 2, runs: { '': 2 } })]);

			expect(runNow()).toBeDisabled();
			expect(runNow()).toHaveAttribute('title', 'This watcher reached its max');
		});

		it('never stops an unlimited one', async () => {
			await show([aPhase({ max: -1, runs: { '': 99 } })]);
			expect(runNow()).toBeEnabled();
		});

		it('counts the max per open merge request', async () => {
			const mrs = [
				aLiveMr({ repo: 'repo', url: 'https://git.test/1' }),
				aLiveMr({ repo: 'docs', url: 'https://git.test/2' })
			];
			const { unmount } = await show([aPhase({ max: 1, runs: { repo: 1 } })], mrs);
			expect(runNow()).toBeEnabled();
			unmount();

			await show([aPhase({ max: 1, runs: { repo: 1, docs: 1 } })], mrs);
			expect(runNow()).toBeDisabled();
		});

		it('holds every button while one starts', async () => {
			const starting = deferred<void>();
			vi.mocked(api.runs.startPhase).mockReturnValue(starting.promise as never);
			await show([aPhase({ id: 'p' }), aPhase({ id: 'q' })]);
			const [p, q] = screen.getAllByRole('button', { name: 'Run now' });

			await userEvent.click(p);

			expect(p).toBeDisabled();
			expect(p).toHaveAttribute('aria-busy', 'true');
			expect(q).toBeDisabled();
			expect(q).toHaveAttribute('aria-busy', 'false');

			starting.resolve();
			await vi.waitFor(() => expect(p).toBeEnabled());
		});

		it('reads the run again afterwards', async () => {
			await show([aPhase()]);
			vi.mocked(api.runs.phases).mockResolvedValue([aPhase({ live: {} })]);

			await userEvent.click(runNow());

			expect(await screen.findByText('running now')).toBeInTheDocument();
		});

		it('says why it could not start, and reads the run again', async () => {
			vi.mocked(api.runs.startPhase).mockRejectedValue(new Error('cursor held'));
			await show([aPhase({ id: 'fix-ci' })]);
			vi.mocked(api.runs.phases).mockClear();

			await userEvent.click(runNow());

			const card = await screen.findByRole('alert');
			expect(card).toHaveTextContent('fix-ci could not start.');
			expect(within(card).getByText('cursor held')).toBeInTheDocument();
			expect(toasts.list).toEqual([]);
			await vi.waitFor(() => expect(api.runs.phases).toHaveBeenCalledOnce());
		});

		it('dismisses the error', async () => {
			vi.mocked(api.runs.startPhase).mockRejectedValue(new Error('cursor held'));
			await show([aPhase()]);
			await userEvent.click(runNow());
			await screen.findByRole('alert');

			await userEvent.click(screen.getByRole('button', { name: 'Dismiss' }));

			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
		});

		it('clears the last error on the next try', async () => {
			vi.mocked(api.runs.startPhase).mockRejectedValueOnce(new Error('cursor held'));
			await show([aPhase()]);
			await userEvent.click(runNow());
			await screen.findByRole('alert');

			await userEvent.click(runNow());

			await vi.waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument());
		});
	});

	describe('with several open merge requests', () => {
		const mrs = [
			aLiveMr({ repo: 'repo', branch: 'feat/a', url: 'https://git.test/1' }),
			aLiveMr({ repo: 'docs', branch: 'feat/b', url: 'https://git.test/2' })
		];
		const choice = (name: string) => screen.getByRole('button', { name });

		it('asks which one before starting', async () => {
			await show([aPhase({ id: 'p' })], mrs);

			await userEvent.click(runNow());

			expect(screen.getByText('On which merge request?')).toBeInTheDocument();
			expect(choice('feat/a')).toBeInTheDocument();
			expect(choice('docs: feat/b')).toBeInTheDocument();
			expect(api.runs.startPhase).not.toHaveBeenCalled();
		});

		it('starts the phase asked for on the one picked', async () => {
			await show([aPhase({ id: 'p' }), aPhase({ id: 'q' })], mrs);

			await userEvent.click(screen.getAllByRole('button', { name: 'Run now' })[1]);
			await userEvent.click(choice('docs: feat/b'));

			expect(api.runs.startPhase).toHaveBeenCalledExactlyOnceWith('r1', 'q', 'docs');
			await vi.waitFor(() =>
				expect(screen.queryByText('On which merge request?')).not.toBeInTheDocument()
			);
		});

		it('does not ask with one open and one merged', async () => {
			await show([aPhase({ id: 'p' })], [mrs[0], { ...mrs[1], state: 'merged' }]);

			await userEvent.click(runNow());

			expect(api.runs.startPhase).toHaveBeenCalledExactlyOnceWith('r1', 'p', '');
		});
	});

	describe('when the repository cannot be read', () => {
		it('says so on the first load', async () => {
			vi.mocked(api.runs.phases).mockResolvedValue([aPhase()]);
			vi.mocked(api.runs.mrStatus).mockRejectedValue(new Error('host down'));
			render(PhasesCard, { runId: 'r1', idle: true });

			expect(await screen.findByRole('alert')).toHaveTextContent(
				'The repository could not be read.'
			);
		});

		it('says so on a later read, keeping what it had', async () => {
			await show([aPhase({ id: 'p' })]);
			vi.mocked(api.runs.mrStatus).mockRejectedValue(new Error('host down'));

			await userEvent.click(runNow());

			expect(await screen.findByRole('alert')).toHaveTextContent(
				'The repository could not be read.'
			);
			expect(screen.getByText('p')).toBeInTheDocument();
		});

		it('clears the card when Retry works', async () => {
			await show([aPhase()]);
			vi.mocked(api.runs.mrStatus).mockRejectedValueOnce(new Error('host down'));
			await userEvent.click(runNow());
			const card = await screen.findByRole('alert');

			await userEvent.click(within(card).getByRole('button', { name: /Retry/ }));

			await vi.waitFor(() => expect(api.runs.mrStatus).toHaveBeenCalledTimes(3));
			await vi.waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument());
		});
	});

	describe('polling', () => {
		let hidden: ReturnType<typeof vi.spyOn>;

		beforeEach(() => {
			vi.useFakeTimers();
			hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(false);
			load([aPhase()]);
		});
		afterEach(() => {
			vi.useRealTimers();
		});

		it('reads the run every 30 seconds', async () => {
			render(PhasesCard, { runId: 'r1', idle: true });
			await vi.advanceTimersByTimeAsync(0);
			expect(api.runs.phases).toHaveBeenCalledTimes(1);

			await vi.advanceTimersByTimeAsync(29_999);
			expect(api.runs.phases).toHaveBeenCalledTimes(1);

			await vi.advanceTimersByTimeAsync(1);
			expect(api.runs.phases).toHaveBeenCalledTimes(2);
		});

		it('sits out while the tab is hidden', async () => {
			render(PhasesCard, { runId: 'r1', idle: true });
			await vi.advanceTimersByTimeAsync(0);

			hidden.mockReturnValue(true);
			await vi.advanceTimersByTimeAsync(60_000);

			expect(api.runs.phases).toHaveBeenCalledTimes(1);
		});

		it('stops when it leaves the page', async () => {
			const { unmount } = render(PhasesCard, { runId: 'r1', idle: true });
			await vi.advanceTimersByTimeAsync(0);

			unmount();
			await vi.advanceTimersByTimeAsync(60_000);

			expect(api.runs.phases).toHaveBeenCalledTimes(1);
		});
	});
});
