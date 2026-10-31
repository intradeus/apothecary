import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { goto } from '$app/navigation';
import { api } from '$lib/api';
import type { Flow, MergeRequestRow } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { aFlow, aWorkshop } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import MergeRequestsTab from './MergeRequestsTab.svelte';

vi.mock('$lib/api', () => ({
	api: { projects: { mergeRequests: vi.fn(), startRunOnMR: vi.fn() } }
}));

const project = aWorkshop({ id: 'p1' });
const reviewer = aFlow({ name: 'reviewer', operatesOn: 'merge_request' });
const row = (over: Partial<MergeRequestRow['mr']> = {}, rest: Partial<MergeRequestRow> = {}) =>
	({
		mr: {
			iid: 12,
			title: 'Add keys',
			url: 'https://git.test/mr/12',
			author: 'lu',
			source: 'feat/keys',
			target: 'main',
			draft: false,
			fork: false,
			...over
		},
		mine: false,
		runs: 0,
		...rest
	}) as MergeRequestRow;

function setup(rows: MergeRequestRow[] = [row()], flows: Flow[] | null = [reviewer]) {
	vi.mocked(api.projects.mergeRequests).mockResolvedValue(rows);
	return render(MergeRequestsTab, { project, flows });
}
const start = () => screen.findByRole('button', { name: /Start a run/ });

beforeEach(() => {
	vi.resetAllMocks();
	toasts.list = [];
});
afterEach(() => {
	vi.useRealTimers();
});

describe('MergeRequestsTab, listing', () => {
	it('shows a skeleton first', () => {
		vi.mocked(api.projects.mergeRequests).mockReturnValue(new Promise(() => {}));
		render(MergeRequestsTab, { project, flows: [reviewer] });
		expect(screen.queryByText('No open merge requests')).not.toBeInTheDocument();
	});

	it('says when none are open', async () => {
		setup([]);
		expect(await screen.findByText('No open merge requests')).toBeInTheDocument();
	});

	it('offers a retry when listing fails', async () => {
		vi.mocked(api.projects.mergeRequests).mockRejectedValueOnce(new Error('down'));
		vi.mocked(api.projects.mergeRequests).mockResolvedValueOnce([row()]);
		render(MergeRequestsTab, { project, flows: [reviewer] });

		expect(await screen.findByText('Could not list the merge requests')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByRole('link', { name: /Add keys/ })).toBeInTheDocument();
	});

	it('links the merge request and shows its branches', async () => {
		setup();
		expect(await screen.findByRole('link', { name: '!12 Add keys' })).toHaveAttribute(
			'href',
			'https://git.test/mr/12'
		);
		expect(screen.getByText('feat/keys → main')).toBeInTheDocument();
		expect(screen.getByText('no run yet')).toBeInTheDocument();
	});

	it('marks a draft, a fork, and one the factory opened', async () => {
		setup([row({ draft: true, fork: true }, { mine: true })]);
		expect(await screen.findByText('draft')).toBeInTheDocument();
		expect(screen.getByText('from a fork')).toBeInTheDocument();
		expect(screen.getByText('opened by the factory')).toBeInTheDocument();
	});

	it('says what it is stacked on, linking it', async () => {
		setup([
			row(
				{},
				{ stackedOn: { iid: 9, url: 'https://git.test/mr/9', title: 'Base', branch: 'feat/base' } }
			)
		]);
		expect(await screen.findByRole('link', { name: 'stacked on !9' })).toHaveAttribute(
			'href',
			'https://git.test/mr/9'
		);
	});

	it('shows its last run, and all of them when there are several', async () => {
		setup([
			row(
				{},
				{
					run: { id: 'r1', state: 'running', flow: 'reviewer', createdAt: '2026-10-31T12:00:00Z' },
					runs: 3,
					ticketId: 't1'
				}
			)
		]);
		expect(await screen.findByRole('link', { name: 'all 3 runs' })).toHaveAttribute(
			'href',
			'/tickets/t1'
		);
		expect(screen.getByText(/reviewer/)).toBeInTheDocument();
	});

	it('reads again every thirty seconds, unless the tab is hidden', async () => {
		vi.useFakeTimers();
		vi.mocked(api.projects.mergeRequests).mockResolvedValue([]);
		render(MergeRequestsTab, { project, flows: [reviewer] });
		await vi.advanceTimersByTimeAsync(0);
		expect(api.projects.mergeRequests).toHaveBeenCalledTimes(1);

		await vi.advanceTimersByTimeAsync(30_000);
		expect(api.projects.mergeRequests).toHaveBeenCalledTimes(2);

		vi.spyOn(document, 'hidden', 'get').mockReturnValue(true);
		await vi.advanceTimersByTimeAsync(30_000);
		expect(api.projects.mergeRequests).toHaveBeenCalledTimes(2);
	});

	it('stops reading once it leaves the page', async () => {
		vi.useFakeTimers();
		vi.mocked(api.projects.mergeRequests).mockResolvedValue([]);
		const { unmount } = render(MergeRequestsTab, { project, flows: [reviewer] });
		await vi.advanceTimersByTimeAsync(0);
		unmount();
		await vi.advanceTimersByTimeAsync(60_000);
		expect(api.projects.mergeRequests).toHaveBeenCalledTimes(1);
	});
});

