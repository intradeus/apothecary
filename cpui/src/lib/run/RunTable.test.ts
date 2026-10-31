import { render, screen, within } from '@testing-library/svelte';
import { tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import type { Run, RunEvent, RunMergeRequest } from '$lib/domain';
import { aProject, aRun } from '$test/fixtures';
import RunTable, { type RunCol } from './RunTable.svelte';

vi.mock('$lib/api', () => ({ api: { runs: { get: vi.fn() } } }));

const NOW = new Date('2026-10-31T12:00:00Z');
const anMr = (iid: number) =>
	({ repo: 'repo', url: `https://git.test/${iid}`, iid, branch: 'b' }) as RunMergeRequest;

function row(container: HTMLElement, i: number) {
	return container.querySelectorAll('.rt > .r:not(.head)')[i] as HTMLElement;
}
const headers = (container: HTMLElement) =>
	[...container.querySelectorAll('.r.head > span')].map((s) => s.textContent);

describe('RunTable', () => {
	beforeEach(() => {
		vi.useFakeTimers({ toFake: ['Date'] });
		vi.setSystemTime(NOW);
		vi.resetAllMocks();
		fleet.projects = [];
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	it('shows only the header with no runs', () => {
		const { container } = render(RunTable, { runs: [] });

		expect(headers(container)).toEqual([
			'Run',
			'State',
			'',
			'Stage',
			'Workshop',
			'Flow',
			'MR',
			'Updated'
		]);
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
	});

	it('shows the columns it is asked for, in that order', () => {
		const cols: RunCol[] = ['state', 'id', 'branch'];
		const { container } = render(RunTable, { runs: [], cols });
		expect(headers(container)).toEqual(['State', 'Id', 'Branch']);
	});

	it('keeps the runs in the order given', () => {
		const runs = [
			aRun({ id: 'a', title: 'First', updatedAt: '2026-10-30T12:00:00Z' }),
			aRun({ id: 'b', title: 'Second', updatedAt: '2026-10-31T11:00:00Z' })
		];
		render(RunTable, { runs });

		expect(screen.getAllByRole('link').map((l) => l.textContent)).toEqual(['First', 'Second']);
	});

	describe('the title', () => {
		it('opens the run, with its short id beside it', () => {
			const { container } = render(RunTable, { runs: [aRun({ id: '01ABCD' })] });
			const cells = within(row(container, 0));

			expect(cells.getByRole('link', { name: 'Add idempotency keys' })).toHaveAttribute(
				'href',
				'/runs/01ABCD'
			);
			expect(cells.getByText('r_abcd')).toBeInTheDocument();
		});

		it('names an untitled run', () => {
			render(RunTable, { runs: [aRun({ title: '' })] });
			expect(screen.getByRole('link', { name: 'Untitled run' })).toBeInTheDocument();
		});

		it('adds the branch in detail', () => {
			render(RunTable, { runs: [aRun({ branch: 'feat/a' })], detail: true });
			expect(screen.getByText('r_abcd · feat/a')).toBeInTheDocument();
		});

		it('leaves the branch out otherwise', () => {
			render(RunTable, { runs: [aRun({ branch: 'feat/a' })] });
			expect(screen.getByText('r_abcd')).toBeInTheDocument();
		});
	});

	describe('the id', () => {
		it('is plain beside a title, shown once', () => {
			render(RunTable, { runs: [aRun()], cols: ['title', 'id'] });

			expect(screen.getAllByText('r_abcd')).toHaveLength(1);
			expect(screen.queryByRole('link', { name: 'r_abcd' })).not.toBeInTheDocument();
		});

		it('is the link when there is no title column', () => {
			render(RunTable, { runs: [aRun({ id: '01ABCD' })], cols: ['id', 'state'] });

			const link = screen.getByRole('link', { name: 'r_abcd' });
			expect(link).toHaveAttribute('href', '/runs/01ABCD');
			expect(link).toHaveAttribute('title', '01ABCD');
		});

		// Without a title column it is the row's only link, so phones keep it.
		it('folds away on phones only beside a title', () => {
			const { container, unmount } = render(RunTable, {
				runs: [aRun()],
				cols: ['title', 'id']
			});
			expect(row(container, 0).querySelector('.mono.muted')).toHaveClass('hide-sm');
			expect(container.querySelectorAll('.r.head > span')[1]).toHaveClass('hide-sm');
			unmount();

			const alone = render(RunTable, { runs: [aRun()], cols: ['id', 'state'] });
			expect(alone.container.querySelectorAll('.r.head > span')[0]).not.toHaveClass('hide-sm');
		});
	});

	it('shows the state', () => {
		render(RunTable, { runs: [aRun({ state: 'failed' })] });
		expect(screen.getByText('Failed')).toBeInTheDocument();
	});

	it('shows the workspace status beside it', () => {
		const { container } = render(RunTable, { runs: [aRun({ envId: 'env-1' })], cols: ['ws'] });
		expect(row(container, 0).querySelector('[title]')).toHaveAttribute(
			'title',
			expect.stringContaining('env-1')
		);
	});

	describe('the stage', () => {
		it('shows it with its flask', () => {
			render(RunTable, { runs: [aRun({ currentStage: 'build' })], cols: ['stage'] });

			expect(screen.getByText('build')).toBeInTheDocument();
			expect(screen.getByTitle('running')).toBeInTheDocument();
		});

		it('takes the flask from the state', () => {
			render(RunTable, {
				runs: [aRun({ state: 'waiting_on_human', currentStage: 'build' })],
				cols: ['stage']
			});
			expect(screen.getByTitle('waiting')).toBeInTheDocument();
		});

		it('dashes a run with no stage', () => {
			const { container } = render(RunTable, { runs: [aRun()], cols: ['stage'] });
			expect(row(container, 0)).toHaveTextContent('—');
		});

		it('fetches nothing unless asked to', async () => {
			// A run of its own: facts are cached per run across this file.
			render(RunTable, {
				runs: [aRun({ id: 'no-facts', currentStage: 'build' })],
				cols: ['stage']
			});
			await tick();
			expect(api.runs.get).not.toHaveBeenCalled();
		});

		it('adds the attempt when facts are on', async () => {
			const run: Run = aRun({ id: 'with-facts', currentStage: 'build' });
			const started = {
				type: 'stage_started',
				ts: '2026-10-31T11:00:00Z',
				payload: { stage_id: 'build', iteration: 3 }
			} as unknown as RunEvent;
			vi.mocked(api.runs.get).mockResolvedValue({ run, events: [started] } as never);

			render(RunTable, { runs: [run], cols: ['stage'], facts: true });

			const attempt = await screen.findByText('#3');
			expect(attempt).toHaveAttribute('title', 'attempt 3');
			expect(api.runs.get).toHaveBeenCalledExactlyOnceWith('with-facts');
		});
	});

	describe('the workshop', () => {
		it('names it', () => {
			fleet.projects = [aProject({ id: 'p1', name: 'ledger' })];
			render(RunTable, { runs: [aRun({ projectId: 'p1' })], cols: ['workshop'] });
			expect(screen.getByText('ledger')).toBeInTheDocument();
		});

		it('dashes a run without one', () => {
			const { container } = render(RunTable, { runs: [aRun()], cols: ['workshop'] });
			expect(row(container, 0)).toHaveTextContent('—');
		});
	});

	describe('the flow', () => {
		it('names it, without the hash', () => {
			const { container } = render(RunTable, { runs: [aRun()], cols: ['flow'] });
			expect(row(container, 0).textContent?.trim()).toBe('builder');
		});

		it('pins the hash in detail', () => {
			const { container } = render(RunTable, { runs: [aRun()], cols: ['flow'], detail: true });
			expect(row(container, 0)).toHaveTextContent('builder@e7c90c');
		});

		it('has nothing to pin without a hash', () => {
			const { container } = render(RunTable, {
				runs: [aRun({ flowHash: '' })],
				cols: ['flow'],
				detail: true
			});
			expect(row(container, 0).textContent?.trim()).toBe('builder');
		});
	});

	it('shows the branch', () => {
		const { container } = render(RunTable, {
			runs: [aRun({ branch: 'feat/a' })],
			cols: ['branch']
		});
		expect(within(row(container, 0)).getByText('feat/a')).toHaveAttribute('title', 'feat/a');
	});

	describe('the merge request', () => {
		it('links it', () => {
			render(RunTable, { runs: [aRun({ mrs: [anMr(20)] })], cols: ['mr'] });
			expect(screen.getByRole('link', { name: /^MR/ })).toHaveAttribute(
				'href',
				'https://git.test/20'
			);
		});

		it('lists each one in detail', () => {
			render(RunTable, {
				runs: [aRun({ mrs: [anMr(20), anMr(21)] })],
				cols: ['mr'],
				detail: true
			});

			expect(screen.getByRole('link', { name: /!20/ })).toBeInTheDocument();
			expect(screen.getByRole('link', { name: /!21/ })).toBeInTheDocument();
		});
	});

	it('says how long ago it moved', () => {
		render(RunTable, {
			runs: [aRun({ updatedAt: '2026-10-31T11:30:00Z' })],
			cols: ['updated']
		});
		expect(screen.getByText('30m ago')).toBeInTheDocument();
	});
});