describe('MergeRequestsTab, starting a run', () => {
	const pick = async (name = 'reviewer') => {
		await userEvent.click(await start());
		await userEvent.click(await screen.findByRole('button', { name }));
	};

	it('offers only the flows that start on a merge request', async () => {
		setup([row()], [reviewer, aFlow({ name: 'builder', operatesOn: 'ticket' })]);
		await userEvent.click(await start());
		expect(await screen.findByText('Start on !12 with')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'reviewer' })).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'builder' })).not.toBeInTheDocument();
	});

	it('says so when no flow can, and disables the button', async () => {
		setup([row()], [aFlow({ name: 'builder', operatesOn: 'ticket' })]);
		expect(await start()).toBeDisabled();
		expect(screen.getByText(/No flow of this workshop operates/)).toBeInTheDocument();
	});

	it('says nothing about flows until they have loaded', async () => {
		setup([row()], null);
		expect(await start()).toBeDisabled();
		expect(screen.queryByText(/No flow of this workshop operates/)).not.toBeInTheDocument();
	});

	it('warns where a stacked one will land', async () => {
		setup([
			row({}, { stackedOn: { iid: 9, url: 'https://x', title: 'Base', branch: 'feat/base' } })
		]);
		await userEvent.click(await start());
		expect(await screen.findByText(/feat\/base/)).toBeInTheDocument();
	});

	it('starts, offers the run, and reads the list again', async () => {
		vi.mocked(api.projects.startRunOnMR).mockResolvedValue({ runId: 'r9' } as never);
		setup();
		await pick();

		expect(api.projects.startRunOnMR).toHaveBeenCalledWith('p1', 12, { flow: 'reviewer' });
		await waitFor(() => expect(api.projects.mergeRequests).toHaveBeenCalledTimes(2));
		const told = toasts.list.at(-1)!;
		expect(told.text).toBe('reviewer started on !12');

		told.action!.run();
		expect(goto).toHaveBeenCalledWith('/runs/r9');
	});

	it('holds every start while one is on its way', async () => {
		const run = deferred<never>();
		vi.mocked(api.projects.startRunOnMR).mockReturnValue(run.promise);
		setup([row(), row({ iid: 13 })]);
		await screen.findAllByRole('button', { name: /Start a run/ });
		await userEvent.click(screen.getAllByRole('button', { name: /Start a run/ })[0]);
		await userEvent.click(await screen.findByRole('button', { name: 'reviewer' }));

		const buttons = screen.getAllByRole('button', { name: /Start a run/ });
		expect(buttons[0]).toBeDisabled();
		expect(buttons[0]).toHaveAttribute('aria-busy', 'true');
		expect(buttons[1]).toBeDisabled();
		expect(buttons[1]).toHaveAttribute('aria-busy', 'false');

		run.resolve({ runId: 'r9' } as never);
		await waitFor(() =>
			expect(screen.getAllByRole('button', { name: /Start a run/ })[1]).toBeEnabled()
		);
	});

	it('shows a refusal as a toast and frees the buttons', async () => {
		vi.mocked(api.projects.startRunOnMR).mockRejectedValue(new Error('no capacity'));
		setup();
		await pick();

		await waitFor(() => expect(toasts.list.at(-1)?.text).toBe('no capacity'));
		await waitFor(async () => expect(await start()).toBeEnabled());
	});

	// t() groups numbers, and an iid is an identifier.
	it('names a merge request as the repository does', async () => {
		setup([row({ iid: 1234 })]);
		await userEvent.click(await start());
		expect(await screen.findByText('Start on !1234 with')).toBeInTheDocument();
	});

	it('names it in the toast', async () => {
		vi.mocked(api.projects.startRunOnMR).mockResolvedValue({ runId: 'r9' } as never);
		setup([row({ iid: 1234 })]);
		await pick();

		await waitFor(() => expect(toasts.list.at(-1)?.text).toBe('reviewer started on !1234'));
	});

	it('names the one it is stacked on', async () => {
		const stackedOn = { iid: 1234, url: 'https://x', title: 'Base', branch: 'feat/base' };
		setup([row({ iid: 5678 }, { stackedOn })]);

		const pill = await screen.findByRole('link', { name: 'stacked on !1234' });
		expect(pill.getAttribute('title')).toContain('Stacked on !1234 (Base)');
		expect(pill.getAttribute('title')).toContain('Merge !1234 first.');

		await userEvent.click(await start());
		expect(await screen.findByText(/^!5678 is stacked on !1234:/)).toBeInTheDocument();
	});
});
